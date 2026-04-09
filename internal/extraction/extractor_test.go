package extraction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestParseExtractionResponse_Valid(t *testing.T) {
	response := `{
		"domain": "code",
		"learnings": [
			{"category": "explicit_teaching", "content": "Docker needs no sudo on this system", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3},
			{"category": "gotcha", "content": "phpunit without Redis — Connection Refused — start redis-server", "context": "Redis muss laufen", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 4},
			{"category": "decision", "content": "Go statt Rust, weil pure Go SQLite", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 4},
			{"category": "user_preference", "content": "Deutsch, locker, du", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3},
			{"category": "unfinished", "content": "Migration Step 3/7 done, Step 4 pending", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3},
			{"category": "relationship", "content": "Working together since Nov 2025", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 2}
		],
		"session_emotional_intensity": 0.3,
		"session_flavor": "Routine setup session"
	}`

	learnings, err := parseExtractionResponse(response, "test-session", "opus")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(learnings) != 6 {
		t.Errorf("expected 6 learnings, got %d", len(learnings))
	}

	// Check categories
	cats := map[string]int{}
	for _, l := range learnings {
		cats[l.Category]++
		if l.SessionID != "test-session" {
			t.Errorf("session_id should be 'test-session', got %q", l.SessionID)
		}
		if l.ModelUsed != "opus" {
			t.Errorf("model_used should be 'opus', got %q", l.ModelUsed)
		}
		if l.Source != "llm_extracted" {
			t.Errorf("source should be 'llm_extracted', got %q", l.Source)
		}
		if l.Domain != "code" {
			t.Errorf("domain should be 'code', got %q", l.Domain)
		}
	}

	if cats["explicit_teaching"] != 1 {
		t.Errorf("expected 1 explicit_teaching, got %d", cats["explicit_teaching"])
	}
	if cats["gotcha"] != 1 {
		t.Errorf("expected 1 gotcha, got %d", cats["gotcha"])
	}
}

