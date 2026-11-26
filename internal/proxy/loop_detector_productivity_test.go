package proxy

import "testing"

// Regression tests for the idle-consumer detector's productivity set.
//
// Two real false positives motivated these:
//  1. The set used "Write"/"Edit" while the proxy sees lower-case tool names,
//     so no tool ever matched and normal turns were reported as consumer loops.
//  2. Code mode ("execute") was missing entirely, so a turn that only ran code
//     counted as consumptive.
func TestHasProductiveTool_LowerCaseNamesMatch(t *testing.T) {
	for _, name := range []string{"write", "edit", "execute", "task", "webfetch", "yesmem_remember"} {
		msg := toolUseMessage(name, map[string]any{})
		if !hasProductiveTool(msg) {
			t.Errorf("tool %q must count as productive", name)
		}
	}
}

func TestHasProductiveTool_CaseInsensitive(t *testing.T) {
	for _, name := range []string{"Write", "Edit", "EXECUTE"} {
		msg := toolUseMessage(name, map[string]any{})
		if !hasProductiveTool(msg) {
			t.Errorf("tool %q must count as productive regardless of case", name)
		}
	}
}

func TestHasProductiveTool_ReadOnlyIsNotProductive(t *testing.T) {
	for _, name := range []string{"read", "grep", "glob", "list", "bash"} {
		msg := toolUseMessage(name, map[string]any{})
		if hasProductiveTool(msg) {
			t.Errorf("tool %q must NOT count as productive", name)
		}
	}
}

func TestDetectIdleConsumer_ProductiveTurnIsNoLoop(t *testing.T) {
	msgs := []any{
		assistantMessage(toolUseBlock("read")),
		assistantMessage(toolUseBlock("read")),
		assistantMessage(toolUseBlock("execute")),
	}
	if sig := detectIdleConsumer(msgs); sig != nil {
		t.Fatalf("turn with an execute call must not be a consumer loop, got type %d: %s", sig.Type, sig.Description)
	}
}

func TestDetectIdleConsumer_ConsumptiveTurnsAlarm(t *testing.T) {
	msgs := []any{
		assistantMessage(toolUseBlock("read")),
		assistantMessage(toolUseBlock("grep")),
		assistantMessage(toolUseBlock("glob")),
	}
	sig := detectIdleConsumer(msgs)
	if sig == nil {
		t.Fatal("three consumptive turns must raise the idle-consumer signal")
	}
	if sig.Type != LoopIdleConsumer {
		t.Fatalf("expected LoopIdleConsumer, got type %d", sig.Type)
	}
}

// toolUseBlock builds a tool_use content block.
func toolUseBlock(name string) map[string]any {
	return map[string]any{"type": "tool_use", "name": name, "input": map[string]any{}}
}

// toolUseMessage builds a single assistant message holding one tool_use block.
func toolUseMessage(name string, input map[string]any) map[string]any {
	return map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "tool_use", "name": name, "input": input},
		},
	}
}

func assistantMessage(blocks ...map[string]any) map[string]any {
	content := make([]any, 0, len(blocks))
	for _, b := range blocks {
		content = append(content, b)
	}
	return map[string]any{"role": "assistant", "content": content}
}
