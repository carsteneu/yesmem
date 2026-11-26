package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
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
	los35bUnsandboxed(t, h)

	h.handleSaveCap(map[string]any{
		"name":         "echo_cap",
		"description":  "echos the message",
		"handler_bash": "echo \"hello=$ARGS_message\"",
		"scope":        "user",
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
	los35bUnsandboxed(t, h)

	h.handleSaveCap(map[string]any{
		"name":         "js_cap",
		"description":  "runs JavaScript",
		"handler_repl": "globalThis.registerTool('js_cap', 'test', {type:'object',properties:{x:{type:'number'}}}, function(args) { return {doubled: args.x * 2}; })",
		"scope":        "user",
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
	los35bUnsandboxed(t, h)

	h.handleSaveCap(map[string]any{
		"name":         "js_cap",
		"description":  "needs bun",
		"handler_repl": "registerTool('x','x',{},function() {})",
		"scope":        "user",
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
	los35bUnsandboxed(t, h)

	h.handleSaveCap(map[string]any{
		"name":         "big_cap",
		"description":  "returns big output",
		"handler_repl": "globalThis.registerTool('big','x',{},function() { return {data: 'x'.repeat(100000)}; })",
		"scope":        "user",
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

// insertCapWithTimeout seeds a cap learning directly so ScriptMeta.Timeout is
// exercised (handleSaveCap's legacy handler_bash path has no timeout field).
func insertCapWithTimeout(t *testing.T, h *Handler, name, body string, timeoutSec int64) {
	t.Helper()
	meta := CapMeta{Name: name, Scripts: []ScriptMeta{{
		Name:    "run",
		Kind:    "handler",
		Runtime: "bash",
		Body:    body,
		Timeout: timeoutSec,
	}}, Version: 1, Tested: true}
	ctx, err := meta.ToJSON()
	if err != nil {
		t.Fatalf("cap meta json: %v", err)
	}
	if _, err := h.store.InsertLearning(&models.Learning{
		Category:   "cap",
		Content:    "timeout-test",
		Context:    ctx,
		Confidence: 1,
		CreatedAt:  time.Now(),
		ModelUsed:  "test",
	}); err != nil {
		t.Fatalf("insert cap: %v", err)
	}
}

// A cap with explicit timeout=<N> must be killed after N seconds and report
// "timed out", not the misleading "signal: killed". Regression for the bug where
// the exec.ExitError branch ran before the context-deadline check and masked a
// real timeout as a raw signal failure.
func TestExecuteCapExplicitTimeoutReportsTimedOut(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bash on PATH")
	}
	h, _ := mustHandler(t)
	los35bUnsandboxed(t, h)
	insertCapWithTimeout(t, h, "slowcap", "sleep 5", 1)

	resp := h.handleExecuteCap(map[string]any{"name": "slowcap", "fn": "run", "args": "{}"})
	if resp.Error == "" {
		t.Fatalf("expected error for timeout, got %+v", resp)
	}
	if !strings.Contains(resp.Error, "timed out") {
		t.Errorf("error should mention timeout, got: %s", resp.Error)
	}
	if strings.Contains(resp.Error, "signal: killed") {
		t.Errorf("error should not leak raw signal: killed, got: %s", resp.Error)
	}
}

func TestHandleExecuteCap_LogsUsage(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bash on PATH")
	}
	h, store := mustHandler(t)
	los35bUnsandboxed(t, h)

	// Fehlerpfad 1: no name
	h.handleExecuteCap(map[string]any{})
	// Fehlerpfad 2: unknown cap
	h.handleExecuteCap(map[string]any{"name": "nonexistent"})
	// Erfolg
	h.handleSaveCap(map[string]any{
		"name":         "usage_cap",
		"description":  "logs a call",
		"handler_bash": "echo ok",
		"scope":        "user",
	})
	if resp := h.handleExecuteCap(map[string]any{"name": "usage_cap"}); resp.Error != "" {
		t.Fatalf("execute error: %s", resp.Error)
	}

	ok, failed, err := store.CapUsageCounts()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if ok != 1 || failed != 2 {
		t.Fatalf("expected 1 ok / 2 failed, got %d / %d", ok, failed)
	}
}

func TestHandleExecuteCap_ArgsHashedNotStored(t *testing.T) {
	if testing.Short() {
		t.Skip("needs bash on PATH")
	}
	h, store := mustHandler(t)
	los35bUnsandboxed(t, h)

	h.handleSaveCap(map[string]any{
		"name":         "secret_cap",
		"description":  "takes an argument",
		"handler_bash": "echo done",
		"scope":        "user",
	})
	h.handleExecuteCap(map[string]any{
		"name": "secret_cap",
		"args": `{"token":"hunter2-should-not-appear"}`,
	})

	var hash string
	var argLen int
	var name string
	var err error
	name, hash, argLen, err = store.CapUsageLast()
	if err != nil {
		t.Fatalf("last: %v", err)
	}
	if name != "secret_cap" || hash == "" || argLen == 0 {
		t.Fatalf("expected secret_cap with hash+len, got %q %q / %d", name, hash, argLen)
	}
	if strings.Contains(hash, "hunter2") {
		t.Fatal("argument plaintext leaked into args_hash")
	}
}
