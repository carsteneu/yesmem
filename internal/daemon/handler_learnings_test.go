package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestHandleRememberStoresExplicitModel(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Explizites Modell soll gespeichert werden",
			"category": "decision",
			"project":  "yesmem",
			"model":    "gpt-5.4-mini",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := result["model_used"]; got != "gpt-5.4-mini" {
		t.Fatalf("response model_used: got %v, want %q", got, "gpt-5.4-mini")
	}

	id, ok := result["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("invalid id in response: %v", result["id"])
	}

	learning, err := s.GetLearning(int64(id))
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if learning.ModelUsed != "gpt-5.4-mini" {
		t.Fatalf("stored model_used: got %q, want %q", learning.ModelUsed, "gpt-5.4-mini")
	}
}

func TestHandleRememberFallsBackToSelfModel(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Ohne Modell bleibt der bestehende Fallback aktiv",
			"category": "decision",
			"project":  "yesmem",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := result["model_used"]; got != "self" {
		t.Fatalf("response model_used: got %v, want %q", got, "self")
	}

	id, ok := result["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("invalid id in response: %v", result["id"])
	}

	learning, err := s.GetLearning(int64(id))
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if learning.ModelUsed != "self" {
		t.Fatalf("stored model_used: got %q, want %q", learning.ModelUsed, "self")
	}
}

