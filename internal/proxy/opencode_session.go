package proxy

import "net/http"

// opencodeSessionID returns opencode's native session ID from request headers.
// opencode sets x-opencode-session for opencode-branded providers and
// x-session-affinity for all others (anthropic, openai, deepseek, ...). Since
// the 1.18.34 fork merge (builds >= 1.18.34-patched.173, Learning #96632) every
// request additionally carries x-opencode-session-id (upstream #52370), which is
// preferred over the affinity fallback. See
// packages/opencode/src/session/llm/request.ts in the opencode repo.
func opencodeSessionID(h http.Header) string {
	if sid := headerCI(h, "x-opencode-session"); sid != "" {
		return sid
	}
	if sid := headerCI(h, "x-opencode-session-id"); sid != "" {
		return sid
	}
	return headerCI(h, "x-session-affinity")
}

// anthropicThreadID resolves the thread ID for /v1/messages requests.
// Priority: X-Claude-Code-Session-Id header > opencode session header
// ("opencode:<ses_id>", same form as the OpenAI parity path and the MCP layer)
// > metadata.user_id session (older Claude Code) > DeriveThreadID.
// Claude Code never sends the opencode headers, so its resolution is unchanged.
// Without the opencode branch, all opencode sessions hash to the same thread
// ID (identical system prompt) and share per-thread state like the briefing cache.
func anthropicThreadID(req map[string]any, h http.Header) string {
	if cc := h.Get("X-Claude-Code-Session-Id"); cc != "" {
		return cc
	}
	if sid := opencodeSessionID(h); sid != "" {
		return "opencode:" + sid
	}
	if tid := extractSessionID(req, "", ""); tid != "" {
		return tid
	}
	return DeriveThreadID(req)
}
