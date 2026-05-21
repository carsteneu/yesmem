package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
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

	// Convert system blocks → role:system messages.
	if sys, ok := anthReq["system"].([]any); ok {
		for _, block := range sys {
			if bm, ok := block.(map[string]any); ok {
				text, _ := bm["text"].(string)
				role, _ := bm["_openai_role"].(string)
				if role == "" {
					role = "system"
				}
			msg := map[string]any{
				"role":    role,
				"content": text,
			}
			if cc, ok := bm["cache_control"]; ok {
				msg["cache_control"] = cc
			}
			result = append(result, msg)
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

	// Drop trailing empty user messages (artifacts from tool_result container split).
	for len(result) > 0 {
		last, ok := result[len(result)-1].(map[string]any)
		if !ok || last["role"] != "user" {
			break
		}
		if content, _ := last["content"].(string); content != "" {
			break
		}
		result = result[:len(result)-1]
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
			schema := normalizeSchema(tm["input_schema"])
			oaiTools = append(oaiTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tm["name"],
					"description": tm["description"],
					"parameters":  schema,
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
	var cacheControl any

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
			log.Printf("OPENAI-REV: tool_result id=%s content_len=%d", toolUseID, len(resultContent))
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
		// Preserve cache_control from the last content block that has it.
		if cc, ok := bm["cache_control"]; ok {
			cacheControl = cc
		}
	}

	// Option B: merge user text into first tool message to maintain tool_use→tool adjacency.
	// DeepSeek requires tool messages immediately after assistant tool_calls.
	if len(textParts) > 0 && len(toolResults) > 0 {
		joined := textParts[0]
		for _, p := range textParts[1:] {
			joined += "\n" + p
		}
		if tm, ok := toolResults[0].(map[string]any); ok {
			tm["content"] = joined + "\n" + fmt.Sprintf("%v", tm["content"])
		}
	}

	var result []any
	// Text-only array (no tool_results): emit a single user message with joined
	// text. The sawtooth path (appendToLastUserMessage) converts string content
	// to []text-block when injecting associative/docs/rules context — without
	// this branch the user's latest turn is silently dropped. Direct
	// concatenation: appendToLastUserMessage already prepends "\n" to injected
	// blocks, so adding another separator would double newlines.
	if len(toolResults) == 0 && len(textParts) > 0 {
		joined := strings.Join(textParts, "")
		umsg := map[string]any{"role": "user", "content": joined}
		if cacheControl != nil {
			umsg["cache_control"] = cacheControl
		}
		result = append(result, umsg)
		return result
	}

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
	var reasoningText string
	var cacheControl any

	for _, block := range blocks {
		bm, ok := block.(map[string]any)
		if !ok {
			continue
		}
		btype, _ := bm["type"].(string)

		switch btype {
		case "thinking":
			if t, ok := bm["thinking"].(string); ok && t != "" {
				if reasoningText != "" {
					reasoningText += "\n"
				}
				reasoningText += t
			}
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
		if cc, ok := bm["cache_control"]; ok {
			cacheControl = cc
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

	if reasoningText != "" {
		oaiMsg["reasoning_content"] = reasoningText
	}

	if len(toolCalls) > 0 {
		oaiMsg["tool_calls"] = toolCalls
	}

	if cacheControl != nil {
		oaiMsg["cache_control"] = cacheControl
	}

	return oaiMsg
}

// normalizeSchema ensures array-type properties have "items" defined.
// DeepSeek enforces strict JSON Schema validation that rejects arrays without items.
func normalizeSchema(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		switch k {
		case "properties":
			if props, ok := v.(map[string]any); ok {
				normalized := make(map[string]any, len(props))
				for pk, pv := range props {
					normalized[pk] = normalizeSchema(pv)
				}
				result[k] = normalized
			} else {
				result[k] = v
			}
		case "items":
			result[k] = normalizeSchema(v)
		default:
			result[k] = v
		}
	}
	if t, _ := result["type"].(string); t == "array" {
		if _, hasItems := result["items"]; !hasItems {
			result["items"] = map[string]any{"type": "string"}
		}
	}
	return result
}
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
			return strings.Join(parts, "\n")
		}
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprintf("%v", content)
	}
	return string(data)
}
