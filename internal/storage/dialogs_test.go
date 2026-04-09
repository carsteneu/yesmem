package storage

import (
	"testing"
)

func TestStartDialogAndGet(t *testing.T) {
	s := mustOpen(t)

	id, err := s.StartDialog("session-a", "session-b", "Ingest-Pipeline")
	if err != nil {
		t.Fatalf("StartDialog: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero dialog ID")
	}

	d, err := s.GetDialog(id)
	if err != nil {
		t.Fatalf("GetDialog: %v", err)
	}
	if d == nil {
		t.Fatal("expected dialog, got nil")
	}
	if d.Initiator != "session-a" || d.Partner != "session-b" {
		t.Errorf("wrong sessions: %s / %s", d.Initiator, d.Partner)
	}
	if d.Topic != "Ingest-Pipeline" {
		t.Errorf("wrong topic: %s", d.Topic)
	}
	if d.Status != "pending" {
		t.Errorf("expected pending, got %s", d.Status)
	}
}

func TestGetActiveDialogForSession(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.StartDialog("session-a", "session-b", "test")
	s.UpdateDialogStatus(id, "active")

	// Both sides should find it
	for _, sid := range []string{"session-a", "session-b"} {
		d, err := s.GetActiveDialogForSession(sid)
		if err != nil {
			t.Fatalf("GetActiveDialogForSession(%s): %v", sid, err)
		}
		if d == nil {
			t.Fatalf("expected dialog for %s, got nil", sid)
		}
		if d.ID != id {
			t.Errorf("wrong dialog ID for %s: got %d, want %d", sid, d.ID, id)
		}
	}

	// Unrelated session should not find it
	d, _ := s.GetActiveDialogForSession("session-c")
	if d != nil {
		t.Error("session-c should not find dialog")
	}
}

func TestSendAndReceiveMessages(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.StartDialog("a", "b", "test")

	// A sends 2 messages
	s.SendDialogMessage(id, "a", "hello from A")
	s.SendDialogMessage(id, "a", "second message")

	// B should see both
	msgs, err := s.GetUnreadMessages(id, "b")
	if err != nil {
		t.Fatalf("GetUnreadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "hello from A" {
		t.Errorf("wrong content: %s", msgs[0].Content)
	}

	// A should NOT see own messages as unread
	ownMsgs, _ := s.GetUnreadMessages(id, "a")
	if len(ownMsgs) != 0 {
		t.Errorf("sender should not see own messages, got %d", len(ownMsgs))
	}
}

func TestMarkRead(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.StartDialog("a", "b", "test")
	s.SendDialogMessage(id, "a", "msg1")
	s.SendDialogMessage(id, "a", "msg2")

	// Mark as read for B
	err := s.MarkMessagesRead(id, "b")
	if err != nil {
		t.Fatalf("MarkMessagesRead: %v", err)
	}

	// Should be empty now
	msgs, _ := s.GetUnreadMessages(id, "b")
	if len(msgs) != 0 {
		t.Errorf("expected 0 after mark read, got %d", len(msgs))
	}
}

func TestCheckPendingInvitations(t *testing.T) {
	s := mustOpen(t)

	s.StartDialog("a", "b", "pending topic")

	// B should see invitation
	d, err := s.CheckPendingInvitations("b")
	if err != nil {
		t.Fatalf("CheckPendingInvitations: %v", err)
	}
	if d == nil {
		t.Fatal("expected pending invitation for b")
	}
	if d.Topic != "pending topic" {
		t.Errorf("wrong topic: %s", d.Topic)
	}

	// A should NOT see invitation (initiator, not partner)
	d, _ = s.CheckPendingInvitations("a")
	if d != nil {
		t.Error("initiator should not get own invitation")
	}

	// C should not see it
	d, _ = s.CheckPendingInvitations("c")
	if d != nil {
		t.Error("unrelated session should not see invitation")
	}
}

func TestEndDialog(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.StartDialog("a", "b", "test")
	s.UpdateDialogStatus(id, "active")
	s.UpdateDialogStatus(id, "ended")

	d, _ := s.GetDialog(id)
	if d.Status != "ended" {
		t.Errorf("expected ended, got %s", d.Status)
	}

	// Should not appear as active
	active, _ := s.GetActiveDialogForSession("a")
	if active != nil {
		t.Error("ended dialog should not appear as active")
	}

	// Should not appear as invitation
	inv, _ := s.CheckPendingInvitations("b")
	if inv != nil {
		t.Error("ended dialog should not appear as invitation")
	}
}

func TestMarkChannelMessagesRead_PrefixMatch(t *testing.T) {
	s := mustOpen(t)

	// Send message with short target (8 chars) — as send_to does in practice
	shortTarget := "a542b034"
	_, err := s.SendChannelMessage(shortTarget, "sender-1", "hello from sender", "command")
	if err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}

	// GetChannelMessages with full UUID should find it (prefix match)
	fullUUID := "a542b034-d69d-4e10-9553-a549eefa4d77"
	msgs, err := s.GetChannelMessages(fullUUID)
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// MarkChannelMessagesRead with full UUID should also work (prefix match)
	err = s.MarkChannelMessagesRead(fullUUID)
	if err != nil {
		t.Fatalf("MarkChannelMessagesRead: %v", err)
	}

	// Should be empty now
	msgs, err = s.GetChannelMessages(fullUUID)
	if err != nil {
		t.Fatalf("GetChannelMessages after mark: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 after mark read, got %d", len(msgs))
	}
}