// TestHandleRememberFallsBackToSessionModel verifies that when no explicit model
// is passed but the proxy has persisted session_model:<sid> in proxy_state
// (CC case: proxy.go persists it per request), the daemon picks it up.
//
// This fixes the bug where in-session remember() calls got model_used='self'
// even though the proxy knew the real model. See learning #76567.
func TestHandleRememberFallsBackToSessionModel(t *testing.T) {
	h, s := mustHandler(t)

	// Simulate proxy persisting the current session's model.
	if err := s.SetProxyState("session_model:ses-abc-123", "claude-opus-4-7"); err != nil {
		t.Fatalf("SetProxyState: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":        "Modell wird via session_model proxy_state aufgelöst",
			"category":    "decision",
			"project":     "yesmem",
			"_session_id": "ses-abc-123",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := result["model_used"]; got != "claude-opus-4-7" {
		t.Fatalf("response model_used: got %v, want %q", got, "claude-opus-4-7")
	}

	id, ok := result["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("invalid id in response: %v", result["id"])
	}

	learning, err := s.GetLearning(int64(id))
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if learning.ModelUsed != "claude-opus-4-7" {
		t.Fatalf("stored model_used: got %q, want %q", learning.ModelUsed, "claude-opus-4-7")
	}
}

// TestHandleRememberExplicitModelOverridesProxyState verifies that an explicit
// `model` param still wins over the session_model proxy_state lookup.
func TestHandleRememberExplicitModelOverridesProxyState(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.SetProxyState("session_model:ses-xyz", "claude-opus-4-7"); err != nil {
		t.Fatalf("SetProxyState: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":        "Explizites Modell schlägt proxy_state",
			"category":    "decision",
			"project":     "yesmem",
			"_session_id": "ses-xyz",
			"model":       "deepseek-v4-pro",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := result["model_used"]; got != "deepseek-v4-pro" {
		t.Fatalf("response model_used: got %v, want %q (explicit param must win)", got, "deepseek-v4-pro")
	}
}

// TestHandleRememberClientModelOverridesProxyState verifies that the MCP-injected
// `_client_model` (set by currentClientModel from env vars) wins over proxy_state.
func TestHandleRememberClientModelOverridesProxyState(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.SetProxyState("session_model:ses-env", "claude-opus-4-7"); err != nil {
		t.Fatalf("SetProxyState: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":          "_client_model schlägt proxy_state",
			"category":      "decision",
			"project":       "yesmem",
			"_session_id":   "ses-env",
			"_client_model": "glm-5.2",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := result["model_used"]; got != "glm-5.2" {
		t.Fatalf("response model_used: got %v, want %q (_client_model must win over proxy_state)", got, "glm-5.2")
	}
}

func TestHandleRememberAcceptsAnticipatedQueries(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":                "Reddit gibt 403 bei WebFetch",
			"category":           "gotcha",
			"project":            "yesmem",
			"anticipated_queries": []any{"reddit 403", "webfetch blocked", "curl workaround reddit"},
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	id := int64(result["id"].(float64))

	learning, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	s.LoadJunctionData(learning)

	if len(learning.AnticipatedQueries) != 3 {
		t.Fatalf("anticipated_queries: got %d, want 3", len(learning.AnticipatedQueries))
	}
	if learning.AnticipatedQueries[0] != "reddit 403" {
		t.Fatalf("anticipated_queries[0]: got %q, want %q", learning.AnticipatedQueries[0], "reddit 403")
	}
}

// --- handleRemember: additional edge cases ---

func TestHandleRememberInvalidCategoryFallsBackToExplicitTeaching(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Category validation test",
			"category": "totally_bogus",
			"project":  "yesmem",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["category"] != "explicit_teaching" {
		t.Fatalf("category: got %v, want %q", result["category"], "explicit_teaching")
	}

	id := int64(result["id"].(float64))
	learning, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if learning.Category != "explicit_teaching" {
		t.Fatalf("stored category: got %q, want %q", learning.Category, "explicit_teaching")
	}
}

func TestHandleRememberDeduplicatesExactContent(t *testing.T) {
	h, _ := mustHandler(t)

	params := map[string]any{
		"text":     "Exact duplicate content for dedup test",
		"category": "pattern",
		"project":  "yesmem",
	}

	resp1 := h.Handle(Request{Method: "remember", Params: params})
	if resp1.Error != "" {
		t.Fatalf("first insert error: %s", resp1.Error)
	}
	var r1 map[string]any
	json.Unmarshal(resp1.Result, &r1)
	originalID := r1["id"].(float64)

	resp2 := h.Handle(Request{Method: "remember", Params: params})
	if resp2.Error != "" {
		t.Fatalf("second insert error: %s", resp2.Error)
	}
	var r2 map[string]any
	json.Unmarshal(resp2.Result, &r2)

	if r2["deduplicated"] != true {
		t.Fatalf("expected deduplicated=true, got %v", r2["deduplicated"])
	}
	if r2["id"].(float64) != originalID {
		t.Fatalf("dedup should return original id %v, got %v", originalID, r2["id"])
	}
}

func TestHandleRememberDoesNotDedupSubsumedShortLearning(t *testing.T) {
	h, s := mustHandler(t)

	// Short learning whose tokens are fully contained in the longer text below.
	// Regression for the containment-metric false positive: a long new text
	// subsumes a short, topically-overlapping old learning's vocabulary and
	// would be wrongly soft-deduplicated (match #85112, Aug 2026).
	shortParams := map[string]any{
		"text":     "daemon make deploy binary version proof signal killed",
		"category": "gotcha",
		"project":  "yesmem",
	}
	shortResp := h.Handle(Request{Method: "remember", Params: shortParams})
	if shortResp.Error != "" {
		t.Fatalf("short insert error: %s", shortResp.Error)
	}
	var shortResult map[string]any
	json.Unmarshal(shortResp.Result, &shortResult)
	shortID := shortResult["id"].(float64)

	longText := "Deploy state only verifiable through the binary version string: make deploy can exit cleanly even when the deployed binary predates the target commit, and the daemon process then still runs the old code so the timeout symptom such as signal killed remains live. The correct proof that a fix is live is checking yesmem version output, confirming the daemon and proxy restarted, and verifying the proc exe inode is not stale."
	longResp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":     longText,
		"category": "gotcha",
		"project":  "yesmem",
	}})
	if longResp.Error != "" {
		t.Fatalf("long insert error: %s", longResp.Error)
	}
	var longResult map[string]any
	json.Unmarshal(longResp.Result, &longResult)

	if longResult["deduplicated"] == true {
		t.Fatalf("long text wrongly deduplicated against shorter learning #%v", shortID)
	}
	longID, ok := longResult["id"].(float64)
	if !ok || longID == shortID {
		t.Fatalf("expected a new, distinct learning id, got %v (short was %v)", longResult["id"], shortID)
	}
	learning, err := s.GetLearning(int64(longID))
	if err != nil {
		t.Fatalf("get long learning: %v", err)
	}
	if learning.Content != longText {
		t.Fatalf("long learning content not stored verbatim:\ngot  %q\nwant %q", learning.Content, longText)
	}
}

func TestHandleRememberStoresEntitiesAndActions(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Nginx config needs reload after SSL cert change",
			"category": "gotcha",
			"project":  "yesmem",
			"entities": []any{"nginx.conf", "ssl-cert"},
			"actions":  []any{"nginx reload", "certbot renew"},
			"trigger":  "when editing nginx SSL",
			"context":  "production server",
			"domain":   "code",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	id := int64(result["id"].(float64))

	learning, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	s.LoadJunctionData(learning)

	if len(learning.Entities) != 2 {
		t.Fatalf("entities: got %d, want 2", len(learning.Entities))
	}
	if len(learning.Actions) != 2 {
		t.Fatalf("actions: got %d, want 2", len(learning.Actions))
	}
	if learning.TriggerRule != "when editing nginx SSL" {
		t.Fatalf("trigger: got %q, want %q", learning.TriggerRule, "when editing nginx SSL")
	}
	if learning.Context != "production server" {
		t.Fatalf("context: got %q, want %q", learning.Context, "production server")
	}
	if learning.Domain != "code" {
		t.Fatalf("domain: got %q, want %q", learning.Domain, "code")
	}
}

func TestHandleRememberSupersedes(t *testing.T) {
	h, s := mustHandler(t)

	resp1 := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Old approach: use polling for updates",
			"category": "decision",
			"project":  "yesmem",
		},
	})
	if resp1.Error != "" {
		t.Fatalf("first insert error: %s", resp1.Error)
	}
	var r1 map[string]any
	json.Unmarshal(resp1.Result, &r1)
	oldID := r1["id"].(float64)

	resp2 := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "New approach: use websockets for updates",
			"category":   "decision",
			"project":    "yesmem",
			"supersedes": oldID,
		},
	})
	if resp2.Error != "" {
		t.Fatalf("supersede error: %s", resp2.Error)
	}
	var r2 map[string]any
	json.Unmarshal(resp2.Result, &r2)

	if r2["supersedes_id"] == nil {
		t.Fatal("expected supersedes_id in response")
	}
	if r2["supersedes_id"].(float64) != oldID {
		t.Fatalf("supersedes_id: got %v, want %v", r2["supersedes_id"], oldID)
	}

	oldLearning, err := s.GetLearning(int64(oldID))
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if oldLearning.SupersededBy == nil {
		t.Fatal("old learning should be superseded")
	}
}

