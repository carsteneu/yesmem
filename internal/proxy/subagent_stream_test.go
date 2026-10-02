package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// rawHeaderRequest builds a request whose header MAP holds the exact keys given.
// net/http canonicalizes the names of headers it parses off the wire, so this
// pins the defensive path for a programmatically built request; http.Header.Set
// would canonicalize the key and hide it.
func rawHeaderRequest(raw map[string]string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header = http.Header{}
	for k, v := range raw {
		r.Header[k] = []string{v}
	}
	return r
}

// TestSubagentStreamInfo_Fork173HeaderSet reproduces the opencode fork's header
// set after the 1.18.34 merge (builds >= 1.18.34-patched.173, Learning #96632):
// no X-Opencode-Agent-Type, no X-Opencode-Parent-Session; the parent marker is
// x-opencode-parent-session-id and every request carries x-opencode-session-id.
func TestSubagentStreamInfo_Fork173HeaderSet(t *testing.T) {
	sub := map[string]string{
		"x-opencode-session-id":        "ses_child",
		"x-opencode-parent-session-id": "ses_parent",
		"x-parent-session-id":          "ses_parent",
		"x-session-affinity":           "ses_child",
		"X-Session-Id":                 "ses_child",
	}
	isSub, parent := (&Server{}).subagentStreamInfo(rawHeaderRequest(sub), []byte(`{"model":"m"}`))
	if !isSub || parent != "opencode:ses_parent" {
		t.Errorf("fork .173 subagent: isSub=%v parent=%q, want true/opencode:ses_parent", isSub, parent)
	}

	main := map[string]string{
		"x-opencode-session-id": "ses_main",
		"x-session-affinity":    "ses_main",
		"X-Session-Id":          "ses_main",
	}
	isSub, parent = (&Server{}).subagentStreamInfo(rawHeaderRequest(main), []byte(`{"model":"m"}`))
	if isSub || parent != "" {
		t.Errorf("fork .173 main session: isSub=%v parent=%q, want false/empty", isSub, parent)
	}
}

// TestSubagentStreamInfo_LowercaseRawHeaderKeys pins the defensive lookup path
// for a header map with non-canonical keys.
func TestSubagentStreamInfo_LowercaseRawHeaderKeys(t *testing.T) {
	r := rawHeaderRequest(map[string]string{"x-opencode-parent-session-id": "ses_lc"})
	isSub, parent := (&Server{}).subagentStreamInfo(r, []byte(`{"model":"m"}`))
	if !isSub || parent != "opencode:ses_lc" {
		t.Errorf("lowercase raw header key: isSub=%v parent=%q, want true/opencode:ses_lc", isSub, parent)
	}
}

// TestSubagentStreamInfo_PrefersNamespacedParentHeader pins the documented
// order: the namespaced parent header is the primary source, so it wins when a
// legacy and a namespaced parent header disagree.
func TestSubagentStreamInfo_PrefersNamespacedParentHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("X-Opencode-Parent-Session-Id", "ses_upstream")
	r.Header.Set("X-Parent-Session-Id", "ses_generic")
	isSub, parent := (&Server{}).subagentStreamInfo(r, []byte(`{"model":"m"}`))
	if !isSub || parent != "opencode:ses_upstream" {
		t.Errorf("namespaced parent header must win: isSub=%v parent=%q", isSub, parent)
	}
}

func TestSubagentStreamInfo_OpenCodeHeaders(t *testing.T) {
	s := &Server{}
	body := []byte(`{"model":"gateway/privateTomMax"}`)

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("X-Opencode-Agent-Type", "subagent")
	r.Header.Set("X-Opencode-Parent-Session", "ses_abc123")

	isSub, parentThread := s.subagentStreamInfo(r, body)
	if !isSub {
		t.Error("isSub should be true for opencode subagent headers")
	}
	if parentThread != "opencode:ses_abc123" {
		t.Errorf("parentThread = %q, want %q", parentThread, "opencode:ses_abc123")
	}
}

