package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateResponsesToAnthropic_BasicMessages(t *testing.T) {
	reqJSON := `{
		"model": "gpt-5.4",
		"instructions": "You are helpful.",
		"input": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		],
		"max_output_tokens": 2048,
		"stream": true
	}`

	var req map[string]any
	json.Unmarshal([]byte(reqJSON), &req)

	anthReq, err := translateResponsesToAnthropic(req)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if anthReq["model"] != "gpt-5.4" {
		t.Errorf("model = %v", anthReq["model"])
	}

	// instructions → system[]
	sys, ok := anthReq["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatal("system block missing")
	}
	sb, _ := sys[0].(map[string]any)
	if sb["text"] != "You are helpful." {
		t.Errorf("system text = %v", sb["text"])
	}

	msgs, _ := anthReq["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}

	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != "Hello" {
		t.Errorf("msg[0] = %v", m0)
	}
}

func TestTranslateResponsesToAnthropic_StringInput(t *testing.T) {
	reqJSON := `{
		"model": "gpt-5.4",
		"input": "Just a simple prompt"
	}`
	var req map[string]any
	json.Unmarshal([]byte(reqJSON), &req)

	anthReq, err := translateResponsesToAnthropic(req)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	msgs, _ := anthReq["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != "Just a simple prompt" {
		t.Errorf("msg[0] = %v", m0)
	}
}

func TestTranslateResponsesToAnthropic_FunctionCalls(t *testing.T) {
	reqJSON := `{
		"model": "gpt-5.4",
		"input": [
			{"role": "user", "content": "Read /tmp/foo"},
			{"type": "function_call", "call_id": "call_1", "name": "read", "arguments": "{\"path\":\"/tmp/foo\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "file contents here"}
		]
	}`
	var req map[string]any
	json.Unmarshal([]byte(reqJSON), &req)

	anthReq, err := translateResponsesToAnthropic(req)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	msgs, _ := anthReq["messages"].([]any)
	// user, assistant(tool_use), user(tool_result)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}

	// function_call → assistant with tool_use content block
	m1, _ := msgs[1].(map[string]any)
	if m1["role"] != "assistant" {
		t.Errorf("msg[1].role = %v, want assistant", m1["role"])
	}
	content, _ := m1["content"].([]any)
	if len(content) == 0 {
		t.Fatal("assistant content empty")
	}
	tu, _ := content[0].(map[string]any)
	if tu["type"] != "tool_use" {
		t.Errorf("content[0].type = %v, want tool_use", tu["type"])
	}
	if tu["id"] != "call_1" {
		t.Errorf("tool_use id = %v", tu["id"])
	}
	if tu["name"] != "read" {
		t.Errorf("tool_use name = %v", tu["name"])
	}

	// function_call_output → user with tool_result content block
	m2, _ := msgs[2].(map[string]any)
	if m2["role"] != "user" {
		t.Errorf("msg[2].role = %v, want user", m2["role"])
	}
	content2, _ := m2["content"].([]any)
	tr, _ := content2[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Errorf("content[0].type = %v, want tool_result", tr["type"])
	}
	if tr["tool_use_id"] != "call_1" {
		t.Errorf("tool_use_id = %v", tr["tool_use_id"])
	}
}

func TestTranslateResponsesToAnthropic_Tools(t *testing.T) {
	reqJSON := `{
		"model": "gpt-5.4",
		"input": [{"role": "user", "content": "Hi"}],
		"tools": [{
			"type": "function",
			"name": "read_file",
			"description": "Read a file",
			"parameters": {"type": "object", "properties": {"path": {"type": "string"}}}
		}]
	}`
	var req map[string]any
	json.Unmarshal([]byte(reqJSON), &req)

	anthReq, err := translateResponsesToAnthropic(req)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	tools, ok := anthReq["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", anthReq["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "read_file" {
		t.Errorf("name = %v", tool["name"])
	}
	if tool["input_schema"] == nil {
		t.Error("input_schema missing (mapped from parameters)")
	}
}

func TestTranslateAnthropicToResponses_BasicResponse(t *testing.T) {
	// After pipeline, convert Anthropic internal format back to Responses API format
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"system": []any{
			map[string]any{"type": "text", "text": "You are helpful."},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Hi there!"},
			}},
		},
	}

	respReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if respReq["model"] != "gpt-5.4" {
		t.Errorf("model = %v", respReq["model"])
	}

	instructions, _ := respReq["instructions"].(string)
	if instructions != "You are helpful." {
		t.Errorf("instructions = %q", instructions)
	}

	input, ok := respReq["input"].([]any)
	if !ok {
		t.Fatal("input not []any")
	}
	if len(input) != 2 {
		t.Fatalf("input = %d, want 2", len(input))
	}
}

func TestTranslateAnthropicToResponses_ToolCalls(t *testing.T) {
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "user", "content": "Read /tmp/foo"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{
					"type": "tool_use", "id": "call_1",
					"name": "read", "input": map[string]any{"path": "/tmp/foo"},
				},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{
					"type": "tool_result", "tool_use_id": "call_1",
					"content": "file contents",
				},
			}},
		},
	}

	respReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	input, _ := respReq["input"].([]any)
	// user, function_call, function_call_output
	if len(input) != 3 {
		t.Fatalf("input = %d, want 3", len(input))
	}

	fc, _ := input[1].(map[string]any)
	if fc["type"] != "function_call" {
		t.Errorf("input[1].type = %v, want function_call", fc["type"])
	}
	if fc["call_id"] != "call_1" {
		t.Errorf("call_id = %v", fc["call_id"])
	}

	fco, _ := input[2].(map[string]any)
	if fco["type"] != "function_call_output" {
		t.Errorf("input[2].type = %v, want function_call_output", fco["type"])
	}
	if fco["output"] != "file contents" {
		t.Errorf("output = %v", fco["output"])
	}
}

func TestTranslateResponses_RoundTrip(t *testing.T) {
	original := `{
		"model": "gpt-5.4",
		"instructions": "Be concise",
		"input": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"},
			{"role": "user", "content": "Bye"}
		],
		"max_output_tokens": 1024,
		"stream": true
	}`
	var req map[string]any
	json.Unmarshal([]byte(original), &req)

	anthReq, err := translateResponsesToAnthropic(req)
	if err != nil {
		t.Fatalf("to anthropic: %v", err)
	}

	respReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		t.Fatalf("to responses: %v", err)
	}

	if respReq["model"] != "gpt-5.4" {
		t.Errorf("model = %v", respReq["model"])
	}
	instructions, _ := respReq["instructions"].(string)
	if instructions != "Be concise" {
		t.Errorf("instructions = %q", instructions)
	}

	input, _ := respReq["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("round-trip input = %d, want 3", len(input))
	}
}