func TestParseExtractionResponse_StructuredOutput(t *testing.T) {
	// With structured outputs, API guarantees clean JSON — no markdown wrapping
	response := `{"domain": "code", "learnings": [{"category": "gotcha", "content": "Port 8080 is taken", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}], "session_emotional_intensity": 0.3, "session_flavor": "test"}`

	learnings, err := parseExtractionResponse(response, "s1", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(learnings) != 1 {
		t.Errorf("expected 1 learning, got %d", len(learnings))
	}
}

func TestParseExtractionResponse_EmptyCategories(t *testing.T) {
	response := `{"domain": "code", "learnings": [], "session_emotional_intensity": 0, "session_flavor": ""}`

	learnings, err := parseExtractionResponse(response, "s1", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(learnings) != 0 {
		t.Errorf("expected 0 learnings from empty categories, got %d", len(learnings))
	}
}

func TestParseExtractionResponse_PivotMoments(t *testing.T) {
	response := `{
		"domain": "code",
		"learnings": [{"category": "pivot_moment", "content": "User sagte 'warum generieren wir überhaupt für 5-Message-Sessions?' — Perspektivwechsel von Output-Filtern zu Input-Filtern", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "Bei Session-Filter-Design", "importance": 5}],
		"session_emotional_intensity": 0.8,
		"session_flavor": "Perspektivwechsel"
	}`

	learnings, err := parseExtractionResponse(response, "test-session", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	pivots := 0
	for _, l := range learnings {
		if l.Category == "pivot_moment" {
			pivots++
			if l.Content == "" {
				t.Error("pivot_moment content should not be empty")
			}
		}
	}
	if pivots != 1 {
		t.Errorf("expected 1 pivot_moment, got %d", pivots)
	}
}

func TestParseExtractionResponse_EmotionalIntensity(t *testing.T) {
	response := `{
		"domain": "code",
		"learnings": [{"category": "gotcha", "content": "Port conflict — fixed by changing to 8081", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}],
		"session_emotional_intensity": 0.7,
		"session_flavor": "Port-Wechsel"
	}`

	learnings, err := parseExtractionResponse(response, "test-session", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, l := range learnings {
		if l.EmotionalIntensity != 0.7 {
			t.Errorf("expected emotional_intensity 0.7, got %f", l.EmotionalIntensity)
		}
	}
}

func TestParseExtractionResponse_EmotionalIntensityDefault(t *testing.T) {
	// Response without emotional_intensity should default to 0
	response := `{
		"domain": "code",
		"learnings": [{"category": "explicit_teaching", "content": "test learning item here", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}],
		"session_emotional_intensity": 0,
		"session_flavor": ""
	}`

	learnings, err := parseExtractionResponse(response, "s1", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, l := range learnings {
		if l.EmotionalIntensity != 0.0 {
			t.Errorf("expected default 0.0, got %f", l.EmotionalIntensity)
		}
	}
}

func TestParseExtractionResponse_SessionFlavor(t *testing.T) {
	response := `{
		"domain": "code",
		"learnings": [{"category": "gotcha", "content": "Port conflict leading to issues", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}],
		"session_emotional_intensity": 0.8,
		"session_flavor": "3h Sandbox-Kampf bis User sie ausschaltet, dann läuft alles"
	}`

	learnings, err := parseExtractionResponse(response, "test-session", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, l := range learnings {
		if l.SessionFlavor != "3h Sandbox-Kampf bis User sie ausschaltet, dann läuft alles" {
			t.Errorf("expected session_flavor on learning, got %q", l.SessionFlavor)
		}
	}
}

func TestParseExtractionResponse_SessionFlavorDefault(t *testing.T) {
	// Response without session_flavor should default to empty
	response := `{
		"domain": "code",
		"learnings": [{"category": "explicit_teaching", "content": "test learning item here", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}],
		"session_emotional_intensity": 0.5,
		"session_flavor": ""
	}`

	learnings, err := parseExtractionResponse(response, "s1", "haiku")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, l := range learnings {
		if l.SessionFlavor != "" {
			t.Errorf("expected empty session_flavor default, got %q", l.SessionFlavor)
		}
	}
}

// mockLLMClient for testing two-pass extraction.
type mockLLMClient struct {
	completeFunc     func(system, user string) (string, error)
	completeJSONFunc func(system, user string, schema map[string]any) (string, error)
	model            string
}

func (m *mockLLMClient) Complete(system, user string, opts ...CallOption) (string, error) {
	if m.completeFunc != nil {
		return m.completeFunc(system, user)
	}
	return "", nil
}

func (m *mockLLMClient) CompleteJSON(system, user string, schema map[string]any, opts ...CallOption) (string, error) {
	if m.completeJSONFunc != nil {
		return m.completeJSONFunc(system, user, schema)
	}
	return "", nil
}

func (m *mockLLMClient) Name() string  { return "mock" }
func (m *mockLLMClient) Model() string { return m.model }

func TestExtractFromSession_TwoPass(t *testing.T) {
	summarizeClient := &mockLLMClient{
		completeFunc: func(system, user string) (string, error) {
			return "User entschied Go statt Rust. Problem: Port 8080 belegt, gelöst durch Wechsel auf 8081. User sagte 'immer Deutsch'. Ruhige Session.", nil
		},
		model: "haiku",
	}

	extractClient := &mockLLMClient{
		completeJSONFunc: func(system, user string, schema map[string]any) (string, error) {
			return `{
				"domain": "code",
				"learnings": [
					{"category": "explicit_teaching", "content": "Immer Deutsch verwenden", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 4},
					{"category": "gotcha", "content": "Port 8080 belegt — Wechsel auf 8081", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3},
					{"category": "decision", "content": "Go statt Rust weil pure Go SQLite", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 4}
				],
				"session_emotional_intensity": 0.3,
				"session_flavor": "Ruhige Entscheidungs-Session"
			}`, nil
		},
		model: "sonnet",
	}

	ext := NewTwoPassExtractor(summarizeClient, extractClient, nil)

	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Sollen wir Go oder Rust nehmen?"},
		{Role: "assistant", MessageType: "text", Content: "Go — pure SQLite ohne CGO."},
		{Role: "user", MessageType: "text", Content: "Ok, Go. Und bitte immer Deutsch."},
	}

	learnings, err := ext.ExtractFromSession("test-session-id", msgs)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if len(learnings) != 3 {
		t.Errorf("expected 3 learnings, got %d", len(learnings))
	}

	// Model should be from extract client (Sonnet), not summarize client
	for _, l := range learnings {
		if l.ModelUsed != "sonnet" {
			t.Errorf("model should be 'sonnet', got %q", l.ModelUsed)
		}
	}
}

func TestTwoPassExtractor_PreFiltersMessages(t *testing.T) {
	var summarizeInput string
	summarizeClient := &mockLLMClient{
		completeFunc: func(system, user string) (string, error) {
			summarizeInput = user
			return "User entschied X.", nil
		},
		model: "haiku",
	}
	extractClient := &mockLLMClient{
		completeJSONFunc: func(system, user string, schema map[string]any) (string, error) {
			return `{"domain": "code", "learnings": [{"category": "decision", "content": "X decision made here", "context": "", "entities": [], "actions": [], "keywords": [], "trigger": "", "importance": 3}], "session_emotional_intensity": 0.3, "session_flavor": "test"}`, nil
		},
		model: "sonnet",
	}

	ext := NewTwoPassExtractor(summarizeClient, extractClient, nil)

	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Do X"},
		{Role: "assistant", MessageType: "tool_use", ToolName: "Read", FilePath: "/foo.go", Content: `{"file_path":"/foo.go"}`},
		{Role: "user", MessageType: "tool_result", Content: strings.Repeat("source code here ", 100)},
		{Role: "assistant", MessageType: "text", Content: "Done"},
	}

	_, err := ext.ExtractFromSession("test-session-id", msgs)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Verify tool_result was filtered out
	if strings.Contains(summarizeInput, "source code here") {
		t.Error("tool_result content should have been pre-filtered")
	}
	// Verify tool_use name is still present
	if !strings.Contains(summarizeInput, "Read") {
		t.Error("tool_use name should be preserved in summarize input")
	}
}

func TestTwoPassExtractor_EmptySummary(t *testing.T) {
	summarizeClient := &mockLLMClient{
		completeFunc: func(system, user string) (string, error) {
			return "", nil
		},
		model: "haiku",
	}
	extractClient := &mockLLMClient{
		model: "sonnet",
	}

	ext := NewTwoPassExtractor(summarizeClient, extractClient, nil)

	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Hi"},
	}

	learnings, err := ext.ExtractFromSession("test-session-id", msgs)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(learnings) != 0 {
		t.Errorf("expected 0 learnings from empty summary, got %d", len(learnings))
	}
}

func TestApplyEvolutionResponse_SingleIDNoOp(t *testing.T) {
	store := mustOpenStore(t)
	id1 := insertTestLearning(store, "some valid learning", "gotcha")

	ext := &Extractor{store: store}

	// Single-ID supersede should be a no-op (no loser to supersede)
	response := `{"actions":[{"type":"supersede","supersedes_ids":[` + fmt.Sprintf("%d", id1) + `],"reason":"junk","new_learning":""}]}`
	superseded := ext.applyEvolutionResponse(response, "test", store, nil)

	if superseded != 0 {
		t.Errorf("expected 0 superseded for single-ID action, got %d", superseded)
	}

	// Learning should still be active
	l, _ := store.GetLearning(id1)
	if l.SupersededBy != nil {
		t.Errorf("learning should not be superseded, got superseded_by=%v", *l.SupersededBy)
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"a": 1}`, `{"a": 1}`},
		{"Some text\n```json\n{\"a\": 1}\n```\nMore text", `{"a": 1}`},
		{`Here is the result: {"nested": {"b": 2}} done`, `{"nested": {"b": 2}}`},
	}
	for _, tt := range tests {
		got := extractJSON(tt.input)
		if got != tt.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tt.input[:30], got, tt.want)
		}
	}
}
