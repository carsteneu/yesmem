package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// WorktreeState is what a verdict key needs beside HEAD and the branch: a clean
// tree and a tree with residue are two states, and a judgement about one must not
// answer for the other.
func TestWorktreeStateChangesWithResidue(t *testing.T) {
	repo := worktreeRepo(t)
	clean := WorktreeState(repo)
	if clean == "" {
		t.Fatal("a clean repository has no worktree state")
	}
	if again := WorktreeState(repo); again != clean {
		t.Errorf("the state moved without a change: %q then %q", clean, again)
	}

	if err := os.WriteFile(filepath.Join(repo, "leftover.go"), []byte("package leftover\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dirty := WorktreeState(repo)
	if dirty == clean {
		t.Error("uncommitted residue did not change the worktree state")
	}
	if dirty == "" {
		t.Error("a dirty repository reports no worktree state")
	}

	// An ignored file is not residue: the project excluded it from its history.
	if err := os.MkdirAll(filepath.Join(repo, ".cache"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".cache", "x"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if withIgnored := WorktreeState(repo); withIgnored != dirty {
		t.Errorf("an ignored file changed the state: %q, want %q", withIgnored, dirty)
	}
}

// A directory that is not a repository cannot be read: the state is empty, and a
// key built from it stays usable instead of claiming a tree nobody looked at.
func TestWorktreeStateWithoutARepositoryIsEmpty(t *testing.T) {
	if got := WorktreeState(t.TempDir()); got != "" {
		t.Errorf("state = %q for a directory that is no repository, want empty", got)
	}
	if got := WorktreeState(""); got != "" {
		t.Errorf("state = %q without a path, want empty", got)
	}
}

// worktreeRepo is a one-commit repository under the project's own temp space.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".yesmem", "tmp", "worktreestate"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	repo, err := os.MkdirTemp(root, "repo-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"HOME=" + repo,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"PATH=" + os.Getenv("PATH"),
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".cache/\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "one")
	return repo
}

// The digest is a read. Rewriting the index would contend with the commit the
// agent is about to make, and the documented operation is read-only.
func TestWorktreeStateDoesNotWriteTheIndex(t *testing.T) {
	repo := worktreeRepo(t)
	// A touched file makes the stat cache stale, which is what makes git refresh
	// (and rewrite) the index on a status run.
	if err := os.Chtimes(filepath.Join(repo, "file.txt"), time.Unix(1000, 0), time.Unix(1000, 0)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}

	WorktreeState(repo)

	after, err := os.Stat(index)
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf("the digest rewrote .git/index (mtime %v -> %v)", before.ModTime(), after.ModTime())
	}
}
