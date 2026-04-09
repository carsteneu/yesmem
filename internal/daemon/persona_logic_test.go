package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

func TestLearningToTraits_Language(t *testing.T) {
	l := models.Learning{Content: "User spricht deutsch und bevorzugt du"}
	traits := learningToTraits(l)

	found := false
	for _, tr := range traits {
		if tr.TraitKey == "language" && tr.TraitValue == "de" {
			found = true
		}
	}
	if !found {
		t.Error("expected language=de trait for 'deutsch' keyword")
	}
}

func TestLearningToTraits_Formality(t *testing.T) {
	l := models.Learning{Content: "User bevorzugt du statt Sie"}
	traits := learningToTraits(l)

	found := false
	for _, tr := range traits {
		if tr.TraitKey == "formality" && tr.TraitValue == "informal_du" {
			found = true
		}
	}
	if !found {
		t.Error("expected formality=informal_du")
	}
}

func TestLearningToTraits_Automation(t *testing.T) {
	l := models.Learning{Content: "Automatisierung ist wichtig für den Workflow"}
	traits := learningToTraits(l)

	found := false
	for _, tr := range traits {
		if tr.TraitKey == "automation_preference" {
			found = true
		}
	}
	if !found {
		t.Error("expected automation_preference trait")
	}
}

func TestLearningToTraits_Boundaries(t *testing.T) {
	l := models.Learning{Content: "Nie automatisch committen, kein emoji bitte"}
	traits := learningToTraits(l)

	keys := map[string]bool{}
	for _, tr := range traits {
		keys[tr.TraitKey] = true
	}
	if !keys["never_auto_commit"] {
		t.Error("expected never_auto_commit trait")
	}
	if !keys["no_emoji_unless_asked"] {
		t.Error("expected no_emoji_unless_asked trait")
	}
}

func TestLearningToTraits_NoMatch(t *testing.T) {
	l := models.Learning{Content: "Fixed a bug in the proxy handler"}
	traits := learningToTraits(l)
	if len(traits) != 0 {
		t.Errorf("expected no traits for unmatched content, got %d", len(traits))
	}
}

func TestLearningToTraits_QualityVsCost(t *testing.T) {
	l := models.Learning{Content: "Qualität vor Kosten, immer"}
	traits := learningToTraits(l)

	found := false
	for _, tr := range traits {
		if tr.TraitKey == "quality_vs_cost" && tr.TraitValue == "quality_first" {
			found = true
		}
	}
	if !found {
		t.Error("expected quality_vs_cost=quality_first")
	}
}

func TestBootstrapPersonaFromLearnings_Integration(t *testing.T) {
	_, s := mustHandler(t)

	// Insert a learning with persona-relevant content
	_, _ = s.InsertLearning(&models.Learning{
		Content:  "User spricht deutsch und bevorzugt du",
		Category: "preference",
		Source:   "user_stated",
	})

	count := bootstrapPersonaFromLearnings(s)
	if count == 0 {
		t.Error("expected at least 1 trait bootstrapped")
	}
}

func TestBootstrapPersonaFromLearningsForce(t *testing.T) {
	_, s := mustHandler(t)

	_, _ = s.InsertLearning(&models.Learning{
		Content:  "deutsch casual tone",
		Category: "preference",
		Source:   "user_stated",
	})

	count1 := bootstrapPersonaFromLearningsForce(s)
	// Force should always process, even if already bootstrapped
	count2 := bootstrapPersonaFromLearningsForce(s)
	if count1 == 0 {
		t.Error("first bootstrap should produce traits")
	}
	_ = count2 // second run may or may not produce new traits (upsert)
}

func TestCountActiveTraits(t *testing.T) {
	_, s := mustHandler(t)
	count := countActiveTraits(s)
	if count != 0 {
		t.Errorf("expected 0 traits on fresh DB, got %d", count)
	}
}

func TestResetPersona(t *testing.T) {
	_, s := mustHandler(t)
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "test", TraitKey: "x", TraitValue: "y",
		Confidence: 1.0, Source: "test",
	})
	ResetPersona(s)
	count := countActiveTraits(s)
	if count != 0 {
		t.Errorf("expected 0 after reset, got %d", count)
	}
}

