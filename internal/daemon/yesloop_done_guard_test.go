package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Test helpers (mirror yesloop_done_verify_test.go) ---

// makeDoneGuardAgent creates a yesloop agent with a given scratchpad content.
func makeDoneGuardAgent(t *testing.T, h *Handler, s *storage.Store, id, sessionID, content string) {
	t.Helper()
	// The fixture's PID is this test process. C3 gates the paused-agent path on
	// isAgentProcess, so the fixture declares itself an opencode process and
	// reports the matching /proc cmdline (a test binary has no opencode cmdline
	// of its own).
	stubProcCmdline(t, agentProcCmdlineStub)
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     sessionID,
		PID:           testPID,
		Status:        "running",
		Backend:       "opencode",
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

// hasDoneGuardState checks that an agent has a specific done-guard state.
func hasDoneGuardState(agentID string, expectedState int) bool {
	yesloopDoneGuardAgentsMu.Lock()
	defer yesloopDoneGuardAgentsMu.Unlock()
	st, ok := yesloopDoneGuardAgents[agentID]
	if !ok {
		return false
	}
	return st.state == expectedState
}

// getDoneGuardRefireCount returns refireCount for an agent. Returns -1 if untracked.
func getDoneGuardRefireCount(agentID string) int {
	yesloopDoneGuardAgentsMu.Lock()
	defer yesloopDoneGuardAgentsMu.Unlock()
	st, ok := yesloopDoneGuardAgents[agentID]
	if !ok {
		return -1
	}
	return st.refireCount
}

// setDoneGuardLastRelayAt sets lastRelayAt to the given time. Used in tests
// to advance time past the refire interval without waiting 90 seconds.
func setDoneGuardLastRelayAt(agentID string, t time.Time) {
	yesloopDoneGuardAgentsMu.Lock()
	defer yesloopDoneGuardAgentsMu.Unlock()
	st, ok := yesloopDoneGuardAgents[agentID]
	if !ok {
		return
	}
	st.lastRelayAt = t
}

// allPhasesPhase4Invalid is a scratchpad with all 6 phase headers but Phase 4
// contains only **Status:** — none of Tests run / Build / Verification.
// Used to drive the DoneGuard refire state machine through its full arc.
const allPhasesPhase4Invalid = `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** test fixture
**Session id:** ses_test
**Codebase explored:** test

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** test

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 2: Cold Review
**task() dispatched:** yes
**Security:** none

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy executed:** yes
**send_to orchestrator:** yes
`

// --- Tests ---

// TestDoneGuard_FirstFailure_NoPause: first validation failure must NOT pause,
// only relay and track state. Previously the guard paused on the first tick.
func TestDoneGuard_FirstFailure_NoPause(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)

	makeDoneGuardAgent(t, h, s, "dg-1", "sess-dg-1", allPhasesPhase4Invalid)

	h.checkYesloopDoneGuard()

	agent, _ := s.AgentGet("dg-1")
	if agent == nil {
		t.Fatalf("AgentGet: agent not found")
	}
	if agent.Status != "running" {
		t.Errorf("after first DoneGuard check, agent should still be running, got status=%q (error=%q)",
			agent.Status, agent.Error)
	}
	if !hasDoneGuardState("dg-1", doneGuardStateRefiring) {
		t.Errorf("agent should be tracked in refiring state after first check")
	}
}

// TestDoneGuard_ImmediateRecheck_NoRefire: an immediate re-check must not
// increment refireCount (interval-gated).
func TestDoneGuard_ImmediateRecheck_NoRefire(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)

	makeDoneGuardAgent(t, h, s, "dg-2", "sess-dg-2", allPhasesPhase4Invalid)
	h.checkYesloopDoneGuard()

	if rc := getDoneGuardRefireCount("dg-2"); rc != 0 {
		t.Errorf("after first check, refireCount should be 0 (initial relay), got %d", rc)
	}

	// Immediate re-check: should be interval-gated, no increment.
	h.checkYesloopDoneGuard()
	if rc := getDoneGuardRefireCount("dg-2"); rc != 0 {
		t.Errorf("immediate second check should not increment refireCount (interval-gated), got %d", rc)
	}
}

