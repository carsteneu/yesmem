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

// makeRepo builds a one-commit repository for tests that do not need the eval
// fixture.
func makeRepo(t *testing.T, name string) (repo, commit string) {
	t.Helper()
	return makeRepoFiles(t, name, map[string]string{"file.txt": "x\n"})
}

// makeRepoFiles builds a one-commit repository from the given files.
func makeRepoFiles(t *testing.T, name string, files map[string]string) (repo, commit string) {
	t.Helper()
	parent := filepath.Join(fixtureTmpRoot(t), "repos")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
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
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	return root, git("rev-parse", "HEAD")
}

// The run budget bounds the whole run: a spent budget is reported as a limit,
// never as an agent failure, and strict mode gives it its own exit code.
func TestSpentBudgetIsALimit(t *testing.T) {
	repo, commit := makeRepo(t, "budget")
	report := "**send_to payload:** `[COMMIT: " + commit + "]`\n" +
		"Tests run: python3 -m unittest test_ok -> exit 0\n"

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  report,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "budget"),
		Budget:  1,
		Strict:  true,
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	var tested *Verdict
	for index := range result.Verdicts {
		if result.Verdicts[index].Kind == "tests_green" {
			tested = &result.Verdicts[index]
		}
	}
	if tested == nil {
		t.Fatal("no tests_green verdict")
	}
	if tested.Class != ClassLimit {
		t.Errorf("class = %q (%s), want %q", tested.Class, tested.Reason, ClassLimit)
	}
	if result.ExitCode != ExitLimit {
		t.Errorf("exit = %d, want %d", result.ExitCode, ExitLimit)
	}
}

// Without bubblewrap a command claim is refused, not run unsandboxed. The
// repository is reachable (git is on PATH), so the gate under test is the
// sandbox, not the repo guard.
func TestSandboxRequireRefusesWithoutBwrap(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	repo, commit := makeRepo(t, "sandbox")

	binDir, err := os.MkdirTemp(fixtureTmpRoot(t), "bin-no-bwrap-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("PATH", binDir)
	if found := engine.FindBwrap(); found != "" {
		t.Skipf("bwrap is still reachable at %s", found)
	}

	ctx := &engine.Ctx{
		Repo:    repo,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "sandbox"),
		Sandbox: engine.SandboxRequire,
	}
	_, refusal := ctx.RunInCheckout("checkout", "true", commit, []string{"true"})
	if refusal == nil {
		t.Fatal("the run was not refused without bubblewrap")
	}
	if refusal.Status != StatusUnverifiable {
		t.Errorf("status = %s, want %s", refusal.Status, StatusUnverifiable)
	}
	if refusal.Class != ClassEnvironment {
		t.Errorf("class = %q, want %q", refusal.Class, ClassEnvironment)
	}
	if !strings.Contains(refusal.Reason, "bubblewrap") {
		t.Errorf("reason = %q, want it to name bubblewrap", refusal.Reason)
	}
}

// A report with more claims than the verifier accepts is refused outright:
// every claim can cost a traversal or a run, so the count is bounded.
func TestMaxClaimsIsEnforced(t *testing.T) {
	repo, commit := makeRepo(t, "manyclaims")
	var builder strings.Builder
	for index := 0; index < MaxClaims+1; index++ {
		builder.WriteString("[COMMIT: " + commit + "]\n")
	}

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  builder.String(),
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "manyclaims"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(result.Verdicts) != 1 {
		t.Fatalf("verdicts = %d, want 1 refusal", len(result.Verdicts))
	}
	if result.Verdicts[0].Class != ClassDefect {
		t.Errorf("class = %q (%s), want defect", result.Verdicts[0].Class, result.Verdicts[0].Reason)
	}
	if result.ExitCode != ExitDefect {
		t.Errorf("exit = %d, want %d", result.ExitCode, ExitDefect)
	}
}

