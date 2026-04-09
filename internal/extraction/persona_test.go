package extraction

import (
	"encoding/json"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestPersonaExtractionSchema(t *testing.T) {
	schema := PersonaExtractionSchema()
	if schema == nil {
		t.Fatal("schema should not be nil")
	}
	// Should have updates and new_traits as required fields
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("required should be []string")
	}
	found := map[string]bool{}
	for _, r := range required {
		found[r] = true
	}
	if !found["updates"] || !found["new_traits"] {
		t.Errorf("required should contain updates and new_traits, got %v", required)
	}
}

func TestParsePersonaExtractionResponse(t *testing.T) {
	response := `{
		"updates": [
			{
				"dimension": "communication",
				"trait_key": "language",
				"trait_value": "de",
				"confidence_delta": 0.1,
				"evidence": "User schrieb alle Nachrichten auf Deutsch"
			}
		],
		"new_traits": [
			{
				"dimension": "expertise",
				"trait_key": "expertise.ansible",
				"trait_value": "intermediate",
				"confidence": 0.6,
				"evidence": "User nutzte Ansible-Playbooks"
			}
		]
	}`

	result, err := ParsePersonaExtractionResponse(response)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result.Updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(result.Updates))
	}
	if result.Updates[0].TraitKey != "language" {
		t.Errorf("trait_key: got %q, want %q", result.Updates[0].TraitKey, "language")
	}
	if result.Updates[0].ConfidenceDelta != 0.1 {
		t.Errorf("confidence_delta: got %f, want 0.1", result.Updates[0].ConfidenceDelta)
	}
	if len(result.NewTraits) != 1 {
		t.Fatalf("expected 1 new trait, got %d", len(result.NewTraits))
	}
	if result.NewTraits[0].TraitKey != "expertise.ansible" {
		t.Errorf("new trait key: got %q", result.NewTraits[0].TraitKey)
	}
}

func TestParsePersonaExtractionResponseEmpty(t *testing.T) {
	response := `{"updates": [], "new_traits": []}`
	result, err := ParsePersonaExtractionResponse(response)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result.Updates) != 0 || len(result.NewTraits) != 0 {
		t.Error("empty response should have empty arrays")
	}
}

func TestBuildPersonaExtractionPrompt(t *testing.T) {
	existingTraits := []models.PersonaTrait{
		{Dimension: "communication", TraitKey: "language", TraitValue: "de", Confidence: 0.8},
		{Dimension: "workflow", TraitKey: "autonomy", TraitValue: "high", Confidence: 0.6},
	}

	prompt := BuildPersonaExtractionPrompt(existingTraits)
	if prompt == "" {
		t.Fatal("prompt should not be empty")
	}
	// Should contain the existing traits as JSON context
	if !containsAll(prompt, "language", "de", "autonomy", "high") {
		t.Error("prompt should contain existing trait values")
	}
}

func TestPersonaExtractionSchemaValidJSON(t *testing.T) {
	schema := PersonaExtractionSchema()
	// Should be marshalable to valid JSON
	_, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("schema should marshal: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
