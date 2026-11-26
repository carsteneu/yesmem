package storage

import (
	"database/sql"
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
	// OrphanResumeSuperseded: another live agent owns this project+section, so
	// this row can never become the active one again — it was replaced, not
	// blocked. Resuming is structurally impossible, and spending an attempt on it
	// would burn the budget on a call that cannot succeed. Closing the marker with
	// the successor named is the honest answer, and it makes the orphan count
	// actually go down instead of parking the row forever.
	OrphanResumeSuperseded
)

func (d OrphanResumeDecision) String() string {
	switch d {
	case OrphanResumeAttempt:
		return "attempt"
	case OrphanResumeClearMarker:
		return "clear-marker"
	case OrphanResumeGiveUp:
		return "give-up"
	case OrphanResumeSuperseded:
		return "superseded"
	}
	return "unknown"
}

// The two timestamp columns of agents are written by different code paths and do
// NOT share a format or a zone. Measured 2026-11-26:
//
//	stopped_at       2026-11-26T07:01:12+01:00   RFC3339, carries +01:00
//	last_activity_at 2026-11-26 07:04:53         space separator, NO zone, local
//
// Parsing both as UTC shifts last_activity_at by the local offset and flips the
// comparison: agent-09 (activity 09:02 local, stop 09:05 local = 08:05 UTC) looks
// like "worked after the stop" when it did not. String comparison is worse still:
// ' ' (0x20) sorts before 'T' (0x54), so stopped_at always loses.
var (
	timeZoneBoundLayouts = []string{time.RFC3339Nano, time.RFC3339}
	timeLocalLayouts     = []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"}
)

// parseZoneBoundTime parses a timestamp that states its own offset.
func parseZoneBoundTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timeZoneBoundLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseLocalTime parses a timestamp with no zone, interpreting it in the
// machine's local zone — which is how last_activity_at is written.
func parseLocalTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range timeLocalLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseAnyAgentTime is the lenient fallback: either shape, each in its own zone.
func parseAnyAgentTime(s string) time.Time {
	if t := parseZoneBoundTime(s); !t.IsZero() {
		return t
	}
	return parseLocalTime(s)
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

	// Section-Frage vor dem Resume: handleResumeAgent refuses a resume while
	// another live agent owns the same project+section. Such a row is superseded —
	// it can never take the section back while the successor lives.
	if holder, herr := s.SectionHolderFor(id); herr == nil && holder != "" {
		return OrphanResumeSuperseded, nil
	}

	// stopped_at states its zone (+01:00); last_activity_at does not and is
	// written in local time. Each is read with the shape it actually has.
	stopped := parseZoneBoundTime(stoppedAt)
	if stopped.IsZero() {
		stopped = parseAnyAgentTime(stoppedAt)
	}
	activity := parseLocalTime(lastActivity)
	if activity.IsZero() {
		activity = parseAnyAgentTime(lastActivity)
	}
	if !stopped.IsZero() && !activity.IsZero() && activity.After(stopped) {
		return OrphanResumeClearMarker, nil
	}

	return OrphanResumeAttempt, nil
}

// SectionHolderFor returns the ID of another live agent that owns the same
// project+section as the given row, or "" when the section is free. The live
// statuses mirror what handleResumeAgent treats as "owns its section".
func (s *Store) SectionHolderFor(id string) (string, error) {
	var project, section string
	if err := s.db.QueryRow(
		`SELECT COALESCE(project,''), COALESCE(section,'') FROM agents WHERE id = ?`, id,
	).Scan(&project, &section); err != nil {
		return "", err
	}
	if section == "" {
		return "", nil
	}
	var holder string
	err := s.db.QueryRow(`SELECT id FROM agents
		WHERE project = ? AND section = ? AND id != ?
		  AND status IN ('running', 'pending', 'spawning', 'paused')
		LIMIT 1`, project, section, id).Scan(&holder)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return holder, nil
}

// RecordOrphanResumeAttempt counts a FAILED attempt. A resume that dies within
// seconds must move the budget, otherwise the loop never ends.
func (s *Store) RecordOrphanResumeAttempt(id string) error {
	_, err := s.db.Exec(
		`UPDATE agents SET restart_count = COALESCE(restart_count, 0) + 1 WHERE id = ?`, id)
	return err
}

// ClearOrphanResumeBudget resets the counter after a resume that demonstrably
// worked. Counting every resume — including the successful ones — punishes the
// agents that run best: one that is always up when the daemon restarts gets
// orphaned and resumed on every start, and would be abandoned after three
// restarts while functioning the whole time. The budget guards a retry LOOP, not
// a healthy agent's lifetime.
func (s *Store) ClearOrphanResumeBudget(id string) error {
	_, err := s.db.Exec(`UPDATE agents SET restart_count = 0 WHERE id = ?`, id)
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
