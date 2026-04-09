package proxy

import (
	"testing"
)

func TestNormalizeBillingHeader_ReplacesHash(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.86; cch=627d9;"},
		},
	}

	changed := NormalizeBillingHeader(req)
	if !changed {
		t.Fatal("expected changed=true")
	}

	text := req["system"].([]any)[0].(map[string]any)["text"].(string)
	expected := "x-anthropic-billing-header: cc_version=2.1.86; cch=00000;"
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

func TestNormalizeBillingHeader_ReplacesRawSentinel(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cch=d6c5c00000;"},
		},
	}

	changed := NormalizeBillingHeader(req)
	if !changed {
		t.Fatal("expected changed=true")
	}

	text := req["system"].([]any)[0].(map[string]any)["text"].(string)
	expected := "x-anthropic-billing-header: cch=00000;"
	if text != expected {
		t.Errorf("expected %q, got %q", expected, text)
	}
}

func TestNormalizeBillingHeader_NoCchField(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.86;"},
		},
	}

	changed := NormalizeBillingHeader(req)
	if changed {
		t.Fatal("expected changed=false when no cch= present")
	}
}

func TestNormalizeBillingHeader_NoSystemBlock(t *testing.T) {
	req := map[string]any{
		"messages": []any{},
	}

	changed := NormalizeBillingHeader(req)
	if changed {
		t.Fatal("expected changed=false with no system block")
	}
}

func TestNormalizeBillingHeader_OnlySystem0(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cch=abcde;"},
			map[string]any{"type": "text", "text": "You are Claude Code with cch=12345 in prompt"},
		},
	}

	NormalizeBillingHeader(req)

	// system[0] normalized
	text0 := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if text0 != "x-anthropic-billing-header: cch=00000;" {
		t.Errorf("system[0] not normalized: %s", text0)
	}

	// system[1] untouched
	text1 := req["system"].([]any)[1].(map[string]any)["text"].(string)
	if text1 != "You are Claude Code with cch=12345 in prompt" {
		t.Errorf("system[1] was mutated: %s", text1)
	}
}

func TestNormalizeBillingHeader_Idempotent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cch=abcde;"},
		},
	}

	NormalizeBillingHeader(req)
	changed := NormalizeBillingHeader(req)

	if changed {
		t.Fatal("second call should return false (already normalized to 00000)")
	}
}

func TestNormalizeBillingHeader_NotBillingHeader(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code, an AI assistant."},
		},
	}

	changed := NormalizeBillingHeader(req)
	if changed {
		t.Fatal("should not change non-billing system[0]")
	}
}
