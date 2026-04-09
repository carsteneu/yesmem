package proxy

import (
	"strings"
	"sync"
	"testing"
)

func TestLastUserHasText_ToolResultOnly(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "Hallo"},
		map[string]any{"role": "assistant", "content": "Hi"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "ok"},
		}},
	}
	if lastUserHasText(messages) {
		t.Error("expected false for tool_result-only last user message")
	}
}

func TestLastUserHasText_RealUserMessage(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": "Was ist los?"},
	}
	if !lastUserHasText(messages) {
		t.Error("expected true for real user text message")
	}
}

func TestLastUserHasText_MixedContentWithText(t *testing.T) {
	messages := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "ok"},
			map[string]any{"type": "text", "text": "Schau mal hier"},
		}},
	}
	if !lastUserHasText(messages) {
		t.Error("expected true when user message has text block alongside tool_result")
	}
}

func TestBuildThinkReminder_Basic(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	// First call — base reminder only
	r := s.buildThinkReminder("thread-1", "", false)
	if !strings.Contains(r, "hybrid_search()") {
		t.Error("expected hybrid_search reminder")
	}
	if !strings.Contains(r, "check your memory first") {
		t.Error("expected Claude-style reminder text")
	}
	if !strings.Contains(r, "Do not repeat memory lookups") {
		t.Error("expected generic anti-loop guardrail")
	}
	if !strings.Contains(r, "prefer visible timestamps and current state before memory lookup") {
		t.Error("expected direct-status guardrail")
	}
	if !strings.Contains(r, "use it instead of starting a new memory lookup") {
		t.Error("expected existing-context guardrail")
	}
	if strings.Contains(r, "remember()") {
		t.Error("first call should NOT contain learning extraction reminder")
	}
}

func TestBuildThinkReminder_NonClaudeUsesSelectiveSearchText(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	r := s.buildThinkReminder("thread-1", "", true)
	if !strings.Contains(r, "decide whether prior memory is likely to matter") {
		t.Error("expected non-Claude reminder text")
	}
	if !strings.Contains(r, "follow with deep_search()") {
		t.Error("expected deep_search fallback in non-Claude reminder")
	}
	if strings.Contains(r, "check your memory first") {
		t.Error("non-Claude reminder should not use the Claude-style mandatory text")
	}
	if !strings.Contains(r, "Do not repeat memory lookups") {
		t.Error("expected generic anti-loop guardrail")
	}
	if !strings.Contains(r, "prefer visible timestamps and current state before memory lookup") {
		t.Error("expected direct-status guardrail")
	}
	if !strings.Contains(r, "use it instead of starting a new memory lookup") {
		t.Error("expected existing-context guardrail")
	}
}

func TestBuildThinkReminder_Every10th(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	// Call 9 times — none should have extraction reminder
	for i := 0; i < 9; i++ {
		r := s.buildThinkReminder("thread-1", "", false)
		if strings.Contains(r, "remember()") {
			t.Errorf("call %d should NOT contain extraction reminder", i+1)
		}
	}

	// 10th call — should have extraction reminder
	r := s.buildThinkReminder("thread-1", "", false)
	if !strings.Contains(r, "remember()") {
		t.Error("10th call should contain learning extraction reminder")
	}

	// 11th — back to base
	r = s.buildThinkReminder("thread-1", "", false)
	if strings.Contains(r, "remember()") {
		t.Error("11th call should NOT contain extraction reminder")
	}
}

func TestBuildThinkReminder_PerThread(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	// Thread A: 10 calls
	for i := 0; i < 10; i++ {
		s.buildThinkReminder("thread-A", "", false)
	}

	// Thread B: 1 call — should not be at 10th
	r := s.buildThinkReminder("thread-B", "", false)
	if strings.Contains(r, "remember()") {
		t.Error("thread-B call 1 should NOT be affected by thread-A counter")
	}
}

func TestBuildThinkReminder_EmptyThread(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	r := s.buildThinkReminder("", "", false)
	if r != "" {
		t.Error("empty threadID should return empty string")
	}
}

func TestBuildThinkReminder_SystemReminderWrapping(t *testing.T) {
	s := &Server{thinkCounters: make(map[string]int)}

	r := s.buildThinkReminder("thread-1", "", false)
	if !strings.HasPrefix(r, "<system-reminder>") {
		t.Error("reminder should be wrapped in system-reminder tags")
	}
	if !strings.HasSuffix(r, "</system-reminder>") {
		t.Error("reminder should end with closing system-reminder tag")
	}
}

