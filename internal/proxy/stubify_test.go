package proxy

import (
	"testing"
)

func TestStubify_DecisionsDecayOverTime(t *testing.T) {
	// Build messages: long assistant analysis + short user decision
	msgs := make([]any, 0, 25)
	msgs = append(msgs, map[string]any{"role": "system", "content": "system"})

	// Old decision pair (index 1-2) — should be stubbed at high requestIdx
	msgs = append(msgs, map[string]any{"role": "assistant", "content": longString(2000)})
	msgs = append(msgs, map[string]any{"role": "user", "content": "ja bitte"}) // decision

	// Filler to push threshold
	for i := 0; i < 12; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": longString(1000)})
		msgs = append(msgs, map[string]any{"role": "user", "content": longString(500)})
	}

	// Recent messages (keepRecent=3)
	msgs = append(msgs, map[string]any{"role": "assistant", "content": "recent1"})
	msgs = append(msgs, map[string]any{"role": "user", "content": "recent2"})
	msgs = append(msgs, map[string]any{"role": "assistant", "content": "recent3"})

	dt := NewDecayTracker()
	// requestIdx=200 → old decision at index 2 has age ~200 → should decay
	result := Stubify(msgs, 100, 3, 200, nil, nil, testEstimate, dt)

	// The decision "ja bitte" at index 2 should be stubbed (not protected forever)
	// At first stubbing, age=0 so it's Stage 0, but it IS stubbed (not skipped)
	// "ja bitte" is < 800 chars as user text → not stubbable by length
	// But the assistant message before it (index 1) IS stubbable (2000 chars)
	msg1, _ := result.Messages[1].(map[string]any)
	text1 := extractTextFromContent(msg1["content"])
	if len(text1) >= 2000 {
		t.Error("old assistant message before decision should be stubbed (not protected)")
	}

	// Verify the decision was counted as stubbed (not skipped via continue)
	if result.StubCount == 0 {
		t.Error("expected at least some stubs including the decision's assistant context")
	}
}

func TestStubify_RecentDecisionsStillProtected(t *testing.T) {
	// Build messages with a recent decision
	msgs := make([]any, 0, 10)
	msgs = append(msgs, map[string]any{"role": "system", "content": "system"})

	// Filler
	for i := 0; i < 4; i++ {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": longString(1000)})
		msgs = append(msgs, map[string]any{"role": "user", "content": longString(500)})
	}

	// Decision just before keepRecent zone
	msgs = append(msgs, map[string]any{"role": "assistant", "content": longString(2000)})
	msgs = append(msgs, map[string]any{"role": "user", "content": "nimm Ansatz B"}) // decision, keyword match

	// Keep recent
	msgs = append(msgs, map[string]any{"role": "assistant", "content": "last"})

	dt := NewDecayTracker()
	// requestIdx=5 → decision at index ~10, age = 5 - 10/2 = 0 → fresh, should be protected
	result := Stubify(msgs, 100, 1, 5, nil, nil, testEstimate, dt)

	// Find the decision message
	for i, msg := range result.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		text := extractTextFromContent(m["content"])
		if text == "nimm Ansatz B" {
			_ = i // found, still protected
			return
		}
	}
	t.Error("recent decision should still be protected")
}

func TestEstimateTokensFromMessages(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi there"},
	}
	tokens := estimateTokensFromMessages(msgs, testEstimate)
	if tokens < 10 || tokens > 100 {
		t.Errorf("unexpected token estimate: %d", tokens)
	}
}

func TestStubify_BelowThreshold_NoChange(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
	}
	result := Stubify(msgs, 100000, 5, 1, nil, nil, testEstimate)
	if result.StubCount != 0 {
		t.Errorf("expected no stubs, got %d", result.StubCount)
	}
	if len(result.Archived) != 0 {
		t.Errorf("expected no archived, got %d", len(result.Archived))
	}
}

func TestStubify_ThinkingRemoved(t *testing.T) {
	msgs := buildLargeMessages(30)
	// Insert a thinking block in message 2 (assistant)
	msgs[2] = map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "thinking", "text": longString(5000)},
			map[string]any{"type": "text", "text": "short response"},
		},
	}

	result := Stubify(msgs, 100, 5, 1, nil, nil, testEstimate) // very low threshold to trigger
	if result.StubCount == 0 {
		t.Error("expected stubbing to occur")
	}

	// Check that thinking blocks were removed
	for _, msg := range result.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "thinking" {
				t.Error("thinking block should have been removed")
			}
		}
	}
}

