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

// TestDoneVerify_3Refires_EscalatesToOrchestrator: after 3 refires without progress,
// agent is frozen + orchestrator notified.
func TestDoneVerify_3Refires_EscalatesToOrchestrator(t *testing.T) {
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

	// Refire 3 — should escalate
	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("stuck", yesloopDoneVerifyStateDeadAgentEscalation) {
		t.Error("after 3 refires without progress, agent should escalate to DEAD_AGENT")
	}

	agent, err := s.AgentGet("stuck")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "frozen" {
		t.Errorf("after escalation, agent status should be frozen, got %s", agent.Status)
	}
	if !strings.Contains(agent.Progress, "yesloop-done-verify") {
		t.Errorf("progress should mention yesloop-done-verify, got %s", agent.Progress)
	}
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

// buildDoneVerifyCompleteContent returns scratchpad content with all 6 phases
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

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy executed:** yes
**send_to orchestrator:** yes

BEWEISEN: alle 5 Phasen durch, Phase 6 finish mit commit.
`
}
