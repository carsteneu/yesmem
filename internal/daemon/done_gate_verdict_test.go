package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
)

// A section names its agent, so the operation derives the session and the plan
// thread itself. A caller that has to guess two opaque ids spends a block on the
// first attempt, and the verdict has to land under the key the gate looks up.
func TestVerifyClaimsDerivesTheAgentFromTheSection(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	// No test claim: the profile blocks this report without running a suite.
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "section-call", repo, report)

	response := h.handleVerifyClaims(map[string]any{
		"repo":    repo,
		"project": agent.Project,
		"section": verifyReportSection(agent.Section),
		"profile": verify.ProfileYesloopDone,
		"policy":  doneVerifyPolicyName,
		// A caller that guesses its ids must not be able to file the verdict under
		// a key the gate never reads: the section's agent owns them.
		"report_session": "ses_guessed",
		"plan_thread":    "thread_guessed",
	})
	if response.Error != "" {
		t.Fatalf("verify_claims: %s", response.Error)
	}
	key := h.verifyContextFor(agent, repo, report).key
	if got := h.verifyVerdictFor(key); got == nil {
		t.Fatalf("no verdict under the gate's key %s: the operation did not derive the agent's session", key)
	}
}

// The verdict key binds a judgement to one report about one tree on one branch.
// The branch is part of it because a report that names a pushed branch is judged
// against that branch: switching the checkout invalidates the judgement, exactly
// like a moved HEAD.
func TestVerifyFingerprintBindsTheTreeAndTheBranch(t *testing.T) {
	const report = "[COMMIT: abc]\n[BRANCH: main]\n"
	base := verifyFingerprint("/repo", report, "ses_agent", "thread", "abc123", "wt-clean", "plan-v1", "main")

	for _, tc := range []struct {
		name string
		key  string
		why  string
	}{
		{name: "another report", key: verifyFingerprint("/repo", report+"\n", "ses_agent", "thread", "abc123", "wt-clean", "plan-v1", "main"), why: "the report text"},
		{name: "another tree", key: verifyFingerprint("/repo", report, "ses_agent", "thread", "def456", "wt-clean", "plan-v1", "main"), why: "HEAD"},
		{name: "uncommitted residue", key: verifyFingerprint("/repo", report, "ses_agent", "thread", "abc123", "wt-dirty", "plan-v1", "main"), why: "the uncommitted residue beside the commit"},
		{name: "another branch", key: verifyFingerprint("/repo", report, "ses_agent", "thread", "abc123", "wt-clean", "plan-v1", "feature"), why: "the branch"},
		{name: "another plan revision", key: verifyFingerprint("/repo", report, "ses_agent", "thread", "abc123", "wt-clean", "plan-v2", "main"), why: "the plan"},
		{name: "another report session", key: verifyFingerprint("/repo", report, "ses_other", "thread", "abc123", "wt-clean", "plan-v1", "main"), why: "the report session"},
		{name: "another repository", key: verifyFingerprint("/other", report, "ses_agent", "thread", "abc123", "wt-clean", "plan-v1", "main"), why: "the repository"},
	} {
		if tc.key == base {
			t.Errorf("the key ignores %s: both sides hash to %s", tc.why, base)
		}
	}

	// A key is a digest, not a formatted string: a separator inside one part must
	// not make two different judgements look alike.
	a := verifyFingerprint("/repo", "x\x00y", "s1", "t1", "h1", "w1", "p1", "b1")
	b := verifyFingerprint("/repo", "x", "y", "t1", "h1", "w1", "p1", "b1")
	if a == b {
		t.Error("a NUL inside the report text shifted the parts into the same key")
	}
	if len(base) != 64 {
		t.Errorf("key length = %d, want the 64 hex characters of a sha256", len(base))
	}
}

