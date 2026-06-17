package daemon

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

// mockCommitEvalClient mocks the LLM client for commit evaluation tests.
type mockCommitEvalClient struct {
	response string
	err      error
	called   bool
}

func (m *mockCommitEvalClient) Complete(system, userMsg string, opts ...extraction.CallOption) (string, error) {
	m.called = true
	return m.response, m.err
}

func (m *mockCommitEvalClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...extraction.CallOption) (string, error) {
	m.called = true
	return m.response, m.err
}

func (m *mockCommitEvalClient) Name() string  { return "mock" }
func (m *mockCommitEvalClient) Model() string { return "mock-model" }

func TestEvaluateStaleness_StaleDetected(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "stale", "reason": "function removed"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "proxy.go has a handleRequest function", Category: "gotcha"},
	}

	decisions, err := evaluateStaleness(mock, "diff --git a/proxy.go\n-func handleRequest() {}", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mock.called {
		t.Fatal("expected LLM client to be called")
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].ID != 1 {
		t.Errorf("expected ID 1, got %d", decisions[0].ID)
	}
	if decisions[0].Action != "stale" {
		t.Errorf("expected action 'stale', got %q", decisions[0].Action)
	}
	if decisions[0].Reason != "function removed" {
		t.Errorf("expected reason 'function removed', got %q", decisions[0].Reason)
	}
}

func TestEvaluateStaleness_ValidPreserved(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "valid", "reason": "still accurate"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "config uses YAML format", Category: "pattern"},
	}

	decisions, err := evaluateStaleness(mock, "diff --git a/main.go\n+// comment", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != "valid" {
		t.Errorf("expected action 'valid', got %q", decisions[0].Action)
	}
}

func TestEvaluateStaleness_MixedResults(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "stale", "reason": "renamed"}, {"id": 2, "action": "valid", "reason": "unrelated"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "handler is in server.go", Category: "gotcha"},
		{ID: 2, Content: "tests use table-driven pattern", Category: "pattern"},
	}

	decisions, err := evaluateStaleness(mock, "diff content", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}

	staleCount := 0
	validCount := 0
	for _, d := range decisions {
		switch d.Action {
		case "stale":
			staleCount++
		case "valid":
			validCount++
		}
	}
	if staleCount != 1 {
		t.Errorf("expected 1 stale, got %d", staleCount)
	}
	if validCount != 1 {
		t.Errorf("expected 1 valid, got %d", validCount)
	}
}

func TestEvaluateStaleness_LLMError(t *testing.T) {
	mock := &mockCommitEvalClient{
		err: fmt.Errorf("api timeout"),
	}
	learnings := []models.Learning{
		{ID: 1, Content: "some learning", Category: "gotcha"},
	}

	_, err := evaluateStaleness(mock, "diff content", learnings)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "api timeout" {
		t.Errorf("expected 'api timeout', got %q", err.Error())
	}
}

func TestEvaluateStaleness_MarkdownFences(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: "```json\n[{\"id\": 1, \"action\": \"stale\", \"reason\": \"removed\"}]\n```",
	}
	learnings := []models.Learning{
		{ID: 1, Content: "old function exists", Category: "gotcha"},
	}

	decisions, err := evaluateStaleness(mock, "diff content", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != "stale" {
		t.Errorf("expected 'stale', got %q", decisions[0].Action)
	}
}

func TestHandleInvalidateOnCommit_NoClient(t *testing.T) {
	h, _ := mustHandler(t)
	// CommitEvalClient is nil by default

	resp := h.Handle(Request{
		Method: "invalidate_on_commit",
		Params: map[string]any{
			"hash":    "abc1234",
			"project": "yesmem",
		},
	})

	if resp.Error == "" {
		t.Fatal("expected error for nil CommitEvalClient")
	}
	if resp.Error != "no LLM client for commit evaluation" {
		t.Errorf("unexpected error message: %q", resp.Error)
	}
}

func TestHandleInvalidateOnCommit_MissingHash(t *testing.T) {
	h, _ := mustHandler(t)
	h.CommitEvalClient = &mockCommitEvalClient{}

	resp := h.Handle(Request{
		Method: "invalidate_on_commit",
		Params: map[string]any{
			"project": "yesmem",
		},
	})

	if resp.Error == "" {
		t.Fatal("expected error for missing hash")
	}
	if resp.Error != "hash required" {
		t.Errorf("unexpected error message: %q", resp.Error)
	}
}

