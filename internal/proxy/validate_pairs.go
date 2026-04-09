package proxy

import (
	"log"
)

// validateToolPairs scans messages for orphaned tool_result blocks whose
// tool_use_id has no matching tool_use "id" earlier in the conversation.
// Returns the repaired messages slice and the count of removed orphans.
// If no orphans are found, returns the original slice unchanged (zero alloc).
func validateToolPairs(messages []any, logger *log.Logger) ([]any, int) {
	if len(messages) == 0 {
		return messages, 0
	}

	// Pass 1: collect all tool_use IDs
	toolUseIDs := make(map[string]bool)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_use" {
				if id, ok := b["id"].(string); ok {
					toolUseIDs[id] = true
				}
			}
		}
	}

	// Pass 2: find orphaned tool_results
	orphanCount := 0
	result := make([]any, 0, len(messages))

	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			result = append(result, msg)
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			// String content or other — keep as-is
			result = append(result, msg)
			continue
		}

		var cleaned []any
		removed := 0
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				cleaned = append(cleaned, block)
				continue
			}
			if b["type"] == "tool_result" {
				if useID, ok := b["tool_use_id"].(string); ok {
					if !toolUseIDs[useID] {
						removed++
						orphanCount++
						if logger != nil {
							logger.Printf("[validate] removed orphan tool_result (tool_use_id=%s)", useID)
						}
						continue
					}
				}
			}
			cleaned = append(cleaned, block)
		}

		if removed == 0 {
			result = append(result, msg)
			continue
		}

		if len(cleaned) == 0 {
			// Entire message was orphan tool_results — drop message
			continue
		}

		// Rebuild message with cleaned content
		newMsg := make(map[string]any, len(m))
		for k, v := range m {
			newMsg[k] = v
		}
		newMsg["content"] = cleaned
		result = append(result, newMsg)
	}

	if orphanCount == 0 {
		return messages, 0
	}

	// Pass 3: fix alternation violations from removed messages
	result = fixAlternation(result)

	return result, orphanCount
}

// fixAlternation merges consecutive same-role messages to maintain
// the user/assistant alternation required by the Anthropic API.
func fixAlternation(messages []any) []any {
	if len(messages) < 2 {
		return messages
	}

	fixed := make([]any, 0, len(messages))
	fixed = append(fixed, messages[0])

	for i := 1; i < len(messages); i++ {
		prev, prevOK := fixed[len(fixed)-1].(map[string]any)
		curr, currOK := messages[i].(map[string]any)
		if !prevOK || !currOK {
			fixed = append(fixed, messages[i])
			continue
		}

		if prev["role"] == curr["role"] {
			// Merge: append curr's content to prev
			mergeMessageContent(prev, curr)
		} else {
			fixed = append(fixed, messages[i])
		}
	}

	return fixed
}

// mergeMessageContent appends src's content blocks to dst.
func mergeMessageContent(dst, src map[string]any) {
	dstContent := toContentSlice(dst["content"])
	srcContent := toContentSlice(src["content"])
	dst["content"] = append(dstContent, srcContent...)
}

// toContentSlice normalizes message content to []any.
func toContentSlice(content any) []any {
	switch c := content.(type) {
	case []any:
		return c
	case string:
		return []any{map[string]any{"type": "text", "text": c}}
	default:
		return nil
	}
}
