package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// normalizeTraitKey strips dimension prefixes from trait keys and trims whitespace.
// "communication.directness" → "directness", "context.context.context.yesmem_depth" → "yesmem_depth"
func normalizeTraitKey(dimension, key string) (string, string) {
	dimension = strings.TrimSpace(dimension)
	key = strings.TrimSpace(key)
	if i := strings.LastIndex(key, "."); i >= 0 {
		key = key[i+1:]
	}
	return dimension, key
}

// UpsertPersonaTrait inserts or updates a persona trait.
// Uses the UNIQUE(user_id, dimension, trait_key) constraint for upsert.
func (s *Store) UpsertPersonaTrait(t *models.PersonaTrait) error {
	t.Dimension, t.TraitKey = normalizeTraitKey(t.Dimension, t.TraitKey)
	now := fmtTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO persona_traits
		(user_id, dimension, trait_key, trait_value, confidence, source, evidence_count, first_seen, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, dimension, trait_key) DO UPDATE SET
			trait_value = excluded.trait_value,
			confidence = excluded.confidence,
			source = excluded.source,
			evidence_count = excluded.evidence_count,
			updated_at = excluded.updated_at,
			superseded = FALSE`,
		t.UserID, t.Dimension, t.TraitKey, t.TraitValue, t.Confidence,
		t.Source, t.EvidenceCount, now, now)
	if err != nil {
		return fmt.Errorf("upsert persona trait: %w", err)
	}
	return nil
}

// GetPersonaTrait returns a single trait by user/dimension/key.
func (s *Store) GetPersonaTrait(userID, dimension, traitKey string) (*models.PersonaTrait, error) {
	row := s.readerDB().QueryRow(`SELECT id, user_id, dimension, trait_key, trait_value,
		confidence, source, evidence_count, first_seen, updated_at, superseded
		FROM persona_traits
		WHERE user_id = ? AND dimension = ? AND trait_key = ? AND superseded = FALSE`,
		userID, dimension, traitKey)

	t := &models.PersonaTrait{}
	var firstSeen, updatedAt string
	err := row.Scan(&t.ID, &t.UserID, &t.Dimension, &t.TraitKey, &t.TraitValue,
		&t.Confidence, &t.Source, &t.EvidenceCount, &firstSeen, &updatedAt, &t.Superseded)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get persona trait: %w", err)
	}
	t.FirstSeen = parseTime(firstSeen)
	t.UpdatedAt = parseTime(updatedAt)
	return t, nil
}

// GetActivePersonaTraits returns all non-superseded traits above the confidence threshold.
func (s *Store) GetActivePersonaTraits(userID string, minConfidence float64) ([]models.PersonaTrait, error) {
	rows, err := s.readerDB().Query(`SELECT id, user_id, dimension, trait_key, trait_value,
		confidence, source, evidence_count, first_seen, updated_at, superseded
		FROM persona_traits
		WHERE user_id = ? AND superseded = FALSE AND confidence >= ?
		ORDER BY dimension, trait_key`,
		userID, minConfidence)
	if err != nil {
		return nil, fmt.Errorf("get active persona traits: %w", err)
	}
	defer rows.Close()

	var traits []models.PersonaTrait
	for rows.Next() {
		var t models.PersonaTrait
		var firstSeen, updatedAt string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Dimension, &t.TraitKey, &t.TraitValue,
			&t.Confidence, &t.Source, &t.EvidenceCount, &firstSeen, &updatedAt, &t.Superseded); err != nil {
			return nil, err
		}
		t.FirstSeen = parseTime(firstSeen)
		t.UpdatedAt = parseTime(updatedAt)
		traits = append(traits, t)
	}
	return traits, rows.Err()
}

// GetWellEvidencedTraits returns active traits filtered by both confidence and evidence count.
// Traits with source "user_override" or "bootstrapped" are always included regardless of evidence.
func (s *Store) GetWellEvidencedTraits(userID string, minConfidence float64, minEvidence int) ([]models.PersonaTrait, error) {
	rows, err := s.readerDB().Query(`SELECT id, user_id, dimension, trait_key, trait_value,
		confidence, source, evidence_count, first_seen, updated_at, superseded
		FROM persona_traits
		WHERE user_id = ? AND superseded = FALSE AND confidence >= ?
		AND (evidence_count >= ? OR source IN ('user_override', 'bootstrapped', 'learning_scan'))
		ORDER BY dimension, trait_key`,
		userID, minConfidence, minEvidence)
	if err != nil {
		return nil, fmt.Errorf("get well-evidenced traits: %w", err)
	}
	defer rows.Close()

	var traits []models.PersonaTrait
	for rows.Next() {
		var t models.PersonaTrait
		var firstSeen, updatedAt string
		if err := rows.Scan(&t.ID, &t.UserID, &t.Dimension, &t.TraitKey, &t.TraitValue,
			&t.Confidence, &t.Source, &t.EvidenceCount, &firstSeen, &updatedAt, &t.Superseded); err != nil {
			return nil, err
		}
		t.FirstSeen = parseTime(firstSeen)
		t.UpdatedAt = parseTime(updatedAt)
		traits = append(traits, t)
	}
	return traits, rows.Err()
}

// SupersedePersonaTrait marks a trait as superseded.
func (s *Store) SupersedePersonaTrait(userID, dimension, traitKey string) error {
	_, err := s.db.Exec(`UPDATE persona_traits SET superseded = TRUE
		WHERE user_id = ? AND dimension = ? AND trait_key = ?`,
		userID, dimension, traitKey)
	return err
}

// ApplyConfidenceDelta adjusts confidence for an existing trait and increments evidence_count.
// Clamps result to [0.0, 1.0].
func (s *Store) ApplyConfidenceDelta(userID, dimension, traitKey string, delta float64) error {
	_, err := s.db.Exec(`UPDATE persona_traits SET
		confidence = MIN(1.0, MAX(0.0, confidence + ?)),
		evidence_count = evidence_count + 1,
		updated_at = ?
		WHERE user_id = ? AND dimension = ? AND trait_key = ? AND superseded = FALSE`,
		delta, fmtTime(time.Now()), userID, dimension, traitKey)
	return err
}

// DeleteAllPersonaData removes all traits and directives for a user. Returns count of deleted traits.
func (s *Store) DeleteAllPersonaData(userID string) int {
	var count int
	s.readerDB().QueryRow("SELECT COUNT(*) FROM persona_traits WHERE user_id = ?", userID).Scan(&count)
	s.db.Exec("DELETE FROM persona_traits WHERE user_id = ?", userID)
	s.db.Exec("DELETE FROM persona_directives WHERE user_id = ?", userID)
	return count
}

// SavePersonaDirective upserts a persona directive (delete-then-insert since user_id has no UNIQUE constraint).
func (s *Store) SavePersonaDirective(d *models.PersonaDirective) error {
	s.db.Exec(`DELETE FROM persona_directives WHERE user_id = ?`, d.UserID)
	_, err := s.db.Exec(`INSERT INTO persona_directives
		(user_id, directive, traits_hash, generated_at, model_used)
		VALUES (?, ?, ?, ?, ?)`,
		d.UserID, d.Directive, d.TraitsHash, fmtTime(d.GeneratedAt), d.ModelUsed)
	if err != nil {
		return fmt.Errorf("save persona directive: %w", err)
	}
	return nil
}

// InvalidatePersonaDirectiveHash forces re-synthesis by setting the hash to an invalid value.
func (s *Store) InvalidatePersonaDirectiveHash(userID string) {
	s.db.Exec(`UPDATE persona_directives SET traits_hash = 'invalidated' WHERE user_id = ?`, userID)
}

// GetPersonaDirective returns the most recent directive for a user.
func (s *Store) GetPersonaDirective(userID string) (*models.PersonaDirective, error) {
	row := s.readerDB().QueryRow(`SELECT id, user_id, directive, traits_hash, generated_at, model_used
		FROM persona_directives
		WHERE user_id = ?
		ORDER BY generated_at DESC LIMIT 1`, userID)

	d := &models.PersonaDirective{}
	var generatedAt string
	err := row.Scan(&d.ID, &d.UserID, &d.Directive, &d.TraitsHash, &generatedAt, &d.ModelUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get persona directive: %w", err)
	}
	d.GeneratedAt = parseTime(generatedAt)
	return d, nil
}