func TestMarkChannelMessagesRead_ExactMatch(t *testing.T) {
	s := mustOpen(t)

	// Send with full UUID target
	fullUUID := "b1234567-aaaa-bbbb-cccc-dddddddddddd"
	_, err := s.SendChannelMessage(fullUUID, "sender-1", "direct message", "command")
	if err != nil {
		t.Fatalf("SendChannelMessage: %v", err)
	}

	// Mark with exact same UUID — should work via exact match branch
	err = s.MarkChannelMessagesRead(fullUUID)
	if err != nil {
		t.Fatalf("MarkChannelMessagesRead: %v", err)
	}

	msgs, _ := s.GetChannelMessages(fullUUID)
	if len(msgs) != 0 {
		t.Errorf("expected 0 after mark read, got %d", len(msgs))
	}
}

func TestSendChannelMessage_WithMsgType(t *testing.T) {
	s := mustOpen(t)
	msgID, err := s.SendChannelMessage("target-1", "sender-1", "hello", "command")
	if err != nil {
		t.Fatal(err)
	}
	if msgID == 0 {
		t.Fatal("expected non-zero message ID")
	}
	msgs, err := s.GetChannelMessages("target-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].MsgType != "command" {
		t.Errorf("msg_type=%q want command", msgs[0].MsgType)
	}
}

func TestSendChannelMessage_DefaultMsgType(t *testing.T) {
	s := mustOpen(t)
	msgID, err := s.SendChannelMessage("target-2", "sender-2", "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	if msgID == 0 {
		t.Fatal("expected non-zero message ID")
	}
	msgs, err := s.GetChannelMessages("target-2")
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].MsgType != "command" {
		t.Errorf("msg_type=%q want command (default)", msgs[0].MsgType)
	}
}

func TestIsAckOnAck_DetectsLoop(t *testing.T) {
	s := mustOpen(t)
	s.SendChannelMessage("session-B", "session-A", "ok", "ack")
	s.MarkChannelMessagesRead("session-B")
	isLoop := s.IsAckOnAck("session-A", "session-B", "ack")
	if !isLoop {
		t.Error("expected ACK-on-ACK detection, got false")
	}
}

func TestIsAckOnAck_CommandAfterAck_NotLoop(t *testing.T) {
	s := mustOpen(t)
	s.SendChannelMessage("session-B", "session-A", "ok", "ack")
	s.MarkChannelMessagesRead("session-B")
	isLoop := s.IsAckOnAck("session-A", "session-B", "command")
	if isLoop {
		t.Error("command after ack should not be detected as loop")
	}
}

func TestIsAckOnAck_NoHistory_NotLoop(t *testing.T) {
	s := mustOpen(t)
	isLoop := s.IsAckOnAck("session-A", "session-B", "ack")
	if isLoop {
		t.Error("no history should not be detected as loop")
	}
}

func TestBroadcast(t *testing.T) {
	s := mustOpen(t)

	// Session A broadcasts
	id, err := s.SendBroadcast("session-a", "yesmem", "Schema v0.29 ist drin")
	if err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero message ID")
	}

	// Session B sees it
	msgs, err := s.GetUnreadBroadcasts("session-b", "yesmem")
	if err != nil {
		t.Fatalf("GetUnreadBroadcasts: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(msgs))
	}
	if msgs[0].Content != "Schema v0.29 ist drin" {
		t.Errorf("wrong content: %s", msgs[0].Content)
	}

	// Session A should NOT see own broadcast
	own, _ := s.GetUnreadBroadcasts("session-a", "yesmem")
	if len(own) != 0 {
		t.Errorf("sender should not see own broadcast, got %d", len(own))
	}

	// Wrong project should not see it
	other, _ := s.GetUnreadBroadcasts("session-b", "other-project")
	if len(other) != 0 {
		t.Errorf("wrong project should not see broadcast, got %d", len(other))
	}

	// Mark read
	s.MarkBroadcastsRead([]int64{msgs[0].ID}, "session-b")
	after, _ := s.GetUnreadBroadcasts("session-b", "yesmem")
	if len(after) != 0 {
		t.Errorf("expected 0 after mark read, got %d", len(after))
	}
}
