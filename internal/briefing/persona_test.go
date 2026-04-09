package briefing

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestTraitsHash(t *testing.T) {
	traits := []models.PersonaTrait{
		{Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.9},
		{Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.8},
	}

	hash1 := TraitsHash(traits)
	if hash1 == "" {
		t.Fatal("hash should not be empty")
	}

	// Same traits → same hash
	hash2 := TraitsHash(traits)
	if hash1 != hash2 {
		t.Errorf("same traits should produce same hash: %q vs %q", hash1, hash2)
	}

	// Different traits → different hash
	traits[0].Confidence = 0.5
	hash3 := TraitsHash(traits)
	if hash1 == hash3 {
		t.Error("different traits should produce different hash")
	}
}

func TestTraitsHashEmpty(t *testing.T) {
	hash := TraitsHash(nil)
	if hash == "" {
		t.Fatal("empty traits should still produce a hash")
	}
}

func TestBuildSynthesisPrompt(t *testing.T) {
	traits := []models.PersonaTrait{
		{Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.9},
		{Dimension: "communication", TraitKey: "tone", TraitValue: "direct", Confidence: 0.7},
		{Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.8},
		{Dimension: "expertise", TraitKey: "expertise.go", TraitValue: "learning", Confidence: 0.5},
	}

	prompt := BuildSynthesisPrompt(traits, 285, nil)
	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	// Should contain trait data
	if !containsStr(prompt, "language") || !containsStr(prompt, "de") {
		t.Error("prompt should contain trait keys and values")
	}
	// Should contain session count
	if !containsStr(prompt, "285") {
		t.Error("prompt should contain session count")
	}
}

func TestFormatPersonaDirective(t *testing.T) {
	directive := "Du arbeitest mit einem erfahrenen Entwickler.\n\nHARTE REGELN:\n- Deutsch, du, locker"

	formatted := FormatPersonaDirective(directive)
	if formatted == "" {
		t.Fatal("should not be empty")
	}
	// Should contain the content
	if !containsStr(formatted, "erfahrenen Entwickler") {
		t.Error("should contain the directive content")
	}
}

func TestFormatPersonaDirectiveEmpty(t *testing.T) {
	formatted := FormatPersonaDirective("")
	if formatted != "" {
		t.Error("empty directive should return empty string")
	}
}

func TestFormatPersonaDirectiveHeader(t *testing.T) {
	directive := "Du arbeitest mit einem pragmatischen Entwickler."
	formatted := FormatPersonaDirective(directive)

	// Should NOT start with heavy header
	if containsStr(formatted, "PERSONA DIRECTIVE") {
		t.Error("should NOT contain PERSONA DIRECTIVE header anymore")
	}
	// Should contain the directive content
	if !containsStr(formatted, directive) {
		t.Error("should contain the directive content")
	}
}

func TestBuildSynthesisPromptWithPivots(t *testing.T) {
	traits := []models.PersonaTrait{
		{Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.9},
	}
	pivots := []models.Learning{
		{Content: "User sagte 'immer optimal' — erkannt als tiefe Praeferenz"},
		{Content: "User fragte 'wie schaffen wir dass du yesmem oefter nutzt'"},
	}

	prompt := BuildSynthesisPrompt(traits, 100, pivots)
	if !containsStr(prompt, "Key moments") {
		t.Error("prompt should contain Key moments section")
	}
	if !containsStr(prompt, "immer optimal") {
		t.Error("prompt should contain first pivot")
	}
	if !containsStr(prompt, "yesmem oefter nutzt") {
		t.Error("prompt should contain second pivot")
	}
}

func TestPersonaSynthesisSystemPromptContainsRelationshipAnchors(t *testing.T) {
	prompt := PersonaSynthesisSystemPrompt
	if !strings.Contains(prompt, "RELATIONSHIP ANCHOR") && !strings.Contains(prompt, "key moments") {
		t.Error("synthesis prompt should mention relationship anchors")
	}
	if strings.Contains(prompt, "Kollegenbeschreibung") {
		t.Error("synthesis prompt should no longer mention Kollegenbeschreibung")
	}
}

// containsStr already defined in i18n_test.go — reuse it
