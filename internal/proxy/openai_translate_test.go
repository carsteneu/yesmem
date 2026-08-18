package proxy

import (
	"encoding/json"
	"testing"
)

func TestTranslateOpenAIToAnthropic_BasicMessages(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
		MaxTokens: 1024,
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	system, _ := req["system"].([]any)
	if len(system) == 0 {
		t.Fatal("system blocks missing")
	}
	sysBlock, _ := system[0].(map[string]any)
	if sysBlock["text"] != "You are helpful." {
		t.Errorf("system text = %v", sysBlock["text"])
	}

	messages, _ := req["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3 (no system)", len(messages))
	}

	m0, _ := messages[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("msg[0].role = %v, want user", m0["role"])
	}

	if req["model"] != "gpt-5.4" {
		t.Errorf("model = %v", req["model"])
	}
	if req["max_tokens"] != 1024 {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
}

func TestTranslateOpenAIToAnthropic_ToolCalls(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Read /tmp/foo"},
			{Role: "assistant", Content: "", ToolCalls: []OpenAIToolCall{
				{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{Name: "read", Arguments: `{"path":"/tmp/foo"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
			{Role: "user", Content: "Thanks"},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	messages, _ := req["messages"].([]any)
	if len(messages) < 3 {
		t.Fatalf("messages = %d, want >= 3", len(messages))
	}

	// Assistant should have tool_use block
	m1, _ := messages[1].(map[string]any)
	content1, _ := m1["content"].([]any)
	if len(content1) == 0 {
		t.Fatal("assistant content blocks missing")
	}
	block, _ := content1[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v, want tool_use", block["type"])
	}
	if block["name"] != "read" {
		t.Errorf("tool name = %v, want read", block["name"])
	}

	// Next user message should have tool_result
	m2, _ := messages[2].(map[string]any)
	if m2["role"] != "user" {
		t.Errorf("msg[2].role = %v, want user", m2["role"])
	}
	content2, _ := m2["content"].([]any)
	found := false
	for _, b := range content2 {
		bm, _ := b.(map[string]any)
		if bm["type"] == "tool_result" {
			found = true
			if bm["tool_use_id"] != "call_1" {
				t.Errorf("tool_use_id = %v", bm["tool_use_id"])
			}
		}
	}
	if !found {
		t.Error("tool_result block not found in user message")
	}
}

func TestTranslateOpenAIToAnthropic_Tools(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
		},
		Tools: []OpenAITool{
			{Type: "function", Function: OpenAIToolFunction{
				Name:        "read_file",
				Description: "Read a file from disk",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			}},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "read_file" {
		t.Errorf("tool name = %v", tool["name"])
	}
	if tool["description"] != "Read a file from disk" {
		t.Errorf("tool description = %v", tool["description"])
	}
	if tool["input_schema"] == nil {
		t.Error("input_schema missing (should be mapped from parameters)")
	}
}

func TestTranslateOpenAIToAnthropic_MultipleToolResults(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Read two files"},
			{Role: "assistant", Content: "", ToolCalls: []OpenAIToolCall{
				{ID: "call_1", Type: "function", Function: OpenAIFunctionCall{Name: "read", Arguments: `{"path":"/tmp/a"}`}},
				{ID: "call_2", Type: "function", Function: OpenAIFunctionCall{Name: "read", Arguments: `{"path":"/tmp/b"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "contents of a"},
			{Role: "tool", ToolCallID: "call_2", Content: "contents of b"},
			{Role: "user", Content: "Done"},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	messages, _ := req["messages"].([]any)
	// user, assistant, user(2 tool_results), user(Done) = 4
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(messages))
	}

	// m2 should be a user message with 2 tool_result blocks
	m2, _ := messages[2].(map[string]any)
	if m2["role"] != "user" {
		t.Errorf("msg[2].role = %v, want user", m2["role"])
	}
	content2, _ := m2["content"].([]any)
	if len(content2) != 2 {
		t.Errorf("tool_result blocks = %d, want 2", len(content2))
	}

	ids := map[string]bool{}
	for _, b := range content2 {
		bm, _ := b.(map[string]any)
		if bm["type"] != "tool_result" {
			t.Errorf("block type = %v, want tool_result", bm["type"])
		}
		ids[bm["tool_use_id"].(string)] = true
	}
	if !ids["call_1"] || !ids["call_2"] {
		t.Errorf("missing tool_use_ids, got: %v", ids)
	}
}

// Ensure JSON round-trip works (used by the proxy pipeline)
func TestTranslateOpenAIToAnthropic_JSONRoundTrip(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	_, err = json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
}

// Vision: OpenAI image_url content parts must survive translation to Anthropic
// format. opencode (@ai-sdk/openai-compatible) sends images as
// {type:"image_url", image_url:{url:"data:..."}} — the proxy must translate
// these to Anthropic {type:"image", source:{type:"base64",...}} blocks.
// Dokument-Attachments (OpenAI file-Part): muss den OpenAI→Anthropic→OpenAI
// Round-Trip überleben — der Gateway docling_hook konvertiert sie zu Markdown.
func TestTranslateFilePartRoundTrip(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "privateBob",
		Messages: []OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "was steht drin?"},
					map[string]any{
						"type": "file",
						"file": map[string]any{
							"filename":  "doc.pdf",
							"file_data": "data:application/pdf;base64,JVBERi0=",
						},
					},
				},
			},
		},
	}

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	msgs, _ := anthReq["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	m, _ := msgs[0].(map[string]any)
	blocks, ok := m["content"].([]any)
	if !ok {
		t.Fatalf("content = %T, want []any (file part was flattened)", m["content"])
	}
	var sawFile bool
	for _, b := range blocks {
		bm, _ := b.(map[string]any)
		if bm["type"] == "file" {
			sawFile = true
			f, _ := bm["file"].(map[string]any)
			if f["filename"] != "doc.pdf" {
				t.Errorf("file.filename = %v, want doc.pdf", f["filename"])
			}
		}
	}
	if !sawFile {
		t.Fatalf("no file block survived forward translation: %+v", blocks)
	}

	outReq, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	outMsgs, _ := outReq["messages"].([]any)
	if len(outMsgs) != 1 {
		t.Fatalf("out messages = %d, want 1", len(outMsgs))
	}
	om, _ := outMsgs[0].(map[string]any)
	arr, ok := om["content"].([]any)
	if !ok {
		t.Fatalf("out content = %T, want []any (file part was dropped in reverse)", om["content"])
	}
	sawFile = false
	for _, b := range arr {
		bm, _ := b.(map[string]any)
		if bm["type"] == "file" {
			sawFile = true
			f, _ := bm["file"].(map[string]any)
			if f["file_data"] != "data:application/pdf;base64,JVBERi0=" {
				t.Errorf("file_data mismatch: %v", f["file_data"])
			}
		}
	}
	if !sawFile {
		t.Fatalf("no file part in reverse output: %+v", arr)
	}
}

