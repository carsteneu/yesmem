package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
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
			"category":            "gotcha",
			"project":             "yesmem",
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

// TestHandleRememberHighTrustSupersedeRecordsContradiction (YM-43 / Decision D6)
// verifies the confirmation path: superseding a high-trust learning must not
// park a status nobody reads. The old learning stays active and the conflict is
// recorded as a contradicts edge instead.
func TestHandleRememberHighTrustSupersedeRecordsContradiction(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "Always run the full baseline before merging",
		Project: "yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "user_stated", Importance: 5,
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}
	// InsertLearning never persists use_count; pin it so the learning is TrustHigh.
	if _, err := s.DB().Exec("UPDATE learnings SET use_count = 2 WHERE id = ?", oldID); err != nil {
		t.Fatalf("set use_count: %v", err)
	}
	old, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if storage.ClassifyTrust(storage.TrustScore(old)) != storage.TrustHigh {
		t.Fatalf("test setup: old learning #%d must be TrustHigh", oldID)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "Run the baseline suite before any merge to main",
			"category":   "decision",
			"project":    "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if resp.Error != "" {
		t.Fatalf("remember error: %s", resp.Error)
	}
	var r map[string]any
	json.Unmarshal(resp.Result, &r)
	newID := int64(r["id"].(float64))

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning after: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("high-trust learning #%d must stay active, got superseded_by=%d", oldID, *after.SupersededBy)
	}
	if after.SupersedeStatus == "pending_confirmation" {
		t.Fatalf("retired pending_confirmation status must not be written (#%d)", oldID)
	}
	pairs, err := s.GetContradictingPairs([]int64{oldID}, []int64{newID})
	if err != nil {
		t.Fatalf("get contradicting pairs: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatalf("expected a contradicts edge between #%d and #%d", oldID, newID)
	}
}

// insertLos11eHighTrust stores a TrustHigh learning (user_stated plus a pinned
// use_count) — the setup every blocked-supersede path needs.
func insertLos11eHighTrust(t *testing.T, s *storage.Store) int64 {
	t.Helper()
	id, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "BuildSandboxedCommand ist der Weg, einen Befehl einzusperren",
		Project: "yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "user_stated", Importance: 3,
	})
	if err != nil {
		t.Fatalf("insert high trust learning: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET use_count = 2 WHERE id = ?", id); err != nil {
		t.Fatalf("set use_count: %v", err)
	}
	old, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get high trust learning: %v", err)
	}
	if storage.ClassifyTrust(storage.TrustScore(old)) != storage.TrustHigh {
		t.Fatalf("test setup: learning #%d must be TrustHigh", id)
	}
	return id
}

// TestHandleRememberSupersedeConfirmsAcrossReaderPool runs the read back on a
// file backed store, where GetLearning reads through a second connection pool
// (production topology, unlike the shared handle of ":memory:").
func TestHandleRememberSupersedeConfirmsAcrossReaderPool(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "yesmem.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandler(s, nil)
	h.dataDir = t.TempDir()
	h.disableAgentProcesses = true

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Datei-Store: Sandbox laeuft ueber WrapCommand",
		Project: "yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "Datei-Store: Sandbox laeuft ueber Run",
			"category":   "gotcha",
			"project":    "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if resp.Error != "" {
		t.Fatalf("remember error: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "replaced" {
		t.Fatalf("supersede_outcome: got %q, want \"replaced\": the read back must see the commit (response: %s)", got, resp.Result)
	}
}

// TestHandleRememberHighTrustSupersedeReportsOutcome (YM-278) fixes the
// misreport: the blocked case must be visible as a field, not only as prose
// inside "message" that the MCP formatter drops.
func TestHandleRememberHighTrustSupersedeReportsOutcome(t *testing.T) {
	h, s := mustHandler(t)
	oldID := insertLos11eHighTrust(t, s)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "internal/daemon/sandbox.go hat kein BuildSandboxedCommand mehr",
			"category":   "gotcha",
			"project":    "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if resp.Error != "" {
		t.Fatalf("remember error: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "blocked" {
		t.Fatalf("supersede_outcome: got %q, want \"blocked\" (response: %s)", got, resp.Result)
	}
	if trust, ok := r["supersede_trust"].(float64); !ok || trust < 3.0 {
		t.Fatalf("blocked supersede must report the trust that blocked it, got %v (response: %s)", r["supersede_trust"], resp.Result)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("high-trust learning #%d must stay active, got superseded_by=%d", oldID, *after.SupersededBy)
	}
}

