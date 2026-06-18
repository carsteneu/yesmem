package storage

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestStaleness_CRUD(t *testing.T) {
	s := mustOpen(t)

	// Insert test learning
	l := &models.Learning{
		Category:  "gotcha",
		Content:   "Test function FooBar was removed in v2.0",
		Project:   "yesmem",
		ModelUsed: "test",
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	// Set staleness score
	if err := s.SetStalenessScore(id, 0.9, "commit abc123: function removed", "code_removed"); err != nil {
		t.Fatalf("SetStalenessScore: %v", err)
	}

	// Verify via GetStaleLearnings
	stale, err := s.GetStaleLearnings("yesmem", 0.5)
	if err != nil {
		t.Fatalf("GetStaleLearnings: %v", err)
	}
	found := false
	for _, sl := range stale {
		if sl.ID == id {
			found = true
			if sl.StalenessScore != 0.9 {
				t.Errorf("staleness_score = %f, want 0.9", sl.StalenessScore)
			}
			if sl.StalenessReason != "commit abc123: function removed" {
				t.Errorf("staleness_reason = %q", sl.StalenessReason)
			}
			if sl.StalenessType != "code_removed" {
				t.Errorf("staleness_type = %q, want code_removed", sl.StalenessType)
			}
			if sl.StalenessCheckedAt == "" {
				t.Error("staleness_checked_at should not be empty")
			}
		}
	}
	if !found {
		t.Error("learning not found in GetStaleLearnings result")
	}
}

func TestStaleness_FilterByMinScore(t *testing.T) {
	s := mustOpen(t)

	l1 := &models.Learning{Category: "gotcha", Content: "High staleness", Project: "yesmem", ModelUsed: "test"}
	id1, _ := s.InsertLearning(l1)
	s.SetStalenessScore(id1, 0.9, "test", "code_contradicts")

	l2 := &models.Learning{Category: "gotcha", Content: "Medium staleness", Project: "yesmem", ModelUsed: "test"}
	id2, _ := s.InsertLearning(l2)
	s.SetStalenessScore(id2, 0.6, "test", "code_renamed")

	l3 := &models.Learning{Category: "gotcha", Content: "Low staleness", Project: "yesmem", ModelUsed: "test"}
	id3, _ := s.InsertLearning(l3)
	s.SetStalenessScore(id3, 0.3, "test", "code_changed_insight_holds")

	stale, err := s.GetStaleLearnings("yesmem", 0.5)
	if err != nil {
		t.Fatalf("GetStaleLearnings: %v", err)
	}
	ids := make(map[int64]bool)
	for _, sl := range stale {
		ids[sl.ID] = true
	}
	if !ids[id1] {
		t.Error("id1 (0.9) should be in results")
	}
	if !ids[id2] {
		t.Error("id2 (0.6) should be in results")
	}
	if ids[id3] {
		t.Error("id3 (0.3) should NOT be in results")
	}
}

func TestStaleness_Fingerprint(t *testing.T) {
	s := mustOpen(t)

	l := &models.Learning{Category: "pattern", Content: "Fingerprint test", Project: "yesmem", ModelUsed: "test"}
	id, _ := s.InsertLearning(l)

	if err := s.StoreCodeFingerprint(id, "abc123def456"); err != nil {
		t.Fatalf("StoreCodeFingerprint: %v", err)
	}

	loaded, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if loaded.CodeFingerprint != "abc123def456" {
		t.Errorf("code_fingerprint = %q, want abc123def456", loaded.CodeFingerprint)
	}
}

func TestStaleness_GetScores(t *testing.T) {
	s := mustOpen(t)

	l1 := &models.Learning{Category: "gotcha", Content: "Score 0.9", Project: "yesmem", ModelUsed: "test"}
	id1, _ := s.InsertLearning(l1)
	s.SetStalenessScore(id1, 0.9, "t1", "code_contradicts")

	l2 := &models.Learning{Category: "gotcha", Content: "No score", Project: "yesmem", ModelUsed: "test"}
	id2, _ := s.InsertLearning(l2)

	scores, err := s.GetStalenessScores([]int64{id1, id2})
	if err != nil {
		t.Fatalf("GetStalenessScores: %v", err)
	}
	if scores[id1] != 0.9 {
		t.Errorf("score for id1 = %f, want 0.9", scores[id1])
	}
	if _, ok := scores[id2]; ok {
		t.Error("id2 should not be in scores (no staleness check)")
	}
}
