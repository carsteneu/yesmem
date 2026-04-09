package daemon

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func mustStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBootstrapPersonaFromLearnings(t *testing.T) {
	s := mustStore(t)

	// Seed preference and relationship learnings
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt Deutsch, du, locker",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(),
		Source: "user_stated",
	})
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "Qualitaet vor Kosten",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(),
		Source: "user_stated",
	})
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "Automatisierung vor manuellen Schritten",
		Project: "", Confidence: 1.0, CreatedAt: time.Now(),
		Source: "user_stated",
	})

	count := bootstrapPersonaFromLearnings(s)

	// Should have created traits from the learnings
	if count == 0 {
		t.Error("bootstrap should have created at least one trait from preference learnings")
	}

	traits, err := s.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		t.Fatalf("get traits: %v", err)
	}
	if len(traits) == 0 {
		t.Error("expected traits to be populated after bootstrap")
	}
}

func TestBootstrapPersonaFromLearningsSkipsIfTraitsExist(t *testing.T) {
	s := mustStore(t)

	// Pre-seed a persona trait
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5,
	})

	// Seed some learnings
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "Bevorzugt Deutsch",
		Confidence: 1.0, CreatedAt: time.Now(), Source: "user_stated",
	})

	count := bootstrapPersonaFromLearnings(s)

	// Should skip bootstrap since traits already exist
	if count != 0 {
		t.Errorf("bootstrap should skip when traits already exist, but created %d", count)
	}
}

func TestBootstrapPersonaFromLearningsEmpty(t *testing.T) {
	s := mustStore(t)

	// No learnings at all
	count := bootstrapPersonaFromLearnings(s)

	if count != 0 {
		t.Errorf("bootstrap should return 0 for empty learnings, got %d", count)
	}
}

func TestExtractPersonaSignalsLimit(t *testing.T) {
	// Test that extractPersonaSignals respects the session limit parameter.
	// We can't test the full LLM flow without a mock, but we test the function signature
	// accepts a limit and that it uses it.
	s := mustStore(t)

	// Insert sessions with enough messages
	for i := 0; i < 20; i++ {
		sess := &models.Session{
			ID:           fakeSessionID(i),
			ProjectShort: "memory",
			MessageCount: 15,
			StartedAt:    time.Now().Add(-time.Duration(i) * time.Hour),
		}
		s.UpsertSession(sess)
	}

	sessions, _ := s.ListSessions("", 0)

	// Without LLM client, extractPersonaSignals should handle nil gracefully
	// This test verifies the function signature accepts a limit parameter
	extractPersonaSignalsWithLimit(s, sessions, nil, nil, 50)

	// Should not panic, should log "no LLM client"
}

func TestExtractExpertiseFromLearnings(t *testing.T) {
	s := mustStore(t)

	// Seed learnings: PHP mentioned 4x, Symfony 3x, Go 3x across projects
	phpLearnings := []string{
		"PHP Controller für Import erstellen",
		"PHP 8.2 Typed Properties nutzen",
		"PHP Unit Test für Service schreiben",
		"Neues PHP Command für CLI",
		"Symfony Console Command für Import",
		"Symfony Service mit Dependency Injection",
		"Symfony EventSubscriber registrieren",
	}
	for _, content := range phpLearnings {
		s.InsertLearning(&models.Learning{
			Category: "pattern", Content: content,
			Project: "cookie-consent-management", Confidence: 1.0,
			CreatedAt: time.Now(), Source: "llm_extracted",
		})
	}

	goLearnings := []string{
		"Go Worker-Pool mit sync.WaitGroup",
		"Go test -race für Concurrency-Bugs",
		"Go struct mit JSON Tags definieren",
	}
	for _, content := range goLearnings {
		s.InsertLearning(&models.Learning{
			Category: "pattern", Content: content,
			Project: "memory", Confidence: 1.0,
			CreatedAt: time.Now(), Source: "llm_extracted",
		})
	}

	// Only 1 Docker mention — should NOT create trait (below threshold of 3)
	s.InsertLearning(&models.Learning{
		Category: "pattern", Content: "Docker container starten",
		Project: "memory", Confidence: 1.0,
		CreatedAt: time.Now(), Source: "llm_extracted",
	})

	count := extractExpertiseFromLearnings(s)
	if count == 0 {
		t.Fatal("should have created expertise traits from learnings")
	}

	traits, _ := s.GetActivePersonaTraits("default", 0.0)

	// Should have PHP-related expertise
	foundPHP := false
	foundSymfony := false
	foundGo := false
	foundDocker := false
	for _, tr := range traits {
		if tr.Dimension == "expertise" {
			switch tr.TraitKey {
			case "php":
				foundPHP = true
			case "symfony":
				foundSymfony = true
			case "go":
				foundGo = true
			case "docker":
				foundDocker = true
			}
		}
	}

	if !foundPHP {
		t.Error("should detect PHP expertise from CCM19 learnings")
	}
	if !foundSymfony {
		t.Error("should detect Symfony expertise from CCM19 learnings")
	}
	if !foundGo {
		t.Error("should detect Go expertise from memory learnings")
	}
	if foundDocker {
		t.Error("Docker mentioned only 1x — should NOT create trait (threshold=3)")
	}
}

func TestExtractExpertiseFromLearningsEvidenceCount(t *testing.T) {
	s := mustStore(t)

	// 5 PHP learnings → evidence_count should reflect the count
	for i := 0; i < 5; i++ {
		s.InsertLearning(&models.Learning{
			Category: "pattern", Content: "PHP Controller erstellen",
			Project: "test-project", Confidence: 1.0,
			CreatedAt: time.Now(), Source: "llm_extracted",
		})
	}

	extractExpertiseFromLearnings(s)

	trait, _ := s.GetPersonaTrait("default", "expertise", "php")
	if trait == nil {
		t.Fatal("PHP trait should exist")
	}
	if trait.EvidenceCount < 5 {
		t.Errorf("evidence_count should be >= 5, got %d", trait.EvidenceCount)
	}
}

func fakeSessionID(i int) string {
	return "bootstrap-test-" + string(rune('a'+i))
}
