package storage

import (
	"fmt"
	"testing"
	"time"
)

// insertTestLearningWithTime inserts a learning with a specific created_at time and returns its ID.
func insertTestLearningWithTime(t *testing.T, s *Store, content, category string, createdAt time.Time) int64 {
	t.Helper()

	res, err := s.db.Exec(`INSERT INTO learnings(session_id, category, content, created_at, confidence, model_used, source, embedding_content_hash)
		VALUES (?, ?, ?, ?, 1.0, 'test', 'llm_extracted', '')`,
		"test-session", category, content, createdAt.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert test learning: %v", err)
	}
	id, _ := res.LastInsertId()

	// FTS insert so bm25() can find it
	s.db.Exec(`INSERT INTO learnings_fts(rowid, content) VALUES (?, ?)`, id, content)
	return id
}

// insertAnticipatedQuery inserts an anticipated query for a learning into both
// the junction table and the FTS virtual table.
func insertAnticipatedQuery(t *testing.T, s *Store, learningID int64, value string) {
	t.Helper()

	_, err := s.db.Exec(`INSERT INTO learning_anticipated_queries (learning_id, value) VALUES (?, ?)`, learningID, value)
	if err != nil {
		t.Fatalf("insert learning_aq: %v", err)
	}
	_, err = s.db.Exec(`INSERT INTO anticipated_queries_fts (value, learning_id) VALUES (?, ?)`, value, learningID)
	if err != nil {
		t.Fatalf("insert aq_fts: %v", err)
	}
}

func TestSearchAnticipatedQueries_NoDateFilter(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	oneDayAgo := now.Add(-24 * time.Hour)

	oldID := insertTestLearningWithTime(t, s, "old cache bug in SQLite", "gotcha", oneDayAgo)
	newID := insertTestLearningWithTime(t, s, "new cache bug in SQLite", "gotcha", now.Add(-30*time.Minute))
	insertAnticipatedQuery(t, s, oldID, "cache bug sqlite fix")
	insertAnticipatedQuery(t, s, newID, "cache bug sqlite fix")

	results, err := s.SearchAnticipatedQueries("cache bug sqlite fix", "", 10, "", "")
	if err != nil {
		t.Fatalf("SearchAnticipatedQueries: %v", err)
	}

	foundOld, foundNew := false, false
	for _, r := range results {
		if r.ID == fmt.Sprintf("%d", oldID) {
			foundOld = true
		}
		if r.ID == fmt.Sprintf("%d", newID) {
			foundNew = true
		}
	}
	if !foundOld || !foundNew {
		t.Fatalf("without date filter: expected both old(%d) and new(%d), got old=%v new=%v", oldID, newID, foundOld, foundNew)
	}
}

func TestSearchAnticipatedQueries_SinceFilter(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	oneHourAgo := now.Add(-1 * time.Hour)
	oneDayAgo := now.Add(-24 * time.Hour)

	oldID := insertTestLearningWithTime(t, s, "old SSL cert expiry handling", "gotcha", oneDayAgo)
	newID := insertTestLearningWithTime(t, s, "new SSL cert expiry handling", "gotcha", now.Add(-30*time.Minute))
	insertAnticipatedQuery(t, s, oldID, "ssl cert handling")
	insertAnticipatedQuery(t, s, newID, "ssl cert handling")

	since := oneHourAgo.Format(time.RFC3339)
	results, err := s.SearchAnticipatedQueries("ssl cert handling", "", 10, since, "")
	if err != nil {
		t.Fatalf("SearchAnticipatedQueries with since: %v", err)
	}

	for _, r := range results {
		if r.ID == fmt.Sprintf("%d", oldID) {
			t.Fatalf("old learning(%d) created at %s should NOT appear with since=%s", oldID, oneDayAgo.Format(time.RFC3339), since)
		}
	}
	foundNew := false
	for _, r := range results {
		if r.ID == fmt.Sprintf("%d", newID) {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("new learning(%d) created at %s SHOULD appear with since=%s", newID, now.Add(-30*time.Minute).Format(time.RFC3339), since)
	}
}

func TestSearchAnticipatedQueries_BeforeFilter(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	oneHourAgo := now.Add(-1 * time.Hour)

	oldID := insertTestLearningWithTime(t, s, "ancient deployment recipe for k8s", "pattern", now.Add(-2*time.Hour))
	newID := insertTestLearningWithTime(t, s, "latest deployment recipe for k8s", "pattern", now)
	insertAnticipatedQuery(t, s, oldID, "deployment recipe k8s")
	insertAnticipatedQuery(t, s, newID, "deployment recipe k8s")

	before := oneHourAgo.Format(time.RFC3339)
	results, err := s.SearchAnticipatedQueries("deployment recipe k8s", "", 10, "", before)
	if err != nil {
		t.Fatalf("SearchAnticipatedQueries with before: %v", err)
	}

	for _, r := range results {
		if r.ID == fmt.Sprintf("%d", newID) {
			t.Fatalf("new learning(%d) created at %s should NOT appear with before=%s", newID, now.Format(time.RFC3339), before)
		}
	}
	foundOld := false
	for _, r := range results {
		if r.ID == fmt.Sprintf("%d", oldID) {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("old learning(%d) created at %s SHOULD appear with before=%s", oldID, now.Add(-2*time.Hour).Format(time.RFC3339), before)
	}
}

func TestSearchAnticipatedQueries_EmptyWithDateMismatch(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()

	oldID := insertTestLearningWithTime(t, s, "very old redis connection pool fix", "gotcha", now.Add(-7*24*time.Hour))
	insertAnticipatedQuery(t, s, oldID, "redis connection pool fix")

	// Search for a time window where no learnings exist
	since := now.Add(-1 * time.Hour).Format(time.RFC3339)
	results, err := s.SearchAnticipatedQueries("redis connection pool fix", "", 10, since, "")
	if err != nil {
		t.Fatalf("SearchAnticipatedQueries: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty window, got %d", len(results))
	}
}
