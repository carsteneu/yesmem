package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/archive"
	"github.com/carsteneu/yesmem/internal/bloom"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func setupIndexer(t *testing.T) (*Indexer, *storage.Store) {
	t.Helper()

	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bloomMgr := bloom.New()
	archiveDir := filepath.Join(os.TempDir(), "archive-test-"+t.Name())
	os.RemoveAll(archiveDir)
	t.Cleanup(func() { os.RemoveAll(archiveDir) })
	arch := archive.New(archiveDir)

	return New(store, bloomMgr, arch), store
}

func TestIndexSession(t *testing.T) {
	idx, store := setupIndexer(t)

	// Use the test fixture from parser package
	fixture := filepath.Join("..", "parser", "testdata", "sample-session.jsonl")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("fixture not found: %v", err)
	}

	err := idx.IndexSession(fixture)
	if err != nil {
		t.Fatalf("index session: %v", err)
	}

	// Verify session stored
	sess, err := store.GetSession("test-session-001")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.ProjectShort != "test-project" {
		t.Errorf("project_short: got %q, want %q", sess.ProjectShort, "test-project")
	}
	if sess.FirstMessage != "Fix the nginx config for port 8080" {
		t.Errorf("first_message: got %q", sess.FirstMessage)
	}

	// Verify messages stored
	msgs, err := store.GetMessagesBySession("test-session-001")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}

	// Verify bloom filter
	matches := idx.bloom.MayContain("nginx")
	if !containsStr(matches, "test-session-001") {
		t.Error("bloom should match 'nginx' for test-session-001")
	}

	// Verify idempotence (second index should be no-op)
	err = idx.IndexSession(fixture)
	if err != nil {
		t.Fatalf("re-index: %v", err)
	}
}

func TestIndexSession_EmptyFile(t *testing.T) {
	idx, _ := setupIndexer(t)

	// Create an empty JSONL
	tmp := filepath.Join(os.TempDir(), "empty-test.jsonl")
	os.WriteFile(tmp, []byte{}, 0644)
	t.Cleanup(func() { os.Remove(tmp) })

	// Should not error, just skip
	err := idx.IndexSession(tmp)
	if err != nil {
		t.Fatalf("empty session should not error: %v", err)
	}
}

func TestIndexSession_CodexPersistsMessageSourceAgent(t *testing.T) {
	idx, store := setupIndexer(t)

	srcFixture := filepath.Join("..", "parser", "testdata", "sample-codex-session.jsonl")
	if _, err := os.Stat(srcFixture); err != nil {
		t.Skipf("fixture not found: %v", err)
	}
	root := t.TempDir()
	codexDir := filepath.Join(root, ".codex", "sessions", "2026", "03", "27")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	fixture := filepath.Join(codexDir, "sample-codex-session.jsonl")
	data, err := os.ReadFile(srcFixture)
	if err != nil {
		t.Fatalf("read codex fixture: %v", err)
	}
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}

	if err := idx.IndexSession(fixture); err != nil {
		t.Fatalf("index codex session: %v", err)
	}

	sess, err := store.GetSession("codex:test-codex-session-001")
	if err != nil {
		t.Fatalf("get codex session: %v", err)
	}
	if sess.SourceAgent != models.SourceAgentCodex {
		t.Fatalf("session source_agent: got %q, want %q", sess.SourceAgent, models.SourceAgentCodex)
	}

	msgs, err := store.GetMessagesBySession("codex:test-codex-session-001")
	if err != nil {
		t.Fatalf("get codex messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected codex messages")
	}
	for i, msg := range msgs {
		if msg.SourceAgent != models.SourceAgentCodex {
			t.Fatalf("message[%d] source_agent: got %q, want %q", i, msg.SourceAgent, models.SourceAgentCodex)
		}
	}
}

func TestNormalizeCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		expected string
	}{
		{"docker-compose up -d", "docker-compose"},
		{"git status", "git"},
		{"ls -la /var/www", "ls"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeCommand(tt.cmd)
		if got != tt.expected {
			t.Errorf("normalizeCommand(%q) = %q, want %q", tt.cmd, got, tt.expected)
		}
	}
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func TestGuessAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Explore the codebase and find all test files", "explore"},
		{"find files matching *.go", "explore"},
		{"search code for handleRequest", "explore"},
		{"Write a plan for implementing auth", "plan"},
		{"Design a plan for the migration", "plan"},
		{"Create an implementation plan", "plan"},
		{"Review this PR for issues", "code-reviewer"},
		{"Run a code-review on the diff", "code-reviewer"},
		{"Fix the bug in handler.go", "general-purpose"},
		{"Implement the new feature", "general-purpose"},
		{"", "general-purpose"},
	}
	for _, tt := range tests {
		got := guessAgentType(tt.input)
		if got != tt.want {
			t.Errorf("guessAgentType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestProjectDirName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/user/.claude/projects/-var-www-html-myproject/session.jsonl", "-var-www-html-myproject"},
		{"/home/user/.claude/projects/-var-www-html-myproject/abc123/subagents/agent.jsonl", "-var-www-html-myproject"},
	}
	for _, tt := range tests {
		got := projectDirName(tt.path)
		if got != tt.want {
			t.Errorf("projectDirName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
