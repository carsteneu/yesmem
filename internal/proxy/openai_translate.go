package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

func translateOpenAIToAnthropic(oai OpenAIChatRequest) (map[string]any, error) {
	req := map[string]any{
		"model": oai.Model,
	}
	if oai.MaxTokens > 0 {
		req["max_tokens"] = oai.MaxTokens
	}
	if oai.Temperature != nil {
		req["temperature"] = *oai.Temperature
	}
	if oai.TopP != nil {
		req["top_p"] = *oai.TopP
	}
	if oai.Stream {
		req["stream"] = true
	}
	if len(oai.Metadata) > 0 {
		req["metadata"] = oai.Metadata
	}
	if oai.ToolChoice != nil {
		req["tool_choice"] = oai.ToolChoice
	}

	var systemBlocks []any
	var nonSystemMsgs []OpenAIMessage
	for _, m := range oai.Messages {
		switch m.Role {
		case "system", "developer":
			block := map[string]any{
				"type": "text",
				"text": openAIContentText(m.Content),
			}
			if m.Role == "developer" {
				block["_openai_role"] = "developer"
			}
			systemBlocks = append(systemBlocks, block)
		default:
			nonSystemMsgs = append(nonSystemMsgs, m)
		}
	}
	if len(systemBlocks) > 0 {
		req["system"] = systemBlocks
	}

	messages, err := translateMessages(nonSystemMsgs)
	if err != nil {
		return nil, err
	}
	req["messages"] = messages

	if len(oai.Tools) > 0 {
		var tools []any
		for _, t := range oai.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Function.Name,
				"description":  t.Function.Description,
				"input_schema": t.Function.Parameters,
			})
		}
		req["tools"] = tools
	}

	return req, nil
}

func openAIContentText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			bm, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := bm["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	default:
		data, err := json.Marshal(content)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

func translateMessages(msgs []OpenAIMessage) ([]any, error) {
	var result []any

	for i := 0; i < len(msgs); i++ {
		m := msgs[i]

		switch m.Role {
		case "user":
			// Check for tool_result blocks in content array (opencode format).
			if contentArr, ok := m.Content.([]any); ok {
				hasToolResult := false
				for _, block := range contentArr {
					if bm, ok := block.(map[string]any); ok && bm["type"] == "tool_result" {
						hasToolResult = true
						break
					}
				}
				if hasToolResult {
					// Don't emit text as separate user message — opencode sends the text
					// in a separate standalone user message right after.
					for _, block := range contentArr {
						bm, ok := block.(map[string]any)
						if !ok {
							continue
						}
						if bm["type"] == "tool_result" {
							tid, _ := bm["tool_use_id"].(string)
							content := extractToolResultContent(bm["content"])
							result = append(result, map[string]any{
								"role":         "tool",
								"tool_use_id":  tid,
								"tool_call_id": tid,
								"content":      content,
							})
						} else if text, ok := bm["text"].(string); ok && text != "" {
							result = append(result, map[string]any{
								"role":    "user",
								"content": text,
							})
						}
					}
					continue // skip default text extraction
				}
			}
			result = append(result, map[string]any{
				"role":    "user",
				"content": openAIContentText(m.Content),
			})

		case "assistant":
			var blocks []any
			// Preserve reasoning_content (DeepSeek thinking mode requires it in subsequent requests)
			if m.ReasoningContent != "" {
				blocks = append(blocks, map[string]any{
					"type":     "thinking",
					"thinking": m.ReasoningContent,
				})
			}
			// Extract text from content (string or blocks)
			if text := openAIContentText(m.Content); text != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": text,
				})
			}
			// Handle tool_use blocks in content array (opencode skill format)
			if contentArr, ok := m.Content.([]any); ok {
				for _, block := range contentArr {
					if bm, ok := block.(map[string]any); ok && bm["type"] == "tool_use" {
						id, _ := bm["id"].(string)
						name, _ := bm["name"].(string)
						input := bm["input"]
						log.Printf("OPENAI-FWD: assistant tool_use in content id=%s name=%s", id, name)
						blocks = append(blocks, map[string]any{
							"type":  "tool_use",
							"id":    id,
							"name":  name,
							"input": input,
						})
					}
				}
			}
			// Handle standard tool_calls format
			for _, tc := range m.ToolCalls {
				var input any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
					input = map[string]any{"raw": tc.Function.Arguments}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": "",
				})
			}
			result = append(result, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})

		case "tool":
			log.Printf("OPENAI-FWD: tool msg tool_call_id=%s content_type=%T", msgs[i].ToolCallID, msgs[i].Content)
			var toolBlocks []any
			for i < len(msgs) && msgs[i].Role == "tool" {
				content := openAIContentText(msgs[i].Content)
				toolBlocks = append(toolBlocks, map[string]any{
					"type":        "tool_result",
					"tool_use_id": msgs[i].ToolCallID,
					"content":     content,
				})
				i++
			}
			i-- // back up one so the outer loop's i++ lands on the next non-tool message
			result = append(result, map[string]any{
				"role":    "user",
				"content": toolBlocks,
			})

		default:
			return nil, fmt.Errorf("unknown role: %s", m.Role)
		}
	}

	return result, nil
}
