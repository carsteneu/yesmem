package proxy

import (
	"testing"
)

func TestCountConsecutiveErrorRuns_NoErrors(t *testing.T) {
	msgs := buildMessages(
		userMsg("do something"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(false, "ok"),
		assistantMsg("done"),
	)
	result := countConsecutiveErrorRuns(msgs, 8)
	if result.runs != 0 || result.affectedMessages != 0 {
		t.Errorf("expected 0 runs, got %d runs, %d msgs", result.runs, result.affectedMessages)
	}
}

func TestCountConsecutiveErrorRuns_ShortRun(t *testing.T) {
	// 3 errors in a row — below threshold of 8
	msgs := buildMessages(
		userMsg("fix it"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "error 1"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "error 2"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "error 3"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(false, "ok"),
	)
	result := countConsecutiveErrorRuns(msgs, 8)
	if result.runs != 0 {
		t.Errorf("3 errors should not trigger (threshold 8), got %d runs", result.runs)
	}
}

func TestCountConsecutiveErrorRuns_LongRun(t *testing.T) {
	// 10 errors in a row — above threshold
	var parts []any
	parts = append(parts, userMsg("fix it"))
	for i := 0; i < 10; i++ {
		parts = append(parts, assistantWithToolUse("Bash", "go build"))
		parts = append(parts, toolResult(true, "error"))
	}
	parts = append(parts, assistantWithToolUse("Bash", "go build"))
	parts = append(parts, toolResult(false, "ok"))
	msgs := buildMessages(parts...)

	result := countConsecutiveErrorRuns(msgs, 8)
	if result.runs != 1 {
		t.Errorf("expected 1 run, got %d", result.runs)
	}
	// 10 errors × 2 messages each (tool_use + tool_result) = 20
	if result.affectedMessages < 16 {
		t.Errorf("expected ≥16 affected messages, got %d", result.affectedMessages)
	}
}

func TestCountConsecutiveErrorRuns_ResolvedNotCounted(t *testing.T) {
	// 10 errors then success — resolved run should still count
	// (the messages were still spent in fixation)
	var parts []any
	parts = append(parts, userMsg("fix"))
	for i := 0; i < 10; i++ {
		parts = append(parts, assistantWithToolUse("Bash", "go build"))
		parts = append(parts, toolResult(true, "error"))
	}
	parts = append(parts, assistantWithToolUse("Bash", "go build"))
	parts = append(parts, toolResult(false, "success"))
	msgs := buildMessages(parts...)

	result := countConsecutiveErrorRuns(msgs, 8)
	// Run counts even if resolved — the messages were still spent
	if result.runs != 1 {
		t.Errorf("expected 1 run (even if resolved), got %d", result.runs)
	}
}

func TestCountEditBuildCycles_NoCycles(t *testing.T) {
	msgs := buildMessages(
		userMsg("edit file"),
		assistantWithToolUse("Edit", "fix.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(false, "ok"),
	)
	result := countEditBuildErrorCycles(msgs, 6)
	if result.runs != 0 {
		t.Errorf("expected 0 cycles, got %d", result.runs)
	}
}

func TestCountEditBuildCycles_Above_Threshold(t *testing.T) {
	var parts []any
	parts = append(parts, userMsg("fix"))
	for i := 0; i < 8; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "main.go"))
		parts = append(parts, toolResult(false, "edited"))
		parts = append(parts, assistantWithToolUse("Bash", "go build"))
		parts = append(parts, toolResult(true, "compile error"))
	}
	parts = append(parts, assistantWithToolUse("Edit", "main.go"))
	parts = append(parts, toolResult(false, "final fix"))
	parts = append(parts, assistantWithToolUse("Bash", "go build"))
	parts = append(parts, toolResult(false, "ok"))
	msgs := buildMessages(parts...)

	result := countEditBuildErrorCycles(msgs, 6)
	if result.runs != 1 {
		t.Errorf("expected 1 cycle run, got %d", result.runs)
	}
	// 8 cycles × 4 messages each = 32
	if result.affectedMessages < 24 {
		t.Errorf("expected ≥24 affected messages, got %d", result.affectedMessages)
	}
}

func TestCountFileRetries_Normal(t *testing.T) {
	var parts []any
	parts = append(parts, userMsg("edit"))
	for i := 0; i < 5; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "main.go"))
		parts = append(parts, toolResult(false, "ok"))
	}
	msgs := buildMessages(parts...)
	retries := countFileRetries(msgs, 10)
	if len(retries) != 0 {
		t.Errorf("5 edits should not trigger (threshold 10), got %v", retries)
	}
}

