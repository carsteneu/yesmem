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

func TestValidateToolPairs_SynthesizesNakedToolUse_LastMessage(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "run the tool"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "calling tool"},
			map[string]any{"type": "tool_use", "id": "tu_naked", "name": "bash"},
		}},
	}

	result, repairs := validateToolPairs(messages, nil)
	if repairs != 1 {
		t.Fatalf("expected 1 repair, got %d", repairs)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (synthetic user appended), got %d", len(result))
	}
	synthMsg, ok := result[2].(map[string]any)
	if !ok || synthMsg["role"] != "user" {
		t.Fatalf("expected user message at index 2, got %v", result[2])
	}
	content := synthMsg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "tool_result" {
		t.Errorf("expected tool_result, got %v", block["type"])
	}
	if block["tool_use_id"] != "tu_naked" {
		t.Errorf("expected tu_naked, got %v", block["tool_use_id"])
	}
}

func TestValidateToolPairs_SynthesizesNakedToolUse_UserWithoutResult(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_a", "name": "read"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "user reply without tool_result"},
		}},
	}

	result, repairs := validateToolPairs(messages, nil)
	if repairs != 1 {
		t.Fatalf("expected 1 repair, got %d", repairs)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	userMsg := result[2].(map[string]any)
	content := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks (text + synthetic), got %d", len(content))
	}
	synthBlock := content[1].(map[string]any)
	if synthBlock["type"] != "tool_result" {
		t.Errorf("expected tool_result, got %v", synthBlock["type"])
	}
	if synthBlock["tool_use_id"] != "tu_a" {
		t.Errorf("expected tu_a, got %v", synthBlock["tool_use_id"])
	}
}

func TestValidateToolPairs_SynthesizesOnlyMissingResults(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_has_result", "name": "read"},
			map[string]any{"type": "tool_use", "id": "tu_naked", "name": "bash"},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_has_result", "content": "ok"},
		}},
	}

	result, repairs := validateToolPairs(messages, nil)
	if repairs != 1 {
		t.Fatalf("expected 1 repair, got %d", repairs)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	userMsg := result[2].(map[string]any)
	content := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks (original + synthetic), got %d", len(content))
	}
	synthBlock := content[1].(map[string]any)
	if synthBlock["tool_use_id"] != "tu_naked" {
		t.Errorf("expected tu_naked synthesized, got %v", synthBlock["tool_use_id"])
	}
}
