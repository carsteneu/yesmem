package storage

import (
	"fmt"
	"time"
)

// OrphanResumeDecision is what startup should do with one orphaned row.
type OrphanResumeDecision int

const (
	// OrphanResumeAttempt: the row needs a resume_agent call.
	OrphanResumeAttempt OrphanResumeDecision = iota
	// OrphanResumeClearMarker: the backend session already worked after the stop,
	// so the work continued outside the row. Resuming would restart finished work.
	OrphanResumeClearMarker
	// OrphanResumeGiveUp: the attempt budget is spent. Abandon loudly.
	OrphanResumeGiveUp
)

func (d OrphanResumeDecision) String() string {
	switch d {
	case OrphanResumeAttempt:
		return "attempt"
	case OrphanResumeClearMarker:
		return "clear-marker"
	case OrphanResumeGiveUp:
		return "give-up"
	}
	return "unknown"
}

// parseAgentTime accepts the two shapes this database carries: RFC3339 as Go
// writes it ("2026-11-26T09:05:03Z", UTC) and SQLite's CURRENT_TIMESTAMP
// ("2026-11-26 09:05:03", also UTC but without the marker). Anything else
// returns the zero time so the caller can treat it as unknown.
func parseAgentTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// defaultOrphanResumeBudget applies when a row carries no max_restarts. 753 of
// 770 agent rows on this host have 0 there, so treating 0 as "no limit" would
// leave the loop unguarded for almost every row — the opposite of what the
// budget is for. A missing budget means "use the default", never "retry
// forever".
const defaultOrphanResumeBudget = 3

// OrphanResumeDecision decides what startup does with one marked row. It reads
// only, so the caller can log the decision before acting on it.
func (s *Store) OrphanResumeDecision(id string) (OrphanResumeDecision, error) {
	var restartCount, maxRestarts int
	var stoppedAt, lastActivity string
	err := s.db.QueryRow(`SELECT
			COALESCE(restart_count, 0), COALESCE(max_restarts, 0),
			COALESCE(stopped_at, ''), COALESCE(last_activity_at, '')
		FROM agents WHERE id = ?`, id,
	).Scan(&restartCount, &maxRestarts, &stoppedAt, &lastActivity)
	if err != nil {
		return OrphanResumeAttempt, err
	}

	budget := maxRestarts
	if budget <= 0 {
		budget = defaultOrphanResumeBudget
	}
	if restartCount >= budget {
		return OrphanResumeGiveUp, nil
	}

	stopped := parseAgentTime(stoppedAt)
	activity := parseAgentTime(lastActivity)
	if !stopped.IsZero() && !activity.IsZero() && activity.After(stopped) {
		return OrphanResumeClearMarker, nil
	}

	return OrphanResumeAttempt, nil
}

// RecordOrphanResumeAttempt counts the ATTEMPT, not the success. A resume that
// dies within seconds must move the budget, otherwise the loop never ends.
func (s *Store) RecordOrphanResumeAttempt(id string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET restart_count = COALESCE(restart_count, 0) + 1 WHERE id = ?`, id)
	return err
}

// ClearOrphanMarker drops the retention marker after the work was resumed or
// proven to have continued. The row itself stays: status and transcript are what
// relay/stop/whoami keep working on.
func (s *Store) ClearOrphanMarker(id, reason string) error {
	note := reason
	if len(note) > 200 {
		note = note[:200]
	}
	res, err := s.db.Exec(`UPDATE agents
		SET orphan_reattachable = 0, progress = ?
		WHERE id = ? AND COALESCE(orphan_reattachable, 0) = 1`,
		"stopped: "+note, id)
	if err != nil {
		return err
	}
	if n, nerr := res.RowsAffected(); nerr == nil && n == 0 {
		return fmt.Errorf("agent %q had no orphan marker to clear", id)
	}
	return nil
}
