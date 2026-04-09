package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateAnthropicToOpenAI_BasicMessages(t *testing.T) {
	anthReq := map[string]any{
		"model":      "gpt-5.4",
		"max_tokens": float64(1024),
		"system": []any{
			map[string]any{"type": "text", "text": "You are helpful."},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Hi there!"},
			}},
			map[string]any{"role": "user", "content": "How are you?"},
		},
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if oaiBody["model"] != "gpt-5.4" {
		t.Errorf("model = %v", oaiBody["model"])
	}

	msgs, ok := oaiBody["messages"].([]any)
	if !ok {
		t.Fatal("messages not []any")
	}

	// system block becomes role:system message at front
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "system" {
		t.Errorf("msg[0].role = %v, want system", m0["role"])
	}
	if m0["content"] != "You are helpful." {
		t.Errorf("msg[0].content = %v", m0["content"])
	}

	// user message
	m1, _ := msgs[1].(map[string]any)
	if m1["role"] != "user" {
		t.Errorf("msg[1].role = %v, want user", m1["role"])
	}

	// assistant message — content blocks flattened to string
	m2, _ := msgs[2].(map[string]any)
	if m2["role"] != "assistant" {
		t.Errorf("msg[2].role = %v, want assistant", m2["role"])
	}
	content, _ := m2["content"].(string)
	if content != "Hi there!" {
		t.Errorf("msg[2].content = %q, want 'Hi there!'", content)
	}

	if len(msgs) != 4 { // system + user + assistant + user
		t.Errorf("messages = %d, want 4", len(msgs))
	}
}

func TestTranslateAnthropicToOpenAI_ToolCalls(t *testing.T) {
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "user", "content": "Read /tmp/foo"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_1",
					"name":  "read",
					"input": map[string]any{"path": "/tmp/foo"},
				},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_1",
					"content":     "file contents",
				},
			}},
			map[string]any{"role": "user", "content": "Thanks"},
		},
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	msgs, _ := oaiBody["messages"].([]any)

	// assistant with tool_calls
	m1, _ := msgs[1].(map[string]any)
	toolCalls, ok := m1["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Fatal("assistant should have tool_calls")
	}
	tc, _ := toolCalls[0].(map[string]any)
	if tc["id"] != "toolu_1" {
		t.Errorf("tool_call id = %v", tc["id"])
	}
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "read" {
		t.Errorf("function name = %v", fn["name"])
	}
	// arguments should be JSON string
	args, _ := fn["arguments"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Errorf("arguments not valid JSON: %v", err)
	}

	// tool_result user message → role:tool message
	m2, _ := msgs[2].(map[string]any)
	if m2["role"] != "tool" {
		t.Errorf("msg[2].role = %v, want tool", m2["role"])
	}
	if m2["tool_call_id"] != "toolu_1" {
		t.Errorf("tool_call_id = %v", m2["tool_call_id"])
	}
	content, _ := m2["content"].(string)
	if content != "file contents" {
		t.Errorf("tool content = %q", content)
	}

	// next user message
	m3, _ := msgs[3].(map[string]any)
	if m3["role"] != "user" {
		t.Errorf("msg[3].role = %v, want user", m3["role"])
	}
}

func TestTranslateAnthropicToOpenAI_Tools(t *testing.T) {
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
		"tools": []any{
			map[string]any{
				"name":         "read_file",
				"description":  "Read a file",
				"input_schema": map[string]any{"type": "object"},
			},
		},
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	tools, ok := oaiBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", oaiBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool type = %v, want function", tool["type"])
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "read_file" {
		t.Errorf("function name = %v", fn["name"])
	}
	if fn["parameters"] == nil {
		t.Error("parameters missing (mapped from input_schema)")
	}
}

func TestTranslateAnthropicToOpenAI_MultipleToolResults(t *testing.T) {
	// Anthropic groups tool_results in one user message; OpenAI splits into separate tool messages
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "user", "content": "Read two files"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "read", "input": map[string]any{"path": "/a"}},
				map[string]any{"type": "tool_use", "id": "t2", "name": "read", "input": map[string]any{"path": "/b"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "file a"},
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": "file b"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Here are both"},
			}},
		},
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	msgs, _ := oaiBody["messages"].([]any)
	// user, assistant(2 tool_calls), tool(t1), tool(t2), assistant
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5", len(msgs))
	}

	m2, _ := msgs[2].(map[string]any)
	m3, _ := msgs[3].(map[string]any)
	if m2["role"] != "tool" || m3["role"] != "tool" {
		t.Errorf("expected two tool messages, got roles %v, %v", m2["role"], m3["role"])
	}
	if m2["tool_call_id"] != "t1" || m3["tool_call_id"] != "t2" {
		t.Errorf("tool_call_ids = %v, %v", m2["tool_call_id"], m3["tool_call_id"])
	}
}

func TestTranslateAnthropicToOpenAI_RoundTrip(t *testing.T) {
	// OpenAI → Anthropic → OpenAI should preserve essential structure
	original := OpenAIChatRequest{
		Model:     "gpt-5.4",
		MaxTokens: 2048,
		Messages: []OpenAIMessage{
			{Role: "system", Content: "Be helpful"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
	}

	anthReq, err := translateOpenAIToAnthropic(original)
	if err != nil {
		t.Fatalf("to anthropic: %v", err)
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("to openai: %v", err)
	}

	msgs, _ := oaiBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("round-trip messages = %d, want 3", len(msgs))
	}

	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "system" || m0["content"] != "Be helpful" {
		t.Errorf("system lost: %v", m0)
	}

	m2, _ := msgs[2].(map[string]any)
	if m2["role"] != "assistant" {
		t.Errorf("assistant lost: %v", m2)
	}
	content, _ := m2["content"].(string)
	if content != "Hi!" {
		t.Errorf("assistant content = %q, want 'Hi!'", content)
	}
}

func TestTranslateAnthropicToOpenAI_PassthroughParams(t *testing.T) {
	temp := 0.7
	anthReq := map[string]any{
		"model":       "gpt-5.4",
		"max_tokens":  float64(4096),
		"temperature": temp,
		"top_p":       0.9,
		"stream":      true,
		"messages":    []any{map[string]any{"role": "user", "content": "Hi"}},
	}

	oaiBody, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if oaiBody["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v", oaiBody["max_tokens"])
	}
	if oaiBody["temperature"] != temp {
		t.Errorf("temperature = %v", oaiBody["temperature"])
	}
	if oaiBody["stream"] != true {
		t.Errorf("stream = %v", oaiBody["stream"])
	}
}
