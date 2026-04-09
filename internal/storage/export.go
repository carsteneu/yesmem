package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// ExportData is the top-level JSON structure for export/import.
type ExportData struct {
	Version          int                    `json:"version"`
	ExportedAt       time.Time              `json:"exported_at"`
	Learnings        []ExportLearning       `json:"learnings"`
	PersonaOverrides []ExportPersonaTrait   `json:"persona_overrides,omitempty"`
}

// ExportLearning is a single learning in the export format.
type ExportLearning struct {
	Category           string    `json:"category"`
	Content            string    `json:"content"`
	Project            string    `json:"project,omitempty"`
	Source             string    `json:"source"`
	Confidence         float64   `json:"confidence,omitempty"`
	EmotionalIntensity float64   `json:"emotional_intensity,omitempty"`
	SessionFlavor      string    `json:"session_flavor,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

// ExportPersonaTrait is a persona override in the export format.
type ExportPersonaTrait struct {
	Dimension  string `json:"dimension"`
	TraitKey   string `json:"trait_key"`
	TraitValue string `json:"trait_value"`
}

// GetNonRecoverableLearnings returns active learnings that cannot be re-extracted from JSONL.
func (s *Store) GetNonRecoverableLearnings() ([]*models.Learning, error) {
	rows, err := s.readerDB().Query(`
		SELECT id, session_id, category, content, project, source, confidence,
		       emotional_intensity, session_flavor, created_at
		FROM learnings
		WHERE superseded_by IS NULL
		  AND source IN ('user_stated', 'claude_suggested', 'agreed_upon', 'user_override')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query non-recoverable: %w", err)
	}
	defer rows.Close()

	var result []*models.Learning
	for rows.Next() {
		l := &models.Learning{}
		var sessionID, project, source, flavor, createdAt *string
		if err := rows.Scan(&l.ID, &sessionID, &l.Category, &l.Content, &project,
			&source, &l.Confidence, &l.EmotionalIntensity, &flavor, &createdAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if sessionID != nil {
			l.SessionID = *sessionID
		}
		if project != nil {
			l.Project = *project
		}
		if source != nil {
			l.Source = *source
		}
		if flavor != nil {
			l.SessionFlavor = *flavor
		}
		if createdAt != nil {
			l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", *createdAt)
		}
		result = append(result, l)
	}
	return result, nil
}

// ExportLearnings writes non-recoverable learnings + persona overrides to a JSON file.
func (s *Store) ExportLearnings(path string) error {
	learnings, err := s.GetNonRecoverableLearnings()
	if err != nil {
		return err
	}

	export := ExportData{
		Version:    1,
		ExportedAt: time.Now(),
	}

	for _, l := range learnings {
		export.Learnings = append(export.Learnings, ExportLearning{
			Category:           l.Category,
			Content:            l.Content,
			Project:            l.Project,
			Source:             l.Source,
			Confidence:         l.Confidence,
			EmotionalIntensity: l.EmotionalIntensity,
			SessionFlavor:      l.SessionFlavor,
			CreatedAt:          l.CreatedAt,
		})
	}

	// Export user_override persona traits
	rows, err := s.readerDB().Query(`
		SELECT dimension, trait_key, trait_value
		FROM persona_traits
		WHERE source = 'user_override'
		ORDER BY trait_key ASC
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pt ExportPersonaTrait
			if err := rows.Scan(&pt.Dimension, &pt.TraitKey, &pt.TraitValue); err == nil {
				export.PersonaOverrides = append(export.PersonaOverrides, pt)
			}
		}
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// ImportLearnings reads a JSON export file and inserts non-duplicate learnings.
// Returns (imported, skipped, error).
func (s *Store) ImportLearnings(path string) (int, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read file: %w", err)
	}

	var export ExportData
	if err := json.Unmarshal(data, &export); err != nil {
		return 0, 0, fmt.Errorf("unmarshal: %w", err)
	}

	var imported, skipped int
	for _, el := range export.Learnings {
		// Check for duplicate (same content + category + project)
		var count int
		err := s.readerDB().QueryRow(`
			SELECT COUNT(*) FROM learnings
			WHERE content = ? AND category = ? AND COALESCE(project, '') = ?
			  AND superseded_by IS NULL
		`, el.Content, el.Category, el.Project).Scan(&count)
		if err != nil {
			return imported, skipped, fmt.Errorf("check dup: %w", err)
		}
		if count > 0 {
			skipped++
			continue
		}

		_, err = s.InsertLearning(&models.Learning{
			Category:           el.Category,
			Content:            el.Content,
			Project:            el.Project,
			Source:             el.Source,
			Confidence:         el.Confidence,
			EmotionalIntensity: el.EmotionalIntensity,
			SessionFlavor:      el.SessionFlavor,
			CreatedAt:          el.CreatedAt,
			ModelUsed:          "import",
		})
		if err != nil {
			return imported, skipped, fmt.Errorf("insert: %w", err)
		}
		imported++
	}

	// Import persona overrides
	for _, pt := range export.PersonaOverrides {
		s.UpsertPersonaTrait(&models.PersonaTrait{
			UserID:     "default",
			Dimension:  pt.Dimension,
			TraitKey:   pt.TraitKey,
			TraitValue: pt.TraitValue,
			Source:     "user_override",
		})
	}

	return imported, skipped, nil
}
