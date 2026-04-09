
package locomo

import (
	"strings"
	"testing"
)

func TestFormatContext(t *testing.T) {
	results := []SearchResult{
		{Content: "Paris was amazing!", Score: 1.5},
		{Content: "The croissants were incredible.", Score: 1.0},
	}

	ctx := FormatContext(results)
	if ctx == "" {
		t.Fatal("FormatContext returned empty string")
	}
	if !strings.Contains(ctx, "Paris was amazing!") {
		t.Errorf("context missing first result: %q", ctx)
	}
	if !strings.Contains(ctx, "croissants") {
		t.Errorf("context missing second result: %q", ctx)
	}
	// Should be a numbered list
	if !strings.Contains(ctx, "1.") {
		t.Errorf("context should contain numbered items: %q", ctx)
	}
}

func TestFormatContextEmpty(t *testing.T) {
	ctx := FormatContext(nil)
	if ctx != "" {
		t.Errorf("FormatContext(nil) = %q, want empty", ctx)
	}
}

func TestAnswerQuestion(t *testing.T) {
	llm := &mockLLM{response: "Paris"}

	results := []SearchResult{
		{Content: "Bob traveled to Paris in March.", Score: 1.0},
	}

	answer, err := AnswerQuestion(llm, "Where did Bob travel?", results)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answer != "Paris" {
		t.Errorf("answer = %q, want %q", answer, "Paris")
	}
}

func TestAnswerQuestionNoContext(t *testing.T) {
	llm := &mockLLM{response: "I don't know"}

	answer, err := AnswerQuestion(llm, "Where did Bob travel?", nil)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answer != "I don't know" {
		t.Errorf("answer = %q, want %q", answer, "I don't know")
	}
}

func TestBuildContextFromMessages(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	ctx, err := BuildContextFromMessages(store, "locomo_1", "Paris", 5)
	if err != nil {
		t.Fatalf("BuildContextFromMessages: %v", err)
	}
	if ctx == "" {
		t.Fatal("BuildContextFromMessages returned empty string")
	}
	if !strings.Contains(ctx, "Paris") {
		t.Errorf("context should contain 'Paris': %q", ctx)
	}
}

func TestRunQueries(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	llm := &mockLLM{response: "test answer"}
	cfg := DefaultQueryConfig()
	cfg.UseMessages = true

	results, err := RunQueries(store, llm, samples[0], cfg)
	if err != nil {
		t.Fatalf("RunQueries: %v", err)
	}

	// miniDataset has 3 scored QA pairs (categories 1, 2, 1 — category 5 excluded)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, r := range results {
		if r.SampleID != "1" {
			t.Errorf("result[%d].SampleID = %q, want %q", i, r.SampleID, "1")
		}
		if r.Generated != "test answer" {
			t.Errorf("result[%d].Generated = %q, want %q", i, r.Generated, "test answer")
		}
		if r.Question == "" {
			t.Errorf("result[%d].Question is empty", i)
		}
		if r.Gold == "" {
			t.Errorf("result[%d].Gold is empty", i)
		}
		if r.Category == 5 {
			t.Errorf("result[%d] should not have category 5", i)
		}
	}

	// Verify specific QA mapping
	if results[0].Question != "Where did Bob travel?" {
		t.Errorf("result[0].Question = %q", results[0].Question)
	}
	if results[0].Gold != "Paris" {
		t.Errorf("result[0].Gold = %q", results[0].Gold)
	}
}

func TestDefaultQueryConfig(t *testing.T) {
	cfg := DefaultQueryConfig()
	if cfg.TopK <= 0 {
		t.Errorf("TopK = %d, want > 0", cfg.TopK)
	}
	if cfg.MessageLimit <= 0 {
		t.Errorf("MessageLimit = %d, want > 0", cfg.MessageLimit)
	}
}

func TestFullContextAnswer(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	llm := &mockLLM{response: "Paris"}
	answer, err := FullContextAnswer(store, llm, "locomo_1", "Where did Bob travel?")
	if err != nil {
		t.Fatalf("FullContextAnswer: %v", err)
	}
	if answer == "" {
		t.Fatal("FullContextAnswer returned empty string")
	}
	if answer != "Paris" {
		t.Errorf("answer = %q, want %q", answer, "Paris")
	}
}

func TestRunQueriesFullContext(t *testing.T) {
	store := mustStore(t)

	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	llm := &mockLLM{response: "test full context answer"}
	cfg := DefaultQueryConfig()
	cfg.FullContext = true

	results, err := RunQueries(store, llm, samples[0], cfg)
	if err != nil {
		t.Fatalf("RunQueries(FullContext): %v", err)
	}

	// miniDataset has 3 scored QA pairs (categories 1, 2, 1 — category 5 excluded)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, r := range results {
		if r.Generated != "test full context answer" {
			t.Errorf("result[%d].Generated = %q, want %q", i, r.Generated, "test full context answer")
		}
		if r.Generated == "" {
			t.Errorf("result[%d].Generated is empty", i)
		}
		if r.SampleID != "1" {
			t.Errorf("result[%d].SampleID = %q, want %q", i, r.SampleID, "1")
		}
		if r.Category == 5 {
			t.Errorf("result[%d] should not have category 5", i)
		}
	}
}
