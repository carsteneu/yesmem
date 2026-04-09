package daemon

import (
	"encoding/json"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

func TestHandleSendTo_WithMsgType(t *testing.T) {
	h, s := mustHandler(t)
	resp := h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target":   "target-session-1",
			"content":  "task done",
			"sender":   "sender-session-1",
			"msg_type": "response",
		},
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	msgs, err := s.GetChannelMessages("target-session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].MsgType != "response" {
		t.Errorf("msg_type=%q want response", msgs[0].MsgType)
	}
}

func TestHandleSendTo_DefaultMsgType(t *testing.T) {
	h, s := mustHandler(t)
	resp := h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target":  "target-session-2",
			"content": "do this task",
			"sender":  "sender-session-2",
		},
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	msgs, err := s.GetChannelMessages("target-session-2")
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].MsgType != "command" {
		t.Errorf("msg_type=%q want command (default)", msgs[0].MsgType)
	}
}

func TestHandleSendTo_AckOnAckDropped(t *testing.T) {
	h, _ := mustHandler(t)

	// First: A sends ack to B
	h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target": "session-B", "content": "ok",
			"sender": "session-A", "msg_type": "ack",
		},
	})

	// B sends ack back to A → ACK-on-ACK, should be dropped
	resp := h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target": "session-A", "content": "acknowledged",
			"sender": "session-B", "msg_type": "ack",
		},
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	if result["ack_dropped"] != true {
		t.Error("expected ack_dropped=true for ACK-on-ACK")
	}
}

// --- resolveSessionID ---

func TestResolveSessionID_DirectParam(t *testing.T) {
	h, _ := mustHandler(t)
	got := h.resolveSessionID(map[string]any{"session_id": "direct-123"}, "session_id")
	if got != "direct-123" {
		t.Errorf("got %q, want direct-123", got)
	}
}

func TestResolveSessionID_FallbackSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	got := h.resolveSessionID(map[string]any{"_session_id": "fallback-456"}, "missing_key")
	if got != "fallback-456" {
		t.Errorf("got %q, want fallback-456", got)
	}
}

func TestResolveSessionID_PIDLookup(t *testing.T) {
	h, _ := mustHandler(t)
	h.pidMapMu.Lock()
	h.pidMap["pid-session-789"] = 42
	h.pidMapMu.Unlock()

	got := h.resolveSessionID(map[string]any{"_caller_pid": float64(42)}, "missing")
	if got != "pid-session-789" {
		t.Errorf("got %q, want pid-session-789", got)
	}
}

func TestResolveSessionID_ActiveSessionFallback(t *testing.T) {
	h, _ := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "active-fallback"
	h.activeSessionMu.Unlock()

	got := h.resolveSessionID(map[string]any{}, "missing")
	if got != "active-fallback" {
		t.Errorf("got %q, want active-fallback", got)
	}
}

// --- handleStartDialog ---

func TestHandleStartDialog_OK(t *testing.T) {
	h, _ := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "initiator-1"
	h.activeSessionMu.Unlock()

	resp := h.handleStartDialog(map[string]any{"partner": "partner-1", "topic": "test topic"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "pending" {
		t.Errorf("status=%v, want pending", m["status"])
	}
	if m["dialog_id"] == nil || m["dialog_id"].(float64) == 0 {
		t.Error("expected non-zero dialog_id")
	}
}

func TestHandleStartDialog_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleStartDialog(map[string]any{"partner": "p"})
	if resp.Error == "" {
		t.Error("expected error for missing initiator")
	}

	h.activeSessionMu.Lock()
	h.activeSessionID = "init-1"
	h.activeSessionMu.Unlock()
	resp = h.handleStartDialog(map[string]any{})
	if resp.Error == "" {
		t.Error("expected error for missing partner")
	}
}

// --- handleSendTo additional cases ---

func TestHandleSendTo_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "send_to", Params: map[string]any{"target": "t"}})
	if resp.Error == "" {
		t.Error("expected error for missing content")
	}
	resp = h.Handle(Request{Method: "send_to", Params: map[string]any{"content": "c"}})
	if resp.Error == "" {
		t.Error("expected error for missing target")
	}
}