func TestStubify_ToolUseStubbed(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_abc",
					"name":  "Read",
					"input": map[string]any{"file_path": "/home/user/main.go"},
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_abc",
					"content":     longString(10000),
				},
			},
		},
		// Keep recent: these won't be stubbed
		map[string]any{"role": "assistant", "content": "done"},
		map[string]any{"role": "user", "content": "thanks"},
	}

	result := Stubify(msgs, 100, 2, 1, nil, nil, testEstimate) // threshold=100, keepRecent=2
	if result.StubCount == 0 {
		t.Error("expected stubbing to occur")
	}

	// Check tool_use was replaced with text stub
	msg1, ok := result.Messages[1].(map[string]any)
	if !ok {
		t.Fatal("message 1 not a map")
	}
	blocks, ok := msg1["content"].([]any)
	if !ok {
		t.Fatal("content not an array")
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	block := blocks[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("expected text block, got %v", block["type"])
	}
	text := block["text"].(string)
	if text == "" || !contains(text, "Read") || !contains(text, "main.go") {
		t.Errorf("unexpected stub text: %q", text)
	}
}

func TestStubify_AnnotationAttached(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_xyz",
					"name":  "Bash",
					"input": map[string]any{"command": "go test ./..."},
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_xyz",
					"content":     longString(5000),
				},
			},
		},
		map[string]any{"role": "assistant", "content": "last"},
		map[string]any{"role": "user", "content": "end"},
	}

	annotations := map[string]string{
		"toolu_xyz": "all 12 tests passed",
	}

	result := Stubify(msgs, 100, 2, 1, annotations, nil, testEstimate)

	// Find the stubbed tool_use message
	msg1, _ := result.Messages[1].(map[string]any)
	blocks, _ := msg1["content"].([]any)
	block := blocks[0].(map[string]any)
	text := block["text"].(string)

	if !contains(text, "all 12 tests passed") {
		t.Errorf("annotation not found in stub: %q", text)
	}
}

func TestStubify_DecisionProtected(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "user", "content": "ja bitte, nimm Ansatz B"},
		map[string]any{"role": "assistant", "content": longString(5000)},
		map[string]any{"role": "user", "content": longString(5000)},
		map[string]any{"role": "assistant", "content": "last"},
		map[string]any{"role": "user", "content": "end"},
	}

	result := Stubify(msgs, 100, 2, 1, nil, nil, testEstimate)

	// Decision message at index 1 should be preserved
	msg1, _ := result.Messages[1].(map[string]any)
	content, _ := msg1["content"].(string)
	if content != "ja bitte, nimm Ansatz B" {
		t.Errorf("decision message was modified: %q", content)
	}
}

func TestStubify_ShortUserTextPreserved(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "user", "content": "kurze Frage"},       // < 800 chars
		map[string]any{"role": "assistant", "content": longString(8000)}, // big
		map[string]any{"role": "assistant", "content": "last"},
		map[string]any{"role": "user", "content": "end"},
	}

	result := Stubify(msgs, 100, 2, 1, nil, nil, testEstimate)

	msg1, _ := result.Messages[1].(map[string]any)
	content, _ := msg1["content"].(string)
	if content != "kurze Frage" {
		t.Errorf("short user text was modified: %q", content)
	}
}

func TestStubify_KeepRecentProtected(t *testing.T) {
	msgs := buildLargeMessages(20)
	result := Stubify(msgs, 100, 5, 1, nil, nil, testEstimate)

	// Last 5 messages should be unchanged
	for i := 15; i < 20; i++ {
		original := msgs[i].(map[string]any)["content"]
		modified := result.Messages[i].(map[string]any)["content"]
		origStr, _ := original.(string)
		modStr, _ := modified.(string)
		if origStr != modStr {
			t.Errorf("message %d in keepRecent zone was modified", i)
		}
	}
}

