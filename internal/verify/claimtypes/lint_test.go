package claimtypes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The corpus only exercises a non-allowlisted lint command, so the tool
// adapters need their own tests: a success line is what confirms a lint claim,
// and it has to be bound to the checkout.

func lintRepo(t *testing.T, name, consoleScript string, twigFiles int) (repo, commit string) {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "lint"))
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
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "templates"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "console"), []byte(consoleScript), 0o755); err != nil {
		t.Fatalf("write console: %v", err)
	}
	// A directory git can see: an empty templates/ would not be committed.
	if err := os.WriteFile(filepath.Join(root, "templates", "README.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	for index := 0; index < twigFiles; index++ {
		file := filepath.Join(root, "templates", "t"+string(rune('a'+index))+".twig")
		if err := os.WriteFile(file, []byte("{{ x }}\n"), 0o644); err != nil {
			t.Fatalf("write twig: %v", err)
		}
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
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
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	return root, git("rev-parse", "HEAD")
}

func lintCtx(t *testing.T, repo string) *engine.Ctx {
	t.Helper()
	return &engine.Ctx{
		Repo:   repo,
		TmpDir: filepath.Join(filepath.Dir(repo), "tmp"),
		// The sandbox path is covered by the corpus and the limits test; these
		// tests judge the adapters, not bubblewrap.
		Sandbox:          engine.SandboxOff,
		Allowlist:        engine.DefaultCommandAllowlist,
		LintAllowlist:    DefaultLintAllowlist,
		ComputeAllowlist: DefaultComputeAllowlist,
	}
}

func TestCheckLintConfirmsOnTheToolsOwnLine(t *testing.T) {
	script := "#!/bin/sh\necho 'All 2 Twig files contain valid syntax.'\nexit 0\n"
	repo, commit := lintRepo(t, "ok", script, 2)

	verdict := CheckLint(engine.Claim{
		Kind:    "lint",
		Command: "bin/console lint:twig templates",
		Commit:  commit,
	}, lintCtx(t, repo))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "evidence: All 2 Twig files") {
		t.Errorf("reason = %q", verdict.Reason)
	}
}

// A success line whose count does not match the files in the checkout means the
// run was redirected, and a redirected run confirms nothing.
func TestCheckLintRefusesARedirectedCount(t *testing.T) {
	script := "#!/bin/sh\necho 'All 1 Twig files contain valid syntax.'\nexit 0\n"
	repo, commit := lintRepo(t, "redirect", script, 2)

	verdict := CheckLint(engine.Claim{
		Kind:    "lint",
		Command: "bin/console lint:twig templates",
		Commit:  commit,
	}, lintCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if verdict.Class != engine.ClassDefect {
		t.Errorf("class = %q (%s), want defect", verdict.Class, verdict.Reason)
	}
}

// Exit 0 without the tool's own success line is not a lint result.
func TestCheckLintRefusesSilentSuccess(t *testing.T) {
	repo, commit := lintRepo(t, "silent", "#!/bin/sh\nexit 0\n", 2)

	verdict := CheckLint(engine.Claim{
		Kind:    "lint",
		Command: "bin/console lint:twig templates",
		Commit:  commit,
	}, lintCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if verdict.Class != engine.ClassUnverifiable {
		t.Errorf("class = %q, want the residual class", verdict.Class)
	}
}

func TestCheckLintRefutesARealFailure(t *testing.T) {
	script := "#!/bin/sh\necho 'Twig syntax error in templates/a.twig' >&2\nexit 1\n"
	repo, commit := lintRepo(t, "failure", script, 2)

	verdict := CheckLint(engine.Claim{
		Kind:    "lint",
		Command: "bin/console lint:twig templates",
		Commit:  commit,
	}, lintCtx(t, repo))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "Twig syntax error") {
		t.Errorf("reason = %q, want the tool's own line", verdict.Reason)
	}
}

// An empty target confirms nothing, exactly like "0 passing" in the test gate.
func TestCheckLintRefusesAnEmptyTarget(t *testing.T) {
	repo, commit := lintRepo(t, "emptytarget", "#!/bin/sh\necho 'All 0 Twig files contain valid syntax.'\n", 0)

	verdict := CheckLint(engine.Claim{
		Kind:    "lint",
		Command: "bin/console lint:twig templates",
		Commit:  commit,
	}, lintCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "confirms nothing") {
		t.Errorf("reason = %q", verdict.Reason)
	}
}

// A lint claim without a commit has nothing to run against.
func TestCheckLintNeedsACommit(t *testing.T) {
	repo, _ := lintRepo(t, "nocommit", "#!/bin/sh\nexit 0\n", 1)

	verdict := CheckLint(engine.Claim{Kind: "lint", Command: "bin/console lint:twig templates"}, lintCtx(t, repo))
	if verdict.Class != engine.ClassDefect {
		t.Errorf("class = %q (%s), want defect", verdict.Class, verdict.Reason)
	}
}

// A report-wide lint claim with no repository at all cannot be checked: the
// missing repository is an environment gap, not the report's fault.
func TestCheckLintWithoutRepoIsEnvironment(t *testing.T) {
	verdict := CheckLint(engine.Claim{Kind: "lint", Command: "php -l x.php"}, &engine.Ctx{})
	if verdict.Class != engine.ClassEnvironment {
		t.Errorf("class = %q (%s), want environment", verdict.Class, verdict.Reason)
	}
}
