package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestClaudeMdStateCRUD(t *testing.T) {
	s := mustOpen(t)

	// initially nil
	st, err := s.GetClaudeMdState("testproject")
	if err != nil {
		t.Fatal(err)
	}
	if st != nil {
		t.Fatal("expected nil for unknown project")
	}

	// save
	now := time.Now().UTC().Truncate(time.Second)
	err = s.SaveClaudeMdState(&ClaudeMdState{
		Project:       "testproject",
		LastGenerated: now,
		LearningsHash: "abc123",
		OutputPath:    "/tmp/test/.claude/yesmem-ops.md",
	})
	if err != nil {
		t.Fatal(err)
	}

	// retrieve
	st, err = s.GetClaudeMdState("testproject")
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("expected state after save")
	}
	if st.LearningsHash != "abc123" {
		t.Errorf("hash: got %q, want %q", st.LearningsHash, "abc123")
	}
	if !st.LastGenerated.Equal(now) {
		t.Errorf("time: got %v, want %v", st.LastGenerated, now)
	}

	// upsert
	err = s.SaveClaudeMdState(&ClaudeMdState{
		Project:       "testproject",
		LastGenerated: now.Add(time.Hour),
		LearningsHash: "def456",
		OutputPath:    "/tmp/test/.claude/yesmem-ops.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetClaudeMdState("testproject")
	if st.LearningsHash != "def456" {
		t.Errorf("upsert failed: got hash %q", st.LearningsHash)
	}
}

func seedLearning(t *testing.T, s *Store, project, category, content string) {
	t.Helper()
	_, err := s.InsertLearning(&models.Learning{
		SessionID:  "test-session",
		Category:   category,
		Content:    content,
		Project:    project,
		Confidence: 1.0,
		CreatedAt:  time.Now(),
		Source:     "llm_extracted",
	})
	if err != nil {
		t.Fatalf("seed learning: %v", err)
	}
}

func TestGetLearningsForClaudeMdPerCategoryCap(t *testing.T) {
	s := mustOpen(t)

	// Seed 5 gotchas, 4 decisions
	for i := 0; i < 5; i++ {
		seedLearning(t, s, "proj", "gotcha", fmt.Sprintf("Gotcha-Falle beim Deployment Variante %d", i))
	}
	for i := 0; i < 4; i++ {
		seedLearning(t, s, "proj", "decision", fmt.Sprintf("Architektur-Entscheidung Nummer %d zu Infrastruktur", i))
	}

	// Cap gotcha at 2, decision at 3
	caps := map[string]int{"gotcha": 2, "decision": 3}
	result, err := s.GetLearningsForClaudeMd("proj", caps)
	if err != nil {
		t.Fatal(err)
	}

	gotchaCount, decisionCount := 0, 0
	for _, l := range result {
		switch l.Category {
		case "gotcha":
			gotchaCount++
		case "decision":
			decisionCount++
		}
	}
	if gotchaCount != 2 {
		t.Errorf("gotcha count: got %d, want 2", gotchaCount)
	}
	if decisionCount != 3 {
		t.Errorf("decision count: got %d, want 3", decisionCount)
	}
}

func TestGetLearningsForClaudeMdDefaultCap(t *testing.T) {
	s := mustOpen(t)

	// Seed 12 learnings in a category not in caps — should use default cap of 10
	for i := 0; i < 12; i++ {
		seedLearning(t, s, "proj", "pattern", fmt.Sprintf("Einzigartiges Pattern Nummer %d fuer Systemdesign und Refactoring", i))
	}

	caps := map[string]int{"gotcha": 5} // pattern not in caps → default 10
	result, err := s.GetLearningsForClaudeMd("proj", caps)
	if err != nil {
		t.Fatal(err)
	}

	patternCount := 0
	for _, l := range result {
		if l.Category == "pattern" {
			patternCount++
		}
	}
	if patternCount > 10 {
		t.Errorf("pattern count %d exceeds default cap of 10", patternCount)
	}
}

func TestGetLearningsForClaudeMdNoCaps(t *testing.T) {
	s := mustOpen(t)

	seedLearning(t, s, "proj", "gotcha", "Sandbox blockiert DNS-Aufloesung bei externen Hosts")
	seedLearning(t, s, "proj", "decision", "Redis-Cache statt Filesystem-Cache wegen Cluster-Setup")

	// Empty caps = return all
	result, err := s.GetLearningsForClaudeMd("proj", map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Errorf("with no caps: got %d results, want 2", len(result))
	}
}

func TestGetLearningsForClaudeMdProjectIsolation(t *testing.T) {
	s := mustOpen(t)

	seedLearning(t, s, "projA", "gotcha", "Problem mit SSL-Zertifikaten auf dem Staging-Server")
	seedLearning(t, s, "projB", "gotcha", "Docker-Container startet nicht wegen Port-Konflikt")

	result, err := s.GetLearningsForClaudeMd("projA", map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Errorf("projA should have 1 learning, got %d", len(result))
	}
	if result[0].Content != "Problem mit SSL-Zertifikaten auf dem Staging-Server" {
		t.Errorf("wrong learning returned: %q", result[0].Content)
	}
}