func TestHandleSendTo_SenderFallbackToResolve(t *testing.T) {
	h, s := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "resolved-sender"
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "send_to", Params: map[string]any{"target": "tgt-1", "content": "hello"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	msgs, _ := s.GetChannelMessages("tgt-1")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(msgs))
	}
	if msgs[0].Sender != "resolved-sender" {
		t.Errorf("sender=%q, want resolved-sender", msgs[0].Sender)
	}
}

func TestHandleSendTo_ImplicitAck(t *testing.T) {
	h, s := mustHandler(t)
	// Send a message to sender-X first
	s.SendChannelMessage("sender-X", "other", "incoming msg", "command")

	// When sender-X sends a reply, their incoming messages should be marked read
	resp := h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target": "other", "content": "reply", "sender": "sender-X",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}

	// The message targeted at sender-X should now be delivered/read
	msgs, _ := s.GetChannelMessages("sender-X")
	if len(msgs) != 0 {
		t.Errorf("expected 0 undelivered messages after implicit ack, got %d", len(msgs))
	}
}

// --- handleCheckChannel ---

func TestHandleCheckChannel_OK(t *testing.T) {
	h, s := mustHandler(t)
	s.SendChannelMessage("sess-chan-1", "sender-a", "msg one", "command")
	s.SendChannelMessage("sess-chan-1", "sender-b", "msg two", "status")

	resp := h.Handle(Request{Method: "check_channel", Params: map[string]any{"session_id": "sess-chan-1"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "msg one" {
		t.Errorf("content=%v, want msg one", first["content"])
	}
	if first["msg_type"] != "command" {
		t.Errorf("msg_type=%v, want command", first["msg_type"])
	}
}

func TestHandleCheckChannel_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "check_channel", Params: map[string]any{"session_id": "no-messages"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 0 {
		t.Errorf("expected empty messages, got %d", len(msgs))
	}
}

func TestHandleCheckChannel_MissingSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "check_channel", Params: map[string]any{}})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleMarkChannelRead ---

func TestHandleMarkChannelRead_OK(t *testing.T) {
	h, s := mustHandler(t)
	s.SendChannelMessage("sess-mark-1", "other", "unread msg", "command")

	resp := h.Handle(Request{Method: "mark_channel_read", Params: map[string]any{"session_id": "sess-mark-1"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ok" {
		t.Errorf("status=%v, want ok", m["status"])
	}
	// Verify messages are now read
	msgs, _ := s.GetChannelMessages("sess-mark-1")
	if len(msgs) != 0 {
		t.Errorf("expected 0 undelivered after mark_channel_read, got %d", len(msgs))
	}
}

func TestHandleMarkChannelRead_MissingSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "mark_channel_read", Params: map[string]any{}})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleRegisterPID ---

func TestHandleRegisterPID_OK(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_pid", Params: map[string]any{"session_id": "pid-sess-1", "pid": float64(12345)}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}

	h.pidMapMu.Lock()
	pid := h.pidMap["pid-sess-1"]
	h.pidMapMu.Unlock()
	if pid != 12345 {
		t.Errorf("pid=%d, want 12345", pid)
	}

	// Also updates activeSessionID
	h.activeSessionMu.Lock()
	active := h.activeSessionID
	h.activeSessionMu.Unlock()
	if active != "pid-sess-1" {
		t.Errorf("activeSessionID=%q, want pid-sess-1", active)
	}
}

func TestHandleRegisterPID_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_pid", Params: map[string]any{"session_id": "s"}})
	if resp.Error == "" {
		t.Error("expected error for missing pid")
	}
	resp = h.Handle(Request{Method: "register_pid", Params: map[string]any{"pid": float64(1)}})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleRegisterWindow ---

func TestHandleRegisterWindow_OK(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_window", Params: map[string]any{
		"session_id": "win-sess-1", "window_id": "0x1234", "terminal": "ghostty",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}

	h.windowMapMu.Lock()
	wid := h.windowMap["win-sess-1"]
	term := h.terminalMap["win-sess-1"]
	h.windowMapMu.Unlock()
	if wid != "0x1234" {
		t.Errorf("window_id=%q, want 0x1234", wid)
	}
	if term != "ghostty" {
		t.Errorf("terminal=%q, want ghostty", term)
	}
}

