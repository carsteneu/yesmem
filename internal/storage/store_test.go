package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAndClose(t *testing.T) {
	s := mustOpen(t)
	if s.DB() == nil {
		t.Error("db should not be nil")
	}
}

func TestProxyStateUsesRuntimeDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "yesmem.db")

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.SetProxyState("calibrator_ratio", "1.234"); err != nil {
		t.Fatalf("set proxy state: %v", err)
	}

	var mainCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM proxy_state`).Scan(&mainCount); err != nil {
		t.Fatalf("count main proxy_state: %v", err)
	}
	if mainCount != 0 {
		t.Fatalf("main db proxy_state should stay empty, got %d rows", mainCount)
	}

	runtimeDB, err := sql.Open("sqlite", filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open runtime db: %v", err)
	}
	defer runtimeDB.Close()

	var runtimeCount int
	if err := runtimeDB.QueryRow(`SELECT COUNT(*) FROM proxy_state`).Scan(&runtimeCount); err != nil {
		t.Fatalf("count runtime proxy_state: %v", err)
	}
	if runtimeCount != 1 {
		t.Fatalf("runtime db proxy_state should have 1 row, got %d", runtimeCount)
	}
}

func TestSessionCRUD(t *testing.T) {
	s := mustOpen(t)

	sess := &models.Session{
		ID: "sess-001", Project: "/var/www/test", ProjectShort: "test",
		GitBranch: "main", FirstMessage: "Fix the bug",
		MessageCount: 10, StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/path/to/sess.jsonl", JSONLSize: 1024,
		IndexedAt: time.Now().Truncate(time.Second),
	}
	if err := s.UpsertSession(sess); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetSession("sess-001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProjectShort != "test" {
		t.Errorf("project_short: got %q, want %q", got.ProjectShort, "test")
	}
	if got.FirstMessage != "Fix the bug" {
		t.Errorf("first_message: got %q, want %q", got.FirstMessage, "Fix the bug")
	}
}

func TestSessionList(t *testing.T) {
	s := mustOpen(t)

	for i, id := range []string{"a", "b", "c"} {
		s.UpsertSession(&models.Session{
			ID: id, Project: "/test", ProjectShort: "test",
			StartedAt: time.Now().Add(time.Duration(i) * time.Hour),
			JSONLPath: "/test/" + id + ".jsonl",
			IndexedAt: time.Now(),
		})
	}

	list, err := s.ListSessions("test", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2, got %d", len(list))
	}
	// Should be ordered DESC
	if list[0].ID != "c" {
		t.Errorf("expected first session 'c', got '%s'", list[0].ID)
	}
}

func TestMessageCRUD(t *testing.T) {
	s := mustOpen(t)

	s.UpsertSession(&models.Session{
		ID: "sess-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now(), JSONLPath: "/test.jsonl", IndexedAt: time.Now(),
	})

	msgs := []models.Message{
		{SessionID: "sess-001", Role: "user", MessageType: "text",
			Content: "Hello", Timestamp: time.Now(), Sequence: 0},
		{SessionID: "sess-001", Role: "assistant", MessageType: "text",
			Content: "Hi there", Timestamp: time.Now(), Sequence: 1},
		{SessionID: "sess-001", SourceAgent: models.SourceAgentCodex, Role: "assistant", MessageType: "tool_use",
			ToolName: "Bash", Content: "ls -la", Timestamp: time.Now(), Sequence: 2},
	}
	if err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.GetMessagesBySession("sess-001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 messages, got %d", len(got))
	}
	if got[0].SourceAgent != models.SourceAgentClaude {
		t.Errorf("message[0] source_agent: got %q, want %q", got[0].SourceAgent, models.SourceAgentClaude)
	}
	if got[2].SourceAgent != models.SourceAgentCodex {
		t.Errorf("message[2] source_agent: got %q, want %q", got[2].SourceAgent, models.SourceAgentCodex)
	}

	byType, err := s.GetMessagesBySessionAndType("sess-001", "tool_use")
	if err != nil {
		t.Fatalf("get by type: %v", err)
	}
	if len(byType) != 1 {
		t.Errorf("expected 1 tool_use, got %d", len(byType))
	}
}

func TestLearningCRUD(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "Docker needs no sudo",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	learnings, err := s.GetActiveLearnings("", "", "", "")
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if len(learnings) != 1 {
		t.Fatalf("expected 1, got %d", len(learnings))
	}
	if learnings[0].Content != "Docker needs no sudo" {
		t.Errorf("content mismatch: %q", learnings[0].Content)
	}
}

func TestLearningSupersede(t *testing.T) {
	s := mustOpen(t)

	id1, _ := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "Use Redis",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus",
	})
	id2, _ := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "Switch to Memcached",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus",
	})

	if err := s.SupersedeLearning(id1, id2, "Performance reasons"); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	active, _ := s.GetActiveLearnings("decision", "", "", "")
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}
	if active[0].Content != "Switch to Memcached" {
		t.Errorf("wrong active learning: %q", active[0].Content)
	}
}

func TestResolveLearning(t *testing.T) {
	s := mustOpen(t)

	// Insert an unfinished item
	id, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Implement TTL filter",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Resolve it
	err := s.ResolveLearning(id, "completed in commit abc123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Should no longer appear in active learnings
	active, _ := s.GetActiveLearnings("unfinished", "memory", "", "")
	if len(active) != 0 {
		t.Errorf("expected 0 active unfinished, got %d", len(active))
	}

	// Verify the learning has superseded_by = -2
	l, _ := s.GetLearning(id)
	if l.SupersededBy == nil || *l.SupersededBy != -2 {
		t.Errorf("expected superseded_by=-2, got %v", l.SupersededBy)
	}
	if l.SupersedeReason != "completed in commit abc123" {
		t.Errorf("expected reason, got %q", l.SupersedeReason)
	}
}

func TestResolveLearningAlreadySuperseded(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Old task",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Supersede it first
	s.SupersedeLearning(id, -1, "junk")

	// Trying to resolve should fail
	err := s.ResolveLearning(id, "trying to resolve")
	if err == nil {
		t.Fatal("expected error resolving already-superseded learning")
	}
}

func TestResolveLearningNotFound(t *testing.T) {
	s := mustOpen(t)

	err := s.ResolveLearning(99999, "does not exist")
	if err == nil {
		t.Fatal("expected error resolving non-existent learning")
	}
}

func TestSearchUnfinished(t *testing.T) {
	s := mustOpen(t)

	// Insert some unfinished items
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Implement TTL filter for open work items",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Fix remember flow not working",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Add dark mode to frontend",
		Project: "other", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Search should find matching items
	results, err := s.SearchUnfinished("remember flow", "memory")
	if err != nil {
		t.Fatalf("search unfinished: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "Fix remember flow not working" {
		t.Errorf("wrong match: %q", results[0].Content)
	}

	// Search across all projects
	results, err = s.SearchUnfinished("dark mode", "")
	if err != nil {
		t.Fatalf("search unfinished: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// No match
	results, err = s.SearchUnfinished("nonexistent xyz", "memory")
	if err != nil {
		t.Fatalf("search unfinished: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGetStaleUnfinished(t *testing.T) {
	s := mustOpen(t)

	// Insert items: one recent, one old
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Recent task",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Old task from weeks ago",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now().AddDate(0, 0, -45), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Another old task",
		Project: "other", Confidence: 1.0, CreatedAt: time.Now().AddDate(0, 0, -60), ModelUsed: "haiku",
	})

	// Get all active unfinished (no age filter)
	all, _ := s.GetActiveLearnings("unfinished", "", "", "")
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}

	// Resolve by IDs in batch
	err := s.ResolveBatch([]int64{all[1].ID, all[2].ID}, "batch-cleanup: stale")
	if err != nil {
		t.Fatalf("resolve batch: %v", err)
	}

	// Only the recent one should remain
	remaining, _ := s.GetActiveLearnings("unfinished", "", "", "")
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(remaining))
	}
	if remaining[0].Content != "Recent task" {
		t.Errorf("wrong remaining: %q", remaining[0].Content)
	}
}

func TestMatchUnfinishedByKeywords(t *testing.T) {
	s := mustOpen(t)

	id1, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Implement TTL filter for open work items",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	id2, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Fix remember flow not working",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Add dark mode to frontend",
		Project: "other", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Commit message "NEW: unfinished TTL" should match TTL item
	matches, _ := s.SearchUnfinished("TTL", "")
	if len(matches) != 1 || matches[0].ID != id1 {
		t.Errorf("expected TTL match on #%d, got %v", id1, matches)
	}

	// "fix remember" should match remember flow
	matches, _ = s.SearchUnfinished("remember", "")
	if len(matches) != 1 || matches[0].ID != id2 {
		t.Errorf("expected remember match on #%d, got %v", id2, matches)
	}

	// "refactor CSS" should match nothing
	matches, _ = s.SearchUnfinished("refactor CSS", "")
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
}

func TestSyncFTSIncludesTriggerRule(t *testing.T) {
	s := mustOpen(t)

	// Insert learning with trigger_rule that contains search terms NOT in content
	s.InsertLearning(&models.Learning{
		Category:    "gotcha",
		Content:     "Reddit gibt 403. Workaround: curl mit User-Agent",
		TriggerRule: "webfetch www.reddit.com unable fetch",
		Project:     "yesmem",
		Confidence:  1.0,
		CreatedAt:   time.Now(),
		ModelUsed:   "test",
	})

	// Sync FTS5
	s.SyncFTSNow()

	// Search for terms only in trigger_rule, not in content
	var count int
	s.DB().QueryRow(`SELECT COUNT(*) FROM learnings_fts WHERE learnings_fts MATCH 'webfetch'`).Scan(&count)
	if count == 0 {
		t.Error("FTS5 should index trigger_rule text — 'webfetch' not found")
	}
}

func TestSearchUnfinishedFTS5(t *testing.T) {
	s := mustOpen(t)

	// The original problem: "Signal-to-Noise verbessern" should find
	// "Signal-to-Noise 60/40→90/10" even though "verbessern" is NOT in the content.
	// LIKE with AND-logic fails here. FTS5 with OR-logic succeeds.
	id1, _ := s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Signal-to-Noise 60/40→90/10",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "Add user profile page",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// "Signal-to-Noise verbessern" — "verbessern" NOT in content,
	// but "Signal" and "Noise" ARE → should still find it (OR-match)
	matches, err := s.SearchUnfinished("Signal-to-Noise verbessern", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for 'Signal-to-Noise verbessern', got %d", len(matches))
	}
	if matches[0].ID != id1 {
		t.Errorf("expected ID %d, got %d", id1, matches[0].ID)
	}
}

func TestAssociations(t *testing.T) {
	s := mustOpen(t)

	assocs := []models.Association{
		{SourceType: "session", SourceID: "s1", TargetType: "file", TargetID: "/etc/nginx/conf", Weight: 1},
		{SourceType: "session", SourceID: "s1", TargetType: "project", TargetID: "ccm19", Weight: 1},
		{SourceType: "session", SourceID: "s2", TargetType: "file", TargetID: "/etc/nginx/conf", Weight: 1},
	}
	if err := s.InsertAssociationBatch(assocs); err != nil {
		t.Fatalf("batch insert: %v", err)
	}

	from, err := s.GetAssociationsFrom("session", "s1")
	if err != nil {
		t.Fatalf("get from: %v", err)
	}
	if len(from) != 2 {
		t.Errorf("expected 2 edges from s1, got %d", len(from))
	}

	to, err := s.GetAssociationsTo("file", "/etc/nginx/conf")
	if err != nil {
		t.Fatalf("get to: %v", err)
	}
	if len(to) != 2 {
		t.Errorf("expected 2 edges to /etc/nginx/conf, got %d", len(to))
	}
}

func TestIndexState(t *testing.T) {
	s := mustOpen(t)

	now := time.Now().Truncate(time.Second)
	if !s.NeedsReindex("/test.jsonl", 1024, now) {
		t.Error("new file should need reindex")
	}

	s.UpsertIndexState(&models.IndexState{
		JSONLPath: "/test.jsonl", FileSize: 1024, FileMtime: now, IndexedAt: now,
	})

	if s.NeedsReindex("/test.jsonl", 1024, now) {
		t.Error("same file should NOT need reindex")
	}
	if !s.NeedsReindex("/test.jsonl", 2048, now) {
		t.Error("changed size should need reindex")
	}
}

func TestListProjects(t *testing.T) {
	s := mustOpen(t)

	s.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		StartedAt: time.Now(), JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})
	s.UpsertSession(&models.Session{
		ID: "s2", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		StartedAt: time.Now(), JSONLPath: "/s2.jsonl", IndexedAt: time.Now(),
	})
	s.UpsertSession(&models.Session{
		ID: "s3", Project: "/var/www/green", ProjectShort: "green",
		StartedAt: time.Now(), JSONLPath: "/s3.jsonl", IndexedAt: time.Now(),
	})

	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	found := false
	for _, p := range projects {
		if p.ProjectShort == "ccm19" && p.SessionCount == 2 {
			found = true
		}
	}
	if !found {
		t.Error("expected ccm19 with 2 sessions")
	}
}

func TestGetRecentNarratives(t *testing.T) {
	s := mustOpen(t)

	// Insert 4 narratives for "yesmem" project
	for i, content := range []string{
		"Session 1: Setup gebaut",
		"Session 2: Dedup implementiert",
		"Session 3: Templates erstellt",
		"Session 4: i18n hinzugefügt",
	} {
		s.InsertLearning(&models.Learning{
			Category:  "narrative",
			Content:   content,
			Project:   "yesmem",
			SessionID: fmt.Sprintf("s%d", i+1),
			CreatedAt: time.Now().Add(-time.Duration(4-i) * time.Hour),
			ModelUsed: "haiku",
		})
	}

	// Insert 1 narrative for different project
	s.InsertLearning(&models.Learning{
		Category:  "narrative",
		Content:   "Anderes Projekt",
		Project:   "greenWebsite",
		SessionID: "s99",
		CreatedAt: time.Now(),
		ModelUsed: "haiku",
	})

	// Should return newest 3 for yesmem
	narratives, err := s.GetRecentNarratives("yesmem", 3)
	if err != nil {
		t.Fatalf("get narratives: %v", err)
	}
	if len(narratives) != 3 {
		t.Fatalf("expected 3, got %d", len(narratives))
	}
	// Newest first
	if narratives[0].Content != "Session 4: i18n hinzugefügt" {
		t.Errorf("expected newest first, got %q", narratives[0].Content)
	}
	// Should NOT contain greenWebsite narrative
	for _, n := range narratives {
		if n.Project == "greenWebsite" {
			t.Error("should not contain other project's narrative")
		}
	}
}

func TestGetRecentNarrativesEmpty(t *testing.T) {
	s := mustOpen(t)
	narratives, err := s.GetRecentNarratives("nonexistent", 3)
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if len(narratives) != 0 {
		t.Errorf("expected 0, got %d", len(narratives))
	}
}

func TestSupersedeSessionLearnings(t *testing.T) {
	s := mustOpen(t)

	sid := "sess-reextract"

	// Insert auto-extracted learnings (should be deleted)
	s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "gotcha", Content: "Auto gotcha",
		Source: "llm_extracted", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "decision", Content: "Auto decision",
		Source: "", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "narrative", Content: "Auto narrative",
		Source: "llm_extracted", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Insert user-stated learnings (should be preserved)
	s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "preference", Content: "User preference",
		Source: "user_stated", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
	})
	s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "preference", Content: "User override",
		Source: "user_override", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
	})

	// Insert learning for different session (should not be affected)
	s.InsertLearning(&models.Learning{
		SessionID: "other-session", Category: "gotcha", Content: "Other session gotcha",
		Source: "llm_extracted", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	// Supersede auto-extracted learnings for our session
	supersededIDs, err := s.SupersedeSessionLearnings(sid)
	if err != nil {
		t.Fatalf("delete session learnings: %v", err)
	}
	if len(supersededIDs) != 3 {
		t.Errorf("expected 3 superseded, got %d", len(supersededIDs))
	}

	// Check: user-stated learnings preserved (still active)
	all, _ := s.GetActiveLearnings("", "", "", "")
	var preserved, otherSession int
	for _, l := range all {
		if l.SessionID == sid {
			preserved++
			if l.Source != "user_stated" && l.Source != "user_override" {
				t.Errorf("non-user learning still active: source=%q content=%q", l.Source, l.Content)
			}
		}
		if l.SessionID == "other-session" {
			otherSession++
		}
	}
	if preserved != 2 {
		t.Errorf("expected 2 preserved user learnings, got %d", preserved)
	}
	if otherSession != 1 {
		t.Errorf("expected 1 other-session learning untouched, got %d", otherSession)
	}
}

func TestMigrateHitCounts(t *testing.T) {
	s := mustOpen(t)
	sid := "sess-migrate"
	now := time.Now()

	// Insert old learning with hits, then supersede it
	oldID, _ := s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "gotcha", Content: "Old gotcha",
		Source: "llm_extracted", Confidence: 1.0, CreatedAt: now, ModelUsed: "haiku",
	})
	s.IncrementHitCounts([]int64{oldID})
	s.IncrementHitCounts([]int64{oldID})
	s.IncrementHitCounts([]int64{oldID}) // 3 hits

	// Supersede as reextract
	s.SupersedeSessionLearnings(sid)

	// Insert new replacement learning (hit_count=0)
	newID, _ := s.InsertLearning(&models.Learning{
		SessionID: sid, Category: "gotcha", Content: "New gotcha with pivot",
		Source: "llm_extracted", Confidence: 1.0, CreatedAt: now, ModelUsed: "haiku",
	})

	// Migrate
	migrated, err := s.MigrateHitCounts()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if migrated != 1 {
		t.Errorf("expected 1 migrated, got %d", migrated)
	}

	// Verify new learning has the hits
	newL, err := s.GetLearning(newID)
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if newL.HitCount != 3 {
		t.Errorf("expected hit_count=3, got %d", newL.HitCount)
	}
	if newL.LastHitAt == nil {
		t.Error("expected last_hit_at to be migrated")
	}
}

func TestSessionsWithoutFlavor(t *testing.T) {
	s := mustOpen(t)

	s.UpsertSession(&models.Session{
		ID: "s1", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), MessageCount: 20, JSONLPath: "/s1.jsonl", IndexedAt: time.Now(),
	})
	s.UpsertSession(&models.Session{
		ID: "s2", Project: "/var/www/proj", ProjectShort: "proj",
		StartedAt: time.Now(), MessageCount: 15, JSONLPath: "/s2.jsonl", IndexedAt: time.Now(),
	})

	// s1 has learnings without flavor, s2 has learnings with flavor
	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "gotcha", Content: "no flavor here",
		Project: "proj", CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		SessionID: "s2", Category: "gotcha", Content: "has flavor",
		Project: "proj", CreatedAt: time.Now(), ModelUsed: "haiku",
		SessionFlavor: "Sauberer Sprint",
	})

	ids, err := s.SessionsWithoutFlavor(0)
	if err != nil {
		t.Fatalf("sessions without flavor: %v", err)
	}
	if len(ids) != 1 || ids[0] != "s1" {
		t.Errorf("expected [s1], got %v", ids)
	}
}

func TestUpdateSessionFlavor(t *testing.T) {
	s := mustOpen(t)

	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "gotcha", Content: "test1",
		CreatedAt: time.Now(), ModelUsed: "haiku",
	})
	s.InsertLearning(&models.Learning{
		SessionID: "s1", Category: "decision", Content: "test2",
		CreatedAt: time.Now(), ModelUsed: "haiku",
	})

	n, err := s.UpdateSessionFlavor("s1", "Intensiver Debug-Marathon")
	if err != nil {
		t.Fatalf("update flavor: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 updated, got %d", n)
	}

	// Verify
	learnings, _ := s.GetActiveLearnings("", "", "", "")
	for _, l := range learnings {
		if l.SessionID == "s1" && l.SessionFlavor != "Intensiver Debug-Marathon" {
			t.Errorf("expected flavor on learning %d, got %q", l.ID, l.SessionFlavor)
		}
	}
}

func TestGetSubagentCounts(t *testing.T) {
	s := mustOpen(t)

	// Parent session with 2 subagents
	s.UpsertSession(&models.Session{
		ID: "parent-a", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/parent-a.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})
	s.UpsertSession(&models.Session{
		ID: "sub-a1", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/sub-a1.jsonl", IndexedAt: time.Now().Truncate(time.Second),
		ParentSessionID: "parent-a", AgentType: "Explore",
	})
	s.UpsertSession(&models.Session{
		ID: "sub-a2", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/sub-a2.jsonl", IndexedAt: time.Now().Truncate(time.Second),
		ParentSessionID: "parent-a", AgentType: "general-purpose",
	})

	// Parent session with 0 subagents
	s.UpsertSession(&models.Session{
		ID: "parent-b", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/parent-b.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})

	counts, err := s.GetSubagentCounts([]string{"parent-a", "parent-b"})
	if err != nil {
		t.Fatalf("GetSubagentCounts: %v", err)
	}

	if counts["parent-a"] != 2 {
		t.Errorf("parent-a: expected 2 subagents, got %d", counts["parent-a"])
	}
	if counts["parent-b"] != 0 {
		// parent-b has no subagents, so it should either be absent or 0
		if c, ok := counts["parent-b"]; ok && c != 0 {
			t.Errorf("parent-b: expected 0 or absent, got %d", c)
		}
	}

	// Empty input should return nil
	empty, err := s.GetSubagentCounts([]string{})
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil for empty input, got %v", empty)
	}
}

func TestGetSessionMetaBulk(t *testing.T) {
	s := mustOpen(t)

	// Insert a parent session (no parent_session_id, no agent_type)
	s.UpsertSession(&models.Session{
		ID: "parent-m", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/parent-m.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})

	// Insert a subagent session with agent_type and parent
	s.UpsertSession(&models.Session{
		ID: "sub-m1", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/sub-m1.jsonl", IndexedAt: time.Now().Truncate(time.Second),
		ParentSessionID: "parent-m", AgentType: "explore",
	})

	meta, err := s.GetSessionMetaBulk([]string{"sub-m1", "nonexistent-id"})
	if err != nil {
		t.Fatalf("GetSessionMetaBulk: %v", err)
	}

	// sub-m1 should be present with correct fields
	m, ok := meta["sub-m1"]
	if !ok {
		t.Fatal("expected sub-m1 in meta map")
	}
	if m.AgentType != "explore" {
		t.Errorf("agent_type: got %q, want 'explore'", m.AgentType)
	}
	if m.ParentSessionID != "parent-m" {
		t.Errorf("parent_session_id: got %q, want 'parent-m'", m.ParentSessionID)
	}

	// nonexistent-id should be absent
	if _, ok := meta["nonexistent-id"]; ok {
		t.Error("nonexistent-id should not be in meta map")
	}

	// Plain sessions are still returned because project/source metadata is used
	// to enrich normal search hits, even when parent/agent_type are empty.
	meta2, err := s.GetSessionMetaBulk([]string{"parent-m"})
	if err != nil {
		t.Fatalf("GetSessionMetaBulk for parent: %v", err)
	}
	parentMeta, ok := meta2["parent-m"]
	if !ok {
		t.Fatal("expected parent-m in meta map")
	}
	if parentMeta.ParentSessionID != "" {
		t.Errorf("parent parent_session_id: got %q, want empty", parentMeta.ParentSessionID)
	}
	if parentMeta.AgentType != "" {
		t.Errorf("parent agent_type: got %q, want empty", parentMeta.AgentType)
	}
	if parentMeta.Project != "/test" {
		t.Errorf("parent project: got %q, want /test", parentMeta.Project)
	}
	if parentMeta.SourceAgent != models.SourceAgentClaude {
		t.Errorf("parent source_agent: got %q, want %q", parentMeta.SourceAgent, models.SourceAgentClaude)
	}

	// Empty input should return nil
	empty, err := s.GetSessionMetaBulk([]string{})
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil for empty input, got %v", empty)
	}
}

func TestSubagentUpsertAndFilter(t *testing.T) {
	s := mustOpen(t)

	// Parent session
	parent := &models.Session{
		ID: "parent-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second),
		JSONLPath: "/test/parent.jsonl", IndexedAt: time.Now().Truncate(time.Second),
		MessageCount: 50,
	}
	if err := s.UpsertSession(parent); err != nil {
		t.Fatalf("upsert parent: %v", err)
	}

	// Subagent sessions
	for _, sub := range []struct {
		id, agentType string
		msgs          int
	}{
		{"sub-001", "Explore", 12},
		{"sub-002", "Explore", 8},
		{"sub-003", "general-purpose", 25},
	} {
		sess := &models.Session{
			ID: sub.id, Project: "/test", ProjectShort: "test",
			StartedAt: time.Now().Truncate(time.Second),
			JSONLPath: "/test/" + sub.id + ".jsonl", IndexedAt: time.Now().Truncate(time.Second),
			ParentSessionID: "parent-001", AgentType: sub.agentType,
			MessageCount: sub.msgs,
		}
		if err := s.UpsertSession(sess); err != nil {
			t.Fatalf("upsert sub %s: %v", sub.id, err)
		}
	}

	// ListSessions should exclude subagents
	list, err := s.ListSessions("test", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListSessions: expected 1 (parent only), got %d", len(list))
	}
	if list[0].ID != "parent-001" {
		t.Errorf("expected parent-001, got %s", list[0].ID)
	}

	// GetSession should return subagent fields
	got, err := s.GetSession("sub-001")
	if err != nil {
		t.Fatalf("get sub: %v", err)
	}
	if got.ParentSessionID != "parent-001" {
		t.Errorf("parent_session_id: got %q, want 'parent-001'", got.ParentSessionID)
	}
	if got.AgentType != "Explore" {
		t.Errorf("agent_type: got %q, want 'Explore'", got.AgentType)
	}

	// GetSubagentSummary
	summaries, err := s.GetSubagentSummary("parent-001")
	if err != nil {
		t.Fatalf("subagent summary: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 agent types, got %d", len(summaries))
	}
	// Find Explore summary
	var exploreSummary SubagentSummary
	for _, sum := range summaries {
		if sum.AgentType == "Explore" {
			exploreSummary = sum
		}
	}
	if exploreSummary.Count != 2 {
		t.Errorf("Explore count: got %d, want 2", exploreSummary.Count)
	}
	if exploreSummary.TotalMessages != 20 {
		t.Errorf("Explore total messages: got %d, want 20", exploreSummary.TotalMessages)
	}

	// ListProjects should not double-count subagents
	projects, err := s.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].SessionCount != 1 {
		t.Errorf("project session count: got %d, want 1 (excluding subagents)", projects[0].SessionCount)
	}
}

func TestUpsertSessionResetsExtractedWhenGrowing(t *testing.T) {
	s := mustOpen(t)

	// Insert a short session (3 messages)
	s.UpsertSession(&models.Session{
		ID: "grow-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second), MessageCount: 3,
		JSONLPath: "/test/grow.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})

	// Mark it as extracted (simulating MarkShortSessionsExtracted)
	s.MarkSessionExtracted("grow-001")
	got, _ := s.GetSession("grow-001")
	if got.ExtractedAt.IsZero() {
		t.Fatal("expected extracted_at to be set")
	}

	// Re-index with 10 messages — should reset extracted_at
	s.UpsertSession(&models.Session{
		ID: "grow-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second), MessageCount: 10,
		JSONLPath: "/test/grow.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})
	got, _ = s.GetSession("grow-001")
	if !got.ExtractedAt.IsZero() {
		t.Errorf("expected extracted_at to be reset when session grew past threshold, got %v", got.ExtractedAt)
	}
}

func TestUpsertSessionPreservesExtractedForLargeSessions(t *testing.T) {
	s := mustOpen(t)

	// Insert a large session (20 messages)
	s.UpsertSession(&models.Session{
		ID: "large-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second), MessageCount: 20,
		JSONLPath: "/test/large.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})
	s.MarkSessionExtracted("large-001")

	// Re-index with 25 messages — should preserve extracted_at
	s.UpsertSession(&models.Session{
		ID: "large-001", Project: "/test", ProjectShort: "test",
		StartedAt: time.Now().Truncate(time.Second), MessageCount: 25,
		JSONLPath: "/test/large.jsonl", IndexedAt: time.Now().Truncate(time.Second),
	})
	got, _ := s.GetSession("large-001")
	if got.ExtractedAt.IsZero() {
		t.Error("expected extracted_at to be preserved for already-extracted large session")
	}
}

func TestMarkShortSessionsExtracted(t *testing.T) {
	s := mustOpen(t)

	// Insert sessions of various sizes
	for _, tc := range []struct {
		id   string
		msgs int
	}{
		{"short-1", 2},
		{"short-2", 5},
		{"medium-1", 6},
		{"large-1", 20},
	} {
		s.UpsertSession(&models.Session{
			ID: tc.id, Project: "/test", ProjectShort: "test",
			StartedAt: time.Now().Truncate(time.Second), MessageCount: tc.msgs,
			JSONLPath: "/test/" + tc.id + ".jsonl", IndexedAt: time.Now().Truncate(time.Second),
		})
	}

	count, err := s.MarkShortSessionsExtracted(5)
	if err != nil {
		t.Fatalf("mark short: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 marked, got %d", count)
	}

	// Short sessions should be extracted
	for _, id := range []string{"short-1", "short-2"} {
		got, _ := s.GetSession(id)
		if got.ExtractedAt.IsZero() {
			t.Errorf("%s: expected extracted_at to be set", id)
		}
	}
	// Larger sessions should not be touched
	for _, id := range []string{"medium-1", "large-1"} {
		got, _ := s.GetSession(id)
		if !got.ExtractedAt.IsZero() {
			t.Errorf("%s: expected extracted_at to remain unset", id)
		}
	}
}

func TestCountPendingExtractions(t *testing.T) {
	s := mustOpen(t)

	for _, tc := range []struct {
		id        string
		msgs      int
		extracted bool
	}{
		{"pending-1", 10, false},
		{"pending-2", 20, false},
		{"done-1", 15, true},
		{"short-1", 3, false},
	} {
		s.UpsertSession(&models.Session{
			ID: tc.id, Project: "/test", ProjectShort: "test",
			StartedAt: time.Now().Truncate(time.Second), MessageCount: tc.msgs,
			JSONLPath: "/test/" + tc.id + ".jsonl", IndexedAt: time.Now().Truncate(time.Second),
		})
		if tc.extracted {
			s.MarkSessionExtracted(tc.id)
		}
	}

	count, err := s.CountPendingExtractions(5)
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 pending extractions, got %d", count)
	}
}
