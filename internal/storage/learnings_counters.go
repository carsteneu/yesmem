package storage

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// IncrementHitCounts bumps hit_count for the given learning IDs.
func (s *Store) IncrementHitCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET hit_count = COALESCE(hit_count, 0) + 1, last_hit_at = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	now := time.Now().Format(time.RFC3339)
	for _, id := range ids {
		if _, err := stmt.Exec(now, id); err != nil {
			tx.Rollback()
			return fmt.Errorf("increment hit for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// IncrementFailCounts bumps fail_count for the given learning IDs.
// Only called from hook-failure when an actual Bash command fails.
func (s *Store) IncrementFailCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET fail_count = COALESCE(fail_count, 0) + 1 WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return fmt.Errorf("increment fail for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// IncrementNoiseCounts increments the noise_count for the given learning IDs.
func (s *Store) IncrementNoiseCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET noise_count = COALESCE(noise_count, 0) + 1 WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return fmt.Errorf("increment noise for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// SetImportance sets the importance field for a single learning.
func (s *Store) SetImportance(id int64, importance int) error {
	_, err := s.db.Exec("UPDATE learnings SET importance = ? WHERE id = ?", importance, id)
	return err
}

// SetStability sets the stability field for a single learning.
func (s *Store) SetStability(id int64, stability float64) error {
	_, err := s.db.Exec("UPDATE learnings SET stability = ? WHERE id = ?", stability, id)
	return err
}

// IncrementMatchCounts bumps match_count for the given learning IDs.
// Called when a learning is matched as a candidate (before injection decision).
func (s *Store) IncrementMatchCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET match_count = COALESCE(match_count, 0) + 1 WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return fmt.Errorf("increment match for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// CountActiveUnfinished returns the number of active unfinished learnings for a project.
// Used as a fast check to decide whether to inject the open work reminder instruction.
func (s *Store) CountActiveUnfinished(project string) (int, error) {
	var count int
	err := s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE category = 'unfinished' AND superseded_by IS NULL AND (expires_at IS NULL OR expires_at > ?) AND (project = ? OR project IS NULL OR project = '')`, fmtTime(time.Now()), project).Scan(&count)
	return count, err
}

// UpdateLastHitAt sets last_hit_at to now for the given learning IDs.
// Used by deadline triggers to enforce cooldown without incrementing counters.
func (s *Store) UpdateLastHitAt(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET last_hit_at = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(now, id); err != nil {
			tx.Rollback()
			return fmt.Errorf("update last_hit_at for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// IncrementInjectCounts bumps inject_count for the given learning IDs and updates last_hit_at.
// Called when a learning is actually injected into context.
func (s *Store) IncrementInjectCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)
	stmt, err := tx.Prepare(`UPDATE learnings SET inject_count = COALESCE(inject_count, 0) + 1, last_hit_at = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(now, id); err != nil {
			return fmt.Errorf("increment inject for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// IncrementUseCounts bumps use_count for the given learning IDs.
// Called when Claude demonstrably acts on an injected learning.
// If gap since last injection >= 24h, stability grows by 30% (capped at 365.0).
func (s *Store) IncrementUseCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().Format(time.RFC3339)
	for _, id := range ids {
		// Check gap for stability growth — only USE grows stability
		var lastHit sql.NullString
		tx.QueryRow(`SELECT last_hit_at FROM learnings WHERE id = ?`, id).Scan(&lastHit)

		stabilityClause := ""
		if lastHit.Valid && lastHit.String != "" {
			if last, err := time.Parse(time.RFC3339, lastHit.String); err == nil {
				if time.Since(last).Hours() >= 24.0 {
					stabilityClause = ", stability = COALESCE(stability, 30.0) * 1.3"
				}
			}
		}

		_, err := tx.Exec(
			`UPDATE learnings SET use_count = COALESCE(use_count, 0) + 1`+stabilityClause+` WHERE id = ?`,
			id)
		if err != nil {
			return fmt.Errorf("increment use for %d: %w", id, err)
		}
	}
	_ = now // available for future use
	return tx.Commit()
}

// IncrementSaveCounts bumps save_count for the given learning IDs.
// Called when a learning is explicitly saved/confirmed by the user.
func (s *Store) IncrementSaveCounts(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET save_count = COALESCE(save_count, 0) + 1 WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			tx.Rollback()
			return fmt.Errorf("increment save for %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// UpdateSessionFixationRatio writes the fixation ratio for a session.
func (s *Store) UpdateSessionFixationRatio(sessionID string, ratio float64) error {
	_, err := s.db.Exec(`UPDATE sessions SET fixation_ratio = ? WHERE id = ?`, ratio, sessionID)
	return err
}

// GetSessionFixationRatio returns the fixation_ratio for a session.
func (s *Store) GetSessionFixationRatio(sessionID string) (float64, error) {
	var ratio float64
	err := s.readerDB().QueryRow(`SELECT COALESCE(fixation_ratio, 0) FROM sessions WHERE id = ?`, sessionID).Scan(&ratio)
	if err != nil {
		return 0, err
	}
	return ratio, nil
}

// EnrichSessionFixationScores bulk-loads fixation ratios from sessions table
// and sets SessionFixationRatio on each learning.
func (s *Store) EnrichSessionFixationScores(learnings []models.Learning) {
	sessionIDs := make(map[string]bool)
	for _, l := range learnings {
		if l.SessionID != "" {
			sessionIDs[l.SessionID] = true
		}
	}
	if len(sessionIDs) == 0 {
		return
	}

	ratios := make(map[string]float64, len(sessionIDs))
	for sid := range sessionIDs {
		ratio, err := s.GetSessionFixationRatio(sid)
		if err == nil && ratio > 0 {
			ratios[sid] = ratio
		}
	}

	for i := range learnings {
		if r, ok := ratios[learnings[i].SessionID]; ok {
			learnings[i].SessionFixationRatio = r
		}
	}
}