// TestSubagentStreamInfo_HeaderMatrix covers both fork generations: the current
// one (x-opencode-parent-session-id, upstream #52370, builds >= .173) and the
// legacy one (X-Opencode-Agent-Type + X-Opencode-Parent-Session), so either side
// can change independently without silently losing subagent attribution.
func TestSubagentStreamInfo_HeaderMatrix(t *testing.T) {
	const body = `{"model":"gateway/privateTomMax"}`
	tests := []struct {
		name       string
		headers    map[string]string
		body       string
		wantSub    bool
		wantParent string
	}{
		{
			name:       "fork-only",
			headers:    map[string]string{"X-Opencode-Agent-Type": "subagent", "X-Opencode-Parent-Session": "ses_fork1"},
			body:       body,
			wantSub:    true,
			wantParent: "opencode:ses_fork1",
		},
		{
			name:       "fork-parent-header-only",
			headers:    map[string]string{"x-parent-session-id": "ses_fork2"},
			body:       body,
			wantSub:    true,
			wantParent: "opencode:ses_fork2",
		},
		{
			name:       "upstream-only",
			headers:    map[string]string{"x-opencode-parent-session-id": "ses_up1"},
			body:       body,
			wantSub:    true,
			wantParent: "opencode:ses_up1",
		},
		{
			// The namespaced parent header is the primary source, so it wins
			// over the legacy fork header when both are present.
			name: "both",
			headers: map[string]string{
				"X-Opencode-Agent-Type":        "subagent",
				"X-Opencode-Parent-Session":    "ses_fork3",
				"x-opencode-parent-session-id": "ses_up3",
			},
			body:       body,
			wantSub:    true,
			wantParent: "opencode:ses_up3",
		},
		{
			name:    "none",
			headers: map[string]string{},
			body:    body,
			wantSub: false,
		},
		{
			name:       "agent-type-only",
			headers:    map[string]string{"X-Opencode-Agent-Type": "subagent"},
			body:       body,
			wantSub:    true,
			wantParent: "",
		},
		{
			name:       "claude-code-body",
			headers:    map[string]string{},
			body:       `{"metadata":{"user_id":"{\"agent_type\":\"subagent\"}"}}`,
			wantSub:    true,
			wantParent: "",
		},
		{
			// A body-detected subagent keeps the Claude path: the body branch
			// short-circuits, so parent stays empty (Claude code has no parent header).
			name:       "claude-code-body-plus-opencode-header",
			headers:    map[string]string{"x-opencode-parent-session-id": "ses_up9"},
			body:       `{"metadata":{"user_id":"{\"agent_type\":\"subagent\"}"}}`,
			wantSub:    true,
			wantParent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{}
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			isSub, parentThread := s.subagentStreamInfo(r, []byte(tt.body))
			if isSub != tt.wantSub {
				t.Errorf("isSub = %v, want %v", isSub, tt.wantSub)
			}
			if parentThread != tt.wantParent {
				t.Errorf("parentThread = %q, want %q", parentThread, tt.wantParent)
			}
		})
	}
}

func TestSubagentStreamInfo_MainRequest(t *testing.T) {
	s := &Server{}
	body := []byte(`{"model":"gateway/privateTomMax"}`)

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	isSub, parentThread := s.subagentStreamInfo(r, body)
	if isSub {
		t.Error("isSub should be false without subagent markers")
	}
	if parentThread != "" {
		t.Errorf("parentThread = %q, want empty", parentThread)
	}
}

func TestSubagentStreamInfo_ClaudeCodeBody(t *testing.T) {
	s := &Server{}
	body := []byte(`{"metadata":{"user_id":"{\"agent_type\":\"subagent\"}"}}`)
	r := httptest.NewRequest("POST", "/v1/messages", nil)

	isSub, parentThread := s.subagentStreamInfo(r, body)
	if !isSub {
		t.Error("isSub should be true for claude code subagent body")
	}
	if parentThread != "" {
		t.Errorf("parentThread = %q, want empty", parentThread)
	}
}
