package proxy

import (
	"fmt"
	"strings"
	"testing"
)

func TestFindCompactableRunsExactly50(t *testing.T) {
	dt := NewDecayTracker()
	// Exactly 50 messages (the minimum) should form a run
	for i := 10; i <= 59; i++ {
		dt.MarkStubbed(i, 1, 0.0)
	}
	runs := findCompactableRuns(dt, 200, 1, 180, 300)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run for exactly 50 stubs, got %d", len(runs))
	}
	if runs[0].start != 10 || runs[0].end != 59 {
		t.Errorf("expected run 10-59, got %d-%d", runs[0].start, runs[0].end)
	}
}

func TestFindCompactableRuns49NotEnough(t *testing.T) {
	dt := NewDecayTracker()
	// 49 messages should NOT form a run
	for i := 10; i <= 58; i++ {
		dt.MarkStubbed(i, 1, 0.0)
	}
	runs := findCompactableRuns(dt, 200, 1, 180, 300)
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs for 49 stubs, got %d", len(runs))
	}
}

func TestFindCompactableRuns(t *testing.T) {
	dt := NewDecayTracker()
	// threadLen=200 → boundaries (5, 15, 50)
	// For Stage 3: age > s2end=50, so currentReqIdx must be > stub_at + 50
	for i := 10; i <= 80; i++ {
		dt.MarkStubbed(i, 1, 0.0) // no boost → fastest decay
	}

	runs := findCompactableRuns(dt, 200, 10, 80, 300)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].start != 10 || runs[0].end != 80 {
		t.Errorf("expected run 10-80, got %d-%d", runs[0].start, runs[0].end)
	}
}

func TestFindCompactableRunsMinLength(t *testing.T) {
	dt := NewDecayTracker()
	// Only 30 consecutive Stage 3 — below threshold of 50
	for i := 10; i <= 39; i++ {
		dt.MarkStubbed(i, 1, 0.0)
	}

	runs := findCompactableRuns(dt, 200, 1, 180, 300)
	if len(runs) != 0 {
		t.Errorf("expected 0 runs for short sequence, got %d", len(runs))
	}
}

func TestFindCompactableRunsMultiple(t *testing.T) {
	dt := NewDecayTracker()
	// Two runs of 50+ separated by a gap (message 70 is not stage 3)
	for i := 10; i <= 69; i++ {
		dt.MarkStubbed(i, 1, 0.0)
	}
	// Gap at 70 (not stubbed)
	for i := 71; i <= 130; i++ {
		dt.MarkStubbed(i, 1, 0.0)
	}

	runs := findCompactableRuns(dt, 200, 1, 180, 300)
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}
	if runs[0].start != 10 || runs[0].end != 69 {
		t.Errorf("run 0: expected 10-69, got %d-%d", runs[0].start, runs[0].end)
	}
	if runs[1].start != 71 || runs[1].end != 130 {
		t.Errorf("run 1: expected 71-130, got %d-%d", runs[1].start, runs[1].end)
	}
}

func TestExtractStatsFromMessages(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/app/main.go"}},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "content": "file contents..."},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/app/main.go"}},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "content": "edited"},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/app/config.go"}},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "content": "config"},
			},
		},
	}

	stats := extractStatsFromMessages(messages, 0, 6)

	// Tool stats: Read:2, Edit:1
	if stats.ToolStats == "" {
		t.Error("expected non-empty tool stats")
	}

	// File stats: main.go(2), config.go(1)
	if stats.FileStats == "" {
		t.Error("expected non-empty file stats")
	}
}

func TestBuildCompactedContent(t *testing.T) {
	stats := compactionStats{
		FileStats: "main.go(2), config.go(1)",
		ToolStats: "Read:2, Edit:1",
		Decisions: "switched to websockets",
	}

	content := buildCompactedContent(10, 60, stats)
	if content == "" {
		t.Fatal("expected non-empty compacted content")
	}

	// Should contain the range
	if !containsString(content, "10") || !containsString(content, "60") {
		t.Error("content should mention message range")
	}
}

