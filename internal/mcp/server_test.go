package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentClientModelEmpty(t *testing.T) {
	for _, key := range []string{"CODEX_MODEL", "OPENAI_MODEL", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "MODEL"} {
		t.Setenv(key, "")
	}

	if got := currentClientModel(); got != "" {
		t.Fatalf("currentClientModel(): got %q, want empty", got)
	}
}

func TestCurrentClientModelPriority(t *testing.T) {
	t.Setenv("MODEL", "generic")
	t.Setenv("CLAUDE_MODEL", "claude-sonnet")
	t.Setenv("ANTHROPIC_MODEL", "anthropic-opus")
	t.Setenv("OPENAI_MODEL", "gpt-5")
	t.Setenv("CODEX_MODEL", " gpt-5.4-mini ")

	if got := currentClientModel(); got != "gpt-5.4-mini" {
		t.Fatalf("currentClientModel(): got %q, want %q", got, "gpt-5.4-mini")
	}
}

func TestFormatRememberIncludesModel(t *testing.T) {
	raw := []byte(`{
		"id": 42,
		"category": "decision",
		"project": "yesmem",
		"content": "Model provenance should be visible",
		"model_used": "gpt-5.4-mini"
	}`)

	got := formatRemember(raw)

	for _, want := range []string{
		"Learning #42 saved",
		"Category:   decision",
		"Project:    yesmem",
		"Model:      gpt-5.4-mini",
		"Content:    Model provenance should be visible",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatRemember() missing %q in output:\n%s", want, got)
		}
	}
}

func TestFormatRememberOmitsEmptyModel(t *testing.T) {
	raw := []byte(`{
		"id": 7,
		"category": "gotcha",
		"content": "No model line expected"
	}`)

	got := formatRemember(raw)

	if strings.Contains(got, "Model:") {
		t.Fatalf("formatRemember() should omit empty model line, got:\n%s", got)
	}
}

func TestFormatPersonaGroupsByDimension(t *testing.T) {
	input := json.RawMessage(`{
		"traits": [
			{"dimension":"communication","trait_key":"language","trait_value":"de","confidence":0.95,"source":"auto_extracted"},
			{"dimension":"communication","trait_key":"tone","trait_value":"direct","confidence":0.80,"source":"auto_extracted"},
			{"dimension":"expertise","trait_key":"go","trait_value":"high","confidence":1.0,"source":"learning_scan"}
		],
		"directive": "Test directive",
		"last_updated": "2026-04-03T12:00:00Z"
	}`)
	result := formatPersona(input)
	for _, want := range []string{
		"Directive: Test directive",
		"[communication]",
		"language: de",
		"tone: direct",
		"[expertise]",
		"go: high",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in output:\n%s", want, result)
		}
	}
}

func TestFormatPersonaDirectiveOnly(t *testing.T) {
	input := json.RawMessage(`{"directive": "Only directive", "traits": []}`)
	result := formatPersona(input)
	if !strings.Contains(result, "Directive: Only directive") {
		t.Errorf("missing directive in output: %s", result)
	}
}
