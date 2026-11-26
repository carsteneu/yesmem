package storage

import "log"

// AutoResumeOrphans is the step AFTER AgentRecoverOrphaned. The sweep keeps rows
// whose backend session still exists on disk and marks them reattachable
// (YM-297); nothing resumes them, so the work never continues on its own.
//
// Per marked row it decides:
//
//   - the backend session produced work after the stop -> the work continued
//     outside the row: clear the marker, do not resume (resuming would restart
//     finished work)
//   - the attempt budget is spent -> abandon loudly, never loop
//   - otherwise -> call resume, and count the ATTEMPT against the budget
//
// The budget is a guard against a resume loop, not a usage limit: an agent that
// dies immediately after every resume would otherwise be restarted on every
// daemon start, forever, silently.
//
// resume is injected so this stays testable without a live daemon.
func (s *Store) AutoResumeOrphans(
	orphans []OrphanedAgent,
	resume func(OrphanedAgent) error,
	logf func(format string, args ...any),
) (attempted, cleared, gaveUp, blocked int) {
	if logf == nil {
		logf = log.Printf
	}
	for _, o := range orphans {
		decision, err := s.OrphanResumeDecision(o.ID)
		if err != nil {
			logf("[agent-recovery] cannot decide for %s: %v", o.ID, err)
			continue
		}

		switch decision {
		case OrphanResumeClearMarker:
			if err := s.ClearOrphanMarker(o.ID, "backend session produced work after the stop"); err != nil {
				logf("[agent-recovery] cannot clear marker on %s: %v", o.ID, err)
				continue
			}
			logf("[agent-recovery] %s: work continued after the stop — marker cleared, no resume", o.ID)
			cleared++

		case OrphanResumeGiveUp:
			logf("[agent-recovery] GIVING UP on %s: resume budget spent (restart_count/max_restarts). "+
				"Clear it by hand once the backend session is unusable: "+
				"UPDATE agents SET orphan_reattachable=0 WHERE id='%s'", o.ID, o.ID)
			gaveUp++

		case OrphanResumeSkipSection:
			holder, _ := s.SectionHolderFor(o.ID)
			logf("[agent-recovery] %s: skipped — section %q is held by live agent %s; "+
				"no budget spent, retried on the next start", o.ID, o.Section, holder)
			blocked++

		case OrphanResumeAttempt:
			resumeErr := resume(o)
			// The attempt moves the budget whether it succeeded or not.
			if err := s.RecordOrphanResumeAttempt(o.ID); err != nil {
				logf("[agent-recovery] cannot record attempt for %s: %v", o.ID, err)
			}
			if resumeErr != nil {
				logf("[agent-recovery] %s: resume failed: %v", o.ID, resumeErr)
				continue
			}
			logf("[agent-recovery] %s: resumed", o.ID)
			attempted++
		}
	}
	return attempted, cleared, gaveUp, blocked
}