// --- handleGetLearnings ---

func TestHandleGetLearningsEmpty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "get_learnings",
		Params: map[string]any{"category": "gotcha", "project": "nonexistent"},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	if err := json.Unmarshal(resp.Result, &learnings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(learnings) != 0 {
		t.Fatalf("expected 0 learnings, got %d", len(learnings))
	}
}

func TestHandleGetLearningsByID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Specific learning for ID retrieval",
			"category": "pattern",
			"project":  "yesmem",
		},
	})
	var r map[string]any
	json.Unmarshal(resp.Result, &r)
	id := r["id"].(float64)

	resp = h.Handle(Request{
		Method: "get_learnings",
		Params: map[string]any{"id": id},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	json.Unmarshal(resp.Result, &learnings)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d", len(learnings))
	}
	if learnings[0].ID != int64(id) {
		t.Fatalf("id: got %d, want %d", learnings[0].ID, int64(id))
	}
	if learnings[0].Content != "Specific learning for ID retrieval" {
		t.Fatalf("content mismatch: got %q", learnings[0].Content)
	}
}

func TestHandleGetLearningsByCategory(t *testing.T) {
	h, _ := mustHandler(t)

	for _, cat := range []string{"gotcha", "decision", "gotcha"} {
		h.Handle(Request{
			Method: "remember",
			Params: map[string]any{
				"text":     "Learning for " + cat + " category test unique " + cat,
				"category": cat,
				"project":  "testproj",
			},
		})
	}

	resp := h.Handle(Request{
		Method: "get_learnings",
		Params: map[string]any{"category": "gotcha", "project": "testproj"},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	json.Unmarshal(resp.Result, &learnings)
	for _, l := range learnings {
		if l.Category != "gotcha" {
			t.Fatalf("expected category gotcha, got %q", l.Category)
		}
	}
}

func TestHandleGetLearningsRespectsLimit(t *testing.T) {
	h, _ := mustHandler(t)

	for i := 0; i < 5; i++ {
		h.Handle(Request{
			Method: "remember",
			Params: map[string]any{
				"text":     "Limit test learning " + string(rune('A'+i)) + " unique content here xyz",
				"category": "pattern",
				"project":  "limitproj",
			},
		})
	}

	resp := h.Handle(Request{
		Method: "get_learnings",
		Params: map[string]any{"category": "pattern", "project": "limitproj", "limit": float64(2)},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	json.Unmarshal(resp.Result, &learnings)
	if len(learnings) > 2 {
		t.Fatalf("expected at most 2 learnings, got %d", len(learnings))
	}
}

func TestHandleGetLearningsByIDNotFound(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "get_learnings",
		Params: map[string]any{"id": float64(99999)},
	})

	if resp.Error == "" {
		t.Fatal("expected error for nonexistent ID")
	}
}

// --- handleRelate ---

func TestHandleRelateHappyPath(t *testing.T) {
	h, _ := mustHandler(t)

	resp1 := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{"text": "Learning A for relation test", "category": "pattern", "project": "yesmem"},
	})
	resp2 := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{"text": "Learning B for relation test", "category": "pattern", "project": "yesmem"},
	})
	var r1, r2 map[string]any
	json.Unmarshal(resp1.Result, &r1)
	json.Unmarshal(resp2.Result, &r2)
	idA := r1["id"].(float64)
	idB := r2["id"].(float64)

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": idA,
			"learning_id_b": idB,
			"relation_type": "supports",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["status"] != "ok" {
		t.Fatalf("status: got %v, want %q", result["status"], "ok")
	}
	if result["relation_type"] != "supports" {
		t.Fatalf("relation_type: got %v, want %q", result["relation_type"], "supports")
	}
}

