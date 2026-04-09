
package locomo

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/carsteneu/yesmem/internal/benchmark"
)

// mockLLMFunc implements benchmark.LLMClient with a custom function.
// Only needed in integration tests for dynamic responses (e.g., alternating CORRECT/WRONG).
type mockLLMFunc struct{ fn func(system, user string) (string, error) }

func (m *mockLLMFunc) Complete(s, u string) (string, error) { return m.fn(s, u) }
func (m *mockLLMFunc) Model() string                        { return "mock-func" }

// Compile-time check.
var _ benchmark.LLMClient = (*mockLLMFunc)(nil)

func TestFullPipelineWithMockLLM(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	answerLLM := &mockLLM{response: "Paris"}
	judgeLLM := &mockLLM{response: `{"reasoning": "Match", "label": "CORRECT"}`}
	runner := NewRunner(store, answerLLM, judgeLLM)

	cfg := DefaultQueryConfig()
	cfg.UseMessages = true

	report, err := runner.RunFull(samples, cfg, true)
	if err != nil {
		t.Fatalf("RunFull: %v", err)
	}

	if report.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3", report.TotalCount)
	}
	if report.TotalCorrect != 3 {
		t.Errorf("TotalCorrect = %d, want 3", report.TotalCorrect)
	}
	if report.OverallMean != 1.0 {
		t.Errorf("OverallMean = %f, want 1.0", report.OverallMean)
	}
}

func TestFullPipelineMixedScores(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	answerLLM := &mockLLM{response: "some answer"}

	// judgeLLM alternates: call 1 → CORRECT, call 2 → WRONG, call 3 → CORRECT
	var counter int64
	judgeLLM := &mockLLMFunc{fn: func(system, user string) (string, error) {
		n := atomic.AddInt64(&counter, 1)
		if n%2 == 1 { // odd calls → CORRECT
			return `{"reasoning": "match", "label": "CORRECT"}`, nil
		}
		return `{"reasoning": "no match", "label": "WRONG"}`, nil
	}}

	runner := NewRunner(store, answerLLM, judgeLLM)

	cfg := DefaultQueryConfig()
	cfg.UseMessages = true

	report, err := runner.RunFull(samples, cfg, true)
	if err != nil {
		t.Fatalf("RunFull: %v", err)
	}

	// 3 QA pairs: CORRECT, WRONG, CORRECT → 2 correct out of 3
	if report.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3", report.TotalCount)
	}
	if report.TotalCorrect != 2 {
		t.Errorf("TotalCorrect = %d, want 2", report.TotalCorrect)
	}

	wantMean := 2.0 / 3.0
	if report.OverallMean < wantMean-0.001 || report.OverallMean > wantMean+0.001 {
		t.Errorf("OverallMean = %f, want ~%f", report.OverallMean, wantMean)
	}
}

func TestFullPipelineReport(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	answerLLM := &mockLLM{response: "Paris"}
	judgeLLM := &mockLLM{response: `{"reasoning": "Match", "label": "CORRECT"}`}
	runner := NewRunner(store, answerLLM, judgeLLM)

	cfg := DefaultQueryConfig()
	cfg.UseMessages = true

	report, err := runner.RunFull(samples, cfg, true)
	if err != nil {
		t.Fatalf("RunFull: %v", err)
	}

	// Verify report content
	if report.TotalCount != 3 {
		t.Errorf("TotalCount = %d, want 3", report.TotalCount)
	}
	if report.TotalCorrect != 3 {
		t.Errorf("TotalCorrect = %d, want 3", report.TotalCorrect)
	}
	if report.OverallMean != 1.0 {
		t.Errorf("OverallMean = %f, want 1.0", report.OverallMean)
	}

	// PrintReport must not panic
	PrintReport(io.Discard, report, 1, 1)
}
