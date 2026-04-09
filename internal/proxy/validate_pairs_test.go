package proxy

import (
	"testing"
)

func TestValidateToolPairs_NoOrphans(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "read"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "file contents"},
		}},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 0 {
		t.Errorf("expected 0 orphans, got %d", orphans)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}
}

func TestValidateToolPairs_SingleOrphan(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_missing", "content": "orphan"},
			map[string]any{"type": "text", "text": "keep this"},
		}},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 1 {
		t.Errorf("expected 1 orphan, got %d", orphans)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}
	// The remaining message should have only the text block
	msg := result[2].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Errorf("expected 1 block after orphan removal, got %d", len(content))
	}
}

func TestValidateToolPairs_OrphanOnlyMessage(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "thinking..."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_gone", "content": "orphan"},
		}},
		map[string]any{"role": "assistant", "content": "response"},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 1 {
		t.Errorf("expected 1 orphan, got %d", orphans)
	}
	// Message 2 (orphan-only) removed, messages 1+3 both assistant → merged
	// Result: user, assistant (merged)
	if len(result) != 2 {
		t.Errorf("expected 2 messages after removal+merge, got %d", len(result))
	}
}

func TestValidateToolPairs_MultipleOrphans(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "read"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "valid"},
			map[string]any{"type": "tool_result", "tool_use_id": "tu_dead1", "content": "orphan1"},
			map[string]any{"type": "tool_result", "tool_use_id": "tu_dead2", "content": "orphan2"},
		}},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 2 {
		t.Errorf("expected 2 orphans, got %d", orphans)
	}
	msg := result[2].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Errorf("expected 1 block remaining, got %d", len(content))
	}
}

func TestValidateToolPairs_StringContent(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "just text"},
		map[string]any{"role": "assistant", "content": "also text"},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 0 {
		t.Errorf("expected 0 orphans, got %d", orphans)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result))
	}
}

func TestValidateToolPairs_EmptySlice(t *testing.T) {
	result, orphans := validateToolPairs([]any{}, nil)
	if orphans != 0 || len(result) != 0 {
		t.Errorf("expected empty result, got %d messages %d orphans", len(result), orphans)
	}
}

func TestValidateToolPairs_MixedValidAndOrphan(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_valid", "name": "bash"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_valid", "content": "ok"},
			map[string]any{"type": "tool_result", "tool_use_id": "tu_orphan", "content": "nope"},
		}},
	}

	result, orphans := validateToolPairs(messages, nil)
	if orphans != 1 {
		t.Errorf("expected 1 orphan, got %d", orphans)
	}
	msg := result[2].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Errorf("expected 1 valid block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["tool_use_id"] != "tu_valid" {
		t.Errorf("expected tu_valid to survive, got %v", block["tool_use_id"])
	}
}
