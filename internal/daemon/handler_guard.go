package daemon

import (
	"encoding/json"

	"github.com/carsteneu/yesmem/internal/guard"
)

// handleGuardCheck judges one tool call for a harness that is not Claude Code. The opencode plugin dials it:
// the plugin owns the event wiring and the rendering, the daemon owns the rules, the model and the policy,
// exactly as the hook does through the same internal/guard core.
func (h *Handler) handleGuardCheck(params map[string]any) Response {
	tool := stringOr(params, "tool", "")
	if tool == "" {
		return errorResponse("guard_check: tool is required")
	}
	cwd := stringOr(params, "cwd", "")
	// An empty rules text is not a reason to skip the call: the deterministic half of the guard needs no rules,
	// and Check answers PASS with a reason when there is nothing to judge against.
	rules := guard.LoadRules(h.dataDir, cwd)
	decision := guard.Check(guard.Request{
		Tool:      tool,
		ToolInput: toolInputJSON(params["tool_input"]),
		CWD:       cwd,
		SessionID: stringOr(params, "session_id", ""),
		// One RPC, two templates: the nudge suggests a skill, the guard judges a call.
		Purpose: stringOr(params, "purpose", ""),
		// The harness's own addendum (recent conversation, branch note). The daemon has no session view for an
		// opencode session, so the plugin builds it.
		Context: stringOr(params, "context", ""),
		Rules:   rules,
		DataDir: h.dataDir,
	})
	return jsonResponse(decision)
}

// toolInputJSON keeps the call's arguments as JSON. A missing or unreadable value becomes an empty object:
// the guard must still see the tool rather than fail on a malformed field.
func toolInputJSON(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return json.RawMessage("{}")
	}
	return encoded
}