func TestTranslateOpenAIToAnthropic_ImageURLPreserved(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "was ist das?"},
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"},
					},
				},
			},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	messages, _ := req["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" {
		t.Fatalf("role = %v, want user", msg["role"])
	}
	// Content must be an array with text + image blocks, not a flattened string.
	blocks, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content type = %T, want []any (image was stripped)", msg["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + image)", len(blocks))
	}
	// Block 0: text
	b0, _ := blocks[0].(map[string]any)
	if b0["type"] != "text" || b0["text"] != "was ist das?" {
		t.Errorf("block[0] = %+v, want type:text text:%q", b0, "was ist das?")
	}
	// Block 1: image with base64 source
	b1, _ := blocks[1].(map[string]any)
	if b1["type"] != "image" {
		t.Fatalf("block[1].type = %v, want image", b1["type"])
	}
	src, _ := b1["source"].(map[string]any)
	if src["type"] != "base64" {
		t.Errorf("source.type = %v, want base64", src["type"])
	}
	if src["media_type"] != "image/png" {
		t.Errorf("source.media_type = %v, want image/png", src["media_type"])
	}
	if src["data"] != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB" {
		t.Errorf("source.data mismatch")
	}
}

// Vision: image-only message (no text block) — must still produce an image block.
func TestTranslateOpenAIToAnthropic_ImageOnly(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/jpeg;base64,/9j/4AAQ"},
					},
				},
			},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	messages, _ := req["messages"].([]any)
	msg := messages[0].(map[string]any)
	blocks, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content type = %T, want []any", msg["content"])
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	b0 := blocks[0].(map[string]any)
	if b0["type"] != "image" {
		t.Errorf("type = %v, want image", b0["type"])
	}
}

// Vision: image_url with a real URL (not base64 data URI) — pass through as-is.
func TestTranslateOpenAIToAnthropic_ImageURLExternal(t *testing.T) {
	oaiReq := OpenAIChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []OpenAIMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "https://example.com/logo.png"},
					},
				},
			},
		},
	}

	req, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	messages, _ := req["messages"].([]any)
	msg := messages[0].(map[string]any)
	blocks := msg["content"].([]any)
	b0 := blocks[0].(map[string]any)
	src := b0["source"].(map[string]any)
	if src["type"] != "url" {
		t.Errorf("source.type = %v, want url", src["type"])
	}
	if src["url"] != "https://example.com/logo.png" {
		t.Errorf("source.url = %v, want https://example.com/logo.png", src["url"])
	}
}