func TestCountFileRetries_Excessive(t *testing.T) {
	var parts []any
	parts = append(parts, userMsg("edit"))
	for i := 0; i < 12; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "broken.go"))
		parts = append(parts, toolResult(false, "ok"))
	}
	for i := 0; i < 3; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "other.go"))
		parts = append(parts, toolResult(false, "ok"))
	}
	msgs := buildMessages(parts...)
	retries := countFileRetries(msgs, 10)
	if retries["broken.go"] != 12 {
		t.Errorf("expected 12 retries for broken.go, got %d", retries["broken.go"])
	}
	if _, ok := retries["other.go"]; ok {
		t.Error("other.go should not be in retries (only 3)")
	}
}

func TestAnalyzeFixation_CleanSession(t *testing.T) {
	var parts []any
	for i := 0; i < 50; i++ {
		parts = append(parts, userMsg("task"))
		parts = append(parts, assistantWithToolUse("Bash", "echo ok"))
		parts = append(parts, toolResult(false, "ok"))
		parts = append(parts, assistantMsg("done"))
	}
	msgs := buildMessages(parts...)

	analysis := AnalyzeFixation(msgs)
	if analysis.Ratio > 0.0 {
		t.Errorf("clean session should have 0 ratio, got %f", analysis.Ratio)
	}
}

func TestAnalyzeFixation_ShortBadSession(t *testing.T) {
	var parts []any
	parts = append(parts, userMsg("fix"))
	// 10 consecutive errors in a 22-message session
	for i := 0; i < 10; i++ {
		parts = append(parts, assistantWithToolUse("Bash", "go build"))
		parts = append(parts, toolResult(true, "error"))
	}
	parts = append(parts, assistantMsg("giving up"))
	msgs := buildMessages(parts...)

	analysis := AnalyzeFixation(msgs)
	if analysis.TotalMessages != 22 {
		t.Errorf("expected 22 total messages, got %d", analysis.TotalMessages)
	}
	if analysis.Ratio < 0.5 {
		t.Errorf("short bad session should have high ratio, got %f", analysis.Ratio)
	}
}

func TestAnalyzeFixation_LongSessionSmallFixation(t *testing.T) {
	var parts []any
	// 200 productive messages
	for i := 0; i < 50; i++ {
		parts = append(parts, userMsg("task"))
		parts = append(parts, assistantWithToolUse("Bash", "echo ok"))
		parts = append(parts, toolResult(false, "ok"))
		parts = append(parts, assistantMsg("done"))
	}
	// 1 fixation run of 10 errors
	for i := 0; i < 10; i++ {
		parts = append(parts, assistantWithToolUse("Bash", "go build"))
		parts = append(parts, toolResult(true, "error"))
	}
	// 20 more productive messages
	for i := 0; i < 5; i++ {
		parts = append(parts, userMsg("next"))
		parts = append(parts, assistantWithToolUse("Bash", "echo ok"))
		parts = append(parts, toolResult(false, "ok"))
		parts = append(parts, assistantMsg("done"))
	}
	msgs := buildMessages(parts...)

	analysis := AnalyzeFixation(msgs)
	// ~240 total, 20 fixation → ~8%
	if analysis.Ratio > 0.15 {
		t.Errorf("long session with small fixation should have low ratio, got %f", analysis.Ratio)
	}
	if analysis.Ratio < 0.01 {
		t.Errorf("should still detect the fixation, got %f", analysis.Ratio)
	}
}

// --- Test helpers ---

func userMsg(text string) any {
	return map[string]any{"role": "user", "content": text}
}

func assistantMsg(text string) any {
	return map[string]any{"role": "assistant", "content": text}
}

func assistantWithToolUse(toolName, input string) any {
	return map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type": "tool_use",
				"id":   "tool_" + toolName,
				"name": toolName,
				"input": map[string]any{
					"command":   input,
					"file_path": input,
				},
			},
		},
	}
}

func toolResult(isError bool, content string) any {
	return map[string]any{
		"role":    "user",
		"content": []any{
			map[string]any{
				"type":       "tool_result",
				"tool_use_id": "tool_id",
				"is_error":   isError,
				"content":    content,
			},
		},
	}
}

func buildMessages(msgs ...any) []any {
	return msgs
}
