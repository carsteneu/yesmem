package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestGetBriefingHealth_Empty(t *testing.T) {
	s := newTestStore(t)
	h, err := s.GetBriefingHealth("testproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Contradictions != 0 || h.Unfinished != 0 || h.Stale != 0 {
		t.Errorf("empty store should return all zeros, got %+v", h)
	}
}

func TestGetBriefingHealth_Contradictions(t *testing.T) {
	s := newTestStore(t)

	id1 := insertLearningWithProject(s, "fact A", "testproject")
	id2 := insertLearningWithProject(s, "fact B contradicts A", "testproject")

	if err := s.InsertTypedAssociation(id1, id2, "contradicts"); err != nil {
		t.Fatalf("insert association: %v", err)
	}

	h, err := s.GetBriefingHealth("testproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Contradictions != 1 {
		t.Errorf("expected 1 contradiction, got %d", h.Contradictions)
	}
}

func TestGetBriefingHealth_Unfinished(t *testing.T) {
	s := newTestStore(t)

	// Insert 3 unfinished tasks (category='unfinished' is the marker)
	for i := 0; i < 3; i++ {
		l := &models.Learning{
			SessionID: "test-session", Category: "unfinished", Content: "open task",
			Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
			Source: "user_stated", Project: "testproject", TaskType: "task",
		}
		if _, err := s.InsertLearning(l); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	h, err := s.GetBriefingHealth("testproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Unfinished != 3 {
		t.Errorf("expected 3 unfinished, got %d", h.Unfinished)
	}
}

func TestGetBriefingHealth_Stale(t *testing.T) {
	s := newTestStore(t)

	// Insert a learning that's old and never cited
	l := &models.Learning{
		SessionID: "test-session", Category: "gotcha", Content: "old forgotten fact",
		Confidence: 1.0, CreatedAt: time.Now().AddDate(0, -4, 0), ModelUsed: "test",
		Source: "llm_extracted", Project: "testproject",
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Backdate created_at to >90 days ago
	_, err = s.db.Exec("UPDATE learnings SET created_at = datetime('now', '-100 days') WHERE id = ?", id)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}

	h, err := s.GetBriefingHealth("testproject")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Stale != 1 {
		t.Errorf("expected 1 stale, got %d", h.Stale)
	}
}

func TestGetRecentFiles_Empty(t *testing.T) {
	s := newTestStore(t)
	files, err := s.GetRecentFiles("testproject", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("empty store should return empty slice, got %d", len(files))
	}
}

func TestGetRecentFiles_ReturnsRecent(t *testing.T) {
	s := newTestStore(t)

	// Insert file coverage records
	now := time.Now().Format(time.RFC3339)
	old := time.Now().AddDate(0, -2, 0).Format(time.RFC3339)

	s.db.Exec("INSERT INTO file_coverage (project, file_path, directory, session_count, last_touched) VALUES (?, ?, ?, ?, ?)", "testproject", "internal/proxy/cache.go", "internal/proxy", 5, now)
	s.db.Exec("INSERT INTO file_coverage (project, file_path, directory, session_count, last_touched) VALUES (?, ?, ?, ?, ?)", "testproject", "internal/old/forgotten.go", "internal/old", 1, old)

	files, err := s.GetRecentFiles("testproject", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the recent file should be returned (within 30 days)
	if len(files) != 1 {
		t.Errorf("expected 1 recent file, got %d", len(files))
	}
	if len(files) > 0 && files[0].Path != "internal/proxy/cache.go" {
		t.Errorf("expected cache.go, got %s", files[0].Path)
	}
}

func TestGetRecentFiles_Limit(t *testing.T) {
	s := newTestStore(t)

	now := time.Now().Format(time.RFC3339)
	for i := 0; i < 20; i++ {
		s.db.Exec("INSERT INTO file_coverage (project, file_path, directory, session_count, last_touched) VALUES (?, ?, ?, ?, ?)", "testproject", "file_"+string(rune('a'+i))+".go", ".", i+1, now)
	}

	files, err := s.GetRecentFiles("testproject", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 5 {
		t.Errorf("expected 5 files with limit, got %d", len(files))
	}
}

// insertLearningWithProject inserts a test learning with a specific project.
func insertLearningWithProject(s *Store, content, project string) int64 {
	id, err := s.InsertLearning(&models.Learning{
		SessionID: "test-session", Category: "gotcha", Content: content,
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
		Source: "llm_extracted", Project: project,
	})
	if err != nil {
		panic(err)
	}
	return id
}
