package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// --- handleGetSession ---

func TestHandleGetSession_NotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetSession(map[string]any{"session_id": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestHandleGetSession_SummaryMode(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-1", "yesmem", 3)

	resp := h.handleGetSession(map[string]any{"session_id": "sess-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)

	// Summary mode is the default — should have session, summary, recent_messages
	if m["session"] == nil {
		t.Error("expected 'session' key in summary")
	}
	if m["summary"] == nil {
		t.Error("expected 'summary' key in summary")
	}
	if m["recent_messages"] == nil {
		t.Error("expected 'recent_messages' key in summary")
	}
	if m["hint"] == nil {
		t.Error("expected 'hint' key in summary")
	}
}

func TestHandleGetSession_SummaryMode_Explicit(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-2", "yesmem", 2)

	resp := h.handleGetSession(map[string]any{"session_id": "sess-2", "mode": "summary"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["summary"] == nil {
		t.Error("expected 'summary' key")
	}
	// subagent_count should be present (0 for no subagents)
	if _, ok := m["subagent_count"]; !ok {
		t.Error("expected 'subagent_count' key in summary response")
	}
}

func TestHandleGetSession_RecentMode(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-r", "yesmem", 5)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-r",
		"mode":       "recent",
		"limit":      float64(3),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)

	msgs, ok := m["messages"].([]any)
	if !ok {
		t.Fatal("expected 'messages' to be a slice")
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 recent messages, got %d", len(msgs))
	}
	if m["total"].(float64) != 5 {
		t.Errorf("expected total=5, got %v", m["total"])
	}
}

func TestHandleGetSession_RecentMode_LimitExceedsMessages(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-rl", "yesmem", 2)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-rl",
		"mode":       "recent",
		"limit":      float64(100),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (all of them), got %d", len(msgs))
	}
}

func TestHandleGetSession_PaginatedMode(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-p", "yesmem", 10)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-p",
		"mode":       "paginated",
		"offset":     float64(2),
		"limit":      float64(3),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)

	msgs := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	if m["offset"].(float64) != 2 {
		t.Errorf("expected offset=2, got %v", m["offset"])
	}
	if m["has_more"] != true {
		t.Error("expected has_more=true")
	}
	if m["total"].(float64) != 10 {
		t.Errorf("expected total=10, got %v", m["total"])
	}
}

func TestHandleGetSession_PaginatedMode_OffsetPastEnd(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-pe", "yesmem", 3)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-pe",
		"mode":       "paginated",
		"offset":     float64(100),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for offset past end, got %d", len(msgs))
	}
}

func TestHandleGetSession_PaginatedMode_LastPage(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-lp", "yesmem", 5)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-lp",
		"mode":       "paginated",
		"offset":     float64(3),
		"limit":      float64(10),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages on last page, got %d", len(msgs))
	}
	if m["has_more"] != false {
		t.Error("expected has_more=false on last page")
	}
}

func TestHandleGetSession_FullMode(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-f", "yesmem", 5)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-f",
		"mode":       "full",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 5 {
		t.Errorf("expected 5 messages in full mode, got %d", len(msgs))
	}
}

func TestHandleGetSession_FullMode_TruncatesOver100(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-ft", "yesmem", 110)

	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-ft",
		"mode":       "full",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 100 {
		t.Errorf("expected 100 messages (cap), got %d", len(msgs))
	}
	if m["truncated"] != true {
		t.Error("expected truncated=true")
	}
	if m["hint"] == nil {
		t.Error("expected hint for truncated full mode")
	}
}

func TestHandleGetSession_SubagentCount(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "parent-1", "yesmem", 2)

	// Insert a child session referencing parent-1
	child := &models.Session{
		ID: "child-1", Project: "/home/test/yesmem", ProjectShort: "yesmem",
		MessageCount: 5, StartedAt: time.Now(), ParentSessionID: "parent-1",
		AgentType: "task",
	}
	if err := s.UpsertSession(child); err != nil {
		t.Fatalf("upsert child: %v", err)
	}

	resp := h.handleGetSession(map[string]any{"session_id": "parent-1", "mode": "recent"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["subagent_count"].(float64) != 1 {
		t.Errorf("expected subagent_count=1, got %v", m["subagent_count"])
	}
}

func TestHandleGetSession_DefaultLimitAndOffset(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "sess-def", "yesmem", 3)

	// No explicit limit/offset — defaults should apply (limit=50, offset=0)
	resp := h.handleGetSession(map[string]any{
		"session_id": "sess-def",
		"mode":       "recent",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	msgs := m["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages with default limit, got %d", len(msgs))
	}
}

// --- buildSessionSummary ---

func TestHandleGetSession_SummaryContent(t *testing.T) {
	h, s := mustHandler(t)

	sess := &models.Session{
		ID: "sess-sc", Project: "/home/test/yesmem", ProjectShort: "yesmem",
		MessageCount: 3, StartedAt: time.Now(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	now := time.Now()
	msgs := []models.Message{
		{SessionID: "sess-sc", Role: "user", MessageType: "text", Content: "fix the bug", Timestamp: now, Sequence: 1},
		{SessionID: "sess-sc", Role: "assistant", MessageType: "tool_use", ToolName: "Bash", FilePath: "/tmp/foo.go", Timestamp: now, Sequence: 2},
		{SessionID: "sess-sc", Role: "assistant", MessageType: "text", Content: "done", Timestamp: now, Sequence: 3},
	}
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	resp := h.handleGetSession(map[string]any{"session_id": "sess-sc", "mode": "summary"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)

	summary := m["summary"].(map[string]any)
	requests := summary["user_requests"].([]any)
	if len(requests) != 1 || requests[0] != "fix the bug" {
		t.Errorf("unexpected user_requests: %v", requests)
	}
	tools := summary["tools_used"].(map[string]any)
	if tools["Bash"].(float64) != 1 {
		t.Errorf("expected Bash tool count=1, got %v", tools["Bash"])
	}
	files := summary["files_worked_on"].([]any)
	if len(files) != 1 || files[0] != "/tmp/foo.go" {
		t.Errorf("unexpected files_worked_on: %v", files)
	}
}

// --- handleListProjects ---

func TestHandleListProjects_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleListProjects()
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	// Result should be nil or empty slice for no projects
	if resp.Result == nil {
		return // nil is valid for empty
	}
	s := resultSlice(t, resp)
	if len(s) != 0 {
		t.Errorf("expected 0 projects, got %d", len(s))
	}
}

func TestHandleListProjects_WithSessions(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "lp-1", "proj-a", 5)
	seedSession(t, s, "lp-2", "proj-b", 3)

	resp := h.handleListProjects()
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	projects := resultSlice(t, resp)
	if len(projects) < 2 {
		t.Errorf("expected at least 2 projects, got %d", len(projects))
	}
}

// --- handleProjectSummary ---

func TestHandleProjectSummary_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleProjectSummary(map[string]any{"project": "nonexistent"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	// Empty project returns nil or empty slice
	if resp.Result == nil {
		return
	}
	s := resultSlice(t, resp)
	if len(s) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(s))
	}
}

func TestHandleProjectSummary_ReturnsSessions(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "ps-1", "myproject", 10)
	seedSession(t, s, "ps-2", "myproject", 5)
	seedSession(t, s, "ps-3", "other", 3)

	resp := h.handleProjectSummary(map[string]any{"project": "myproject"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	sessions := resultSlice(t, resp)
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions for 'myproject', got %d", len(sessions))
	}
}

func TestHandleProjectSummary_RespectsLimit(t *testing.T) {
	h, s := mustHandler(t)
	for i := 0; i < 5; i++ {
		seedSession(t, s, fmt.Sprintf("psl-%d", i), "limited", 2)
	}

	resp := h.handleProjectSummary(map[string]any{
		"project": "limited",
		"limit":   float64(2),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	sessions := resultSlice(t, resp)
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions (limit), got %d", len(sessions))
	}
}

func TestHandleProjectSummary_DefaultLimit(t *testing.T) {
	h, s := mustHandler(t)
	seedSession(t, s, "psd-1", "defproj", 3)

	// No limit param — defaults to 20
	resp := h.handleProjectSummary(map[string]any{"project": "defproj"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	sessions := resultSlice(t, resp)
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}
}

// --- lightMessages / fullMessages ---

func TestHandleGetSession_LightMessagesTruncatesContent(t *testing.T) {
	h, s := mustHandler(t)

	sess := &models.Session{
		ID: "sess-trunc", Project: "/home/test/yesmem", ProjectShort: "yesmem",
		MessageCount: 1, StartedAt: time.Now(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	longContent := make([]byte, 600)
	for i := range longContent {
		longContent[i] = 'x'
	}
	msgs := []models.Message{
		{SessionID: "sess-trunc", Role: "user", MessageType: "text", Content: string(longContent), Timestamp: time.Now(), Sequence: 1},
	}
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	// Recent mode uses lightMessages (500 char limit)
	resp := h.handleGetSession(map[string]any{"session_id": "sess-trunc", "mode": "recent"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	recentMsgs := m["messages"].([]any)
	msg0 := recentMsgs[0].(map[string]any)
	content := msg0["content"].(string)
	if len(content) > 520 {
		t.Errorf("expected truncated content in recent mode, got length %d", len(content))
	}

	// Full mode uses fullMessages (no truncation)
	resp = h.handleGetSession(map[string]any{"session_id": "sess-trunc", "mode": "full"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m = resultMap(t, resp)
	fullMsgs := m["messages"].([]any)
	msg0 = fullMsgs[0].(map[string]any)
	content = msg0["content"].(string)
	if len(content) != 600 {
		t.Errorf("expected full content (600 chars) in full mode, got length %d", len(content))
	}
}

// --- helpers ---

// seedSession creates a session and N messages for testing.
func seedSession(t *testing.T, s *storage.Store, id, projectShort string, msgCount int) {
	t.Helper()
	sess := &models.Session{
		ID:           id,
		Project:      "/home/test/" + projectShort,
		ProjectShort: projectShort,
		MessageCount: msgCount,
		StartedAt:    time.Now(),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert session %s: %v", id, err)
	}
	msgs := make([]models.Message, msgCount)
	now := time.Now()
	for i := 0; i < msgCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = models.Message{
			SessionID:   id,
			Role:        role,
			MessageType: "text",
			Content:     fmt.Sprintf("message %d", i),
			Timestamp:   now.Add(time.Duration(i) * time.Second),
			Sequence:    i + 1,
		}
	}
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert messages for %s: %v", id, err)
	}
}
