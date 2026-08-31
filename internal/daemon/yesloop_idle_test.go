package daemon

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Test helpers ---

var testPID = os.Getpid()

// --- Test helpers ---

func resetIdleState() {
	resetYesloopIdleState()
	resetStreamState()
}

// makeYesloopAgent creates a minimal yesloop agent in the store and populates
// stream state to simulate the given stream_active status.
func makeYesloopAgent(t *testing.T, h *Handler, s *storage.Store, id, sessionID string, pid int, streamActive bool, idleFor time.Duration) {
	t.Helper()
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     sessionID,
		PID:           pid,
		Status:        "running",
		SockPath:      "/nonexistent/" + id + ".sock",
		CallerSession: "caller-" + id,
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// Register stream state
	streamStatesMu.Lock()
	state := &StreamState{
		Active:    streamActive,
		StartedAt: time.Now().Add(-idleFor),
	}
	streamStates[sessionID] = state
	streamStatesMu.Unlock()

	sessionToThreadMu.Lock()
	sessionToThread[sessionID] = sessionID
	sessionToThreadMu.Unlock()

	_ = h // used via h.getStreamFields
}

// hasIdleState checks that an agent has a specific idle state.
func hasIdleState(agentID string, expectedState int) bool {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	s, ok := yesloopIdleAgents[agentID]
	if !ok {
		return false
	}
	return s.state == expectedState
}

// getIdleStateRefireCount returns refireCount for an agent.
func getIdleStateRefireCount(agentID string) int {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	s, ok := yesloopIdleAgents[agentID]
	if !ok {
		return -1
	}
	return s.refireCount
}

// --- Tests ---