func TestUserProfileInputHash(t *testing.T) {
	learnings := []models.Learning{{ID: 1, Content: "a"}, {ID: 2, Content: "b"}}
	traits := []models.PersonaTrait{{TraitKey: "x", TraitValue: "y"}}

	h1 := userProfileInputHash(learnings, traits)
	h2 := userProfileInputHash(learnings, traits)
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}

	// Different input → different hash
	h3 := userProfileInputHash(learnings[:1], traits)
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
}

func TestLoadTopPivotMoments_Empty(t *testing.T) {
	_, s := mustHandler(t)
	moments := loadTopPivotMoments(s, 5)
	if len(moments) != 0 {
		t.Errorf("expected empty on fresh DB, got %d", len(moments))
	}
}

func TestApplyPersonaUpdatesRejectsUnknownKey(t *testing.T) {
	_, s := mustHandler(t)
	result := &extraction.PersonaExtractionResult{
		NewTraits: []extraction.PersonaNewTrait{
			{Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.5, Evidence: "ok"},
			{Dimension: "workflow", TraitKey: "invented_key_xyz", TraitValue: "yes", Confidence: 0.5, Evidence: "bad"},
		},
	}
	applyPersonaUpdates(s, result, "test")

	trait, _ := s.GetPersonaTrait("default", "workflow", "autonomy")
	if trait == nil {
		t.Error("valid key 'autonomy' should be stored")
	}

	bad, _ := s.GetPersonaTrait("default", "workflow", "invented_key_xyz")
	if bad != nil {
		t.Error("unknown key 'invented_key_xyz' should be rejected")
	}
}

func TestApplyPersonaUpdatesAllowsDynamicExpertise(t *testing.T) {
	_, s := mustHandler(t)
	result := &extraction.PersonaExtractionResult{
		NewTraits: []extraction.PersonaNewTrait{
			{Dimension: "expertise", TraitKey: "rust", TraitValue: "beginner", Confidence: 0.5, Evidence: "ok"},
		},
	}
	applyPersonaUpdates(s, result, "test")

	trait, _ := s.GetPersonaTrait("default", "expertise", "rust")
	if trait == nil {
		t.Error("dynamic expertise key 'rust' should be allowed")
	}
}

func TestApplyPersonaUpdatesRejectsUnknownKeyInUpdates(t *testing.T) {
	_, s := mustHandler(t)

	// Seed a valid trait first
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "autonomy",
		TraitValue: "medium", Confidence: 0.5, Source: "auto_extracted", EvidenceCount: 1,
	})

	result := &extraction.PersonaExtractionResult{
		Updates: []extraction.PersonaTraitUpdate{
			{Dimension: "workflow", TraitKey: "autonomy", TraitValue: "medium", ConfidenceDelta: 0.1, Evidence: "ok"},
			{Dimension: "workflow", TraitKey: "bogus_update_key", TraitValue: "yes", ConfidenceDelta: 0.1, Evidence: "bad"},
		},
	}
	applyPersonaUpdates(s, result, "test")

	// Valid update should have increased confidence
	trait, _ := s.GetPersonaTrait("default", "workflow", "autonomy")
	if trait == nil || trait.Confidence <= 0.5 {
		t.Error("valid update should have increased confidence")
	}

	// Invalid key should not exist
	bad, _ := s.GetPersonaTrait("default", "workflow", "bogus_update_key")
	if bad != nil {
		t.Error("unknown key 'bogus_update_key' should be rejected in updates")
	}
}

// dedupSpyProvider captures embed texts and returns controlled vectors.
type dedupSpyProvider struct {
	captured []string
	dims     int
}

func (p *dedupSpyProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	p.captured = texts
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		v := make([]float32, p.dims)
		for j := range v {
			v[j] = 1.0 / float32(p.dims) // identical unit vectors = cosine 1.0
		}
		vecs[i] = v
	}
	return vecs, nil
}
func (p *dedupSpyProvider) Dimensions() int { return p.dims }
func (p *dedupSpyProvider) Enabled() bool   { return true }
func (p *dedupSpyProvider) Close() error    { return nil }

