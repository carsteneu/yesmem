package httpapi

import (
	"encoding/json"
	"testing"
)

func TestAssembleRequestParsing(t *testing.T) {
	var req AssembleRequest
	input := `{"session_id":"s1","project":"/tmp/proj","messages":[{"role":"user","content":"hi"}]}`
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.SessionID != "s1" {
		t.Errorf("session_id = %q", req.SessionID)
	}
	if req.Project != "/tmp/proj" {
		t.Errorf("project = %q", req.Project)
	}
	if len(req.Messages) != 1 {
		t.Errorf("messages count = %d", len(req.Messages))
	}
}

func TestAssembleRequestDefaults(t *testing.T) {
	var req AssembleRequest
	input := `{"session_id":"s2","messages":[{"role":"user","content":"hello"}]}`
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.TokenBudget != 0 {
		t.Errorf("token_budget default = %d, want 0", req.TokenBudget)
	}
	if req.KeepRecent != 0 {
		t.Errorf("keep_recent default = %d, want 0", req.KeepRecent)
	}
}

func TestLastUserText(t *testing.T) {
	tests := []struct {
		name     string
		messages []any
		want     string
	}{
		{
			name: "string content",
			messages: []any{
				map[string]any{"role": "user", "content": "hello world"},
			},
			want: "hello world",
		},
		{
			name: "block content",
			messages: []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "text", "text": "block message"},
					},
				},
			},
			want: "block message",
		},
		{
			name: "picks last user message",
			messages: []any{
				map[string]any{"role": "user", "content": "first"},
				map[string]any{"role": "assistant", "content": "response"},
				map[string]any{"role": "user", "content": "second"},
			},
			want: "second",
		},
		{
			name:     "empty messages",
			messages: []any{},
			want:     "",
		},
		{
			name: "no user message",
			messages: []any{
				map[string]any{"role": "assistant", "content": "only assistant"},
			},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lastUserText(tc.messages)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("short string: got %q", got)
	}
	if got := truncateStr("hello world", 5); got != "hello" {
		t.Errorf("truncated: got %q", got)
	}
	if got := truncateStr("", 10); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

func TestInjectReflectionHint(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "my question"},
	}
	injectReflectionHint(&messages)
	// Last user message should now have block content with the hint appended
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		t.Fatal("last message not a map")
	}
	blocks, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content not []any after inject, got %T", last["content"])
	}
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}
	lastBlock, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		t.Fatal("last block not a map")
	}
	text, _ := lastBlock["text"].(string)
	if text == "" {
		t.Error("reflection hint text is empty")
	}
	// Must contain the Bohrhammer marker
	if len(text) < 10 {
		t.Errorf("reflection hint too short: %q", text)
	}
}

func TestInjectReflectionHint_AlreadyBlocks(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "existing block"},
			},
		},
	}
	injectReflectionHint(&messages)
	last := messages[len(messages)-1].(map[string]any)
	blocks := last["content"].([]any)
	if len(blocks) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestInjectAssociativeContext(t *testing.T) {
	searchResult := json.RawMessage(`{
		"results": [
			{"id": "42", "content": "some learning content", "score": 0.5},
			{"id": "99", "content": "another learning", "score": 0.03}
		]
	}`)
	messages := []any{
		map[string]any{"role": "user", "content": "test question"},
	}
	ids := injectAssociativeContext(&messages, searchResult, "testproject")
	// Should have injected the above-threshold results
	if len(ids) == 0 {
		t.Error("expected injected IDs, got none")
	}
	// Check that the last message was modified
	last := messages[len(messages)-1].(map[string]any)
	blocks, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content not blocks after inject, got %T", last["content"])
	}
	if len(blocks) < 2 {
		t.Errorf("expected at least 2 content blocks (original + context), got %d", len(blocks))
	}
}

func TestInjectAssociativeContext_BelowThreshold(t *testing.T) {
	// Score 0.01 is below threshold 0.020
	searchResult := json.RawMessage(`{
		"results": [
			{"id": "1", "content": "low score content", "score": 0.01}
		]
	}`)
	messages := []any{
		map[string]any{"role": "user", "content": "query"},
	}
	ids := injectAssociativeContext(&messages, searchResult, "")
	if len(ids) != 0 {
		t.Errorf("expected no IDs below threshold, got %v", ids)
	}
}

func TestInjectAssociativeContext_EmptyResult(t *testing.T) {
	searchResult := json.RawMessage(`{"results": []}`)
	messages := []any{
		map[string]any{"role": "user", "content": "query"},
	}
	ids := injectAssociativeContext(&messages, searchResult, "")
	if len(ids) != 0 {
		t.Errorf("expected no IDs for empty results, got %v", ids)
	}
}

func TestInjectFreshRemember(t *testing.T) {
	result := json.RawMessage(`{"items": [{"id": 7, "text": "fresh learning text"}]}`)
	messages := []any{
		map[string]any{"role": "user", "content": "current question"},
	}
	ids := injectFreshRemember(&messages, result)
	if len(ids) != 1 || ids[0] != 7 {
		t.Errorf("expected [7], got %v", ids)
	}
	// Message should have been modified
	last := messages[len(messages)-1].(map[string]any)
	blocks, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("content not blocks, got %T", last["content"])
	}
	if len(blocks) < 2 {
		t.Errorf("expected >= 2 blocks, got %d", len(blocks))
	}
}

func TestInjectFreshRemember_Empty(t *testing.T) {
	result := json.RawMessage(`{"items": []}`)
	messages := []any{
		map[string]any{"role": "user", "content": "question"},
	}
	ids := injectFreshRemember(&messages, result)
	if len(ids) != 0 {
		t.Errorf("expected no IDs for empty items, got %v", ids)
	}
	// Messages should be unchanged
	last := messages[len(messages)-1].(map[string]any)
	if _, ok := last["content"].([]any); ok {
		t.Error("content should remain string when nothing injected")
	}
}