// TestDoneGuard_Pauses_After_Max_Refires: after 3 total relay attempts
// (initial + 2 refires), the next interval-elapsed check pauses the agent.
func TestDoneGuard_Pauses_After_Max_Refires(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)

	makeDoneGuardAgent(t, h, s, "dg-3", "sess-dg-3", allPhasesPhase4Invalid)

	// Attempt 1: initial relay (refireCount=0).
	h.checkYesloopDoneGuard()
	if rc := getDoneGuardRefireCount("dg-3"); rc != 0 {
		t.Fatalf("after first check, refireCount should be 0, got %d", rc)
	}

	// Attempt 2: interval elapsed → refire 1.
	setDoneGuardLastRelayAt("dg-3", time.Now().Add(-2*doneGuardRefireInterval))
	h.checkYesloopDoneGuard()
	if rc := getDoneGuardRefireCount("dg-3"); rc != 1 {
		t.Fatalf("after second check, refireCount should be 1, got %d", rc)
	}
	agent, _ := s.AgentGet("dg-3")
	if agent.Status != "running" {
		t.Errorf("after 1 refire, agent should still be running, got status=%q", agent.Status)
	}

	// Attempt 3: interval elapsed → refire 2 (= max, still running).
	setDoneGuardLastRelayAt("dg-3", time.Now().Add(-2*doneGuardRefireInterval))
	h.checkYesloopDoneGuard()
	if rc := getDoneGuardRefireCount("dg-3"); rc != 2 {
		t.Fatalf("after third check, refireCount should be 2, got %d", rc)
	}
	agent, _ = s.AgentGet("dg-3")
	if agent.Status != "running" {
		t.Errorf("after 2 refires (3 total attempts), agent should still be running, got status=%q", agent.Status)
	}

	// Attempt 4: interval elapsed → refireCount=3 > maxRefires=2 → pause.
	setDoneGuardLastRelayAt("dg-3", time.Now().Add(-2*doneGuardRefireInterval))
	h.checkYesloopDoneGuard()

	agent, _ = s.AgentGet("dg-3")
	if agent.Status != "paused" {
		t.Errorf("after refireCount exceeds max, agent should be paused, got status=%q", agent.Status)
	}
	// pauseAgent writes the reason into the progress field (not error).
	if !strings.Contains(agent.Progress, "DONE-GUARD") {
		t.Errorf("paused agent progress should mention DONE-GUARD, got %q", agent.Progress)
	}

	// L5/I1: DEAD_AGENT message to orchestrator must include the pause hint.
	msgs, err := s.GetChannelMessages([]string{"caller-dg-3"})
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	var foundHint bool
	for _, m := range msgs {
		if strings.Contains(m.Content, orchestratorPauseHint) {
			foundHint = true
			break
		}
	}
	if !foundHint {
		t.Errorf("DEAD_AGENT message for DoneGuard escalation must include %q", orchestratorPauseHint)
	}
}

// TestDoneGuard_Compliance_Resets_State: when agent fixes the format issue,
// state is cleared from the map.
func TestDoneGuard_Compliance_Resets_State(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// This test covers the format guard's state reset. The verification stage
	// that follows a compliant DONE claim has its own test
	// (done_gate_verify_test.go), and leaving it on here would queue a run whose
	// goroutine outlives the test.
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})

	makeDoneGuardAgent(t, h, s, "dg-4", "sess-dg-4", allPhasesPhase4Invalid)
	h.checkYesloopDoneGuard()

	if !hasDoneGuardState("dg-4", doneGuardStateRefiring) {
		t.Fatalf("expected refiring state after first check")
	}

	// Agent fixes Phase 4 → content now compliant.
	s.ScratchpadWrite("testproj", "yesloop-dg-4", validV3Content, "")

	h.checkYesloopDoneGuard()

	yesloopDoneGuardAgentsMu.Lock()
	_, tracked := yesloopDoneGuardAgents["dg-4"]
	yesloopDoneGuardAgentsMu.Unlock()
	if tracked {
		t.Errorf("compliant agent should have its state cleared from the map")
	}
}

