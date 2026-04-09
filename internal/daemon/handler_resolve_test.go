package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestHandleResolve(t *testing.T) {
	h, s := mustHandler(t)

	// Insert an unfinished item
	id, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Fix the login bug",
		Project: "myproject", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	resp := h.Handle(Request{
		Method: "resolve",
		Params: map[string]any{
			"learning_id": float64(id), // JSON numbers are float64
			"reason":      "fixed in commit abc123",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	msg, _ := result["message"].(string)
	if msg == "" {
		t.Error("expected message in response")
	}

	// Verify learning is no longer active
	active, _ := s.GetActiveLearnings("unfinished", "myproject", "", "")
	if len(active) != 0 {
		t.Errorf("expected 0 active unfinished, got %d", len(active))
	}
}

func TestHandleResolveMissingID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "resolve",
		Params: map[string]any{
			"reason": "some reason",
		},
	})

	if resp.Error == "" {
		t.Error("expected error for missing learning_id")
	}
}

func TestHandleResolveNonExistent(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "resolve",
		Params: map[string]any{
			"learning_id": float64(99999),
			"reason":      "does not exist",
		},
	})

	if resp.Error == "" {
		t.Error("expected error for non-existent learning")
	}
}

func TestHandleResolveByText(t *testing.T) {
	h, s := mustHandler(t)

	// Insert unfinished items
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Fix the login authentication bug",
		Project: "myproject", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Add user profile page",
		Project: "myproject", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	resp := h.Handle(Request{
		Method: "resolve_by_text",
		Params: map[string]any{
			"text":    "login authentication",
			"project": "myproject",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	// Should have resolved something
	if result["id"] == nil {
		t.Error("expected id in response")
	}

	// Only the profile page task should remain
	active, _ := s.GetActiveLearnings("unfinished", "myproject", "", "")
	if len(active) != 1 {
		t.Fatalf("expected 1 remaining unfinished, got %d", len(active))
	}
	if active[0].Content != "Add user profile page" {
		t.Errorf("wrong remaining task: %q", active[0].Content)
	}
}

func TestHandleResolveByTextNoMatch(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "resolve_by_text",
		Params: map[string]any{
			"text":    "nonexistent task xyz",
			"project": "myproject",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	if result["id"] != nil {
		t.Error("should not resolve anything for no match")
	}
}
