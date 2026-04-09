package proxy

import (
	"strings"
	"testing"
)

// --- StripOutputEfficiency ---

func TestStripOutputEfficiency_RemovesSection(t *testing.T) {
	text := "# Introduction\nSome intro text.\n\n# Output efficiency\nBe brief.\nUse short answers.\n\n# Next Section\nMore content here."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripOutputEfficiency(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)

	if strings.Contains(result, "Output efficiency") {
		t.Error("section header should be removed")
	}
	if strings.Contains(result, "Be brief.") {
		t.Error("section body should be removed")
	}
	if !strings.Contains(result, "Introduction") {
		t.Error("preceding section should be preserved")
	}
	if !strings.Contains(result, "Next Section") {
		t.Error("following section should be preserved")
	}
}

func TestStripOutputEfficiency_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "# Introduction\nSome intro text."},
		},
	}

	modified := StripOutputEfficiency(req)
	if modified {
		t.Error("expected false when section not present")
	}
}

func TestStripOutputEfficiency_PreservesCacheControl(t *testing.T) {
	text := "# Output efficiency\nBe terse.\n\n# Other\nContent."
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          text,
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}

	StripOutputEfficiency(req)

	blocks := req["system"].([]any)
	block := blocks[0].(map[string]any)
	cc, ok := block["cache_control"]
	if !ok {
		t.Fatal("cache_control should be preserved after modification")
	}
	if cc.(map[string]any)["type"] != "ephemeral" {
		t.Error("cache_control type should remain ephemeral")
	}
}

func TestStripOutputEfficiency_SectionAtEnd(t *testing.T) {
	text := "# Introduction\nSome intro.\n\n# Output efficiency\nBe brief and short."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripOutputEfficiency(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "Output efficiency") {
		t.Error("section should be removed even at EOF")
	}
	if !strings.Contains(result, "Introduction") {
		t.Error("preceding section should be preserved")
	}
}

// --- StripToneBrevity ---

func TestStripToneBrevity_RemovesLine(t *testing.T) {
	text := "You are a helpful assistant.\nYour responses should be short and concise.\nAnswer questions accurately."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripToneBrevity(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)

	if strings.Contains(result, "Your responses should be short and concise.") {
		t.Error("line should be removed")
	}
	if !strings.Contains(result, "You are a helpful assistant.") {
		t.Error("preceding line should be preserved")
	}
	if !strings.Contains(result, "Answer questions accurately.") {
		t.Error("following line should be preserved")
	}
}

func TestStripToneBrevity_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are a helpful assistant."},
		},
	}

	modified := StripToneBrevity(req)
	if modified {
		t.Error("expected false when line not present")
	}
}

// --- InjectAntDirectives ---

func TestInjectAntDirectives_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectAntDirectives(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-directives]") {
		t.Errorf("block should be tagged yesmem-directives, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "verify it actually works") {
		t.Error("should contain verification directive")
	}
	if !strings.Contains(text, "Report outcomes faithfully") {
		t.Error("should contain reporting directive")
	}
	if !strings.Contains(text, "collaborator") {
		t.Error("should contain collaborator directive")
	}
	if !strings.Contains(text, "Err on the side of more explanation") {
		t.Error("should contain explanation directive")
	}
}

// --- InjectCLAUDEMDAuthority ---

func TestInjectCLAUDEMDAuthority_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectCLAUDEMDAuthority(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-enhance]") {
		t.Errorf("block should be tagged yesmem-enhance, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "CLAUDE.md") {
		t.Error("should mention CLAUDE.md")
	}
	if !strings.Contains(text, "authoritative") {
		t.Error("should mention authoritative")
	}
	if !strings.Contains(text, "Comment discipline") {
		t.Error("should contain comment discipline section")
	}
}

// --- InjectPersonaTone ---

func TestInjectPersonaTone_Verbose(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "verbose")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-tone]") {
		t.Errorf("block should be tagged yesmem-tone, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "explanation") {
		t.Error("verbose tone should mention explanation")
	}
}

func TestInjectPersonaTone_EmptyIsNoop(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "")

	blocks := req["system"].([]any)
	if len(blocks) != 1 {
		t.Errorf("empty verbosity should be no-op, got %d blocks", len(blocks))
	}
}

func TestInjectPersonaTone_UnknownIsNoop(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "chatterbox")

	blocks := req["system"].([]any)
	if len(blocks) != 1 {
		t.Errorf("unknown verbosity should be no-op, got %d blocks", len(blocks))
	}
}

func TestInjectPersonaTone_Concise(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "concise")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.Contains(text, "concise") {
		t.Error("concise tone should mention concise")
	}
}

// min helper for safe string slicing in test error messages
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