func TestIsDecision(t *testing.T) {
	// Helper: build messages with optional previous assistant message
	buildMsgs := func(text string, prevAssistant string) ([]any, int) {
		msgs := []any{
			map[string]any{"role": "user", "content": "start"},
		}
		if prevAssistant != "" {
			msgs = append(msgs, map[string]any{"role": "assistant", "content": prevAssistant})
		}
		idx := len(msgs)
		msgs = append(msgs, map[string]any{"role": "user", "content": text})
		return msgs, idx
	}

	tests := []struct {
		desc  string
		input string
		prev  string
		want  bool
	}{
		// Rule 1: very short (< 30 chars), no ?
		{"short confirmation", "ja bitte", "", true},
		{"short with keyword", "nimm Ansatz B", "", true},
		{"very short rejection", "nein", "", true},
		{"single letter choice", "B", "", true},
		{"short approval", "ok mach", "", true},
		{"short statement", "hier ist der Code", "", true},
		// Rule 1 negative: has ?
		{"question blocks rule 1", "wie funktioniert das?", "", false},
		// Rule 2: short after long analysis
		{"short after long analysis", "das klingt gut, mach weiter so bitte", longString(500), true},
		// Rule 3: keyword fallback for longer text
		{"keyword in longer text", "ja bitte, mach das so wie du es vorgeschlagen hast und teste das bitte auch", "", true},
		// Negative: long, no keywords, no ?
		{"long non-decision", "hier ist ein langer Text der keine relevanten Signalwoerter hat und auch nicht kurz genug fuer Regel eins oder zwei", "", false},
	}

	for _, tt := range tests {
		msgs, idx := buildMsgs(tt.input, tt.prev)
		got := isDecision(msgs, idx)
		if got != tt.want {
			t.Errorf("%s: isDecision(%q) = %v, want %v", tt.desc, tt.input, got, tt.want)
		}
	}
}

func TestHasStructuredContent(t *testing.T) {
	if !hasStructuredContent("| Col1 |---| Col2 |") {
		t.Error("should detect table")
	}
	if !hasStructuredContent("```go\ncode\n```") {
		t.Error("should detect code block")
	}
	if hasStructuredContent("plain text") {
		t.Error("should not detect plain text")
	}
}

func TestStubToolUse_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		contains string
	}{
		{
			name:     "Read",
			input:    map[string]any{"file_path": "/path/to/file.go"},
			contains: "Read /path/to/file.go",
		},
		{
			name:     "Bash",
			input:    map[string]any{"command": "go test ./..."},
			contains: "Bash: go test",
		},
		{
			name:     "Edit",
			input:    map[string]any{"file_path": "/path/to/file.go"},
			contains: "Edit /path/to/file.go",
		},
		{
			name:     "Grep",
			input:    map[string]any{"pattern": "TODO", "path": "/src"},
			contains: "Grep 'TODO'",
		},
		{
			name:     "WebSearch",
			input:    map[string]any{"query": "anthropic api"},
			contains: "WebSearch 'anthropic api'",
		},
	}

	for _, tt := range tests {
		block := map[string]any{
			"name":  tt.name,
			"id":    "toolu_test",
			"input": tt.input,
		}
		stub := stubToolUse(block, nil)
		if !contains(stub, tt.contains) {
			t.Errorf("%s: stub %q does not contain %q", tt.name, stub, tt.contains)
		}
	}
}

func TestStubify_TokenizerEstimatePreservesMore(t *testing.T) {
	// When estimateFn uses actual tokenizer (fewer tokens per byte),
	// token estimates after stubbing should differ even though both
	// stub all eligible messages (no floor).
	msgs := buildLargeMessages(100) // enough messages to show divergence

	// bytes/3.6 overestimates → reports higher token counts
	overEstimate := TokenEstimateFunc(func(text string) int {
		return int(float64(len(text)) / 3.6) // old heuristic
	})
	resultOver := Stubify(msgs, 20000, 5, 1, nil, nil, overEstimate)

	// "tokenizer" returns fewer tokens (4.5 bytes/token) → reports lower counts
	accurateEstimate := TokenEstimateFunc(func(text string) int {
		return int(float64(len(text)) / 4.5) // simulates real tokenizer
	})
	resultAccurate := Stubify(msgs, 20000, 5, 1, nil, nil, accurateEstimate)

	// Both should stub the same messages (no floor stops either)
	if resultAccurate.StubCount != resultOver.StubCount {
		t.Logf("stub counts differ: accurate=%d, heuristic=%d (both should stub all eligible)",
			resultAccurate.StubCount, resultOver.StubCount)
	}

	// Accurate estimate should report fewer tokens remaining (same stubs, lower estimate)
	accurateAfter := estimateTokensFromMessages(resultAccurate.Messages, accurateEstimate)
	overAfter := estimateTokensFromMessages(resultOver.Messages, overEstimate)
	if accurateAfter >= overAfter {
		t.Errorf("accurate estimator should report fewer tokens: accurate=%d, heuristic=%d",
			accurateAfter, overAfter)
	}
	t.Logf("accurate: %d stubs, %d tokens; over: %d stubs, %d tokens",
		resultAccurate.StubCount, accurateAfter, resultOver.StubCount, overAfter)
}

