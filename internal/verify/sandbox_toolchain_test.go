package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// suiteRepo is the repository this test lives in, with the commit it stands on.
//
// The sandbox test needs a real module graph: a fixture module without
// dependencies would compile from the standard library alone and say nothing
// about the module cache that is the point of these tests.
func suiteRepo(t *testing.T) (repo, commit string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("cannot resolve the repository root: %v", err)
	}
	commit = engine.Head(root)
	if commit == "" {
		t.Skip("the working tree is not a git repository with a commit")
	}
	return root, commit
}

// goAvailability names the missing prerequisite instead of skipping silently.
func goAvailability(t *testing.T) string {
	t.Helper()
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is not available")
	}
	command := exec.Command("go", "env", "GOMODCACHE")
	output, err := command.Output()
	if err != nil {
		t.Skipf("go cannot report its module cache: %v", err)
	}
	cache := strings.TrimSpace(string(output))
	if cache == "" {
		t.Skip("go reports no GOMODCACHE, so there is no module cache to bind")
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Skipf("the reported module cache %q does not exist", cache)
	}
	return cache
}

// The defect: a green test run in the sandbox was unverifiable, because the
// sandbox hid the module cache and has no network. With the profile's cache
// bridge the same suite compiles and the claim is confirmed.
func TestSandboxConfirmsAGoTestRun(t *testing.T) {
	goAvailability(t)
	repo, commit := suiteRepo(t)
	report := "**send_to payload:** `[COMMIT: " + commit + "]`\n" +
		"Tests run: go test ./internal/verify/engine -count=1 -> exit 0\n"

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  report,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "toolchain-go"),
		Budget:  5 * time.Minute,
		Sandbox: engine.SandboxRequire,
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
		t.Fatalf("verdicts = %+v, want a tests_green verdict", result.Verdicts)
	}
	if tested.Status != StatusConfirmed {
		t.Fatalf("status = %s (class %q): %s", tested.Status, tested.Class, tested.Reason)
	}
	if tested.Sandboxed == nil || !*tested.Sandboxed {
		t.Error("the confirmed run was not sandboxed")
	}
	// The confirmation must rest on the suite's own output, not on exit 0: the
	// inner run reports the package it tested.
	if !strings.Contains(tested.Reason, "ok") ||
		!strings.Contains(tested.Reason, "github.com/carsteneu/yesmem/internal/verify/engine") {
		t.Fatalf("reason = %q, want the inner test run's own report", tested.Reason)
	}
	// The verdict shows the sandbox that ran, so the cache bind must be part of
	// the command line it records (the read-only root alone is one bind).
	if binds := strings.Count(tested.Command, "--ro-bind"); binds < 2 {
		t.Fatalf("command = %q, want the bound cache in the recorded sandbox", tested.Command)
	}
}

// Without the profile the same run stays an environment gap: the sandbox has no
// network, so the toolchain switch cannot happen and nothing is claimed about
// the code.
func TestSandboxKeepsAGoTestRunUnverifiableWithoutAProfile(t *testing.T) {
	goAvailability(t)
	repo, commit := suiteRepo(t)

	ctx := &engine.Ctx{
		Repo:      repo,
		TmpDir:    filepath.Join(fixtureTmpRoot(t), "toolchain-noprofile"),
		Allowlist: engine.DefaultCommandAllowlist,
		Sandbox:   engine.SandboxRequire,
		// No language profile: no cache is bound.
	}
	verdict := claimtypes.CheckTests(engine.Claim{
		Kind:        "tests_green",
		Command:     "go test ./internal/verify/engine -count=1",
		Commit:      commit,
		ClaimedExit: 0,
	}, ctx)

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s), want %s", verdict.Status, verdict.Reason, engine.StatusUnverifiable)
	}
	if verdict.Class != engine.ClassEnvironment {
		t.Fatalf("class = %q (%s), want %q", verdict.Class, verdict.Reason, engine.ClassEnvironment)
	}
}

// A suite that keeps its temp files under a deep directory is refuted, not
// confirmed: a unix socket path beyond the kernel's limit cannot be bound, and
// a real suite hits exactly that when it starts a server on t.TempDir(). The
// sandbox's temp directory must therefore be short, which is what the profile
// decides.
func TestSandboxGivesATruthfulSuiteAShortTempDir(t *testing.T) {
	goAvailability(t)
	repo, commit := makeRepoFiles(t, "shortsocket", map[string]string{
		"go.mod": "module fixture\n\ngo 1.21\n",
		"socket_test.go": `package fixture

import (
	"net"
	"path/filepath"
	"testing"
)

func TestTempDirCanHostAUnixSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("cannot bind %q: %v", socket, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
`,
	})
	report := "**send_to payload:** `[COMMIT: " + commit + "]`\n" +
		"Tests run: go test ./... -> exit 0\n"

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  report,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "toolchain-shortsocket"),
		Budget:  5 * time.Minute,
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
			t.Fatalf("status = %s (class %q): %s", verdict.Status, verdict.Class, verdict.Reason)
		}
		return
	}
	t.Fatalf("verdicts = %+v, want a tests_green verdict", result.Verdicts)
}

// A bound cache must not come at the price of the network namespace: the run
// still cannot reach the outside.
func TestSandboxHasNoNetworkWithABoundCache(t *testing.T) {
	cache := goAvailability(t)
	repo, commit := suiteRepo(t)
	// The cache path was read with the real HOME; move HOME to the fixture root
	// afterwards. A cache under the operator's home may only be bound when the
	// toolchain named it itself, and this test is about the network namespace,
	// not about that rule.
	t.Setenv("HOME", fixtureTmpRoot(t))

	probe := "import socket; socket.create_connection(('1.1.1.1', 53), 3); print('REACHED')"
	ctx := &engine.Ctx{
		Repo:    repo,
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "toolchain-network"),
		Sandbox: engine.SandboxRequire,
		Languages: []engine.Language{{
			Name:            "probe",
			TestCommand:     "python3 -m pytest",
			SandboxReadOnly: []string{cache},
		}},
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not available")
	}

	outcome, refusal := ctx.RunInCheckout("checkout", "python3 -c ...", commit,
		[]string{"python3", "-c", probe})
	if refusal != nil {
		t.Fatalf("run refused: %s", refusal.Reason)
	}
	if strings.Contains(outcome.RawOutput, "REACHED") {
		t.Fatalf("the sandbox reached the network: %q", outcome.RawOutput)
	}
	if outcome.Code == 0 {
		t.Fatalf("the probe exited 0, so it never failed to connect: %q", outcome.RawOutput)
	}
	if !outcome.Sandboxed {
		t.Fatal("the probe was not sandboxed")
	}
}
