package daemon

import (
	"testing"
	"time"
)

// --- handleResolveProject ---

func TestHandleResolveProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleResolveProject(map[string]any{"project_dir": "/home/user/projects/test"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandleResolveProject_MissingDir(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleResolveProject(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing project_dir")
	}
}

// --- handleGetLearningsSince ---

func TestHandleGetLearningsSince(t *testing.T) {
	h, _ := mustHandler(t)
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	resp := h.handleGetLearningsSince(map[string]any{"since": since, "project": "test"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandleGetLearningsSince_InvalidTimestamp(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetLearningsSince(map[string]any{"since": "not-a-date"})
	if resp.Error == "" {
		t.Fatal("expected error for invalid since")
	}
}

// --- handleGetSessionFlavorsSince ---

func TestHandleGetSessionFlavorsSince(t *testing.T) {
	h, _ := mustHandler(t)
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	resp := h.handleGetSessionFlavorsSince(map[string]any{"since": since})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandleGetSessionFlavorsSince_InvalidTimestamp(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetSessionFlavorsSince(map[string]any{"since": "nope"})
	if resp.Error == "" {
		t.Fatal("expected error for invalid since")
	}
}

// --- LoadPlansFromDB ---

func TestLoadPlansFromDB_EmptyStore(t *testing.T) {
	_, s := mustHandler(t)
	LoadPlansFromDB(s)
	// Should not panic on empty DB
}

// --- handleGenerateBriefing ---

func TestHandleGenerateBriefing(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGenerateBriefing(map[string]any{"project": "test"})
	// Without LLM client, should still produce a result (or a meaningful error)
	_ = resp
}

// --- Remaining dispatch methods ---

func TestHandle_GetLearningsSince_Via_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	since := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	resp := h.Handle(Request{
		Method: "get_learnings_since",
		Params: map[string]any{"since": since, "project": "yesmem"},
	})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

func TestHandle_ResolveProject_Via_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "resolve_project",
		Params: map[string]any{"project_dir": "/tmp/test"},
	})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

func TestHandle_QuarantineSession_Via_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "quarantine_session",
		Params: map[string]any{"session_id": "test-quarantine"},
	})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}