// TestHandleRememberSupersedeReportsReplaced pins the other outcome of the same
// field: a low-trust supersede really replaces, and says so.
func TestHandleRememberSupersedeReportsReplaced(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Alte Fassung: Sandbox laeuft ueber WrapCommand",
		Project: "yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "Neue Fassung: Sandbox laeuft ueber Run(command, timeoutSec)",
			"category":   "gotcha",
			"project":    "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if resp.Error != "" {
		t.Fatalf("remember error: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "replaced" {
		t.Fatalf("supersede_outcome: got %q, want \"replaced\" (response: %s)", got, resp.Result)
	}
	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy == nil {
		t.Fatalf("low-trust learning #%d should be superseded", oldID)
	}
}

// TestHandleRememberSupersedeMissingTargetReportsNotFound: a target that cannot
// be loaded must not be reported as replaced (same bug class as the misreported
// blocked supersede).
func TestHandleRememberSupersedeMissingTargetReportsNotFound(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "Sandbox bietet Run und RunWithEnv",
			"category":   "gotcha",
			"project":    "yesmem",
			"supersedes": float64(999999),
		},
	})
	if resp.Error != "" {
		t.Fatalf("remember error: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "not_found" {
		t.Fatalf("supersede_outcome: got %q, want \"not_found\" (response: %s)", got, resp.Result)
	}
	if msg, _ := r["message"].(string); strings.Contains(msg, "Supersedes #999999.") {
		t.Fatalf("message must not claim a replacement: %s", msg)
	}
}

// TestHandleRememberForceSupersedeAcceptsCanonicalProjectMatch: the target may
// store the path while the caller passes the short name. Both name the same
// project, so the guard must compare canonical forms.
func TestHandleRememberForceSupersedeAcceptsCanonicalProjectMatch(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Pfadform: BuildSandboxedCommand gibt es",
		Project: "/home/tester/memory/yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "user_stated", Importance: 3,
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET use_count = 2 WHERE id = ?", oldID); err != nil {
		t.Fatalf("set use_count: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "Kurzname: Sandbox bietet Run",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": "seit YM-224 entfernt",
		},
	})
	if resp.Error != "" {
		t.Fatalf("canonical project match must not be refused: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "forced" {
		t.Fatalf("supersede_outcome: got %q, want \"forced\" (response: %s)", got, resp.Result)
	}
}

// TestHandleRememberForceSupersedeFailsClosedOnMissingProject: without a
// project on the target side the guard cannot prove ownership, so it refuses.
func TestHandleRememberForceSupersedeFailsClosedOnMissingProject(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Global: BuildSandboxedCommand gibt es",
		Project: "", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "user_stated", Importance: 3,
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "yesmem: Sandbox bietet Run",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": "veraltet",
		},
	})
	if resp.Error == "" {
		t.Fatalf("force_supersede without a target project must be refused, got result: %s", resp.Result)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("learning #%d must stay active", oldID)
	}
}

// TestHandleRememberForceSupersedeRejectedSavesNothing closes the gap between
// the promise in the rejection message and the store state.
func TestHandleRememberForceSupersedeRejectedSavesNothing(t *testing.T) {
	h, s := mustHandler(t)
	oldID := insertLos11eHighTrust(t, s)

	var before int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM learnings").Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "Sandbox hat nur noch Run",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": "   ",
		},
	})
	if resp.Error == "" {
		t.Fatalf("whitespace-only supersede_reason must be rejected, got result: %s", resp.Result)
	}

	var after int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM learnings").Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("rejected force must not insert: before=%d after=%d", before, after)
	}
}

