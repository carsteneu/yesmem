package storage

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestLearningClusters_InsertAndGet(t *testing.T) {
	s := mustOpen(t)

	clusters := []models.LearningCluster{
		{
			Project:        "yesmem",
			Label:          "Proxy-Architektur",
			LearningCount:  5,
			AvgRecencyDays: 3.2,
			AvgHitCount:    2.5,
			Confidence:     0.85,
			LearningIDs:    "[1,2,3,4,5]",
		},
		{
			Project:        "yesmem",
			Label:          "Persona-Engine",
			LearningCount:  3,
			AvgRecencyDays: 7.0,
			AvgHitCount:    1.0,
			Confidence:     0.92,
			LearningIDs:    "[10,11,12]",
		},
	}

	if err := s.ReplaceLearningClusters("yesmem", clusters); err != nil {
		t.Fatalf("replace clusters: %v", err)
	}

	got, err := s.GetLearningClusters("yesmem")
	if err != nil {
		t.Fatalf("get clusters: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(got))
	}

	// Ordered by confidence DESC: Persona-Engine (0.92) first, then Proxy-Architektur (0.85)
	if got[0].Label != "Persona-Engine" {
		t.Errorf("expected highest confidence first, got %q (confidence=%.2f)", got[0].Label, got[0].Confidence)
	}
	if got[1].Label != "Proxy-Architektur" {
		t.Errorf("expected second cluster Proxy-Architektur, got %q", got[1].Label)
	}

	// Verify fields
	if got[0].LearningCount != 3 {
		t.Errorf("learning_count = %d, want 3", got[0].LearningCount)
	}
	if got[0].LearningIDs != "[10,11,12]" {
		t.Errorf("learning_ids = %q, want [10,11,12]", got[0].LearningIDs)
	}
	if got[1].AvgRecencyDays != 3.2 {
		t.Errorf("avg_recency_days = %f, want 3.2", got[1].AvgRecencyDays)
	}

	// Different project should return empty
	other, err := s.GetLearningClusters("other")
	if err != nil {
		t.Fatalf("get other clusters: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("expected 0 clusters for other project, got %d", len(other))
	}
}

func TestLearningClusters_ReplaceDeletesOld(t *testing.T) {
	s := mustOpen(t)

	// Insert initial clusters
	old := []models.LearningCluster{
		{Project: "yesmem", Label: "Old-Cluster-1", LearningCount: 2, Confidence: 0.5, LearningIDs: "[1,2]"},
		{Project: "yesmem", Label: "Old-Cluster-2", LearningCount: 1, Confidence: 0.3, LearningIDs: "[3]"},
	}
	if err := s.ReplaceLearningClusters("yesmem", old); err != nil {
		t.Fatalf("insert old: %v", err)
	}

	// Replace with new clusters
	fresh := []models.LearningCluster{
		{Project: "yesmem", Label: "New-Cluster", LearningCount: 4, Confidence: 0.95, LearningIDs: "[10,11,12,13]"},
	}
	if err := s.ReplaceLearningClusters("yesmem", fresh); err != nil {
		t.Fatalf("replace: %v", err)
	}

	got, err := s.GetLearningClusters("yesmem")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 cluster after replace, got %d", len(got))
	}
	if got[0].Label != "New-Cluster" {
		t.Errorf("expected New-Cluster, got %q", got[0].Label)
	}

	// Verify old clusters are gone (not just hidden)
	var count int
	s.DB().QueryRow(`SELECT COUNT(*) FROM learning_clusters WHERE project = 'yesmem'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row in DB, got %d", count)
	}
}

func TestTrackGap_IncrementOnDuplicate(t *testing.T) {
	s := mustOpen(t)

	if err := s.TrackGap("proxy routing", "memory"); err != nil {
		t.Fatalf("first track: %v", err)
	}
	if err := s.TrackGap("proxy routing", "memory"); err != nil {
		t.Fatalf("second track: %v", err)
	}

	gaps, err := s.GetActiveGaps("memory", 10)
	if err != nil {
		t.Fatalf("get gaps: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(gaps))
	}
	if gaps[0].HitCount != 2 {
		t.Errorf("expected hit_count=2, got %d", gaps[0].HitCount)
	}
	if gaps[0].Topic != "proxy routing" {
		t.Errorf("expected topic 'proxy routing', got %q", gaps[0].Topic)
	}
}

func TestResolveGap(t *testing.T) {
	s := mustOpen(t)

	s.TrackGap("proxy routing", "memory")
	if err := s.ResolveGap("proxy routing", "memory", 42); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	gaps, err := s.GetActiveGaps("memory", 10)
	if err != nil {
		t.Fatalf("get gaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("expected 0 active gaps after resolve, got %d", len(gaps))
	}
}

func TestGetActiveGaps_OrderByHitCount(t *testing.T) {
	s := mustOpen(t)

	s.TrackGap("topic-a", "memory")
	s.TrackGap("topic-b", "memory")
	s.TrackGap("topic-b", "memory") // hit_count=2
	s.TrackGap("topic-b", "memory") // hit_count=3

	gaps, err := s.GetActiveGaps("memory", 10)
	if err != nil {
		t.Fatalf("get gaps: %v", err)
	}
	if len(gaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(gaps))
	}
	// topic-b should be first (higher hit_count)
	if gaps[0].Topic != "topic-b" {
		t.Errorf("expected topic-b first (hit_count=3), got %q", gaps[0].Topic)
	}
}

func TestAutoResolveGap_ViaStorage(t *testing.T) {
	s := mustOpen(t)

	// Track a gap about "proxy"
	s.TrackGap("proxy", "memory")

	gaps, _ := s.GetActiveGaps("memory", 10)
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap before resolve, got %d", len(gaps))
	}

	// Insert a learning about proxy
	id, err := s.InsertLearning(&models.Learning{
		Content: "Proxy liegt in internal/mcp", Category: "gotcha",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Manually resolve (simulates what daemon handler does)
	s.ResolveGap("proxy", "memory", id)

	gaps, _ = s.GetActiveGaps("memory", 10)
	if len(gaps) != 0 {
		t.Errorf("expected 0 active gaps after resolve, got %d", len(gaps))
	}
}

func TestGetLearningCounts(t *testing.T) {
	s := mustOpen(t)

	now := time.Now()
	for i := 0; i < 5; i++ {
		s.InsertLearning(&models.Learning{
			Category: "gotcha", Content: fmt.Sprintf("gotcha %d", i),
			Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
		})
	}
	for i := 0; i < 3; i++ {
		s.InsertLearning(&models.Learning{
			Category: "decision", Content: fmt.Sprintf("decision %d", i),
			Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
		})
	}
	// One superseded gotcha — should NOT be counted
	id, _ := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "old gotcha",
		Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
	})
	s.SupersedeLearning(id, 1, "replaced")

	// Unfinished and preference — should be excluded
	s.InsertLearning(&models.Learning{
		Category: "unfinished", Content: "task",
		Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
	})
	s.InsertLearning(&models.Learning{
		Category: "preference", Content: "pref",
		Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
	})

	counts, err := s.GetLearningCounts("proj")
	if err != nil {
		t.Fatal(err)
	}

	if counts["gotcha"] != 5 {
		t.Errorf("gotcha count = %d, want 5", counts["gotcha"])
	}
	if counts["decision"] != 3 {
		t.Errorf("decision count = %d, want 3", counts["decision"])
	}
	if _, ok := counts["unfinished"]; ok {
		t.Error("unfinished should be excluded from counts")
	}
	if _, ok := counts["preference"]; ok {
		t.Error("preference should be excluded from counts")
	}
}

func TestInjectCountsDoesNotGrowStability(t *testing.T) {
	store := mustOpen(t)
	id := insertTestLearning(store, "test no stability growth on inject")

	store.IncrementInjectCounts([]int64{id})
	// Simulate 24h gap
	store.db.Exec(`UPDATE learnings SET last_hit_at = ? WHERE id = ?`,
		time.Now().Add(-25*time.Hour).Format(time.RFC3339), id)
	store.IncrementInjectCounts([]int64{id})

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.Stability != 30.0 {
		t.Errorf("expected stability 30.0 (no growth on inject), got %f", l.Stability)
	}
}

func TestUseCountsGrowsStabilityOnSpacedAccess(t *testing.T) {
	store := newTestStore(t)
	id := insertTestLearning(store, "test stability growth on use")

	store.IncrementUseCounts([]int64{id})
	// Simulate 24h gap by setting last_hit_at
	store.db.Exec(`UPDATE learnings SET last_hit_at = ? WHERE id = ?`,
		time.Now().Add(-25*time.Hour).Format(time.RFC3339), id)
	store.IncrementUseCounts([]int64{id})

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	expected := 30.0 * 1.3
	if l.Stability < expected-1.0 || l.Stability > expected+1.0 {
		t.Errorf("expected stability ~%.1f after spaced use, got %f", expected, l.Stability)
	}
}

func TestStabilityGrowsUnbounded(t *testing.T) {
	store := mustOpen(t)
	id := insertTestLearning(store, "test stability no cap")

	// Set stability high
	store.db.Exec(`UPDATE learnings SET stability = 300.0 WHERE id = ?`, id)

	// Simulate gap and use — 300 * 1.3 = 390 (no cap anymore)
	store.db.Exec(`UPDATE learnings SET last_hit_at = ? WHERE id = ?`,
		time.Now().Add(-25*time.Hour).Format(time.RFC3339), id)
	store.IncrementUseCounts([]int64{id})

	l, err := store.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	expected := 300.0 * 1.3 // 390
	if l.Stability < expected-1.0 || l.Stability > expected+1.0 {
		t.Errorf("stability should grow unbounded to ~%.0f, got %f", expected, l.Stability)
	}
}

func newTestLearning(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "test learning for 5-count model",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	if err != nil {
		t.Fatalf("insert test learning: %v", err)
	}
	return id
}

func TestCountLearningSince(t *testing.T) {
	s := mustOpen(t)

	before := time.Now().Add(-1 * time.Hour)
	now := time.Now()

	// Insert 3 learnings with "old" timestamp
	for i := 0; i < 3; i++ {
		s.InsertLearning(&models.Learning{
			Category: "gotcha", Content: fmt.Sprintf("old %d", i),
			Project: "proj", Confidence: 1.0, CreatedAt: before, ModelUsed: "self",
		})
	}
	// Insert 2 learnings with "now" timestamp
	for i := 0; i < 2; i++ {
		s.InsertLearning(&models.Learning{
			Category: "gotcha", Content: fmt.Sprintf("new %d", i),
			Project: "proj", Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
		})
	}

	// Count since 30min ago — should find only the 2 "now" learnings
	since := time.Now().Add(-30 * time.Minute)
	count := s.CountLearningSince(since)
	if count != 2 {
		t.Errorf("CountLearningSince = %d, want 2", count)
	}

	// Count since 2h ago — should find all 5
	count = s.CountLearningSince(time.Now().Add(-2 * time.Hour))
	if count != 5 {
		t.Errorf("CountLearningSince(2h ago) = %d, want 5", count)
	}

	// Count since now — should find 0
	count = s.CountLearningSince(time.Now().Add(1 * time.Second))
	if count != 0 {
		t.Errorf("CountLearningSince(future) = %d, want 0", count)
	}
}

func TestLearningCountsRoundTrip(t *testing.T) {
	s := mustOpen(t)
	id := newTestLearning(t, s)

	if err := s.IncrementMatchCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementMatchCounts: %v", err)
	}
	if err := s.IncrementMatchCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementMatchCounts second: %v", err)
	}
	if err := s.IncrementInjectCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementInjectCounts: %v", err)
	}
	if err := s.IncrementUseCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementUseCounts: %v", err)
	}
	if err := s.IncrementUseCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementUseCounts second: %v", err)
	}
	if err := s.IncrementUseCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementUseCounts third: %v", err)
	}
	if err := s.IncrementSaveCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementSaveCounts: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.MatchCount != 2 {
		t.Errorf("match_count = %d, want 2", l.MatchCount)
	}
	if l.InjectCount != 1 {
		t.Errorf("inject_count = %d, want 1", l.InjectCount)
	}
	if l.UseCount != 3 {
		t.Errorf("use_count = %d, want 3", l.UseCount)
	}
	if l.SaveCount != 1 {
		t.Errorf("save_count = %d, want 1", l.SaveCount)
	}
	// Legacy hit_count must be untouched
	if l.HitCount != 0 {
		t.Errorf("hit_count = %d, want 0 (should be untouched)", l.HitCount)
	}
}

func TestIncrementMatchCounts(t *testing.T) {
	s := mustOpen(t)
	id := newTestLearning(t, s)

	if err := s.IncrementMatchCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementMatchCounts: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.MatchCount != 1 {
		t.Errorf("match_count = %d, want 1", l.MatchCount)
	}
	// last_hit_at should NOT be set by match (no injection yet)
	if l.LastHitAt != nil {
		t.Errorf("last_hit_at should be nil after match-only, got %v", l.LastHitAt)
	}
}

func TestIncrementInjectCounts(t *testing.T) {
	s := mustOpen(t)
	id := newTestLearning(t, s)

	before := time.Now().Add(-time.Second)
	if err := s.IncrementInjectCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementInjectCounts: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.InjectCount != 1 {
		t.Errorf("inject_count = %d, want 1", l.InjectCount)
	}
	// IncrementInjectCounts must also set last_hit_at
	if l.LastHitAt == nil {
		t.Fatal("last_hit_at should be set after inject")
	}
	if l.LastHitAt.Before(before) {
		t.Errorf("last_hit_at %v is before test start %v", l.LastHitAt, before)
	}
}

func TestIncrementUseCounts(t *testing.T) {
	s := mustOpen(t)
	id := newTestLearning(t, s)

	if err := s.IncrementUseCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementUseCounts: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.UseCount != 1 {
		t.Errorf("use_count = %d, want 1", l.UseCount)
	}
}

func TestIncrementSaveCounts(t *testing.T) {
	s := mustOpen(t)
	id := newTestLearning(t, s)

	if err := s.IncrementSaveCounts([]int64{id}); err != nil {
		t.Fatalf("IncrementSaveCounts: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.SaveCount != 1 {
		t.Errorf("save_count = %d, want 1", l.SaveCount)
	}
}

func TestUpdateLearningContent_ActiveLearning(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "original content",
		CreatedAt: time.Now(),
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	// Set a non-empty embedding_text to verify it gets cleared
	s.db.Exec(`UPDATE learnings SET embedding_text = 'old embedding' WHERE id = ?`, id)

	if err := s.UpdateLearningContent(id, "updated content"); err != nil {
		t.Fatalf("UpdateLearningContent: %v", err)
	}

	l, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.Content != "updated content" {
		t.Errorf("content = %q, want %q", l.Content, "updated content")
	}
	if l.EmbeddingText != "" {
		t.Errorf("embedding_text = %q, want empty string", l.EmbeddingText)
	}
	var embeddingStatus string
	if err := s.DB().QueryRow(`SELECT embedding_status FROM learnings WHERE id = ?`, id).Scan(&embeddingStatus); err != nil {
		t.Fatalf("embedding_status query: %v", err)
	}
	if embeddingStatus != "pending" {
		t.Errorf("embedding_status = %q, want pending", embeddingStatus)
	}
}

func TestInsertLearningMarksEmbeddingPending(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "docker needs host networking",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	var status, hash string
	if err := s.DB().QueryRow(`SELECT embedding_status, embedding_content_hash FROM learnings WHERE id = ?`, id).Scan(&status, &hash); err != nil {
		t.Fatalf("embedding tracking query: %v", err)
	}
	if status != "pending" {
		t.Fatalf("embedding_status = %q, want pending", status)
	}
	if hash == "" {
		t.Fatal("embedding_content_hash should not be empty")
	}
}

func TestMarkEmbeddingsDone(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "nginx wants explicit proxy headers",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	if err := s.MarkEmbeddingsDone([]int64{id}); err != nil {
		t.Fatalf("MarkEmbeddingsDone: %v", err)
	}

	var status string
	var embeddedAt sql.NullString
	if err := s.DB().QueryRow(`SELECT embedding_status, embedded_at FROM learnings WHERE id = ?`, id).Scan(&status, &embeddedAt); err != nil {
		t.Fatalf("embedding done query: %v", err)
	}
	if status != "done" {
		t.Fatalf("embedding_status = %q, want done", status)
	}
	if !embeddedAt.Valid || embeddedAt.String == "" {
		t.Fatal("embedded_at should be set")
	}
}

func TestBackfillContentHashesWithSingleConnection(t *testing.T) {
	s := mustOpen(t)

	now := time.Now()
	id1, err := s.InsertLearning(&models.Learning{
		Category:   "gotcha",
		Content:    "first learning without hash",
		Project:    "yesmem",
		Confidence: 1.0,
		CreatedAt:  now,
		ModelUsed:  "self",
	})
	if err != nil {
		t.Fatalf("insert first learning: %v", err)
	}
	id2, err := s.InsertLearning(&models.Learning{
		Category:   "decision",
		Content:    "second learning without hash",
		Project:    "yesmem",
		Confidence: 1.0,
		CreatedAt:  now,
		ModelUsed:  "self",
	})
	if err != nil {
		t.Fatalf("insert second learning: %v", err)
	}

	if _, err := s.DB().Exec(`UPDATE learnings SET content_hash = '' WHERE id IN (?, ?)`, id1, id2); err != nil {
		t.Fatalf("clear content_hash: %v", err)
	}

	updated, err := s.BackfillContentHashes(func(text string) string { return "hash:" + text })
	if err != nil {
		t.Fatalf("backfill content hashes: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated=%d, want 2", updated)
	}

	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE content_hash = '' OR content_hash IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count missing hashes: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing content_hash rows=%d, want 0", count)
	}
}

func TestUpdateLearningContent_SupersededLearning(t *testing.T) {
	s := mustOpen(t)

	// Insert the learning to be superseded
	oldID, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "original content",
		CreatedAt: time.Now(),
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("InsertLearning old: %v", err)
	}

	// Insert the replacement learning
	newID, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "replacement content",
		CreatedAt: time.Now(),
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("InsertLearning new: %v", err)
	}

	if err := s.SupersedeLearning(oldID, newID, "test supersede"); err != nil {
		t.Fatalf("SupersedeLearning: %v", err)
	}

	// UpdateLearningContent on a superseded learning must be a no-op
	if err := s.UpdateLearningContent(oldID, "should not apply"); err != nil {
		t.Fatalf("UpdateLearningContent on superseded: %v", err)
	}

	l, err := s.GetLearning(oldID)
	if err != nil {
		t.Fatalf("GetLearning: %v", err)
	}
	if l.Content != "original content" {
		t.Errorf("content = %q, want %q (superseded learning must not be changed)", l.Content, "original content")
	}
}

func TestUpdateImpactScore(t *testing.T) {
	s := newTestStore(t)

	id := insertTestLearning(s, "test learning for impact")

	// First impact score
	if err := s.UpdateImpactScore(id, 0.8); err != nil {
		t.Fatal(err)
	}

	// Verify via direct SQL query (since GetLearning scan order might not include new cols yet)
	var score float64
	var count int
	s.db.QueryRow("SELECT COALESCE(impact_score, 0), COALESCE(impact_count, 0) FROM learnings WHERE id = ?", id).Scan(&score, &count)
	if score < 0.79 || score > 0.81 {
		t.Errorf("expected impact_score ~0.8, got %f", score)
	}
	if count != 1 {
		t.Errorf("expected impact_count 1, got %d", count)
	}

	// Second impact score — running average: (0.8 + 0.4) / 2 = 0.6
	if err := s.UpdateImpactScore(id, 0.4); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow("SELECT COALESCE(impact_score, 0), COALESCE(impact_count, 0) FROM learnings WHERE id = ?", id).Scan(&score, &count)
	if score < 0.59 || score > 0.61 {
		t.Errorf("expected impact_score ~0.6, got %f", score)
	}
	if count != 2 {
		t.Errorf("expected impact_count 2, got %d", count)
	}
}

func TestGetForkLearnings(t *testing.T) {
	s := newTestStore(t)

	// Insert fork learnings for session-1
	id1, _ := s.InsertLearning(&models.Learning{
		SessionID: "session-1", Category: "decision", Content: "first fork learning",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test", Source: "fork",
	})
	id2, _ := s.InsertLearning(&models.Learning{
		SessionID: "session-1", Category: "pattern", Content: "second fork learning",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test", Source: "fork",
	})
	// Different session
	s.InsertLearning(&models.Learning{
		SessionID: "session-2", Category: "decision", Content: "other session",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test", Source: "fork",
	})
	// Same session but not fork source
	s.InsertLearning(&models.Learning{
		SessionID: "session-1", Category: "decision", Content: "user stated",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test", Source: "user_stated",
	})

	learnings, err := s.GetForkLearnings("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(learnings) != 2 {
		t.Fatalf("expected 2 fork learnings, got %d", len(learnings))
	}
	if learnings[0].ID != id1 || learnings[1].ID != id2 {
		t.Errorf("wrong IDs: got %d, %d — expected %d, %d", learnings[0].ID, learnings[1].ID, id1, id2)
	}
}

func TestForkCoverage(t *testing.T) {
	s := newTestStore(t)

	// No coverage yet
	if s.IsCoveredByFork("session-1", 0, 25) {
		t.Error("should not be covered yet")
	}

	// Insert coverage
	if err := s.InsertForkCoverage("session-1", 0, 25, 1); err != nil {
		t.Fatal(err)
	}

	// Exact match
	if !s.IsCoveredByFork("session-1", 0, 25) {
		t.Error("should be covered (exact match)")
	}

	// Subset
	if !s.IsCoveredByFork("session-1", 5, 20) {
		t.Error("should be covered (subset)")
	}

	// Not fully covered
	if s.IsCoveredByFork("session-1", 20, 50) {
		t.Error("should not be fully covered")
	}

	// Different session
	if s.IsCoveredByFork("session-2", 0, 25) {
		t.Error("different session should not be covered")
	}
}

func TestLearningLineage(t *testing.T) {
	s := mustOpen(t)
	l := &models.Learning{
		SessionID:     "lineage-session",
		Category:      "decision",
		Content:       "Use ephemeral cache TTL",
		Source:        "fork",
		SourceMsgFrom: 10,
		SourceMsgTo:   25,
		Confidence:    1.0,
		CreatedAt:     time.Now(),
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLearning(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceMsgFrom != 10 {
		t.Errorf("SourceMsgFrom = %d, want 10", got.SourceMsgFrom)
	}
	if got.SourceMsgTo != 25 {
		t.Errorf("SourceMsgTo = %d, want 25", got.SourceMsgTo)
	}

	// Zero-value normalization: 0,0 should be stored as -1,-1
	noLineage := &models.Learning{
		SessionID:  "no-lineage",
		Category:   "pattern",
		Content:    "Learning without lineage data",
		Source:     "user_stated",
		Confidence: 1.0,
		CreatedAt:  time.Now(),
	}
	id2, err := s.InsertLearning(noLineage)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetLearning(id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.SourceMsgFrom != -1 {
		t.Errorf("no-lineage SourceMsgFrom = %d, want -1", got2.SourceMsgFrom)
	}
	if got2.SourceMsgTo != -1 {
		t.Errorf("no-lineage SourceMsgTo = %d, want -1", got2.SourceMsgTo)
	}
}
