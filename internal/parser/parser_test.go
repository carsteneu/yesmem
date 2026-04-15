package parser

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestParseSessionFile(t *testing.T) {
	messages, meta, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Metadata
	if meta.SessionID != "test-session-001" {
		t.Errorf("session ID: got %q, want %q", meta.SessionID, "test-session-001")
	}
	if meta.Project != "/var/www/html/test-project" {
		t.Errorf("project: got %q, want %q", meta.Project, "/var/www/html/test-project")
	}
	if meta.GitBranch != "feature/auth" {
		t.Errorf("branch: got %q, want %q", meta.GitBranch, "feature/auth")
	}
	if meta.StartedAt.IsZero() {
		t.Error("started_at should not be zero")
	}

	// Count by type
	typeCounts := map[string]int{}
	for _, m := range messages {
		typeCounts[m.MessageType]++
	}

	// Should have user text messages
	if typeCounts["text"] < 4 {
		t.Errorf("expected at least 4 text messages, got %d (types: %v)", typeCounts["text"], typeCounts)
	}

	// Should have thinking blocks
	if typeCounts["thinking"] < 1 {
		t.Errorf("expected at least 1 thinking block, got %d", typeCounts["thinking"])
	}

	// Should have tool_use
	if typeCounts["tool_use"] < 1 {
		t.Errorf("expected at least 1 tool_use, got %d", typeCounts["tool_use"])
	}

	// Should have tool_result
	if typeCounts["tool_result"] < 1 {
		t.Errorf("expected at least 1 tool_result, got %d", typeCounts["tool_result"])
	}

	// Should have bash_output from progress
	if typeCounts["bash_output"] < 1 {
		t.Errorf("expected at least 1 bash_output, got %d", typeCounts["bash_output"])
	}

	// Check first user message content
	found := false
	for _, m := range messages {
		if m.Role == "user" && m.MessageType == "text" && m.Content == "Fix the nginx config for port 8080" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected user message 'Fix the nginx config for port 8080'")
	}

	// Check tool_use has correct ToolName
	for _, m := range messages {
		if m.MessageType == "tool_use" && m.ToolName == "Bash" {
			return // found it
		}
	}
	t.Error("expected tool_use with ToolName 'Bash'")
}

func TestParseSessionFile_ToolUseFilePath(t *testing.T) {
	messages, _, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The Edit tool_use should have a file_path extracted
	for _, m := range messages {
		if m.MessageType == "tool_use" && m.ToolName == "Edit" {
			if m.FilePath != "/etc/nginx/sites-enabled/default" {
				t.Errorf("Edit tool_use file_path: got %q, want %q", m.FilePath, "/etc/nginx/sites-enabled/default")
			}
			return
		}
	}
	t.Error("expected Edit tool_use with file_path")
}

func TestParseSessionFile_IgnoresSystemMessages(t *testing.T) {
	messages, _, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, m := range messages {
		if m.Role == "system" {
			t.Error("system messages should be ignored")
		}
	}
}

func TestParseSessionFile_AwaySummaryAsPulse(t *testing.T) {
	messages, meta, err := ParseSessionFile("testdata/session-with-away-summary.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.SessionID != "pulse-test-001" {
		t.Errorf("sessionID: got %q, want %q", meta.SessionID, "pulse-test-001")
	}

	// Find the pulse message
	var pulse *models.Message
	for i := range messages {
		if messages[i].MessageType == "pulse" {
			pulse = &messages[i]
			break
		}
	}
	if pulse == nil {
		t.Fatal("expected a pulse message from away_summary, got none")
	}

	if pulse.Role != "system" {
		t.Errorf("pulse role: got %q, want %q", pulse.Role, "system")
	}

	// Content should be cleaned: no <!-- --> tags, no "(disable recaps in /config)"
	if strings.Contains(pulse.Content, "<!-- [IDs:") {
		t.Error("pulse content should not contain <!-- --> metadata tags")
	}
	if strings.Contains(pulse.Content, "disable recaps") {
		t.Error("pulse content should not contain '(disable recaps in /config)' suffix")
	}

	// Content should contain the actual summary
	if !strings.Contains(pulse.Content, "Tree-sitter-Integration") {
		t.Errorf("pulse content missing expected text, got: %q", pulse.Content)
	}

	// Regular system messages (without away_summary subtype) should still be ignored
	for _, m := range messages {
		if m.Role == "system" && m.MessageType != "pulse" {
			t.Error("regular system messages should still be ignored")
		}
	}
}

