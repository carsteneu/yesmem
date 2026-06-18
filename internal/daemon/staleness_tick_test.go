package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestCommitsSince_NoSince(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	// Create first commit
	writeCommit(t, dir, "a.txt", "initial")
	hash1 := gitHead(t, dir)

	// latestCommitsSince with empty since should return all commits
	hashes, err := latestCommitsSince(dir, "")
	if err != nil {
		t.Fatalf("latestCommitsSince: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != hash1 {
		t.Errorf("expected [%s], got %v", hash1, hashes)
	}

	// Create second commit
	writeCommit(t, dir, "b.txt", "second")
	hash2 := gitHead(t, dir)

	hashes, err = latestCommitsSince(dir, "")
	if err != nil {
		t.Fatalf("latestCommitsSince: %v", err)
	}
	if len(hashes) != 2 || hashes[0] != hash1 || hashes[1] != hash2 {
		t.Errorf("expected [%s, %s], got %v", hash1, hash2, hashes)
	}
}

func TestLatestCommitsSince_WithSince(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	writeCommit(t, dir, "a.txt", "first")
	hash1 := gitHead(t, dir)
	writeCommit(t, dir, "b.txt", "second")
	hash2 := gitHead(t, dir)

	// With since=hash1, should return only hash2
	hashes, err := latestCommitsSince(dir, hash1)
	if err != nil {
		t.Fatalf("latestCommitsSince: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != hash2 {
		t.Errorf("expected [%s] after %s, got %v", hash2, hash1, hashes)
	}
}

func TestLatestCommitsSince_NoNewCommits(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	writeCommit(t, dir, "a.txt", "only")
	hash1 := gitHead(t, dir)

	// Since the only commit, nothing newer
	hashes, err := latestCommitsSince(dir, hash1)
	if err != nil {
		t.Fatalf("latestCommitsSince: %v", err)
	}
	if len(hashes) != 0 {
		t.Errorf("expected empty slice, got %v", hashes)
	}
}

func TestStartStalenessTick_SkipsWhenNoClient(t *testing.T) {
	handler := &Handler{} // no CommitEvalClient set
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately without panic
	done := make(chan struct{})
	go func() {
		startStalenessTick(ctx, handler, "/nonexistent")
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(1 * time.Second):
		t.Fatal("startStalenessTick did not return when client is nil")
	}
}

// Helpers

func initGit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, string(out))
		}
	}
}

func writeCommit(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"add", filename},
		{"commit", "-m", filename},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[0], err, string(out))
		}
	}
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}