func TestHandleRelateMissingIDs(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{"relation_type": "supports"},
	})

	if resp.Error == "" {
		t.Fatal("expected error for missing IDs")
	}
}

func TestHandleRelateInvalidRelationType(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(1),
			"learning_id_b": float64(2),
			"relation_type": "invalid_type",
		},
	})

	if resp.Error == "" {
		t.Fatal("expected error for invalid relation_type")
	}
	if resp.Error != "relation_type must be one of: supports, contradicts, depends_on, relates_to" {
		t.Fatalf("unexpected error message: %s", resp.Error)
	}
}

func TestHandleRelateAllValidTypes(t *testing.T) {
	h, _ := mustHandler(t)

	validTypes := []string{"supports", "contradicts", "depends_on", "relates_to"}
	for _, relType := range validTypes {
		resp1 := h.Handle(Request{
			Method: "remember",
			Params: map[string]any{"text": "Relation type test A " + relType, "category": "pattern", "project": "yesmem"},
		})
		resp2 := h.Handle(Request{
			Method: "remember",
			Params: map[string]any{"text": "Relation type test B " + relType, "category": "pattern", "project": "yesmem"},
		})
		var r1, r2 map[string]any
		json.Unmarshal(resp1.Result, &r1)
		json.Unmarshal(resp2.Result, &r2)

		resp := h.Handle(Request{
			Method: "relate_learnings",
			Params: map[string]any{
				"learning_id_a": r1["id"].(float64),
				"learning_id_b": r2["id"].(float64),
				"relation_type": relType,
			},
		})

		if resp.Error != "" {
			t.Fatalf("relation_type %q: unexpected error: %s", relType, resp.Error)
		}
	}
}

