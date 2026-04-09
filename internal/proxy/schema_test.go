package proxy

import (
	"testing"
)

// === Task #11: Schema-Drift Resilience ===

func TestStubBlocks_UnknownBlockTypePreserved(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "code_execution", "code": "print('hello')", "result": "hello"},
		map[string]any{"type": "server_tool_use", "id": "st_123", "name": "internal"},
		map[string]any{"type": "text", "text": longString(5000)},
	}

	result, anyStubbed := stubBlocks(blocks, "assistant", 1, nil, nil)

	if !anyStubbed {
		t.Error("text block should have been stubbed")
	}

	// Unknown block types must be preserved as-is
	unknownCount := 0
	for _, block := range result {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := b["type"].(string)
		if blockType == "code_execution" || blockType == "server_tool_use" {
			unknownCount++
		}
	}
	if unknownCount != 2 {
		t.Errorf("expected 2 unknown blocks preserved, got %d", unknownCount)
	}
}

func TestStubBlocks_UnknownBlockContentUntouched(t *testing.T) {
	original := map[string]any{
		"type":   "future_block",
		"data":   "important",
		"nested": map[string]any{"key": "value"},
	}
	blocks := []any{original}

	result, _ := stubBlocks(blocks, "assistant", 1, nil, nil)

	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	b := result[0].(map[string]any)
	if b["type"] != "future_block" {
		t.Errorf("type changed: %v", b["type"])
	}
	if b["data"] != "important" {
		t.Errorf("data changed: %v", b["data"])
	}
}