func TestParseSessionFile_SequenceOrder(t *testing.T) {
	messages, _, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i := 1; i < len(messages); i++ {
		if messages[i].Sequence <= messages[i-1].Sequence {
			t.Errorf("messages not in sequence order: msg[%d].seq=%d <= msg[%d].seq=%d",
				i, messages[i].Sequence, i-1, messages[i-1].Sequence)
		}
	}
}

func TestParseSessionFile_LargeLine(t *testing.T) {
	// Create a temp JSONL file with a line >4MB to verify grow-on-demand works
	bigContent := strings.Repeat("x", 5*1024*1024) // 5MB content
	line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"%s"},"sessionId":"large-test","uuid":"uuid-1","timestamp":"2026-01-01T00:00:00Z"}`, bigContent)

	tmp, err := os.CreateTemp(t.TempDir(), "large-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(tmp, line)
	tmp.Close()

	messages, meta, err := ParseSessionFile(tmp.Name())
	if err != nil {
		t.Fatalf("failed to parse >4MB line: %v", err)
	}
	if meta.SessionID != "large-test" {
		t.Errorf("sessionID: got %q, want 'large-test'", meta.SessionID)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if len(messages[0].Content) < 5*1024*1024 {
		t.Errorf("content too short: %d bytes", len(messages[0].Content))
	}
}

func TestParseSessionFile_NotFound(t *testing.T) {
	_, _, err := ParseSessionFile("testdata/nonexistent.jsonl")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseSubagentSession(t *testing.T) {
	messages, meta, err := ParseSessionFile("testdata/sample-subagent.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Subagent metadata
	if meta.AgentID != "abc123def" {
		t.Errorf("agentId: got %q, want 'abc123def'", meta.AgentID)
	}
	// SessionID in subagent JSONL is the parent's session ID
	if meta.SessionID != "parent-session-001" {
		t.Errorf("sessionId (parent): got %q, want 'parent-session-001'", meta.SessionID)
	}
	if meta.Project != "/home/user/memory" {
		t.Errorf("project: got %q, want '/home/user/memory'", meta.Project)
	}
	if meta.FirstUserMessage != "Explore the project structure and find all Go files" {
		t.Errorf("first message: got %q", meta.FirstUserMessage)
	}

	// Should have messages
	if len(messages) < 3 {
		t.Errorf("expected at least 3 messages, got %d", len(messages))
	}

	// Check tool_use extraction works for subagent
	foundBash := false
	for _, m := range messages {
		if m.MessageType == "tool_use" && m.ToolName == "Bash" {
			foundBash = true
		}
	}
	if !foundBash {
		t.Error("expected Bash tool_use in subagent messages")
	}
}

func TestParseSubagentSession_RegularSessionHasNoAgentID(t *testing.T) {
	_, meta, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.AgentID != "" {
		t.Errorf("regular session should have empty agentId, got %q", meta.AgentID)
	}
}

func TestParseSessionFile_FirstMessage(t *testing.T) {
	_, meta, err := ParseSessionFile("testdata/sample-session.jsonl")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.FirstUserMessage != "Fix the nginx config for port 8080" {
		t.Errorf("first user message: got %q, want %q", meta.FirstUserMessage, "Fix the nginx config for port 8080")
	}
}
