package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestGetSessionFlavorsForSession(t *testing.T) {
	s := mustOpen(t)

	// Insert learnings with different flavors for same session (simulates multiple extraction runs)
	now := time.Now()
	s.InsertLearning(&models.Learning{
		SessionID: "mega-session-123", Category: "gotcha", Content: "early gotcha",
		CreatedAt: now.Add(-2 * time.Hour), ModelUsed: "haiku", SessionFlavor: "Phase A: Initial exploration",
	})
	s.InsertLearning(&models.Learning{
		SessionID: "mega-session-123", Category: "decision", Content: "mid decision",
		CreatedAt: now.Add(-1 * time.Hour), ModelUsed: "haiku", SessionFlavor: "Phase B: Deep debugging",
	})
	s.InsertLearning(&models.Learning{
		SessionID: "mega-session-123", Category: "gotcha", Content: "late gotcha",
		CreatedAt: now, ModelUsed: "haiku", SessionFlavor: "Phase C: Final fixes",
	})
	// Learning from different session (should NOT appear)
	s.InsertLearning(&models.Learning{
		SessionID: "other-session", Category: "gotcha", Content: "other",
		CreatedAt: now, ModelUsed: "haiku", SessionFlavor: "Other session flavor",
	})

	flavors, err := s.GetSessionFlavorsForSession("mega-session-123")
	if err != nil {
		t.Fatalf("get flavors: %v", err)
	}

	// Should return all 3 distinct flavors for this session, chronologically
	if len(flavors) != 3 {
		t.Errorf("expected 3 flavors, got %d: %v", len(flavors), flavors)
	}

	// Verify chronological order (oldest first)
	expected := []string{"Phase A: Initial exploration", "Phase B: Deep debugging", "Phase C: Final fixes"}
	for i, exp := range expected {
		if i >= len(flavors) {
			break
		}
		flavor, ok := flavors[i]["session_flavor"].(string)
		if !ok || flavor != exp {
			t.Errorf("flavor[%d]: expected %q, got %v", i, exp, flavors[i]["session_flavor"])
		}
	}

	// Other session should not appear
	for _, f := range flavors {
		if f["session_flavor"] == "Other session flavor" {
			t.Error("other session flavor should not appear")
		}
	}
}

func TestUpdateSessionFlavorOnlyEmpty(t *testing.T) {
	s := mustOpen(t)

	now := time.Now()
	// Learning WITH existing flavor (should NOT be overwritten)
	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "gotcha", Content: "has flavor",
		CreatedAt: now.Add(-1 * time.Hour), ModelUsed: "haiku", SessionFlavor: "Phase A",
	})
	// Learning WITHOUT flavor (should be set)
	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "decision", Content: "no flavor",
		CreatedAt: now, ModelUsed: "haiku", SessionFlavor: "",
	})
	// Another learning WITHOUT flavor (should be set)
	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "pattern", Content: "also no flavor",
		CreatedAt: now, ModelUsed: "haiku",
	})

	n, err := s.UpdateSessionFlavorOnlyEmpty("s1", "Phase B")
	if err != nil {
		t.Fatalf("update flavor: %v", err)
	}
	// Only 2 learnings should be updated (the ones without flavor)
	if n != 2 {
		t.Errorf("expected 2 updated, got %d", n)
	}

	// Verify: learning with "Phase A" should still have "Phase A"
	learnings, _ := s.GetActiveLearnings("", "", "", "")
	for _, l := range learnings {
		if l.SessionID != "s1" {
			continue
		}
		if l.Content == "has flavor" && l.SessionFlavor != "Phase A" {
			t.Errorf("existing flavor was overwritten: expected 'Phase A', got %q", l.SessionFlavor)
		}
		if l.Content == "no flavor" && l.SessionFlavor != "Phase B" {
			t.Errorf("empty flavor not set: expected 'Phase B', got %q", l.SessionFlavor)
		}
		if l.Content == "also no flavor" && l.SessionFlavor != "Phase B" {
			t.Errorf("empty flavor not set: expected 'Phase B', got %q", l.SessionFlavor)
		}
	}
}