func TestDedupPersonaTraitsEmbedTextExcludesDimension(t *testing.T) {
	_, s := mustHandler(t)

	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "pragmatic_not_sloppy",
		TraitValue: "true", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5,
	})
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "pragmatism_principles",
		TraitValue: "no_shortcuts", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 3,
	})

	spy := &dedupSpyProvider{dims: 8}
	dedupPersonaTraits(s, spy)

	// Verify embed texts don't contain dimension
	for _, text := range spy.captured {
		if strings.Contains(text, "workflow ") {
			t.Errorf("embed text should NOT contain dimension, got: %q", text)
		}
	}

	// With identical vectors (similarity 1.0 > 0.75), one trait should be superseded
	traits, _ := s.GetActivePersonaTraits("default", 0.0)
	count := 0
	for _, tr := range traits {
		if tr.Dimension == "workflow" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 workflow trait after dedup, got %d", count)
	}
}

func TestDecayContextTraits(t *testing.T) {
	_, s := mustHandler(t)

	// Old context trait — 90 days ago (use RFC3339 format for parseTime compatibility)
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "context", TraitKey: "active_branch",
		TraitValue: "old_branch", Confidence: 0.6, Source: "auto_extracted", EvidenceCount: 1,
	})
	oldDate := time.Now().AddDate(0, 0, -90).Format(time.RFC3339)
	s.DB().Exec(`UPDATE persona_traits SET updated_at = ? WHERE trait_key = 'active_branch'`, oldDate)

	// Fresh context trait — just now
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "context", TraitKey: "os",
		TraitValue: "linux", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 5,
	})

	// Non-context trait — old but should be untouched
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "expertise", TraitKey: "go",
		TraitValue: "high", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 10,
	})
	s.DB().Exec(`UPDATE persona_traits SET updated_at = ? WHERE trait_key = 'go'`, time.Now().AddDate(0, 0, -120).Format(time.RFC3339))

	decayed := DecayContextTraits(s)
	if decayed == 0 {
		t.Error("expected at least 1 context trait to be decayed")
	}

	// Old context trait should be superseded (90 days = 3 periods * 0.1 = 0.3 decay, 0.6-0.3=0.3 > 0.2, so confidence lowered)
	trait, _ := s.GetPersonaTrait("default", "context", "active_branch")
	if trait == nil {
		t.Fatal("active_branch trait should still exist")
	}
	if trait.Confidence >= 0.6 {
		t.Errorf("old context trait confidence should have decayed from 0.6, got %.2f", trait.Confidence)
	}

	// Fresh trait unaffected
	osTrait, _ := s.GetPersonaTrait("default", "context", "os")
	if osTrait == nil || osTrait.Confidence != 0.8 {
		t.Errorf("fresh context trait should be unaffected, got conf=%.2f", osTrait.Confidence)
	}

	// Expertise trait unaffected despite being old
	goTrait, _ := s.GetPersonaTrait("default", "expertise", "go")
	if goTrait == nil || goTrait.Confidence != 0.9 {
		t.Errorf("non-context trait should be unaffected, got conf=%.2f", goTrait.Confidence)
	}
}

func TestDecayContextTraitsSupersedesVeryOld(t *testing.T) {
	_, s := mustHandler(t)

	// Very old context trait — confidence will drop below 0.2
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "context", TraitKey: "session_count",
		TraitValue: "500+", Confidence: 0.5, Source: "auto_extracted", EvidenceCount: 1,
	})
	s.DB().Exec(`UPDATE persona_traits SET updated_at = ? WHERE trait_key = 'session_count'`, time.Now().AddDate(0, 0, -180).Format(time.RFC3339))

	DecayContextTraits(s)

	// 180 days = 6 periods * 0.1 = 0.6 decay. 0.5 - 0.6 = -0.1 < 0.2 → superseded
	trait, _ := s.GetPersonaTrait("default", "context", "session_count")
	if trait != nil && !trait.Superseded {
		t.Errorf("very old context trait should be superseded, got conf=%.2f superseded=%v", trait.Confidence, trait.Superseded)
	}
}
