package claimtypes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// makeClaimRepo builds a one-commit repository from the given files.
func makeClaimRepo(t *testing.T, name string, files map[string]string) (repo, commit string) {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "claimrepos"))
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
	for filename, content := range files {
		path := filepath.Join(root, filename)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(filename, "console") || strings.HasSuffix(filename, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", filename, err)
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

func unsandboxedCtx(t *testing.T, repo string) *engine.Ctx {
	t.Helper()
	return &engine.Ctx{
		Repo:             repo,
		TmpDir:           filepath.Join(filepath.Dir(repo), "tmp"),
		Sandbox:          engine.SandboxOff,
		Allowlist:        engine.DefaultCommandAllowlist,
		LintAllowlist:    DefaultLintAllowlist,
		ComputeAllowlist: DefaultComputeAllowlist,
	}
}

// The critical false-confirmation path: a wrapper swallows the runner's exit
// code, so the claim says exit 0 while the output reports failures. A green
// claim must never be confirmed from that.
func TestCheckTestsRefusesGreenWithFailureOutput(t *testing.T) {
	makefile := "test:\n\t@echo \"Ran 4 tests in 0.1s\"\n\t@echo \"3 passed, 1 failed\"\n\t@exit 0\n"
	repo, commit := makeClaimRepo(t, "greenwithfailure", map[string]string{"Makefile": makefile})

	verdict := CheckTests(engine.Claim{
		Kind:        "tests_green",
		Command:     "make test",
		Commit:      commit,
		ClaimedExit: 0,
		HasExit:     true,
	}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s), want unverifiable", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "reports failing tests") {
		t.Errorf("reason = %q", verdict.Reason)
	}
}

// The same run without the failure line is green: the guard must not reject a
// healthy suite.
func TestCheckTestsConfirmsACleanGreenRun(t *testing.T) {
	makefile := "test:\n\t@echo \"Ran 4 tests in 0.1s\"\n\t@echo \"4 passed, 0 skipped\"\n\t@exit 0\n"
	repo, commit := makeClaimRepo(t, "cleangreen", map[string]string{"Makefile": makefile})

	verdict := CheckTests(engine.Claim{
		Kind:        "tests_green",
		Command:     "make test",
		Commit:      commit,
		ClaimedExit: 0,
		HasExit:     true,
	}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
}

// A runner shadowed from an operator-owned directory must not decide the
// verdict: the run uses a PATH without the operator's home.
func TestCheckTestsIgnoresARunnerFromTheHomeDirectory(t *testing.T) {
	homeParent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "fakehome"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home, err := os.MkdirTemp(homeParent, "home-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	stubDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stub := "#!/bin/sh\necho 'Ran 1 test in 0.001s'\necho 'OK'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "python3"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	repo, commit := makeClaimRepo(t, "shadowed", map[string]string{
		"test_fail.py": "import unittest\n\n\nclass T(unittest.TestCase):\n    def test_fail(self):\n        self.assertEqual(1, 2)\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+"/usr/local/bin:/usr/bin:/bin")

	verdict := CheckTests(engine.Claim{
		Kind:        "tests_green",
		Command:     "python3 -m unittest test_fail",
		Commit:      commit,
		ClaimedExit: 0,
		HasExit:     true,
	}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want refuted: a shadowed runner must not confirm", verdict.Status, verdict.Reason)
	}
}
