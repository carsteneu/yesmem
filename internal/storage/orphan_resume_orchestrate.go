package storage

import (
	"fmt"
	"log"
	"strings"
)

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
) (attempted, cleared, gaveUp, superseded int) {
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

		case OrphanResumeSuperseded:
			holder, _ := s.SectionHolderFor(o.ID)
			var reason string
			if strings.HasPrefix(o.Section, "sched-") {
				reason = fmt.Sprintf("superseded by schedule (section %q gets a fresh agent)", o.Section)
			} else if holder != "" {
				reason = fmt.Sprintf("superseded by %s, which holds the section", holder)
			} else {
				reason = "superseded"
			}
			if err := s.ClearOrphanMarker(o.ID, reason); err != nil {
				logf("[agent-recovery] cannot close superseded %s: %v", o.ID, err)
				continue
			}
			logf("[agent-recovery] %s: %s — marker cleared, no resume attempt spent", o.ID, reason)
			superseded++

		case OrphanResumeAttempt:
			resumeErr := resume(o)
			if resumeErr != nil {
				// Only the FAILURE moves the budget: a resume that returns is not
				// a failed attempt, and counting it was what abandoned agents that
				// were running the whole time.
				if err := s.RecordOrphanResumeAttempt(o.ID); err != nil {
					logf("[agent-recovery] cannot record attempt for %s: %v", o.ID, err)
				}
				logf("[agent-recovery] %s: resume failed: %v", o.ID, resumeErr)
				continue
			}
			// Deliberately no counter reset: restart_count keeps counting failed
			// resumes only, so the number stays meaningful ("how often did it
			// fail") instead of describing a lifetime.
			logf("[agent-recovery] %s: resumed", o.ID)
			attempted++
		}
	}
	return attempted, cleared, gaveUp, superseded
}
