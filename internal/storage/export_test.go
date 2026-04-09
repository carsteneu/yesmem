package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestExportNonRecoverableLearnings(t *testing.T) {
	store := mustOpen(t)
	now := time.Now()

	// Insert recoverable learning (llm_extracted)
	store.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "recoverable from JSONL",
		Project:   "test",
		Source:    "llm_extracted",
		CreatedAt: now,
	})

	// Insert non-recoverable learnings
	store.InsertLearning(&models.Learning{
		Category:  "preference",
		Content:   "user prefers dark mode",
		Project:   "test",
		Source:    "user_stated",
		CreatedAt: now,
	})
	store.InsertLearning(&models.Learning{
		Category:  "decision",
		Content:   "use DB instead of tmpfiles",
		Project:   "test",
		Source:    "agreed_upon",
		CreatedAt: now,
	})
	store.InsertLearning(&models.Learning{
		Category:  "pattern",
		Content:   "suggested pattern for deploy",
		Project:   "test",
		Source:    "claude_suggested",
		CreatedAt: now,
	})

	// Superseded learning — should NOT be exported
	superseded, _ := store.InsertLearning(&models.Learning{
		Category:  "preference",
		Content:   "old superseded preference",
		Project:   "test",
		Source:    "user_stated",
		CreatedAt: now,
	})
	store.SupersedeLearning(superseded, 999, "replaced by newer")

	learnings, err := store.GetNonRecoverableLearnings()
	if err != nil {
		t.Fatalf("GetNonRecoverableLearnings: %v", err)
	}

	if len(learnings) != 3 {
		t.Fatalf("expected 3 non-recoverable learnings, got %d", len(learnings))
	}

	// Verify only non-llm sources
	for _, l := range learnings {
		if l.Source == "llm_extracted" {
			t.Errorf("should not include llm_extracted, got: %s", l.Content)
		}
		if l.SupersededBy != nil {
			t.Errorf("should not include superseded, got: %s", l.Content)
		}
	}
}

func TestExportToFile(t *testing.T) {
	store := mustOpen(t)
	now := time.Now()

	store.InsertLearning(&models.Learning{
		Category:           "preference",
		Content:            "test export content",
		Project:            "myproject",
		Source:             "user_stated",
		Confidence:         0.9,
		EmotionalIntensity: 0.7,
		SessionFlavor:      "debugging proxy",
		CreatedAt:          now,
	})

	outPath := filepath.Join(t.TempDir(), "export.json")
	err := store.ExportLearnings(outPath)
	if err != nil {
		t.Fatalf("ExportLearnings: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}

	var export ExportData
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}

	if export.Version != 1 {
		t.Errorf("expected version 1, got %d", export.Version)
	}
	if len(export.Learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d", len(export.Learnings))
	}
	if export.Learnings[0].Content != "test export content" {
		t.Errorf("wrong content: %s", export.Learnings[0].Content)
	}
	if export.Learnings[0].Source != "user_stated" {
		t.Errorf("wrong source: %s", export.Learnings[0].Source)
	}
}

func TestImportLearnings(t *testing.T) {
	store := mustOpen(t)
	now := time.Now()

	// Create export data
	exportData := ExportData{
		Version:    1,
		ExportedAt: now,
		Learnings: []ExportLearning{
			{
				Category:           "preference",
				Content:            "imported preference",
				Project:            "imported-project",
				Source:             "user_stated",
				Confidence:         0.9,
				EmotionalIntensity: 0.5,
				SessionFlavor:      "from export",
				CreatedAt:          now,
			},
			{
				Category:  "decision",
				Content:   "imported decision",
				Project:   "imported-project",
				Source:    "agreed_upon",
				CreatedAt: now,
			},
		},
	}

	tmpFile := filepath.Join(t.TempDir(), "import.json")
	data, _ := json.MarshalIndent(exportData, "", "  ")
	os.WriteFile(tmpFile, data, 0644)

	imported, skipped, err := store.ImportLearnings(tmpFile)
	if err != nil {
		t.Fatalf("ImportLearnings: %v", err)
	}
	if imported != 2 {
		t.Errorf("expected 2 imported, got %d", imported)
	}
	if skipped != 0 {
		t.Errorf("expected 0 skipped, got %d", skipped)
	}

	// Verify they exist in DB
	learnings, _ := store.GetActiveLearnings("preference", "imported-project", "", "")
	if len(learnings) == 0 {
		t.Fatal("imported preference not found")
	}
	if learnings[0].Content != "imported preference" {
		t.Errorf("wrong content: %s", learnings[0].Content)
	}
}

func TestImportSkipsDuplicates(t *testing.T) {
	store := mustOpen(t)
	now := time.Now()

	// Pre-existing learning
	store.InsertLearning(&models.Learning{
		Category:  "preference",
		Content:   "already exists",
		Project:   "test",
		Source:    "user_stated",
		CreatedAt: now,
	})

	exportData := ExportData{
		Version:    1,
		ExportedAt: now,
		Learnings: []ExportLearning{
			{
				Category:  "preference",
				Content:   "already exists",
				Project:   "test",
				Source:    "user_stated",
				CreatedAt: now,
			},
			{
				Category:  "decision",
				Content:   "new learning",
				Project:   "test",
				Source:    "agreed_upon",
				CreatedAt: now,
			},
		},
	}

	tmpFile := filepath.Join(t.TempDir(), "import.json")
	data, _ := json.MarshalIndent(exportData, "", "  ")
	os.WriteFile(tmpFile, data, 0644)

	imported, skipped, err := store.ImportLearnings(tmpFile)
	if err != nil {
		t.Fatalf("ImportLearnings: %v", err)
	}
	if imported != 1 {
		t.Errorf("expected 1 imported, got %d", imported)
	}
	if skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", skipped)
	}
}

func TestExportIncludesPersonaOverrides(t *testing.T) {
	store := mustOpen(t)

	// Insert user_override trait
	store.UpsertPersonaTrait(&models.PersonaTrait{
		UserID:     "default",
		Dimension:  "communication",
		TraitKey:   "language",
		TraitValue: "deutsch",
		Source:     "user_override",
	})

	// Insert auto_extracted trait — should NOT be exported
	store.UpsertPersonaTrait(&models.PersonaTrait{
		UserID:     "default",
		Dimension:  "expertise",
		TraitKey:   "go",
		TraitValue: "advanced",
		Source:     "auto_extracted",
	})

	outPath := filepath.Join(t.TempDir(), "export.json")
	err := store.ExportLearnings(outPath)
	if err != nil {
		t.Fatalf("ExportLearnings: %v", err)
	}

	data, _ := os.ReadFile(outPath)
	var export ExportData
	json.Unmarshal(data, &export)

	if len(export.PersonaOverrides) != 1 {
		t.Fatalf("expected 1 persona override, got %d", len(export.PersonaOverrides))
	}
	if export.PersonaOverrides[0].TraitKey != "language" {
		t.Errorf("wrong trait: %s", export.PersonaOverrides[0].TraitKey)
	}
}
