package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func newTestLearningStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProjectSourceColumnExists(t *testing.T) {
	s := newTestLearningStore(t)
	var src string
	err := s.db.QueryRow(`SELECT COALESCE(project_source,'') FROM learnings LIMIT 1`).Scan(&src)
	if err != nil && err.Error() != "sql: no rows in result set" {
		t.Fatalf("project_source column missing: %v", err)
	}
}

func TestProjectSourceRoundtrip(t *testing.T) {
	s := newTestLearningStore(t)

	id, err := s.InsertLearning(&models.Learning{
		Category:      "gotcha",
		Content:       "attributed by content signal",
		Project:       "/home/test/memory/yesmem",
		ProjectSource: "content",
		Source:        "test",
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProjectSource != "content" {
		t.Errorf("ProjectSource = %q, want %q", got.ProjectSource, "content")
	}
}

func TestProjectSourceDefaultEmpty(t *testing.T) {
	s := newTestLearningStore(t)

	id, err := s.InsertLearning(&models.Learning{
		Category:  "gotcha",
		Content:   "no project source given",
		Project:   "/home/test/projects/opencode",
		Source:    "test",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProjectSource != "" {
		t.Errorf("ProjectSource = %q, want empty", got.ProjectSource)
	}
}