// TestHandleRememberForceSupersedeRefusesForeignProject: the override retires
// knowledge of one project; a foreign project's learning is a wrong-project
// typo far more often than intent.
func TestHandleRememberForceSupersedeRefusesForeignProject(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Fremdes Projekt: BuildSandboxedCommand gibt es",
		Project: "/other/project", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "user_stated", Importance: 3,
	})
	if err != nil {
		t.Fatalf("insert foreign learning: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET use_count = 2 WHERE id = ?", oldID); err != nil {
		t.Fatalf("set use_count: %v", err)
	}

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "yesmem: Sandbox bietet Run",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": "veraltet",
		},
	})
	if resp.Error == "" {
		t.Fatalf("force_supersede across projects must be refused, got result: %s", resp.Result)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get foreign learning: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("foreign learning #%d must stay active", oldID)
	}
}

// TestHandleRememberSupersedeAlreadySupersededReportsFailed: the store's UPDATE
// matches only an active row and reports no error when it matches none, so the
// outcome must come from a read back, not from a nil error.
func TestHandleRememberSupersedeAlreadySupersededReportsFailed(t *testing.T) {
	h, s := mustHandler(t)

	oldID, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Alte Fassung: WrapCommand",
		Project: "yesmem", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "llm_extracted",
	})
	if err != nil {
		t.Fatalf("insert old learning: %v", err)
	}

	first := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":       "Erste Korrektur: Run statt WrapCommand",
			"category":   "gotcha",
			"project":    "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if first.Error != "" {
		t.Fatalf("first supersede error: %s", first.Error)
	}
	var r1 map[string]any
	json.Unmarshal(first.Result, &r1)
	winnerID := int64(r1["id"].(float64))

	second := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "Zweite Korrektur: Run(command, timeoutSec)",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": "nochmal praeziser",
		},
	})
	if second.Error != "" {
		t.Fatalf("second supersede error: %s", second.Error)
	}
	var r2 map[string]any
	if err := json.Unmarshal(second.Result, &r2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r2["supersede_outcome"].(string); got != "failed" {
		t.Fatalf("supersede_outcome: got %q, want \"failed\" (response: %s)", got, second.Result)
	}
	if msg, _ := r2["supersede_error"].(string); msg == "" {
		t.Fatalf("failed supersede must carry the reason in supersede_error: %s", second.Result)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy == nil || *after.SupersededBy != winnerID {
		t.Fatalf("the chain must still point at #%d, got %v", winnerID, after.SupersededBy)
	}
}

// TestHandleRememberSupersedeDuplicateSkipsAndSaysSo: the hash dedup returns
// before the supersede is applied. Saying nothing would repeat the original bug
// one level higher.
func TestHandleRememberSupersedeDuplicateSkipsAndSaysSo(t *testing.T) {
	h, s := mustHandler(t)
	oldID := insertLos11eHighTrust(t, s)

	const text = "Derselbe Wortlaut, zweimal gespeichert"
	first := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text": text, "category": "gotcha", "project": "yesmem",
		},
	})
	if first.Error != "" {
		t.Fatalf("first remember error: %s", first.Error)
	}
	var r1 map[string]any
	json.Unmarshal(first.Result, &r1)
	if dedup, _ := r1["deduplicated"].(bool); dedup {
		t.Fatalf("first remember must insert, got: %s", first.Result)
	}

	second := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text": text, "category": "gotcha", "project": "yesmem",
			"supersedes": float64(oldID),
		},
	})
	if second.Error != "" {
		t.Fatalf("second remember error: %s", second.Error)
	}
	var r2 map[string]any
	if err := json.Unmarshal(second.Result, &r2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r2["supersede_outcome"].(string); got != "duplicate" {
		t.Fatalf("supersede_outcome: got %q, want \"duplicate\" (response: %s)", got, second.Result)
	}
	if msg, _ := r2["message"].(string); !strings.Contains(msg, "NOT performed") {
		t.Fatalf("duplicate response must say the supersede was skipped, got: %s", msg)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("skipped supersede must leave #%d active", oldID)
	}
}

