package storage

import "time"

// BriefingHealth holds aggregate counts for the briefing health section.
type BriefingHealth struct {
	Contradictions int
	Unfinished     int
	Stale          int
}

// RecentFile represents a recently-touched file from file_coverage.
type RecentFile struct {
	Path         string
	LastTouched  time.Time
	SessionCount int
}

// GetBriefingHealth returns aggregate health counts for a project.
func (s *Store) GetBriefingHealth(project string) (BriefingHealth, error) {
	var h BriefingHealth

	// Contradictions: count distinct pairs with relation_type='contradicts'
	// where both learnings are active (not superseded)
	row := s.db.QueryRow(`
		SELECT COUNT(*) FROM associations a
		JOIN learnings l1 ON a.source_id = l1.id AND a.source_type = 'learning'
		JOIN learnings l2 ON a.target_id = l2.id AND a.target_type = 'learning'
		WHERE a.relation_type = 'contradicts'
		AND l1.superseded_by IS NULL
		AND l2.superseded_by IS NULL
		AND (l1.project = ? OR l1.project = '' OR ? = '')
		AND (l2.project = ? OR l2.project = '' OR ? = '')`,
		project, project, project, project)
	if err := row.Scan(&h.Contradictions); err != nil {
		return h, err
	}

	// Unfinished: active tasks
	count, err := s.CountActiveUnfinished(project)
	if err != nil {
		return h, err
	}
	h.Unfinished = count

	// Stale: learnings >90 days old, never cited (use_count=0), not superseded
	row = s.db.QueryRow(`
		SELECT COUNT(*) FROM learnings
		WHERE superseded_by IS NULL
		AND use_count = 0
		AND created_at < datetime('now', '-90 days')
		AND (project = ? OR project = '' OR ? = '')`,
		project, project)
	if err := row.Scan(&h.Stale); err != nil {
		return h, err
	}

	return h, nil
}

// GetRecentFiles returns the most recently-touched files for a project, ordered by recency.
func (s *Store) GetRecentFiles(project string, limit int) ([]RecentFile, error) {
	rows, err := s.db.Query(`
		SELECT file_path, last_touched, session_count
		FROM file_coverage
		WHERE project = ?
		AND last_touched > datetime('now', '-30 days')
		ORDER BY last_touched DESC
		LIMIT ?`,
		project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []RecentFile
	for rows.Next() {
		var f RecentFile
		var ts string
		if err := rows.Scan(&f.Path, &ts, &f.SessionCount); err != nil {
			return nil, err
		}
		f.LastTouched, _ = time.Parse(time.RFC3339, ts)
		if f.LastTouched.IsZero() {
			f.LastTouched, _ = time.Parse("2006-01-02 15:04:05", ts)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}
