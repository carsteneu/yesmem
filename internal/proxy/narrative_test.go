package proxy

import (
	"strings"
	"testing"
)

func TestNarrative_SetPivotMoments(t *testing.T) {
	n := NewNarrative()
	n.requestCount = 1 // make Render() produce output

	n.SetPivotMoments([]string{"user asked about memory", "key insight about decay"})

	rendered := n.Render()
	if !strings.Contains(rendered, "Pivot-Moments:") {
		t.Error("rendered should contain Pivot-Moments section")
	}
	if !strings.Contains(rendered, "user asked about memory") {
		t.Error("rendered should contain first pivot moment")
	}
}

func TestNarrative_AddArchivedTopic(t *testing.T) {
	n := NewNarrative()
	n.requestCount = 1

	n.AddArchivedTopic(ArchivedTopic{
		Label:      "proxy.go, stubify.go",
		MsgCount:   5,
		ReqRange:   "bis Req 30",
		SearchHint: "proxy.go stubify.go",
	})

	rendered := n.Render()
	if !strings.Contains(rendered, "Archived topics") {
		t.Error("rendered should contain Archived topics section")
	}
	if !strings.Contains(rendered, "proxy.go, stubify.go") {
		t.Error("rendered should contain topic label")
	}
	if !strings.Contains(rendered, "deep_search('proxy.go stubify.go')") {
		t.Error("rendered should contain search hint")
	}
}

func TestNarrative_AddArchivedTopic_Deduplicates(t *testing.T) {
	n := NewNarrative()

	n.AddArchivedTopic(ArchivedTopic{Label: "same", MsgCount: 1})
	n.AddArchivedTopic(ArchivedTopic{Label: "same", MsgCount: 2})
	n.AddArchivedTopic(ArchivedTopic{Label: "different", MsgCount: 3})

	if len(n.archivedTopics) != 2 {
		t.Errorf("expected 2 topics (deduplicated), got %d", len(n.archivedTopics))
	}
}

func TestNarrative_AddArchivedTopic_MaxTen(t *testing.T) {
	n := NewNarrative()
	for i := 0; i < 15; i++ {
		n.AddArchivedTopic(ArchivedTopic{Label: longString(10) + string(rune('a'+i)), MsgCount: i})
	}
	if len(n.archivedTopics) != 10 {
		t.Errorf("expected max 10 topics, got %d", len(n.archivedTopics))
	}
}

func TestInjectNarrative(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "system"},
		map[string]any{"role": "assistant", "content": "response"},
		map[string]any{"role": "user", "content": "question"},
	}

	result := InjectNarrative(msgs, "Session-Kontext: test")
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}

	// Narrative at index 2 (after first safe assistant→user boundary)
	narr, _ := result[2].(map[string]any)
	if narr["role"] != "user" {
		t.Error("narrative should be user role")
	}

	// Ack at index 3
	ack, _ := result[3].(map[string]any)
	if ack["role"] != "assistant" {
		t.Error("ack should be assistant role")
	}

	// Original last message preserved
	last, _ := result[4].(map[string]any)
	if last["content"] != "question" {
		t.Error("original messages should be preserved")
	}
}

func TestInjectNarrative_SkipsToolPairing(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_abc", "name": "Read"},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_abc", "content": "file content"},
			},
		},
		map[string]any{"role": "assistant", "content": "I read the file"},
		map[string]any{"role": "user", "content": "thanks"},
	}

	result := InjectNarrative(msgs, "Session-Kontext: test")
	if len(result) != 7 {
		t.Fatalf("expected 7 messages, got %d", len(result))
	}

	// Verify tool_use/tool_result pairing is intact
	msg1, _ := result[1].(map[string]any)
	msg2, _ := result[2].(map[string]any)
	if !hasToolUse(msg1) {
		t.Error("message 1 should still be tool_use")
	}
	if !hasToolResult(msg2) {
		t.Error("message 2 should still be tool_result")
	}
}

func TestNarrative_Update_ExtractsGoal(t *testing.T) {
	n := NewNarrative()
	msgs := []any{
		map[string]any{"role": "user", "content": "Build a proxy server for API requests."},
		map[string]any{"role": "assistant", "content": "Sure, let me help."},
	}

	n.Update(msgs)

	if n.goal == "" {
		t.Error("goal should be extracted from first user message")
	}
	if !strings.Contains(n.goal, "proxy server") {
		t.Errorf("goal should contain 'proxy server', got: %q", n.goal)
	}
}

