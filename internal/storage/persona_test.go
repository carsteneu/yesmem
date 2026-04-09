package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestUpsertAndGetPersonaTrait(t *testing.T) {
	s := mustOpen(t)

	trait := &models.PersonaTrait{
		UserID:        "default",
		Dimension:     "communication",
		TraitKey:      "language",
		TraitValue:    "de",
		Confidence:    0.5,
		Source:        "auto_extracted",
		EvidenceCount: 1,
	}

	if err := s.UpsertPersonaTrait(trait); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetPersonaTrait("default", "communication", "language")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TraitValue != "de" {
		t.Errorf("value: got %q, want %q", got.TraitValue, "de")
	}
	if got.Confidence != 0.5 {
		t.Errorf("confidence: got %f, want 0.5", got.Confidence)
	}
	if got.EvidenceCount != 1 {
		t.Errorf("evidence_count: got %d, want 1", got.EvidenceCount)
	}
}

func TestUpsertPersonaTraitUpdate(t *testing.T) {
	s := mustOpen(t)

	// Insert initial trait
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication",
		TraitKey: "language", TraitValue: "de",
		Confidence: 0.5, Source: "auto_extracted", EvidenceCount: 1,
	})

	// Update same trait — should overwrite, not duplicate
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication",
		TraitKey: "language", TraitValue: "de",
		Confidence: 0.6, Source: "auto_extracted", EvidenceCount: 2,
	})

	got, err := s.GetPersonaTrait("default", "communication", "language")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Confidence != 0.6 {
		t.Errorf("confidence after update: got %f, want 0.6", got.Confidence)
	}
	if got.EvidenceCount != 2 {
		t.Errorf("evidence_count after update: got %d, want 2", got.EvidenceCount)
	}
}

func TestGetActivePersonaTraits(t *testing.T) {
	s := mustOpen(t)

	traits := []models.PersonaTrait{
		{UserID: "default", Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5},
		{UserID: "default", Dimension: "communication", TraitKey: "tone", TraitValue: "direct", Confidence: 0.7, Source: "auto_extracted", EvidenceCount: 3},
		{UserID: "default", Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 4},
		{UserID: "default", Dimension: "expertise", TraitKey: "expertise.go", TraitValue: "learning", Confidence: 0.3, Source: "auto_extracted", EvidenceCount: 1},
	}
	for i := range traits {
		s.UpsertPersonaTrait(&traits[i])
	}

	// Get all active traits (confidence >= 0.4)
	active, err := s.GetActivePersonaTraits("default", 0.4)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	// expertise.go has confidence 0.3, should be excluded
	if len(active) != 3 {
		t.Fatalf("expected 3 active traits (>= 0.4), got %d", len(active))
	}

	// Get all regardless of confidence
	all, err := s.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		t.Fatalf("get all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 total traits, got %d", len(all))
	}
}

func TestGetActivePersonaTraitsWithMinEvidence(t *testing.T) {
	s := mustOpen(t)

	traits := []models.PersonaTrait{
		{UserID: "default", Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5},
		{UserID: "default", Dimension: "communication", TraitKey: "tone", TraitValue: "casual", Confidence: 0.7, Source: "auto_extracted", EvidenceCount: 3},
		{UserID: "default", Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 1},
		{UserID: "default", Dimension: "expertise", TraitKey: "expertise.go", TraitValue: "high", Confidence: 0.6, Source: "auto_extracted", EvidenceCount: 2},
		// user_override with evidence=1 should ALWAYS be included
		{UserID: "default", Dimension: "communication", TraitKey: "formality", TraitValue: "informal_du", Confidence: 1.0, Source: "user_override", EvidenceCount: 1},
		// bootstrapped with evidence=1 should ALWAYS be included
		{UserID: "default", Dimension: "learning_style", TraitKey: "wants_options", TraitValue: "true", Confidence: 0.8, Source: "bootstrapped", EvidenceCount: 1},
	}
	for i := range traits {
		s.UpsertPersonaTrait(&traits[i])
	}

	// minEvidence=3: should return language(5), tone(3), formality(user_override:1), wants_options(bootstrapped:1)
	// should exclude: autonomy(1), expertise.go(2)
	active, err := s.GetWellEvidencedTraits("default", 0.4, 3)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	found := map[string]bool{}
	for _, tr := range active {
		found[tr.TraitKey] = true
	}

	if !found["language"] {
		t.Error("language (evidence=5) should be included")
	}
	if !found["tone"] {
		t.Error("tone (evidence=3) should be included")
	}
	if !found["formality"] {
		t.Error("formality (user_override) should always be included regardless of evidence")
	}
	if !found["wants_options"] {
		t.Error("wants_options (bootstrapped) should always be included regardless of evidence")
	}
	if found["autonomy"] {
		t.Error("autonomy (evidence=1) should be excluded")
	}
	if found["expertise.go"] {
		t.Error("expertise.go (evidence=2) should be excluded at threshold=3")
	}
	if len(active) != 4 {
		t.Errorf("expected 4 traits, got %d", len(active))
	}
}

