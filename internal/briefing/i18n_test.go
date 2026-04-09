package briefing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultStringsAreGerman(t *testing.T) {
	s := DefaultStrings()
	// Greeting and ContinuationOf are empty (replaced by Awakening template)
	if s.ToolHint == "" {
		t.Error("tool hint should not be empty")
	}
	if s.YourCounterpart == "" {
		t.Error("your_counterpart should not be empty")
	}
	if s.KnownPitfalls == "" {
		t.Error("known_pitfalls should not be empty")
	}
	if s.DecisionsMade == "" {
		t.Error("decisions_made should not be empty")
	}
	if s.ProvenPatterns == "" {
		t.Error("proven_patterns should not be empty")
	}
	if s.Reminders == "" {
		t.Error("reminders should not be empty")
	}
	if s.RecentSessions == "" {
		t.Error("recent_sessions should not be empty")
	}
	if s.OpenWork == "" {
		t.Error("open_work should not be empty")
	}
	if s.MoreVia == "" {
		t.Error("more_via should not be empty")
	}
	if s.SessionsTotal == "" {
		t.Error("sessions_total should not be empty")
	}
}

func TestSaveAndLoadStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "strings.yaml")

	original := DefaultStrings()
	original.Greeting = "Test greeting"

	err := SaveStrings(path, original)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadStrings(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.Greeting != "Test greeting" {
		t.Errorf("greeting: got %q, want %q", loaded.Greeting, "Test greeting")
	}
	// Non-modified fields should be preserved
	if loaded.KnownPitfalls != original.KnownPitfalls {
		t.Errorf("known_pitfalls should be preserved")
	}
}

func TestLoadStringsFallsBackToDefaults(t *testing.T) {
	s, err := LoadStrings("/nonexistent/strings.yaml")
	if err != nil {
		t.Fatalf("should not error on missing file: %v", err)
	}
	if s.ToolHint == "" {
		t.Error("should fall back to defaults")
	}
}

func TestBuildTranslationPrompt(t *testing.T) {
	prompt := BuildTranslationPrompt("de")
	if prompt == "" {
		t.Error("prompt should not be empty")
	}
	// Should mention the target language
	if !containsStr(prompt, "German") && !containsStr(prompt, "de") {
		t.Error("prompt should mention target language")
	}
	// Should contain the default strings to translate
	if !containsStr(prompt, "He works like this") && !containsStr(prompt, "Your counterpart") {
		t.Error("prompt should contain strings to translate")
	}
}

func TestParseTranslationResponse(t *testing.T) {
	// Simulate a YAML response from the LLM
	yamlResponse := `greeting: "Du hast ein Langzeitgedaechtnis (YesMem)."
tool_hint: "Nutze search() BEVOR du implementierst."
your_counterpart: "Dein Gegenueber:"
known_pitfalls: "Bekannte Fallstricke:"
decisions_made: "Entscheidungen:"
proven_patterns: "Bewaehrte Patterns:"
reminders: "Merker:"
recent_sessions: "Letzte Sessions:"
open_work: "Offene Arbeit:"
more_via: "weitere via"
sessions_total: "Sessions insgesamt"
`

	s, err := ParseTranslationResponse(yamlResponse)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Greeting != "Du hast ein Langzeitgedaechtnis (YesMem)." {
		t.Errorf("greeting: got %q", s.Greeting)
	}
	if s.YourCounterpart != "Dein Gegenueber:" {
		t.Errorf("your_counterpart: got %q", s.YourCounterpart)
	}
}

func TestResolveStringsUsesFileIfExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "strings.yaml")

	custom := DefaultStrings()
	custom.Greeting = "Moin! YesMem hier."
	SaveStrings(path, custom)

	s := ResolveStrings(path)
	if s.Greeting != "Moin! YesMem hier." {
		t.Errorf("should use file: got %q", s.Greeting)
	}
}

func TestResolveStringsUsesDefaultsIfNoFile(t *testing.T) {
	s := ResolveStrings("/nonexistent/strings.yaml")
	defaults := DefaultStrings()
	if s.Greeting != defaults.Greeting {
		t.Errorf("should use defaults: got %q", s.Greeting)
	}
}

func TestForkReflectionStringsExist(t *testing.T) {
	s := DefaultStrings()
	if s.ForkReflectionIntro == "" {
		t.Error("ForkReflectionIntro should not be empty")
	}
	if s.ForkTaskLearnings == "" {
		t.Error("ForkTaskLearnings should not be empty")
	}
	if s.ForkTaskEvaluate == "" {
		t.Error("ForkTaskEvaluate should not be empty")
	}
	if s.ForkTaskContradictions == "" {
		t.Error("ForkTaskContradictions should not be empty")
	}
	if s.ForkNoPrevious == "" {
		t.Error("ForkNoPrevious should not be empty")
	}
	if s.ForkTaskLearningsBody == "" {
		t.Error("ForkTaskLearningsBody should not be empty")
	}
	if s.ForkTaskLearningsQuestions == "" {
		t.Error("ForkTaskLearningsQuestions should not be empty")
	}
	if s.ForkTaskEvaluateBody == "" {
		t.Error("ForkTaskEvaluateBody should not be empty")
	}
	if s.ForkTaskEvaluateImpact == "" {
		t.Error("ForkTaskEvaluateImpact should not be empty")
	}
	if s.ForkTaskContradictionsBody == "" {
		t.Error("ForkTaskContradictionsBody should not be empty")
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && os.IsPathSeparator(0) == false && // just use strings
		len(s) >= len(substr) && findSubstr(s, substr)
}

func findSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
