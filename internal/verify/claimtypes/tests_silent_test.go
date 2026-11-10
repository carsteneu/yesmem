package claimtypes

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// A run that exits non-zero without printing anything never reached the code.
// The CI sandbox failed to start and looked exactly like this, and reading it
// as a refutation claims the tests failed when nothing ran at all.
func TestTestsVerdict_SilentNonZeroExitIsAnEnvironmentGap(t *testing.T) {
	claim := engine.Claim{Kind: "tests_green", Command: "go test ./...", ClaimedExit: 0}
	outcome := &engine.RunOutcome{
		Code:        1,
		Output:      "",
		RawOutput:   "",
		CommandDesc: "go test ./...",
		Sandboxed:   true,
	}

	verdict := testsVerdict(claim, outcome, []string{"go", "test", "./..."})

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s), want %s", verdict.Status, verdict.Reason, engine.StatusUnverifiable)
	}
	if verdict.Class != engine.ClassEnvironment {
		t.Fatalf("class = %q, want %q", verdict.Class, engine.ClassEnvironment)
	}
}

// The blanket rule must not swallow a real failure: a run that says why it
// failed still refutes the green claim.
func TestTestsVerdict_NoisyNonZeroExitStillRefutes(t *testing.T) {
	claim := engine.Claim{Kind: "tests_green", Command: "go test ./...", ClaimedExit: 0}
	outcome := &engine.RunOutcome{
		Code:        1,
		Output:      "--- FAIL: TestThing (0.01s)\n    thing_test.go:12: boom\nFAIL\n",
		RawOutput:   "--- FAIL: TestThing (0.01s)\n    thing_test.go:12: boom\nFAIL\n",
		CommandDesc: "go test ./...",
		Sandboxed:   true,
	}

	verdict := testsVerdict(claim, outcome, []string{"go", "test", "./..."})

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want %s", verdict.Status, verdict.Reason, engine.StatusRefuted)
	}
}