func TestGetWellEvidencedTraitsIncludesLearningScan(t *testing.T) {
	s := mustOpen(t)

	traits := []models.PersonaTrait{
		// learning_scan with evidence=1 in DB (but internally pre-thresholded, so trusted)
		{UserID: "default", Dimension: "expertise", TraitKey: "expertise.php", TraitValue: "high", Confidence: 0.6, Source: "learning_scan", EvidenceCount: 1},
		// auto_extracted with evidence=1 (should be excluded — not pre-thresholded)
		{UserID: "default", Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 1},
	}
	for i := range traits {
		s.UpsertPersonaTrait(&traits[i])
	}

	active, err := s.GetWellEvidencedTraits("default", 0.4, 3)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	found := map[string]bool{}
	for _, tr := range active {
		found[tr.TraitKey] = true
	}

	if !found["php"] {
		t.Error("learning_scan should always be included (pre-thresholded)")
	}
	if found["autonomy"] {
		t.Error("auto_extracted with evidence=1 should be excluded")
	}
}

func TestSupersedePersonaTrait(t *testing.T) {
	s := mustOpen(t)

	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "en", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 3,
	})

	// Supersede it
	if err := s.SupersedePersonaTrait("default", "communication", "language"); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	// Should not appear in active traits
	active, err := s.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected 0 active after supersede, got %d", len(active))
	}
}

func TestUserOverrideWins(t *testing.T) {
	s := mustOpen(t)

	// Auto-extracted trait
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "en", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 10,
	})

	// User override — should replace
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 1.0, Source: "user_override", EvidenceCount: 1,
	})

	got, err := s.GetPersonaTrait("default", "communication", "language")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TraitValue != "de" {
		t.Errorf("user override should win: got %q, want %q", got.TraitValue, "de")
	}
	if got.Source != "user_override" {
		t.Errorf("source should be user_override, got %q", got.Source)
	}
}

func TestSaveAndGetPersonaDirective(t *testing.T) {
	s := mustOpen(t)

	directive := &models.PersonaDirective{
		UserID:      "default",
		Directive:   "Du arbeitest mit einem erfahrenen PHP-Entwickler...\n\nHARTE REGELN:\n- Deutsch, du, locker",
		TraitsHash:  "abc123",
		GeneratedAt: time.Now().Truncate(time.Second),
		ModelUsed:   "opus",
	}

	if err := s.SavePersonaDirective(directive); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetPersonaDirective("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Directive != directive.Directive {
		t.Errorf("directive mismatch")
	}
	if got.TraitsHash != "abc123" {
		t.Errorf("hash: got %q, want %q", got.TraitsHash, "abc123")
	}
	if got.ModelUsed != "opus" {
		t.Errorf("model: got %q, want %q", got.ModelUsed, "opus")
	}
}

func TestGetPersonaDirectiveCached(t *testing.T) {
	s := mustOpen(t)

	// Save two directives — should return the newest
	s.SavePersonaDirective(&models.PersonaDirective{
		UserID: "default", Directive: "old directive",
		TraitsHash: "hash1", GeneratedAt: time.Now().Add(-time.Hour), ModelUsed: "haiku",
	})
	s.SavePersonaDirective(&models.PersonaDirective{
		UserID: "default", Directive: "new directive",
		TraitsHash: "hash2", GeneratedAt: time.Now(), ModelUsed: "opus",
	})

	got, err := s.GetPersonaDirective("default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Directive != "new directive" {
		t.Errorf("should get newest: got %q", got.Directive)
	}
}

func TestGetPersonaDirectiveEmpty(t *testing.T) {
	s := mustOpen(t)

	got, err := s.GetPersonaDirective("default")
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for no directive")
	}
}

