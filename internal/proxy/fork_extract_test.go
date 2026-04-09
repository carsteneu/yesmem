package proxy

import (
	"strings"
	"testing"
)

func TestExtractAndEvaluatePrompt(t *testing.T) {
	ctx := ForkContext{
		LastExtractedIdx: 150,
		InjectedIDs:      map[int64]string{42: "associative", 99: "briefing"},
		Project:          "yesmem",
		PreviousForkLearnings: []PreviousForkLearning{
			{Content: "Deploy immer über make deploy", Category: "decision"},
			{Content: "Pattern X bevorzugt", Category: "pattern"},
		},
	}

	prompt := extractAndEvaluatePrompt(ctx)

	// Must contain i18n reflection intro (English default)
	if !strings.Contains(prompt, "mandatory reflection") {
		t.Error("prompt should contain reflection intro from i18n")
	}
	// Must contain previous fork learnings
	if !strings.Contains(prompt, "Deploy immer über make deploy") {
		t.Error("prompt should contain previous fork learnings")
	}
	if !strings.Contains(prompt, "Pattern X bevorzugt") {
		t.Error("prompt should contain all previous fork learnings")
	}
	// Must contain injected learning IDs
	if !strings.Contains(prompt, "42") {
		t.Error("prompt should contain injected learning IDs")
	}
	if !strings.Contains(prompt, "99") {
		t.Error("prompt should contain all injected learning IDs")
	}
	// Must contain contradiction task
	if !strings.Contains(prompt, "contradictions") {
		t.Error("prompt should contain contradiction task")
	}
	// Must contain impact_score instruction
	if !strings.Contains(prompt, "impact_score") {
		t.Error("prompt should contain impact_score instruction")
	}
	// Must request JSON output with contradictions field
	if !strings.Contains(prompt, "contradictions") {
		t.Error("prompt JSON format should include contradictions")
	}
}

func TestExtractAndEvaluatePrompt_NoInjectedIDs(t *testing.T) {
	ctx := ForkContext{
		LastExtractedIdx: 0,
		InjectedIDs:      nil,
	}

	prompt := extractAndEvaluatePrompt(ctx)

	// Should NOT contain evaluation task header when no IDs injected
	if strings.Contains(prompt, "Task 2") {
		t.Error("prompt should NOT contain evaluation task when no IDs injected")
	}
	// Should always contain learning extraction task
	if !strings.Contains(prompt, "Task 1") {
		t.Error("prompt should always contain learning extraction task")
	}
}

func TestExtractAndEvaluatePrompt_NoPreviousLearnings(t *testing.T) {
	ctx := ForkContext{
		PreviousForkLearnings: nil,
	}
	prompt := extractAndEvaluatePrompt(ctx)
	// Should use "no previous" text from i18n
	if !strings.Contains(prompt, "first reflection") {
		t.Error("prompt should indicate first reflection when no previous learnings")
	}
}

func TestParseExtractionResult(t *testing.T) {
	response := `{
		"learnings": [
			{"content": "Always check cache before forking", "category": "gotcha", "entities": ["proxy", "fork"]}
		],
		"evaluations": [
			{"learning_id": 42, "verdict": "useful", "reason": "Prevented bug", "action": "boost"}
		]
	}`

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(result.Learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d", len(result.Learnings))
	}
	if result.Learnings[0].Category != "gotcha" {
		t.Errorf("expected category=gotcha, got %s", result.Learnings[0].Category)
	}
	if result.Learnings[0].Content != "Always check cache before forking" {
		t.Errorf("unexpected content: %s", result.Learnings[0].Content)
	}
	if len(result.Learnings[0].Entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(result.Learnings[0].Entities))
	}
	if len(result.Evaluations) != 1 {
		t.Fatalf("expected 1 evaluation, got %d", len(result.Evaluations))
	}
	if result.Evaluations[0].LearningID != 42 {
		t.Errorf("expected learning_id=42, got %d", result.Evaluations[0].LearningID)
	}
	if result.Evaluations[0].Action != "boost" {
		t.Errorf("expected action=boost, got %s", result.Evaluations[0].Action)
	}
}

func TestParseExtractionResult_MarkdownWrapped(t *testing.T) {
	response := "Here is the analysis:\n```json\n" + `{
		"learnings": [],
		"evaluations": [{"learning_id": 1, "verdict": "noise", "reason": "irrelevant", "action": "noise"}]
	}` + "\n```"

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(result.Evaluations) != 1 {
		t.Fatalf("expected 1 evaluation, got %d", len(result.Evaluations))
	}
}

func TestParseExtractionResult_NoJSON(t *testing.T) {
	_, err := parseExtractionJSON("no json here at all")
	if err == nil {
		t.Error("expected error for response without JSON")
	}
}

func TestParseExtractionResult_EmptyResults(t *testing.T) {
	response := `{"learnings": [], "evaluations": []}`
	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(result.Learnings) != 0 {
		t.Errorf("expected 0 learnings, got %d", len(result.Learnings))
	}
	if len(result.Evaluations) != 0 {
		t.Errorf("expected 0 evaluations, got %d", len(result.Evaluations))
	}
}

