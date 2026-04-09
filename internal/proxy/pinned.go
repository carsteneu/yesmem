package proxy

import "strings"

// isActiveDebug checks if a message is part of an active error→fix sequence.
// Protects error tool_results and the immediate assistant response after them.
func isActiveDebug(messages []any, idx int) bool {
	msg, ok := messages[idx].(map[string]any)
	if !ok {
		return false
	}

	// Check if this message contains an error tool_result
	if hasErrorToolResult(msg) {
		return true
	}

	// Check if the previous message was an error (this is the fix response)
	if idx > 0 {
		if prev, ok := messages[idx-1].(map[string]any); ok {
			if hasErrorToolResult(prev) {
				return true
			}
		}
	}

	return false
}

// hasErrorToolResult checks if a message contains a tool_result with is_error=true
// or a non-zero exit code.
func hasErrorToolResult(msg map[string]any) bool {
	blocks, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] != "tool_result" {
			continue
		}
		if isErr, ok := b["is_error"].(bool); ok && isErr {
			return true
		}
		if content, ok := b["content"].(string); ok {
			if strings.Contains(content, "exit code") && !strings.Contains(content, "exit code 0") {
				return true
			}
		}
	}
	return false
}

// isTaskList checks if a message contains TODO items or checklists.
func isTaskList(text string) bool {
	if text == "" {
		return false
	}
	return strings.Contains(text, "- [ ]") ||
		strings.Contains(text, "- [x]") ||
		strings.Contains(text, "TODO:")
}

// isProtectedExtended combines all protection checks: pivot moments,
// active debug sequences, and task lists.
func isProtectedExtended(messages []any, idx int, pivotTexts []string) bool {
	msg, ok := messages[idx].(map[string]any)
	if !ok {
		return false
	}

	text := extractTextFromContent(msg["content"])

	// Original pivot moment protection
	if isProtected(text, pivotTexts) {
		return true
	}

	// Active debug protection
	if isActiveDebug(messages, idx) {
		return true
	}

	// Task list protection
	if isTaskList(text) {
		return true
	}

	return false
}
