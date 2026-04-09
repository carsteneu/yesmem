package proxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/hints"
)

// lastUserHasText returns true when the last user message contains actual text content
// (not just tool_result blocks). Used to skip think-reminder injection on tool continuations.
func lastUserHasText(messages []any) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return c != ""
		case []any:
			for _, block := range c {
				b, ok := block.(map[string]any)
				if ok && b["type"] == "text" {
					return true
				}
			}
			return false
		}
		return false
	}
	return false
}

// buildThinkReminder increments the per-thread request counter and returns
// the appropriate reminder text. Returns empty string for subagent threads.
func (s *Server) buildThinkReminder(threadID string, sessionID string, nonClaude bool) string {
	if threadID == "" {
		return ""
	}

	s.thinkMu.Lock()
	s.thinkCounters[threadID]++
	count := s.thinkCounters[threadID]
	s.thinkMu.Unlock()

	reminder := "<system-reminder>\n"
	if nonClaude {
		reminder += "Before acting, decide whether prior memory is likely to matter; if yes, do one focused lookup with hybrid_search(), and if that returns nothing useful but the question still depends on past context, follow with deep_search(); otherwise proceed directly.\n"
	} else {
		reminder += "Before acting — do you already know something about this? Use hybrid_search() to check your memory first.\n"
	}
	reminder += "Do not repeat memory lookups for the same question unless a new concrete hypothesis appears.\n"
	reminder += "For direct status, timing, or \"what just happened\" questions, prefer visible timestamps and current state before memory lookup.\n"
	reminder += "If relevant prior context is already present in the conversation, use it instead of starting a new memory lookup.\n"
	reminder += hints.NextTimestampHint()

	if count%10 == 0 {
		reminder += "\nCheck if the last exchanges contain new learnings (gotcha, decision, pattern, preference) worth saving. You have full context — nuances that post-hoc extraction misses. If yes: remember(). If no: do nothing."
	}

	reminder += "\n</system-reminder>"
	return reminder
}

// buildChannelDirective returns the DIREKTIVE text injected after channel messages.
// Intentionally does NOT include an ACK instruction — sending automatic ACKs caused
// infinite ACK loops (86 spurious messages observed in production).
// msgType controls the reply behaviour: ack/status suppress replies entirely,
// response only replies when open questions remain, command (default) replies normally.
func buildChannelDirective(lastSender, msgType string) string {
	if msgType == "" {
		msgType = "command"
	}
	switch msgType {
	case "ack", "status":
		return "\n\nDIRECTIVE: Message is an acknowledgement/status update. Do NOT reply — no action needed."
	case "response":
		return fmt.Sprintf("\n\nDIRECTIVE: Result/response received."+
			"\n1. Process the response."+
			"\n2. Only reply if open questions remain: send_to(target='%s', content='...', msg_type='command')"+
			"\n3. If no open questions: continue working, do NOT reply with ack.", lastSender)
	default: // "command"
		return fmt.Sprintf("\n\nDIREKTIVE: Die Nachrichten stehen DIREKT OBEN in diesem Block — NICHT in der Datenbank suchen."+
			"\n1. Verarbeite die Nachrichten die du gerade gelesen hast."+
			"\n2. Zum Antworten: send_to(target='%s', content='...', msg_type='response')", lastSender)
	}
}

// dialogResult holds channel check output with metadata for deferred mark_read.
type dialogResult struct {
	Extra     string // Content to inject (empty = nothing to inject)
	SessionID string // For mark_channel_read after successful injection
	HasUnread bool   // Whether there were unread messages to mark
}

// checkDialogMessages uses threadID directly as session_id (they are the same now),
// then checks for channel messages. Caller must call markDialogRead() after successful injection.
func (s *Server) checkDialogMessages(threadID, project string) dialogResult {
	sessionID := threadID // threadID IS the session_id now
	if sessionID == "" {
		return dialogResult{}
	}
	if s.logger != nil {
		s.logger.Printf("[channel] sid=%s", sessionID[:8])
	}

	var extra string
	var hasUnread bool

	// Check channel messages (target-based, no dialog state)
	if msgResult, err := s.queryDaemon("check_channel", map[string]any{"session_id": sessionID}); err == nil {
		var msgs struct {
			Messages []struct {
				Sender  string `json:"sender"`
				Content string `json:"content"`
				MsgType string `json:"msg_type"`
			} `json:"messages"`
		}
		if json.Unmarshal(msgResult, &msgs) == nil && len(msgs.Messages) > 0 {
			hasUnread = true
			var lastSender, lastMsgType string
			for _, m := range msgs.Messages {
				lastSender = m.Sender
				lastMsgType = m.MsgType
				content := strings.ReplaceAll(m.Content, "\\n", "\n")
				extra += fmt.Sprintf("\n📨 Von %s [%s]:\n%s", m.Sender, m.MsgType, content)
			}
			extra += buildChannelDirective(lastSender, lastMsgType)
		}
	}

	// Check broadcasts (keep existing)
	if bcResult, err := s.queryDaemon("check_broadcasts", map[string]any{"session_id": sessionID, "project": project}); err == nil {
		var bcs struct {
			Messages []struct {
				SenderShort string `json:"sender_short"`
				Content     string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(bcResult, &bcs) == nil && len(bcs.Messages) > 0 {
			extra += "\n\n📢 BROADCAST:"
			for _, m := range bcs.Messages {
				content := m.Content
				if len(content) > 200 {
					content = content[:200] + "…"
				}
				extra += fmt.Sprintf("\n   [%s]: %s", m.SenderShort, content)
			}
		}
	}

	return dialogResult{
		Extra:     extra,
		SessionID: sessionID,
		HasUnread: hasUnread,
	}
}

// markDialogRead marks channel messages as read after successful injection.
func (s *Server) markDialogRead(dr dialogResult) {
	if !dr.HasUnread || dr.SessionID == "" {
		return
	}
	s.queryDaemon("mark_channel_read", map[string]any{
		"session_id": dr.SessionID,
	})
}

// shouldMarkChannelRead implements turn-based mark-read logic.
// Returns true after 2 injections for a session, then resets the counter.
func (s *Server) shouldMarkChannelRead(dr dialogResult) bool {
	if !dr.HasUnread || dr.SessionID == "" {
		return false
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	s.channelInjectCount[dr.SessionID]++
	if s.channelInjectCount[dr.SessionID] >= 2 {
		delete(s.channelInjectCount, dr.SessionID)
		return true
	}
	return false
}
