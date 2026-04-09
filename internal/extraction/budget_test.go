package extraction

import (
	"strings"
	"testing"
)

// mockLLM implements LLMClient for testing.
type mockLLM struct {
	model string
	calls int
}

func (m *mockLLM) Name() string  { return "mock" }
func (m *mockLLM) Model() string { return m.model }
func (m *mockLLM) Complete(system, userMsg string, opts ...CallOption) (string, error) {
	m.calls++
	return "ok", nil
}
func (m *mockLLM) CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	m.calls++
	return `{"ok":true}`, nil
}

func TestBudgetClient_ThrottleBlocks(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	bc.ThrottleFn = func() bool { return true } // always throttled

	_, err := bc.Complete("sys", "msg")
	if err == nil {
		t.Fatal("expected throttle error, got nil")
	}
	if !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("expected throttle error, got: %s", err)
	}
	if inner.calls != 0 {
		t.Fatalf("inner client should not have been called, got %d calls", inner.calls)
	}
}

func TestBudgetClient_ThrottleBlocksCompleteJSON(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	bc.ThrottleFn = func() bool { return true }

	_, err := bc.CompleteJSON("sys", "msg", nil)
	if err == nil {
		t.Fatal("expected throttle error, got nil")
	}
	if inner.calls != 0 {
		t.Fatalf("inner client should not have been called, got %d calls", inner.calls)
	}
}

func TestBudgetClient_NoThrottleAllows(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	bc.ThrottleFn = func() bool { return false } // not throttled

	result, err := bc.Complete("sys", "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 call, got %d", inner.calls)
	}
}

func TestBudgetClient_NilThrottleFnAllows(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	// ThrottleFn not set — should allow calls (backward compatible)

	result, err := bc.Complete("sys", "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 call, got %d", inner.calls)
	}
}

func TestHasBudget_RespectsThrottle(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	bc.ThrottleFn = func() bool { return true }

	if HasBudget(bc) {
		t.Fatal("HasBudget should return false when throttled")
	}
}

func TestHasBudget_NoThrottleReturnsTrue(t *testing.T) {
	inner := &mockLLM{model: "claude-haiku-4-5-20251001"}
	tracker := NewBudgetTracker(10.0)
	bc := NewBudgetClient(inner, tracker)
	bc.ThrottleFn = func() bool { return false }

	if !HasBudget(bc) {
		t.Fatal("HasBudget should return true when not throttled")
	}
}
