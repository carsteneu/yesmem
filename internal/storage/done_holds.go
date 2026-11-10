package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The persisted half of the DONE gate's hold.
//
// The gate's per-agent state is re-derived from the report on the next tick, so
// a restart loses the state and nothing else. What it does lose is TIME: when
// the hold began, and whether the watchdog already escalated it. Without those
// two facts a restart silently restarts the clock, and a DONE that nobody
// decided can wait forever.
type DONEHold struct {
	AgentID string `json:"agent_id"`
	// Fingerprint is the report-and-tree key the hold is about. A different one
	// is a corrected report: the attempt starts over, clock included.
	Fingerprint string `json:"fingerprint"`
	// Status is the decision the hold stands for (pending, blocked). A pending
	// hold becomes a blocked one without the report changing, and that
	// transition is the moment the agent gets its correction round: the clock
	// starts there, not when the pre-check was already running.
	Status string `json:"status"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	// HeldSince is when the current decision began waiting.
	HeldSince string `json:"held_since"` // RFC3339
	// WatchdogAt is when the daemon escalated the undecided hold to the
	// orchestrator. Empty means it has not.
	WatchdogAt string `json:"watchdog_at,omitempty"`
}

const tableDONEHolds = `CREATE TABLE IF NOT EXISTS done_verify_holds (
	agent_id    TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL,
	status      TEXT DEFAULT '',
	kind        TEXT DEFAULT '',
	reason      TEXT DEFAULT '',
	held_since  TEXT NOT NULL,
	watchdog_at TEXT DEFAULT '',
	updated_at  TEXT NOT NULL
)`

// SaveDONEHold records the hold for one agent. The clock belongs to the
// decision, not to the write: a later tick about the same report in the same
// status keeps held_since, and a correction (new fingerprint) or a new decision
// (pending to blocked) starts a new clock and clears the watchdog mark with it.
func (s *Store) SaveDONEHold(hold DONEHold) error {
	if hold.AgentID == "" || hold.Fingerprint == "" || hold.HeldSince == "" {
		return errors.New("done hold needs an agent id, a fingerprint and a start time")
	}
	_, err := s.db.Exec(`INSERT INTO done_verify_holds (agent_id, fingerprint, status, kind, reason, held_since, watchdog_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			kind = excluded.kind,
			reason = excluded.reason,
			updated_at = excluded.updated_at,
			held_since = CASE WHEN done_verify_holds.fingerprint <> excluded.fingerprint
					OR done_verify_holds.status <> excluded.status
				THEN excluded.held_since ELSE done_verify_holds.held_since END,
			watchdog_at = CASE WHEN done_verify_holds.fingerprint <> excluded.fingerprint
					OR done_verify_holds.status <> excluded.status
				THEN '' ELSE done_verify_holds.watchdog_at END,
			fingerprint = excluded.fingerprint,
			status = excluded.status`,
		hold.AgentID, hold.Fingerprint, hold.Status, hold.Kind, hold.Reason, hold.HeldSince,
		time.Now().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save done hold: %w", err)
	}
	return nil
}

// DONEHold returns the hold of one agent, or found=false.
func (s *Store) DONEHold(agentID string) (DONEHold, bool, error) {
	var hold DONEHold
	err := s.db.QueryRow(`SELECT agent_id, fingerprint, status, kind, reason, held_since, watchdog_at
		FROM done_verify_holds WHERE agent_id = ?`, agentID).
		Scan(&hold.AgentID, &hold.Fingerprint, &hold.Status, &hold.Kind, &hold.Reason, &hold.HeldSince, &hold.WatchdogAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DONEHold{}, false, nil
	}
	if err != nil {
		return DONEHold{}, false, fmt.Errorf("read done hold: %w", err)
	}
	return hold, true, nil
}

// DONEHolds returns every recorded hold, oldest first. The watchdog uses it as
// its work list: a hold that is not in this list is not held.
func (s *Store) DONEHolds() ([]DONEHold, error) {
	rows, err := s.db.Query(`SELECT agent_id, fingerprint, status, kind, reason, held_since, watchdog_at
		FROM done_verify_holds ORDER BY held_since`)
	if err != nil {
		return nil, fmt.Errorf("list done holds: %w", err)
	}
	defer rows.Close()
	var holds []DONEHold
	for rows.Next() {
		var hold DONEHold
		if err := rows.Scan(&hold.AgentID, &hold.Fingerprint, &hold.Status, &hold.Kind, &hold.Reason, &hold.HeldSince, &hold.WatchdogAt); err != nil {
			return nil, fmt.Errorf("scan done hold: %w", err)
		}
		holds = append(holds, hold)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read done holds: %w", err)
	}
	return holds, nil
}

// MarkDONEHoldWatchdog records that the orchestrator was told about this hold,
// and reports whether this call is the one that recorded it. The condition in
// the WHERE is the lock: two daemons on one database both read an unmarked row,
// and only the update that still finds it unmarked wins.
func (s *Store) MarkDONEHoldWatchdog(agentID, at string) (bool, error) {
	if agentID == "" || at == "" {
		return false, errors.New("watchdog mark needs an agent id and a time")
	}
	result, err := s.db.Exec(`UPDATE done_verify_holds SET watchdog_at = ?, updated_at = ?
		WHERE agent_id = ? AND watchdog_at = ''`,
		at, time.Now().Format(time.RFC3339), agentID)
	if err != nil {
		return false, fmt.Errorf("mark done hold watchdog: %w", err)
	}
	marked, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count the watchdog mark: %w", err)
	}
	return marked == 1, nil
}

// DeleteDONEHold drops the hold of one agent: the report was released, the row
// is gone, or the agent is no longer held.
func (s *Store) DeleteDONEHold(agentID string) error {
	if agentID == "" {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM done_verify_holds WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("delete done hold: %w", err)
	}
	return nil
}

// ClearDONEHoldWatchdog withdraws the mark of an escalation that never left the
// daemon. The next tick owes the message again; without this, a failed delivery
// would be silence with a receipt.
func (s *Store) ClearDONEHoldWatchdog(agentID string) error {
	if agentID == "" {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE done_verify_holds SET watchdog_at = '', updated_at = ? WHERE agent_id = ?`,
		time.Now().Format(time.RFC3339), agentID); err != nil {
		return fmt.Errorf("clear done hold watchdog: %w", err)
	}
	return nil
}