// --- handleQueryFacts ---

func TestHandleQueryFactsNoFiltersError(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "query_facts",
		Params: map[string]any{"project": "yesmem"},
	})

	if resp.Error == "" {
		t.Fatal("expected error when no filters provided")
	}
	if resp.Error != "at least one filter required: entity, action, keyword, domain, or category" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleQueryFactsByEntity(t *testing.T) {
	h, _ := mustHandler(t)

	h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "proxy.go handles HTTP interception for context management",
			"category": "pattern",
			"project":  "yesmem",
			"entities": []any{"proxy.go"},
		},
	})

	resp := h.Handle(Request{
		Method: "query_facts",
		Params: map[string]any{"entity": "proxy.go", "project": "yesmem"},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	json.Unmarshal(resp.Result, &learnings)
	if len(learnings) == 0 {
		t.Fatal("expected at least 1 learning matching entity proxy.go")
	}
}

func TestHandleQueryFactsByCategory(t *testing.T) {
	h, _ := mustHandler(t)

	h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":     "Always check error returns in Go code",
			"category": "gotcha",
			"project":  "yesmem",
		},
	})

	resp := h.Handle(Request{
		Method: "query_facts",
		Params: map[string]any{"category": "gotcha"},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var learnings []models.Learning
	json.Unmarshal(resp.Result, &learnings)
	if len(learnings) == 0 {
		t.Fatal("expected at least 1 gotcha learning")
	}
}

// --- handleQuarantineSession ---

func TestHandleQuarantineSessionMissingID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "quarantine_session",
		Params: map[string]any{},
	})

	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
	}
	if resp.Error != "session_id (string) required" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleQuarantineSessionHappyPath(t *testing.T) {
	h, s := mustHandler(t)

	sid := "test-session-quarantine-001"
	_, err := s.InsertLearning(&models.Learning{
		Category:  "pattern",
		Content:   "Learning from a broken session",
		Project:   "yesmem",
		SessionID: sid,
	})
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "quarantine_session",
		Params: map[string]any{"session_id": sid},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["session_id"] != sid {
		t.Fatalf("session_id: got %v, want %q", result["session_id"], sid)
	}
	quarantined := result["quarantined"].(float64)
	if quarantined != 1 {
		t.Fatalf("quarantined count: got %v, want 1", quarantined)
	}
}

func TestHandleQuarantineSessionNoLearnings(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "quarantine_session",
		Params: map[string]any{"session_id": "nonexistent-session-xyz"},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	quarantined := result["quarantined"].(float64)
	if quarantined != 0 {
		t.Fatalf("quarantined: got %v, want 0", quarantined)
	}
}

// --- handleSkipIndexing ---

func TestHandleSkipIndexingMissingSessionID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "skip_indexing",
		Params: map[string]any{},
	})

	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
	}
}

// --- handleIncrementNoise ---

func TestHandleIncrementNoiseMissingIDs(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleIncrementNoise(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing ids")
	}
}

