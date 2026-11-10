package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// A test claim that came back as an environment gap is the case an init exists
// for: the repository has no profile, so the verifier cannot say what its suite
// is. The deterministic message carries the one command that changes that, so the
// next agent does not have to read it out of a skill.
func TestDoneVerifyMessageNamesTheMissingProfileInit(t *testing.T) {
	findings := []verifyClaimFinding{{
		Kind:   "tests_green",
		Status: string(engine.StatusUnverifiable),
		Class:  string(engine.ClassEnvironment),
		Reason: "the toolchain cannot run in this environment (no module cache, no network)",
	}}
	repo := t.TempDir()

	state := &doneGateVerifyState{
		Status:          doneVerifyBlocked,
		Kind:            "environment",
		Reason:          "a claim could not be judged",
		Findings:        findings,
		ProfileInitHint: wantsProfileInitHint(findings, repo),
	}
	if !state.ProfileInitHint {
		t.Fatal("no hint although the repository has no profile and a test claim is an environment gap")
	}
	message := blockedMessageFor(state)
	if !strings.Contains(message, verify.ProfilePath) {
		t.Errorf("message %q does not name the profile file", message)
	}
	if !strings.Contains(message, "verify-claims --init") {
		t.Errorf("message %q does not name the init command", message)
	}

	// With the file in place the environment gap has another cause. Naming init
	// again would send the agent to a command that refuses to replace the file.
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, verify.ProfilePath),
		[]byte("languages:\n  - name: go\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	if wantsProfileInitHint(findings, repo) {
		t.Fatal("hint for a repository that has a profile")
	}
}

// The hint belongs to the test claims only: a refutation is a report fault, and a
// missing profile would not explain it.
func TestDoneVerifyProfileHintOnlyForATestEnvironmentGap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		finding verifyClaimFinding
		want    bool
	}{
		{"tests_exit environment", verifyClaimFinding{Kind: "tests_exit", Status: "UNVERIFIABLE", Class: "environment"}, true},
		{"tests_green refuted", verifyClaimFinding{Kind: "tests_green", Status: "REFUTED"}, false},
		{"tests_green confirmed", verifyClaimFinding{Kind: "tests_green", Status: "CONFIRMED"}, false},
		{"commit environment", verifyClaimFinding{Kind: "commit_exists", Status: "UNVERIFIABLE", Class: "environment"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsProfileInitHint([]verifyClaimFinding{tc.finding}, t.TempDir()); got != tc.want {
				t.Errorf("hint = %v, want %v", got, tc.want)
			}
		})
	}
}