// TestNarrative_PerThreadIsolation guards the cross-project narrative leak: a
// single proxy-global narrative let one thread's goal/decisions appear in every
// other thread's briefing. Each thread must own an independent Narrative.
func TestNarrative_PerThreadIsolation(t *testing.T) {
	s := &Server{}

	n1 := s.getNarrative("thread-1")
	n1.Update([]any{map[string]any{"role": "user", "content": "Build the alpha parser."}})
	n2 := s.getNarrative("thread-2")
	n2.Update([]any{map[string]any{"role": "user", "content": "Fix the beta exporter."}})

	if n1 == n2 {
		t.Fatal("distinct threads must not share one narrative instance")
	}
	r1 := n1.Render()
	r2 := n2.Render()
	if !strings.Contains(r1, "alpha parser") {
		t.Errorf("thread-1 narrative missing its own goal: %q", r1)
	}
	if strings.Contains(r1, "beta exporter") {
		t.Errorf("thread-1 narrative leaked thread-2 goal: %q", r1)
	}
	if !strings.Contains(r2, "beta exporter") {
		t.Errorf("thread-2 narrative missing its own goal: %q", r2)
	}
	if strings.Contains(r2, "alpha parser") {
		t.Errorf("thread-2 narrative leaked thread-1 goal: %q", r2)
	}
}

// TestNarrative_GetNarrativeStablePerThread verifies getNarrative returns the
// same instance for repeated calls with the same thread id.
func TestNarrative_GetNarrativeStablePerThread(t *testing.T) {
	s := &Server{}
	if s.getNarrative("tid") != s.getNarrative("tid") {
		t.Error("getNarrative must return the same instance for the same thread")
	}
}

// TestComposeBriefingText_UsesThreadNarrative verifies the briefing composition
// pulls the requesting thread's narrative, never a foreign thread's.
func TestComposeBriefingText_UsesThreadNarrative(t *testing.T) {
	s := &Server{}
	s.getNarrative("thread-a").Update([]any{map[string]any{"role": "user", "content": "alpha work."}})
	s.getNarrative("thread-b").Update([]any{map[string]any{"role": "user", "content": "beta work."}})

	gotA := composeBriefingText("BASE", s.getNarrative("thread-a"))
	if !strings.Contains(gotA, "alpha work") {
		t.Errorf("composed briefing missing own narrative: %q", gotA)
	}
	if strings.Contains(gotA, "beta work") {
		t.Errorf("composed briefing leaked foreign narrative: %q", gotA)
	}
}

func TestIsProtected(t *testing.T) {
	pivots := []string{
		"User fragte wie die messages verwaltet werden und das führte zur Proxy-Idee",
		"Einsicht dass Progressive Decay mit Pivot-Schutz zusammen funktioniert",
	}

	tests := []struct {
		text string
		want bool
	}{
		// Should match: shares "messages", "verwaltet", "proxy" with pivot 1
		{"Die messages werden über einen Proxy verwaltet und weitergeleitet", true},
		// Should not match: only 1-2 overlapping words
		{"hello world", false},
		// Should match: shares "progressive", "decay", "pivot" with pivot 2
		{"Progressive Decay mit Pivot-Schutz ist der richtige Ansatz", true},
	}

	for _, tt := range tests {
		got := isProtected(tt.text, pivots)
		if got != tt.want {
			t.Errorf("isProtected(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestIsProtected_EmptyPivots(t *testing.T) {
	if isProtected("any text", nil) {
		t.Error("should return false with nil pivots")
	}
	if isProtected("any text", []string{}) {
		t.Error("should return false with empty pivots")
	}
}

func TestStripOldNarratives_RemovesPairs(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": "hello"},
		// Old narrative pair
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "Session-Kontext (auto-generiert, Request 5):\nZiel: test"},
		}},
		map[string]any{"role": "assistant", "content": "Understood. Continuing with this context."},
		// Real conversation continues
		map[string]any{"role": "user", "content": "what about X?"},
		map[string]any{"role": "assistant", "content": "X is..."},
	}

	result := StripOldNarratives(messages)
	if len(result) != 4 {
		t.Fatalf("expected 4 messages after strip, got %d", len(result))
	}
	// Verify no narrative remains
	for _, m := range result {
		mm := m.(map[string]any)
		text := extractMessageText(mm)
		if strings.Contains(text, "Session-Kontext (auto-generiert") {
			t.Error("narrative should be stripped")
		}
		if c, _ := mm["content"].(string); c == "Understood. Continuing with this context." {
			t.Error("ack should be stripped")
		}
	}
}

func TestStripOldNarratives_MultipleNarratives(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": "hello"},
		// First narrative pair
		map[string]any{"role": "user", "content": "Session-Kontext (auto-generiert, Request 3):\nZiel: old"},
		map[string]any{"role": "assistant", "content": "Understood. Continuing with this context."},
		// Second narrative pair
		map[string]any{"role": "user", "content": "Session-Kontext (auto-generiert, Request 10):\nZiel: newer"},
		map[string]any{"role": "assistant", "content": "Understood. Continuing with this context."},
		map[string]any{"role": "user", "content": "real question"},
	}

	result := StripOldNarratives(messages)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages after strip, got %d", len(result))
	}
}