func TestUpsertPersonaTraitNormalizesKey(t *testing.T) {
	s := mustOpen(t)

	tests := []struct {
		name      string
		dimension string
		traitKey  string
		wantDim   string
		wantKey   string
	}{
		{
			name:      "single prefix stripped",
			dimension: "communication",
			traitKey:  "communication.directness",
			wantDim:   "communication",
			wantKey:   "directness",
		},
		{
			name:      "double prefix stripped",
			dimension: "context",
			traitKey:  "context.context.yesmem_depth",
			wantDim:   "context",
			wantKey:   "yesmem_depth",
		},
		{
			name:      "deep nesting stripped to last segment",
			dimension: "context",
			traitKey:  "context.context.context.context.session_count",
			wantDim:   "context",
			wantKey:   "session_count",
		},
		{
			name:      "expertise prefix stripped",
			dimension: "expertise",
			traitKey:  "expertise.expertise.go",
			wantDim:   "expertise",
			wantKey:   "go",
		},
		{
			name:      "no prefix unchanged",
			dimension: "workflow",
			traitKey:  "autonomy",
			wantDim:   "workflow",
			wantKey:   "autonomy",
		},
		{
			name:      "dimension whitespace trimmed",
			dimension: " context",
			traitKey:  "yesmem_depth",
			wantDim:   "context",
			wantKey:   "yesmem_depth",
		},
		{
			name:      "key whitespace trimmed",
			dimension: "workflow",
			traitKey:  " autonomy ",
			wantDim:   "workflow",
			wantKey:   "autonomy",
		},
		{
			name:      "cross-dimension prefix stripped",
			dimension: "boundaries",
			traitKey:  "boundaries.time_constraint",
			wantDim:   "boundaries",
			wantKey:   "time_constraint",
		},
		{
			name:      "mixed prefix from different dimension",
			dimension: "communication",
			traitKey:  "communication.communication.wants_tool_introspection",
			wantDim:   "communication",
			wantKey:   "wants_tool_introspection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.UpsertPersonaTrait(&models.PersonaTrait{
				UserID:        "default",
				Dimension:     tt.dimension,
				TraitKey:      tt.traitKey,
				TraitValue:    "test",
				Confidence:    0.5,
				Source:        "auto_extracted",
				EvidenceCount: 1,
			})
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}

			got, err := s.GetPersonaTrait("default", tt.wantDim, tt.wantKey)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got == nil {
				t.Fatalf("trait not found with normalized key dim=%q key=%q", tt.wantDim, tt.wantKey)
			}
			if got.Dimension != tt.wantDim {
				t.Errorf("dimension: got %q, want %q", got.Dimension, tt.wantDim)
			}
			if got.TraitKey != tt.wantKey {
				t.Errorf("trait_key: got %q, want %q", got.TraitKey, tt.wantKey)
			}
		})
	}
}

func TestApplyConfidenceDelta(t *testing.T) {
	s := mustOpen(t)

	// Insert initial trait with confidence 0.5
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.5, Source: "auto_extracted", EvidenceCount: 1,
	})

	// Apply positive delta (+0.1) — should increase to 0.6, evidence to 2
	err := s.ApplyConfidenceDelta("default", "communication", "language", 0.1)
	if err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	got, _ := s.GetPersonaTrait("default", "communication", "language")
	if got.Confidence != 0.6 {
		t.Errorf("confidence after +0.1: got %f, want 0.6", got.Confidence)
	}
	if got.EvidenceCount != 2 {
		t.Errorf("evidence_count: got %d, want 2", got.EvidenceCount)
	}
}

func TestApplyConfidenceDeltaNegative(t *testing.T) {
	s := mustOpen(t)

	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.5, Source: "auto_extracted", EvidenceCount: 3,
	})

	// Apply negative delta (-0.2) — should decrease to 0.3
	err := s.ApplyConfidenceDelta("default", "communication", "language", -0.2)
	if err != nil {
		t.Fatalf("apply delta: %v", err)
	}

	got, _ := s.GetPersonaTrait("default", "communication", "language")
	if got.Confidence != 0.3 {
		t.Errorf("confidence after -0.2: got %f, want 0.3", got.Confidence)
	}
}

func TestApplyConfidenceDeltaClamped(t *testing.T) {
	s := mustOpen(t)

	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.95, Source: "auto_extracted", EvidenceCount: 10,
	})

	// Delta that would push above 1.0 — should clamp to 1.0
	s.ApplyConfidenceDelta("default", "communication", "language", 0.1)
	got, _ := s.GetPersonaTrait("default", "communication", "language")
	if got.Confidence != 1.0 {
		t.Errorf("should clamp to 1.0, got %f", got.Confidence)
	}

	// Delta that would push below 0.0 — should clamp to 0.0
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "autonomy",
		TraitValue: "high", Confidence: 0.1, Source: "auto_extracted", EvidenceCount: 1,
	})
	s.ApplyConfidenceDelta("default", "workflow", "autonomy", -0.3)
	got2, _ := s.GetPersonaTrait("default", "workflow", "autonomy")
	if got2.Confidence != 0.0 {
		t.Errorf("should clamp to 0.0, got %f", got2.Confidence)
	}
}
