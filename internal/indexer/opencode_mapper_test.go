package indexer

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestMapOpencodeParts_UserText(t *testing.T) {
	dbMsgs := []opencodeDBMessage{
		{
			ID: "msg_1", SessionID: "ses_test",
			Role: "user",
			Part: opencodeDBPart{Type: "text", Text: "hello world"},
		},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].MessageType != "text" || msgs[0].Content != "hello world" {
		t.Fatalf("unexpected message: role=%s type=%s content=%q", msgs[0].Role, msgs[0].MessageType, msgs[0].Content)
	}
	if msgs[0].Sequence != 0 {
		t.Fatalf("expected sequence 0, got %d", msgs[0].Sequence)
	}
	if msgs[0].SourceAgent != models.SourceAgentOpencode {
		t.Fatalf("expected source_agent opencode, got %s", msgs[0].SourceAgent)
	}
}

func TestMapOpencodeParts_AssistantText(t *testing.T) {
	dbMsgs := []opencodeDBMessage{
		{
			ID: "msg_2", SessionID: "ses_test",
			Role: "assistant",
			Part: opencodeDBPart{Type: "text", Text: "I'll help you."},
		},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" || msgs[0].MessageType != "text" {
		t.Fatalf("unexpected message: role=%s type=%s", msgs[0].Role, msgs[0].MessageType)
	}
}

func TestMapOpencodeParts_Thinking(t *testing.T) {
	thinkingText := "The user wants X, I should do Y."
	dbMsgs := []opencodeDBMessage{
		{
			ID: "msg_3", SessionID: "ses_test",
			Role: "assistant",
			Part: opencodeDBPart{Type: "reasoning", Text: thinkingText},
		},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" || msgs[0].MessageType != "thinking" {
		t.Fatalf("expected thinking, got role=%s type=%s", msgs[0].Role, msgs[0].MessageType)
	}
	if msgs[0].Content != "" {
		t.Fatalf("thinking Content must be empty, got %q", msgs[0].Content)
	}
	if string(msgs[0].ContentBlob) != thinkingText {
		t.Fatalf("thinking ContentBlob mismatch: %q", string(msgs[0].ContentBlob))
	}
}

func TestMapOpencodeParts_ToolUse(t *testing.T) {
	dbMsgs := []opencodeDBMessage{
		{
			ID: "msg_4", SessionID: "ses_test",
			Role: "assistant",
			Part: opencodeDBPart{
				Type:   "tool",
				Tool:   "skill",
				CallID: "call_00_abc123",
				ToolState: &opencodeToolState{
					Status: "completed",
					Input:  json.RawMessage(`{"name":"yesmem-orientation"}`),
					Output: "<skill_content>...</skill_content>",
				},
			},
		},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (tool_use + tool_result), got %d", len(msgs))
	}

	toolUse := msgs[0]
	if toolUse.Role != "assistant" || toolUse.MessageType != "tool_use" || toolUse.ToolName != "skill" {
		t.Fatalf("unexpected tool_use: role=%s type=%s tool=%s", toolUse.Role, toolUse.MessageType, toolUse.ToolName)
	}
	if !strings.Contains(toolUse.Content, "yesmem-orientation") {
		t.Fatalf("tool_use Content should contain tool input, got %q", toolUse.Content)
	}
	if toolUse.Sequence != 0 {
		t.Fatalf("tool_use sequence should be 0, got %d", toolUse.Sequence)
	}

	toolResult := msgs[1]
	if toolResult.Role != "user" || toolResult.MessageType != "tool_result" {
		t.Fatalf("unexpected tool_result: role=%s type=%s", toolResult.Role, toolResult.MessageType)
	}
	if toolResult.Content != "<skill_content>...</skill_content>" {
		t.Fatalf("tool_result Content mismatch: %q", toolResult.Content)
	}
	if toolResult.Sequence != 1 {
		t.Fatalf("tool_result sequence should be 1, got %d", toolResult.Sequence)
	}
}

func TestMapOpencodeParts_SkipStepMarkers(t *testing.T) {
	dbMsgs := []opencodeDBMessage{
		{ID: "m1", SessionID: "ses_test", Role: "assistant", Part: opencodeDBPart{Type: "step-start"}},
		{ID: "m1", SessionID: "ses_test", Role: "assistant", Part: opencodeDBPart{Type: "reasoning", Text: "thinking..."}},
		{ID: "m1", SessionID: "ses_test", Role: "assistant", Part: opencodeDBPart{Type: "text", Text: "result"}},
		{ID: "m1", SessionID: "ses_test", Role: "assistant", Part: opencodeDBPart{Type: "step-finish"}},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (thinking + text), got %d", len(msgs))
	}
	if msgs[0].MessageType != "thinking" {
		t.Fatalf("first should be thinking, got %s", msgs[0].MessageType)
	}
	if msgs[1].MessageType != "text" {
		t.Fatalf("second should be text, got %s", msgs[1].MessageType)
	}
}

