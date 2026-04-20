package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestFindLearningsByEntityMatch_Basic(t *testing.T) {
	s := mustOpen(t)

	l := &models.Learning{
		Content:   "proxy config requires restart after change",
		Category:  "gotcha",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"proxy.go"},
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	results, err := s.FindLearningsByEntityMatch([]string{"proxy.go"}, "yesmem")
	if err != nil {
		t.Fatalf("FindLearningsByEntityMatch: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != l.Content {
		t.Errorf("expected content %q, got %q", l.Content, results[0].Content)
	}
	if results[0].ID != id {
		t.Errorf("expected id %d, got %d", id, results[0].ID)
	}
}

func TestFindLearningsByEntityMatch_NoMatch(t *testing.T) {
	s := mustOpen(t)

	l := &models.Learning{
		Content:   "daemon handles reconnection gracefully",
		Category:  "pattern",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"daemon.go"},
	}
	_, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	results, err := s.FindLearningsByEntityMatch([]string{"proxy.go"}, "yesmem")
	if err != nil {
		t.Fatalf("FindLearningsByEntityMatch: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestFindLearningsByEntityMatch_SupersededExcluded(t *testing.T) {
	s := mustOpen(t)

	l := &models.Learning{
		Content:   "old proxy pattern no longer valid",
		Category:  "gotcha",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"proxy.go"},
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert learning: %v", err)
	}

	// Mark as resolved (superseded_by = -2)
	if err := s.ResolveLearning(id, "test"); err != nil {
		t.Fatalf("resolve learning: %v", err)
	}

	results, err := s.FindLearningsByEntityMatch([]string{"proxy.go"}, "yesmem")
	if err != nil {
		t.Fatalf("FindLearningsByEntityMatch: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for superseded learning, got %d", len(results))
	}
}

func TestFindLearningsByEntityMatch_ProjectScope(t *testing.T) {
	s := mustOpen(t)

	l1 := &models.Learning{
		Content:   "proxy config for yesmem project",
		Category:  "gotcha",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"proxy.go"},
	}
	_, err := s.InsertLearning(l1)
	if err != nil {
		t.Fatalf("insert learning 1: %v", err)
	}

	l2 := &models.Learning{
		Content:   "proxy config for other project",
		Category:  "gotcha",
		Project:   "other",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"proxy.go"},
	}
	_, err = s.InsertLearning(l2)
	if err != nil {
		t.Fatalf("insert learning 2: %v", err)
	}

	results, err := s.FindLearningsByEntityMatch([]string{"proxy.go"}, "yesmem")
	if err != nil {
		t.Fatalf("FindLearningsByEntityMatch: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result scoped to yesmem, got %d", len(results))
	}
	if results[0].Project != "yesmem" {
		t.Errorf("expected project 'yesmem', got %q", results[0].Project)
	}
}

func TestFindLearningsByEntityMatch_MultipleEntities(t *testing.T) {
	s := mustOpen(t)

	l1 := &models.Learning{
		Content:   "proxy needs careful error handling",
		Category:  "gotcha",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"proxy.go"},
	}
	id1, err := s.InsertLearning(l1)
	if err != nil {
		t.Fatalf("insert learning 1: %v", err)
	}

	l2 := &models.Learning{
		Content:   "handler routes must validate input",
		Category:  "pattern",
		Project:   "yesmem",
		CreatedAt: time.Now(),
		Source:    "test",
		ModelUsed: "test",
		Entities:  []string{"handler.go"},
	}
	id2, err := s.InsertLearning(l2)
	if err != nil {
		t.Fatalf("insert learning 2: %v", err)
	}

	results, err := s.FindLearningsByEntityMatch([]string{"proxy.go", "handler.go"}, "yesmem")
	if err != nil {
		t.Fatalf("FindLearningsByEntityMatch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Verify no duplicates — collect IDs
	seen := make(map[int64]bool)
	for _, r := range results {
		if seen[r.ID] {
			t.Errorf("duplicate result with id %d", r.ID)
		}
		seen[r.ID] = true
	}
	if !seen[id1] {
		t.Errorf("missing result for learning %d (proxy.go)", id1)
	}
	if !seen[id2] {
		t.Errorf("missing result for learning %d (handler.go)", id2)
	}
}

func TestFindLearningsByEntityMatch_EmptyInput(t *testing.T) {
	s := mustOpen(t)

	results, err := s.FindLearningsByEntityMatch([]string{}, "yesmem")
	if err != nil {
		t.Fatalf("expected nil error for empty input, got %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results for empty input, got %d results", len(results))
	}
}

func TestGetLearningCountsByEntity(t *testing.T) {
	s := mustOpen(t)

	// Create two learnings: one gotcha, one decision, same entity "handler.go"
	l1 := &models.Learning{Category: "gotcha", Content: "handler bug", Project: "testproj", Entities: []string{"handler.go", "proxy"}, CreatedAt: time.Now(), Source: "test", ModelUsed: "test"}
	l2 := &models.Learning{Category: "decision", Content: "use RWMutex", Project: "testproj", Entities: []string{"handler.go"}, CreatedAt: time.Now(), Source: "test", ModelUsed: "test"}
	// Superseded learning — should NOT be counted
	l3 := &models.Learning{Category: "gotcha", Content: "old bug", Project: "testproj", Entities: []string{"handler.go"}, CreatedAt: time.Now(), Source: "test", ModelUsed: "test"}

	id1, _ := s.InsertLearning(l1)
	s.InsertLearning(l2)
	id3, _ := s.InsertLearning(l3)

	// Supersede l3
	s.DB().Exec("UPDATE learnings SET superseded_by = ? WHERE id = ?", id1, id3)

	counts, err := s.GetLearningCountsByEntity("testproj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// handler.go: 1 gotcha (l3 superseded) + 1 decision = 2 total, 1 gotcha
	hc := counts["handler.go"]
	if hc.Total != 2 {
		t.Errorf("handler.go total: expected 2, got %d", hc.Total)
	}
	if hc.Gotchas != 1 {
		t.Errorf("handler.go gotchas: expected 1, got %d", hc.Gotchas)
	}

	// proxy: 1 total (l1 is gotcha with entity "proxy"), 1 gotcha
	pc := counts["proxy"]
	if pc.Total != 1 {
		t.Errorf("proxy total: expected 1, got %d", pc.Total)
	}
	if pc.Gotchas != 1 {
		t.Errorf("proxy gotchas: expected 1, got %d", pc.Gotchas)
	}

	// Different project — should return empty
	counts2, err := s.GetLearningCountsByEntity("other")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(counts2) != 0 {
		t.Errorf("expected empty for other project, got %d entries", len(counts2))
	}
}
