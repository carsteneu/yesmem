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
		Status:    "frozen",
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

// TestCheckYesloopIdle_CommitToDone transitions on send_to orchestrator evidence.
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

	// Agent should be frozen in the store
	agent, err := s.AgentGet("refire-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "frozen" {
		t.Errorf("after escalation, agent status should be frozen, got %s", agent.Status)
	}
	if !strings.Contains(agent.Progress, "yesloop-idle") {
		t.Errorf("progress should mention yesloop-idle, got %s", agent.Progress)
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

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy executed:** yes
**send_to orchestrator:** yes
`
}
