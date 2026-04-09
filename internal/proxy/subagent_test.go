package proxy

import (
	"strings"
	"testing"
)

// === Subagent Detection ===

// Helper: build a request map with thinking field (main session)
func mainSessionReq() map[string]any {
	return map[string]any{
		"model":   "claude-opus-4-6",
		"thinking": map[string]any{"type": "adaptive"},
		"system": []any{
			map[string]any{"text": "x-anthropic-billing-header: cc_version=2.1.88.cfc; cc_entrypoint=cli; cch=00000;"},
		},
	}
}

// Helper: build a request map without thinking (subagent)
func subagentReq() map[string]any {
	return map[string]any{
		"model":       "claude-opus-4-6",
		"temperature": 1.0,
		"system": []any{
			map[string]any{"text": "x-anthropic-billing-header: cc_version=2.1.88.516; cc_entrypoint=cli; cch=00000;"},
		},
	}
}

func TestIsSubagent_NoThinking(t *testing.T) {
	msgs := buildLargeMessages(30)
	req := subagentReq()
	if !isSubagent(msgs, req) {
		t.Error("request without thinking field should be detected as subagent")
	}
}

func TestIsSubagent_WithThinking(t *testing.T) {
	msgs := buildLargeMessages(1)
	req := mainSessionReq()
	if isSubagent(msgs, req) {
		t.Error("request with thinking field should NOT be detected as subagent")
	}
}

func TestIsSubagent_SdkTsEntrypoint(t *testing.T) {
	msgs := buildLargeMessages(5)
	req := map[string]any{
		"thinking": map[string]any{"type": "adaptive"},
		"system": []any{
			map[string]any{"text": "x-anthropic-billing-header: cc_entrypoint=sdk-ts;"},
		},
	}
	if !isSubagent(msgs, req) {
		t.Error("sdk-ts entrypoint should be detected as subagent regardless of thinking")
	}
}

func TestIsSubagent_HaikuModel(t *testing.T) {
	msgs := buildLargeMessages(5)
	req := map[string]any{
		"model": "claude-haiku-4-5-20251001",
		"system": []any{
			map[string]any{"text": "x-anthropic-billing-header: cc_entrypoint=cli;"},
		},
	}
	if !isSubagent(msgs, req) {
		t.Error("haiku model should be detected as subagent")
	}
}

func TestIsSubagent_CLIWithThinking(t *testing.T) {
	msgs := buildLargeMessages(200)
	req := map[string]any{
		"thinking": map[string]any{"type": "adaptive"},
		"system": []any{
			map[string]any{"text": "x-anthropic-billing-header: cc_entrypoint=cli;"},
		},
	}
	if isSubagent(msgs, req) {
		t.Error("CLI session with thinking should NOT be subagent")
	}
}

func TestIsSubagent_EmptyMessages(t *testing.T) {
	if isSubagent(nil, nil) {
		t.Error("nil messages should not be subagent")
	}
	if isSubagent([]any{}, mainSessionReq()) {
		t.Error("empty messages should not be subagent")
	}
}

// === Subagent DocsHint Injection ===

func TestInjectDocsHintForSubagent(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "Implement the feature"},
		map[string]any{"role": "assistant", "content": "ok"},
	}
	hint := "[Docs available] Reference docs indexed:\n  twig 3.x, go-stdlib\n[/Docs available]"

	result := injectDocsHintForSubagent(msgs, hint)

	if len(result) != len(msgs)+1 {
		t.Fatalf("expected %d messages, got %d", len(msgs)+1, len(result))
	}
	// The hint should be injected as a user message at the end
	last, ok := result[len(result)-1].(map[string]any)
	if !ok {
		t.Fatal("last message should be a map")
	}
	content, _ := last["content"].(string)
	if !strings.Contains(content, "Docs available") {
		t.Errorf("injected message should contain docs hint, got: %s", content)
	}
}

func TestInjectDocsHintForSubagent_EmptyHint(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "Implement the feature"},
	}

	result := injectDocsHintForSubagent(msgs, "")

	if len(result) != len(msgs) {
		t.Error("empty hint should not add messages")
	}
}

func TestInjectDocsHintForSubagent_NilMessages(t *testing.T) {
	result := injectDocsHintForSubagent(nil, "some hint")
	if result != nil {
		t.Error("nil messages should return nil")
	}
}
