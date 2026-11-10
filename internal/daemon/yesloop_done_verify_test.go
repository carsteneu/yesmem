package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// resetDoneVerifyState clears the done-verify state map. Used in tests.
func resetDoneVerifyState() {
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents = make(map[string]*yesloopDoneVerifyState)
	yesloopDoneVerifyAgentsMu.Unlock()
}

// makeDoneVerifyAgent creates a yesloop agent with a given scratchpad content.
func makeDoneVerifyAgent(t *testing.T, h *Handler, s *storage.Store, id, sessionID, content string) {
	t.Helper()
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     sessionID,
		PID:           testPID,
		Status:        "running",
		SockPath:      "/nonexistent/" + id + ".sock",
		CallerSession: "caller-" + id,
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if content != "" {
		s.ScratchpadWrite("testproj", "yesloop-"+id, content, "")
	}
}

// hasDoneVerifyState checks that an agent has a specific done-verify state.
func hasDoneVerifyState(agentID string, expectedState int) bool {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	st, ok := yesloopDoneVerifyAgents[agentID]
	if !ok {
		return false
	}
	return st.state == expectedState
}

// getDoneVerifyRefireCount returns refireCount for an agent.
func getDoneVerifyRefireCount(agentID string) int {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	st, ok := yesloopDoneVerifyAgents[agentID]
	if !ok {
		return -1
	}
	return st.refireCount
}

// --- Tests ---

// TestDoneVerify_NoClaim_NoFire: agent without DONE-claim must not be tracked.
func TestDoneVerify_NoClaim_NoFire(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	makeDoneVerifyAgent(t, h, s, "no-claim", "sess-noclaim",
		"### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking...\n")

	h.checkYesloopDoneVerify()

	if hasDoneVerifyState("no-claim", yesloopDoneVerifyStateNotDone) {
		t.Error("agent without DONE-claim should remain untracked (default NOT_DONE state)")
	}
	yesloopDoneVerifyAgentsMu.Lock()
	_, tracked := yesloopDoneVerifyAgents["no-claim"]
	yesloopDoneVerifyAgentsMu.Unlock()
	if tracked {
		t.Error("agent without DONE-claim should not be tracked in state map")
	}
}

// TestDoneVerify_DoneClaim_TriggersVerify: DONE-claim triggers VERIFY_REQUESTED + relay.
func TestDoneVerify_DoneClaim_TriggersVerify(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	makeDoneVerifyAgent(t, h, s, "done-claim", "sess-done",
		"### Phase 6: FINISH\n**Status:** COMPLETE\n**Deploy executed:** yes\n**send_to orchestrator:** yes\n")

	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("done-claim", yesloopDoneVerifyStateVerifyRequested) {
		t.Error("agent with DONE-claim should transition to VERIFY_REQUESTED")
	}
}

// TestDoneVerify_BewiesenMarker_PlusDoneSendTo_TransitionsToVerified:
// BEWEISEN + send_to orchestrator + 6 phases COMPLETE → DONE_VERIFIED.
func TestDoneVerify_BewiesenMarker_PlusDoneSendTo_TransitionsToVerified(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	content := buildDoneVerifyCompleteContent()
	makeDoneVerifyAgent(t, h, s, "verified", "sess-vfy", content)

	// Force into VERIFY_REQUESTED, then re-check
	h.checkYesloopDoneVerify()
	if !hasDoneVerifyState("verified", yesloopDoneVerifyStateVerifyRequested) {
		t.Fatal("agent should transition to VERIFY_REQUESTED first")
	}

	// Second check should find BEWEISEN + send_to + 6 phases → DONE_VERIFIED
	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("verified", yesloopDoneVerifyStateDoneVerified) {
		t.Error("agent with BEWEISEN + send_to + 6 phases should transition to DONE_VERIFIED")
	}
}

