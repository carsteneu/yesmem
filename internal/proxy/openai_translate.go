package proxy

import (
	"encoding/json"
	"fmt"
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
			result = append(result, map[string]any{
				"role":    "user",
				"content": openAIContentText(m.Content),
			})

		case "assistant":
			var blocks []any
			if text := openAIContentText(m.Content); text != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": text,
				})
			}
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
			var toolBlocks []any
			for i < len(msgs) && msgs[i].Role == "tool" {
				content, _ := msgs[i].Content.(string)
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
