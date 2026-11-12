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

// The checkout a JavaScript run gets is a fresh clone, so the node_modules a
// suite needs is never in it, and the sandbox has no network to install one. The
// run stopped at its first import; that is a boundary of the machine, not a red
// suite, and the DONE gate must not send the agent back to its report over it.
func TestTestsVerdict_MissingNodeModulesIsAnEnvironmentGap(t *testing.T) {
	const output = `Error: Cannot find module 'typescript'
Require stack:
- /checkout/tests/model/minimum-fit-abc.test.js
✖ tests/model/minimum-fit-abc.test.js (106.8ms)
ℹ tests 1
ℹ pass 0
ℹ fail 1
`
	claim := engine.Claim{Kind: "tests_green", Command: "node --test tests/*/*.test.js", ClaimedExit: 0}
	outcome := &engine.RunOutcome{
		Code:        1,
		Output:      output,
		RawOutput:   output,
		CommandDesc: "node --test tests/*/*.test.js",
		Sandboxed:   true,
	}

	verdict := testsVerdict(claim, outcome, []string{"node", "--test", "tests/*/*.test.js"})

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