func TestHandleRegisterWindow_NoTerminal(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_window", Params: map[string]any{
		"session_id": "win-sess-2", "window_id": "0x5678",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	h.windowMapMu.Lock()
	_, hasTerm := h.terminalMap["win-sess-2"]
	h.windowMapMu.Unlock()
	if hasTerm {
		t.Error("expected no terminal entry when not provided")
	}
}

func TestHandleRegisterWindow_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_window", Params: map[string]any{"session_id": "s"}})
	if resp.Error == "" {
		t.Error("expected error for missing window_id")
	}
	resp = h.Handle(Request{Method: "register_window", Params: map[string]any{"window_id": "w"}})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleBroadcast ---

func TestHandleBroadcast_OK(t *testing.T) {
	h, _ := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "bc-sender"
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "broadcast", Params: map[string]any{
		"project": "test-proj", "content": "hello everyone",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "broadcast_sent" {
		t.Errorf("status=%v, want broadcast_sent", m["status"])
	}
	if m["message_id"] == nil || m["message_id"].(float64) == 0 {
		t.Error("expected non-zero message_id")
	}
}

func TestHandleBroadcast_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	h.activeSessionMu.Lock()
	h.activeSessionID = "bc-sender"
	h.activeSessionMu.Unlock()

	resp := h.Handle(Request{Method: "broadcast", Params: map[string]any{"content": "c"}})
	if resp.Error == "" {
		t.Error("expected error for missing project")
	}
	resp = h.Handle(Request{Method: "broadcast", Params: map[string]any{"project": "p"}})
	if resp.Error == "" {
		t.Error("expected error for missing content")
	}
}

func TestHandleBroadcast_MissingSender(t *testing.T) {
	h, _ := mustHandler(t)
	// No activeSessionID, no sender param
	resp := h.Handle(Request{Method: "broadcast", Params: map[string]any{"project": "p", "content": "c"}})
	if resp.Error == "" {
		t.Error("expected error for missing sender")
	}
}

// --- handleCheckBroadcasts ---

func TestHandleCheckBroadcasts_OK(t *testing.T) {
	h, s := mustHandler(t)
	s.SendBroadcast("sender-bc", "proj-1", "broadcast msg")

	resp := h.Handle(Request{Method: "check_broadcasts", Params: map[string]any{
		"session_id": "reader-1", "project": "proj-1",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "broadcast msg" {
		t.Errorf("content=%v, want broadcast msg", first["content"])
	}
}

func TestHandleCheckBroadcasts_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "check_broadcasts", Params: map[string]any{
		"session_id": "reader-2", "project": "proj-2",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 0 {
		t.Errorf("expected empty, got %d", len(msgs))
	}
}

func TestHandleCheckBroadcasts_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "check_broadcasts", Params: map[string]any{}})
	if resp.Error != "" {
		t.Error("expected graceful empty response, not error")
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 0 {
		t.Errorf("expected empty, got %d", len(msgs))
	}
}

func TestHandleCheckBroadcasts_MarksRead(t *testing.T) {
	h, s := mustHandler(t)
	s.SendBroadcast("sender-x", "proj-r", "first read")

	// First check: should see the broadcast
	resp := h.Handle(Request{Method: "check_broadcasts", Params: map[string]any{
		"session_id": "reader-r", "project": "proj-r",
	}})
	m := resultMap(t, resp)
	if len(m["messages"].([]any)) != 1 {
		t.Fatal("expected 1 broadcast on first check")
	}

	// Second check: should be empty (already read)
	resp = h.Handle(Request{Method: "check_broadcasts", Params: map[string]any{
		"session_id": "reader-r", "project": "proj-r",
	}})
	m = resultMap(t, resp)
	if len(m["messages"].([]any)) != 0 {
		t.Error("expected 0 broadcasts after re-read")
	}
}

// --- handleEndDialog ---

func TestHandleEndDialog_ByDialogID(t *testing.T) {
	h, s := mustHandler(t)
	id, _ := s.StartDialog("init-e", "part-e", "ending test")

	resp := h.handleEndDialog(map[string]any{"dialog_id": float64(id)})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ended" {
		t.Errorf("status=%v, want ended", m["status"])
	}
}

