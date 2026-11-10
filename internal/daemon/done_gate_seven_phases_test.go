package daemon

import (
	"strings"
	"testing"
	"time"
)

// The seven-block contract: the daemon's verification is its own phase before
// the handoff. Phase 6 is DAEMON-VERIFY and has to name the accepted pre-check,
// Phase 7 is FINISH and keeps the handoff fields the old Phase 6 carried.
//
// These tests are the contract's witnesses: a six-block loop is not merely
// "unfinished", it is bounced with the name of the block it is missing.

// sevenBlockContent is the minimal content the seven-block contract accepts.
// phase6 and phase7 are parameters so a test can leave out exactly the block it
// is about.
func sevenBlockContent(phase6, phase7 string) string {
	return "### Phase 1: ANALYZE\n**Status:** COMPLETE\n**Goal understood:** g\n" +
		"**Task type:** feature\n**Codebase explored:** c\n**Session id:** ses_x\n\n" +
		"### Phase 2: PLAN\n**Status:** COMPLETE\n**Plan stored via set_plan:** yes\n**Files in scope:** f\n\n" +
		"### Phase 3: EXECUTE\n**Status:** COMPLETE\n\n" +
		"### Phase 4: VERIFY\n**Status:** COMPLETE\n**Tests run:** go test ./... -> exit 0\n" +
		"**Regression baseline:** base=abc failures=none; head failures=none; diff=none\n" +
		"**RED proof:** the test failed before the fix\n\n" +
		"### Phase 5: REVIEW\n**Status:** COMPLETE\nself-review: done\ncold-review: ses_cold\n" +
		"consequence: ses_cons\nsecurity: ses_sec\n**Stufe:** 2 - gate contract\n**Modules:** A, B\n\n" +
		phase6 + "\n" + phase7
}

// acceptedPhase6 is Phase 6 in the shape the contract asks for.
const acceptedPhase6 = "### Phase 6: DAEMON-VERIFY\n**Status:** COMPLETE\n" +
	"**Vorab-Prüfung:** yesmem verify-claims -> exit 0 (accepted)\n"

// handoffPhase7 is Phase 7 in the shape the old Phase 6 had.
const handoffPhase7 = "### Phase 7: FINISH\n**Status:** COMPLETE\n" +
	"**Deploy required:** no\n**send_to orchestrator:** yes\n"

// legacyPhase6 is what a loop that started under the six-block contract has as
// its last block: the handoff under the FINISH name.
const legacyPhase6 = "### Phase 6: FINISH\n**Status:** COMPLETE\n" +
	"**Deploy required:** no\n**send_to orchestrator:** yes\n"

func TestValidatePhaseBlocksAcceptsTheSevenBlockContract(t *testing.T) {
	result := ValidatePhaseBlocks(sevenBlockContent(acceptedPhase6, handoffPhase7))
	if !result.Compliant {
		t.Fatalf("a seven-block scratchpad was refused: %s", result.String())
	}
	if result.PhaseCount != 7 {
		t.Errorf("PhaseCount = %d, want 7", result.PhaseCount)
	}
}

// A loop that started before the change carries six blocks, and its sixth is the
// handoff. It must be told which block is missing, not left looking unfinished.
func TestValidatePhaseBlocksNamesTheMissingSeventhBlock(t *testing.T) {
	result := ValidatePhaseBlocks(sevenBlockContent(legacyPhase6, ""))
	if result.Compliant {
		t.Fatal("a six-block scratchpad passed the seven-block contract")
	}
	if len(result.MissingPhases) != 1 || result.MissingPhases[0] != 7 {
		t.Fatalf("MissingPhases = %v, want [7]", result.MissingPhases)
	}
	summary := summarizeErrors(result)
	if !strings.Contains(summary, "7 FINISH") {
		t.Errorf("the bounce message does not name the missing block: %q", summary)
	}
}

