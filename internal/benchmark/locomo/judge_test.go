
package locomo

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/benchmark"
)

// mockLLM implements benchmark.LLMClient for testing.
// Other test files in this package can reuse this type.
type mockLLM struct{ response string }

func (m *mockLLM) Complete(system, user string) (string, error) { return m.response, nil }
func (m *mockLLM) Model() string                               { return "mock" }

// Compile-time check that mockLLM satisfies the interface.
var _ benchmark.LLMClient = (*mockLLM)(nil)

func TestJudgeAnswerCorrect(t *testing.T) {
	llm := &mockLLM{response: `{"reasoning": "Same city", "label": "CORRECT"}`}

	result, err := JudgeAnswer(llm, "Where did Bob travel?", "Paris", "Paris")
	if err != nil {
		t.Fatalf("JudgeAnswer: %v", err)
	}

	if result.Label != "CORRECT" {
		t.Errorf("label = %q, want %q", result.Label, "CORRECT")
	}
	if result.Score != 1 {
		t.Errorf("score = %d, want 1", result.Score)
	}
	if result.Reasoning != "Same city" {
		t.Errorf("reasoning = %q, want %q", result.Reasoning, "Same city")
	}
	if result.Question != "Where did Bob travel?" {
		t.Errorf("question = %q", result.Question)
	}
	if result.Gold != "Paris" {
		t.Errorf("gold = %q", result.Gold)
	}
	if result.Generated != "Paris" {
		t.Errorf("generated = %q", result.Generated)
	}
}

func TestJudgeAnswerWrong(t *testing.T) {
	llm := &mockLLM{response: `{"reasoning": "Different city", "label": "WRONG"}`}

	result, err := JudgeAnswer(llm, "Where did Bob travel?", "Paris", "London")
	if err != nil {
		t.Fatalf("JudgeAnswer: %v", err)
	}

	if result.Label != "WRONG" {
		t.Errorf("label = %q, want %q", result.Label, "WRONG")
	}
	if result.Score != 0 {
		t.Errorf("score = %d, want 0", result.Score)
	}
	if result.Reasoning != "Different city" {
		t.Errorf("reasoning = %q, want %q", result.Reasoning, "Different city")
	}
}

func TestJudgeAnswerParsesFreeText(t *testing.T) {
	// LLM returns free text instead of JSON — fallback parsing should work.
	llm := &mockLLM{response: "The answer is CORRECT because both mention Paris."}

	result, err := JudgeAnswer(llm, "Where did Bob travel?", "Paris", "Paris")
	if err != nil {
		t.Fatalf("JudgeAnswer: %v", err)
	}

	if result.Label != "CORRECT" {
		t.Errorf("label = %q, want %q", result.Label, "CORRECT")
	}
	if result.Score != 1 {
		t.Errorf("score = %d, want 1", result.Score)
	}
	// Reasoning should contain the raw response when JSON parsing fails.
	if result.Reasoning == "" {
		t.Error("reasoning should not be empty")
	}
}

func TestJudgeAnswerParsesFreeTextWrong(t *testing.T) {
	llm := &mockLLM{response: "The answer is WRONG because London is not Paris."}

	result, err := JudgeAnswer(llm, "Where?", "Paris", "London")
	if err != nil {
		t.Fatalf("JudgeAnswer: %v", err)
	}

	if result.Label != "WRONG" {
		t.Errorf("label = %q, want %q", result.Label, "WRONG")
	}
	if result.Score != 0 {
		t.Errorf("score = %d, want 0", result.Score)
	}
}

func TestJudgeAll(t *testing.T) {
	llm := &mockLLM{response: `{"reasoning": "Match", "label": "CORRECT"}`}

	queries := []QueryResult{
		{SampleID: "1", Question: "Q1", Gold: "A1", Generated: "A1", Category: 1},
		{SampleID: "1", Question: "Q2", Gold: "A2", Generated: "A2", Category: 2},
		{SampleID: "2", Question: "Q3", Gold: "A3", Generated: "wrong", Category: 3},
	}

	results, err := JudgeAll(llm, queries)
	if err != nil {
		t.Fatalf("JudgeAll: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// All should be CORRECT since mock always returns CORRECT.
	for i, r := range results {
		if r.Label != "CORRECT" {
			t.Errorf("result[%d]: label = %q, want %q", i, r.Label, "CORRECT")
		}
		if r.Score != 1 {
			t.Errorf("result[%d]: score = %d, want 1", i, r.Score)
		}
		if r.Category != queries[i].Category {
			t.Errorf("result[%d]: category = %d, want %d", i, r.Category, queries[i].Category)
		}
	}

	// Verify question/gold/generated are passed through.
	if results[0].Question != "Q1" {
		t.Errorf("result[0].Question = %q, want %q", results[0].Question, "Q1")
	}
	if results[2].Generated != "wrong" {
		t.Errorf("result[2].Generated = %q, want %q", results[2].Generated, "wrong")
	}
}
