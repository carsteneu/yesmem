package proxy

import "testing"

func TestPrependMeta_StringContent(t *testing.T) {
	msg := map[string]any{"role": "user", "content": "hello"}
	prependMeta(msg, "[ts] [msg:1]")
	c := msg["content"].(string)
	if c != "[ts] [msg:1]\nhello" {
		t.Errorf("got %q", c)
	}
}

func TestPrependMeta_TextBlock(t *testing.T) {
	msg := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "hello"},
	}}
	prependMeta(msg, "[ts] [msg:1]")
	blocks := msg["content"].([]any)
	b0 := blocks[0].(map[string]any)
	if b0["text"] != "[ts] [msg:1]\nhello" {
		t.Errorf("got %q", b0["text"])
	}
}

func TestPrependMeta_ToolResultOnly_InsertsAfter(t *testing.T) {
	msg := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "result"},
	}}
	prependMeta(msg, "[ts] [msg:1]")
	blocks := msg["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	b0 := blocks[0].(map[string]any)
	if b0["type"] != "tool_result" {
		t.Errorf("expected tool_result at position 0, got %v", b0["type"])
	}
	b1 := blocks[1].(map[string]any)
	if b1["type"] != "text" {
		t.Errorf("expected text at position 1, got %v", b1["type"])
	}
}

func TestPrependMeta_MultipleToolResults_InsertsAfterAll(t *testing.T) {
	msg := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "r1"},
		map[string]any{"type": "tool_result", "tool_use_id": "tu_2", "content": "r2"},
	}}
	prependMeta(msg, "[ts] [msg:1]")
	blocks := msg["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	if blocks[0].(map[string]any)["type"] != "tool_result" {
		t.Errorf("expected tool_result at 0")
	}
	if blocks[1].(map[string]any)["type"] != "tool_result" {
		t.Errorf("expected tool_result at 1")
	}
	if blocks[2].(map[string]any)["type"] != "text" {
		t.Errorf("expected text at 2")
	}
}

func TestPrependMeta_AlreadyHasMeta_Skips(t *testing.T) {
	msg := map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "[Fr 2026-07-10 12:00:00] [msg:1] [+1s]\nexisting"},
	}}
	prependMeta(msg, "[ts] [msg:2]")
	blocks := msg["content"].([]any)
	b0 := blocks[0].(map[string]any)
	if b0["text"] != "[Fr 2026-07-10 12:00:00] [msg:1] [+1s]\nexisting" {
		t.Errorf("should not modify, got %q", b0["text"])
	}
}
