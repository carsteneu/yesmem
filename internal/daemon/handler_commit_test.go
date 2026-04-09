package daemon

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

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
