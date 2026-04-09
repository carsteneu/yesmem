package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseOpenClawSession(t *testing.T) {
	// Sample OpenClaw JSONL
	jsonl := `{"type":"message","timestamp":"2026-03-16T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Hello, help me with auth"}]}}
{"type":"message","timestamp":"2026-03-16T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"I'll help with authentication."}],"usage":{"cost":{"total":0.003}}}}
{"type":"message","timestamp":"2026-03-16T10:01:00Z","message":{"role":"user","content":[{"type":"text","text":"Use JWT please"}]}}
{"type":"message","timestamp":"2026-03-16T10:01:10Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"JWT is a good choice..."},{"type":"text","text":"Got it, using JWT."}]}}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")
	os.WriteFile(path, []byte(jsonl), 0644)

	messages, meta, err := ParseOpenClawSession(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(messages))
	}

	// Check user message
	if messages[0].Role != "user" {
		t.Errorf("msg[0].Role = %q, want user", messages[0].Role)
	}

	// Check assistant with thinking block
	if messages[3].Role != "assistant" {
		t.Errorf("msg[3].Role = %q, want assistant", messages[3].Role)
	}

	// Check metadata
	if meta.StartedAt.IsZero() {
		t.Error("StartedAt should be set from first message")
	}
	if meta.FirstUserMessage != "Hello, help me with auth" {
		t.Errorf("FirstUserMessage = %q", meta.FirstUserMessage)
	}
}
