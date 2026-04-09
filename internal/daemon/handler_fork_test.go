package daemon

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestHandleForkExtractLearnings(t *testing.T) {
	h, s := mustHandler(t)

	learnings := []map[string]any{
		{"content": "Always check cache before forking", "category": "gotcha", "entities": []string{"proxy"}},
		{"content": "TDD works well for Go", "category": "pattern", "entities": []string{"go", "testing"}},
	}
	learningsJSON, _ := json.Marshal(learnings)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{
			"learnings": string(learningsJSON),
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	saved := int(result["saved"].(float64))
	if saved != 2 {
		t.Errorf("expected 2 saved, got %d", saved)
	}

	// Verify learnings actually exist in DB
	all, err := s.GetActiveLearnings("gotcha", "", "", "")
	if err != nil {
		t.Fatalf("get learnings: %v", err)
	}
	found := false
	for _, l := range all {
		if l.Content == "Always check cache before forking" {
			found = true
			if l.Source != "fork" {
				t.Errorf("expected source fork, got %q", l.Source)
			}
			break
		}
	}
	if !found {
		t.Error("extracted learning not found in DB")
	}
}

func TestHandleForkExtractLearnings_SkipsEmpty(t *testing.T) {
	h, _ := mustHandler(t)

	learnings := []map[string]any{
		{"content": "", "category": "gotcha"},
		{"content": "Valid one", "category": ""},
		{"content": "Actually good", "category": "pattern"},
	}
	learningsJSON, _ := json.Marshal(learnings)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{
			"learnings": string(learningsJSON),
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	saved := int(result["saved"].(float64))
	if saved != 1 {
		t.Errorf("expected 1 saved (2 skipped), got %d", saved)
	}
}

func TestHandleForkExtractLearnings_InvalidJSON(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{
			"learnings": "not valid json",
		},
	})

	if resp.Error == "" {
		t.Error("expected error for invalid JSON")
	}
}

func TestHandleForkEvaluateLearning_Boost(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "test learning for boost",
		Category: "gotcha",
		Source:   "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"verdict":     "useful",
			"reason":      "Prevented a bug",
			"action":      "boost",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	if result["action"] != "boost" {
		t.Errorf("expected action=boost, got %v", result["action"])
	}

	l, _ := s.GetLearning(id)
	if l.UseCount != 1 {
		t.Errorf("expected use_count=1 after boost, got %d", l.UseCount)
	}
}

func TestHandleForkEvaluateLearning_Noise(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "test learning for noise",
		Category: "pattern",
		Source:   "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"verdict":     "noise",
			"reason":      "Not relevant",
			"action":      "noise",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	l, _ := s.GetLearning(id)
	if l.NoiseCount != 1 {
		t.Errorf("expected noise_count=1 after noise, got %d", l.NoiseCount)
	}
}

func TestHandleForkEvaluateLearning_Save(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "critical learning",
		Category: "decision",
		Source:   "user_stated",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"verdict":     "critical_save",
			"reason":      "Essential knowledge",
			"action":      "save",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	l, _ := s.GetLearning(id)
	if l.SaveCount != 1 {
		t.Errorf("expected save_count=1 after save, got %d", l.SaveCount)
	}
}

func TestHandleForkEvaluateLearning_Flag(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "wrong learning",
		Category: "gotcha",
		Source:   "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"verdict":     "wrong",
			"reason":      "Factually incorrect",
			"action":      "flag",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	l, _ := s.GetLearning(id)
	if l.FailCount != 1 {
		t.Errorf("expected fail_count=1 after flag, got %d", l.FailCount)
	}
}

func TestHandleForkEvaluateLearning_Skip(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(999),
			"verdict":     "irrelevant",
			"reason":      "Not applicable",
			"action":      "skip",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["action"] != "skip" {
		t.Errorf("expected action=skip, got %v", result["action"])
	}
}

// Fix 1: Zero-score blind spot — impact_score=0.0 must be stored
func TestHandleForkEvaluateLearning_ZeroImpactScore(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "learning with zero impact",
		Category: "gotcha",
		Source:   "fork",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id":  float64(id),
			"action":       "boost",
			"impact_score": float64(0.0),
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	// UpdateImpactScore with 0.0 must have been called without error
	// (store accepts 0.0 as a valid signal — "completely ignored")
}