// The caller's cancellation ends the run: remaining claims are not judged, and
// no claim is confirmed after a cancel.
func TestCancelledRunStopsJudging(t *testing.T) {
	repo, commit := makeRepo(t, "cancelled")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := Verify(cancelled, Request{
		Repo: repo,
		Report: "**send_to payload:** `[COMMIT: " + commit + "]`\n" +
			"Tests run: python3 -m unittest test_ok -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "cancelled"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(result.Verdicts) == 0 {
		t.Fatal("no verdicts")
	}
	for _, verdict := range result.Verdicts {
		if verdict.Status == StatusConfirmed {
			t.Errorf("%s: confirmed after the run was cancelled", verdict.Kind)
		}
		if !strings.Contains(verdict.Reason, "cancelled") {
			t.Errorf("%s: reason = %q, want it to name the cancellation", verdict.Kind, verdict.Reason)
		}
	}
}

// The read-only root must not expose the operator's home: a repository-owned
// command could otherwise print keys or credentials into a verdict.
func TestSandboxHidesTheHomeDirectory(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is not available")
	}
	homeParent := filepath.Join(fixtureTmpRoot(t), "homes")
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home, err := os.MkdirTemp(homeParent, "home-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("OPERATOR-SECRET\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	t.Setenv("HOME", home)

	repo, commit := makeRepo(t, "homesandbox")
	ctx := &engine.Ctx{
		Repo:    repo,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "homesandbox"),
		Sandbox: engine.SandboxRequire,
	}
	// A usable sandbox is required for this assertion; when the host cannot
	// create namespaces, the checker itself refuses and the test says nothing
	// about the mount.
	if err := engine.SandboxProbe(engine.FindBwrap(), repo); err != nil {
		t.Skipf("the host cannot start a sandbox: %v", err)
	}

	outcome, refusal := ctx.RunInCheckout("checkout", "/bin/cat "+secret, commit, []string{"/bin/cat", secret})
	if refusal != nil {
		t.Fatalf("run refused: %s", refusal.Reason)
	}
	if strings.Contains(outcome.RawOutput, "OPERATOR-SECRET") {
		t.Fatalf("the sandbox exposed the operator home: %q", outcome.RawOutput)
	}
	if outcome.Code == 0 {
		t.Fatal("reading a hidden home file succeeded")
	}
}

// No budget means the default budget, not "already spent": a negative or zero
// budget must not read as an exhausted one.
func TestZeroBudgetUsesTheDefault(t *testing.T) {
	repo, commit := makeRepo(t, "defaultbudget")
	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + commit + "]`\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "defaultbudget"),
		Budget:  -1,
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// The run judges the report's commit and the verifier's own worktree
	// criterion; the budget question is about the commit claim.
	var commitVerdict *Verdict
	for index := range result.Verdicts {
		if result.Verdicts[index].Kind == "commit_exists" {
			commitVerdict = &result.Verdicts[index]
		}
	}
	if commitVerdict == nil || commitVerdict.Status != StatusConfirmed {
		t.Fatalf("verdicts = %+v, want one confirmed commit", result.Verdicts)
	}
	if commitVerdict.Class == ClassLimit {
		t.Error("a default budget must not read as an exhausted one")
	}
}

// The report text has its own ceiling: parsing happens before any budget check.
func TestOversizedReportIsRefused(t *testing.T) {
	repo, _ := makeRepo(t, "bigreport")
	report := strings.Repeat("x", MaxReportBytes+1)

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  report,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "bigreport"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(result.Verdicts) != 1 {
		t.Fatalf("verdicts = %d, want one refusal", len(result.Verdicts))
	}
	if result.Verdicts[0].Class != ClassDefect {
		t.Errorf("class = %q (%s), want defect", result.Verdicts[0].Class, result.Verdicts[0].Reason)
	}
}

// A chatty command is stopped at the output cap, not measured until the
// timeout: the limit is meant to end the work.
func TestRunawayOutputIsKilled(t *testing.T) {
	makefile := "test:\n\t@head -c 20000000 /dev/zero | tr '\\0' x\n\t@echo done\n"
	repo, commit := makeRepoFiles(t, "runaway", map[string]string{"Makefile": makefile})

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + commit + "]`\nTests run: make test -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "runaway"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range result.Verdicts {
		if verdict.Kind != "tests_green" {
			continue
		}
		if verdict.Class != ClassLimit {
			t.Fatalf("class = %q (%s), want limit", verdict.Class, verdict.Reason)
		}
		if verdict.Status == StatusConfirmed {
			t.Fatal("a runaway run was confirmed")
		}
		return
	}
	t.Fatal("no tests_green verdict")
}

