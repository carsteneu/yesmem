package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// bunFixtureRepo is a project whose suite needs a program outside the sandbox:
// the shape of the daemon package on this host, where the reporter's tests reach
// for bun and the sandbox hides the home it lives in.
//
// The tool directory is named "bin" on purpose: that is what a tool bin
// directory is, and the profile's mount is judged by that.
func bunFixtureRepo(t *testing.T, profile string) (repo, toolDir string) {
	t.Helper()
	root := fixtureTmpRoot(t)
	parent := filepath.Join(root, "bunfixture")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	repo, err := os.MkdirTemp(parent, "repo-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	toolDir = filepath.Join(root, "tools", t.Name(), "bin")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	tool := filepath.Join(toolDir, "fixture-bun")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'fixture-1.2.3\\n'\n"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.21\n",
		"bun_test.go": `package fixture

import (
	"os/exec"
	"testing"
)

func TestTheSuiteNeedsABun(t *testing.T) {
	out, err := exec.Command("fixture-bun", "--version").Output()
	if err != nil {
		t.Fatalf("fixture-bun is not available: %v", err)
	}
	t.Logf("fixture-bun reports %s", out)
}
`,
	}
	if profile != "" {
		files[filepath.Join(".yesmem", "verify.yaml")] = profile
	}
	for name, content := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=fixture@local", "-c", "user.name=fixture", "commit", "-q", "-m", "fixture"},
	} {
		if out, err := gitFixture(repo, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo, toolDir
}

// gitFixture runs one git command against a fixture repository, with an
// identity of its own so the host configuration cannot leak into it.
func gitFixture(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = []string{
		"HOME=" + repo,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
		"PATH=" + os.Getenv("PATH"),
	}
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// withoutRequires is the honest measure of today: the sandbox hides the tool,
// the suite fails, and the verdict lands on the report — REFUTED. This test
// pins the unchanged default, so a later "improvement" cannot silently turn
// every missing-tool refutation into a gap.
func TestAMissingToolWithoutADeclarationStaysARefutation(t *testing.T) {
	repo, _ := bunFixtureRepo(t, "")

	verdict := judgeFixtureSuite(t, repo)
	if verdict.Status != StatusRefuted {
		t.Fatalf("status = %s class = %q (%s), want the unchanged refutation",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// With the tool declared, the same run is an environment gap: the confinement
// cannot provide a program the reporter's suite needs, and the report is not
// wrong about it. This is the whole difference the declaration buys.
func TestAMissingRequiredToolIsAnEnvironmentGapNotARefutation(t *testing.T) {
	repo, _ := bunFixtureRepo(t, `languages:
  - name: go
    requires: ["fixture-bun"]
`)

	verdict := judgeFixtureSuite(t, repo)
	if verdict.Status != StatusUnverifiable || verdict.Class != ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "fixture-bun") {
		t.Fatalf("reason = %q, want the missing tool named", verdict.Reason)
	}
}

// Declared and mounted, the suite runs: the tool is bound, the run can look it
// up, and the claim is confirmed on the suite's own output.
func TestADeclaredAndMountedToolConfirmsTheSuite(t *testing.T) {
	profile := `languages:
  - name: go
    requires: ["fixture-bun"]
`
	repo, toolDir := bunFixtureRepo(t, profile)
	// The mount is named after the directory the fixture created, which the
	// profile cannot know: write the final profile now that it exists.
	writeFixtureProfile(t, repo, profile+"    sandbox_readonly: [\""+toolDir+"\"]\n")

	verdict := judgeFixtureSuite(t, repo)
	if verdict.Status != StatusConfirmed {
		t.Fatalf("status = %s class = %q (%s), want a confirmed run; output=%q",
			verdict.Status, verdict.Class, verdict.Reason, verdict.Output)
	}
	if verdict.Sandboxed == nil || !*verdict.Sandboxed {
		t.Error("the confirmed run was not sandboxed")
	}
}

// judgeFixtureSuite runs the fixture's suite through the claim the report makes.
func judgeFixtureSuite(t *testing.T, repo string) engine.Verdict {
	t.Helper()
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is not available")
	}
	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + engine.Head(repo) + "]`\nTests run: go test ./... -count=1 -> exit 0\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "bunfixture-runs", t.Name()),
		Budget:  2 * time.Minute,
		Sandbox: engine.SandboxRequire,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range result.Verdicts {
		if verdict.Kind == "tests_green" {
			return verdict
		}
	}
	t.Fatalf("verdicts = %+v, want a tests_green verdict", result.Verdicts)
	return engine.Verdict{}
}

func writeFixtureProfile(t *testing.T, repo, profile string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}
