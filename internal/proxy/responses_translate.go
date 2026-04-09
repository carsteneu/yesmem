package proxy

import (
	"encoding/json"
	"fmt"
)

// translateResponsesToAnthropic converts an OpenAI Responses API request (map[string]any)
// to Anthropic internal format for the compression pipeline.
// Mapping: instructions → system[], input items → messages[],
// function_call → assistant tool_use, function_call_output → user tool_result.
func translateResponsesToAnthropic(req map[string]any) (map[string]any, error) {
	anthReq := map[string]any{}

	// Pass through scalar params
	for _, key := range []string{"model", "stream"} {
		if v, ok := req[key]; ok {
			anthReq[key] = v
		}
	}
	if v, ok := req["max_output_tokens"]; ok {
		anthReq["max_tokens"] = v
	}
	if v, ok := req["temperature"]; ok {
		anthReq["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		anthReq["top_p"] = v
	}
	if v, ok := req["metadata"]; ok {
		anthReq["metadata"] = v
	}

	// instructions → system[]
	if instructions, ok := req["instructions"].(string); ok && instructions != "" {
		anthReq["system"] = []any{
			map[string]any{"type": "text", "text": instructions},
		}
	}

	// input → messages
	var messages []any
	switch input := req["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": input})
	case []any:
		messages = translateResponsesInputItems(input)
	default:
		return nil, fmt.Errorf("unsupported input type: %T", req["input"])
	}
	anthReq["messages"] = messages

	// tools: parameters → input_schema
	if tools, ok := req["tools"].([]any); ok {
		var anthTools []any
		for _, tool := range tools {
			tm, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			toolType, _ := tm["type"].(string)
			if toolType != "function" {
				// Non-function tools (code_interpreter, file_search, etc.)
				// have no name field — skip Anthropic conversion, stash for passthrough
				continue
			}
			anthTools = append(anthTools, map[string]any{
				"name":         tm["name"],
				"description":  tm["description"],
				"input_schema": tm["parameters"],
			})
		}
		anthReq["tools"] = anthTools
		// Stash original tools for reverse translation passthrough
		anthReq["_original_tools"] = tools
	}

	return anthReq, nil
}

// translateResponsesInputItems converts Responses API input items to Anthropic messages.
// Handles: role-based messages, function_call → assistant tool_use, function_call_output → user tool_result.
func translateResponsesInputItems(items []any) []any {
	var messages []any

	for i := 0; i < len(items); i++ {
		im, ok := items[i].(map[string]any)
		if !ok {
			continue
		}

		itemType, _ := im["type"].(string)
		role, _ := im["role"].(string)

		switch itemType {
		case "function_call":
			var toolUses []any
			for i < len(items) {
				call, ok := items[i].(map[string]any)
				if !ok {
					break
				}
				if callType, _ := call["type"].(string); callType != "function_call" {
					break
				}
				callID, _ := call["call_id"].(string)
				name, _ := call["name"].(string)
				argsStr, _ := call["arguments"].(string)
				var argsObj any
				if json.Unmarshal([]byte(argsStr), &argsObj) != nil {
					argsObj = map[string]any{}
				}
				toolUses = append(toolUses, map[string]any{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": argsObj,
				})
				i++
			}
			messages = append(messages, map[string]any{
				"role":    "assistant",
				"content": toolUses,
			})
			i--

		case "function_call_output":
			var toolResults []any
			for i < len(items) {
				call, ok := items[i].(map[string]any)
				if !ok {
					break
				}
				if callType, _ := call["type"].(string); callType != "function_call_output" {
					break
				}
				callID, _ := call["call_id"].(string)
				output, _ := call["output"].(string)
				toolResults = append(toolResults, map[string]any{
					"type":        "tool_result",
					"tool_use_id": callID,
					"content":     output,
				})
				i++
			}
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": toolResults,
			})
			i--

		default:
			// Regular message (role: user/assistant/system)
			if role == "" {
				role = "user"
			}
			content := im["content"]
			if content == nil {
				content = ""
			}
			messages = append(messages, map[string]any{
				"role":    role,
				"content": content,
			})
		}
	}

	return messages
}

// translateAnthropicToResponses converts Anthropic internal format back to
// OpenAI Responses API format for forwarding to OpenAI upstream.
// Reverse of translateResponsesToAnthropic.
func translateAnthropicToResponses(anthReq map[string]any) (map[string]any, error) {
	resp := map[string]any{}

	// Pass through scalar params
	for _, key := range []string{"model", "stream"} {
		if v, ok := anthReq[key]; ok {
			resp[key] = v
		}
	}
	if v, ok := anthReq["max_tokens"]; ok {
		resp["max_output_tokens"] = v
	}
	if v, ok := anthReq["temperature"]; ok {
		resp["temperature"] = v
	}
	if v, ok := anthReq["top_p"]; ok {
		resp["top_p"] = v
	}
	if v, ok := anthReq["metadata"]; ok {
		resp["metadata"] = v
	}

	// system[] → instructions
	if sys, ok := anthReq["system"].([]any); ok && len(sys) > 0 {
		var parts []string
		for _, block := range sys {
			if bm, ok := block.(map[string]any); ok {
				if text, ok := bm["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) == 1 {
			resp["instructions"] = parts[0]
		} else if len(parts) > 1 {
			joined := parts[0]
			for _, p := range parts[1:] {
				joined += "\n" + p
			}
			resp["instructions"] = joined
		}
	}

	// messages → input items
	messages, _ := anthReq["messages"].([]any)
	var input []any

	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content := m["content"]

		// Check for content blocks (tool_use, tool_result)
		if blocks, ok := content.([]any); ok {
			for _, block := range blocks {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				btype, _ := bm["type"].(string)

				switch btype {
				case "tool_use":
					id, _ := bm["id"].(string)
					name, _ := bm["name"].(string)
					argsBytes, _ := json.Marshal(bm["input"])
					input = append(input, map[string]any{
						"type":      "function_call",
						"call_id":   id,
						"name":      name,
						"arguments": string(argsBytes),
					})
				case "tool_result":
					toolUseID, _ := bm["tool_use_id"].(string)
					resultContent := extractToolResultContent(bm["content"])
					input = append(input, map[string]any{
						"type":    "function_call_output",
						"call_id": toolUseID,
						"output":  resultContent,
					})
				case "text":
					text, _ := bm["text"].(string)
					input = append(input, map[string]any{
						"role":    role,
						"content": text,
					})
				default:
					text, _ := bm["text"].(string)
					if text != "" {
						input = append(input, map[string]any{
							"role":    role,
							"content": text,
						})
					}
				}
			}
		} else {
			// Simple string content
			input = append(input, map[string]any{
				"role":    role,
				"content": content,
			})
		}
	}

	resp["input"] = input

	// tools: use original tools if stashed (preserves non-function tools like code_interpreter)
	if origTools, ok := anthReq["_original_tools"].([]any); ok {
		resp["tools"] = origTools
	} else if tools, ok := anthReq["tools"].([]any); ok {
		var respTools []any
		for _, tool := range tools {
			tm, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			respTools = append(respTools, map[string]any{
				"type":        "function",
				"name":        tm["name"],
				"description": tm["description"],
				"parameters":  tm["input_schema"],
			})
		}
		resp["tools"] = respTools
	}

	return resp, nil
}
