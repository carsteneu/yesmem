package storage

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetRefinedBriefing returns the cached refined briefing for a project, or empty string if not found.
func (s *Store) GetRefinedBriefing(project string) (string, error) {
	var text string
	err := s.readerDB().QueryRow("SELECT refined_text FROM refined_briefings WHERE project = ?", project).Scan(&text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return text, nil
}

// SaveRefinedBriefing upserts the refined briefing cache for a project.
func (s *Store) SaveRefinedBriefing(project, rawHash, refinedText, modelUsed string) error {
	_, err := s.db.Exec(`INSERT INTO refined_briefings (project, raw_hash, refined_text, model_used, generated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(project) DO UPDATE SET raw_hash=excluded.raw_hash, refined_text=excluded.refined_text, model_used=excluded.model_used, generated_at=excluded.generated_at`,
		project, rawHash, refinedText, modelUsed, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GetRefinedBriefingHash returns the raw_hash of the cached briefing, or empty if not found.
func (s *Store) GetRefinedBriefingHash(project string) (string, error) {
	var hash string
	err := s.readerDB().QueryRow("SELECT raw_hash FROM refined_briefings WHERE project = ?", project).Scan(&hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return hash, nil
}

// ProjectChangeFingerprint computes a lightweight fingerprint from learnings + sessions counts
// and timestamps. This is stable between checks unless actual data changes (new sessions,
// new/superseded learnings). Avoids generating the full raw briefing just to check for changes.
func (s *Store) ProjectChangeFingerprint(projectShort string) string {
	var sessionCount int
	var maxSessionAt sql.NullString
	s.readerDB().QueryRow(`SELECT COUNT(*), MAX(indexed_at) FROM sessions WHERE project_short = ?`, projectShort).Scan(&sessionCount, &maxSessionAt)

	var learningCount int
	var maxLearningAt sql.NullString
	s.readerDB().QueryRow(`SELECT COUNT(*), MAX(created_at) FROM learnings WHERE project = ? AND superseded_by IS NULL`, projectShort).Scan(&learningCount, &maxLearningAt)

	var supersededCount int
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE project = ? AND superseded_by IS NOT NULL`, projectShort).Scan(&supersededCount)

	var maxDirectiveAt sql.NullString
	s.readerDB().QueryRow(`SELECT MAX(generated_at) FROM persona_directives`).Scan(&maxDirectiveAt)

	raw := fmt.Sprintf("s:%d:%s|l:%d:%s|x:%d|p:%s", sessionCount, maxSessionAt.String, learningCount, maxLearningAt.String, supersededCount, maxDirectiveAt.String)
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h[:16])
}
