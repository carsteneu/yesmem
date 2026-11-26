package proxy

import (
	"encoding/json"
	"fmt"

	"github.com/carsteneu/yesmem/internal/channeltext"
	"github.com/carsteneu/yesmem/internal/hints"
)

// lastUserHasText returns true when the last user message contains actual text content
// (not just tool_result blocks). Used to skip think-reminder injection on tool continuations.
func lastUserHasText(messages []any) bool {
	return lastUserTextLen(messages) > 0
}

// lastUserTextLen returns the total text length of the last user message.
// Returns 0 if no user text message found.
func lastUserTextLen(messages []any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok || m["role"] != "user" {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			return len(c)
		case []any:
			total := 0
			for _, block := range c {
				b, ok := block.(map[string]any)
				if ok && b["type"] == "text" {
					if t, ok := b["text"].(string); ok {
						total += len(t)
					}
				}
			}
			return total
		}
		return 0
	}
	return 0
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
		reminder += "Consider checking your memory (hybrid_search) if you're unsure or need context.\n"
	} else {
		reminder += "Before acting — do you already know something about this? Use hybrid_search() to check your memory first.\n"
		reminder += "Do not repeat memory lookups for the same question unless a new concrete hypothesis appears.\n"
		reminder += "For direct status, timing, or \"what just happened\" questions, prefer visible timestamps and current state before memory lookup.\n"
		reminder += "Conversation context supplements but does not replace memory — search when acting on assumptions about external behavior (settings, APIs, config, conventions). Skip lookups only for pure status questions about the current conversation.\n"
	}
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
//
// The reply target is named as a placeholder, never as the address: the sender
// is caller-supplied, and a caller-supplied string must not become part of an
// instruction that another agent executes. The daemon's own channel text
// (send_opencode.go channelPromptDirective) makes the same choice — a quote in
// an interpolated label is enough to close the target and open a new
// instruction. The header line above the directive still names the sender,
// flattened and bounded.
func buildChannelDirective(msgType string) string {
	switch msgType {
	case "ack", "status":
		return "\n\nDIRECTIVE: Message is an acknowledgement/status update. Do NOT reply — no action needed."
	case "response":
		return "\n\nDIRECTIVE: Result/response received." +
			"\n1. Process the response." +
			"\n2. Only reply if open questions remain: send_to(target='<the sender of the message above>', content='...', msg_type='command')" +
			"\n3. If no open questions: continue working, do NOT reply with ack."
	default: // "command", and anything the caller invented
		return "\n\nDIREKTIVE: Die Nachrichten stehen DIREKT OBEN in diesem Block — NICHT in der Datenbank suchen." +
			"\n1. Verarbeite die Nachrichten die du gerade gelesen hast." +
			"\n2. Zum Antworten: send_to(target='<Absender der Nachricht oben>', content='...', msg_type='response')"
	}
}

// dialogResult holds channel check output with metadata for deferred mark_read.
type dialogResult struct {
	Extra     string // Content to inject (empty = nothing to inject)
	SessionID string // For mark_channel_read after successful injection
	HasUnread bool   // Whether there were unread messages to mark
}

// channelMessageLine renders one channel message for injection, the way the
// receiving agent sees it. The header carries the sender and the message type
// because the metadata of an injected turn is not rendered by every client.
// Sender and type are caller-supplied and go through channeltext for the
// header; the body goes through channeltext as well, which opens escaped
// newlines but keeps the body from adding a header line of its own.
func channelMessageLine(sender, msgType, content string) string {
	return fmt.Sprintf("\n📨 Von %s [%s]:\n%s", channeltext.SenderLabel(sender), channeltext.MessageType(msgType), channeltext.Body(content))
}

// broadcastEntry is one broadcast as the daemon hands it to the proxy.
type broadcastEntry struct {
	SenderShort string `json:"sender_short"`
	Content     string `json:"content"`
}

// broadcastLines renders the broadcast block of an injected turn. The content
// goes through channeltext.Neutralise: a broadcast lands in the same turn as a
// channel message, so it must not be able to open a header line of its own —
// but unlike a channel body it is rendered as written, so escaped sequences are
// left alone. The sender label is bounded and flattened, the content is cut at
// a readable width.
func broadcastLines(msgs []broadcastEntry) string {
	out := "\n\n📢 BROADCAST:"
	for _, m := range msgs {
		content := m.Content
		if len(content) > 200 {
			content = content[:200] + "…"
		}
		out += fmt.Sprintf("\n   [%s]: %s", channeltext.SenderLabel(m.SenderShort), channeltext.Neutralise(content))
	}
	return out
}

// checkDialogMessages uses threadID directly as session_id (they are the same now),
// then checks for channel messages. Caller must call markDialogRead() after successful injection.
func (s *Server) checkDialogMessages(threadID, project string) dialogResult {
	sessionID := threadID // threadID IS the session_id now
	if sessionID == "" {
		return dialogResult{}
	}
	if s.logger != nil {
		// sessionID is client text (a body-carried metadata.user_id reaches this
		// unchanged), so the preview goes through journalToken like every other
		// id in a journal line.
		s.logger.Printf("[channel] sid=%.8s", journalToken(sessionID))
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
			var lastMsgType string
			for _, m := range msgs.Messages {
				lastMsgType = m.MsgType
				extra += channelMessageLine(m.Sender, m.MsgType, m.Content)
			}
			extra += buildChannelDirective(lastMsgType)
		}
	}

	// Check broadcasts (keep existing)
	if bcResult, err := s.queryDaemon("check_broadcasts", map[string]any{"session_id": sessionID, "project": project}); err == nil {
		var bcs struct {
			Messages []broadcastEntry `json:"messages"`
		}
		if json.Unmarshal(bcResult, &bcs) == nil && len(bcs.Messages) > 0 {
			extra += broadcastLines(bcs.Messages)
		}
	}

	return dialogResult{
		Extra:     extra,
		SessionID: sessionID,
		HasUnread: hasUnread,
	}
}

// loadChannelContent returns the channel messages and broadcasts a session has
// not seen. channelLoader is a test-only seam; production runs the same
// check_channel/check_broadcasts lookup the Anthropic path performs in
// checkDialogMessages (proxy.go, dialog injection).
func (s *Server) loadChannelContent(threadID, project string) dialogResult {
	if s.channelLoader != nil {
		return s.channelLoader(threadID, project)
	}
	return s.checkDialogMessages(threadID, project)
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