// TestDoneVerify_3Refires_NudgesTheOrchestrator: after 3 refires without progress
// the layer reports once and lets the agent run. The seven-phase DONE gate is
// what actually decides a DONE, so this layer must not hold the row.
func TestDoneVerify_3Refires_NudgesTheOrchestrator(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	makeDoneVerifyAgent(t, h, s, "stuck", "sess-stuck",
		"### Phase 6: FINISH\n**Status:** COMPLETE\n**Deploy executed:** yes\n**send_to orchestrator:** yes\n")

	// Initial transition to VERIFY_REQUESTED
	h.checkYesloopDoneVerify()
	if !hasDoneVerifyState("stuck", yesloopDoneVerifyStateVerifyRequested) {
		t.Fatal("agent should transition to VERIFY_REQUESTED first")
	}

	// Force refire interval to be elapsed for each re-fire
	forceRefireReady("stuck")

	// Refire 1
	h.checkYesloopDoneVerify()
	if getDoneVerifyRefireCount("stuck") != 1 {
		t.Fatalf("refire 1 expected, got %d", getDoneVerifyRefireCount("stuck"))
	}
	forceRefireReady("stuck")

	// Refire 2
	h.checkYesloopDoneVerify()
	if getDoneVerifyRefireCount("stuck") != 2 {
		t.Fatalf("refire 2 expected, got %d", getDoneVerifyRefireCount("stuck"))
	}
	forceRefireReady("stuck")

	// Refire 3 — should nudge
	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("stuck", yesloopDoneVerifyStateNudged) {
		t.Error("after 3 refires without progress, the layer should reach its nudge state")
	}

	// The layer reports once and lets the agent run: the seven-phase DONE gate
	// decides a DONE, not this ladder.
	assertGuardNudged(t, s, "stuck", "yesloop-done-verify")
}