func TestMapOpencodeParts_SequenceIncrement(t *testing.T) {
	dbMsgs := []opencodeDBMessage{
		{ID: "m1", SessionID: "ses_test", Role: "user", Part: opencodeDBPart{Type: "text", Text: "hi"}},
		{ID: "m2", SessionID: "ses_test", Role: "assistant", Part: opencodeDBPart{Type: "text", Text: "hello"}},
		{ID: "m3", SessionID: "ses_test", Role: "user", Part: opencodeDBPart{Type: "text", Text: "help me"}},
	}
	msgs := mapOpencodeMessages(dbMsgs)
	if len(msgs) != 3 {
		t.Fatalf("expected 3, got %d", len(msgs))
	}
	for i, m := range msgs {
		if m.Sequence != i {
			t.Fatalf("msg %d: expected sequence %d, got %d", i, i, m.Sequence)
		}
	}
}

func TestMapOpencodeMessages_Empty(t *testing.T) {
	msgs := mapOpencodeMessages(nil)
	if len(msgs) != 0 {
		t.Fatalf("expected empty, got %d", len(msgs))
	}
	msgs = mapOpencodeMessages([]opencodeDBMessage{})
	if len(msgs) != 0 {
		t.Fatalf("expected empty, got %d", len(msgs))
	}
}

func TestBuildSession_Opencode(t *testing.T) {
	dbID := "ses_203379163ffeOCkZGQFP71ATIH"
	directory := "/home/chief/memory/yesmem/.worktrees/opencode-proxy"
	title := "Test Session"
	created := time.UnixMilli(1778072048326)
	updated := time.UnixMilli(1778080000000)

	dbSess := opencodeDBSession{
		ID:        dbID,
		Directory: directory,
		Title:     title,
		Created:   created,
		Updated:   updated,
		ParentID:  "",
	}

	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "first message"},
		{Role: "assistant", MessageType: "text", Content: "reply"},
	}

	sess := buildOpencodeSession(dbSess, msgs)
	if sess.ID == "" {
		t.Fatal("session ID is empty")
	}
	// ID should be normalized with "opencode:" prefix
	if !strings.HasPrefix(sess.ID, models.SourceAgentOpencode+":") {
		t.Fatalf("expected ID prefix 'opencode:', got %s", sess.ID)
	}
	if sess.ProjectShort != "opencode-proxy" {
		t.Fatalf("expected project 'opencode-proxy', got %s", sess.ProjectShort)
	}
	if sess.MessageCount != 2 {
		t.Fatalf("expected 2 messages, got %d", sess.MessageCount)
	}
	if !sess.StartedAt.Equal(created) {
		t.Fatalf("started_at mismatch: %v vs %v", sess.StartedAt, created)
	}
	if !sess.EndedAt.Equal(updated) {
		t.Fatalf("ended_at mismatch: %v vs %v", sess.EndedAt, updated)
	}
	if sess.SourceAgent != models.SourceAgentOpencode {
		t.Fatalf("expected source_agent opencode, got %s", sess.SourceAgent)
	}
	if sess.FirstMessage != "first message" {
		t.Fatalf("expected first message 'first message', got %q", sess.FirstMessage)
	}
	if sess.JSONLPath != "" {
		t.Fatalf("JSONLPath should be empty for opencode, got %s", sess.JSONLPath)
	}
}

func TestBuildSession_OpencodeSubagent(t *testing.T) {
	dbSess := opencodeDBSession{
		ID:        "ses_child",
		Directory: "/tmp/test",
		Title:     fmt.Sprintf("Subagent (%s)", models.SourceAgentOpencode),
		Created:   time.Now(),
		Updated:   time.Now(),
		ParentID:  "ses_parent",
	}
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "subagent task"},
	}

	sess := buildOpencodeSession(dbSess, msgs)
	if sess.ParentSessionID == "" {
		t.Fatal("subagent parent_id should not be empty")
	}
	if !strings.HasPrefix(sess.ParentSessionID, models.SourceAgentOpencode+":") {
		t.Fatalf("parent_id should be opencode-namespaced: %s", sess.ParentSessionID)
	}
}
