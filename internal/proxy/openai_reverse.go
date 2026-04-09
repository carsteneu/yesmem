package proxy

import (
	"encoding/json"
	"fmt"
)

// translateAnthropicToOpenAI converts an Anthropic-format request (map[string]any)
// back to OpenAI Chat Completion format for forwarding to OpenAI-compatible upstreams.
// This is the reverse of translateOpenAIToAnthropic.
func translateAnthropicToOpenAI(anthReq map[string]any) (map[string]any, error) {
	oai := map[string]any{}

	// Pass through scalar params
	for _, key := range []string{"model", "max_tokens", "temperature", "top_p", "stream", "tool_choice"} {
		if v, ok := anthReq[key]; ok {
			oai[key] = v
		}
	}
	if meta, ok := anthReq["metadata"]; ok {
		oai["metadata"] = meta
	}

	var result []any

	// Convert system blocks → role:system messages
	if sys, ok := anthReq["system"].([]any); ok {
		for _, block := range sys {
			if bm, ok := block.(map[string]any); ok {
				text, _ := bm["text"].(string)
				role, _ := bm["_openai_role"].(string)
				if role == "" {
					role = "system"
				}
				result = append(result, map[string]any{
					"role":    role,
					"content": text,
				})
			}
		}
	}

	// Convert messages
	messages, _ := anthReq["messages"].([]any)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)

		switch role {
		case "user":
			oaiMsgs := translateAnthropicUserMsg(m)
			result = append(result, oaiMsgs...)

		case "assistant":
			oaiMsg := translateAnthropicAssistantMsg(m)
			result = append(result, oaiMsg)

		default:
			result = append(result, m)
		}
	}

	oai["messages"] = result

	// Convert tools: input_schema → parameters, wrap in function
	if tools, ok := anthReq["tools"].([]any); ok {
		var oaiTools []any
		for _, tool := range tools {
			tm, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			oaiTools = append(oaiTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tm["name"],
					"description": tm["description"],
					"parameters":  tm["input_schema"],
				},
			})
		}
		oai["tools"] = oaiTools
	}

	return oai, nil
}

// translateAnthropicUserMsg handles user messages which may contain tool_result blocks.
// Anthropic groups tool_results inside a user message; OpenAI expects separate role:tool messages.
func translateAnthropicUserMsg(m map[string]any) []any {
	content := m["content"]

	// Simple string content
	if text, ok := content.(string); ok {
		return []any{map[string]any{"role": "user", "content": text}}
	}

	// Array content — may contain text blocks and/or tool_result blocks
	blocks, ok := content.([]any)
	if !ok {
		return []any{m}
	}

	var textParts []string
	var toolResults []any

	for _, block := range blocks {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		btype, _ := bm["type"].(string)

		switch btype {
		case "tool_result":
			toolUseID, _ := bm["tool_use_id"].(string)
			resultContent := extractToolResultContent(bm["content"])
			toolResults = append(toolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": toolUseID,
				"content":      resultContent,
			})
		case "text":
			text, _ := bm["text"].(string)
			if text != "" {
				textParts = append(textParts, text)
			}
		}
	}

	var result []any
	// If there's text alongside tool results, emit as user message first
	if len(textParts) > 0 {
		joined := textParts[0]
		for _, p := range textParts[1:] {
			joined += "\n" + p
		}
		result = append(result, map[string]any{"role": "user", "content": joined})
	}
	// Emit each tool result as separate role:tool message
	result = append(result, toolResults...)

	if len(result) == 0 {
		return []any{map[string]any{"role": "user", "content": ""}}
	}
	return result
}

// translateAnthropicAssistantMsg converts assistant content blocks to OpenAI format.
// text blocks → content string, tool_use blocks → tool_calls array.
func translateAnthropicAssistantMsg(m map[string]any) map[string]any {
	content := m["content"]

	// Simple string content (rare but possible after stubbing)
	if text, ok := content.(string); ok {
		return map[string]any{"role": "assistant", "content": text}
	}

	blocks, ok := content.([]any)
	if !ok {
		return map[string]any{"role": "assistant", "content": ""}
	}

	var textParts []string
	var toolCalls []any

	for _, block := range blocks {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		btype, _ := bm["type"].(string)

		switch btype {
		case "text":
			text, _ := bm["text"].(string)
			textParts = append(textParts, text)
		case "tool_use":
			id, _ := bm["id"].(string)
			name, _ := bm["name"].(string)
			args, _ := json.Marshal(bm["input"])
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(args),
				},
			})
		}
	}

	oaiMsg := map[string]any{"role": "assistant"}

	joined := ""
	for i, p := range textParts {
		if i > 0 {
			joined += "\n"
		}
		joined += p
	}
	oaiMsg["content"] = joined

	if len(toolCalls) > 0 {
		oaiMsg["tool_calls"] = toolCalls
	}

	return oaiMsg
}

// extractToolResultContent normalizes tool_result content to a string.
func extractToolResultContent(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	if blocks, ok := content.([]any); ok {
		var parts []string
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok {
				if text, ok := bm["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return parts[0]
			// join if multiple
		}
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprintf("%v", content)
	}
	return string(data)
}
