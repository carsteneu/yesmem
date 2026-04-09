package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateAnthropicSSE_TextDelta(t *testing.T) {
	event := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)

	translator := newSSETranslator("gpt-5.4", "chatcmpl-123")
	chunks := translator.TranslateEvent(event)

	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}

	var chunk OpenAIStreamChunk
	if err := json.Unmarshal(chunks[0], &chunk); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	if chunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("content = %q, want Hello", chunk.Choices[0].Delta.Content)
	}
	if chunk.Model != "gpt-5.4" {
		t.Errorf("model = %q", chunk.Model)
	}
}

func TestTranslateAnthropicSSE_ToolUse(t *testing.T) {
	translator := newSSETranslator("gpt-5.4", "chatcmpl-123")

	startEvent := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}`)
	chunks := translator.TranslateEvent(startEvent)
	if len(chunks) != 1 {
		t.Fatalf("start chunks = %d, want 1", len(chunks))
	}
	var startChunk OpenAIStreamChunk
	json.Unmarshal(chunks[0], &startChunk)
	if len(startChunk.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatal("expected tool_call in delta")
	}
	if startChunk.Choices[0].Delta.ToolCalls[0].Function.Name != "read_file" {
		t.Error("tool name mismatch")
	}

	inputEvent := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp\"}"}}`)
	chunks = translator.TranslateEvent(inputEvent)
	if len(chunks) != 1 {
		t.Fatalf("input chunks = %d, want 1", len(chunks))
	}
	var inputChunk OpenAIStreamChunk
	json.Unmarshal(chunks[0], &inputChunk)
	if len(inputChunk.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatal("expected tool_call in input delta")
	}
	if inputChunk.Choices[0].Delta.ToolCalls[0].Function.Arguments != `{"path":"/tmp"}` {
		t.Errorf("arguments = %q", inputChunk.Choices[0].Delta.ToolCalls[0].Function.Arguments)
	}
}

func TestTranslateAnthropicSSE_MessageStop(t *testing.T) {
	translator := newSSETranslator("gpt-5.4", "chatcmpl-123")

	stopEvent := []byte(`{"type":"message_stop"}`)
	chunks := translator.TranslateEvent(stopEvent)

	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	if string(chunks[0]) != "[DONE]" {
		t.Errorf("expected [DONE], got %q", string(chunks[0]))
	}
}

func TestTranslateAnthropicSSE_Usage(t *testing.T) {
	translator := newSSETranslator("gpt-5.4", "chatcmpl-123")

	event := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":150,"output_tokens":0}}}`)
	chunks := translator.TranslateEvent(event)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	var chunk OpenAIStreamChunk
	json.Unmarshal(chunks[0], &chunk)
	if chunk.Usage == nil {
		t.Fatal("usage missing")
	}
	if chunk.Usage.PromptTokens != 150 {
		t.Errorf("prompt_tokens = %d, want 150", chunk.Usage.PromptTokens)
	}
}
