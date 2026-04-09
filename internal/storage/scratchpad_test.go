package storage

import (
	"testing"
)

func TestScratchpadWrite_And_Read(t *testing.T) {
	s := newTestStore(t)

	err := s.ScratchpadWrite("proj-a", "plan", "some plan content", "agent-1")
	if err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}

	sections, err := s.ScratchpadRead("proj-a", "plan")
	if err != nil {
		t.Fatalf("ScratchpadRead: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Section != "plan" {
		t.Errorf("expected section='plan', got %q", sections[0].Section)
	}
	if sections[0].Content != "some plan content" {
		t.Errorf("expected content='some plan content', got %q", sections[0].Content)
	}
	if sections[0].Owner != "agent-1" {
		t.Errorf("expected owner='agent-1', got %q", sections[0].Owner)
	}
}

func TestScratchpadWrite_Upsert(t *testing.T) {
	s := newTestStore(t)

	if err := s.ScratchpadWrite("proj", "notes", "original content", "agent-1"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := s.ScratchpadWrite("proj", "notes", "updated content", "agent-2"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	sections, err := s.ScratchpadRead("proj", "notes")
	if err != nil {
		t.Fatalf("ScratchpadRead: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected 1 section after upsert, got %d", len(sections))
	}
	if sections[0].Content != "updated content" {
		t.Errorf("expected content='updated content', got %q", sections[0].Content)
	}
	if sections[0].Owner != "agent-2" {
		t.Errorf("expected owner='agent-2', got %q", sections[0].Owner)
	}
}

func TestScratchpadRead_AllSections(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj", "plan", "plan content", "")
	s.ScratchpadWrite("proj", "status", "status content", "")
	s.ScratchpadWrite("proj", "notes", "notes content", "")

	sections, err := s.ScratchpadRead("proj", "")
	if err != nil {
		t.Fatalf("ScratchpadRead all: %v", err)
	}
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
}

func TestScratchpadRead_SingleSection(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj", "plan", "plan content", "")
	s.ScratchpadWrite("proj", "status", "status content", "")

	sections, err := s.ScratchpadRead("proj", "status")
	if err != nil {
		t.Fatalf("ScratchpadRead single: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Section != "status" {
		t.Errorf("expected section='status', got %q", sections[0].Section)
	}
}

func TestScratchpadRead_NotFound(t *testing.T) {
	s := newTestStore(t)

	sections, err := s.ScratchpadRead("proj", "nonexistent")
	if err != nil {
		t.Fatalf("ScratchpadRead not found: %v", err)
	}
	if len(sections) != 0 {
		t.Errorf("expected 0 sections for nonexistent, got %d", len(sections))
	}
}

func TestScratchpadList_AllProjects(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj-a", "plan", "a plan", "")
	s.ScratchpadWrite("proj-a", "notes", "a notes", "")
	s.ScratchpadWrite("proj-b", "status", "b status", "")

	projects, err := s.ScratchpadList("")
	if err != nil {
		t.Fatalf("ScratchpadList all: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	counts := make(map[string]int)
	for _, p := range projects {
		counts[p.Project] = p.SectionCount
	}
	if counts["proj-a"] != 2 {
		t.Errorf("expected proj-a SectionCount=2, got %d", counts["proj-a"])
	}
	if counts["proj-b"] != 1 {
		t.Errorf("expected proj-b SectionCount=1, got %d", counts["proj-b"])
	}
	// Verify Sections are populated
	for _, p := range projects {
		if len(p.Sections) != p.SectionCount {
			t.Errorf("project %s: Sections length %d != SectionCount %d", p.Project, len(p.Sections), p.SectionCount)
		}
	}
}

func TestScratchpadList_SingleProject(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj-a", "plan", "content", "owner-a")
	s.ScratchpadWrite("proj-b", "notes", "content", "")

	projects, err := s.ScratchpadList("proj-a")
	if err != nil {
		t.Fatalf("ScratchpadList single project: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Project != "proj-a" {
		t.Errorf("expected project='proj-a', got %q", projects[0].Project)
	}
	if len(projects[0].Sections) != 1 {
		t.Errorf("expected 1 section, got %d", len(projects[0].Sections))
	}
	if projects[0].Sections[0].Owner != "owner-a" {
		t.Errorf("expected owner='owner-a', got %q", projects[0].Sections[0].Owner)
	}
}

func TestScratchpadDelete_SingleSection(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj", "plan", "plan content", "")
	s.ScratchpadWrite("proj", "status", "status content", "")

	n, err := s.ScratchpadDelete("proj", "plan")
	if err != nil {
		t.Fatalf("ScratchpadDelete section: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row deleted, got %d", n)
	}

	sections, err := s.ScratchpadRead("proj", "")
	if err != nil {
		t.Fatalf("ScratchpadRead after delete: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected 1 section remaining, got %d", len(sections))
	}
	if sections[0].Section != "status" {
		t.Errorf("expected remaining section='status', got %q", sections[0].Section)
	}
}

func TestScratchpadDelete_EntireProject(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj", "plan", "plan content", "")
	s.ScratchpadWrite("proj", "status", "status content", "")
	s.ScratchpadWrite("proj", "notes", "notes content", "")

	n, err := s.ScratchpadDelete("proj", "")
	if err != nil {
		t.Fatalf("ScratchpadDelete project: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 rows deleted, got %d", n)
	}

	sections, err := s.ScratchpadRead("proj", "")
	if err != nil {
		t.Fatalf("ScratchpadRead after project delete: %v", err)
	}
	if len(sections) != 0 {
		t.Errorf("expected 0 sections after project delete, got %d", len(sections))
	}
}

func TestScratchpadDelete_NotFound(t *testing.T) {
	s := newTestStore(t)

	n, err := s.ScratchpadDelete("nonexistent", "section")
	if err != nil {
		t.Fatalf("ScratchpadDelete not found: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows deleted for nonexistent, got %d", n)
	}
}

func TestScratchpadWrite_EmptyOwner(t *testing.T) {
	s := newTestStore(t)

	if err := s.ScratchpadWrite("proj", "section", "content", ""); err != nil {
		t.Fatalf("ScratchpadWrite empty owner: %v", err)
	}

	sections, err := s.ScratchpadRead("proj", "section")
	if err != nil {
		t.Fatalf("ScratchpadRead: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	if sections[0].Owner != "" {
		t.Errorf("expected owner='', got %q", sections[0].Owner)
	}
}

func TestScratchpadRead_IsolatedByProject(t *testing.T) {
	s := newTestStore(t)

	s.ScratchpadWrite("proj-a", "shared-section", "a content", "")
	s.ScratchpadWrite("proj-b", "shared-section", "b content", "")

	a, err := s.ScratchpadRead("proj-a", "shared-section")
	if err != nil {
		t.Fatalf("ScratchpadRead proj-a: %v", err)
	}
	b, err := s.ScratchpadRead("proj-b", "shared-section")
	if err != nil {
		t.Fatalf("ScratchpadRead proj-b: %v", err)
	}

	if len(a) != 1 || a[0].Content != "a content" {
		t.Errorf("proj-a content mismatch: %+v", a)
	}
	if len(b) != 1 || b[0].Content != "b content" {
		t.Errorf("proj-b content mismatch: %+v", b)
	}
}