// Phase 6 is the verification, so it has to carry the accepted pre-check. A
// block that only says "COMPLETE" claims the phase without the fact it stands
// for.
func TestValidatePhaseBlocksRequiresTheAcceptedPreCheckInPhase6(t *testing.T) {
	without := "### Phase 6: DAEMON-VERIFY\n**Status:** COMPLETE\n**Vorab-Prüfung:** pending\n"
	result := ValidatePhaseBlocks(sevenBlockContent(without, handoffPhase7))
	if result.Compliant {
		t.Fatal("Phase 6 without an accepted pre-check passed")
	}
	if !hasFieldError(result, 6, "Vorab-Prüfung (accepted)") {
		t.Errorf("no Phase 6 field error for the accepted pre-check: %s", result.String())
	}
}

// The handoff fields move to Phase 7 with the phase itself.
func TestValidatePhaseBlocksKeepsTheHandoffInPhase7(t *testing.T) {
	without := "### Phase 7: FINISH\n**Status:** COMPLETE\n**Deploy required:** no\n"
	result := ValidatePhaseBlocks(sevenBlockContent(acceptedPhase6, without))
	if result.Compliant {
		t.Fatal("Phase 7 without send_to orchestrator passed")
	}
	if !hasFieldError(result, 7, "send_to orchestrator") {
		t.Errorf("no Phase 7 field error for the handoff: %s", result.String())
	}
}

func TestCountCompletedPhasesCountsTheSeventhPhase(t *testing.T) {
	if got := CountCompletedPhases(sevenBlockContent(acceptedPhase6, handoffPhase7)); got != 7 {
		t.Fatalf("CountCompletedPhases = %d, want 7", got)
	}
	// A sixth block marked done is not a finished loop.
	six := sevenBlockContent(legacyPhase6, "### Phase 7: FINISH\n**Status:** IN PROGRESS\n")
	if got := CountCompletedPhases(six); got != 6 {
		t.Fatalf("CountCompletedPhases = %d, want 6 while Phase 7 is in progress", got)
	}
}

// One number decides every count in the daemon. A validator that grows past it
// without the constant is the drift this test exists for.
func TestDonePhaseCountMatchesTheValidator(t *testing.T) {
	if got := len(phaseValidations); got != 7 {
		t.Fatalf("phaseValidations has %d entries, want 7 (Phase 6 DAEMON-VERIFY, Phase 7 FINISH)", got)
	}
	if phaseValidations[5].Number != 6 || phaseValidations[5].Name != "DAEMON-VERIFY" {
		t.Errorf("phase 6 = %d %q, want 6 DAEMON-VERIFY", phaseValidations[5].Number, phaseValidations[5].Name)
	}
	if phaseValidations[6].Number != 7 || phaseValidations[6].Name != "FINISH" {
		t.Errorf("phase 7 = %d %q, want 7 FINISH", phaseValidations[6].Number, phaseValidations[6].Name)
	}
}

// The marker form the idle relay asks for has to reach phase 7 as well, and it
// has to do so without an index panic.
func TestCountMarkedDonePhasesReachesTheSeventh(t *testing.T) {
	content := sevenBlockContent(acceptedPhase6, "### Phase 7: FINISH [x]\n**Status:** IN PROGRESS\n")
	if got := countMarkedDonePhases(content); got != 7 {
		t.Fatalf("countMarkedDonePhases = %d, want 7: the [x] on the Phase 7 header counts", got)
	}
}

// A DONE claim with a seventh block in progress is not in scope: the gate must
// not hold an agent that has not finished writing its own handoff.
func TestGateDoneVerifyRequiresTheCompletedSeventhPhase(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := endToEndAgent(t, h, s, "seven-in-progress", repo, "[COMMIT: "+strings.Repeat("0", 40)+"]\n")
	content := sevenBlockContent(acceptedPhase6, "### Phase 7: FINISH\n**Status:** IN PROGRESS\n")
	if h.gateDoneVerify(agent, content) {
		t.Fatal("gateDoneVerify = true, want false: a seventh block in progress is not a DONE")
	}
}

func hasFieldError(result ValidationResult, phase int, field string) bool {
	for _, fe := range result.FieldErrors {
		if fe.Phase == phase && fe.Field == field {
			return true
		}
	}
	return false
}