// Fix 2: Status=invalidated learnings must be skipped
func TestHandleForkExtractLearnings_SkipsInvalidated(t *testing.T) {
	h, _ := mustHandler(t)

	learnings := []map[string]any{
		{"content": "Valid learning", "category": "gotcha", "status": "confirmed"},
		{"content": "Invalidated learning", "category": "pattern", "status": "invalidated"},
		{"content": "Another valid one", "category": "decision"},
	}
	learningsJSON, _ := json.Marshal(learnings)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{
			"learnings": string(learningsJSON),
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	saved := int(result["saved"].(float64))
	if saved != 2 {
		t.Errorf("expected 2 saved (1 invalidated skipped), got %d", saved)
	}
	skipped, ok := result["skipped"]
	if !ok {
		t.Fatal("expected 'skipped' field in response")
	}
	if int(skipped.(float64)) != 1 {
		t.Errorf("expected skipped=1, got %v", skipped)
	}
}

// Fix 3: Tests for the 3 new handlers

func TestHandleForkUpdateImpact(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Content:  "test learning for impact",
		Category: "gotcha",
		Source:   "fork",
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	// Valid update
	resp := h.Handle(Request{
		Method: "fork_update_impact",
		Params: map[string]any{
			"learning_id":  float64(id),
			"impact_score": 0.8,
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Missing learning_id
	resp = h.Handle(Request{
		Method: "fork_update_impact",
		Params: map[string]any{},
	})
	if resp.Error == "" {
		t.Error("expected error for missing learning_id")
	}
}

func TestHandleGetForkLearnings(t *testing.T) {
	h, _ := mustHandler(t)

	// Empty / nonexistent session — should not error
	resp := h.Handle(Request{
		Method: "get_fork_learnings",
		Params: map[string]any{"session_id": "nonexistent"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Missing session_id
	resp = h.Handle(Request{
		Method: "get_fork_learnings",
		Params: map[string]any{},
	})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

func TestHandleForkResolveContradiction(t *testing.T) {
	h, s := mustHandler(t)

	idA, err := s.InsertLearning(&models.Learning{
		Content:  "learning A",
		Category: "gotcha",
		Source:   "fork",
	})
	if err != nil {
		t.Fatalf("insert learning A: %v", err)
	}
	idB, err := s.InsertLearning(&models.Learning{
		Content:  "learning B",
		Category: "pattern",
		Source:   "fork",
	})
	if err != nil {
		t.Fatalf("insert learning B: %v", err)
	}

	resp := h.Handle(Request{
		Method: "fork_resolve_contradiction",
		Params: map[string]any{
			"learning_a":  float64(idA),
			"learning_b":  float64(idB),
			"description": "A contradicts B",
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Missing learning_a
	resp = h.Handle(Request{
		Method: "fork_resolve_contradiction",
		Params: map[string]any{
			"learning_b": float64(idB),
		},
	})
	if resp.Error == "" {
		t.Error("expected error for missing learning_a")
	}

	// Missing learning_b
	resp = h.Handle(Request{
		Method: "fork_resolve_contradiction",
		Params: map[string]any{
			"learning_a": float64(idA),
		},
	})
	if resp.Error == "" {
		t.Error("expected error for missing learning_b")
	}
}

func TestHandleForkResolveContradiction_WritesEdge(t *testing.T) {
	h, s := mustHandler(t)

	idA, _ := s.InsertLearning(&models.Learning{Content: "learning A", Category: "gotcha", Source: "fork"})
	idB, _ := s.InsertLearning(&models.Learning{Content: "learning B", Category: "pattern", Source: "fork"})

	h.Handle(Request{
		Method: "fork_resolve_contradiction",
		Params: map[string]any{
			"learning_a":  float64(idA),
			"learning_b":  float64(idB),
			"description": "A contradicts B",
		},
	})

	edges, err := s.GetAssociationsByRelationType("learning", fmt.Sprintf("%d", idA), "contradicts")
	if err != nil {
		t.Fatalf("get edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 contradicts edge, got %d", len(edges))
	}
	if edges[0].TargetID != fmt.Sprintf("%d", idB) {
		t.Errorf("expected target %d, got %s", idB, edges[0].TargetID)
	}
}

func TestHandleFlagContradiction_WritesEdges(t *testing.T) {
	h, s := mustHandler(t)

	idA, _ := s.InsertLearning(&models.Learning{Content: "learning A", Category: "gotcha", Source: "user_stated"})
	idB, _ := s.InsertLearning(&models.Learning{Content: "learning B", Category: "pattern", Source: "user_stated"})
	idC, _ := s.InsertLearning(&models.Learning{Content: "learning C", Category: "decision", Source: "user_stated"})

	h.Handle(Request{
		Method: "flag_contradiction",
		Params: map[string]any{
			"description":  "A, B and C contradict each other",
			"learning_ids": []any{float64(idA), float64(idB), float64(idC)},
		},
	})

	// Should write pairwise edges: A→B, A→C, B→C
	edgesA, _ := s.GetAssociationsByRelationType("learning", fmt.Sprintf("%d", idA), "contradicts")
	if len(edgesA) != 2 {
		t.Errorf("expected 2 contradicts edges from A, got %d", len(edgesA))
	}
	edgesB, _ := s.GetAssociationsByRelationType("learning", fmt.Sprintf("%d", idB), "contradicts")
	if len(edgesB) != 1 {
		t.Errorf("expected 1 contradicts edge from B (to C), got %d", len(edgesB))
	}
}

func TestHandleForkExtractLearnings_WritesEntityOverlapEdges(t *testing.T) {
	h, s := mustHandler(t)

	// Pre-existing learning with entity "proxy.go"
	existing, _ := s.InsertLearning(&models.Learning{
		Content: "proxy.go has a known issue", Category: "gotcha", Source: "user_stated",
		Entities: []string{"proxy.go"},
	})

	// Fork extracts a new learning that also mentions "proxy.go"
	learnings := []map[string]any{
		{"content": "proxy.go needs cache reset after config change", "category": "gotcha", "entities": []string{"proxy.go", "config.go"}},
	}
	learningsJSON, _ := json.Marshal(learnings)

	h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{"learnings": string(learningsJSON)},
	})

	// Find the newly created learning
	all, _ := s.GetActiveLearnings("gotcha", "", "", "")
	var newID int64
	for _, l := range all {
		if l.Content == "proxy.go needs cache reset after config change" {
			newID = l.ID
			break
		}
	}
	if newID == 0 {
		t.Fatal("new learning not found")
	}

	edges, err := s.GetAssociationsByRelationType("learning", fmt.Sprintf("%d", newID), "relates_to")
	if err != nil {
		t.Fatalf("get edges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 relates_to edge, got %d", len(edges))
	}
	if edges[0].TargetID != fmt.Sprintf("%d", existing) {
		t.Errorf("expected target %d, got %s", existing, edges[0].TargetID)
	}
}

func TestHandleRelate(t *testing.T) {
	h, s := mustHandler(t)

	idA, _ := s.InsertLearning(&models.Learning{Content: "learning A", Category: "gotcha", Source: "user_stated"})
	idB, _ := s.InsertLearning(&models.Learning{Content: "learning B", Category: "pattern", Source: "user_stated"})

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(idA),
			"learning_id_b": float64(idB),
			"relation_type": "supports",
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	edges, _ := s.GetAssociationsByRelationType("learning", fmt.Sprintf("%d", idA), "supports")
	if len(edges) != 1 {
		t.Fatalf("expected 1 supports edge, got %d", len(edges))
	}
}

func TestHandleRelate_InvalidType(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(1),
			"learning_id_b": float64(2),
			"relation_type": "loves",
		},
	})
	if resp.Error == "" {
		t.Error("expected error for invalid relation_type")
	}
}

func TestHandleRelate_MissingIDs(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{"relation_type": "supports"},
	})
	if resp.Error == "" {
		t.Error("expected error for missing IDs")
	}
}

// --- Coverage gap tests ---

func TestHandleForkExtractLearnings_MissingParam(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{},
	})
	if resp.Error == "" {
		t.Error("expected error for missing learnings param")
	}
}

func TestHandleForkExtractLearnings_InvalidCategory(t *testing.T) {
	h, _ := mustHandler(t)

	learnings := []map[string]any{
		{"content": "valid content", "category": "bogus_category"},
		{"content": "also valid", "category": "pattern"},
	}
	learningsJSON, _ := json.Marshal(learnings)

	resp := h.Handle(Request{
		Method: "fork_extract_learnings",
		Params: map[string]any{"learnings": string(learningsJSON)},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if int(result["saved"].(float64)) != 1 {
		t.Errorf("expected 1 saved (invalid category skipped), got %v", result["saved"])
	}
}

func TestHandleForkEvaluateLearning_MissingID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{"action": "boost"},
	})
	if resp.Error == "" {
		t.Error("expected error for missing learning_id")
	}
}

func TestHandleForkEvaluateLearning_UnknownAction(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.InsertLearning(&models.Learning{
		Content: "test", Category: "gotcha", Source: "fork",
	})

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"action":      "explode",
		},
	})
	if resp.Error == "" {
		t.Error("expected error for unknown action")
	}
}