// TestBuildDoneGuardRelayMessage_IncludesFieldName: relay message includes
// the concrete missing-field name from summarizeErrors so the agent knows
// exactly what to fix.
func TestBuildDoneGuardRelayMessage_IncludesFieldName(t *testing.T) {
	result := ValidatePhaseBlocks(allPhasesPhase4Invalid)
	if result.Compliant {
		t.Fatalf("test precondition: allPhasesPhase4Invalid should fail validation")
	}
	msg := buildDoneGuardRelayMessage(result)
	if !strings.Contains(msg, "Phase4") {
		t.Errorf("relay message should include Phase4 marker (from summarizeErrors), got %q", msg)
	}
	if !strings.Contains(msg, "DONE-GUARD") {
		t.Errorf("relay message should mention DONE-GUARD so the agent can identify the source, got %q", msg)
	}
}

// TestDoneGuard_Revalidates_Paused_Agent_When_Compliant: when an agent was paused
// by DONE-GUARD but later its scratchpad becomes compliant, the guard must unpause it.
// Safety gates: status=paused, progress prefix "paused: DONE-GUARD:", result.Compliant=true.
func TestDoneGuard_Revalidates_Paused_Agent_When_Compliant(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// The format guard's recovery path, without the verification stage that a
	// compliant DONE claim now enters (done_gate_verify_test.go owns that).
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})

	makeDoneGuardAgent(t, h, s, "dg-recv-1", "sess-dg-recv-1", validV3Content)
	s.AgentUpdate("dg-recv-1", map[string]any{
		"status":   "paused",
		"progress": "paused: DONE-GUARD: phase validation failed — Phase4 missing required field",
	})

	h.checkYesloopDoneGuard()

	agent, _ := s.AgentGet("dg-recv-1")
	if agent.Status != "running" {
		t.Errorf("paused+DONE-GUARD agent with compliant scratchpad should be unpaused, got status=%q", agent.Status)
	}
	if !strings.Contains(agent.Progress, "recovered by DONE-GUARD") {
		t.Errorf("agent progress should indicate recovery, got %q", agent.Progress)
	}
}

// TestDoneGuard_DoesNot_Revalidate_Idle_Paused_Agent: agents paused by other guards
// (Idle, DoneVerify) must not be touched even if scratchpad is compliant.
// Safety gate #3: progress prefix must be "paused: DONE-GUARD:".
func TestDoneGuard_DoesNot_Revalidate_Idle_Paused_Agent(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)

	makeDoneGuardAgent(t, h, s, "dg-recv-2", "sess-dg-recv-2", validV3Content)
	s.AgentUpdate("dg-recv-2", map[string]any{
		"status":   "paused",
		"progress": "paused: yesloop-idle escalation: no PROVEN marker",
	})

	h.checkYesloopDoneGuard()

	agent, _ := s.AgentGet("dg-recv-2")
	if agent.Status != "paused" {
		t.Errorf("agent paused by Idle must NOT be revalidated by DoneGuard, got status=%q", agent.Status)
	}
}

// TestDoneGuard_BindsUnboundAgentBeforeEvidenceCheck: the Phase 5 evidence check
// verifies subagent session ids against opencode's session DB, and it needs the
// agent's own session id to do so. For an unbound agent the check was skipped
// silently — the guard then accepted unverified review evidence from exactly the
// agents whose bind had failed. The guard binds on demand before verifying.
func TestDoneGuard_BindsUnboundAgentBeforeEvidenceCheck(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeFind(t, "ses_guard")

	if err := s.AgentCreate(storage.Agent{
		ID: "dg-unbound", Project: "testproj", Section: "yesloop-dg-unbound",
		SessionID: "sess-dg-unbound", PID: testPID, Status: "running",
		Backend: "opencode", SockPath: "/nonexistent/dg-unbound.sock",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	agent, _ := s.AgentGet("dg-unbound")

	h.checkOneDoneGuardAgent(*agent, ValidationResult{
		Compliant:      true,
		PhaseCount:     6,
		Phase5Evidence: map[string]string{"cold-review": "ses_reviewer"},
	})

	stored, _ := s.AgentGet("dg-unbound")
	if stored.OpencodeSessionID != "ses_guard" {
		t.Errorf("OpencodeSessionID = %q, want the session bound before the evidence check", stored.OpencodeSessionID)
	}
}