func TestHandleInvalidateOnCommit_NoAffectedLearnings(t *testing.T) {
	h, s := mustHandler(t)
	mock := &mockCommitEvalClient{
		response: `[]`,
	}
	h.CommitEvalClient = mock

	// Insert a learning WITHOUT matching entity — won't be found by entity match
	_, err := s.InsertLearning(&models.Learning{
		Content:   "some unrelated knowledge",
		Category:  "gotcha",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// This will fail at gitChangedFiles (no real commit hash) and return affected:0
	resp := h.Handle(Request{
		Method: "invalidate_on_commit",
		Params: map[string]any{
			"hash":    "abc1234567890",
			"project": "yesmem",
		},
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var result map[string]any
	json.Unmarshal(resp.Result, &result)

	affected, _ := result["affected"].(float64)
	if affected != 0 {
		t.Errorf("expected 0 affected, got %v", affected)
	}

	// LLM should NOT be called when no affected learnings
	if mock.called {
		t.Error("LLM client should not be called when no learnings affected")
	}
}

func TestEvaluateStaleness_TruncatesLongContent(t *testing.T) {
	longContent := ""
	for i := 0; i < 50; i++ {
		longContent += "this is a very long learning content that should be truncated "
	}

	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "valid", "reason": "ok"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: longContent, Category: "pattern"},
	}

	decisions, err := evaluateStaleness(mock, "diff content", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
}

func TestEvaluateStaleness_WithConfidenceAndType(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "stale", "reason": "function signature changed", "confidence": 0.95, "type": "code_contradicts"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "Foo(a, b) returns string", Category: "gotcha"},
	}

	decisions, err := evaluateStaleness(mock, "diff --git a/lib.go\n-func Foo(a, b) string {}\n+func Foo(ctx, a, b) error {}", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", decisions[0].Confidence)
	}
	if decisions[0].Type != "code_contradicts" {
		t.Errorf("expected type 'code_contradicts', got %q", decisions[0].Type)
	}
}

func TestEvaluateStaleness_CodeChangedInsightHolds(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "code_changed_insight_holds", "reason": "code refactored but design insight still valid", "confidence": 0.7, "type": "code_changed_insight_holds"}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "proxy uses RWMutex for concurrent access — design decision driven by read-heavy workload", Category: "decision"},
	}

	decisions, err := evaluateStaleness(mock, "diff --git a/proxy.go\n- rw.RLock()\n+ mu.Lock() // refactored to use simpler mutex", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != "code_changed_insight_holds" {
		t.Errorf("expected 'code_changed_insight_holds', got %q", decisions[0].Action)
	}
}

func TestEvaluateStaleness_Uncertain(t *testing.T) {
	mock := &mockCommitEvalClient{
		response: `[{"id": 1, "action": "uncertain", "reason": "cannot determine from diff alone", "confidence": 0.3, "type": ""}]`,
	}
	learnings := []models.Learning{
		{ID: 1, Content: "This learning references library code that may be affected", Category: "pattern"},
	}

	decisions, err := evaluateStaleness(mock, "diff --git a/go.mod\n-require github.com/foo v1.0\n+require github.com/foo v2.0", learnings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	if decisions[0].Action != "uncertain" {
		t.Errorf("expected 'uncertain', got %q", decisions[0].Action)
	}
}

func TestApplyStalenessPenalty(t *testing.T) {
	// Test the helper directly
	results := []embedding.RankedResult{
		{ID: "1", Score: 1.0},
		{ID: "2", Score: 0.8},
		{ID: "3", Score: 0.5},
		{ID: "4", Score: 1.0},
	}
	scores := map[int64]float64{
		1: 0.9,  // stale → 0.5× penalty
		2: 0.6,  // moderately stale → 0.8× penalty (≥0.5)
		4: 0.3,  // low staleness → no penalty (<0.5)
		// 3: not checked → no penalty
	}

	out := applyStalenessPenalty(results, scores)
	if out[0].Score != 0.5 {
		t.Errorf("id=1 staleness=0.9: expected score 0.5, got %f", out[0].Score)
	}
	if out[1].Score < 0.63 || out[1].Score > 0.65 {
		t.Errorf("id=2 staleness=0.6: expected score ~0.64 (0.8× penalty), got %f", out[1].Score)
	}
	if out[2].Score != 0.5 {
		t.Errorf("id=3 unchecked: expected score 0.5 (no penalty), got %f", out[2].Score)
	}
	if out[3].Score != 1.0 {
		t.Errorf("id=4 staleness=0.3: expected score 1.0 (no penalty), got %f", out[3].Score)
	}
}
