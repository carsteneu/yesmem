package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Test helpers (mirror yesloop_done_guard_test.go) ---

// makeSkillCheckAgent creates a running yesloop agent with zero counters and an
// optional scratchpad content ("" means none).
func makeSkillCheckAgent(t *testing.T, h *Handler, s *storage.Store, id, sessionID string, content string) {
	t.Helper()
	makeDoneGuardAgent(t, h, s, id, sessionID, content)
}

// jumpSkillCheckCounters drives an agent's telemetry counters past a threshold
// AFTER it has been tracked (baseline fixed at 0), so the delta fires REMIND.
func jumpSkillCheckCounters(t *testing.T, s *storage.Store, id string, outputTokens, turns int) {
	t.Helper()
	if err := s.AgentUpdateTelemetry(id, turns, 0, outputTokens); err != nil {
		t.Fatalf("AgentUpdateTelemetry: %v", err)
	}
}

// hasSkillCheckState reports whether an agent occupies a specific skillcheck state.
func hasSkillCheckState(agentID string, expectedState int) bool {
	yesloopSkillCheckAgentsMu.Lock()
	defer yesloopSkillCheckAgentsMu.Unlock()
	st, ok := yesloopSkillCheckAgents[agentID]
	if !ok {
		return false
	}
	return st.state == expectedState
}

// getSkillCheckRefireCount returns the refire count for an agent (-1 if untracked).
func getSkillCheckRefireCount(agentID string) int {
	yesloopSkillCheckAgentsMu.Lock()
	defer yesloopSkillCheckAgentsMu.Unlock()
	st, ok := yesloopSkillCheckAgents[agentID]
	if !ok {
		return -1
	}
	return st.refireCount
}

// setSkillCheckLastRelayAt backdates lastRelayAt past the refire interval.
func setSkillCheckLastRelayAt(agentID string, t time.Time) {
	yesloopSkillCheckAgentsMu.Lock()
	defer yesloopSkillCheckAgentsMu.Unlock()
	st, ok := yesloopSkillCheckAgents[agentID]
	if !ok {
		return
	}
	st.lastRelayAt = t
}

// --- Tests ---

func TestSkillCheck_TracksBelowThreshold(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-low", "sess-sc-low", "")
	h.checkYesloopSkillCheck()                        // track, baseline=0
	jumpSkillCheckCounters(t, s, "sc-low", 10_000, 5) // below threshold
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-low", yesloopSkillCheckStateTracking) {
		t.Errorf("below-threshold agent should stay TRACKING")
	}
	if rc := getSkillCheckRefireCount("sc-low"); rc != 0 {
		t.Errorf("refireCount should be 0 while tracking, got %d", rc)
	}
}

func TestSkillCheck_TriggersRemind_OnOutputTokens(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-out", "sess-sc-out", "")
	h.checkYesloopSkillCheck() // baseline 0
	jumpSkillCheckCounters(t, s, "sc-out", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-out", yesloopSkillCheckStateRemind) {
		t.Errorf("output-token threshold agent should transition to REMIND")
	}
}

func TestSkillCheck_TriggersRemind_OnTurnsFallback(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-turn", "sess-sc-turn", "")
	h.checkYesloopSkillCheck() // baseline 0
	jumpSkillCheckCounters(t, s, "sc-turn", 0, skillCheckTurnThreshold)
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-turn", yesloopSkillCheckStateRemind) {
		t.Errorf("turn-count fallback agent should transition to REMIND")
	}
}

func TestSkillCheck_NoImmediateRefire(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-imm", "sess-sc-imm", "")
	h.checkYesloopSkillCheck()
	jumpSkillCheckCounters(t, s, "sc-imm", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-imm", yesloopSkillCheckStateRemind) {
		t.Fatalf("precondition: agent should be in REMIND")
	}
	// Immediate re-check without progress must be interval-gated.
	h.checkYesloopSkillCheck()
	if rc := getSkillCheckRefireCount("sc-imm"); rc != 0 {
		t.Errorf("immediate re-check should not refire (interval-gated), got %d", rc)
	}
}

