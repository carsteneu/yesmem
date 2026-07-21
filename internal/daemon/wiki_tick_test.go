package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

func newWikiTickTestStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func insertLearningForProject(t *testing.T, s *storage.Store, project string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO learnings (session_id, project, category, content, confidence, created_at, model_used, source)
		 VALUES (?, ?, 'gotcha', 'x', 1.0, ?, 'test', 'llm_extracted')`,
		"sess-"+project, project, time.Now().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert learning for %s: %v", project, err)
	}
}

func insertSessionForProject(t *testing.T, s *storage.Store, projectShort, startedAt string) {
	t.Helper()
	_, err := s.DB().Exec(
		`INSERT INTO sessions (id, project, project_short, started_at, message_count, jsonl_path, indexed_at)
		 VALUES (?, ?, ?, ?, 1, '', ?)`,
		"sess-"+projectShort+"-"+startedAt, projectShort, projectShort, startedAt, startedAt,
	)
	if err != nil {
		t.Fatalf("insert session for %s @ %s: %v", projectShort, startedAt, err)
	}
}

// --- isLiveProjectPath ---

func TestIsLiveProjectPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"tmp_prefix", "/tmp/foo", false},
		{"tmp_subdir", "/tmp/opencode-xyz123/foo", false},
		{"missing_absolute", "/this/path/does/not/exist/anywhere", false},
		{"relative_kept", "relative/path", true},
		{"empty_kept", "", true},
		{"root_itself", "/", true}, // exists
	}
	// Add a real-existing absolute path (this worktree).
	wd, _ := os.Getwd()
	cases = append(cases, struct {
		name string
		path string
		want bool
	}{"real_worktree", wd, true})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLiveProjectPath(tc.path); got != tc.want {
				t.Errorf("isLiveProjectPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// --- activeProjects dead-path filtering ---

func TestActiveProjects_FiltersTmpAndMissing(t *testing.T) {
	s := newWikiTickTestStore(t)
	// t.TempDir() lives under /tmp on Linux, which the dead-path filter rejects
	// by design. Use the test process CWD as a guaranteed-existing non-/tmp path.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	insertLearningForProject(t, s, "/tmp/opencode-ghost-1")
	insertLearningForProject(t, s, "/tmp/opencode-ghost-2")
	insertLearningForProject(t, s, "/definitely/not/on/disk/path-1")
	insertLearningForProject(t, s, cwd)

	got, err := activeProjects(context.Background(), s)
	if err != nil {
		t.Fatalf("activeProjects: %v", err)
	}

	foundReal := false
	for _, g := range got {
		if g == cwd {
			foundReal = true
			break
		}
	}
	if !foundReal {
		t.Errorf("expected activeProjects to include real path %q, got %v", cwd, got)
	}

	wantExcluded := []string{"/tmp/opencode-ghost-1", "/tmp/opencode-ghost-2", "/definitely/not/on/disk/path-1"}
	for _, excluded := range wantExcluded {
		for _, g := range got {
			if g == excluded {
				t.Errorf("expected activeProjects to exclude dead path %q, but it was included", excluded)
			}
		}
	}
}

// --- projectTierHot ---

func TestProjectTierHot_HotWithin24h(t *testing.T) {
	s := newWikiTickTestStore(t)
	project := t.TempDir() // unused for stat, just a key
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	insertSessionForProject(t, s, project, recent)

	if !projectTierHot(context.Background(), s, project) {
		t.Errorf("expected project with 1h-old session to be hot, got cold")
	}
}

func TestProjectTierHot_ColdAfter24h(t *testing.T) {
	s := newWikiTickTestStore(t)
	project := t.TempDir()
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	insertSessionForProject(t, s, project, old)

	if projectTierHot(context.Background(), s, project) {
		t.Errorf("expected project with 48h-old session to be cold, got hot")
	}
}

func TestProjectTierHot_NoSessionsIsHot(t *testing.T) {
	s := newWikiTickTestStore(t)
	project := t.TempDir()

	if !projectTierHot(context.Background(), s, project) {
		t.Errorf("expected project with no sessions to be treated as hot (fail-open), got cold")
	}
}

// --- coldTierDueForRender ---

func TestColdTierDueForRender_NeverRendered(t *testing.T) {
	outDir := t.TempDir()
	if !coldTierDueForRender(outDir) {
		t.Errorf("expected coldTierDueForRender=true when no snapshot exists")
	}
}

func TestColdTierDueForRender_RecentSnapshot(t *testing.T) {
	outDir := t.TempDir()
	snapPath := filepath.Join(outDir, ".wiki-snapshot.json")
	if err := os.WriteFile(snapPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	recentMtime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(snapPath, recentMtime, recentMtime); err != nil {
		t.Fatal(err)
	}
	if coldTierDueForRender(outDir) {
		t.Errorf("expected coldTierDueForRender=false (1h-old snapshot too fresh)")
	}
}

func TestColdTierDueForRender_StaleSnapshot(t *testing.T) {
	outDir := t.TempDir()
	snapPath := filepath.Join(outDir, ".wiki-snapshot.json")
	if err := os.WriteFile(snapPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	staleMtime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(snapPath, staleMtime, staleMtime); err != nil {
		t.Fatal(err)
	}
	if !coldTierDueForRender(outDir) {
		t.Errorf("expected coldTierDueForRender=true (25h-old snapshot is due)")
	}
}
