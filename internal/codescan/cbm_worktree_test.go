package codescan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithWorktreeGitSymlink_NoGit(t *testing.T) {
	dir := t.TempDir()
	called := false
	err := withWorktreeGitSymlink(dir, func() error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("expected fn to run once, err=%v called=%v", err, called)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should not exist, got err=%v", err)
	}
}

func TestWithWorktreeGitSymlink_RealRepoDir(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := withWorktreeGitSymlink(dir, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".git should still be a directory, got mode=%v", info.Mode())
	}
}

func TestWithWorktreeGitSymlink_WorktreeSwap(t *testing.T) {
	dir := t.TempDir()
	realGit := filepath.Join(dir, "realgit")
	if err := os.Mkdir(realGit, 0o755); err != nil {
		t.Fatal(err)
	}
	dotGit := filepath.Join(dir, ".git")
	original := []byte("gitdir: " + realGit + "\n")
	if err := os.WriteFile(dotGit, original, 0o644); err != nil {
		t.Fatal(err)
	}

	var observedMode os.FileMode
	var observedTarget string
	err := withWorktreeGitSymlink(dir, func() error {
		info, err := os.Lstat(dotGit)
		if err != nil {
			return err
		}
		observedMode = info.Mode()
		if observedMode&os.ModeSymlink != 0 {
			target, err := os.Readlink(dotGit)
			if err != nil {
				return err
			}
			observedTarget = target
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if observedMode&os.ModeSymlink == 0 {
		t.Errorf("expected .git to be symlink during fn, got mode=%v", observedMode)
	}
	if observedTarget != realGit {
		t.Errorf("symlink target mismatch: got %q want %q", observedTarget, realGit)
	}
	after, err := os.ReadFile(dotGit)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Errorf(".git file content not restored: got %q want %q", after, original)
	}
	info, err := os.Lstat(dotGit)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf(".git should be a regular file after restore, got mode=%v", info.Mode())
	}
}

func TestWithWorktreeGitSymlink_RestoresOnError(t *testing.T) {
	dir := t.TempDir()
	realGit := filepath.Join(dir, "realgit")
	if err := os.Mkdir(realGit, 0o755); err != nil {
		t.Fatal(err)
	}
	dotGit := filepath.Join(dir, ".git")
	original := []byte("gitdir: " + realGit)
	if err := os.WriteFile(dotGit, original, 0o644); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("fn failed")
	err := withWorktreeGitSymlink(dir, func() error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
	after, err := os.ReadFile(dotGit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "gitdir:") {
		t.Errorf(".git content not restored after error: got %q", after)
	}
}

func TestWithWorktreeGitSymlink_GitdirMissing(t *testing.T) {
	dir := t.TempDir()
	dotGit := filepath.Join(dir, ".git")
	if err := os.WriteFile(dotGit, []byte("gitdir: /nonexistent/path"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	err := withWorktreeGitSymlink(dir, func() error {
		called = true
		info, err := os.Lstat(dotGit)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("should not symlink when gitdir missing, mode=%v", info.Mode())
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("fn should still run, err=%v called=%v", err, called)
	}
}
