package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
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

// --- handleGetSessionFlavorsForSession ---

func TestHandleGetSessionFlavorsForSession(t *testing.T) {
	h, s := mustHandler(t)

	// Seed learnings with different flavors for same session
	now := time.Now()
	s.InsertLearning(&models.Learning{
		SessionID: "test-flavor-session", Category: "gotcha", Content: "early",
		CreatedAt: now.Add(-2 * time.Hour), ModelUsed: "haiku", SessionFlavor: "Phase A",
	})
	s.InsertLearning(&models.Learning{
		SessionID: "test-flavor-session", Category: "decision", Content: "late",
		CreatedAt: now, ModelUsed: "haiku", SessionFlavor: "Phase B",
	})

	resp := h.handleGetSessionFlavorsForSession(map[string]any{"session_id": "test-flavor-session"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var flavors []map[string]any
	if err := json.Unmarshal(resp.Result, &flavors); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(flavors) != 2 {
		t.Fatalf("expected 2 flavors, got %d", len(flavors))
	}
	// Verify chronological order (Phase A before Phase B)
	if flavors[0]["session_flavor"] != "Phase A" {
		t.Errorf("expected first flavor 'Phase A', got %v", flavors[0]["session_flavor"])
	}
	if flavors[1]["session_flavor"] != "Phase B" {
		t.Errorf("expected second flavor 'Phase B', got %v", flavors[1]["session_flavor"])
	}
}

func TestHandleGetSessionFlavorsForSession_MissingParam(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetSessionFlavorsForSession(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
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

// --- handleGetSessionStart ---

func TestHandleGetSessionStart(t *testing.T) {
	h, s := mustHandler(t)

	// Seed a session with a known start time
	sess := &models.Session{
		ID:           "test-session-abc",
		Project:      "/home/user/myproject",
		ProjectShort: "myproject",
		StartedAt:    time.Date(2026, 4, 10, 14, 30, 0, 0, time.UTC),
		JSONLPath:    "/tmp/test.jsonl",
		IndexedAt:    time.Now(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	resp := h.Handle(Request{
		Method: "get_session_start",
		Params: map[string]any{"session_id": "test-session-abc"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result struct {
		StartedAt string `json:"started_at"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, result.StartedAt)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	if !parsed.Equal(sess.StartedAt) {
		t.Fatalf("expected %v, got %v", sess.StartedAt, parsed)
	}
}

func TestHandleGetSessionStart_NotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "get_session_start",
		Params: map[string]any{"session_id": "nonexistent-session"},
	})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestHandleGetSessionStart_MissingParam(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "get_session_start",
		Params: map[string]any{},
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
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