// TestCheckYesloopIdle_NonYesloopAgent skips agents without yesloop- prefix.
func TestCheckYesloopIdle_NonYesloopAgent(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	agent := storage.Agent{
		ID:        "regular-agent",
		Project:   "testproj",
		Section:   "general-task",
		SessionID: "sess-1",
		PID:       999999,
		Status:    "running",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.checkYesloopIdle()

	if hasIdleState("regular-agent", yesloopIdleStateWorking) {
		t.Error("non-yesloop agent should not be tracked")
	}
}

// TestCheckYesloopIdle_NotRunning skips agents with status != running.
func TestCheckYesloopIdle_NotRunning(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	agent := storage.Agent{
		ID:        "stopped-yesloop",
		Project:   "testproj",
		Section:   "yesloop-stopped",
		SessionID: "sess-2",
		PID:       999998,
		Status:    "paused",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.checkYesloopIdle()

	if hasIdleState("stopped-yesloop", yesloopIdleStateWorking) {
		t.Error("non-running agent should not be tracked")
	}
}

// TestCheckYesloopIdle_ActiveStream stays in WORKING when stream is active.
func TestCheckYesloopIdle_ActiveStream(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	// Agent with active stream, no idle time
	makeYesloopAgent(t, h, s, "active-1", "sess-active", testPID, true, 0)

	// First tick: create entry
	h.checkYesloopIdle()

	// Second tick: should reset to WORKING because stream is active
	h.checkYesloopIdle()

	if !hasIdleState("active-1", yesloopIdleStateWorking) {
		t.Error("active stream agent should stay in WORKING")
	}

	yesloopIdleAgentsMu.Lock()
	idleState, ok := yesloopIdleAgents["active-1"]
	if ok {
		idleZero := idleState.idleSince.IsZero()
		if !idleZero {
			t.Error("idleSince should be zero for active-stream agent")
		}
	}
	yesloopIdleAgentsMu.Unlock()
}

// TestCheckYesloopIdle_StreamBlipDoesNotResetProgression: a relay response
// flips stream_active to true for a few ticks. While the state machine is
// progressing (SELF_CHECK), that blip must NOT reset state and refireCount
// (#82787). Only WORKING resets.
func TestCheckYesloopIdle_StreamBlipDoesNotResetProgression(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	// Agent idle 11min (above 10min threshold) with inactive stream.
	makeYesloopAgent(t, h, s, "blip-1", "sess-blip", testPID, false, 11*time.Minute)

	// First tick: register idleSince.
	h.checkYesloopIdle()

	// Backdate idleSince so the next tick transitions WORKING -> SELF_CHECK.
	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["blip-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	yesloopIdleAgentsMu.Unlock()

	// Second tick: transition to SELF_CHECK (relay 1 fires).
	h.checkYesloopIdle()
	if !hasIdleState("blip-1", yesloopIdleStateSelfCheck) {
		t.Fatalf("expected SELF_CHECK after idle timeout")
	}

	// Relay response: stream flips active for a few ticks.
	streamStatesMu.Lock()
	streamStates["sess-blip"].Active = true
	streamStatesMu.Unlock()
	h.checkYesloopIdle()

	// State machine must still be in SELF_CHECK with refireCount intact.
	if !hasIdleState("blip-1", yesloopIdleStateSelfCheck) {
		t.Errorf("stream blip must not reset progression state")
	}
	if got := getIdleStateRefireCount("blip-1"); got != 0 {
		t.Errorf("stream blip must not touch refireCount, got %d", got)
	}
}

// TestCheckYesloopIdle_InsufficientIdle stays in WORKING until timeout.
func TestCheckYesloopIdle_InsufficientIdle(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	// Agent idle for only 1 minute (well below 10min threshold)
	makeYesloopAgent(t, h, s, "short-idle", "sess-short", testPID, false, 1*time.Minute)

	// First tick: detect inactivity, set idleSince
	h.checkYesloopIdle()

	// Second tick: still below timeout
	h.checkYesloopIdle()

	if !hasIdleState("short-idle", yesloopIdleStateWorking) {
		t.Error("agent idle for 1min should stay in WORKING")
	}
}

// TestCheckYesloopIdle_IdleToSelfCheck transitions after 10min idle.
func TestCheckYesloopIdle_IdleToSelfCheck(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	// Agent idle for 11 minutes (above 10min threshold)
	makeYesloopAgent(t, h, s, "idle-1", "sess-idle1", testPID, false, 11*time.Minute)

	// First tick: registers idleSince based on stream_started + 11min ago
	h.checkYesloopIdle()

	// We need to simulate that idleSince was set in the past.
	// After first check, idleSince = now. We need to backdate it.
	yesloopIdleAgentsMu.Lock()
	if state, ok := yesloopIdleAgents["idle-1"]; ok {
		state.idleSince = time.Now().Add(-11 * time.Minute)
	}
	yesloopIdleAgentsMu.Unlock()

	// Now check again — should transition to SELF_CHECK
	h.checkYesloopIdle()

	if !hasIdleState("idle-1", yesloopIdleStateSelfCheck) {
		t.Error("agent idle 11min should transition to SELF_CHECK, got state",
			func() int {
				yesloopIdleAgentsMu.Lock()
				defer yesloopIdleAgentsMu.Unlock()
				if s, ok := yesloopIdleAgents["idle-1"]; ok {
					return s.state
				}
				return -1
			}())
	}
}

// TestCheckYesloopIdle_SelfCheckToRemark transitions on PROVEN marker.
func TestCheckYesloopIdle_SelfCheckToRemark(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "selfcheck-1", "sess-sc1", testPID, false, 11*time.Minute)

	// First tick: set idleSince
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["selfcheck-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateSelfCheck // force to SELF_CHECK
	yesloopIdleAgentsMu.Unlock()

	// Write PROVEN marker into scratchpad content
	s.ScratchpadWrite("testproj", "yesloop-selfcheck-1", "Some text with PROVEN marker", "")

	// Check — should transition to REMARK_REQUEST
	h.checkYesloopIdle()

	if !hasIdleState("selfcheck-1", yesloopIdleStateRemarkRequest) {
		t.Error("agent with PROVEN marker should transition to REMARK_REQUEST")
	}
}

// TestCheckYesloopIdle_RemarkToCommit transitions on 6 completed phases.
func TestCheckYesloopIdle_RemarkToCommit(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "remark-1", "sess-rm1", testPID, false, 11*time.Minute)

	// First tick: set idleSince
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["remark-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest // force to REMARK_REQUEST
	yesloopIdleAgentsMu.Unlock()

	// Write a scratchpad with all 6 phases completed
	phaseContent := buildSixPhaseCompleteContent()
	s.ScratchpadWrite("testproj", "yesloop-remark-1", phaseContent, "")

	// Check — should transition to COMMIT_REQUEST
	h.checkYesloopIdle()

	if !hasIdleState("remark-1", yesloopIdleStateCommitRequest) {
		t.Error("agent with 6 completed phases should transition to COMMIT_REQUEST")
	}
}

// TestCheckYesloopIdle_RemarkToCommit_Phase5ColdReviewMissing verifies that
// a scratchpad claiming all 6 phases COMPLETE but Phase 5 has only "REVIEW
// BLOCKED" without a subagent / task() / cold review trace does NOT transition
// to COMMIT_REQUEST. This catches the rationalization pattern where the agent
// writes Status: COMPLETE but documents Stage 2 as blocked in the body.
func TestCheckYesloopIdle_RemarkToCommit_Phase5ColdReviewMissing(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "remark-blocked-1", "sess-rb1", testPID, false, 11*time.Minute)

	// First tick: set idleSince
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["remark-blocked-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest
	yesloopIdleAgentsMu.Unlock()

	// 6 phases COMPLETE, Phase 5 dispatch field carries the blocked value.
	// Mirrors the modern field-contract form of the rationalization pattern:
	// "Stage 2 failed, dispatched: blocked because no Subagent infrastructure".
	content := `### Phase 1: ANALYZE
**Status:** COMPLETE
Goal understood

### Phase 2: PLAN
**Status:** COMPLETE
Plan stored

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE
Tests run: ok

### Phase 5: REVIEW
**Status:** COMPLETE
Stage 1 Self-Review: Done
**task() dispatched:** blocked
No Subagent infrastructure available

### Phase 6: FINISH
**Status:** COMPLETE
Deploy executed: yes
send_to orchestrator: yes
`
	s.ScratchpadWrite("testproj", "yesloop-remark-blocked-1", content, "")

	h.checkYesloopIdle()

	if hasIdleState("remark-blocked-1", yesloopIdleStateCommitRequest) {
		t.Error("agent with Phase 5 'task() dispatched: blocked' (no subagent trace) must NOT transition to COMMIT_REQUEST")
	}
}

// TestPhase5ColdReviewPresent_VetoScope pins the veto granularity: the word
// "blocked" must only veto when it is the VALUE of a dispatch field
// (task() dispatched / consequence dispatched). Prose occurrences — template
// quotes in findings text, idiom discussions — must NOT deadlock the idle
// machine (observed 2026-08-28: a Phase-5 findings line quoting
// "(yes|blocked)" froze a fully completed agent in REMARK_REQUEST).
func TestPhase5ColdReviewPresent_VetoScope(t *testing.T) {
	phase5 := func(body string) string {
		return `### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 2: Cold Review via task()**
**task() dispatched:** yes
**Subagent ID:** agent-x
` + body
	}

	t.Run("template_quote_in_findings_passes", func(t *testing.T) {
		block := phase5("**Findings:** none — idiom discussion cited `consequence dispatched: (yes|blocked)`")
		if !phase5ColdReviewPresent(block) {
			t.Error("prose occurrence of 'blocked' (template quote) must NOT veto a completed Stage 2")
		}
	})

	t.Run("task_dispatched_blocked_vetoes", func(t *testing.T) {
		block := phase5("**Subagent ID:** none")
		block = strings.Replace(block, "**task() dispatched:** yes", "**task() dispatched:** blocked", 1)
		if phase5ColdReviewPresent(block) {
			t.Error("task() dispatched: blocked must veto")
		}
	})

	t.Run("consequence_dispatched_blocked_vetoes", func(t *testing.T) {
		block := phase5("**Findings:** none")
		block += "\n**consequence dispatched:** blocked\n**Subagent ID:** agent-y\n**Findings:** none\n"
		if phase5ColdReviewPresent(block) {
			t.Error("consequence dispatched: blocked must veto")
		}
	})
}
func TestCheckYesloopIdle_CommitToDone(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "commit-1", "sess-cm1", testPID, false, 11*time.Minute)

	// First tick: set idleSince
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["commit-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateCommitRequest // force to COMMIT_REQUEST
	yesloopIdleAgentsMu.Unlock()

	// Write scratchpad with send_to orchestrator evidence
	s.ScratchpadWrite("testproj", "yesloop-commit-1", "Some content\n**send_to orchestrator:** yes - 2026-06-23T10:00\n", "")

	// Check — should transition to DONE
	h.checkYesloopIdle()

	if !hasIdleState("commit-1", yesloopIdleStateDone) {
		t.Error("agent with send_to evidence should transition to DONE")
	}
}

// TestCheckYesloopIdle_MaxRefires escalates to DEAD_AGENT after 2 re-fires.
func TestCheckYesloopIdle_MaxRefires(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "refire-1", "sess-rf1", testPID, false, 11*time.Minute)

	// First tick: set idleSince
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["refire-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateSelfCheck
	state.lastRelayAt = time.Now().Add(-91 * time.Second) // force ready for refire
	yesloopIdleAgentsMu.Unlock()

	// First re-fire
	h.checkYesloopIdle()

	if getIdleStateRefireCount("refire-1") != 1 {
		t.Error("first refire should set refireCount=1")
	}

	// Second re-fire
	yesloopIdleAgentsMu.Lock()
	state.lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	if getIdleStateRefireCount("refire-1") != 2 {
		t.Error("second refire should set refireCount=2")
	}

	// Third attempt — should escalate (beyond max re-fires)
	yesloopIdleAgentsMu.Lock()
	state.lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	// Agent should be in DONE state (terminal, after escalation)
	if !hasIdleState("refire-1", yesloopIdleStateDone) {
		t.Error("after max re-fires, agent should transition to DONE (escalated)")
	}

	// Agent should be paused in the store
	agent, err := s.AgentGet("refire-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "paused" {
		t.Errorf("after escalation, agent status should be paused, got %s", agent.Status)
	}
	if !strings.Contains(agent.Progress, "yesloop-idle") {
		t.Errorf("progress should mention yesloop-idle, got %s", agent.Progress)
	}
}

// TestCheckYesloopIdle_EscalationPayload_HasHint: the DEAD_AGENT notification
// sent to the orchestrator on idle escalation must include the pause-hint so
// the orchestrator reaches for relay_agent, not resume_agent (Learning #81175).
func TestCheckYesloopIdle_EscalationPayload_HasHint(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "hint-1", "sess-hint1", testPID, false, 11*time.Minute)

	// Drive the agent into the DONE/escalation state by walking through the
	// refire cycle, same as TestCheckYesloopIdle_MaxRefires.
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["hint-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateSelfCheck
	state.lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()

	h.checkYesloopIdle() // refire 1

	yesloopIdleAgentsMu.Lock()
	state.lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle() // refire 2

	yesloopIdleAgentsMu.Lock()
	state.lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle() // escalation

	if !hasIdleState("hint-1", yesloopIdleStateDone) {
		t.Fatal("expected DONE (escalation) state")
	}

	// Read the channel messages for the caller session and check the hint.
	msgs, err := s.GetChannelMessages("caller-hint-1")
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one DEAD_AGENT message to caller")
	}
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Content, orchestratorPauseHint) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no DEAD_AGENT message contained the pause hint %q; got messages: %+v",
			orchestratorPauseHint, msgs)
	}
}

// --- Helpers ---

// buildSixPhaseCompleteContent returns a scratchpad string with all 6 phases
// marked as COMPLETE.
func buildSixPhaseCompleteContent() string {
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

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy executed:** yes
**send_to orchestrator:** yes
`
}