// TestHandleRememberSupersedeRejectsNonIntegerID: 0.5 truncates to 0 and used to
// vanish into the not_found branch without a response line.
func TestHandleRememberSupersedeRejectsNonIntegerID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text": "Sandbox bietet Run", "category": "gotcha", "project": "yesmem",
			"supersedes": 1.5,
		},
	})
	if resp.Error == "" {
		t.Fatalf("non-integer supersedes must be rejected, got result: %s", resp.Result)
	}
}

// TestHandleRememberForceSupersedeRequiresReason (YM-278) keeps the override
// from becoming a silent production lever against the trust gate.
func TestHandleRememberForceSupersedeRequiresReason(t *testing.T) {
	h, s := mustHandler(t)
	oldID := insertLos11eHighTrust(t, s)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":            "Sandbox hat nur noch Run, kein BuildSandboxedCommand",
			"category":        "gotcha",
			"project":         "yesmem",
			"supersedes":      float64(oldID),
			"force_supersede": true,
		},
	})
	if resp.Error == "" {
		t.Fatalf("force_supersede without supersede_reason must be rejected, got result: %s", resp.Result)
	}

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy != nil {
		t.Fatalf("rejected force must leave #%d active, got superseded_by=%d", oldID, *after.SupersededBy)
	}
}

// TestHandleRememberForceSupersedeRequiresTarget: the override is meaningless
// without a learning to retire.
func TestHandleRememberForceSupersedeRequiresTarget(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "Sandbox hat nur noch Run",
			"category":         "gotcha",
			"project":          "yesmem",
			"force_supersede":  true,
			"supersede_reason": "veraltet",
		},
	})
	if resp.Error == "" {
		t.Fatalf("force_supersede without supersedes must be rejected, got result: %s", resp.Result)
	}
}

// TestHandleRememberForceSupersedeReplacesHighTrust (YM-278) is the documented
// way out: an explicit, justified override retires a high-trust learning and
// records the reason in the supersede chain.
func TestHandleRememberForceSupersedeReplacesHighTrust(t *testing.T) {
	h, s := mustHandler(t)
	oldID := insertLos11eHighTrust(t, s)
	reason := "seit YM-224 entfernt: BuildSandboxedCommand existiert nicht mehr"

	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{
			"text":             "internal/daemon/sandbox.go bietet Run, RunWithEnv, ... aber kein BuildSandboxedCommand",
			"category":         "gotcha",
			"project":          "yesmem",
			"supersedes":       float64(oldID),
			"force_supersede":  true,
			"supersede_reason": reason,
		},
	})
	if resp.Error != "" {
		t.Fatalf("forced supersede error: %s", resp.Error)
	}
	var r map[string]any
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := r["supersede_outcome"].(string); got != "forced" {
		t.Fatalf("supersede_outcome: got %q, want \"forced\" (response: %s)", got, resp.Result)
	}
	newID := int64(r["id"].(float64))

	after, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("get old learning: %v", err)
	}
	if after.SupersededBy == nil {
		t.Fatalf("forced supersede must retire #%d", oldID)
	}
	if *after.SupersededBy != newID {
		t.Fatalf("superseded_by: got %d, want %d", *after.SupersededBy, newID)
	}
	if !strings.Contains(after.SupersedeReason, reason) {
		t.Fatalf("supersede_reason must carry the justification, got %q", after.SupersedeReason)
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
		Params: map[string]any{"text": "engine reads the allowlist before the argument rules", "category": "pattern", "project": "yesmem"},
	})
	resp2 := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{"text": "sandbox masks the home directory", "category": "pattern", "project": "yesmem"},
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
			Params: map[string]any{"text": "retry budget for the " + relType + " edges", "category": "pattern", "project": "yesmem"},
		})
		resp2 := h.Handle(Request{
			Method: "remember",
			Params: map[string]any{"text": "sandbox profile forbids the " + relType + " writes", "category": "pattern", "project": "yesmem"},
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

// --- handleUnquarantineSession (YM-44) ---

func TestHandleUnquarantineSessionMissingID(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "unquarantine_session",
		Params: map[string]any{},
	})

	if resp.Error != "session_id (string) required" {
		t.Fatalf("unexpected error: %q", resp.Error)
	}
}

