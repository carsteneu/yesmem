package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodexSession(t *testing.T) {
	path := filepath.Join("testdata", "sample-codex-session.jsonl")
	messages, meta, err := ParseCodexSession(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.SourceAgent != "codex" {
		t.Fatalf("source_agent: got %q, want codex", meta.SourceAgent)
	}
	if meta.SessionID != "codex:test-codex-session-001" {
		t.Fatalf("session id: got %q", meta.SessionID)
	}
	if meta.Project != "/home/testuser/projects/myapp" {
		t.Fatalf("project: got %q", meta.Project)
	}
	if meta.FirstUserMessage != "Please inspect the parser and implement Codex ingestion." {
		t.Fatalf("first user message: got %q", meta.FirstUserMessage)
	}
	if len(messages) < 5 {
		t.Fatalf("expected at least 5 messages, got %d", len(messages))
	}

	typeCounts := map[string]int{}
	for _, m := range messages {
		typeCounts[m.MessageType]++
		if m.SourceAgent != "" {
			t.Fatalf("parser should not stamp message source_agent directly, got %q", m.SourceAgent)
		}
	}
	if typeCounts["text"] < 2 {
		t.Fatalf("expected text messages, got %v", typeCounts)
	}
	if typeCounts["tool_use"] < 1 {
		t.Fatalf("expected tool_use, got %v", typeCounts)
	}
	if typeCounts["tool_result"]+typeCounts["bash_output"] < 1 {
		t.Fatalf("expected tool output, got %v", typeCounts)
	}
	if typeCounts["thinking"] < 1 {
		t.Fatalf("expected thinking, got %v", typeCounts)
	}
}

func TestParseAutoDispatchesCodex(t *testing.T) {
	dir := t.TempDir()
	codexDir := filepath.Join(dir, ".codex", "sessions", "2026", "03", "27")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "sample-codex-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(codexDir, "rollout.jsonl")
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		t.Fatal(err)
	}

	_, meta, err := ParseAuto(dst)
	if err != nil {
		t.Fatalf("ParseAuto: %v", err)
	}
	if meta.SourceAgent != "codex" {
		t.Fatalf("expected codex dispatch, got %q", meta.SourceAgent)
	}
}