func TestHandleEndDialog_BySession(t *testing.T) {
	h, s := mustHandler(t)
	s.StartDialog("init-es", "part-es", "session lookup")

	resp := h.handleEndDialog(map[string]any{"session_id": "init-es"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ended" {
		t.Errorf("status=%v, want ended", m["status"])
	}
}

func TestHandleEndDialog_NoDialog(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleEndDialog(map[string]any{"session_id": "no-dialog-sess"})
	if resp.Error == "" {
		t.Error("expected error for no active dialog")
	}
}

func TestHandleEndDialog_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleEndDialog(map[string]any{})
	if resp.Error == "" {
		t.Error("expected error for missing dialog_id and session_id")
	}
}

// --- handleCheckInvitations ---

func TestHandleCheckInvitations_None(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleCheckInvitations(map[string]any{"session_id": "no-inv-sess"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["has_invitation"] != false {
		t.Error("expected has_invitation=false")
	}
}

func TestHandleCheckInvitations_WithPending(t *testing.T) {
	h, s := mustHandler(t)
	dialogID, _ := s.StartDialog("init-inv", "partner-inv", "collab topic")
	// Add an initial message
	s.SendDialogMessage(dialogID, "init-inv", "let's work together")

	resp := h.handleCheckInvitations(map[string]any{"session_id": "partner-inv"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["has_invitation"] != true {
		t.Error("expected has_invitation=true")
	}
	if m["initiator"] != "init-inv" {
		t.Errorf("initiator=%v, want init-inv", m["initiator"])
	}
	if m["topic"] != "collab topic" {
		t.Errorf("topic=%v, want collab topic", m["topic"])
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 initial message, got %d", len(msgs))
	}
	if msgs[0] != "let's work together" {
		t.Errorf("message=%v, want let's work together", msgs[0])
	}
}

func TestHandleCheckInvitations_MissingSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleCheckInvitations(map[string]any{})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleCheckMessages ---

func TestHandleCheckMessages_NoDialog(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleCheckMessages(map[string]any{"session_id": "no-dialog-cm"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["has_dialog"] != false {
		t.Error("expected has_dialog=false")
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 0 {
		t.Errorf("expected empty messages, got %d", len(msgs))
	}
}

func TestHandleCheckMessages_WithMessages(t *testing.T) {
	h, s := mustHandler(t)
	dialogID, _ := s.StartDialog("init-cm", "partner-cm", "messages topic")
	s.SendDialogMessage(dialogID, "init-cm", "hello partner")
	s.SendDialogMessage(dialogID, "init-cm", "second msg")

	resp := h.handleCheckMessages(map[string]any{"session_id": "partner-cm"})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["has_dialog"] != true {
		t.Error("expected has_dialog=true")
	}
	if m["topic"] != "messages topic" {
		t.Errorf("topic=%v, want messages topic", m["topic"])
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestHandleCheckMessages_MissingSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleCheckMessages(map[string]any{})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
}

// --- handleMarkRead ---

func TestHandleMarkRead_OK(t *testing.T) {
	h, s := mustHandler(t)
	dialogID, _ := s.StartDialog("init-mr", "partner-mr", "mark read topic")
	s.SendDialogMessage(dialogID, "init-mr", "unread msg")

	resp := h.handleMarkRead(map[string]any{
		"dialog_id": float64(dialogID), "session_id": "partner-mr",
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ok" {
		t.Errorf("status=%v, want ok", m["status"])
	}

	// Verify messages are now read
	msgs, _ := s.GetUnreadMessages(dialogID, "partner-mr")
	if len(msgs) != 0 {
		t.Errorf("expected 0 unread after mark_read, got %d", len(msgs))
	}
}

func TestHandleMarkRead_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleMarkRead(map[string]any{"dialog_id": float64(1)})
	if resp.Error == "" {
		t.Error("expected error for missing session_id")
	}
	resp = h.handleMarkRead(map[string]any{"session_id": "s"})
	if resp.Error == "" {
		t.Error("expected error for missing dialog_id")
	}
}

// --- handleWhoami ---

func TestHandleWhoami_NonAgent(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "whoami", Params: map[string]any{"session_id": "regular-sess", "project": "proj-w"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["session_id"] != "regular-sess" {
		t.Errorf("session_id=%v, want regular-sess", m["session_id"])
	}
	if m["project"] != "proj-w" {
		t.Errorf("project=%v, want proj-w", m["project"])
	}
	if m["is_agent"] != false {
		t.Error("expected is_agent=false for non-agent session")
	}
}

func TestHandleWhoami_Agent(t *testing.T) {
	h, s := mustHandler(t)
	err := s.AgentCreate(storage.Agent{
		ID: "agent-001", Project: "proj-a", Section: "testing",
		SessionID: "agent-sess-1", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := h.Handle(Request{Method: "whoami", Params: map[string]any{"session_id": "agent-sess-1", "project": "proj-a"}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["is_agent"] != true {
		t.Error("expected is_agent=true")
	}
	if m["agent_id"] != "agent-001" {
		t.Errorf("agent_id=%v, want agent-001", m["agent_id"])
	}
	if m["section"] != "testing" {
		t.Errorf("section=%v, want testing", m["section"])
	}
	if m["status"] != "running" {
		t.Errorf("status=%v, want running", m["status"])
	}
}

func TestHandleWhoami_EmptySession(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "whoami", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	m := resultMap(t, resp)
	if m["session_id"] != "" {
		t.Errorf("expected empty session_id, got %v", m["session_id"])
	}
	if m["is_agent"] != false {
		t.Error("expected is_agent=false")
	}
}

// --- formatCheckMessagesResult ---

func TestFormatCheckMessagesResult_WithMessages(t *testing.T) {
	raw := json.RawMessage(`{"has_dialog":true,"topic":"collab","messages":[{"sender":"abcdefghij","content":"hello world"}]}`)
	got := formatCheckMessagesResult(raw)
	if got == "" {
		t.Fatal("expected non-empty output")
	}
	if got != "\U0001f4e8 DIALOG [collab] abcdefgh: hello world\n" {
		t.Errorf("unexpected format: %q", got)
	}
}

func TestFormatCheckMessagesResult_NoDialog(t *testing.T) {
	raw := json.RawMessage(`{"has_dialog":false,"messages":[]}`)
	got := formatCheckMessagesResult(raw)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFormatCheckMessagesResult_EmptyMessages(t *testing.T) {
	raw := json.RawMessage(`{"has_dialog":true,"messages":[],"topic":"t"}`)
	got := formatCheckMessagesResult(raw)
	if got != "" {
		t.Errorf("expected empty for no messages, got %q", got)
	}
}

func TestFormatCheckMessagesResult_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid}`)
	got := formatCheckMessagesResult(raw)
	if got != "" {
		t.Errorf("expected empty for invalid JSON, got %q", got)
	}
}

// --- notifySession (edge cases without xdotool) ---

func TestNotifySession_NoWindow(t *testing.T) {
	h, _ := mustHandler(t)
	if h.notifySession("unknown-sess", "text") {
		t.Error("expected false for unregistered session")
	}
}

func TestNotifySession_Ghostty(t *testing.T) {
	h, _ := mustHandler(t)
	h.windowMapMu.Lock()
	h.windowMap["ghost-sess"] = "0xABCD"
	h.terminalMap["ghost-sess"] = "ghostty"
	h.windowMapMu.Unlock()

	if h.notifySession("ghost-sess", "text") {
		t.Error("expected false for ghostty terminal")
	}
}

func TestNotifySession_SharedWindow(t *testing.T) {
	h, _ := mustHandler(t)
	h.windowMapMu.Lock()
	h.windowMap["sess-a"] = "0xSHARED"
	h.windowMap["sess-b"] = "0xSHARED"
	h.windowMapMu.Unlock()

	if h.notifySession("sess-a", "text") {
		t.Error("expected false for shared window")
	}
}
