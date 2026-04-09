package storage

import "time"

// PlanRow represents a persisted plan.
type PlanRow struct {
	ThreadID  string
	Content   string
	Status    string
	Scope     string
	Project   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertPlan creates or updates a plan for a thread.
func (s *Store) UpsertPlan(p *PlanRow) error {
	_, err := s.db.Exec(`INSERT INTO plans (thread_id, content, status, scope, project, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET content=?, status=?, scope=?, project=?, updated_at=?`,
		p.ThreadID, p.Content, p.Status, p.Scope, p.Project,
		p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
		p.Content, p.Status, p.Scope, p.Project, p.UpdatedAt.Format(time.RFC3339))
	return err
}

// GetPlan returns the plan for a thread, or nil if none exists.
func (s *Store) GetPlan(threadID string) (*PlanRow, error) {
	var p PlanRow
	var createdAt, updatedAt string
	err := s.readerDB().QueryRow("SELECT thread_id, content, status, scope, COALESCE(project,''), created_at, updated_at FROM plans WHERE thread_id = ?", threadID).
		Scan(&p.ThreadID, &p.Content, &p.Status, &p.Scope, &p.Project, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &p, nil
}

// GetActivePlans returns all active plans (for loading into memory on startup).
func (s *Store) GetActivePlans() ([]*PlanRow, error) {
	rows, err := s.readerDB().Query("SELECT thread_id, content, status, scope, COALESCE(project,''), created_at, updated_at FROM plans WHERE status = 'active'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*PlanRow
	for rows.Next() {
		var p PlanRow
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ThreadID, &p.Content, &p.Status, &p.Scope, &p.Project, &createdAt, &updatedAt); err != nil {
			continue
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		plans = append(plans, &p)
	}
	return plans, nil
}

// DeletePlan removes a plan for a thread.
func (s *Store) DeletePlan(threadID string) error {
	_, err := s.db.Exec("DELETE FROM plans WHERE thread_id = ?", threadID)
	return err
}