// A report without a commit cannot bind a test run: that is a defect, not an
// environment gap.
func TestTestClaimWithoutCommitIsADefect(t *testing.T) {
	repo, _ := makeRepo(t, "nocommit")
	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "Tests run: python3 -m unittest test_ok -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "nocommit"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// One verdict for the report's claim and one for the verifier's worktree
	// criterion; the defect must sit on the report's claim.
	var reported *Verdict
	for index := range result.Verdicts {
		if result.Verdicts[index].Kind == "tests_green" {
			reported = &result.Verdicts[index]
		}
	}
	if reported == nil {
		t.Fatalf("verdicts = %+v, want the test claim", result.Verdicts)
	}
	if got := reported.Class; got != ClassDefect {
		t.Errorf("class = %q (%s), want %q",
			got, reported.Reason, ClassDefect)
	}
}

// The plan's false-negative probe with a compiled toolchain: an honest Go test
// report must be confirmed. A checkout whose temp directory sits at the module
// root, or a run without a module cache, would refute a true report instead.
func TestHonestGoTestReportIsConfirmed(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is not available")
	}
	repo, commit := makeRepoFiles(t, "gotest", map[string]string{
		"go.mod":      "module example.test/fixture\n\ngo 1.21\n",
		"sum_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestSum(t *testing.T) {\n\tif 1+1 != 2 {\n\t\tt.Fatal(\"arithmetic\")\n\t}\n}\n",
	})

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + commit + "]`\nTests run: go test ./... -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "gotest"),
		Sandbox: engine.SandboxRequire,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range result.Verdicts {
		if verdict.Kind != "tests_green" {
			continue
		}
		if verdict.Status != StatusConfirmed {
			t.Fatalf("honest go test report ended %s (%s): %s",
				verdict.Status, verdict.Class, verdict.Reason)
		}
		return
	}
	t.Fatal("no tests_green verdict")
}

// A missing runner is an environment gap in both modes. In the default
// sandboxed mode the sandbox used to fail execvp with exit 1, which read as a
// refutation of an honest report.
func TestSandboxedMissingRunnerIsAnEnvironmentGap(t *testing.T) {
	if _, found := engine.LookPathIn(engine.RunPath(), "yarn"); found {
		t.Skip("yarn is available on this host")
	}
	repo, commit := makeRepo(t, "missingrunner")

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + commit + "]`\nTests run: yarn test -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "missingrunner"),
		Sandbox: engine.SandboxRequire,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range result.Verdicts {
		if verdict.Kind != "tests_green" {
			continue
		}
		if verdict.Class != ClassEnvironment {
			t.Fatalf("class = %q (%s), want environment", verdict.Class, verdict.Reason)
		}
		if verdict.Status == StatusRefuted {
			t.Fatal("a missing runner was reported as a failing suite")
		}
		return
	}
	t.Fatal("no tests_green verdict")
}

// A binary named bwrap in an operator-writable directory would report
// "sandboxed" while confining nothing. The lookup ignores the operator's home,
// so the real binary wins.
func TestFakeBwrapInTheHomeIsIgnored(t *testing.T) {
	realBwrap := engine.FindBwrap()
	if realBwrap == "" {
		t.Skip("bubblewrap is not installed on this host")
	}
	homeParent := filepath.Join(fixtureTmpRoot(t), "fakebwrap")
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home, err := os.MkdirTemp(homeParent, "home-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fake := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "bwrap"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake bwrap: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	found := engine.FindBwrap()
	if found != realBwrap {
		t.Fatalf("FindBwrap = %q, want the real %q", found, realBwrap)
	}
	if strings.HasPrefix(found, home) {
		t.Fatalf("FindBwrap = %q, want the operator-owned binary ignored", found)
	}
}
