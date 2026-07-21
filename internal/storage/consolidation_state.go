package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// ConsolidationState belongs to the learning database it describes so a
// backup or restore cannot silently pair learnings with another DB's watermark.
type ConsolidationState struct {
	LastLearningID int64  `json:"last_learning_id"`
	CompletedAt    string `json:"completed_at"`
}

func (s *Store) GetConsolidationState(key string) (ConsolidationState, bool, error) {
	var state ConsolidationState
	err := s.db.QueryRow(`SELECT last_learning_id, completed_at FROM consolidation_state WHERE key = ?`, key).
		Scan(&state.LastLearningID, &state.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsolidationState{}, false, nil
	}
	if err != nil {
		return ConsolidationState{}, false, fmt.Errorf("get consolidation state: %w", err)
	}
	return state, true, nil
}

func (s *Store) SetConsolidationState(key string, state ConsolidationState) error {
	_, err := s.db.Exec(`INSERT INTO consolidation_state (key, last_learning_id, completed_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET last_learning_id=excluded.last_learning_id, completed_at=excluded.completed_at`,
		key, state.LastLearningID, state.CompletedAt)
	if err != nil {
		return fmt.Errorf("set consolidation state: %w", err)
	}
	return nil
}
