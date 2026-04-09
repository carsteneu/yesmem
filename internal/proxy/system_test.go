package proxy

import (
	"strings"
	"testing"
)

func TestEnsureSystemArray_FromString(t *testing.T) {
	req := map[string]any{"system": "You are Claude."}
	blocks := ensureSystemArray(req)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0].(map[string]any)
	if b["type"] != "text" || b["text"] != "You are Claude." {
		t.Errorf("unexpected block: %v", b)
	}
}

func TestEnsureSystemArray_AlreadyArray(t *testing.T) {
	arr := []any{map[string]any{"type": "text", "text": "existing"}}
	req := map[string]any{"system": arr}
	blocks := ensureSystemArray(req)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestEnsureSystemArray_Missing(t *testing.T) {
	req := map[string]any{}
	blocks := ensureSystemArray(req)
	if blocks == nil {
		t.Fatal("expected empty array for missing system")
	}
	if len(blocks) != 0 {
		t.Fatalf("expected empty array, got %d blocks", len(blocks))
	}
	if _, ok := req["system"].([]any); !ok {
		t.Errorf("system should be initialized as []any, got %T", req["system"])
	}
}

func TestAppendSystemBlock_StringToArray(t *testing.T) {
	req := map[string]any{"system": "You are Claude."}
	AppendSystemBlock(req, "yesmem-narrative", "Session-Kontext: test")

	blocks, ok := req["system"].([]any)
	if !ok {
		t.Fatal("system should be array after append")
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	second := blocks[1].(map[string]any)
	text, _ := second["text"].(string)
	if !strings.Contains(text, "Session-Kontext: test") {
		t.Errorf("second block text mismatch: %s", text)
	}
	if !strings.HasPrefix(text, "[yesmem-narrative]") {
		t.Errorf("second block should be tagged: %s", text)
	}
}

func TestAppendSystemBlock_ArrayAppend(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}
	AppendSystemBlock(req, "yesmem-narrative", "Session-Kontext: test")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestReplaceSystemBlock_ReplacesExisting(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
			map[string]any{"type": "text", "text": "[yesmem-narrative]\nold content"},
		},
	}
	ReplaceSystemBlock(req, "yesmem-narrative", "new content")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	second := blocks[1].(map[string]any)
	text, _ := second["text"].(string)
	if text != "[yesmem-narrative]\nnew content" {
		t.Errorf("expected replaced content, got: %s", text)
	}
}

func TestReplaceSystemBlock_AppendsIfNotFound(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}
	ReplaceSystemBlock(req, "yesmem-narrative", "new content")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	second := blocks[1].(map[string]any)
	text, _ := second["text"].(string)
	if text != "[yesmem-narrative]\nnew content" {
		t.Errorf("expected appended content, got: %s", text)
	}
}

func TestAppendSystemBlockCached(t *testing.T) {
	req := map[string]any{"system": "You are Claude."}
	AppendSystemBlockCached(req, "yesmem-briefing", "Briefing text")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	second := blocks[1].(map[string]any)
	cc, ok := second["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("cache_control missing")
	}
	if cc["type"] != "ephemeral" {
		t.Error("cache_control type should be ephemeral")
	}
	text, _ := second["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-briefing]") {
		t.Errorf("should be tagged: %s", text)
	}
}

func TestUpsertSystemBlockCached_RespectsBudget(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "sys1", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "sys2", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "msg1", "cache_control": map[string]any{"type": "ephemeral"}},
				},
			},
		},
		"tools": []any{
			map[string]any{"name": "Bash", "cache_control": map[string]any{"type": "ephemeral"}},
		},
	}

	UpsertSystemBlockCached(req, "yesmem-briefing", "Briefing text")

	blocks := req["system"].([]any)
	last := blocks[len(blocks)-1].(map[string]any)
	if _, ok := last["cache_control"]; ok {
		t.Fatal("briefing should not get cache_control when budget is exhausted")
	}
}

func TestUpsertSystemBlockCached_ReplacesExistingWithoutDuplication(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
			map[string]any{
				"type":          "text",
				"text":          "[yesmem-briefing]\nold briefing",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}

	UpsertSystemBlockCached(req, "yesmem-briefing", "new briefing")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	second := blocks[1].(map[string]any)
	if text, _ := second["text"].(string); text != "[yesmem-briefing]\nnew briefing" {
		t.Fatalf("unexpected text: %s", text)
	}
	if _, ok := second["cache_control"]; !ok {
		t.Fatal("existing cache_control should be preserved")
	}
}