func TestSkillCheck_Confirmation_ResetsBaseline(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-conf", "sess-sc-conf", "")
	h.checkYesloopSkillCheck()
	jumpSkillCheckCounters(t, s, "sc-conf", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-conf", yesloopSkillCheckStateRemind) {
		t.Fatalf("precondition: agent should be in REMIND")
	}
	// Agent reloads the skill and confirms via scratchpad marker (round token 1).
	s.ScratchpadWrite("testproj", "yesloop-sc-conf", "**SKILL-RELOAD-1:** yes", "")
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-conf", yesloopSkillCheckStateConfirmed) {
		t.Errorf("SKILL-RELOAD marker should move agent to CONFIRMED")
	}
	// Next tick: baseline reset to current counters → back to TRACKING, no fire.
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-conf", yesloopSkillCheckStateTracking) {
		t.Errorf("after CONFIRMED, agent should return to TRACKING with reset baseline")
	}
}

func TestSkillCheck_ConfirmationDelaysNextFire(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-conf2", "sess-sc-conf2", "")
	h.checkYesloopSkillCheck() // TRACKING baseline 0
	jumpSkillCheckCounters(t, s, "sc-conf2", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND (round 1)
	s.ScratchpadWrite("testproj", "yesloop-sc-conf2", "**SKILL-RELOAD-1:** yes", "")
	h.checkYesloopSkillCheck() // -> CONFIRMED
	h.checkYesloopSkillCheck() // -> TRACKING, baseline reset to current (100k)
	// No new output tokens since confirmation → must NOT re-fire immediately.
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-conf2", yesloopSkillCheckStateTracking) {
		t.Errorf("agent with fresh baseline must stay TRACKING, got state %d", stateOfSkillCheck("sc-conf2"))
	}
}

