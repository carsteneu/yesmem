package indexer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"

	_ "modernc.org/sqlite"
)

func createTestOpencodeDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS session (
			id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
			slug TEXT, directory TEXT, title TEXT, version TEXT,
			time_created INTEGER, time_updated INTEGER,
			time_compacting INTEGER, time_archived INTEGER,
			workspace_id TEXT, path TEXT, agent TEXT, model TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS message (
			id TEXT PRIMARY KEY, session_id TEXT,
			time_created INTEGER, time_updated INTEGER, data TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS part (
			id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
			time_created INTEGER, time_updated INTEGER, data TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS project (
			id TEXT PRIMARY KEY, worktree TEXT, vcs TEXT,
			name TEXT, time_created INTEGER, time_updated INTEGER,
			time_initialized INTEGER, sandboxes TEXT, commands TEXT
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func writeOpencodeSession(db *sql.DB, sessID, directory, title string, created, updated time.Time, parentID string) error {
	projectID := "proj_test"
	_, err := db.Exec(`INSERT OR REPLACE INTO project (id, worktree, name, time_created, time_updated, sandboxes)
		VALUES (?, ?, ?, ?, ?, '[]')`, projectID, directory, title, created.UnixMilli(), updated.UnixMilli())
	if err != nil {
		return err
	}

	slug := strings.ReplaceAll(strings.ToLower(title), " ", "-")
	var parentVal any
	if parentID != "" {
		parentVal = parentID
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO session
		(id, project_id, parent_id, slug, directory, title, version, time_created, time_updated, agent, model)
		VALUES (?, ?, ?, ?, ?, ?, '1.0', ?, ?, 'build', '{"id":"deepseek-v4-pro","providerID":"deepseek","variant":"max"}')`,
		sessID, projectID, parentVal, slug, directory, title, created.UnixMilli(), updated.UnixMilli())
	return err
}

func writeOpencodeMessage(db *sql.DB, msgID, sessionID, role string, created time.Time, data map[string]any) error {
	dataJSON, _ := json.Marshal(data)
	_, err := db.Exec(`INSERT OR REPLACE INTO message (id, session_id, time_created, time_updated, data)
		VALUES (?, ?, ?, ?, ?)`, msgID, sessionID, created.UnixMilli(), created.UnixMilli(), string(dataJSON))
	return err
}

func writeOpencodePart(db *sql.DB, partID, msgID, sessionID string, created time.Time, partData map[string]any) error {
	dataJSON, _ := json.Marshal(partData)
	_, err := db.Exec(`INSERT OR REPLACE INTO part (id, message_id, session_id, time_created, time_updated, data)
		VALUES (?, ?, ?, ?, ?, ?)`, partID, msgID, sessionID, created.UnixMilli(), created.UnixMilli(), string(dataJSON))
	return err
}

func TestOpencodeScanner_Basic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "opencode-scanner-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ocDBPath := filepath.Join(tmpDir, "opencode.db")
	ocDB, err := createTestOpencodeDB(ocDBPath)
	if err != nil {
		t.Fatal(err)
	}

	sessID := "ses_test123"
	dir := "/home/test/opencode-proxy"
	title := "Test Session"
	created := time.Now().Add(-1 * time.Hour)
	updated := time.Now().Add(-30 * time.Minute)

	if err := writeOpencodeSession(ocDB, sessID, dir, title, created, updated, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeMessage(ocDB, "msg_1", sessID, "user", created, map[string]any{
		"role": "user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "part_1", "msg_1", sessID, created, map[string]any{
		"type": "text",
		"text": "hello world",
	}); err != nil {
		t.Fatal(err)
	}

	if err := writeOpencodeMessage(ocDB, "msg_2", sessID, "assistant", created.Add(time.Second), map[string]any{
		"role": "assistant",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "part_2", "msg_2", sessID, created.Add(time.Second), map[string]any{
		"type": "text",
		"text": "I can help!",
	}); err != nil {
		t.Fatal(err)
	}
	ocDB.Close()

	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	scanner := NewOpencodeScanner(ocDBPath, store, nil, nil, nil)
	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	normID := models.NormalizeSessionID(models.SourceAgentOpencode, sessID)
	sess, err := store.GetSession(normID)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil {
		t.Fatalf("session %s not found after scan", normID)
	}
	if sess.MessageCount != 2 {
		t.Fatalf("expected 2 messages, got %d", sess.MessageCount)
	}
	if sess.ProjectShort != "opencode-proxy" {
		t.Fatalf("expected project 'opencode-proxy', got %s", sess.ProjectShort)
	}
	if sess.SourceAgent != "opencode" {
		t.Fatalf("expected source_agent 'opencode', got %s", sess.SourceAgent)
	}

	msgs, err := store.GetMessagesBySession(normID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "hello world" {
		t.Fatalf("first message: %q", msgs[0].Content)
	}
	if msgs[1].Content != "I can help!" {
		t.Fatalf("second message: %q", msgs[1].Content)
	}
}

func TestOpencodeScanner_ToolUse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "opencode-scanner-tool-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ocDBPath := filepath.Join(tmpDir, "opencode.db")
	ocDB, err := createTestOpencodeDB(ocDBPath)
	if err != nil {
		t.Fatal(err)
	}

	sessID := "ses_tooltest"
	now := time.Now()
	if err := writeOpencodeSession(ocDB, sessID, "/tmp/test", "Tool Test", now.Add(-1*time.Hour), now, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeMessage(ocDB, "msg_1", sessID, "assistant", now, map[string]any{
		"role": "assistant",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "part_1", "msg_1", sessID, now, map[string]any{
		"type":   "tool",
		"tool":   "Bash",
		"callID": "call_00_abc",
		"state": map[string]any{
			"status": "completed",
			"input":  map[string]any{"command": "ls", "description": "list files"},
			"output": "file1\nfile2\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	ocDB.Close()

	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	scanner := NewOpencodeScanner(ocDBPath, store, nil, nil, nil)
	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	normID := models.NormalizeSessionID(models.SourceAgentOpencode, sessID)
	msgs, err := store.GetMessagesBySession(normID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 (tool_use + tool_result), got %d", len(msgs))
	}
	if msgs[0].MessageType != "tool_use" || msgs[0].ToolName != "Bash" {
		t.Fatalf("expected tool_use Bash: type=%s name=%s", msgs[0].MessageType, msgs[0].ToolName)
	}
	if msgs[1].MessageType != "tool_result" || msgs[1].Content != "file1\nfile2\n" {
		t.Fatalf("expected tool_result with output: type=%s content=%q", msgs[1].MessageType, msgs[1].Content)
	}
}

func TestOpencodeScanner_Incremental(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "opencode-scanner-inc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ocDBPath := filepath.Join(tmpDir, "opencode.db")
	ocDB, err := createTestOpencodeDB(ocDBPath)
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// First scan: seed one session
	created := time.Now().Add(-2 * time.Hour)
	updated := time.Now().Add(-1 * time.Hour)
	if err := writeOpencodeSession(ocDB, "ses_s1", "/tmp/a", "Session A", created, updated, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeMessage(ocDB, "m1", "ses_s1", "user", created, map[string]any{"role": "user"}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "p1", "m1", "ses_s1", created, map[string]any{"type": "text", "text": "msg a"}); err != nil {
		t.Fatal(err)
	}

	scanner := NewOpencodeScanner(ocDBPath, store, nil, nil, nil)
	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	nID1 := models.NormalizeSessionID(models.SourceAgentOpencode, "ses_s1")
	s1, _ := store.GetSession(nID1)
	if s1 == nil {
		t.Fatal("session A not found after first scan")
	}

	// Second scan: add another session with later time_updated
	updated2 := time.Now().Add(-30 * time.Minute)
	if err := writeOpencodeSession(ocDB, "ses_s2", "/tmp/b", "Session B", created, updated2, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeMessage(ocDB, "m2", "ses_s2", "user", updated2, map[string]any{"role": "user"}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "p2", "m2", "ses_s2", updated2, map[string]any{"type": "text", "text": "msg b"}); err != nil {
		t.Fatal(err)
	}

	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	nID2 := models.NormalizeSessionID(models.SourceAgentOpencode, "ses_s2")
	s2, _ := store.GetSession(nID2)
	if s2 == nil {
		t.Fatal("session B not found after incremental scan")
	}

	// Session A should NOT be re-scanned (time_updated unchanged),
	// but should still have its original message count
	aMsgs, _ := store.GetMessagesBySession(nID1)
	if len(aMsgs) != 1 {
		t.Fatalf("session A: expected 1 msg, got %d", len(aMsgs))
	}
	if aMsgs[0].Content != "msg a" {
		t.Fatalf("session A content wrong: %q", aMsgs[0].Content)
	}

	bMsgs, _ := store.GetMessagesBySession(nID2)
	if len(bMsgs) != 1 {
		t.Fatalf("session B: expected 1 msg, got %d", len(bMsgs))
	}
	if bMsgs[0].Content != "msg b" {
		t.Fatalf("session B content wrong: %q", bMsgs[0].Content)
	}
}

func TestOpencodeScanner_ReindexSameSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "opencode-scanner-reidx-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ocDBPath := filepath.Join(tmpDir, "opencode.db")
	ocDB, err := createTestOpencodeDB(ocDBPath)
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created := time.Now().Add(-1 * time.Hour)
	updated := time.Now().Add(-30 * time.Minute)
	if err := writeOpencodeSession(ocDB, "ses_r1", "/tmp/r", "Reindex", created, updated, ""); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodeMessage(ocDB, "rm1", "ses_r1", "user", created, map[string]any{"role": "user"}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "rp1", "rm1", "ses_r1", created, map[string]any{"type": "text", "text": "first"}); err != nil {
		t.Fatal(err)
	}
	ocDB.Close()

	scanner := NewOpencodeScanner(ocDBPath, store, nil, nil, nil)
	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	nID := models.NormalizeSessionID(models.SourceAgentOpencode, "ses_r1")
	msgs, _ := store.GetMessagesBySession(nID)
	if len(msgs) != 1 || msgs[0].Content != "first" {
		t.Fatalf("first scan: %d msgs, content=%q", len(msgs), msgs[0].Content)
	}

	// Re-open DB, add a new message with a later time_updated
	ocDB, _ = sql.Open("sqlite", ocDBPath)
	newTime := time.Now()
	if _, err := ocDB.Exec(`UPDATE session SET time_updated = ? WHERE id = 'ses_r1'`,
		newTime.UnixMilli()); err != nil {
		panic(fmt.Sprintf("update: %v", err))
	}
	if err := writeOpencodeMessage(ocDB, "rm2", "ses_r1", "assistant", newTime, map[string]any{
		"role": "assistant",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeOpencodePart(ocDB, "rp2", "rm2", "ses_r1", newTime, map[string]any{
		"type": "text", "text": "second",
	}); err != nil {
		t.Fatal(err)
	}
	ocDB.Close()

	// Re-scan should pick up the updated session (DELETE old + INSERT new)
	scanner.lastCheckAt = time.Now().Add(-2 * scanOpencodeInterval)
	scanner.MaybeScan()

	msgs, _ = store.GetMessagesBySession(nID)
	if len(msgs) != 2 {
		t.Fatalf("re-scan: expected 2 msgs, got %d (old should be deleted + both re-inserted)", len(msgs))
	}
	contents := []string{msgs[0].Content, msgs[1].Content}
	foundFirst, foundSecond := false, false
	for _, c := range contents {
		if c == "first" {
			foundFirst = true
		}
		if c == "second" {
			foundSecond = true
		}
	}
	if !foundFirst {
		t.Fatal("re-scan: 'first' message not found")
	}
	if !foundSecond {
		t.Fatal("re-scan: 'second' message not found")
	}
}

func TestOpencodeScanner_SessionOrder(t *testing.T) {
	// Test case to ensure sessions are loaded in the correct order
	// based on time_created, not by how they appear in the DB.
	t.Skip("not yet implemented")
}

func TestIsExtractionPipelineSession_MoreThanFiveMessages(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", Content: "Du bist der Briefing-Autor für YesMem — ein Memory-System..."},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "Generate the narrative"},
		{Role: "assistant", Content: "I'm back. 22000 times..."},
		{Role: "user", Content: "Continue"},
		{Role: "assistant", Content: "done"},
	}
	if !isExtractionPipelineSession(msgs) {
		t.Fatal("should detect briefing-author session with 6 messages")
	}
}

func TestIsExtractionPipelineSession_FewMessagesStillDetected(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", Content: "You are a knowledge extractor. Read the conversation and extract knowledge."},
		{Role: "assistant", Content: "ok"},
	}
	if !isExtractionPipelineSession(msgs) {
		t.Fatal("should detect knowledge-extractor session with 2 messages")
	}
}

func TestIsExtractionPipelineSession_NormalSession(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", Content: "Fix the nginx config for port 8080"},
		{Role: "assistant", Content: "Sure, let me look at that."},
	}
	if isExtractionPipelineSession(msgs) {
		t.Fatal("should NOT detect normal session as extraction pipeline")
	}
}