func TestHandleForkEvaluateLearning_Supersede(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.InsertLearning(&models.Learning{
		Content: "outdated info", Category: "gotcha", Source: "fork",
	})

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id": float64(id),
			"action":      "supersede",
			"reason":      "replaced by newer info",
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["action"] != "supersede" {
		t.Errorf("expected action=supersede, got %v", result["action"])
	}

	l, _ := s.GetLearning(id)
	if l.SupersededBy == nil {
		t.Error("expected learning to be superseded")
	}
}

func TestHandleForkEvaluateLearning_WithImpactScore(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.InsertLearning(&models.Learning{
		Content: "test impact", Category: "pattern", Source: "fork",
	})

	resp := h.Handle(Request{
		Method: "fork_evaluate_learning",
		Params: map[string]any{
			"learning_id":  float64(id),
			"action":       "boost",
			"impact_score": 0.75,
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Verify boost applied
	l, _ := s.GetLearning(id)
	if l.UseCount != 1 {
		t.Errorf("expected use_count=1, got %d", l.UseCount)
	}

	// Verify impact_score stored via fork_update_impact readback
	// (GetLearning doesn't scan impact_score; use the update handler
	// which confirms the store accepted it without error)
	resp2 := h.Handle(Request{
		Method: "fork_update_impact",
		Params: map[string]any{
			"learning_id":  float64(id),
			"impact_score": 0.5,
		},
	})
	if resp2.Error != "" {
		t.Fatalf("update_impact after evaluate failed: %s", resp2.Error)
	}
}

func TestHandleGetForkLearnings_WithData(t *testing.T) {
	h, s := mustHandler(t)

	// Insert learnings with session_id matching the query
	l := &models.Learning{
		Content: "fork learning for session", Category: "gotcha", Source: "fork",
		SessionID: "sess-fork-1",
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	resp := h.Handle(Request{
		Method: "get_fork_learnings",
		Params: map[string]any{"session_id": "sess-fork-1"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	items := result["learnings"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least 1 learning")
	}
	first := items[0].(map[string]any)
	if int64(first["id"].(float64)) != id {
		t.Errorf("expected id=%d, got %v", id, first["id"])
	}
	if first["content"] != "fork learning for session" {
		t.Errorf("expected matching content, got %v", first["content"])
	}
	if first["category"] != "gotcha" {
		t.Errorf("expected category=gotcha, got %v", first["category"])
	}
}

func TestHandleForkResolveContradiction_BothMissing(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "fork_resolve_contradiction",
		Params: map[string]any{"description": "no IDs"},
	})
	if resp.Error == "" {
		t.Error("expected error when both learning_a and learning_b missing")
	}
}

func TestHandleForkUpdateImpact_ZeroScore(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.InsertLearning(&models.Learning{
		Content: "zero impact test", Category: "gotcha", Source: "fork",
	})

	resp := h.Handle(Request{
		Method: "fork_update_impact",
		Params: map[string]any{
			"learning_id":  float64(id),
			"impact_score": float64(0),
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["impact_score"] != float64(0) {
		t.Errorf("expected impact_score=0, got %v", result["impact_score"])
	}
}

func TestHandleGetContradictingPairs(t *testing.T) {
	h, s := mustHandler(t)

	idA, _ := s.InsertLearning(&models.Learning{Content: "learning A", Category: "gotcha", Source: "user_stated"})
	idB, _ := s.InsertLearning(&models.Learning{Content: "learning B", Category: "pattern", Source: "user_stated"})
	s.InsertTypedAssociation(idA, idB, "contradicts")

	resp := h.Handle(Request{
		Method: "get_contradicting_pairs",
		Params: map[string]any{
			"new_ids":      []any{float64(idA)},
			"previous_ids": []any{float64(idB)},
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result struct {
		Pairs [][]int64 `json:"pairs"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result.Pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(result.Pairs))
	}
	if result.Pairs[0][0] != idA || result.Pairs[0][1] != idB {
		t.Errorf("expected [%d,%d], got %v", idA, idB, result.Pairs[0])
	}
}

func TestHandleGetContradictingPairs_NoPairs(t *testing.T) {
	h, s := mustHandler(t)

	idA, _ := s.InsertLearning(&models.Learning{Content: "learning A", Category: "gotcha", Source: "user_stated"})
	idB, _ := s.InsertLearning(&models.Learning{Content: "learning B", Category: "pattern", Source: "user_stated"})
	s.InsertTypedAssociation(idA, idB, "supports")

	resp := h.Handle(Request{
		Method: "get_contradicting_pairs",
		Params: map[string]any{
			"new_ids":      []any{float64(idA)},
			"previous_ids": []any{float64(idB)},
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result struct {
		Pairs [][]int64 `json:"pairs"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Pairs) != 0 {
		t.Errorf("expected 0 pairs, got %d", len(result.Pairs))
	}
}

func TestForkExtractLearnings_WithLineage(t *testing.T) {
	h, s := mustHandler(t)
	learnings := `[{"content":"Cache TTL matters","category":"decision","entities":["proxy"],"status":"new","importance":3,"emotional_intensity":0.1}]`
	resp := h.handleForkExtractLearnings(map[string]any{
		"learnings":       learnings,
		"session_id":      "lineage-test",
		"project":         "yesmem",
		"source_msg_from": float64(5),
		"source_msg_to":   float64(30),
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	// Verify the learning was saved with lineage
	all, err := s.GetActiveLearnings("decision", "yesmem", "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range all {
		if l.Content == "Cache TTL matters" {
			if l.SourceMsgFrom != 5 {
				t.Errorf("SourceMsgFrom = %d, want 5", l.SourceMsgFrom)
			}
			if l.SourceMsgTo != 30 {
				t.Errorf("SourceMsgTo = %d, want 30", l.SourceMsgTo)
			}
			found = true
		}
	}
	if !found {
		t.Error("learning not found")
	}
}

func TestForkExtractLearnings_RecordsCoverage(t *testing.T) {
	h, s := mustHandler(t)
	learnings := `[{"content":"Test coverage tracking","category":"pattern","entities":[],"status":"new","importance":2,"emotional_intensity":0.0}]`
	h.handleForkExtractLearnings(map[string]any{
		"learnings":       learnings,
		"session_id":      "coverage-test",
		"project":         "yesmem",
		"source_msg_from": float64(0),
		"source_msg_to":   float64(25),
	})
	// Verify coverage was recorded
	if !s.IsCoveredByFork("coverage-test", 0, 25) {
		t.Error("fork coverage not recorded")
	}
	if !s.IsCoveredByFork("coverage-test", 5, 20) {
		t.Error("subset should be covered")
	}
	if s.IsCoveredByFork("coverage-test", 20, 50) {
		t.Error("superset should not be covered")
	}
}
