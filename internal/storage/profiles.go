package storage

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/models"
)

// UpsertProjectProfile inserts or updates a project profile.
func (s *Store) UpsertProjectProfile(p *models.ProjectProfile) error {
	_, err := s.db.Exec(`INSERT INTO project_profiles (project, profile_text, generated_at, updated_at, session_count, model_used)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET
			profile_text = excluded.profile_text,
			updated_at = excluded.updated_at,
			session_count = excluded.session_count,
			model_used = excluded.model_used`,
		p.Project, p.ProfileText, fmtTime(p.GeneratedAt), fmtTime(p.UpdatedAt),
		p.SessionCount, p.ModelUsed)
	return err
}

// GetProjectProfile returns the profile for a project.
func (s *Store) GetProjectProfile(project string) (*models.ProjectProfile, error) {
	row := s.readerDB().QueryRow(`SELECT project, profile_text, generated_at, updated_at, session_count, model_used
		FROM project_profiles WHERE project = ?`, project)

	p := &models.ProjectProfile{}
	var generatedAt, updatedAt string
	err := row.Scan(&p.Project, &p.ProfileText, &generatedAt, &updatedAt, &p.SessionCount, &p.ModelUsed)
	if err != nil {
		return nil, fmt.Errorf("get project profile %s: %w", project, err)
	}
	p.GeneratedAt = parseTime(generatedAt)
	p.UpdatedAt = parseTime(updatedAt)
	return p, nil
}

// UpsertStrategicContext inserts or updates a strategic context entry.
func (s *Store) UpsertStrategicContext(ctx *models.StrategicContext) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO strategic_context (scope, context, source, created_at, active)
		VALUES (?, ?, ?, ?, ?)`,
		ctx.Scope, ctx.Context, ctx.Source, fmtTime(ctx.CreatedAt), ctx.Active)
	if err != nil {
		return 0, fmt.Errorf("insert strategic context: %w", err)
	}
	return result.LastInsertId()
}

// GetActiveStrategicContext returns all active strategic context entries.
func (s *Store) GetActiveStrategicContext() ([]models.StrategicContext, error) {
	rows, err := s.readerDB().Query(`SELECT id, scope, context, source, created_at, active
		FROM strategic_context WHERE active = TRUE AND superseded_by IS NULL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ctxs []models.StrategicContext
	for rows.Next() {
		var ctx models.StrategicContext
		var createdAt string
		if err := rows.Scan(&ctx.ID, &ctx.Scope, &ctx.Context, &ctx.Source, &createdAt, &ctx.Active); err != nil {
			return nil, err
		}
		ctx.CreatedAt = parseTime(createdAt)
		ctxs = append(ctxs, ctx)
	}
	return ctxs, rows.Err()
}
