package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// TrackGap records or increments a knowledge gap.
func (s *Store) TrackGap(topic, project string) error {
	_, err := s.db.Exec(`
		INSERT INTO knowledge_gaps (topic, project)
		VALUES (?, ?)
		ON CONFLICT(topic, project) DO UPDATE SET
			hit_count = hit_count + 1,
			last_seen = CURRENT_TIMESTAMP
	`, topic, project)
	return err
}

// GetActiveGaps returns unresolved gaps for a project, ordered by hit_count.
func (s *Store) GetActiveGaps(project string, limit int) ([]models.KnowledgeGap, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.readerDB().Query(`
		SELECT id, topic, COALESCE(project, ''), first_seen, last_seen, hit_count
		FROM knowledge_gaps
		WHERE resolved_at IS NULL
		AND (project = ? OR project IS NULL)
		ORDER BY hit_count DESC, last_seen DESC
		LIMIT ?
	`, project, limit)
	if err != nil {
		return nil, fmt.Errorf("get active gaps: %w", err)
	}
	defer rows.Close()

	var gaps []models.KnowledgeGap
	for rows.Next() {
		var g models.KnowledgeGap
		var firstSeen, lastSeen string
		if err := rows.Scan(&g.ID, &g.Topic, &g.Project, &firstSeen, &lastSeen, &g.HitCount); err != nil {
			return nil, err
		}
		g.FirstSeen = parseTime(firstSeen)
		g.LastSeen = parseTime(lastSeen)
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}

// ResolveGap marks a gap as resolved when a learning is created for that topic.
func (s *Store) ResolveGap(topic, project string, learningID int64) error {
	_, err := s.db.Exec(`
		UPDATE knowledge_gaps
		SET resolved_at = CURRENT_TIMESTAMP, resolved_by = ?
		WHERE topic = ? AND (project = ? OR project IS NULL)
		AND resolved_at IS NULL
	`, learningID, topic, project)
	return err
}

// GetUnreviewedGaps returns open gaps that haven't been reviewed by LLM yet.
func (s *Store) GetUnreviewedGaps(project string, limit int) ([]models.KnowledgeGap, error) {
	if limit <= 0 {
		limit = 100
	}
	filter := "resolved_at IS NULL AND reviewed_at IS NULL"
	args := []any{}
	if project != "" {
		filter += " AND project = ?"
		args = append(args, project)
	}
	args = append(args, limit)
	rows, err := s.readerDB().Query(`SELECT id, topic, COALESCE(project, ''), first_seen, last_seen, hit_count FROM knowledge_gaps WHERE `+filter+` ORDER BY hit_count DESC, last_seen DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gaps []models.KnowledgeGap
	for rows.Next() {
		var g models.KnowledgeGap
		var firstSeen, lastSeen string
		if err := rows.Scan(&g.ID, &g.Topic, &g.Project, &firstSeen, &lastSeen, &g.HitCount); err != nil {
			return nil, err
		}
		g.FirstSeen = parseTime(firstSeen)
		g.LastSeen = parseTime(lastSeen)
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}

// MarkGapReviewed sets the review verdict on a gap.
func (s *Store) MarkGapReviewed(id int64, verdict string) error {
	_, err := s.db.Exec(`UPDATE knowledge_gaps SET reviewed_at = CURRENT_TIMESTAMP, review_verdict = ? WHERE id = ?`, verdict, id)
	return err
}

// DeleteGap removes a gap entirely.
func (s *Store) DeleteGap(id int64) error {
	_, err := s.db.Exec(`DELETE FROM knowledge_gaps WHERE id = ?`, id)
	return err
}

// GetLastGapReviewTime returns the most recent reviewed_at timestamp, or zero time if none.
func (s *Store) GetLastGapReviewTime() time.Time {
	var ts sql.NullString
	s.readerDB().QueryRow(`SELECT MAX(reviewed_at) FROM knowledge_gaps WHERE reviewed_at IS NOT NULL`).Scan(&ts)
	if !ts.Valid || ts.String == "" {
		return time.Time{}
	}
	return parseTime(ts.String)
}
