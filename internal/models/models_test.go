package models

import (
	"testing"
	"time"
)

func TestSessionFields(t *testing.T) {
	s := Session{
		ID:           "005aac34-1de8-44b2-96e9-b815ed92be00",
		Project:      "/var/www/html/ccm19/cookie-consent-management",
		ProjectShort: "cookie-consent-management",
		GitBranch:    "main",
		FirstMessage: "Fix the auth module",
		MessageCount: 42,
		StartedAt:    time.Now(),
		JSONLPath:    "/home/user/.claude/projects/-var-www-html-myproject/005aac34.jsonl",
		JSONLSize:    1024000,
		IndexedAt:    time.Now(),
	}
	if s.ID == "" {
		t.Error("session ID should not be empty")
	}
	if s.ProjectShort != "cookie-consent-management" {
		t.Errorf("expected 'cookie-consent-management', got '%s'", s.ProjectShort)
	}
}

func TestProjectShortFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/var/www/html/ccm19/cookie-consent-management", "cookie-consent-management"},
		{"/home/user/memory", "memory"},
		{"/var/www/html/GreenWashProjekt/greenwashCCm19/cookie-consent-management", "cookie-consent-management"},
		{"/", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := ProjectShortFromPath(tt.path)
		if got != tt.expected {
			t.Errorf("ProjectShortFromPath(%q) = %q, want %q", tt.path, got, tt.expected)
		}
	}
}

func TestIsValidMessageType(t *testing.T) {
	valid := []string{"text", "tool_use", "tool_result", "thinking", "bash_output"}
	for _, mt := range valid {
		if !IsValidMessageType(mt) {
			t.Errorf("expected '%s' to be valid", mt)
		}
	}
	if IsValidMessageType("invalid") {
		t.Error("'invalid' should not be a valid message type")
	}
}

func TestIsValidCategory(t *testing.T) {
	valid := []string{"explicit_teaching", "gotcha", "decision", "pattern", "preference", "unfinished", "relationship", "strategic"}
	for _, cat := range valid {
		if !IsValidCategory(cat) {
			t.Errorf("expected '%s' to be valid category", cat)
		}
	}
	if IsValidCategory("bogus") {
		t.Error("'bogus' should not be a valid category")
	}
}

func TestLearningIsActive(t *testing.T) {
	active := Learning{
		ID:       1,
		Category: "gotcha",
		Content:  "Docker needs no sudo",
	}
	if !active.IsActive() {
		t.Error("learning without superseded_by should be active")
	}

	supersededBy := int64(5)
	inactive := Learning{
		ID:           2,
		Category:     "decision",
		Content:      "Use Redis",
		SupersededBy: &supersededBy,
	}
	if inactive.IsActive() {
		t.Error("learning with superseded_by should not be active")
	}

	expired := Learning{
		ID:        3,
		Category:  "unfinished",
		Content:   "Migration step 3",
		ExpiresAt: timePtr(time.Now().Add(-24 * time.Hour)),
	}
	if expired.IsActive() {
		t.Error("learning past expires_at should not be active")
	}
}

func TestLearning_HasEmotionalIntensity(t *testing.T) {
	l := Learning{
		Category:           "gotcha",
		Content:            "test",
		EmotionalIntensity: 0.8,
	}
	if l.EmotionalIntensity != 0.8 {
		t.Errorf("expected 0.8, got %f", l.EmotionalIntensity)
	}
}

func TestLearning_HasLastHitAt(t *testing.T) {
	now := time.Now()
	l := Learning{
		Category:  "gotcha",
		Content:   "test",
		LastHitAt: &now,
	}
	if l.LastHitAt == nil || l.LastHitAt.IsZero() {
		t.Error("LastHitAt should be set")
	}
}

func TestSession_IsSubagent(t *testing.T) {
	main := Session{ID: "abc", Project: "/home/user/memory"}
	if main.IsSubagent() {
		t.Error("session without parent should not be subagent")
	}

	sub := Session{ID: "def", ParentSessionID: "abc", AgentType: "Explore"}
	if !sub.IsSubagent() {
		t.Error("session with parent should be subagent")
	}
}

func TestBuildEmbeddingText_WithAnticipatedQueries(t *testing.T) {
	l := Learning{
		Project:            "yesmem",
		Domain:             "code",
		Category:           "gotcha",
		Content:            "FTS5 braucht quoted terms bei Bindestrichen",
		Context:            "SQL error mit column operator",
		TriggerRule:        "Wenn FTS5 SQL error mit Spaltennamen",
		Entities:           []string{"handler_search.go", "handler_hybrid.go"},
		Actions:            []string{"sanitizeFTS5Query"},
		AnticipatedQueries: []string{"deep_search schlägt fehl", "SQL error no such column", "Bindestrich in Suchbegriff"},
	}
	text := l.BuildEmbeddingText()

	// Must contain all sections
	checks := []string{
		"[yesmem code gotcha]",
		"FTS5 braucht quoted terms",
		"Context: SQL error",
		"Trigger: Wenn FTS5",
		"Entities: handler_search.go",
		"Actions: sanitizeFTS5Query",
		"Queries: deep_search schlägt fehl; SQL error no such column; Bindestrich in Suchbegriff",
	}
	for _, want := range checks {
		if !contains(text, want) {
			t.Errorf("BuildEmbeddingText missing %q\ngot: %s", want, text)
		}
	}
}

func TestBuildEmbeddingText_WithoutAnticipatedQueries(t *testing.T) {
	l := Learning{
		Project:  "test",
		Category: "decision",
		Content:  "Use additive scoring",
	}
	text := l.BuildEmbeddingText()
	if contains(text, "Queries:") {
		t.Error("should not contain Queries: section when AnticipatedQueries is empty")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func timePtr(t time.Time) *time.Time {
	return &t
}
