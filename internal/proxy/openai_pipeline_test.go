package proxy

import (
	"encoding/json"
	"testing"
)

func TestOpenAIPipeline_TranslatedMessagesAreValidJSON(t *testing.T) {
	// Full round-trip: OpenAI request → Anthropic format → JSON → back to map
	oaiReq := OpenAIChatRequest{
		Model:     "gpt-5.4",
		MaxTokens: 4096,
		Messages: []OpenAIMessage{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
			{Role: "user", Content: "Thanks"},
		},
		Stream: true,
	}

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	// Serialize and deserialize (simulates what proxy does)
	data, err := json.Marshal(anthReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify structure matches what handleMessages expects
	msgs, ok := req["messages"].([]any)
	if !ok {
		t.Fatal("messages not []any after round-trip")
	}
	if len(msgs) != 3 { // system extracted, 3 remaining
		t.Errorf("messages = %d, want 3", len(msgs))
	}

	sys, ok := req["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatal("system blocks missing after round-trip")
	}

	if req["stream"] != true {
		t.Error("stream flag lost")
	}
}

func TestOpenAIPipeline_ToolResultStructure(t *testing.T) {
	// Verify translated tool_result messages have correct Anthropic structure
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Read file"},
			{Role: "assistant", Content: "", ToolCalls: []OpenAIToolCall{
				{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{
					Name: "read", Arguments: `{"path":"/tmp"}`,
				}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents here"},
		},
	}

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	// Serialize/deserialize to simulate real proxy flow
	data, _ := json.Marshal(anthReq)
	var req map[string]any
	json.Unmarshal(data, &req)

	messages, _ := req["messages"].([]any)

	// Verify tool_result is inside a user message with proper structure
	lastMsg, _ := messages[len(messages)-1].(map[string]any)
	if lastMsg["role"] != "user" {
		t.Fatalf("last msg role = %v, want user", lastMsg["role"])
	}

	content, _ := lastMsg["content"].([]any)
	if len(content) == 0 {
		t.Fatal("user message content blocks empty")
	}

	block, _ := content[0].(map[string]any)
	if block["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", block["type"])
	}
	if block["tool_use_id"] != "call_1" {
		t.Errorf("tool_use_id = %v, want call_1", block["tool_use_id"])
	}
}

func TestOpenAIPipeline_MessageAlternation(t *testing.T) {
	// Verify user/assistant messages alternate correctly after translation
	// (Anthropic API requires strict alternation)
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
			{Role: "user", Content: "Read /tmp"},
			{Role: "assistant", Content: "", ToolCalls: []OpenAIToolCall{
				{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{
					Name: "read", Arguments: `{"path":"/tmp"}`,
				}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "contents"},
			{Role: "assistant", Content: "Here are the contents"},
		},
	}

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	data, _ := json.Marshal(anthReq)
	var req map[string]any
	json.Unmarshal(data, &req)

	messages, _ := req["messages"].([]any)

	// Check alternation: user, assistant, user, assistant, user(tool_result), assistant
	expectedRoles := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	if len(messages) != len(expectedRoles) {
		t.Fatalf("messages = %d, want %d", len(messages), len(expectedRoles))
	}

	for i, expected := range expectedRoles {
		m, _ := messages[i].(map[string]any)
		if m["role"] != expected {
			t.Errorf("msg[%d].role = %v, want %v", i, m["role"], expected)
		}
	}
}

func TestOpenAIPipeline_MultipleConsecutiveTools(t *testing.T) {
	// Multiple tool responses should be grouped into one user message
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Read two files"},
			{Role: "assistant", Content: "", ToolCalls: []OpenAIToolCall{
				{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{Name: "read", Arguments: `{"path":"/a"}`}},
				{ID: "call_2", Type: "function", Function: OpenAIFunctionCall{Name: "read", Arguments: `{"path":"/b"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "file a"},
			{Role: "tool", ToolCallID: "call_2", Content: "file b"},
			{Role: "assistant", Content: "Here are both files"},
		},
	}

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	data, _ := json.Marshal(anthReq)
	var req map[string]any
	json.Unmarshal(data, &req)

	messages, _ := req["messages"].([]any)

	// Expected: user, assistant(2 tool_use), user(2 tool_result), assistant
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(messages))
	}

	// Check grouped tool_results
	toolMsg, _ := messages[2].(map[string]any)
	if toolMsg["role"] != "user" {
		t.Fatalf("tool msg role = %v, want user", toolMsg["role"])
	}
	content, _ := toolMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("tool_result blocks = %d, want 2", len(content))
	}

	for i, block := range content {
		b, _ := block.(map[string]any)
		if b["type"] != "tool_result" {
			t.Errorf("block[%d] type = %v, want tool_result", i, b["type"])
		}
	}
}
