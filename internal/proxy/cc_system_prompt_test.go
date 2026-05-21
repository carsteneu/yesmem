package proxy

import (
	"io"
	"log"
	"strings"
	"testing"
)

func newTestServer(ccEnabled bool, tpl []byte) *Server {
	return &Server{
		cfg: Config{
			CustomSystemPrompt: CustomSystemPromptConfig{
				EnabledClaudeCode: ccEnabled,
				TemplatePath:      "~/.claude/yesmem/SYSTEM.md",
			},
		},
		customSystemPrompt: tpl,
		logger:             log.New(io.Discard, "", 0),
	}
}

func TestFindCCSystemBlockIndex(t *testing.T) {
	req := map[string]any{"system": []any{
		map[string]any{"text": "billing cch=abc"},
		map[string]any{"text": "You are Claude Code, Anthropic's official CLI for Claude. ..."},
	}}
	got := findCCSystemBlockIndex(req)
	if got != 1 {
		t.Errorf("expected index 1, got %d", got)
	}

	req2 := map[string]any{"system": []any{map[string]any{"text": "random"}}}
	got2 := findCCSystemBlockIndex(req2)
	if got2 != -1 {
		t.Errorf("expected -1, got %d", got2)
	}

	req3 := map[string]any{}
	got3 := findCCSystemBlockIndex(req3)
	if got3 != -1 {
		t.Errorf("expected -1 for empty req, got %d", got3)
	}
}

func TestExtractCCWorkingDir(t *testing.T) {
	orig := "...\nPrimary working directory: /home/user/projects/myproject\n..."
	got := extractCCWorkingDir(orig)
	if got != "/home/user/projects/myproject" {
		t.Errorf("expected /home/user/projects/myproject, got %q", got)
	}

	if got2 := extractCCWorkingDir("no marker"); got2 != "" {
		t.Errorf("expected empty for no marker, got %q", got2)
	}
}

func TestReplaceCCSystemBlock(t *testing.T) {
	req := map[string]any{"system": []any{
		map[string]any{"text": "billing"},
		map[string]any{"text": "You are Claude Code, Anthropic's official CLI"},
	}}
	replaceCCSystemBlock(req, []byte("REPLACED"))
	sys := req["system"].([]any)
	if sys[0].(map[string]any)["text"] != "billing" {
		t.Errorf("expected billing block unchanged, got %v", sys[0])
	}
	if sys[1].(map[string]any)["text"] != "REPLACED" {
		t.Errorf("expected replaced block, got %v", sys[1])
	}
}

func TestApplyCCSystemPrompt_DisabledIsNoOp(t *testing.T) {
	req := map[string]any{"system": []any{
		map[string]any{"text": "You are Claude Code, Anthropic's official CLI"},
	}}
	s := newTestServer(false, []byte("TPL"))
	if s.applyCCSystemPrompt(req) {
		t.Error("expected false (disabled)")
	}
	text := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Claude Code, Anthropic's official CLI") {
		t.Errorf("expected original text preserved, got %q", text)
	}
}

func TestApplyCCSystemPrompt_EnabledReplaces(t *testing.T) {
	req := map[string]any{"system": []any{
		map[string]any{"text": "You are Claude Code, Anthropic's official CLI for Claude.\n" +
			"Primary working directory: /tmp/test\n"},
	}, "model": "claude-opus-4-7"}
	s := newTestServer(true, []byte("Host:{{.HostAgentName}} CWD:{{.WorkingDir}}"))
	if !s.applyCCSystemPrompt(req) {
		t.Error("expected true (enabled)")
	}
	text := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Host:Claude Code") {
		t.Errorf("expected Host:Claude Code, got %q", text)
	}
	if !strings.Contains(text, "CWD:/tmp/test") {
		t.Errorf("expected CWD:/tmp/test, got %q", text)
	}
	if strings.Contains(text, "{{.") {
		t.Errorf("unfilled placeholder in output: %s", text)
	}
}
