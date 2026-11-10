package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A content diff must never run a program the repository configures. git does that
// for a diff driver (.gitattributes plus textconv) and for diff.external, and the
// verifier runs git on the host, outside the sandbox: repository-owned code would
// execute with the daemon's privileges.
//
// The case below commits a textconv driver that drops a marker file outside the
// repository. The marker must not appear.
func TestChangedLinesDoesNotRunRepositoryPrograms(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	marker := filepath.Join(root, "textconv-ran")
	script := filepath.Join(root, "driver.sh")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"HOME=" + root,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"PATH=" + os.Getenv("PATH"),
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt diff=probe\n"), 0o644); err != nil {
		t.Fatalf("write attributes: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	base := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "head")
	head := run("rev-parse", "HEAD")
	run("config", "diff.probe.textconv", script)
	run("config", "diff.external", script)

	ctx := &Ctx{Repo: repo}
	changed, result := ctx.ChangedLines(base, head)
	if result.Code != 0 {
		t.Fatalf("diff failed: %s", result.Output())
	}
	if len(changed["a.txt"]) != 1 || changed["a.txt"][0] != 1 {
		t.Errorf("changed = %v, want a.txt line 1", changed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's diff driver ran: a textconv program executed outside the sandbox")
	}
}