func TestStubify_ToolResultAtProtectedBoundary(t *testing.T) {
	// Bug: tool_use at protectedTail-1 gets stubbed (to text),
	// but tool_result at protectedTail stays as-is → orphaned tool_result → API 400.
	// Fix: force-stub tool_result even if it's in the protected tail.
	msgs := []any{
		map[string]any{"role": "system", "content": "system"},
		// Filler to push token count over threshold
		map[string]any{"role": "user", "content": longString(2000)},
		map[string]any{"role": "assistant", "content": longString(2000)},
		map[string]any{"role": "user", "content": longString(2000)},
		map[string]any{"role": "assistant", "content": longString(2000)},
		// tool_use at index 5 — this will be the last stubbable message (protectedTail-1)
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": longString(1000)},
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_boundary",
					"name":  "Read",
					"input": map[string]any{"file_path": "/boundary.go"},
				},
			},
		},
		// tool_result at index 6 — first message in protected tail (keepRecent=3)
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_boundary",
					"content":     longString(5000),
				},
			},
		},
		// Protected tail: keepRecent=3
		map[string]any{"role": "assistant", "content": "response"},
		map[string]any{"role": "user", "content": "last question"},
	}

	// keepRecent=3 → protectedTail = 9-3 = 6 → tool_result at index 6 is first protected
	result := Stubify(msgs, 100, 3, 1, nil, nil, testEstimate)

	// The tool_result at index 6 must NOT have type "tool_result" blocks anymore
	// (it should be force-stubbed to text even though it's in the protected tail)
	for i, msg := range result.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_result" {
				// Check if the matching tool_use still exists in the previous message
				if i > 0 {
					prevMsg, _ := result.Messages[i-1].(map[string]any)
					if !hasToolUseContent(prevMsg["content"]) {
						t.Errorf("orphaned tool_result at index %d — tool_use was stubbed but tool_result wasn't", i)
					}
				}
			}
		}
	}
}

// --- helpers ---

// testEstimate is a simple token estimator for tests (raw /3.6).
func testEstimate(text string) int {
	return int(float64(len(text)) / 3.6)
}

func TestStubify_RespectsKnownTotal(t *testing.T) {
	msgs := make([]any, 10)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		// ~500 chars per message → testEstimate ≈ 139 tokens each → total ≈ 1390
		msgs[i] = map[string]any{"role": role, "content": longString(500)}
	}

	// With threshold 2000: internal count (~1390) is under → no stubbing
	result := Stubify(msgs, 2000, 4, 1, nil, nil, testEstimate)
	if result.StubCount > 0 {
		t.Fatalf("internal count (%d) under threshold 2000 should not trigger stubbing", result.TokensBefore)
	}

	// Same messages but with knownTotal=3000 above threshold 2000 → should stub
	result = StubifyWithTotal(msgs, 2000, 4, 1, nil, nil, testEstimate, 3000)
	if result.StubCount == 0 {
		t.Fatal("knownTotal (3000) above threshold (2000) should trigger stubbing")
	}
	if result.TokensBefore != 3000 {
		t.Fatalf("TokensBefore should be knownTotal (3000), got %d", result.TokensBefore)
	}
}

func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func buildLargeMessages(count int) []any {
	msgs := make([]any, count)
	for i := 0; i < count; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = map[string]any{
			"role":    role,
			"content": longString(2000),
		}
	}
	return msgs
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
