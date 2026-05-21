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

// resolveClientSessionID picks up the calling agent's session ID from env vars.
// Claude Code 2.1.131 does NOT export CLAUDE_SESSION_ID; Claude Code 2.1.132+
// exports CLAUDE_CODE_SESSION_ID. Both must resolve to source_agent="claude".
func TestResolveClientSessionID_AllUnset(t *testing.T) {
	for _, k := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE"} {
		t.Setenv(k, "")
	}
	sid, sa := resolveClientSessionID()
	if sid != "" {
		t.Errorf("sid: want empty, got %q", sid)
	}
	if sa != "claude" {
		t.Errorf("sa: want claude (default), got %q", sa)
	}
}

func TestResolveClientSessionID_LegacyClaudeVar(t *testing.T) {
	for _, k := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_SESSION_ID", "legacy-sid-1")

	sid, sa := resolveClientSessionID()
	if sid != "legacy-sid-1" {
		t.Errorf("sid: want legacy-sid-1, got %q", sid)
	}
	if sa != "claude" {
		t.Errorf("sa: want claude, got %q", sa)
	}
}

func TestResolveClientSessionID_NewClaudeCodeVar(t *testing.T) {
	for _, k := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_SESSION_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cc-new-sid-2")

	sid, sa := resolveClientSessionID()
	if sid != "cc-new-sid-2" {
		t.Errorf("sid: want cc-new-sid-2, got %q", sid)
	}
	if sa != "claude" {
		t.Errorf("sa: want claude, got %q", sa)
	}
}

func TestResolveClientSessionID_LegacyTakesPrecedence(t *testing.T) {
	for _, k := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	t.Setenv("CLAUDE_SESSION_ID", "legacy-sid-3")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "code-sid-3")

	sid, _ := resolveClientSessionID()
	if sid != "legacy-sid-3" {
		t.Errorf("sid: want legacy-sid-3 (CLAUDE_SESSION_ID precedes CLAUDE_CODE_SESSION_ID), got %q", sid)
	}
}

func TestResolveClientSessionID_OpenCodeUnaffected(t *testing.T) {
	t.Setenv("YESMEM_SOURCE_AGENT", "opencode")
	t.Setenv("YESMEM_SESSION_ID", "oc-sid-4")
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "should-be-ignored")

	sid, sa := resolveClientSessionID()
	if sid != "opencode:oc-sid-4" {
		t.Errorf("sid: want opencode:oc-sid-4, got %q", sid)
	}
	if sa != "opencode" {
		t.Errorf("sa: want opencode, got %q", sa)
	}
}

func TestResolveClientSessionID_OpenCodeAutoDetect(t *testing.T) {
	for _, k := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE"} {
		t.Setenv(k, "")
	}
	t.Setenv("OPENCODE", "1")

	sid, sa := resolveClientSessionID()
	if sid != "" {
		t.Errorf("sid: want empty (auto-detect), got %q", sid)
	}
	if sa != "opencode" {
		t.Errorf("sa: want opencode (auto-detected via OPENCODE=1), got %q", sa)
	}
}

func TestResolveClientSessionID_CodexUnaffected(t *testing.T) {
	t.Setenv("YESMEM_SOURCE_AGENT", "codex")
	t.Setenv("YESMEM_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "cx-sid-5")
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "should-be-ignored")

	sid, sa := resolveClientSessionID()
	if sid != "codex:cx-sid-5" {
		t.Errorf("sid: want codex:cx-sid-5, got %q", sid)
	}
	if sa != "codex" {
		t.Errorf("sa: want codex, got %q", sa)
	}
}

func TestResolveClientSessionID_OpenCodeNoSessionID(t *testing.T) {
	for _, k := range []string{"YESMEM_SESSION_ID", "CODEX_THREAD_ID", "CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "OPENCODE"} {
		t.Setenv(k, "")
	}
	t.Setenv("YESMEM_SOURCE_AGENT", "opencode")

	sid, sa := resolveClientSessionID()
	if sid != "" {
		t.Errorf("sid: want empty (no session ID env var), got %q", sid)
	}
	if sa != "opencode" {
		t.Errorf("sa: want opencode, got %q", sa)
	}
}