func TestHandleIncrementNoiseSkipsBehavioralCategories(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "gotcha",
		Content:  "Never push to main without review",
		Project:  "yesmem",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	resp := h.handleIncrementNoise(map[string]any{
		"ids": []any{float64(id)},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["bumped"].(float64) != 0 {
		t.Fatalf("bumped: got %v, want 0 (gotcha is behavioral)", result["bumped"])
	}
	if result["skipped_behavioral"].(float64) != 1 {
		t.Fatalf("skipped_behavioral: got %v, want 1", result["skipped_behavioral"])
	}
}

func TestHandleIncrementNoiseBumpsActiveRefCategories(t *testing.T) {
	h, s := mustHandler(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "pattern",
		Content:  "Use structured logging everywhere",
		Project:  "yesmem",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	resp := h.handleIncrementNoise(map[string]any{
		"ids": []any{float64(id)},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["bumped"].(float64) != 1 {
		t.Fatalf("bumped: got %v, want 1", result["bumped"])
	}
}

// --- handleFlagContradiction ---

func TestHandleFlagContradictionMissingDescription(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleFlagContradiction(map[string]any{"project": "yesmem"})
	if resp.Error == "" {
		t.Fatal("expected error for missing description")
	}
}

func TestHandleFlagContradictionHappyPath(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleFlagContradiction(map[string]any{
		"description":  "Learning 1 says X, learning 2 says Y",
		"project":      "yesmem",
		"learning_ids": []any{float64(1), float64(2)},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["status"] != "ok" {
		t.Fatalf("status: got %v, want %q", result["status"], "ok")
	}
}

// gitTmpRoot builds a fake main repo + containing worktree so repo.Root(cwd)
// is deterministic in tests: cwd points at the worktree, Root resolves to main.
func gitTmpRoot(t *testing.T) (main, worktree string) {
	t.Helper()
	main = t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree = filepath.Join(main, ".worktrees", "wt")
	gitDir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return main, worktree
}

const (
	attrOpenencode = "/home/test/projects/opencode"
	attrYesmem     = "/home/test/memory/yesmem"
	attrDeepseek   = "/home/test/projects/deepseek"
)

func TestAttributeLearningProjectShortNameSession(t *testing.T) {
	h, _ := mustHandler(t)

	// A short project name (not a filesystem path) must pass through
	// uncoalesced — repo.Root on a bare name resolves to whatever repo
	// hosts the running process, not to the named project.
	proj, source := h.attributeLearningProject("Cache TTL matters", nil, "", "yesmem", "")
	if proj != "yesmem" || source != "session" {
		t.Errorf("short-name session project = (%q, %q), want (yesmem, session)", proj, source)
	}
}

func TestAttributeLearningProject(t *testing.T) {
	h, s := mustHandler(t)
	for _, p := range []string{attrOpenencode, attrYesmem, attrDeepseek} {
		if _, err := s.InsertLearning(&models.Learning{
			Category: "gotcha", Content: "seed " + p, Project: p,
			Source: "test", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	subjectMain, subjectWorktree := gitTmpRoot(t)

	dup := func(p string) string { return "touch " + p + "/a.go and " + p + "/b.go" }

	cases := []struct {
		name        string
		text        string
		explicit    string
		session     string
		cwd         string
		wantProject string
		wantSource  string
	}{
		{"explicit wins regardless of content", dup(attrYesmem), attrYesmem, "", subjectWorktree, attrYesmem, "explicit"},
		{"content reassign ≥2 foreign, 0 session", dup(attrYesmem), "", "", subjectWorktree, attrYesmem, "content"},
		{"ambiguous: two foreign ≥2", dup(attrYesmem) + " plus " + dup(attrDeepseek), "", "", subjectWorktree, subjectMain, "ambiguous"},
		{"ambiguous: foreign ≥2 and session present", dup(attrYesmem) + " plus " + dup(subjectMain), "", "", subjectWorktree, subjectMain, "ambiguous"},
		{"repo default without content signal", "some generic note without paths", "", "", subjectWorktree, subjectMain, "repo"},
		{"single foreign mention stays at repo default", "one mention of " + attrYesmem + "/a.go", "", "", subjectWorktree, subjectMain, "repo"},
		{"fork content reassign", dup(attrYesmem), "", attrOpenencode, "", attrYesmem, "content"},
		{"fork session default without signal", "some generic note without paths", "", attrOpenencode, "", attrOpenencode, "session"},
		{"fork ambiguous keeps session project", dup(attrYesmem) + " plus " + dup(attrDeepseek), "", attrOpenencode, "", attrOpenencode, "ambiguous"},
		{"worktree token prefix-matches main repo", dup(subjectMain + "/.worktrees/yesloop-x"), "", "", subjectWorktree, subjectMain, "repo"},
		{"trailing sentence dot still counts", dup(attrYesmem+"/a.go")+". Plus "+attrYesmem+"/b.go.", "", "", subjectWorktree, attrYesmem, "content"},
		{"known worktree session path counts as main repo", dup(subjectMain+"/.worktrees/wt/ent"), "", subjectMain + "/.worktrees/wt", subjectWorktree, subjectMain, "session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotProject, gotSource := h.attributeLearningProject(tc.text, nil, tc.explicit, tc.session, tc.cwd)
			if gotProject != tc.wantProject {
				t.Errorf("project = %q, want %q", gotProject, tc.wantProject)
			}
			if gotSource != tc.wantSource {
				t.Errorf("source = %q, want %q", gotSource, tc.wantSource)
			}
		})
	}
}

func TestAttributeLearningProjectEntitiesCounted(t *testing.T) {
	h, s := mustHandler(t)
	for _, p := range []string{attrYesmem} {
		if _, err := s.InsertLearning(&models.Learning{
			Category: "gotcha", Content: "seed " + p, Project: p,
			Source: "test", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, worktree := gitTmpRoot(t)

	entities := []string{attrYesmem + "/internal/daemon", attrYesmem + "/internal/proxy"}
	got, src := h.attributeLearningProject("fix these files", entities, "", "", worktree)
	if got != attrYesmem || src != "content" {
		t.Errorf("got (%q, %q), want (%q, content)", got, src, attrYesmem)
	}
}

func TestHandleRememberAttributesProjectByContent(t *testing.T) {
	h, s := mustHandler(t)
	_, worktree := gitTmpRoot(t)
	if _, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "seed " + attrYesmem, Project: attrYesmem,
		Source: "test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":     "check " + attrYesmem + "/internal/daemon and " + attrYesmem + "/internal/proxy",
		"category": "gotcha",
		"_cwd":     worktree,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	id, ok := resultMap(t, resp)["id"].(float64)
	if !ok {
		t.Fatalf("no id in response: %v", resp.Result)
	}
	got, err := s.GetLearning(int64(id))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != attrYesmem {
		t.Errorf("Project = %q, want %q", got.Project, attrYesmem)
	}
	if got.ProjectSource != "content" {
		t.Errorf("ProjectSource = %q, want content", got.ProjectSource)
	}
	if got.CanonicalProject != models.CanonicalProject(attrYesmem) {
		t.Errorf("CanonicalProject = %q", got.CanonicalProject)
	}
}

func TestHandleRememberExplicitProjectSource(t *testing.T) {
	h, s := mustHandler(t)
	_, worktree := gitTmpRoot(t)

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":     "some note without path signals",
		"category": "gotcha",
		"project":  attrYesmem,
		"_cwd":     worktree,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	id := resultMap(t, resp)["id"].(float64)
	got, err := s.GetLearning(int64(id))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != attrYesmem {
		t.Errorf("Project = %q, want %q", got.Project, attrYesmem)
	}
	if got.ProjectSource != "explicit" {
		t.Errorf("ProjectSource = %q, want explicit", got.ProjectSource)
	}
	if got.CanonicalProject != models.CanonicalProject(attrYesmem) {
		t.Errorf("CanonicalProject = %q", got.CanonicalProject)
	}
}
