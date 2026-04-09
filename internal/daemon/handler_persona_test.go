package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func mustHandler(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandler(s, nil)
	return h, s
}

func TestHandleSetPersona(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "language",
			"value":     "de",
			"dimension": "communication",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Verify trait was stored with user_override source and confidence 1.0
	trait, err := s.GetPersonaTrait("default", "communication", "language")
	if err != nil {
		t.Fatalf("get trait: %v", err)
	}
	if trait == nil {
		t.Fatal("trait should exist after set_persona")
	}
	if trait.TraitValue != "de" {
		t.Errorf("value: got %q, want %q", trait.TraitValue, "de")
	}
	if trait.Confidence != 1.0 {
		t.Errorf("confidence: got %f, want 1.0 (user_override)", trait.Confidence)
	}
	if trait.Source != "user_override" {
		t.Errorf("source: got %q, want %q", trait.Source, "user_override")
	}
}

func TestHandleSetPersonaOverridesExisting(t *testing.T) {
	h, s := mustHandler(t)

	// Insert auto-extracted trait first
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "en", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 5,
	})

	// User overrides via MCP
	resp := h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "language",
			"value":     "de",
			"dimension": "communication",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	trait, _ := s.GetPersonaTrait("default", "communication", "language")
	if trait.TraitValue != "de" {
		t.Errorf("override should win: got %q, want %q", trait.TraitValue, "de")
	}
	if trait.Source != "user_override" {
		t.Errorf("source should be user_override after override, got %q", trait.Source)
	}
}

func TestHandleSetPersonaAutoDetectDimension(t *testing.T) {
	h, s := mustHandler(t)

	// No dimension provided — should auto-detect from trait_key
	resp := h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "language",
			"value":     "de",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Should have auto-detected "communication" dimension
	trait, _ := s.GetPersonaTrait("default", "communication", "language")
	if trait == nil {
		t.Fatal("trait should exist with auto-detected dimension")
	}
	if trait.Dimension != "communication" {
		t.Errorf("dimension: got %q, want %q", trait.Dimension, "communication")
	}
}

func TestHandleSetPersonaMissingParams(t *testing.T) {
	h, _ := mustHandler(t)

	// Missing trait_key
	resp := h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"value": "de",
		},
	})
	if resp.Error == "" {
		t.Error("should error when trait_key missing")
	}
	if resp.Error == "unknown method: set_persona" {
		t.Skip("set_persona not implemented yet")
	}

	// Missing value
	resp = h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "language",
		},
	})
	if resp.Error == "" {
		t.Error("should error when value missing")
	}
}

func TestHandleGetPersona(t *testing.T) {
	h, s := mustHandler(t)

	// Seed some traits
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5,
	})
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "autonomy",
		TraitValue: "high", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 3,
	})

	// Save a directive
	s.SavePersonaDirective(&models.PersonaDirective{
		UserID: "default", Directive: "Du arbeitest mit...",
		TraitsHash: "abc123", GeneratedAt: time.Now(), ModelUsed: "opus",
	})

	resp := h.Handle(Request{Method: "get_persona", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Parse response
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should have traits
	traits, ok := result["traits"]
	if !ok {
		t.Fatal("response should contain 'traits'")
	}
	traitList, ok := traits.([]any)
	if !ok || len(traitList) != 2 {
		t.Errorf("expected 2 traits, got %v", traits)
	}

	// Should have directive
	directive, ok := result["directive"].(string)
	if !ok || directive != "Du arbeitest mit..." {
		t.Errorf("directive: got %q, want %q", directive, "Du arbeitest mit...")
	}
}

func TestAutoDetectDimensionNewDimensions(t *testing.T) {
	tests := []struct {
		traitKey string
		want     string
	}{
		// Existing dimensions still work
		{"language", "communication"},
		{"tone", "communication"},
		{"autonomy", "workflow"},
		{"expertise.go", "expertise"},

		// NEW: context dimension
		{"os", "context"},
		{"shell", "context"},
		{"editor", "context"},
		{"go_version", "context"},
		{"default_branch", "context"},
		{"context.docker_version", "context"},

		// NEW: boundaries dimension
		{"never_auto_commit", "boundaries"},
		{"no_emoji_unless_asked", "boundaries"},
		{"boundary.no_force_push", "boundaries"},

		// NEW: learning_style dimension
		{"prefers_examples", "learning_style"},
		{"wants_tradeoff_tables", "learning_style"},
		{"learning.visual_thinker", "learning_style"},

		// Unknown stays general
		{"something_random", "general"},
	}

	for _, tt := range tests {
		t.Run(tt.traitKey, func(t *testing.T) {
			got := autoDetectDimension(tt.traitKey)
			if got != tt.want {
				t.Errorf("autoDetectDimension(%q) = %q, want %q", tt.traitKey, got, tt.want)
			}
		})
	}
}

func TestHandleSetPersonaNewDimensions(t *testing.T) {
	h, s := mustHandler(t)

	// Set a context trait
	resp := h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "os",
			"value":     "linux",
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	trait, _ := s.GetPersonaTrait("default", "context", "os")
	if trait == nil {
		t.Fatal("context trait should exist after auto-detect")
	}
	if trait.Dimension != "context" {
		t.Errorf("dimension: got %q, want %q", trait.Dimension, "context")
	}

	// Set a boundary trait
	resp = h.Handle(Request{
		Method: "set_persona",
		Params: map[string]any{
			"trait_key": "never_auto_commit",
			"value":     "true",
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	trait, _ = s.GetPersonaTrait("default", "boundaries", "never_auto_commit")
	if trait == nil {
		t.Fatal("boundary trait should exist after auto-detect")
	}
}

func TestHandleGetPersonaEmpty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{Method: "get_persona", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Should have empty traits
	traits := result["traits"]
	if traits != nil {
		if traitList, ok := traits.([]any); ok && len(traitList) > 0 {
			t.Errorf("expected empty traits, got %d", len(traitList))
		}
	}

	// Directive should be empty
	directive, _ := result["directive"].(string)
	if directive != "" {
		t.Errorf("directive should be empty, got %q", directive)
	}
}
