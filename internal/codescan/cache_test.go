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
