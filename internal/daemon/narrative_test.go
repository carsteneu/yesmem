package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestNarrativePrompt(t *testing.T) {
	prompt := NarrativePrompt()
	if prompt == "" {
		t.Error("prompt should not be empty")
	}
	if !strings.Contains(prompt, "handover") || !strings.Contains(prompt, "future self") {
		t.Error("prompt should instruct first-person handover style")
	}
	// Immersive narrative checks
	if !strings.Contains(prompt, "narrate") && !strings.Contains(prompt, "NARRATE") {
		t.Error("prompt should instruct storytelling, not summarizing")
	}
	if !strings.Contains(prompt, "KEY MOMENT") && !strings.Contains(prompt, "key moment") {
		t.Error("prompt should ask for key moments")
	}
	if !strings.Contains(prompt, "sentences") && !strings.Contains(prompt, "Sentences") {
		t.Error("prompt should specify sentence count")
	}
	if !strings.Contains(prompt, "DIFFERENT") {
		t.Error("prompt should instruct differentiation from similar sessions")
	}
	if !strings.Contains(prompt, "timestamps") && !strings.Contains(prompt, "NEVER relative") {
		t.Error("prompt should require concrete timestamps")
	}
}

func TestIsNarrativeTooShort(t *testing.T) {
	tests := []struct {
		input string
		short bool
	}{
		{"Too short.", true},
		{"One. Two.", true},
		{"One. Two. Three.", false},
		{"Hey, du bist ich von 14:30. Wir haben den Prompt überarbeitet. Das war gut. Nächste Session: Persona Engine.", false},
		{"", true},
	}
	for _, tt := range tests {
		got := isNarrativeTooShort(tt.input)
		if got != tt.short {
			t.Errorf("isNarrativeTooShort(%q) = %v, want %v", tt.input, got, tt.short)
		}
	}
}

func TestSessionTimeRange(t *testing.T) {
	s := models.Session{}
	if sessionTimeRange(s) != "" {
		t.Error("zero time should return empty string")
	}
}

func TestSummarizeMessages(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix the cookie scanner"},
		{Role: "assistant", MessageType: "text", Content: "I'll look into it."},
		{Role: "user", MessageType: "text", Content: strings.Repeat("a", 1000)},
		{Role: "assistant", MessageType: "text", Content: strings.Repeat("b", 1000)},
		{Role: "assistant", MessageType: "tool_use", Content: "should be skipped"},
	}
	summaries := SummarizeMessages(msgs)
	if len(summaries) != 4 {
		t.Errorf("expected 4 summaries (skip tool_use), got %d", len(summaries))
	}
	// User long message: truncated at 800
	if len(summaries[2]) > 810 {
		t.Errorf("user message should be truncated to ~800, got %d", len(summaries[2]))
	}
	// Assistant long message: truncated at 500
	if len(summaries[3]) > 510 {
		t.Errorf("assistant message should be truncated to ~500, got %d", len(summaries[3]))
	}
}

func TestBuildNarrativeUserMessage(t *testing.T) {
	messages := []string{
		"user: Fix the cookie scanner",
		"assistant: I'll look into the timeout issue.",
		"user: Great, also check the retry logic",
	}
	msg := BuildNarrativeUserMessage(messages, "ccm19")
	if msg == "" {
		t.Error("user message should not be empty")
	}
	if !strings.Contains(msg, "ccm19") {
		t.Error("should contain project name")
	}
	if !strings.Contains(msg, "cookie scanner") {
		t.Error("should contain session content")
	}
}

func TestBuildNarrativeUserMessageTruncates(t *testing.T) {
	// Generate a very long message list
	var messages []string
	for i := 0; i < 1000; i++ {
		messages = append(messages, "user: This is a very long message that goes on and on and on")
	}
	msg := BuildNarrativeUserMessage(messages, "test")
	// Should be capped at reasonable size
	if len(msg) > 30000 {
		t.Errorf("message too long: %d chars, should be capped", len(msg))
	}
}

func TestCleanNarrativeResponse(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hey, du bist ich von gestern.", "Hey, du bist ich von gestern."},
		{"```\nHey, narrative here.\n```", "Hey, narrative here."},
		{"  \n  Some text  \n  ", "Some text"},
		{"", ""},
	}
	for _, tt := range tests {
		got := CleanNarrativeResponse(tt.input)
		if got != tt.want {
			t.Errorf("clean(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTimeSince(t *testing.T) {
	now := time.Now()

	got := timeSince(now.Add(-5 * time.Minute))
	if got != "5 min ago" {
		t.Errorf("expected '5 min ago', got %q", got)
	}

	got = timeSince(now.Add(-3 * time.Hour))
	if got != "3h ago" {
		t.Errorf("expected '3h ago', got %q", got)
	}

	got = timeSince(now.Add(-48 * time.Hour))
	if got != "2 days ago" {
		t.Errorf("expected '2 days ago', got %q", got)
	}
}

func TestBuildNarrativeUserMessageWithTime(t *testing.T) {
	messages := []string{"user: test msg"}
	result := BuildNarrativeUserMessageWithTime(messages, "yesmem", "2026-04-01 14:30")
	if result == "" {
		t.Fatal("expected non-empty message")
	}
	if !strings.Contains(result, "14:30") {
		t.Error("should contain session time")
	}
}

func TestSessionTimeRange_WithTime(t *testing.T) {
	s := models.Session{StartedAt: time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC)}
	result := sessionTimeRange(s)
	if result != "2026-04-01 14:30" {
		t.Errorf("expected formatted time, got %q", result)
	}
}
