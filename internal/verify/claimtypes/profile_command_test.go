package claimtypes

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// A project whose runner is not a built-in still has a word about its own suite.
// The profile names the command, so the test claim may run exactly that command;
// the same command without the profile entry stays refused, which is what keeps
// the widening a binding and not a hole.
func TestCheckTestsAdmitsTheProjectsTestCommand(t *testing.T) {
	repo, commit := makeClaimRepo(t, "profiletestcmd", map[string]string{
		"run.sh": "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
	})
	claim := engine.Claim{
		Kind:        "tests_green",
		Command:     "./run.sh",
		Commit:      commit,
		ClaimedExit: 0,
		HasExit:     true,
	}

	withProfile := unsandboxedCtx(t, repo)
	withProfile.ProfileTestCommands = []string{"./run.sh"}
	verdict := CheckTests(claim, withProfile)
	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s), want confirmed: the profile names this command",
			verdict.Status, verdict.Reason)
	}

	withoutProfile := unsandboxedCtx(t, repo)
	refused := CheckTests(claim, withoutProfile)
	if refused.Status != engine.StatusUnverifiable || !strings.Contains(refused.Reason, "allowlist") {
		t.Fatalf("status = %s (%s), want the allowlist refusal without a profile entry",
			refused.Status, refused.Reason)
	}
}

// The coverage command is a profile entry of its own. A profile that names one
// outside the built-ins must be able to measure, and the test claim's list must
// not stand in for it: the two families are separate bindings.
func TestCheckCoverageDeltaAdmitsTheProjectsCoverageCommand(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "profilecovcmd",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'impl.toy:1.1,1.5 1 1\\n' > .cover.out\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	ctx := unsandboxedCtx(t, repo)
	ctx.Base = base
	ctx.Languages = []engine.Language{toyCoverage}
	ctx.ProfileCoverageCommands = []string{"./cov.sh"}

	verdict := CheckCoverageDelta(coverageClaim(head), ctx)
	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s), want confirmed: the profile names this coverage command",
			verdict.Status, verdict.Reason)
	}

	// The test family's list must not open the coverage gate.
	ctx.ProfileCoverageCommands = nil
	ctx.ProfileTestCommands = []string{"./cov.sh"}
	refused := CheckCoverageDelta(coverageClaim(head), ctx)
	if refused.Status != engine.StatusUnverifiable || !strings.Contains(refused.Reason, "allowlist") {
		t.Fatalf("status = %s (%s), want the allowlist refusal for a test entry on a coverage claim",
			refused.Status, refused.Reason)
	}
}
