package proxy

import (
	"testing"
)

func TestExtractSearchHint(t *testing.T) {
	tests := []struct {
		stub string
		want string
	}{
		{"[→] Read main.go — found stuff → deep_search('Read main.go')", "Read main.go"},
		{"[tool result archived → deep_search('Bash go test')]", "Bash go test"},
		{"[→] Edit proxy.go — file on disk", ""},
		{"no hint here", ""},
		{"deep_search('unclosed", ""},
	}

	for _, tt := range tests {
		got := extractSearchHint(tt.stub)
		if got != tt.want {
			t.Errorf("extractSearchHint(%q) = %q, want %q", tt.stub, got, tt.want)
		}
	}
}

func TestReplaceStubInMessage_Blocks(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "[→] Read main.go → deep_search('Read main.go')"},
			},
		},
	}

	result := replaceStubInMessage(msgs, 0,
		"[→] Read main.go → deep_search('Read main.go')",
		"[re-expanded] full content here")

	msg := result[0].(map[string]any)
	blocks := msg["content"].([]any)
	block := blocks[0].(map[string]any)
	text := block["text"].(string)
	if text != "[re-expanded] full content here" {
		t.Errorf("replacement failed: %q", text)
	}
}

func TestReplaceStubInMessage_StringContent(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role":    "assistant",
			"content": "[→] Read file.go → deep_search('Read file.go')",
		},
	}

	result := replaceStubInMessage(msgs, 0,
		"[→] Read file.go → deep_search('Read file.go')",
		"original content")

	msg := result[0].(map[string]any)
	if msg["content"] != "original content" {
		t.Errorf("string replacement failed: %v", msg["content"])
	}
}

func TestReplaceStubInMessage_NoMatch(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "some other text"},
			},
		},
	}

	result := replaceStubInMessage(msgs, 0, "nonexistent stub", "replacement")
	msg := result[0].(map[string]any)
	blocks := msg["content"].([]any)
	block := blocks[0].(map[string]any)
	if block["text"] != "some other text" {
		t.Error("should not have replaced anything")
	}
}
