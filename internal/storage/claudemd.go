package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// ClaudeMdState tracks when a project's yesmem-ops.md was last generated.
type ClaudeMdState struct {
	Project       string
	LastGenerated time.Time
	LearningsHash string
	OutputPath    string
}

// GetClaudeMdState returns the generation state for a project, or nil if not found.
func (s *Store) GetClaudeMdState(project string) (*ClaudeMdState, error) {
	row := s.readerDB().QueryRow(
		`SELECT project, last_generated, learnings_hash, output_path
		 FROM claudemd_state WHERE project = ?`, project)
	st := &ClaudeMdState{}
	var lastGen string
	err := row.Scan(&st.Project, &lastGen, &st.LearningsHash, &st.OutputPath)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get claudemd_state %s: %w", project, err)
	}
	st.LastGenerated = parseTime(lastGen)
	return st, nil
}

// SaveClaudeMdState upserts the generation state for a project.
func (s *Store) SaveClaudeMdState(st *ClaudeMdState) error {
	_, err := s.db.Exec(
		`INSERT INTO claudemd_state (project, last_generated, learnings_hash, output_path)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(project) DO UPDATE SET
		     last_generated = excluded.last_generated,
		     learnings_hash = excluded.learnings_hash,
		     output_path    = excluded.output_path`,
		st.Project, fmtTime(st.LastGenerated), st.LearningsHash, st.OutputPath)
	if err != nil {
		return fmt.Errorf("save claudemd_state %s: %w", st.Project, err)
	}
	return nil
}
