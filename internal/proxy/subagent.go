package proxy

import (
	"encoding/json"
	"strings"
)

// isSubagent detects subagent requests that don't need full proxy processing.
// Primary marker: subagents have no "thinking" field (no extended thinking).
// Secondary markers: sdk-ts entrypoint, haiku model.
func isSubagent(messages []any, req map[string]any) bool {
	if len(messages) == 0 {
		return false
	}
	// Check billing header in system block for entrypoint
	if sys, ok := req["system"].([]any); ok && len(sys) > 0 {
		if b, ok := sys[0].(map[string]any); ok {
			header, _ := b["text"].(string)
			if strings.Contains(header, "cc_entrypoint=sdk-ts") {
				return true
			}
		}
	}
	// Haiku model = extraction pipeline or lightweight subagent
	model, _ := req["model"].(string)
	if strings.Contains(model, "haiku") {
		return true
	}
	// Primary marker: subagents have no extended thinking
	if _, hasThinking := req["thinking"]; !hasThinking {
		return true
	}
	return false
}

// injectDocsHintForSubagent adds a docs-available reminder to a subagent's messages.
// Returns the original messages unchanged if hint is empty or messages is nil.
func injectDocsHintForSubagent(messages []any, hint string) []any {
	if len(messages) == 0 || hint == "" {
		return messages
	}
	injected := make([]any, len(messages), len(messages)+1)
	copy(injected, messages)
	injected = append(injected, map[string]any{
		"role":    "user",
		"content": hint,
	})
	return injected
}

// getDocsHint fetches the cached docs hint from daemon (5min TTL on daemon side).
func (s *Server) getDocsHint(project string) string {
	params := map[string]any{}
	if project != "" {
		params["project"] = project
	}
	result, err := s.queryDaemon("get_docs_hint", params)
	if err != nil {
		return ""
	}
	var resp struct {
		DocsHint string `json:"docs_hint"`
	}
	if json.Unmarshal(result, &resp) != nil {
		return ""
	}
	return resp.DocsHint
}
