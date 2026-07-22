package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// OpencodeScanState is the durable cursor for one OpenCode database.
// Session ID disambiguates sessions that share the same update timestamp.
type OpencodeScanState struct {
	LastUpdatedMs int64
	LastSessionID string
}

// GetOpencodeScanState returns the durable scanner cursor for sourcePath.
func (s *Store) GetOpencodeScanState(sourcePath string) (OpencodeScanState, bool, error) {
	var state OpencodeScanState
	err := s.db.QueryRow(`SELECT last_updated_ms, last_session_id
		FROM opencode_scan_state WHERE source_path = ?`, sourcePath).
		Scan(&state.LastUpdatedMs, &state.LastSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return OpencodeScanState{}, false, nil
	}
	if err != nil {
		return OpencodeScanState{}, false, fmt.Errorf("get opencode scan state: %w", err)
	}
	return state, true, nil
}

// SetOpencodeScanState persists the scanner cursor for sourcePath.
func (s *Store) SetOpencodeScanState(sourcePath string, state OpencodeScanState) error {
	_, err := s.db.Exec(`INSERT INTO opencode_scan_state
		(source_path, last_updated_ms, last_session_id, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(source_path) DO UPDATE SET
			last_updated_ms = excluded.last_updated_ms,
			last_session_id = excluded.last_session_id,
			updated_at = CURRENT_TIMESTAMP`,
		sourcePath, state.LastUpdatedMs, state.LastSessionID)
	if err != nil {
		return fmt.Errorf("set opencode scan state: %w", err)
	}
	return nil
}
