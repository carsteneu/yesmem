package daemon

import (
	"strings"
	"testing"
)

// --- Plan CRUD ---

func TestHandleSetPlan(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	resp := h.handleSetPlan(map[string]any{
		"plan":      "⬜ Step 1\n⬜ Step 2",
		"scope":     "session",
		"thread_id": "thread-plan-1",
		"project":   "yesmem",
	})
	if resp.Error != "" {
		t.Fatalf("set plan error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "active" {
		t.Errorf("expected active, got %q", m["status"])
	}
}

func TestHandleSetPlan_RequiresPlan(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSetPlan(map[string]any{"thread_id": "t-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing plan")
	}
}

func TestHandleSetPlan_DefaultsToSession(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	resp := h.handleSetPlan(map[string]any{"plan": "test", "thread_id": "t-scope"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	resp = h.handleGetPlan(map[string]any{"thread_id": "t-scope"})
	m := resultMap(t, resp)
	if m["scope"] != "session" {
		t.Errorf("expected default session scope, got %q", m["scope"])
	}
}

func TestHandleGetPlan_NoPlan(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetPlan(map[string]any{"thread_id": "nonexistent"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["exists"] != false {
		t.Error("expected exists=false for missing plan")
	}
}

func TestHandleGetPlan_ReturnsPlan(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "my plan", "thread_id": "t-get", "project": "proj"})

	resp := h.handleGetPlan(map[string]any{"thread_id": "t-get"})
	m := resultMap(t, resp)
	if m["exists"] != true {
		t.Fatal("expected exists=true")
	}
	if m["plan"] != "my plan" {
		t.Errorf("expected 'my plan', got %q", m["plan"])
	}
	if m["project"] != "proj" {
		t.Errorf("expected project 'proj', got %q", m["project"])
	}
}

// --- Plan Updates ---

func TestHandleUpdatePlan_MarkCompleted(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "⬜ Step 1\n⬜ Step 2", "thread_id": "t-up"})

	resp := h.handleUpdatePlan(map[string]any{
		"thread_id": "t-up",
		"completed": []any{"Step 1"},
	})
	if resp.Error != "" {
		t.Fatalf("update error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	plan := m["plan"].(string)
	if !strings.Contains(plan, "✅") {
		t.Error("expected ✅ marker after completing step")
	}
	if !strings.Contains(plan, "⬜ Step 2") {
		t.Error("Step 2 should remain uncompleted")
	}
}

func TestHandleUpdatePlan_AddItems(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "⬜ Step 1", "thread_id": "t-add"})

	resp := h.handleUpdatePlan(map[string]any{
		"thread_id": "t-add",
		"add":       []any{"⬜ Step 3"},
	})
	m := resultMap(t, resp)
	plan := m["plan"].(string)
	if !strings.Contains(plan, "Step 3") {
		t.Error("expected added Step 3")
	}
}

func TestHandleUpdatePlan_RemoveItems(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "⬜ Step 1\n⬜ Step 2\n⬜ Step 3", "thread_id": "t-rm"})

	resp := h.handleUpdatePlan(map[string]any{
		"thread_id": "t-rm",
		"remove":    []any{"Step 2"},
	})
	m := resultMap(t, resp)
	plan := m["plan"].(string)
	if strings.Contains(plan, "Step 2") {
		t.Error("Step 2 should be removed")
	}
	if !strings.Contains(plan, "Step 1") || !strings.Contains(plan, "Step 3") {
		t.Error("Step 1 and 3 should remain")
	}
}

func TestHandleUpdatePlan_ReplaceContent(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "old plan", "thread_id": "t-replace"})

	resp := h.handleUpdatePlan(map[string]any{
		"thread_id": "t-replace",
		"plan":      "completely new plan",
	})
	m := resultMap(t, resp)
	if m["plan"] != "completely new plan" {
		t.Errorf("expected replaced plan, got %q", m["plan"])
	}
}