// The registry is the shared memory between the pre-check and the DONE gate: one
// verdict per key, readable as a copy so no reader mutates the stored judgement.
func TestVerifyVerdictRegistryKeepsOneVerdictPerKey(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	if got := h.verifyVerdictFor("missing"); got != nil {
		t.Fatalf("verdict for an unknown key = %+v, want nil", got)
	}

	h.recordVerifyVerdict(&doneGateVerifyState{
		Fingerprint: "key-1",
		Status:      doneVerifyAccepted,
		Mode:        doneVerifyRequiredMode(),
	})
	stored := h.verifyVerdictFor("key-1")
	if stored == nil || stored.Status != doneVerifyAccepted {
		t.Fatalf("stored verdict = %+v, want accepted", stored)
	}

	// A reader that mutates its copy must not change the stored judgement.
	stored.Status = doneVerifyBlocked
	if again := h.verifyVerdictFor("key-1"); again.Status != doneVerifyAccepted {
		t.Errorf("the stored verdict changed through a reader's copy: %q", again.Status)
	}

	// A newer run for the same key replaces the older judgement: the key names one
	// report, and two judgements about it cannot both be current.
	h.recordVerifyVerdict(&doneGateVerifyState{Fingerprint: "key-1", Status: doneVerifyBlocked, Kind: "refuted"})
	if got := h.verifyVerdictFor("key-1"); got.Status != doneVerifyBlocked || got.Kind != "refuted" {
		t.Errorf("verdict = %+v, want the newer blocked judgement", got)
	}
}

// The registry is written once per verification and would grow for the life of
// the daemon. The oldest verdict is dropped once the store is full; the report it
// judged has to be checked again, which is the safe direction.
func TestVerifyVerdictRegistryDropsTheOldestWhenFull(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	for i := 0; i < maxStoredVerdicts+5; i++ {
		h.recordVerifyVerdict(&doneGateVerifyState{
			Fingerprint: fingerprintForTest(i),
			Status:      doneVerifyAccepted,
			// 0 means "unset" and would be replaced by the clock, so the oldest
			// entry starts at 1.
			RecordedAt: int64(i + 1),
		})
	}
	if got := len(doneGateVerdicts); got != maxStoredVerdicts {
		t.Fatalf("stored verdicts = %d, want the cap %d", got, maxStoredVerdicts)
	}
	if h.verifyVerdictFor(fingerprintForTest(0)) != nil {
		t.Error("the oldest verdict survived the cap, want it dropped")
	}
	last := maxStoredVerdicts + 4
	if h.verifyVerdictFor(fingerprintForTest(last)) == nil {
		t.Errorf("the newest verdict %d was dropped", last)
	}
}

func fingerprintForTest(i int) string {
	return fmt.Sprintf("key-%04d", i)
}

// A gate tick that lands while the pre-check is still running must hold the agent
// instead of blocking it: the verdict is on its way, and freezing it with "run the
// pre-check" would be false advice.
func TestVerifyRunInFlightTracksAKey(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	const key = "report-being-checked"
	h, _ := mustHandler(t)
	if h.verifyRunInFlight(key) {
		t.Fatal("a key that was never started reads as in flight")
	}
	beginVerifyRun(key)
	if !h.verifyRunInFlight(key) {
		t.Fatal("a started run does not read as in flight")
	}
	endVerifyRun(key)
	if h.verifyRunInFlight(key) {
		t.Fatal("a finished run still reads as in flight")
	}
}

// A stored verdict is only reusable when it was computed under the rule set the
// DONE gate applies. An empty profile judges fewer classes, so its acceptance must
// not release a DONE report.
// The printed command is copied into another process's shell. Every value is one
// quoted word, so a path with a space stays an argument and a metacharacter does
// not become a second command.
func TestVerifyClaimsCommandQuotesEveryValue(t *testing.T) {
	agent := storage.Agent{
		ID:        "quoted",
		Project:   "pro ject; rm -f x",
		Section:   "yesloop-quoted",
		SessionID: "sess-quoted",
		WorkDir:   "/tmp/repo with space",
	}
	command := verifyClaimsCommand(agent, agent.WorkDir)
	for _, want := range []string{
		"--repo '/tmp/repo with space'",
		"--project 'pro ject; rm -f x'",
		"--section 'yesloop-quoted-verify'",
	} {
		if !strings.Contains(command, want) {
			t.Errorf("command %q does not carry %q", command, want)
		}
	}
	if strings.Contains(command, "--project pro ject") {
		t.Errorf("command %q leaves the project unquoted", command)
	}
}

