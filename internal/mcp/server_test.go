package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentClientModelEmpty(t *testing.T) {
	for _, key := range []string{"YESMEM_MODEL_ID", "CODEX_MODEL", "OPENAI_MODEL", "ANTHROPIC_MODEL", "CLAUDE_MODEL", "MODEL"} {
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

// TestCurrentClientModelYesmemModelIDWins verifies that YESMEM_MODEL_ID takes
// priority over the generic vendor env vars. This gives direct-MCP clients
// (opencode, codex) a clean, unambiguous way to declare their model without
// colliding with vendor-specific vars they may not own. See learning #76567.
func TestCurrentClientModelYesmemModelIDWins(t *testing.T) {
	t.Setenv("MODEL", "generic")
	t.Setenv("CLAUDE_MODEL", "claude-sonnet")
	t.Setenv("YESMEM_MODEL_ID", " glm-5.2 ")

	if got := currentClientModel(); got != "glm-5.2" {
		t.Fatalf("currentClientModel(): got %q, want %q (YESMEM_MODEL_ID must win)", got, "glm-5.2")
	}
}

func TestBuildProxyParamsPreservesAuthoritativeContext(t *testing.T) {
	for _, key := range []string{
		"YESMEM_SOURCE_AGENT",
		"YESMEM_SESSION_ID",
		"CODEX_THREAD_ID",
		"CLAUDE_SESSION_ID",
		"CLAUDE_CODE_SESSION_ID",
		"OPENCODE",
		"YESMEM_MODEL_ID",
		"CODEX_MODEL",
		"OPENAI_MODEL",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
		"MODEL",
	} {
		t.Setenv(key, "")
	}

	arguments := map[string]any{
		"query":         "needle",
		"_session_id":   "opencode:session-a",
		"_source_agent": "opencode",
		"_cwd":          "/workspace/a",
		"_caller_pid":   float64(4242),
		"_client_model": "plugin-model",
	}
	params := buildProxyParams(arguments, true)

	for key, want := range map[string]any{
		"query":         "needle",
		"_session_id":   "opencode:session-a",
		"_source_agent": "opencode",
		"_cwd":          "/workspace/a",
		"_caller_pid":   float64(4242),
		"_client_model": "plugin-model",
		"thread_id":     "opencode:session-a",
	} {
		if got := params[key]; got != want {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}
	if _, exists := arguments["thread_id"]; exists {
		t.Fatal("buildProxyParams mutated the caller's argument map")
	}
}

func TestBuildProxyParamsSequentialSessionsDoNotLeak(t *testing.T) {
	for _, key := range []string{
		"YESMEM_SOURCE_AGENT",
		"YESMEM_SESSION_ID",
		"CODEX_THREAD_ID",
		"CLAUDE_SESSION_ID",
		"CLAUDE_CODE_SESSION_ID",
		"OPENCODE",
	} {
		t.Setenv(key, "")
	}

	first := buildProxyParams(map[string]any{
		"_session_id":   "opencode:session-a",
		"_source_agent": "opencode",
		"_cwd":          "/workspace/a",
	}, true)
	second := buildProxyParams(map[string]any{
		"_session_id":   "opencode:session-b",
		"_source_agent": "opencode",
		"_cwd":          "/workspace/b",
	}, true)

	if first["_session_id"] != "opencode:session-a" || first["thread_id"] != "opencode:session-a" {
		t.Fatalf("first call leaked or changed identity: %#v", first)
	}
	if second["_session_id"] != "opencode:session-b" || second["thread_id"] != "opencode:session-b" {
		t.Fatalf("second call leaked or changed identity: %#v", second)
	}
	if first["_cwd"] != "/workspace/a" || second["_cwd"] != "/workspace/b" {
		t.Fatalf("cwd leaked between calls: first=%#v second=%#v", first, second)
	}

	withoutThread := buildProxyParams(map[string]any{
		"_session_id": "opencode:session-c",
		"_cwd":        "/workspace/c",
	}, false)
	if _, exists := withoutThread["thread_id"]; exists {
		t.Fatalf("non-thread wrapper unexpectedly injected thread_id: %#v", withoutThread)
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

func TestFormatRememberSurfacesDedupMessage(t *testing.T) {
	// A dedup response carries only id + message (no category/content).
	// It must NOT be rendered as a fake "saved" box with empty fields
	// (regression for the misleading success report, match #85112).
	raw := []byte(`{
		"id": 51530,
		"message": "Already known (similarity 0.75). Bumped match_count for #51530.",
		"deduplicated": true
	}`)

	got := formatRemember(raw)

	if strings.Contains(got, "saved") {
		t.Fatalf("formatRemember() must not claim a save for a dedup response, got:\n%s", got)
	}
	if !strings.Contains(got, "Already known (similarity 0.75)") {
		t.Fatalf("formatRemember() should surface the dedup message, got:\n%s", got)
	}
	if !strings.Contains(got, "Bumped match_count for #51530") {
		t.Fatalf("formatRemember() should surface the bumped match_count, got:\n%s", got)
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
