package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

func insertSessionRow(t *testing.T, s *storage.Store, id, projectPath, startedAt string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO sessions (id, project, project_short, started_at, message_count, jsonl_path, indexed_at)
		 VALUES (?, ?, ?, ?, 1, '', ?)`,
		id, projectPath, projectPath, startedAt, startedAt,
	); err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func TestListProjectsNeedingBriefingRefresh_FiltersDeadAndInactive(t *testing.T) {
	s := newWikiTickTestStore(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	now := time.Now().Format(time.RFC3339)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	old := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339) // 60 days ago

	// Use a real existing dir for the "inactive" case so isLiveProjectPath
	// doesn't reject it first — we want the inactivity filter, not the
	// dead-path filter, to exclude this one.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("userhomedir: %v", err)
	}
	inactiveDir, err := os.MkdirTemp(home, ".yesmem-test-inactive-*")
	if err != nil {
		t.Fatalf("mkdir inactive: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(inactiveDir) })

	// active, real path → should be included
	insertSessionRow(t, s, "sess-active", cwd, recent)
	// /tmp ghost → skipped (dead path)
	insertSessionRow(t, s, "sess-tmp-1", "/tmp/opencode-ghost", now)
	// nonexistent path → skipped (dead path)
	insertSessionRow(t, s, "sess-missing", "/this/path/does/not/exist", recent)
	// old session (>30 days) on a real existing path → skipped (inactive)
	insertSessionRow(t, s, "sess-old", inactiveDir, old)

	// Seed a stale cached hash so each project would otherwise qualify for refresh.
	for _, p := range []string{cwd, "/tmp/opencode-ghost", "/this/path/does/not/exist", inactiveDir} {
		if err := s.SaveRefinedBriefing(p, "stale-hash", "old prose", "opus"); err != nil {
			t.Fatalf("save refined briefing %s: %v", p, err)
		}
	}

	targets, err := listProjectsNeedingBriefingRefresh(s, &config.Config{})
	if err != nil {
		t.Fatalf("listProjectsNeedingBriefingRefresh: %v", err)
	}

	foundCwd := false
	for _, tt := range targets {
		if tt.Project.Project == cwd || tt.Project.ProjectShort == cwd {
			foundCwd = true
			continue
		}
		t.Errorf("unexpected target included: short=%q project=%q lastActive=%q",
			tt.Project.ProjectShort, tt.Project.Project, tt.Project.LastActive)
	}
	if !foundCwd {
		t.Errorf("expected cwd project %q in targets, got %d targets", cwd, len(targets))
	}
}

func TestListProjectsNeedingBriefingRefresh_InactiveThreshold(t *testing.T) {
	s := newWikiTickTestStore(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("userhomedir: %v", err)
	}

	// Two real existing dirs — the dead-path filter must NOT be what excludes these.
	dir29, err := os.MkdirTemp(home, ".yesmem-test-29d-*")
	if err != nil {
		t.Fatalf("mkdir 29d: %v", err)
	}
	dir31, err := os.MkdirTemp(home, ".yesmem-test-31d-*")
	if err != nil {
		t.Fatalf("mkdir 31d: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir29)
		os.RemoveAll(dir31)
	})

	// 31 days ago — just past the 30-day inactivity threshold.
	justStale := time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	// 29 days ago — still inside the active window.
	almostStale := time.Now().Add(-29 * 24 * time.Hour).Format(time.RFC3339)

	insertSessionRow(t, s, "sess-31d", dir31, justStale)
	insertSessionRow(t, s, "sess-29d", dir29, almostStale)

	if err := s.SaveRefinedBriefing(dir31, "stale", "old", "opus"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefinedBriefing(dir29, "stale", "old", "opus"); err != nil {
		t.Fatal(err)
	}

	targets, err := listProjectsNeedingBriefingRefresh(s, &config.Config{})
	if err != nil {
		t.Fatalf("listProjectsNeedingBriefingRefresh: %v", err)
	}

	for _, tt := range targets {
		if tt.Project.Project == dir31 {
			t.Errorf("31-day-inactive project should be filtered out, got target: short=%q lastActive=%q",
				tt.Project.ProjectShort, tt.Project.LastActive)
		}
	}

	found29 := false
	for _, tt := range targets {
		if tt.Project.Project == dir29 {
			found29 = true
		}
	}
	if !found29 {
		t.Errorf("29-day-active project should be included (within 30d window), targets=%v", targets)
	}
}
