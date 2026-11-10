package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// profileRepoDir is a throwaway project root under the worktree's own temp space.
func profileRepoDir(t *testing.T, name string) string {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "verifyprofiles"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	return root
}

// profileRepo builds a two-commit repository: the base tree, then the head tree.
func profileRepo(t *testing.T, name string, base, head map[string]string) (repo, baseCommit, headCommit string) {
	t.Helper()
	repo = profileRepoDir(t, name)
	write := func(files map[string]string) {
		for filename, content := range files {
			path := filepath.Join(repo, filename)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write %s: %v", filename, err)
			}
		}
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"HOME=" + repo,
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
	git("init", "-q", "-b", "main")
	write(base)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	baseCommit = git("rev-parse", "HEAD")
	write(head)
	git("add", "-A")
	git("commit", "-q", "-m", "head")
	headCommit = git("rev-parse", "HEAD")
	return repo, baseCommit, headCommit
}

// The project file has to reach the claims, not only ResolveLanguages. Ruby is
// covered by the file alone and the file names no coverage command, so the
// coverage claim must become an environment gap that names "ruby" — not the
// defect that says no profile covers the changed file. A valid file whose merge
// result never reaches engine.Ctx is the failure this test exists for.
func TestTheProjectProfileReachesTheClaims(t *testing.T) {
	repo, base, head := profileRepo(t, "reaches",
		map[string]string{"lib/impl.rb": "limit = 2\n"},
		map[string]string{"lib/impl.rb": "limit = 1\n"},
	)
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: ruby\n    source_extensions: [\".rb\"]\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Base:    base,
		Report:  "[COMMIT: " + head + "]\n[COVERAGE: changed-lines]\n",
		TmpDir:  profileRepoDir(t, "reaches-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	var coverage *Verdict
	for index := range report.Verdicts {
		if report.Verdicts[index].Kind == "coverage_delta" {
			coverage = &report.Verdicts[index]
		}
	}
	if coverage == nil {
		t.Fatalf("no coverage_delta verdict in %+v", report.Verdicts)
	}
	if coverage.Status != StatusUnverifiable || coverage.Class != ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			coverage.Status, coverage.Class, coverage.Reason)
	}
	if !strings.Contains(coverage.Reason, "ruby") {
		t.Errorf("reason = %q, want it to name the profile's language", coverage.Reason)
	}
}

// A broken project profile blocks the whole run with a defect verdict. The
// alternative, silently running the built-in entry, would execute a suite the
// project's own file says is wrong, and the verdict would be attributed to a
// profile nobody chose.
func TestASchemaErrorBlocksTheRun(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: go\n    test_commnd: go test ./...\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	report, err := Verify(context.Background(), Request{
		Repo:   repo,
		Report: "",
		TmpDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Verify returned an error instead of a verdict: %v", err)
	}
	if report.Decision.Accept {
		t.Fatal("a report was accepted although the project profile is broken")
	}
	if len(report.Verdicts) == 0 {
		t.Fatal("no verdict for a broken project profile")
	}
	verdict := report.Verdicts[0]
	if verdict.Status != StatusUnverifiable || verdict.Class != ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, ".yesmem/verify.yaml") {
		t.Errorf("reason does not name the profile file: %q", verdict.Reason)
	}
}