func TestStripOldNarratives_NoNarratives(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": "hello"},
	}

	result := StripOldNarratives(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (unchanged), got %d", len(result))
	}
}

func TestStripOldNarratives_NarrativeWithoutAck(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "hi"},
		// Narrative without matching ack (edge case)
		map[string]any{"role": "user", "content": "Session-Kontext (auto-generiert, Request 5):\nZiel: test"},
		map[string]any{"role": "user", "content": "real follow-up"},
	}

	result := StripOldNarratives(messages)
	// Narrative removed, next user message kept (not an ack)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestRender_PivotsOnlyEvery10Requests(t *testing.T) {
	n := NewNarrative()
	n.mu.Lock()
	n.requestCount = 5
	n.goal = "test"
	n.pivotMoments = []string{"pivot1", "pivot2"}
	n.mu.Unlock()

	text := n.Render()
	if strings.Contains(text, "pivot1") {
		t.Error("pivots should NOT be in render at request 5")
	}

	// Request 10 — should include pivots
	n.mu.Lock()
	n.requestCount = 10
	n.mu.Unlock()

	text = n.Render()
	if !strings.Contains(text, "pivot1") {
		t.Error("pivots SHOULD be in render at request 10")
	}

	// Request 2 — first 3 always include pivots
	n.mu.Lock()
	n.requestCount = 2
	n.mu.Unlock()

	text = n.Render()
	if !strings.Contains(text, "pivot1") {
		t.Error("pivots SHOULD be in render at request 2 (first 3)")
	}
}

func TestRender_FiltersSystemReminderDecisions(t *testing.T) {
	n := NewNarrative()
	n.mu.Lock()
	n.requestCount = 5
	n.goal = "test"
	n.decisions = []string{
		"echte Entscheidung: Narrative in System-Block",
		"<system-reminder>\nThe task tools haven't been used...",
		"weitere echte Entscheidung",
	}
	n.mu.Unlock()

	text := n.Render()
	if strings.Contains(text, "task tools") {
		t.Error("system-reminder decisions should be filtered")
	}
	if !strings.Contains(text, "echte Entscheidung") {
		t.Error("real decisions should remain")
	}
	if !strings.Contains(text, "weitere echte") {
		t.Error("second real decision should remain")
	}
}

func TestRender_AllSystemReminderDecisionsFiltered(t *testing.T) {
	n := NewNarrative()
	n.mu.Lock()
	n.requestCount = 5
	n.goal = "test"
	n.decisions = []string{
		"<system-reminder>foo</system-reminder>",
		"system-reminder noise",
	}
	n.mu.Unlock()

	text := n.Render()
	if strings.Contains(text, "Key Decisions:") {
		t.Error("Key Decisions section should not appear when all decisions are filtered")
	}
}

// TestIsNarrativeMessage_DoesNotMatchUserContentWithNarrativeSubstring guards
// against the content-loss bug where a user message with narrative text as a
// trailing fragment (e.g. from the now-removed WS1 tail-inject experiment, or
// from any future accidental reintroduction) is fully stripped by
// StripOldNarratives, wiping the user's actual input.
func TestIsNarrativeMessage_DoesNotMatchUserContentWithNarrativeSubstring(t *testing.T) {
	m := map[string]any{
		"role":    "user",
		"content": "hi how are you?\n\n<system-reminder>[yesmem-narrative]\nSession-Kontext (auto-generiert, Request 5):\nZiel: test\n</system-reminder>",
	}
	if isNarrativeMessage(m) {
		t.Error("isNarrativeMessage must NOT match when narrative substring is embedded in real user content — that would cause StripOldNarratives to wipe the user message")
	}
}

// TestIsNarrativeMessage_MatchesLegacyNarrativeOnlyMessage keeps the defensive
// cleanup path alive for legacy sessions where narrative was injected as a
// standalone user-role message whose content starts with the narrative marker.
func TestIsNarrativeMessage_MatchesLegacyNarrativeOnlyMessage(t *testing.T) {
	m := map[string]any{
		"role":    "user",
		"content": "Session-Kontext (auto-generiert, Request 5):\nZiel: test\n",
	}
	if !isNarrativeMessage(m) {
		t.Error("isNarrativeMessage must still match legacy narrative-only user messages so StripOldNarratives can clean them")
	}
}

// TestIsNarrativeMessage_MatchesLegacyNarrativeWithLeadingWhitespace covers the
// case where the legacy narrative-only content starts with whitespace/newlines
// before the marker — HasPrefix on trimmed text handles this.
func TestIsNarrativeMessage_MatchesLegacyNarrativeWithLeadingWhitespace(t *testing.T) {
	m := map[string]any{
		"role":    "user",
		"content": "\n\n  Session-Kontext (auto-generiert, Request 5):\nZiel: test\n",
	}
	if !isNarrativeMessage(m) {
		t.Error("isNarrativeMessage must match narrative-only content even with leading whitespace")
	}
}
