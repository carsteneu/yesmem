package proxy

import (
	"log"
)

// validateToolPairs scans messages for tool pairing violations and repairs
// them before forwarding to the Anthropic API:
//   - Pass 2: removes orphaned tool_result blocks whose tool_use_id has no
//     matching tool_use "id" earlier in the conversation.
//   - Pass 3: synthesizes tool_result blocks for naked tool_use blocks that
//     lack a matching tool_result in the immediately following message.
//
// Returns the repaired messages slice and the total count of repairs.
// If no repairs are needed, returns the original slice unchanged (zero alloc).
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

	// Pass 3: synthesize missing tool_results for naked tool_use blocks.
	// After Stubify/Collapse/Injection, a tool_use block may end up without
	// a matching tool_result in the next message. The Anthropic API requires
	// each tool_use to have a corresponding tool_result immediately after.
	result, synthesizedCount := synthesizeMissingToolResults(result, logger)

	if orphanCount == 0 && synthesizedCount == 0 {
		return messages, 0
	}

	// Pass 4: fix alternation violations from removed/inserted messages
	result = fixAlternation(result)

	return result, orphanCount + synthesizedCount
}

// synthesizeMissingToolResults scans for assistant messages containing tool_use
// blocks whose IDs do not appear in a tool_result in the immediately following
// user message. For each missing result, it injects a synthetic tool_result
// to satisfy the API's pairing requirement.
func synthesizeMissingToolResults(messages []any, logger *log.Logger) ([]any, int) {
	synthesized := 0
	result := make([]any, 0, len(messages)+4)

	for i := 0; i < len(messages); i++ {
		result = append(result, messages[i])

		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		var toolUseIDs []string
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_use" {
				if id, ok := b["id"].(string); ok {
					toolUseIDs = append(toolUseIDs, id)
				}
			}
		}
		if len(toolUseIDs) == 0 {
			continue
		}

		nextToolResultIDs := make(map[string]bool)
		var nextMsg map[string]any
		if i+1 < len(messages) {
			nextMsg, _ = messages[i+1].(map[string]any)
			if nextMsg != nil && nextMsg["role"] == "user" {
				if nextContent, ok := nextMsg["content"].([]any); ok {
					for _, block := range nextContent {
						b, ok := block.(map[string]any)
						if !ok {
							continue
						}
						if b["type"] == "tool_result" {
							if id, ok := b["tool_use_id"].(string); ok {
								nextToolResultIDs[id] = true
							}
						}
					}
				}
			}
		}

		var missingIDs []string
		for _, id := range toolUseIDs {
			if !nextToolResultIDs[id] {
				missingIDs = append(missingIDs, id)
			}
		}
		if len(missingIDs) == 0 {
			continue
		}

		var synthBlocks []any
		for _, id := range missingIDs {
			synthBlocks = append(synthBlocks, map[string]any{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     "[proxy: synthesized tool_result — original result was lost during context compression]",
			})
			synthesized++
			if logger != nil {
				logger.Printf("[validate] synthesized tool_result for naked tool_use (id=%s)", id)
			}
		}

		if nextMsg != nil && nextMsg["role"] == "user" {
			var newContent []any
			switch c := nextMsg["content"].(type) {
			case []any:
				newContent = make([]any, 0, len(c)+len(synthBlocks))
				newContent = append(newContent, c...)
				newContent = append(newContent, synthBlocks...)
			case string:
				newContent = make([]any, 0, 1+len(synthBlocks))
				newContent = append(newContent, map[string]any{"type": "text", "text": c})
				newContent = append(newContent, synthBlocks...)
			default:
				newContent = synthBlocks
			}
			newMsg := make(map[string]any, len(nextMsg))
			for k, v := range nextMsg {
				newMsg[k] = v
			}
			newMsg["content"] = newContent
			messages[i+1] = newMsg
		} else {
			result = append(result, map[string]any{
				"role":    "user",
				"content": synthBlocks,
			})
		}
	}

	return result, synthesized
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