// A loop that started under the six-block contract has every block it wrote
// marked COMPLETE, so it has claimed the end of the pipeline: it must be told
// which block it is missing instead of looking unfinished forever.
func TestDoneGuardBouncesASixBlockLoopWithTheMissingPhase(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)

	content := sevenBlockContent(legacyPhase6, "")
	makeDoneGuardAgent(t, h, s, "bounce-six", "sess-bounce-six", content)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, "bounce-six")+".inject")

	h.checkYesloopDoneGuard()

	// The fake socket keeps only the first 64 bytes of a write (fakeInjectSocket
	// reads buf[:64]), so the wire assertion is the fact of the relay and the
	// message itself is checked at its producer: the same validator input the
	// guard fed the relay.
	requireMessages(t, rec, 1)
	relay := buildDoneGuardRelayMessage(ValidatePhaseBlocks(content))
	if !strings.Contains(relay, "7 FINISH") {
		t.Errorf("the bounce does not name the missing block: %q", relay)
	}
	if !strings.Contains(relay, "Phase6:Vorab-Prüfung (accepted)") {
		t.Errorf("the bounce does not name the sixth block's missing field: %q", relay)
	}
	if got := getDoneGuardRefireCount("bounce-six"); got != 0 {
		t.Errorf("refire count = %d, want 0: the bounce is the first relay", got)
	}
}

// A skeleton of headers is not a claim. The guard must stay quiet while the
// agent fills the blocks in, or it kills agents during legitimate work (#81984).
func TestDoneGuardStaysQuietOnASevenBlockSkeleton(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)

	skeleton := "### Phase 1: ANALYZE\n\n### Phase 2: PLAN\n\n### Phase 3: EXECUTE\n\n" +
		"### Phase 4: VERIFY\n\n### Phase 5: REVIEW\n\n### Phase 6: DAEMON-VERIFY\n\n### Phase 7: FINISH\n"
	makeDoneGuardAgent(t, h, s, "skeleton", "sess-skeleton", skeleton)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, "skeleton")+".inject")

	h.checkYesloopDoneGuard()

	time.Sleep(100 * time.Millisecond)
	if got := contentWrites(rec); len(got) != 0 {
		t.Errorf("a skeleton was relayed to: %q", got)
	}
	if hasDoneGuardState("skeleton", doneGuardStateRefiring) {
		t.Error("a skeleton was tracked as a failed DONE claim")
	}
}

// A blocked early phase is a legitimate stop the validation explicitly allows
// (every phase 1-5 accepts BLOCKED). The guard must leave it to the orchestrator
// instead of relaying 25 missing fields and pausing it as a dead agent.
func TestDoneGuardLeavesAnEarlyBlockedPhaseAlone(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)

	content := "### Phase 1: ANALYZE\n**Status:** BLOCKED — the entry point cannot be found\n"
	makeDoneGuardAgent(t, h, s, "early-blocked", "sess-early-blocked", content)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, "early-blocked")+".inject")

	h.checkYesloopDoneGuard()

	time.Sleep(100 * time.Millisecond)
	if got := contentWrites(rec); len(got) != 0 {
		t.Errorf("a blocked first phase was relayed to: %q", got)
	}
	if hasDoneGuardState("early-blocked", doneGuardStateRefiring) {
		t.Error("a blocked first phase was tracked as a failed DONE claim")
	}
}

// The accepted proof is a fact, not a word: a line that says it was not accepted
// carries the token and none of the fact.
func TestValidatePhaseBlocksRejectsANegatedAcceptedProof(t *testing.T) {
	negated := "### Phase 6: DAEMON-VERIFY\n**Status:** COMPLETE\n" +
		"**Vorab-Prüfung:** verify-claims -> exit 1 (refuted, not accepted)\n"
	result := ValidatePhaseBlocks(sevenBlockContent(negated, handoffPhase7))
	if result.Compliant {
		t.Fatal("a negated accepted proof passed as the phase's evidence")
	}
	if !hasFieldError(result, 6, "Vorab-Prüfung (accepted)") {
		t.Errorf("no Phase 6 field error for the negated proof: %s", result.String())
	}
}
