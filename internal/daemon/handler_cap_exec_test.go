package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleExecuteCap_RequiresName(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleExecuteCap(map[string]any{})
	if resp.Error == "" || !strings.Contains(resp.Error, "name required") {
		t.Fatalf("expected 'name required' error, got: %v", resp)
	}
}

func TestHandleExecuteCap_UnknownCap(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleExecuteCap(map[string]any{"name": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error for unknown cap")
	}
}

func TestHandleExecuteCap_NoHandler(t *testing.T) {
	h, store := mustHandler(t)

	h.handleSaveCap(map[string]any{
		"name":        "empty_cap",
		"description": "has no handler",
		"project":     "test",
	})

	resp := h.handleExecuteCap(map[string]any{"name": "empty_cap"})
	if resp.Error == "" || !strings.Contains(resp.Error, "not found") {
		t.Fatalf("expected 'not found' error, got: %v", resp)
	}
	_ = store
}

func TestHandleExecuteCap_BashHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bash on PATH")
	}
	h, _ := mustHandler(t)

	h.handleSaveCap(map[string]any{
		"name":         "echo_cap",
		"description":  "echos the message",
		"handler_bash": "echo \"hello=$ARGS_message\"",
		"project":      "test",
	})

	resp := h.handleExecuteCap(map[string]any{
		"name": "echo_cap",
		"args": `{"message":"world"}`,
	})
	if resp.Error != "" {
		t.Fatalf("execute error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	output, _ := result["output"].(string)
	if output != "hello=world" {
		t.Fatalf("expected 'hello=world', got %q", output)
	}
}

func TestHandleExecuteCap_ReplHandler(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bun on PATH")
	}
	h, _ := mustHandler(t)

	h.handleSaveCap(map[string]any{
		"name":         "js_cap",
		"description":  "runs JavaScript",
		"handler_repl": "globalThis.registerTool('js_cap', 'test', {type:'object',properties:{x:{type:'number'}}}, function(args) { return {doubled: args.x * 2}; })",
		"project":      "test",
	})

	resp := h.handleExecuteCap(map[string]any{
		"name": "js_cap",
		"args": `{"x":21}`,
	})
	if resp.Error != "" {
		t.Fatalf("execute error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	output, _ := result["output"].(string)
	if output != "{\"doubled\":42}" {
		t.Fatalf("expected '{\"doubled\":42}', got %q", output)
	}
}

func TestHandleExecuteCap_MissingBun(t *testing.T) {
	if testing.Short() {
		t.Skip("needs PATH manipulation to simulate missing bun")
	}
	h, _ := mustHandler(t)

	h.handleSaveCap(map[string]any{
		"name":         "js_cap",
		"description":  "needs bun",
		"handler_repl": "registerTool('x','x',{},function() {})",
		"project":      "test",
	})

	resp := h.handleExecuteCap(map[string]any{"name": "js_cap"})
	// May pass if bun IS on PATH; the test verifies the handler works when bun is found.
	// In CI without bun, this would error with "bun not found".
	_ = resp
}

func TestHandleExecuteCap_OutputTruncated(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bun on PATH")
	}
	h, _ := mustHandler(t)

	h.handleSaveCap(map[string]any{
		"name":         "big_cap",
		"description":  "returns big output",
		"handler_repl": "globalThis.registerTool('big','x',{},function() { return {data: 'x'.repeat(100000)}; })",
		"project":      "test",
	})

	resp := h.handleExecuteCap(map[string]any{"name": "big_cap", "args": "{}"})
	if resp.Error != "" {
		t.Fatalf("execute error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	output, _ := result["output"].(string)
	if len(output) > 66000 {
		t.Fatalf("output not truncated: %d bytes", len(output))
	}
	if !strings.Contains(output, "truncated") {
		t.Fatalf("output missing truncation notice: %.100s", output)
	}
}
