package codescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCachedScanner_CachesResult(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	// Create a fake .git/HEAD for cache key
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git", "refs", "heads"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("abc123\n"), 0644)

	cs := NewCachedScanner(&DirectoryScanner{})

	// First call should scan
	r1, err := cs.Scan(dir)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if r1 == nil || len(r1.Files) != 1 {
		t.Fatal("first scan should return result with 1 file")
	}

	// Second call should return cached (same HEAD)
	r2, err := cs.Scan(dir)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if r1 != r2 {
		t.Error("second scan should return same pointer (cached)")
	}
}

func TestCachedScanner_InvalidatesOnNewHead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git", "refs", "heads"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644)
	os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("abc123\n"), 0644)

	cs := NewCachedScanner(&DirectoryScanner{})

	r1, _ := cs.Scan(dir)

	// Change HEAD
	os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("def456\n"), 0644)

	r2, _ := cs.Scan(dir)

	if r1 == r2 {
		t.Error("should return new result after HEAD change")
	}
}

func TestCachedScanner_WorksWithoutGit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	// No .git directory

	cs := NewCachedScanner(&DirectoryScanner{})

	r1, err := cs.Scan(dir)
	if err != nil {
		t.Fatalf("scan without git: %v", err)
	}
	if r1 == nil {
		t.Fatal("should still return result without git")
	}

	// Second call — no git means no cache key, should re-scan
	r2, _ := cs.Scan(dir)
	if r1 == r2 {
		t.Error("without git, should not cache (no stable key)")
	}
}

func TestReadGitHead_ReadsRef(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git", "refs", "heads"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644)
	os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", "main"), []byte("abc123def\n"), 0644)

	head := ReadGitHead(dir)
	if !strings.Contains(head, "abc123def") {
		t.Errorf("expected abc123def, got %q", head)
	}
}

func TestReadGitHead_DetachedHead(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("abc123def456\n"), 0644)

	head := ReadGitHead(dir)
	if head != "abc123def456" {
		t.Errorf("expected detached HEAD hash, got %q", head)
	}
}

func TestReadGitHead_Worktree(t *testing.T) {
	mainRepo := t.TempDir()
	os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "mybranch"), 0755)
	os.MkdirAll(filepath.Join(mainRepo, ".git", "refs", "heads"), 0755)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "mybranch", "HEAD"), []byte("ref: refs/heads/mybranch\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "mybranch", "commondir"), []byte("../..\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "refs", "heads", "mybranch"), []byte("worktree999\n"), 0644)

	worktreeDir := t.TempDir()
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "mybranch")+"\n"), 0644)

	head := ReadGitHead(worktreeDir)
	if !strings.Contains(head, "worktree999") {
		t.Errorf("expected worktree HEAD worktree999, got %q", head)
	}
}

func TestProjectKey_Worktree(t *testing.T) {
	mainRepo := filepath.Join(t.TempDir(), "myproject")
	os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "feat-x"), 0755)

	worktreeDir := filepath.Join(t.TempDir(), "feat-x")
	os.MkdirAll(worktreeDir, 0755)
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "feat-x")+"\n"), 0644)

	key := projectKey(worktreeDir)
	if key != "myproject" {
		t.Errorf("expected project key 'myproject' (repo name), got %q", key)
	}
}

func TestProjectKey_RegularRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myrepo")
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	key := projectKey(dir)
	if key != "myrepo" {
		t.Errorf("expected 'myrepo', got %q", key)
	}
}

func TestReadGitHead_WorktreeDetachedHead(t *testing.T) {
	mainRepo := t.TempDir()
	os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "detached"), 0755)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "detached", "HEAD"), []byte("deadbeef123\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "detached", "commondir"), []byte("../..\n"), 0644)

	worktreeDir := t.TempDir()
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "detached")+"\n"), 0644)

	head := ReadGitHead(worktreeDir)
	if head != "deadbeef123" {
		t.Errorf("expected detached HEAD deadbeef123, got %q", head)
	}
}

func TestReadGitHead_WorktreePackedRefs(t *testing.T) {
	mainRepo := t.TempDir()
	os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "packed"), 0755)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "packed", "HEAD"), []byte("ref: refs/heads/packed-branch\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "packed", "commondir"), []byte("../..\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "packed-refs"), []byte("# pack-refs\nabc789packed refs/heads/packed-branch\n"), 0644)

	worktreeDir := t.TempDir()
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+filepath.Join(mainRepo, ".git", "worktrees", "packed")+"\n"), 0644)

	head := ReadGitHead(worktreeDir)
	if head != "abc789packed" {
		t.Errorf("expected packed ref abc789packed, got %q", head)
	}
}

func TestReadGitHead_WorktreeRelativeGitdir(t *testing.T) {
	base := t.TempDir()
	mainRepo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "rel"), 0755)
	os.MkdirAll(filepath.Join(mainRepo, ".git", "refs", "heads"), 0755)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "rel", "HEAD"), []byte("ref: refs/heads/rel\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "worktrees", "rel", "commondir"), []byte("../..\n"), 0644)
	os.WriteFile(filepath.Join(mainRepo, ".git", "refs", "heads", "rel"), []byte("relhash42\n"), 0644)

	worktreeDir := filepath.Join(base, "wt")
	os.MkdirAll(worktreeDir, 0755)
	relGitdir := filepath.Join("..", "repo", ".git", "worktrees", "rel")
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+relGitdir+"\n"), 0644)

	head := ReadGitHead(worktreeDir)
	if head != "relhash42" {
		t.Errorf("expected relhash42, got %q", head)
	}
}