// A diff-scope claim is worth what its base is worth, and in the loop's rule set
// the party under judgement would choose it. The operation refuses a caller-chosen
// base there instead of accepting a judgement it cannot use.
func TestVerifyClaimsRefusesACallerChosenBaseInTheLoopRuleSet(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	response := h.handleVerifyClaims(map[string]any{
		"repo":    repo,
		"report":  "[COMMIT: " + head + "]\n",
		"profile": verify.ProfileYesloopDone,
		"policy":  doneVerifyPolicyName,
		"base":    head,
	})
	if response.Error == "" {
		t.Fatal("a caller-chosen base was accepted although the loop's rule set derives it")
	}
	if !strings.Contains(response.Error, "merge base") {
		t.Errorf("error %q does not name the derived base", response.Error)
	}
}

// The registry is written once per verification and would grow for the life of
// the daemon. The oldest verdict of the same repository goes first, so a busy
// project cannot push out the verdicts of another one.
func TestVerifyVerdictRegistryEvictsWithinTheSameRepository(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	h.recordVerifyVerdict(&doneGateVerifyState{Fingerprint: "other-repo", Status: doneVerifyAccepted, Repo: "/repo-other", RecordedAt: 1})
	for i := 0; i < maxStoredVerdicts; i++ {
		h.recordVerifyVerdict(&doneGateVerifyState{
			Fingerprint: fingerprintForTest(i),
			Status:      doneVerifyAccepted,
			Repo:        "/repo-busy",
			RecordedAt:  int64(i + 2),
		})
	}
	if got := h.verifyVerdictFor("other-repo"); got == nil {
		t.Fatal("the verdict of another repository was evicted")
	}
	if got := h.verifyVerdictFor(fingerprintForTest(0)); got != nil {
		t.Error("the oldest verdict of the busy repository survived the cap")
	}
}

// An in-flight mark that never comes back must not freeze a DONE claim forever:
// the verifier's own budget bounds a run, so an older mark belongs to a run that
// will never report.
func TestVerifyRunInFlightExpires(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	const key = "report-that-never-returns"
	beginVerifyRun(key)
	doneGateVerifyStatesMu.Lock()
	doneVerifyRunsInFlight[key] = time.Now().Add(-h.doneVerifyRunCeiling() - time.Second)
	doneGateVerifyStatesMu.Unlock()

	if h.verifyRunInFlight(key) {
		t.Fatal("an expired mark still reads as in flight")
	}
	if _, ok := doneVerifyRunsInFlight[key]; ok {
		t.Error("the expired mark was not dropped")
	}
}

// A report that states no claim was re-counted on every tick before the decision
// itself became the guard. The stored state is shared with the tick that reads it,
// so the copy has to carry its own slices.
func TestDoneVerifyStateForCopiesTheSlices(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, _ := mustHandler(t)
	h.replaceDoneGateVerifyState("copy", &doneGateVerifyState{
		Fingerprint: "key",
		Status:      doneVerifyBlocked,
		Findings:    []verifyClaimFinding{{Kind: "commit_exists"}},
		RoundNotes:  []string{"defect: first"},
	})
	copied := h.doneGateVerifyStateFor("copy")
	copied.Findings[0].Kind = "changed"
	copied.RoundNotes[0] = "changed"

	stored := h.doneGateVerifyStateFor("copy")
	if stored.Findings[0].Kind != "commit_exists" || stored.RoundNotes[0] != "defect: first" {
		t.Errorf("the stored state changed through a reader's copy: %+v", stored)
	}
}