func TestChannelMarkRead_TurnBased(t *testing.T) {
	s := &Server{
		channelMu:          sync.Mutex{},
		channelInjectCount: make(map[string]int),
	}

	sid := "test-session-abc"
	dr := dialogResult{
		Extra:     "test message",
		SessionID: sid,
		HasUnread: true,
	}

	// Turn 1: first injection — should NOT mark as read yet
	shouldMark := s.shouldMarkChannelRead(dr)
	if shouldMark {
		t.Error("turn 1: should NOT mark as read yet")
	}

	// Turn 2: second injection — NOW mark as read
	shouldMark = s.shouldMarkChannelRead(dr)
	if !shouldMark {
		t.Error("turn 2: should mark as read after 2 injections")
	}

	// After marking, counter should be reset
	count := s.channelInjectCount[sid]
	if count != 0 {
		t.Errorf("counter should be reset to 0 after mark, got %d", count)
	}
}

func TestChannelMarkRead_NoUnread(t *testing.T) {
	s := &Server{
		channelMu:          sync.Mutex{},
		channelInjectCount: make(map[string]int),
	}

	dr := dialogResult{
		Extra:     "",
		SessionID: "test-session",
		HasUnread: false,
	}

	shouldMark := s.shouldMarkChannelRead(dr)
	if shouldMark {
		t.Error("should NOT mark as read when no unread messages")
	}
}

func TestBuildChannelDirective_NoACK(t *testing.T) {
	directive := buildChannelDirective("session-abc-123", "command")
	if strings.Contains(directive, "✓ Erhalten") {
		t.Error("DIREKTIVE must NOT contain ACK instruction — causes infinite ACK loops")
	}
	if strings.Contains(directive, "Wenn erledigt") {
		t.Error("DIREKTIVE must NOT contain 'Wenn erledigt' ACK instruction")
	}
	if !strings.Contains(directive, "DIREKTIVE") {
		t.Error("expected DIREKTIVE header")
	}
	if !strings.Contains(directive, "session-abc-123") {
		t.Error("expected lastSender in directive")
	}
}

func TestBuildChannelDirective_Command(t *testing.T) {
	d := buildChannelDirective("sender-1", "command")
	if !strings.Contains(d, "send_to") {
		t.Error("command directive should include send_to instruction")
	}
	if !strings.Contains(d, "sender-1") {
		t.Error("command directive should include sender ID")
	}
}

func TestBuildChannelDirective_Response(t *testing.T) {
	d := buildChannelDirective("sender-2", "response")
	if !strings.Contains(d, "open questions") || !strings.Contains(d, "NOT") {
		t.Error("response directive should indicate only reply if questions remain, otherwise do NOT reply")
	}
}

func TestBuildChannelDirective_Ack(t *testing.T) {
	d := buildChannelDirective("sender-3", "ack")
	if !strings.Contains(d, "Do NOT reply") {
		t.Error("ack directive should tell agent NOT to reply")
	}
}

func TestBuildChannelDirective_Status(t *testing.T) {
	d := buildChannelDirective("sender-4", "status")
	if !strings.Contains(d, "Do NOT reply") {
		t.Error("status directive should tell agent NOT to reply")
	}
}

func TestBuildChannelDirective_Empty_DefaultsToCommand(t *testing.T) {
	d := buildChannelDirective("sender-5", "")
	if !strings.Contains(d, "send_to") {
		t.Error("empty msg_type should behave like command")
	}
}

func TestChannelMarkRead_PerSession(t *testing.T) {
	s := &Server{
		channelMu:          sync.Mutex{},
		channelInjectCount: make(map[string]int),
	}

	drA := dialogResult{Extra: "msg", SessionID: "session-A", HasUnread: true}
	drB := dialogResult{Extra: "msg", SessionID: "session-B", HasUnread: true}

	// Session A: 1 turn
	s.shouldMarkChannelRead(drA)

	// Session B: 1 turn — should not be affected by A
	shouldMark := s.shouldMarkChannelRead(drB)
	if shouldMark {
		t.Error("session-B should not be affected by session-A counter")
	}

	// Session A: 2nd turn — should mark
	shouldMark = s.shouldMarkChannelRead(drA)
	if !shouldMark {
		t.Error("session-A turn 2 should mark as read")
	}
}