func TestParseExtractionResult_WithContradictions(t *testing.T) {
	response := `{
		"learnings": [{"content": "test learning", "category": "decision", "entities": [], "status": "new"}],
		"evaluations": [
			{"learning_id": 42, "verdict": "useful", "reason": "was used", "action": "boost", "impact_score": 0.8}
		],
		"contradictions": [
			{"learning_a": 42, "learning_b": 99, "description": "Pattern X vs anti-Pattern X"}
		]
	}`

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(result.Contradictions) != 1 {
		t.Fatalf("expected 1 contradiction, got %d", len(result.Contradictions))
	}
	if result.Contradictions[0].LearningA != 42 {
		t.Errorf("expected learning_a=42, got %d", result.Contradictions[0].LearningA)
	}
	if result.Contradictions[0].LearningB != 99 {
		t.Errorf("expected learning_b=99, got %d", result.Contradictions[0].LearningB)
	}
	if result.Contradictions[0].Description != "Pattern X vs anti-Pattern X" {
		t.Errorf("unexpected description: %s", result.Contradictions[0].Description)
	}
	if result.Evaluations[0].ImpactScore != 0.8 {
		t.Errorf("expected impact_score=0.8, got %f", result.Evaluations[0].ImpactScore)
	}
	if result.Learnings[0].Status != "new" {
		t.Errorf("expected status=new, got %s", result.Learnings[0].Status)
	}
}

func TestParseExtractionResult_NoContradictions(t *testing.T) {
	response := `{
		"learnings": [],
		"evaluations": [
			{"learning_id": 42, "verdict": "noise", "reason": "ignored", "action": "noise", "impact_score": 0.0}
		]
	}`

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(result.Contradictions) != 0 {
		t.Errorf("expected 0 contradictions, got %d", len(result.Contradictions))
	}
	if result.Evaluations[0].ImpactScore != 0.0 {
		t.Errorf("expected impact_score=0.0, got %f", result.Evaluations[0].ImpactScore)
	}
}

func TestParseExtractionResult_LearningStatuses(t *testing.T) {
	response := `{
		"learnings": [
			{"content": "confirmed one", "category": "decision", "entities": [], "status": "confirmed"},
			{"content": "revised one", "category": "pattern", "entities": [], "status": "revised"},
			{"content": "invalidated one", "category": "gotcha", "entities": [], "status": "invalidated"}
		],
		"evaluations": []
	}`

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(result.Learnings) != 3 {
		t.Fatalf("expected 3 learnings, got %d", len(result.Learnings))
	}
	if result.Learnings[0].Status != "confirmed" {
		t.Errorf("expected status=confirmed, got %s", result.Learnings[0].Status)
	}
	if result.Learnings[1].Status != "revised" {
		t.Errorf("expected status=revised, got %s", result.Learnings[1].Status)
	}
	if result.Learnings[2].Status != "invalidated" {
		t.Errorf("expected status=invalidated, got %s", result.Learnings[2].Status)
	}
}

func TestNewExtractAndEvaluateConfig(t *testing.T) {
	cfg := NewExtractAndEvaluateConfig("claude-sonnet-4-6")

	if cfg.Name != "extract_and_evaluate" {
		t.Errorf("expected name=extract_and_evaluate, got %s", cfg.Name)
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Errorf("expected model=claude-sonnet-4-6, got %s", cfg.Model)
	}
	if cfg.MaxTokens != 3072 {
		t.Errorf("expected max_tokens=3072, got %d", cfg.MaxTokens)
	}

	// Gate: requires cache read tokens > 0
	if cfg.Gate(ForkContext{CacheReadTokens: 0}) {
		t.Error("gate should reject when CacheReadTokens=0")
	}
	if !cfg.Gate(ForkContext{CacheReadTokens: 100}) {
		t.Error("gate should pass when CacheReadTokens>0")
	}
}

func TestExtractAndEvaluatePrompt_ContainsFlavor(t *testing.T) {
	ctx := ForkContext{
		InjectedIDs: map[int64]string{42: "associative"},
	}
	prompt := extractAndEvaluatePrompt(ctx)

	if !strings.Contains(prompt, "session_flavor") {
		t.Error("prompt should request session_flavor field")
	}
}

func TestParseExtractionResult_WithFlavor(t *testing.T) {
	response := `{
		"learnings": [{"content": "test", "category": "gotcha", "entities": [], "status": "new"}],
		"evaluations": [],
		"session_flavor": "Cache-Keepalive debugging & per-thread state fixes"
	}`

	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if result.SessionFlavor != "Cache-Keepalive debugging & per-thread state fixes" {
		t.Errorf("expected session_flavor, got %q", result.SessionFlavor)
	}
}

func TestParseExtractionResult_FlavorEmpty(t *testing.T) {
	response := `{"learnings": [], "evaluations": [], "session_flavor": ""}`
	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if result.SessionFlavor != "" {
		t.Errorf("expected empty flavor, got %q", result.SessionFlavor)
	}
}

func TestParseExtractionResult_FlavorMissing(t *testing.T) {
	response := `{"learnings": [], "evaluations": []}`
	result, err := parseExtractionJSON(response)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if result.SessionFlavor != "" {
		t.Errorf("expected empty flavor when missing, got %q", result.SessionFlavor)
	}
}