func TestReplaceSystemBlock_PreservesCacheControl(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
			map[string]any{
				"type":          "text",
				"text":          "[yesmem-briefing]\nold briefing",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}
	ReplaceSystemBlock(req, "yesmem-briefing", "new briefing")

	blocks := req["system"].([]any)
	second := blocks[1].(map[string]any)
	if _, ok := second["cache_control"]; !ok {
		t.Error("cache_control should be preserved after replace")
	}
	text, _ := second["text"].(string)
	if text != "[yesmem-briefing]\nnew briefing" {
		t.Errorf("text not replaced: %s", text)
	}
}

// --- StripCLAUDEMDDisclaimer tests ---

func TestStripCLAUDEMDDisclaimer_StripsFromSystemBlock(t *testing.T) {
	claudeMdBlock := "# claudeMd\nClaude instructions here.\n\nContents of CLAUDE.md:\n\n## Build\nmake build\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code."},
			map[string]any{"type": "text", "text": claudeMdBlock},
		},
	}

	modified := StripCLAUDEMDDisclaimer(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	text := blocks[1].(map[string]any)["text"].(string)
	if strings.Contains(text, "may or may not be relevant") {
		t.Error("disclaimer should be stripped")
	}
	if !strings.Contains(text, "make build") {
		t.Error("actual CLAUDE.md content should be preserved")
	}
}

func TestStripCLAUDEMDDisclaimer_NoDisclaimerReturnsFalse(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code."},
			map[string]any{"type": "text", "text": "Some other content without disclaimer."},
		},
	}

	modified := StripCLAUDEMDDisclaimer(req)
	if modified {
		t.Error("expected no modification when disclaimer absent")
	}
}

func TestStripCLAUDEMDDisclaimer_PreservesCacheControl(t *testing.T) {
	claudeMdBlock := "# claudeMd\nInstructions.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task."
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          claudeMdBlock,
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}

	StripCLAUDEMDDisclaimer(req)

	blocks := req["system"].([]any)
	cc, ok := blocks[0].(map[string]any)["cache_control"]
	if !ok {
		t.Fatal("cache_control should be preserved")
	}
	if cc.(map[string]any)["type"] != "ephemeral" {
		t.Error("cache_control type should remain ephemeral")
	}
}

func TestStripCLAUDEMDDisclaimer_StripsCleanly(t *testing.T) {
	// The disclaimer may have leading whitespace and trailing newlines.
	// After stripping, no trailing whitespace/blank lines should remain.
	claudeMdBlock := "Instructions here.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n"
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": claudeMdBlock},
		},
	}

	StripCLAUDEMDDisclaimer(req)

	blocks := req["system"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if strings.HasSuffix(text, "\n\n") {
		t.Errorf("should not leave trailing blank lines, got: %q", text)
	}
	if text != "Instructions here." {
		t.Errorf("unexpected result: %q", text)
	}
}

func TestStripCLAUDEMDDisclaimer_StripsFromUserMessage(t *testing.T) {
	// The disclaimer is typically inside a <system-reminder> in a user message, not in system blocks.
	reminder := `<system-reminder>
As you answer the user's questions, you can use the following context:
# claudeMd
Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written.

Contents of /home/testuser/projects/myapp/CLAUDE.md (project instructions):

## Build
make build

      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>`
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code."},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello " + reminder},
		},
	}

	modified := StripCLAUDEMDDisclaimer(req)
	if !modified {
		t.Fatal("expected modification")
	}

	msgs := req["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)
	if strings.Contains(content, "may or may not be relevant") {
		t.Error("disclaimer should be stripped from user message")
	}
	if !strings.Contains(content, "make build") {
		t.Error("actual CLAUDE.md content should be preserved")
	}
	if !strings.Contains(content, "OVERRIDE any default behavior") {
		t.Error("CLAUDE.md header should be preserved")
	}
}

func TestStripCLAUDEMDDisclaimer_StripsFromContentBlocks(t *testing.T) {
	// Messages can have content as array of blocks, not just string.
	reminder := "Some text\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n"
	req := map[string]any{
		"system": []any{},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": reminder},
				},
			},
		},
	}

	modified := StripCLAUDEMDDisclaimer(req)
	if !modified {
		t.Fatal("expected modification")
	}

	msgs := req["messages"].([]any)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	text := blocks[0].(map[string]any)["text"].(string)
	if strings.Contains(text, "may or may not be relevant") {
		t.Error("disclaimer should be stripped from content block")
	}
}

func TestMultipleBlocks(t *testing.T) {
	req := map[string]any{"system": "You are Claude."}
	AppendSystemBlockCached(req, "yesmem-briefing", "Briefing")
	AppendSystemBlock(req, "yesmem-narrative", "Narrative")

	blocks := req["system"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// Replace narrative, briefing should stay
	ReplaceSystemBlock(req, "yesmem-narrative", "Updated Narrative")
	blocks = req["system"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected still 3 blocks after replace, got %d", len(blocks))
	}
}
