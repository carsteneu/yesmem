package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestSynthesizeUserProfileHashMatchSkipsRegeneration(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed learnings so there's input data
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt kurze Antworten",
		Source: "user_stated", CreatedAt: time.Now(), ModelUsed: "opus",
	})

	// First call: generates profile and saves hash
	mock := &capturingMockClient{
		completeResp: "Profil text",
	}
	synthesizeUserProfile(s, mock)
	if mock.lastUserMessage == "" {
		t.Fatal("first call should invoke LLM")
	}

	// Second call with same input: hash matches, should skip
	mock2 := &capturingMockClient{
		completeResp: "Should not be called",
	}
	synthesizeUserProfile(s, mock2)
	if mock2.lastUserMessage != "" {
		t.Error("LLM should not be called when input hash is unchanged")
	}
}

func TestSynthesizeUserProfileHashSkipsUnchanged(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed learnings
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt kurze Antworten",
		Source: "user_stated", CreatedAt: time.Now(), ModelUsed: "opus",
	})
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "expertise", TraitKey: "go",
		TraitValue: "high", Confidence: 0.8, Source: "learning_scan", EvidenceCount: 10,
	})

	// Compute the expected hash by calling the same logic
	// First call: generates the profile
	mock := &capturingMockClient{
		completeResp: "Ein erfahrener Go-Entwickler der kurze Antworten bevorzugt.",
	}
	synthesizeUserProfile(s, mock)

	if mock.lastUserMessage == "" {
		t.Fatal("first call should invoke LLM")
	}

	// Second call with same input data: hash should match, skip LLM
	mock2 := &capturingMockClient{
		completeResp: "Should not be called",
	}
	synthesizeUserProfile(s, mock2)

	if mock2.lastUserMessage != "" {
		t.Error("LLM should not be called when input hash is unchanged")
	}
}

func TestSynthesizeUserProfileGeneratesOnStaleProfile(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed learnings with user_stated/agreed_upon sources
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt kurze Antworten",
		Source: "user_stated", CreatedAt: time.Now(), ModelUsed: "opus",
	})
	s.InsertLearning(&models.Learning{
		Category: "explicit_teaching", Content: "Bash commands immer einzeilig",
		Source: "user_stated", CreatedAt: time.Now(), ModelUsed: "opus",
	})
	s.InsertLearning(&models.Learning{
		Category: "pattern", Content: "TDD workflow: tests first",
		Source: "agreed_upon", CreatedAt: time.Now(), ModelUsed: "opus",
	})

	// Expertise traits
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "expertise", TraitKey: "go",
		TraitValue: "high", Confidence: 0.8, Source: "learning_scan", EvidenceCount: 10,
	})

	// Save an existing profile with DIFFERENT hash, generated >72h ago (simulates changed input data)
	s.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "user_profile",
		Directive:   "Old profile",
		TraitsHash:  "oldhash_that_wont_match",
		GeneratedAt: time.Now().Add(-73 * time.Hour),
		ModelUsed:   "mock-model",
	})

	mock := &capturingMockClient{
		completeResp: "Ein pragmatischer Entwickler mit Go-Expertise.",
	}

	synthesizeUserProfile(s, mock)

	// LLM should have been called
	if mock.lastUserMessage == "" {
		t.Fatal("LLM should be called when profile is stale and hash changed")
	}

	// System prompt should contain the analyst instruction
	if !strings.Contains(mock.lastSystem, "Analyst") {
		t.Error("system prompt should contain Analyst instruction")
	}

	// User prompt should contain the learning content
	if !strings.Contains(mock.lastUserMessage, "kurze Antworten") {
		t.Error("user prompt should contain preference learning content")
	}

	// User prompt should contain expertise traits
	if !strings.Contains(mock.lastUserMessage, "go") {
		t.Error("user prompt should contain expertise trait")
	}

	// Verify it was saved
	saved, err := s.GetPersonaDirective("user_profile")
	if err != nil || saved == nil {
		t.Fatal("profile should be saved after generation")
	}
	if saved.Directive != "Ein pragmatischer Entwickler mit Go-Expertise." {
		t.Errorf("saved directive = %q, want the mock response", saved.Directive)
	}
}

func TestSynthesizeUserProfileNilClient(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Should not panic
	synthesizeUserProfile(s, nil)
}

func TestSynthesizeUserProfileTimeGuardSkipsRecentWithChangedHash(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed learnings
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt kurze Antworten",
		Source: "user_stated", CreatedAt: time.Now(), ModelUsed: "opus",
	})

	// Save a recent profile (1h ago) with a DIFFERENT hash
	s.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "user_profile",
		Directive:   "Recent profile",
		TraitsHash:  "different_hash",
		GeneratedAt: time.Now().Add(-1 * time.Hour),
		ModelUsed:   "mock-model",
	})

	mock := &capturingMockClient{
		completeResp: "Should not be called",
	}
	synthesizeUserProfile(s, mock)

	// Hash changed but profile is <24h old — should NOT regenerate
	if mock.lastUserMessage != "" {
		t.Error("LLM should not be called when profile is less than 24h old, even with changed hash")
	}
}

func TestSynthesizeUserProfileNoInputData(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	mock := &capturingMockClient{
		completeResp: "Should not be called",
	}

	// No learnings, no traits => should bail early
	synthesizeUserProfile(s, mock)

	if mock.lastUserMessage != "" {
		t.Error("LLM should not be called when there is no input data")
	}
}