func TestHandleUpdatePlan_NoPlan(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdatePlan(map[string]any{"thread_id": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error when no active plan")
	}
}

func TestHandleUpdatePlan_CompletedPlanRejects(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "test", "thread_id": "t-done"})
	h.handleCompletePlan(map[string]any{"thread_id": "t-done"})

	resp := h.handleUpdatePlan(map[string]any{"thread_id": "t-done", "add": []any{"nope"}})
	if resp.Error == "" {
		t.Fatal("expected error when updating completed plan")
	}
}

// --- Plan Complete ---

func TestHandleCompletePlan(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "test", "thread_id": "t-complete"})

	resp := h.handleCompletePlan(map[string]any{"thread_id": "t-complete"})
	if resp.Error != "" {
		t.Fatalf("complete error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "completed" {
		t.Errorf("expected completed, got %q", m["status"])
	}

	// Verify via get
	resp = h.handleGetPlan(map[string]any{"thread_id": "t-complete"})
	m = resultMap(t, resp)
	if m["status"] != "completed" {
		t.Errorf("get should show completed, got %q", m["status"])
	}
}

func TestHandleCompletePlan_NoPlan(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleCompletePlan(map[string]any{"thread_id": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error when no plan to complete")
	}
}

// --- Package-level accessors ---

func TestGetActivePlan(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	h.handleSetPlan(map[string]any{"plan": "active plan content", "thread_id": "t-access"})

	content, ok := GetActivePlan("t-access")
	if !ok {
		t.Fatal("expected active plan")
	}
	if content != "active plan content" {
		t.Errorf("expected plan content, got %q", content)
	}
}

func TestGetActivePlan_NotFound(t *testing.T) {
	_, ok := GetActivePlan("nonexistent-thread")
	if ok {
		t.Fatal("expected no plan for unknown thread")
	}
}

func TestHasActivePlan(t *testing.T) {
	h, _ := mustHandler(t)
	defer cleanupPlans()

	if HasActivePlan("t-has") {
		t.Fatal("should be false before set")
	}

	h.handleSetPlan(map[string]any{"plan": "x", "thread_id": "t-has"})
	if !HasActivePlan("t-has") {
		t.Fatal("should be true after set")
	}

	h.handleCompletePlan(map[string]any{"thread_id": "t-has"})
	if HasActivePlan("t-has") {
		t.Fatal("should be false after complete")
	}
}

// --- Docs Hint ---

func TestHandleGetDocsHint_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetDocsHint(map[string]any{})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

// --- Pure helpers ---

func TestMarkCompleted(t *testing.T) {
	input := "⬜ Step 1\n⬜ Step 2\n🔄 Step 3"

	result := markCompleted(input, "Step 1")
	if !strings.Contains(result, "✅ Step 1") {
		t.Errorf("Step 1 should be marked with ✅, got: %s", result)
	}
	if !strings.Contains(result, "⬜ Step 2") {
		t.Error("Step 2 should remain unchanged")
	}

	result = markCompleted(input, "Step 3")
	if !strings.Contains(result, "✅ Step 3") {
		t.Errorf("🔄 should be replaced with ✅, got: %s", result)
	}
}

func TestMarkCompleted_AlreadyCompleted(t *testing.T) {
	input := "✅ Already done"
	result := markCompleted(input, "Already done")
	if strings.Count(result, "✅") != 1 {
		t.Errorf("should not double-mark, got: %s", result)
	}
}

func TestRemoveLine(t *testing.T) {
	input := "Line 1\nLine 2\nLine 3"
	result := removeLine(input, "Line 2")
	if strings.Contains(result, "Line 2") {
		t.Error("Line 2 should be removed")
	}
	if !strings.Contains(result, "Line 1") || !strings.Contains(result, "Line 3") {
		t.Error("Line 1 and 3 should remain")
	}
}

func TestRemoveLine_NoMatch(t *testing.T) {
	input := "Line 1\nLine 2"
	result := removeLine(input, "nonexistent")
	if result != input {
		t.Errorf("should be unchanged when no match, got: %s", result)
	}
}

// cleanupPlans removes all plans from the global store (test isolation).
func cleanupPlans() {
	planStore.Lock()
	planStore.plans = make(map[string]*Plan)
	planStore.Unlock()
}
