package daemon

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

// mockLLM implements extraction.LLMClient for tests.
type mockLLM struct {
	response string
	err      error
}

func (m *mockLLM) Complete(system, user string, opts ...extraction.CallOption) (string, error) {
	return m.response, m.err
}
func (m *mockLLM) CompleteJSON(system, user string, schema map[string]any, opts ...extraction.CallOption) (string, error) {
	return m.response, m.err
}
func (m *mockLLM) Name() string  { return "mock" }
func (m *mockLLM) Model() string { return "mock-model" }

var _ extraction.LLMClient = (*mockLLM)(nil)

// --- Narrative generation (LLM path) ---

func TestGenerateNarrativeForSession_NilClient(t *testing.T) {
	_, s := mustHandler(t)
	sess := models.Session{ID: "sess-nil-narr", ProjectShort: "test", MessageCount: 20}
	// Should not panic with nil client
	generateNarrativeForSession(s, sess, nil)
}

func TestRegenerateNarrativeForSession_NilClient(t *testing.T) {
	_, s := mustHandler(t)
	sess := models.Session{ID: "sess-nil-regen", ProjectShort: "test", MessageCount: 20}
	ok := RegenerateNarrativeForSession(s, sess, nil)
	if ok {
		t.Error("should return false with nil client")
	}
}

func TestRegenerateNarrativeForSession_WithMock(t *testing.T) {
	t.Skip("requires messages.db which is separate from test store")
}

// --- RunRulesRefresh ---

func TestRunRulesRefresh_NilClient(t *testing.T) {
	_, s := mustHandler(t)
	// Should not panic
	RunRulesRefresh(s, nil)
}

// --- ClusterLearnings ---

func TestClusterLearnings_NilVecStore(t *testing.T) {
	_, s := mustHandler(t)
	mock := &mockLLM{response: "test"}
	// Should not panic, should skip
	ClusterLearnings(s, mock, nil)
}

// --- DetectRecurrence ---

func TestDetectRecurrence_Empty(t *testing.T) {
	_, s := mustHandler(t)
	mock := &mockLLM{response: "test"}
	count := DetectRecurrence(s, mock)
	if count != 0 {
		t.Errorf("expected 0 alerts on empty DB, got %d", count)
	}
}

// --- LogExtractionEstimate ---

func TestLogExtractionEstimate(t *testing.T) {
	// Just verify it doesn't panic
	est := CostEstimate{Sessions: 5, TotalChunks: 20, EstCostUSD: 1.50}
	mock := &mockLLM{response: "test"}
	LogExtractionEstimate(est, mock)
}

func TestLogExtractionEstimate_NilClient(t *testing.T) {
	est := CostEstimate{Sessions: 5, TotalChunks: 20}
	LogExtractionEstimate(est, nil)
}