func TestCompactMessages(t *testing.T) {
	dt := NewDecayTracker()
	// Create 60 messages that are all Stage 3
	// threadLen=200, boundaries (5, 15, 50), all stubbed at req 1, now at req 300 → age=299 >> 50
	original := make([]any, 65)
	modified := make([]any, 65)

	// Message 0 = system (protected)
	original[0] = map[string]any{"role": "system", "content": "system prompt"}
	modified[0] = original[0]

	// Messages 1-60 = assistant/user pairs, all stubbed
	for i := 1; i <= 60; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		original[i] = map[string]any{
			"role": role,
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/app/file%d.go", i)}},
			},
		}
		modified[i] = map[string]any{
			"role":    role,
			"content": fmt.Sprintf("[→] Read /app/file%d.go", i),
		}
		dt.MarkStubbed(i, 1, 0.0)
	}

	// Messages 61-64 = recent (protected tail)
	for i := 61; i <= 64; i++ {
		original[i] = map[string]any{"role": "user", "content": "recent message"}
		modified[i] = original[i]
	}

	result, blocks := CompactMessages(modified, original, dt, 200, 300)

	// Should have fewer messages than original
	if len(result) >= len(modified) {
		t.Errorf("expected fewer messages after compaction, got %d (was %d)", len(result), len(modified))
	}

	// Should have exactly 1 compacted block
	if len(blocks) != 1 {
		t.Fatalf("expected 1 compacted block, got %d", len(blocks))
	}

	// Block should cover range 1-60
	if blocks[0].StartIdx != 1 || blocks[0].EndIdx != 60 {
		t.Errorf("block range: expected 1-60, got %d-%d", blocks[0].StartIdx, blocks[0].EndIdx)
	}

	// The compacted message should contain "[Compacted:"
	found := false
	for _, msg := range result {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].(string)
		if ok && strings.Contains(content, "[Compacted:") {
			found = true
			break
		}
	}
	if !found {
		// Debug: dump all messages to find what went wrong
		for i, msg := range result {
			m, _ := msg.(map[string]any)
			c, _ := m["content"].(string)
			if len(c) > 80 {
				c = c[:80]
			}
			t.Logf("  msg[%d] role=%v content=%q", i, m["role"], c)
		}
		t.Error("expected a message containing '[Compacted:' in result")
	}
}

func TestCompactMessagesNoRuns(t *testing.T) {
	dt := NewDecayTracker()
	// Only 10 messages, none old enough for Stage 3
	messages := make([]any, 10)
	for i := range messages {
		messages[i] = map[string]any{"role": "user", "content": "msg"}
	}

	result, blocks := CompactMessages(messages, messages, dt, 10, 5)

	if len(result) != len(messages) {
		t.Errorf("expected same length, got %d", len(result))
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestEndToEndStubifyThenCompact(t *testing.T) {
	// Build a realistic 120-message thread with tool_use/tool_result pairs
	msgs := make([]any, 120)
	msgs[0] = map[string]any{"role": "system", "content": "You are an AI assistant."}

	dt := NewDecayTracker()

	for i := 1; i < 120; i++ {
		if i%2 == 1 {
			msgs[i] = map[string]any{
				"role":    "user",
				"content": fmt.Sprintf("Analyze file %d with detailed explanation of the code structure and patterns", i),
			}
		} else {
			msgs[i] = map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": fmt.Sprintf("[→] Read /app/file%d.go → deep_search('Read /app/file%d.go')", i, i),
					},
				},
			}
		}
		// Mark old messages as stubbed early
		if i < 80 {
			dt.MarkStubbed(i, 1, 0.0) // stubbed at request 1, no boost
		}
	}

	// requestIdx=400 → age=399, boundaries for threadLen=120: (5, 15, 50)
	// Age 399 >> 50 → Stage 3 for all marked messages
	result, runs := CompactMessages(msgs, msgs, dt, 120, 400)

	if len(runs) == 0 {
		t.Fatal("expected at least 1 compaction run")
	}

	if len(result) >= 120 {
		t.Errorf("expected fewer messages after compaction, got %d", len(result))
	}

	// First message should still be system
	first, ok := result[0].(map[string]any)
	if !ok || first["role"] != "system" {
		t.Error("first message should be system")
	}

	// Should contain at least one compacted block
	foundCompacted := false
	for _, msg := range result {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		text, _ := m["content"].(string)
		if strings.Contains(text, "[Compacted:") {
			foundCompacted = true
			break
		}
	}
	if !foundCompacted {
		t.Error("expected at least one [Compacted: block in result")
	}

	// Token savings
	origTokens := estimateTokensFromMessages(msgs, testEstimate)
	compactedTokens := estimateTokensFromMessages(result, testEstimate)
	if compactedTokens >= origTokens {
		t.Errorf("compaction should reduce tokens: before=%d after=%d", origTokens, compactedTokens)
	}
	savings := float64(origTokens-compactedTokens) / float64(origTokens) * 100
	t.Logf("E2E: %d → %d tokens (%.1f%% reduction, %d runs)", origTokens, compactedTokens, savings, len(runs))
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
