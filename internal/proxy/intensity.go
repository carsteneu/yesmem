package proxy

import "strings"

// estimateIntensity estimates the emotional intensity of the current conversation
// from structural signals. Returns 0.0-1.0. Language-agnostic.
func estimateIntensity(messages []any) float64 {
	intensity := 0.0
	recent := lastN(messages, 10)

	// Errors in recent messages → debugging loop
	errors := countErrors(recent)
	intensity += float64(errors) * 0.15

	// Tool call density (many tools = intensive work)
	toolCalls := countToolUses(recent)
	if toolCalls > 5 {
		intensity += 0.2
	}

	// Long user messages (> 500 chars = complex request)
	if lastUserMsgLen(messages) > 500 {
		intensity += 0.15
	}

	if intensity > 1.0 {
		return 1.0
	}
	return intensity
}

// countErrors counts tool_result blocks with is_error=true or non-zero exit codes.
func countErrors(messages []any) int {
	count := 0
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_result" {
				if isErr, ok := b["is_error"].(bool); ok && isErr {
					count++
					continue
				}
				if content, ok := b["content"].(string); ok {
					if strings.Contains(content, "exit code") && !strings.Contains(content, "exit code 0") {
						count++
					}
				}
			}
		}
	}
	return count
}

// countToolUses counts tool_use blocks in messages.
func countToolUses(messages []any) int {
	count := 0
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_use" {
				count++
			}
		}
	}
	return count
}

// lastUserMsgLen returns the rune count of the last user message.
func lastUserMsgLen(messages []any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "user" {
			continue
		}
		text := extractTextFromContent(m["content"])
		return len([]rune(text))
	}
	return 0
}

// lastN returns the last n elements of a slice.
func lastN(messages []any, n int) []any {
	if len(messages) <= n {
		return messages
	}
	return messages[len(messages)-n:]
}