func TestSkillCheck_Escalation_AfterRefires(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-esc", "sess-sc-esc", "")
	h.checkYesloopSkillCheck() // TRACKING
	jumpSkillCheckCounters(t, s, "sc-esc", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND
	if !hasSkillCheckState("sc-esc", yesloopSkillCheckStateRemind) {
		t.Fatalf("precondition: agent should be in REMIND")
	}
	// 3 refires (interval elapsed each time) without marker → escalation.
	for i := 0; i < 3; i++ {
		setSkillCheckLastRelayAt("sc-esc", time.Now().Add(-2*yesloopSkillCheckRefireInterval))
		h.checkYesloopSkillCheck()
	}
	agent, _ := s.AgentGet("sc-esc")
	if agent.Status != "paused" {
		t.Errorf("agent without SKILL-RELOAD marker should be paused after max refires, got status=%q", agent.Status)
	}
	if !strings.Contains(agent.Progress, "skillcheck") {
		t.Errorf("pause reason should reference the skillcheck layer, got %q", agent.Progress)
	}
	if !hasSkillCheckState("sc-esc", yesloopSkillCheckStateDeadAgentEscalation) {
		t.Errorf("agent should end in DEAD_AGENT_ESCALATION")
	}
}

func TestSkillCheck_IgnoresNonYesloopAndNonRunning(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	// Non-yesloop section must not be tracked.
	makeSkillCheckAgent(t, h, s, "sc-other", "sess-sc-other", "")
	s.AgentUpdate("sc-other", map[string]any{"section": "research-foo"})
	h.checkYesloopSkillCheck()
	if hasSkillCheckState("sc-other", yesloopSkillCheckStateRemind) {
		t.Errorf("non-yesloop agent must not be tracked")
	}
	// Non-running yesloop agent must not be tracked.
	makeSkillCheckAgent(t, h, s, "sc-stopped", "sess-sc-stopped", "")
	s.AgentUpdate("sc-stopped", map[string]any{"status": "stopped"})
	h.checkYesloopSkillCheck()
	if hasSkillCheckState("sc-stopped", yesloopSkillCheckStateRemind) {
		t.Errorf("non-running agent must not be tracked")
	}
}

// stateOfSkillCheck reads an agent's current state int (test-helper).
func stateOfSkillCheck(agentID string) int {
	yesloopSkillCheckAgentsMu.Lock()
	defer yesloopSkillCheckAgentsMu.Unlock()
	st, ok := yesloopSkillCheckAgents[agentID]
	if !ok {
		return -1
	}
	return st.state
}

// getSkillCheckRound returns the tracked round token for an agent (-1 if untracked).
func getSkillCheckRound(agentID string) int {
	yesloopSkillCheckAgentsMu.Lock()
	defer yesloopSkillCheckAgentsMu.Unlock()
	st, ok := yesloopSkillCheckAgents[agentID]
	if !ok {
		return -1
	}
	return st.round
}

// TestSkillCheck_StaleMarker_DoesNotAutoConfirm: a SKILL-RELOAD marker from an
// earlier window must NOT confirm a later window — the round token changes per
// window, so a stale marker leaves the machine in REMIND (Cold Review I-1).
func TestSkillCheck_StaleMarker_DoesNotAutoConfirm(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-stale", "sess-sc-stale", "")
	h.checkYesloopSkillCheck() // TRACKING baseline 0
	jumpSkillCheckCounters(t, s, "sc-stale", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND round 1
	if rc := getSkillCheckRound("sc-stale"); rc != 1 {
		t.Fatalf("precondition: round should be 1, got %d", rc)
	}
	s.ScratchpadWrite("testproj", "yesloop-sc-stale", "**SKILL-RELOAD-1:** yes", "")
	h.checkYesloopSkillCheck() // -> CONFIRMED
	h.checkYesloopSkillCheck() // -> TRACKING, baseline reset to 100k

	// Window 2: another 100k output tokens since baseline. Old round-1 marker
	// is still in the scratchpad — it must NOT confirm window 2 (round 2).
	jumpSkillCheckCounters(t, s, "sc-stale", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck()
	if rc := getSkillCheckRound("sc-stale"); rc != 2 {
		t.Fatalf("window 2 should carry round token 2, got %d", rc)
	}
	if !hasSkillCheckState("sc-stale", yesloopSkillCheckStateRemind) {
		t.Errorf("stale marker must NOT auto-confirm window 2 — agent should stay in REMIND")
	}
}

// TestSkillCheck_EscalationAfterPriorConfirmation: the escalation path must stay
// reachable even after an earlier window was confirmed (Cold Review I-1).
func TestSkillCheck_EscalationAfterPriorConfirmation(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-esc2", "sess-sc-esc2", "")
	h.checkYesloopSkillCheck() // TRACKING
	jumpSkillCheckCounters(t, s, "sc-esc2", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND round 1
	s.ScratchpadWrite("testproj", "yesloop-sc-esc2", "**SKILL-RELOAD-1:** yes", "")
	h.checkYesloopSkillCheck() // -> CONFIRMED
	h.checkYesloopSkillCheck() // -> TRACKING (baseline = 100k)

	// Window 2: agent does NOT reload (no round-2 marker) — must escalate.
	jumpSkillCheckCounters(t, s, "sc-esc2", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND round 2 (not auto-confirmed)
	for i := 0; i < 3; i++ {
		setSkillCheckLastRelayAt("sc-esc2", time.Now().Add(-2*yesloopSkillCheckRefireInterval))
		h.checkYesloopSkillCheck()
	}
	agent, _ := s.AgentGet("sc-esc2")
	if agent.Status != "paused" {
		t.Errorf("agent refusing window-2 reload should be paused after max refires, got status=%q", agent.Status)
	}
	if !hasSkillCheckState("sc-esc2", yesloopSkillCheckStateDeadAgentEscalation) {
		t.Errorf("agent should end in DEAD_AGENT_ESCALATION")
	}
}

// TestSkillCheck_WrongRoundMarker_DoesNotConfirm: a marker with a wrong round
// number must not confirm the current window.
func TestSkillCheck_WrongRoundMarker_DoesNotConfirm(t *testing.T) {
	resetYesloopSkillCheckState()
	h, s := mustHandler(t)
	makeSkillCheckAgent(t, h, s, "sc-wrong", "sess-sc-wrong", "")
	h.checkYesloopSkillCheck() // TRACKING
	jumpSkillCheckCounters(t, s, "sc-wrong", skillCheckOutputTokenThreshold, 0)
	h.checkYesloopSkillCheck() // -> REMIND round 1
	// Agent writes the wrong round token (or an old, round-less marker).
	s.ScratchpadWrite("testproj", "yesloop-sc-wrong", "**SKILL-RELOAD:** yes", "")
	h.checkYesloopSkillCheck()
	if !hasSkillCheckState("sc-wrong", yesloopSkillCheckStateRemind) {
		t.Errorf("round-less or wrong-round marker must not confirm the window")
	}
}
