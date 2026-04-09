package proxy

import "encoding/json"

// perMessageOverhead accounts for role markers, delimiters, and structural
// tokens that the API adds per message (~4 tokens).
const perMessageOverhead = 4

// countContentTokens estimates tokens by extracting actual text content
// from messages instead of tokenizing the JSON wire format.
// This gives accurate estimates that match actual API token counts,
// unlike tokenizing json.Marshal output which inflates counts 10-100x
// for tool_use/tool_result heavy conversations.
func countContentTokens(messages []any, tokenize func(string) int) int {
	total := 0
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		total += perMessageOverhead
		total += extractContentTokens(m["content"], tokenize)
	}
	return total
}

// estimateMessageContentTokens estimates tokens for a single message
// using content extraction instead of JSON marshaling.
func estimateMessageContentTokens(msg map[string]any, tokenize func(string) int) int {
	return perMessageOverhead + extractContentTokens(msg["content"], tokenize)
}

// extractContentTokens recursively extracts and tokenizes text content
// from a message's content field. Handles both string content and
// content block arrays (text, thinking, tool_use, tool_result).
func extractContentTokens(content any, tokenize func(string) int) int {
	switch c := content.(type) {
	case string:
		return tokenize(c)

	case []any:
		total := 0
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := b["type"].(string)

			switch blockType {
			case "text":
				if text, ok := b["text"].(string); ok {
					total += tokenize(text)
				}

			case "thinking":
				// API uses "thinking" key, but some test data uses "text"
				if text, ok := b["thinking"].(string); ok {
					total += tokenize(text)
				} else if text, ok := b["text"].(string); ok {
					total += tokenize(text)
				}

			case "tool_use":
				// Count tool name + serialized input
				if name, ok := b["name"].(string); ok {
					total += tokenize(name)
				}
				if input, ok := b["input"]; ok {
					data, err := json.Marshal(input)
					if err == nil {
						total += tokenize(string(data))
					}
				}

			case "tool_result":
				// tool_result content can be string or nested blocks
				if nested, ok := b["content"]; ok {
					total += extractContentTokens(nested, tokenize)
				}
				// Also count tool_use_id (small but present)
				if id, ok := b["tool_use_id"].(string); ok {
					total += tokenize(id)
				}

			default:
				// Unknown block type — fall back to JSON marshal
				data, err := json.Marshal(b)
				if err == nil {
					total += tokenize(string(data))
				}
			}
		}
		return total

	default:
		return 0
	}
}
