package storage

import (
	"database/sql"
	"time"
)

// PinnedLearning represents a pinned instruction visible in every briefing turn.
type PinnedLearning struct {
	ID        int64
	Project   string
	Content   string
	Source    string
	CreatedAt time.Time
	Scope     string // "session" or "permanent" — set on read based on source DB
}

const tablePinnedLearnings = `CREATE TABLE IF NOT EXISTS pinned_learnings (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project    TEXT NOT NULL DEFAULT '',
	content    TEXT NOT NULL,
	source     TEXT NOT NULL DEFAULT 'user_stated',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

func (s *Store) pinnedDB(scope string) *sql.DB {
	if scope == "session" {
		return s.proxyStateDB() // runtime.db
	}
	return s.db // yesmem.db
}

// PinLearning creates a new pinned learning in the appropriate DB.
func (s *Store) PinLearning(scope, project, content, source string) (int64, error) {
	if source == "" {
		source = "user_stated"
	}
	db := s.pinnedDB(scope)
	res, err := db.Exec(`INSERT INTO pinned_learnings (project, content, source) VALUES (?, ?, ?)`,
		project, content, source)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UnpinLearning removes a pinned learning by ID.
func (s *Store) UnpinLearning(scope string, id int64) error {
	db := s.pinnedDB(scope)
	_, err := db.Exec(`DELETE FROM pinned_learnings WHERE id = ?`, id)
	return err
}

// GetPinnedLearnings returns all active pins for a project in the given scope.
func (s *Store) GetPinnedLearnings(scope, project string) ([]PinnedLearning, error) {
	db := s.pinnedDB(scope)
	rows, err := db.Query(`SELECT id, project, content, source, created_at FROM pinned_learnings WHERE project = ? OR project = '' ORDER BY created_at ASC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pins []PinnedLearning
	for rows.Next() {
		var p PinnedLearning
		if err := rows.Scan(&p.ID, &p.Project, &p.Content, &p.Source, &p.CreatedAt); err != nil {
			continue
		}
		p.Scope = scope
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

// ClearSessionPins deletes all session-scoped pins for a project.
func (s *Store) ClearSessionPins(project string) error {
	db := s.proxyStateDB()
	if project == "" {
		_, err := db.Exec(`DELETE FROM pinned_learnings`)
		return err
	}
	_, err := db.Exec(`DELETE FROM pinned_learnings WHERE project = ?`, project)
	return err
}
