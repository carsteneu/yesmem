package storage

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/models"
)

// InsertSelfFeedback stores a self-feedback entry.
func (s *Store) InsertSelfFeedback(fb *models.SelfFeedback) error {
	_, err := s.db.Exec(`INSERT INTO self_feedback (session_id, feedback_type, description, pattern, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		fb.SessionID, fb.FeedbackType, fb.Description, fb.Pattern, fmtTime(fb.CreatedAt))
	return err
}

// GetSelfFeedback returns feedback from the last N days.
func (s *Store) GetSelfFeedback(sinceDays int) ([]models.SelfFeedback, error) {
	rows, err := s.readerDB().Query(`SELECT id, session_id, feedback_type, description, pattern, created_at
		FROM self_feedback
		WHERE created_at > datetime('now', ?)
		ORDER BY created_at DESC`,
		fmt.Sprintf("-%d days", sinceDays))
	if err != nil {
		return nil, fmt.Errorf("get self feedback: %w", err)
	}
	defer rows.Close()

	var fbs []models.SelfFeedback
	for rows.Next() {
		var fb models.SelfFeedback
		var createdAt string
		if err := rows.Scan(&fb.ID, &fb.SessionID, &fb.FeedbackType, &fb.Description, &fb.Pattern, &createdAt); err != nil {
			return nil, err
		}
		fb.CreatedAt = parseTime(createdAt)
		fbs = append(fbs, fb)
	}
	return fbs, rows.Err()
}