// The stored verdict is bounded like the rest of the gate's memory: a finding's
// reason is report-controlled text, and the registry holds it for the life of the
// daemon.
func TestVerdictForCapsTheFindingReason(t *testing.T) {
	long := strings.Repeat("x", maxCommandChars*10)
	state := verdictFor(verifyContext{key: "k", repo: "/repo"}, doneVerifyRequiredMode(), verifyClaimsResult{
		Status: "blocked",
		Kind:   "refuted",
		Reason: "refuted",
		Findings: []verifyClaimFinding{
			{Kind: "diff_scope", Status: "refuted", Reason: long},
		},
	}, verify.Request{Budget: time.Minute}, time.Second)
	if state.Findings == nil {
		t.Fatal("the finding was dropped")
	}
	if got := len([]rune(state.Findings[0].Reason)); got > maxCommandChars+3 {
		t.Errorf("reason length = %d, want it capped near %d", got, maxCommandChars)
	}
}

// A run that never gets its turn judged nothing: the call records a limit verdict
// instead of leaving the gate with no verdict at all, so a DONE claim is blocked as
// "not the report" and an agent is not blamed for a busy machine.
func TestVerifyClaimsRecordsALimitWhenTheSlotNeverFrees(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: true, Budget: "5m", MaxWait: "10ms"})
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "slot-busy", repo, report)

	// The single slot is taken by another run.
	h.verifySlot <- struct{}{}
	defer func() { <-h.verifySlot }()

	response := h.handleVerifyClaims(map[string]any{
		"repo":    repo,
		"project": agent.Project,
		"section": verifyReportSection(agent.Section),
		"profile": verify.ProfileYesloopDone,
		"policy":  doneVerifyPolicyName,
	})
	if response.Error == "" {
		t.Fatal("a busy slot was reported as a completed run")
	}
	verdict := h.verifyVerdictFor(h.verifyContextFor(agent, repo, report).key)
	if verdict == nil || verdict.Status != doneVerifyBlocked || verdict.Kind != "limit" {
		t.Fatalf("verdict = %+v, want a blocked limit: the run never started", verdict)
	}
	if !strings.Contains(verdict.Reason, "the run did not start") {
		t.Errorf("reason = %q, want it to name the wait that expired", verdict.Reason)
	}
	if verdict.Bound != 10*time.Millisecond {
		t.Errorf("bound = %s, want the wait that expired (10ms), not the run budget", verdict.Bound)
	}
}

// The two configuration keys the gate has left both reach a reader: the budget is
// the default of a direct run, the wait is how long a call waits for the slot. A
// key nothing reads is a lie in the config file.
func TestVerifyClaimsUsesTheConfiguredBudgetAndWait(t *testing.T) {
	h, _ := mustHandler(t)
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: true, Budget: "7m", MaxWait: "3m"})

	request, _, err := h.verifyClaimsRequest(map[string]any{
		"repo":   t.TempDir(),
		"report": "[COMMIT: 0]\n",
	})
	if err != nil {
		t.Fatalf("verifyClaimsRequest: %v", err)
	}
	if request.Budget != 7*time.Minute {
		t.Errorf("budget = %s, want the configured 7m", request.Budget)
	}
	if got := h.doneVerifyMaxWait(); got != 3*time.Minute {
		t.Errorf("max wait = %s, want the configured 3m", got)
	}
}

func TestVerifyVerdictModeNamesProfileAndPolicy(t *testing.T) {
	if got := verifyVerdictMode(verify.ProfileYesloopDone, "yesloop-done"); got != doneVerifyRequiredMode() {
		t.Errorf("mode = %q, want the DONE gate's %q", got, doneVerifyRequiredMode())
	}
	if got := verifyVerdictMode("", "yesloop-done"); got == doneVerifyRequiredMode() {
		t.Errorf("an empty profile produced the gate's mode %q", got)
	}
	if got := verifyVerdictMode(verify.ProfileYesloopDone, ""); got == doneVerifyRequiredMode() {
		t.Errorf("an empty policy produced the gate's mode %q", got)
	}
}
