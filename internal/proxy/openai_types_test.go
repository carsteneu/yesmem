package proxy

import (
	"encoding/json"
	"testing"
)

func TestOpenAIRequestUnmarshal(t *testing.T) {
	raw := `{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"/tmp\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "file contents here"}
		],
		"tools": [{"type": "function", "function": {"name": "read", "description": "Read a file", "parameters": {"type": "object"}}}],
		"stream": true
	}`

	var req OpenAIChatRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Model != "gpt-5.4" {
		t.Errorf("model = %q, want gpt-5.4", req.Model)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(req.Messages))
	}
	if !req.Stream {
		t.Error("stream should be true")
	}
	if len(req.Messages[2].ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(req.Messages[2].ToolCalls))
	}
	if req.Messages[2].ToolCalls[0].Function.Name != "read" {
		t.Error("tool_call function name mismatch")
	}
	if req.Messages[3].Role != "tool" {
		t.Errorf("role = %q, want tool", req.Messages[3].Role)
	}
	if req.Messages[3].ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q, want call_1", req.Messages[3].ToolCallID)
	}
}

func TestOpenAIStreamChunkMarshal(t *testing.T) {
	chunk := OpenAIStreamChunk{
		ID:     "chatcmpl-123",
		Object: "chat.completion.chunk",
		Model:  "gpt-5.4",
		Choices: []OpenAIStreamChoice{{
			Index: 0,
			Delta: OpenAIDelta{Content: "Hello"},
		}},
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON")
	}
	var decoded OpenAIStreamChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if decoded.Choices[0].Delta.Content != "Hello" {
		t.Error("content mismatch after round-trip")
	}
}