// A quarantine without a way back is a one-way door: the RPC must lift it again.
func TestHandleUnquarantineSessionLiftsQuarantine(t *testing.T) {
	h, s := mustHandler(t)

	sid := "test-session-unquarantine-001"
	if _, err := s.InsertLearning(&models.Learning{
		Category:  "pattern",
		Content:   "Learning from a session quarantined by mistake",
		SessionID: sid,
	}); err != nil {
		t.Fatalf("insert learning: %v", err)
	}
	if n, err := s.QuarantineSession(sid); err != nil || n != 1 {
		t.Fatalf("quarantine: affected=%d err=%v", n, err)
	}

	resp := h.Handle(Request{
		Method: "unquarantine_session",
		Params: map[string]any{"session_id": sid},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result["session_id"] != sid {
		t.Fatalf("session_id: got %v, want %q", result["session_id"], sid)
	}
	if got := result["unquarantined"].(float64); got != 1 {
		t.Fatalf("unquarantined count: got %v, want 1", got)
	}

	// Second call is a no-op: the rows are already active again.
	resp = h.Handle(Request{
		Method: "unquarantine_session",
		Params: map[string]any{"session_id": sid},
	})
	if resp.Error != "" {
		t.Fatalf("second call error: %s", resp.Error)
	}
	json.Unmarshal(resp.Result, &result)
	if got := result["unquarantined"].(float64); got != 0 {
		t.Fatalf("second call unquarantined: got %v, want 0", got)
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
		{"trailing sentence dot still counts", dup(attrYesmem+"/a.go") + ". Plus " + attrYesmem + "/b.go.", "", "", subjectWorktree, attrYesmem, "content"},
		{"known worktree session path counts as main repo", dup(subjectMain + "/.worktrees/wt/ent"), "", subjectMain + "/.worktrees/wt", subjectWorktree, subjectMain, "session"},
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

// --- handleRemember: emotional intensity ---
//
// The emotional intensity is a session rating: the extraction produces it once
// and stamps it on every learning it creates. Learnings saved later through
// remember() were inserted with the 0.0 default instead (live: every
// user_stated/agreed_upon/claude_suggested learning sits at 0), so they now
// inherit the rating their session already carries.

func TestHandleRemember_InheritsSessionIntensity(t *testing.T) {
	h, s := mustHandler(t)
	const sessionID = "opencode:ses_RATED"
	if _, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "the extraction rated this session",
		Project: attrYesmem, SessionID: sessionID, Source: "llm_extracted",
		Confidence: 1.0, EmotionalIntensity: 0.7, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed sibling learning: %v", err)
	}

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text": "a later remember call in the same session", "category": "decision",
		"project": attrYesmem, "_session_id": sessionID,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EmotionalIntensity != 0.7 {
		t.Errorf("EmotionalIntensity = %v, want the session's 0.7", got.EmotionalIntensity)
	}
}

func TestHandleRemember_UnratedSessionStaysAtZero(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text": "first learning of a session that was never rated", "category": "decision",
		"project": attrYesmem, "_session_id": "opencode:ses_UNRATED",
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EmotionalIntensity != 0 {
		t.Errorf("EmotionalIntensity = %v, want 0 for an unrated session", got.EmotionalIntensity)
	}
}

// The fallbacks name whichever session wrote last, not the caller. Lending their
// rating would push a foreign session's intensity (and its +30% emotionalBoost
// in ComputeScore) onto a hand-written learning.
func TestHandleRemember_SessionFallbackDoesNotLendIntensity(t *testing.T) {
	h, s := mustHandler(t)
	const foreign = "opencode:ses_OTHER_RATED"
	if _, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "a different session carried this rating",
		Project: attrYesmem, SessionID: foreign, Source: "llm_extracted",
		Confidence: 1.0, EmotionalIntensity: 0.9, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed sibling learning: %v", err)
	}
	h.activeSessionMu.Lock()
	h.activeSessionID = foreign
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text": "remember call without a session of its own", "category": "decision",
		"project": attrYesmem,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != foreign {
		t.Errorf("SessionID = %q, want the fallback %q", got.SessionID, foreign)
	}
	if got.EmotionalIntensity != 0 {
		t.Errorf("EmotionalIntensity = %v — borrowed from a session the caller never proved", got.EmotionalIntensity)
	}
}

// --- handleRemember: session attribution ---
//
// The per-call _session_id the MCP layer injects names the session that really
// made the call. activeSessionID is whichever session wrote last — in opencode
// v2 one `opencode serve --service` hosts every session, so the global value
// routinely belongs to a different session than the caller.

func TestHandleRemember_UsesCallerSessionNotActive(t *testing.T) {
	h, s := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "opencode:ses_OTHER_ACTIVE"
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":          "session attribution: the caller's per-call session wins over the global active session",
		"category":      "decision",
		"project":       attrYesmem,
		"_session_id":   "opencode:ses_CALLER",
		"_source_agent": "opencode",
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "opencode:ses_CALLER" {
		t.Errorf("SessionID = %q, want opencode:ses_CALLER", got.SessionID)
	}
}

func TestHandleRemember_FallsBackToActiveSession(t *testing.T) {
	h, s := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "opencode:ses_ACTIVE_ONLY"
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":     "session attribution: without per-call context the active session stays the fallback",
		"category": "decision",
		"project":  attrYesmem,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "opencode:ses_ACTIVE_ONLY" {
		t.Errorf("SessionID = %q, want the active-session fallback opencode:ses_ACTIVE_ONLY", got.SessionID)
	}
}

// --- attributeLearningProject: unresolvable explicit project names ---
//
// resolveProjectParam resolves a short project name through the project DB; when
// it cannot, the name reaches attributeLearningProject verbatim (live:
// "v2-release-notes", "wal", "chief"). Stored as-is it is a project value no
// lookup can ever match, so the caller's working directory identifies the
// repository instead.

func TestHandleRemember_UnresolvableShortProjectFallsBackToCallerRepo(t *testing.T) {
	h, s := mustHandler(t)
	main, worktree := gitTmpRoot(t)

	resp := h.Handle(Request{Method: "remember", Params: map[string]any{
		"text":     "release body note written from the worktree",
		"category": "gotcha",
		"project":  "v2-release-notes",
		"_cwd":     worktree,
	}})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	got, err := s.GetLearning(int64(resultMap(t, resp)["id"].(float64)))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Project != main {
		t.Errorf("Project = %q, want the caller's repository root %q", got.Project, main)
	}
	if got.ProjectSource != "repo" {
		t.Errorf("ProjectSource = %q, want repo", got.ProjectSource)
	}
}

func TestAttributeLearningProjectUnresolvedShortNameWithoutCWD(t *testing.T) {
	h, _ := mustHandler(t)

	proj, source := h.attributeLearningProject("note", nil, "v2-release-notes", "", "")
	if proj != "v2-release-notes" || source != "explicit" {
		t.Errorf("got (%q, %q), want (v2-release-notes, explicit) — no cwd to derive from", proj, source)
	}
}

func TestAttributeLearningProjectAbsoluteExplicitProjectUnchanged(t *testing.T) {
	h, _ := mustHandler(t)
	_, worktree := gitTmpRoot(t)

	proj, source := h.attributeLearningProject("note", nil, attrYesmem, "", worktree)
	if proj != attrYesmem || source != "explicit" {
		t.Errorf("got (%q, %q), want (%q, explicit)", proj, source, attrYesmem)
	}
}
