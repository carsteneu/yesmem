package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOpencodeSessionID_PrefersOpencodeHeader(t *testing.T) {
	h := http.Header{}
	h.Set("x-opencode-session", "ses_primary")
	h.Set("x-session-affinity", "ses_affinity")
	if got := opencodeSessionID(h); got != "ses_primary" {
		t.Fatalf("expected ses_primary, got %q", got)
	}
}

func TestOpencodeSessionID_FallsBackToAffinity(t *testing.T) {
	h := http.Header{}
	h.Set("x-session-affinity", "ses_affinity")
	if got := opencodeSessionID(h); got != "ses_affinity" {
		t.Fatalf("expected ses_affinity, got %q", got)
	}
}

func TestOpencodeSessionID_NoHeaders(t *testing.T) {
	if got := opencodeSessionID(http.Header{}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// opencodeLikeRequest mimics an opencode /v1/messages request: no metadata,
// system prompt identical across all opencode sessions.
func opencodeLikeRequest() map[string]any {
	return map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are OpenCode, the best coding agent on the planet."},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
}

func TestOpencodeSessionID_NewForkSessionIDHeader(t *testing.T) {
	h := http.Header{}
	h.Set("x-opencode-session-id", "ses_new")
	if got := opencodeSessionID(h); got != "ses_new" {
		t.Fatalf("expected ses_new, got %q", got)
	}
}

// TestOpencodeSessionID_ProviderShapes pins the precedence across the three
// request shapes: opencode-branded (x-opencode-session), other providers on
// builds >= .173 (x-opencode-session-id), and pre-.173 other providers
// (x-session-affinity only).
func TestOpencodeSessionID_ProviderShapes(t *testing.T) {
	branded := http.Header{}
	branded.Set("x-opencode-session", "ses_branded")
	branded.Set("x-opencode-session-id", "ses_universal")
	branded.Set("x-session-affinity", "ses_affinity")
	if got := opencodeSessionID(branded); got != "ses_branded" {
		t.Errorf("opencode-branded provider: got %q, want ses_branded", got)
	}

	other := http.Header{}
	other.Set("x-opencode-session-id", "ses_universal")
	other.Set("x-session-affinity", "ses_affinity")
	if got := opencodeSessionID(other); got != "ses_universal" {
		t.Errorf("other provider (.173+): got %q, want ses_universal", got)
	}

	old := http.Header{}
	old.Set("x-session-affinity", "ses_affinity")
	if got := opencodeSessionID(old); got != "ses_affinity" {
		t.Errorf("pre-.173 other provider: got %q, want ses_affinity", got)
	}
}

func TestAnthropicThreadID_ForkSessionIDHeader(t *testing.T) {
	h := http.Header{}
	h.Set("x-opencode-session-id", "ses_child")
	if got := anthropicThreadID(opencodeLikeRequest(), h); got != "opencode:ses_child" {
		t.Fatalf("expected opencode:ses_child, got %q", got)
	}
}

func TestAnthropicThreadID_OpencodeAffinityHeader(t *testing.T) {
	h := http.Header{}
	h.Set("x-session-affinity", "ses_abc")
	if got := anthropicThreadID(opencodeLikeRequest(), h); got != "opencode:ses_abc" {
		t.Fatalf("expected opencode:ses_abc, got %q", got)
	}
}

// Regression: all opencode sessions shared one thread ID derived from the
// identical system prompt, so per-thread state (briefing cache) leaked across sessions.
func TestAnthropicThreadID_DistinctPerOpencodeSession(t *testing.T) {
	h1 := http.Header{}
	h1.Set("x-session-affinity", "ses_one")
	h2 := http.Header{}
	h2.Set("x-session-affinity", "ses_two")
	a := anthropicThreadID(opencodeLikeRequest(), h1)
	b := anthropicThreadID(opencodeLikeRequest(), h2)
	if a == b {
		t.Fatalf("expected distinct thread IDs per opencode session, both = %q", a)
	}
}

func TestAnthropicThreadID_ClaudeCodeHeaderWins(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "cc-123")
	h.Set("x-session-affinity", "ses_abc")
	if got := anthropicThreadID(opencodeLikeRequest(), h); got != "cc-123" {
		t.Fatalf("expected cc-123, got %q", got)
	}
}

// An explicit opencode session header beats body metadata: if a client-side
// plugin ever injected a static metadata.user_id, all opencode sessions would
// collide on one thread again.
func TestAnthropicThreadID_OpencodeHeaderBeatsBodyMetadata(t *testing.T) {
	userID, _ := json.Marshal(map[string]string{"session_id": "static-body"})
	req := opencodeLikeRequest()
	req["metadata"] = map[string]any{"user_id": string(userID)}
	h := http.Header{}
	h.Set("x-session-affinity", "ses_abc")
	if got := anthropicThreadID(req, h); got != "opencode:ses_abc" {
		t.Fatalf("expected opencode:ses_abc, got %q", got)
	}
}

// Older Claude Code versions without the session header still resolve via metadata.
func TestAnthropicThreadID_ClaudeCodeBodyMetadataWithoutHeaders(t *testing.T) {
	userID, _ := json.Marshal(map[string]string{"session_id": "cc-body"})
	req := opencodeLikeRequest()
	req["metadata"] = map[string]any{"user_id": string(userID)}
	if got := anthropicThreadID(req, http.Header{}); got != "cc-body" {
		t.Fatalf("expected cc-body, got %q", got)
	}
}

func TestAnthropicThreadID_FallbackToDerived(t *testing.T) {
	req := opencodeLikeRequest()
	want := DeriveThreadID(req)
	if got := anthropicThreadID(req, http.Header{}); got != want {
		t.Fatalf("expected derived %q, got %q", want, got)
	}
}
