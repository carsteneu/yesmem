package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// IncrementTurnCount atomically increments the turn counter for a project.
// Returns the new count. Creates the row if it doesn't exist.
func (s *Store) IncrementTurnCount(project string) (int64, error) {
	var count int64
	err := s.db.QueryRow(`INSERT INTO turn_counters (project, turn_count, updated_at) VALUES (?, 1, CURRENT_TIMESTAMP) ON CONFLICT(project) DO UPDATE SET turn_count = turn_count + 1, updated_at = CURRENT_TIMESTAMP RETURNING turn_count`, project).Scan(&count)
	return count, err
}

// GetTurnCount returns the current turn count for a project. Returns 0 if not found.
func (s *Store) GetTurnCount(project string) (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT turn_count FROM turn_counters WHERE project = ?`, project).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// GetTurnCountsBulk returns turn counts for multiple projects in a single query.
// Projects not found return 0.
func (s *Store) GetTurnCountsBulk(projects []string) (map[string]int64, error) {
	result := make(map[string]int64)
	if len(projects) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(projects))
	args := make([]any, len(projects))
	for i, p := range projects {
		placeholders[i] = "?"
		args[i] = p
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT project, turn_count FROM turn_counters WHERE project IN (%s)`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var proj string
		var count int64
		if err := rows.Scan(&proj, &count); err != nil {
			return result, err
		}
		result[proj] = count
	}
	return result, rows.Err()
}
