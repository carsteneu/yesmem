package repo_test

import (
	"os"
	"path/filepath"
	"testing"

	repo "github.com/carsteneu/yesmem/internal/repo"
)

func gitMain(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func gitWorktreeOf(t *testing.T, main, wt string) {
	t.Helper()
	gitDir := filepath.Join(main, ".git", "worktrees", filepath.Base(wt))
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRootPlain(t *testing.T) {
	m := t.TempDir()
	gitMain(t, m)
	if got := repo.Root(m); got != m {
		t.Errorf("Root(%q) = %q, want %q", m, got, m)
	}
}

func TestRootSubdir(t *testing.T) {
	m := t.TempDir()
	gitMain(t, m)
	sub := filepath.Join(m, "internal", "repo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repo.Root(sub); got != m {
		t.Errorf("Root(%q) = %q, want %q", sub, got, m)
	}
}

func TestRootWorktreeResolvesToMain(t *testing.T) {
	m := t.TempDir()
	gitMain(t, m)
	wt := filepath.Join(m, ".worktrees", "feat")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	gitWorktreeOf(t, m, wt)
	if got := repo.Root(wt); got != m {
		t.Errorf("Root(%q) = %q, want %q", wt, got, m)
	}
}

func TestRootWorktreeAbsoluteGitDirOutsideMainTree(t *testing.T) {
	m := t.TempDir()
	gitMain(t, m)
	wtRoot := t.TempDir()
	wt := filepath.Join(wtRoot, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	gitWorktreeOf(t, m, wt)
	if got := repo.Root(wt); got != m {
		t.Errorf("Root(%q) = %q, want %q", wt, got, m)
	}
}

func TestRootWorktreeRelativeGitDir(t *testing.T) {
	parent := t.TempDir()
	m := filepath.Join(parent, "main")
	wt := filepath.Join(parent, "wt")
	if err := os.MkdirAll(filepath.Join(m, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("..", "main", ".git", "worktrees", "wt")
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+rel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := repo.Root(wt); got != m {
		t.Errorf("Root(%q) = %q, want %q", wt, got, m)
	}
}

func TestRootNoGit(t *testing.T) {
	dir := t.TempDir()
	if got := repo.Root(dir); got != "" {
		t.Errorf("Root(%q) = %q, want empty", dir, got)
	}
}

func TestRootNoGitSubdir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := repo.Root(sub); got != "" {
		t.Errorf("Root(%q) = %q, want empty", sub, got)
	}
}

func TestRootEmpty(t *testing.T) {
	if got := repo.Root(""); got != "" {
		t.Errorf("Root(\"\") = %q, want empty", got)
	}
}