// TestDoneVerify_NonYesloopSection_NoFire: agents without yesloop- prefix are ignored.
func TestDoneVerify_NonYesloopSection_NoFire(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	agent := storage.Agent{
		ID:        "regular-agent",
		Project:   "testproj",
		Section:   "general-task",
		SessionID: "sess-reg",
		PID:       testPID,
		Status:    "running",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	s.ScratchpadWrite("testproj", "general-task",
		"### Phase 6: FINISH\n**Status:** COMPLETE\n**send_to orchestrator:** yes\n", "")

	h.checkYesloopDoneVerify()

	yesloopDoneVerifyAgentsMu.Lock()
	_, tracked := yesloopDoneVerifyAgents["regular-agent"]
	yesloopDoneVerifyAgentsMu.Unlock()
	if tracked {
		t.Error("non-yesloop agent should not be tracked by done-verify")
	}
}

// TestDoneVerify_DoneVerifiedState_NoMoreRelays: once DONE_VERIFIED is reached,
// no further relays fire.
func TestDoneVerify_DoneVerifiedState_NoMoreRelays(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	content := buildDoneVerifyCompleteContent()
	makeDoneVerifyAgent(t, h, s, "done", "sess-done2", content)

	// Drive to VERIFY_REQUESTED then DONE_VERIFIED
	h.checkYesloopDoneVerify()
	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("done", yesloopDoneVerifyStateDoneVerified) {
		t.Fatal("agent should be in DONE_VERIFIED state")
	}

	// Multiple subsequent checks should not change state, not refire
	h.checkYesloopDoneVerify()
	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("done", yesloopDoneVerifyStateDoneVerified) {
		t.Error("DONE_VERIFIED state should be terminal — no further transitions")
	}
}

// TestDoneVerify_State1_to_State2_NeedsBeweisenAndSendTo: only BEWEISEN without
// send_to evidence is not enough for DONE_VERIFIED transition.
func TestDoneVerify_State1_to_State2_NeedsBeweisenAndSendTo(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	// 6 phases COMPLETE + BEWEISEN but NO send_to orchestrator line
	content := buildDoneVerifyCompleteContent()
	content = strings.Replace(content, "**send_to orchestrator:** yes", "", 1)
	content += "\nBEWEISEN\n"
	makeDoneVerifyAgent(t, h, s, "partial", "sess-part", content)

	h.checkYesloopDoneVerify()
	if !hasDoneVerifyState("partial", yesloopDoneVerifyStateVerifyRequested) {
		t.Fatal("agent should transition to VERIFY_REQUESTED first")
	}

	h.checkYesloopDoneVerify()

	if hasDoneVerifyState("partial", yesloopDoneVerifyStateDoneVerified) {
		t.Error("agent without send_to orchestrator line should NOT transition to DONE_VERIFIED")
	}
	if !hasDoneVerifyState("partial", yesloopDoneVerifyStateVerifyRequested) {
		t.Error("agent should remain in VERIFY_REQUESTED state")
	}
}

// --- Helpers ---

// TestDoneVerify_RelayMessage_MentionsSecurityReview: the verify relay message
// must instruct the agent to run security-review and include the **Security:**
// field. Guards against regression when the message is edited.
func TestDoneVerify_RelayMessage_MentionsSecurityReview(t *testing.T) {
	for _, needle := range []string{
		"security-review",
		"Phase 5",
		"Security",
		"BEWEISEN",
		"send_to",
	} {
		if !strings.Contains(doneVerifyRelayMessage, needle) {
			t.Errorf("doneVerifyRelayMessage missing %q\nfull message: %s", needle, doneVerifyRelayMessage)
		}
	}
	// Must be metachar-free — no markdown, no backticks, no parens, no brackets.
	for _, bad := range []string{"`", "(", ")", "[", "]", "{", "}"} {
		if strings.Contains(doneVerifyRelayMessage, bad) {
			t.Errorf("doneVerifyRelayMessage must be metachar-free but contains %q\nfull message: %s", bad, doneVerifyRelayMessage)
		}
	}
}

// The done-verify notice is a nudge: it names the finding and asks nothing. There
// is no hold to lift, so the relay hint would point the orchestrator at an agent
// that was never stopped (Learning #81175).
func TestDoneVerify_NudgePayload_AsksNothingOfTheOrchestrator(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	makeDoneVerifyAgent(t, h, s, "hint-dv", "sess-hintdv",
		"### Phase 6: FINISH\n**Status:** COMPLETE\n**Deploy executed:** yes\n**send_to orchestrator:** yes\n")

	// Drive through VERIFY_REQUESTED + 3 refires to escalation.
	h.checkYesloopDoneVerify()
	forceRefireReady("hint-dv")
	h.checkYesloopDoneVerify() // refire 1
	forceRefireReady("hint-dv")
	h.checkYesloopDoneVerify() // refire 2
	forceRefireReady("hint-dv")
	h.checkYesloopDoneVerify() // refire 3 → escalation

	if !hasDoneVerifyState("hint-dv", yesloopDoneVerifyStateNudged) {
		t.Fatal("expected the terminal nudge state")
	}

	assertGuardNudged(t, s, "hint-dv", "yesloop-done-verify")
}

// --- L6: hasDoneClaim DONE-claim scope (Phase 6 block only) ---

// TestHasDoneClaim_TextInPhase1_NotTriggered: text-based indicators
// (send_to DONE, ^DONE:) appearing in Phase 1 (e.g. briefing/template text)
// must NOT trigger hasDoneClaim. This is the L6 false-positive fix (#82125).
func TestHasDoneClaim_TextInPhase1_NotTriggered(t *testing.T) {
	// Phase 1 mentions "send_to ... DONE" as part of the briefing — common
	// pattern when the orchestrator instructs the agent to send_to on DONE.
	content := "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\n" +
		"When you are DONE, send send_to(target=\"caller\", content=\"DONE: task complete\")\n" +
		"Goal understood: do the thing\n"
	if hasDoneClaim(content) {
		t.Errorf("send_to DONE in Phase 1 briefing must NOT trigger hasDoneClaim (L6 false-positive)")
	}

	// ^DONE: as an acknowledgement in Phase 1
	content2 := "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\n" +
		"DONE: reading briefing, proceeding to analyze\n"
	if hasDoneClaim(content2) {
		t.Errorf("^DONE: in Phase 1 must NOT trigger hasDoneClaim (L6 false-positive)")
	}
}

// TestHasDoneClaim_TextInPhase6_Triggered: the same text-based indicators
// appearing in the Phase 6 block MUST trigger hasDoneClaim — that's where
// real DONE claims live.
func TestHasDoneClaim_TextInPhase6_Triggered(t *testing.T) {
	content := "### Phase 1: ANALYZE\n**Status:** COMPLETE\n**Session id:** ses_x\n" +
		"### Phase 2: PLAN\n**Status:** COMPLETE\n**Plan stored via set_plan:** yes\n**Files in scope:** x\n" +
		"### Phase 3: EXECUTE\n**Status:** COMPLETE\n" +
		"### Phase 4: VERIFY\n**Status:** COMPLETE\n**Build:** ok\n" +
		"### Phase 5: REVIEW\n**Status:** COMPLETE\n**Stage 2: Cold Review\ntask() dispatched: yes\n**Security:** none\n" +
		"### Phase 6: FINISH\n**Status:** COMPLETE\nsend_to(target=\"caller\", content=\"DONE: finished\")\n"
	if !hasDoneClaim(content) {
		t.Errorf("send_to DONE in Phase 6 block SHOULD trigger hasDoneClaim")
	}

	content2 := strings.Replace(content,
		"send_to(target=\"caller\", content=\"DONE: finished\")", "DONE: finished all phases", 1)
	if !hasDoneClaim(content2) {
		t.Errorf("^DONE: in Phase 6 block SHOULD trigger hasDoneClaim")
	}
}

// TestHasDoneClaim_StructuralIndicators_StillGlobal: the structural
// indicators (Phase 6 header, Phase 6/6 marker) are checked against full
// content and trigger regardless of where they appear — they are
// structural anchors that don't false-positive on body text.
func TestHasDoneClaim_StructuralIndicators_StillGlobal(t *testing.T) {
	if !hasDoneClaim("### Phase 6: FINISH\n**Status:** COMPLETE\n") {
		t.Errorf("Phase 6 header (structural indicator) should trigger hasDoneClaim")
	}
	if !hasDoneClaim("Phase 6/6 complete — exiting") {
		t.Errorf("Phase 6/6 marker (structural indicator) should trigger hasDoneClaim")
	}
	if hasDoneClaim("### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nnothing here") {
		t.Errorf("no indicators present should NOT trigger hasDoneClaim")
	}
}

// TestHasDoneClaim_Phase5Text_NotTriggered: text-based indicators in Phase 5
// (aspirational mentions like "send_to DONE once review complete") do NOT
// trigger — only Phase 6 block is the source of truth for DONE claims.
// Per L6 plan decision: Phase 6-only scope.
func TestHasDoneClaim_Phase5Text_NotTriggered(t *testing.T) {
	content := "### Phase 1: ANALYZE\n**Status:** COMPLETE\n**Session id:** ses_x\n" +
		"### Phase 2: PLAN\n**Status:** COMPLETE\n**Plan stored via set_plan:** yes\n**Files in scope:** x\n" +
		"### Phase 3: EXECUTE\n**Status:** COMPLETE\n" +
		"### Phase 4: VERIFY\n**Status:** COMPLETE\n**Build:** ok\n" +
		"### Phase 5: REVIEW\n**Status:** COMPLETE\n**Stage 2: Cold Review\ntask() dispatched: yes\n**Security:** none\n" +
		"next step: send_to orchestrator DONE once review is complete\n" // Phase 5 mention
	if hasDoneClaim(content) {
		t.Errorf("send_to DONE in Phase 5 must NOT trigger hasDoneClaim (Phase 6-only scope)")
	}
}

// forceRefireReady backdates lastRelayAt so the next check triggers a re-fire.
func forceRefireReady(agentID string) {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	st, ok := yesloopDoneVerifyAgents[agentID]
	if !ok {
		return
	}
	st.lastRelayAt = time.Now().Add(-(yesloopDoneVerifyRefireInterval + time.Second))
}

// buildDoneVerifyCompleteContent returns scratchpad content with all 7 phases
// marked COMPLETE, send_to orchestrator evidence, and the BEWEISEN marker.
func buildDoneVerifyCompleteContent() string {
	return `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Test goal
**Codebase explored:** internal/

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** test.go

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** go test -> exit 0

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 2: Cold Review
task() dispatched: yes
**Security:** none

### Phase 6: DAEMON-VERIFY
**Status:** COMPLETE
**Vorab-Prüfung:** verify-claims -> exit 0 (accepted)

### Phase 7: FINISH
**Status:** COMPLETE
**Deploy executed:** yes
**send_to orchestrator:** yes

BEWEISEN: alle 7 Phasen durch, Phase 7 finish mit commit.
`
}

// A verdict belongs to the row it was reached for: a stale DONE_VERIFIED would
// make both guard layers go silent for an agent that never proved anything
// (review round 3). Since YM-151 the allocator no longer reuses ids; this test
// simulates the remaining case where a row lands on an id the map still carries.
func TestDoneVerify_VerdictDoesNotSurviveARecycledAgentID(t *testing.T) {
	resetDoneVerifyState()
	resetIdleState()
	h, s := mustHandler(t)
	makeYesloopAgent(t, h, s, "recycled-1", "sess-recycled-dv", testPID, false, 11*time.Minute)

	row, err := s.AgentGet("recycled-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["recycled-1"] = &yesloopDoneVerifyState{
		state:     yesloopDoneVerifyStateDoneVerified,
		createdAt: row.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	if !doneVerifySettled(*row) {
		t.Fatal("the verdict of this row must apply to this row")
	}

	// The row is replaced by a new agent under the same id.
	if _, err := s.DB().Exec("UPDATE agents SET created_at = ? WHERE id = ?",
		time.Now().Add(-time.Hour).Format(time.RFC3339), "recycled-1"); err != nil {
		t.Fatalf("recycle the row: %v", err)
	}
	newRow, err := s.AgentGet("recycled-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if doneVerifySettled(*newRow) {
		t.Error("a new agent inherited the verdict of the row that used its id before")
	}
}

// A nudge is not a verdict: it says "claimed DONE without proof", and an agent
// that then goes quiet is exactly what the idle ladder is for. Only a verified
// completion may silence it.
func TestDoneVerify_NudgeDoesNotSettleTheIdleLayer(t *testing.T) {
	resetDoneVerifyState()
	resetIdleState()
	h, s := mustHandler(t)
	makeYesloopAgent(t, h, s, "nudged-1", "sess-nudged-dv", testPID, false, 11*time.Minute)

	row, err := s.AgentGet("nudged-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["nudged-1"] = &yesloopDoneVerifyState{
		state:     yesloopDoneVerifyStateNudged,
		createdAt: row.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	if doneVerifySettled(*row) {
		t.Error("a nudge must not silence the idle guard: the agent claimed DONE without proof and still needs supervision")
	}
}

// doneVerifyTracked reports whether the done-verify map still holds an entry for
// the agent id, regardless of state.
func doneVerifyTracked(agentID string) bool {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	_, ok := yesloopDoneVerifyAgents[agentID]
	return ok
}

// TestDoneVerify_PrunesStateOfGoneAgent (YM-203): the state map is in-memory and
// was never cleaned, so every agent that ever emitted a DONE-claim kept an entry
// until the daemon restarted. A row that no longer exists must lose its entry;
// a live row must keep it (doneVerifySettled feeds the idle guard).
func TestDoneVerify_PrunesStateOfGoneAgent(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)

	claim := "### Phase 6: FINISH\n**Status:** COMPLETE\n**Deploy executed:** yes\nsend_to orchestrator: yes\n"
	makeDoneVerifyAgent(t, h, s, "gone-1", "sess-gone1", claim)
	makeDoneVerifyAgent(t, h, s, "live-1", "sess-live1", claim)

	h.checkYesloopDoneVerify()
	if !doneVerifyTracked("gone-1") || !doneVerifyTracked("live-1") {
		t.Fatalf("both DONE-claiming agents must be tracked after the first sweep")
	}

	if err := s.AgentDelete("gone-1"); err != nil {
		t.Fatalf("AgentDelete: %v", err)
	}
	h.checkYesloopDoneVerify()

	if doneVerifyTracked("gone-1") {
		t.Error("state of an agent whose row is gone must be pruned")
	}
	if !doneVerifyTracked("live-1") {
		t.Error("state of a live agent must survive the prune")
	}
}
