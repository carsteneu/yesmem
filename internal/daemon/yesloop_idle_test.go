package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// The broken-toolcall guard is a second owner of these agents (YM-196): a
	// case left in its package-global map would keep an agent suppressed for
	// every later test in this binary.
	resetBrokenToolcallState()
}

// makeYesloopAgent creates a minimal yesloop agent in the store and populates
// stream state to simulate the given stream_active status. The fixture's PID is
// this test process, so the row declares the backend whose cmdline it reports:
// the idle guard gates on isAgentProcess, and "alive" there means "verifies as
// this agent's own process" (a test binary has no opencode cmdline of its own).
func makeYesloopAgent(t *testing.T, h *Handler, s *storage.Store, id, sessionID string, pid int, streamActive bool, idleFor time.Duration) {
	t.Helper()
	stubProcCmdline(t, agentProcCmdlineStub)
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     sessionID,
		PID:           pid,
		Status:        "running",
		Backend:       "opencode",
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
	phaseContent := buildSevenPhaseCompleteContent()
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

### Phase 6: DAEMON-VERIFY
**Status:** COMPLETE
Vorab-Prüfung: verify-claims -> exit 0 (accepted)

### Phase 7: FINISH
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

	// Agent keeps running: a guard nudges, it does not stop the agent
	assertGuardNudged(t, s, "refire-1", "yesloop-idle")
}

// TestCheckYesloopIdle_EscalationNotice_IsANudge: the DEAD_AGENT wording is gone
// with the pause. The orchestrator gets one informational notice that names the
// standstill and asks nothing: there is no hold to lift, so the relay hint would
// point at a resume that is not due.
func TestCheckYesloopIdle_EscalationNotice_IsANudge(t *testing.T) {
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

	assertGuardNudged(t, s, "hint-1", "yesloop-idle")
}

// --- Helpers ---

// buildSevenPhaseCompleteContent returns a scratchpad string with all 7 phases
// marked as COMPLETE.
func buildSevenPhaseCompleteContent() string {
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
`
}

// TestPhase5ColdReviewPresent_NewFormat: the canonical new-format Phase 5
// block (dispatch lines, Sub-phase headers) must satisfy the positive trace
// check — the DONE-gate accepts exactly these blocks, so the idle checker
// must not later pause them as DEAD_AGENT.
func TestPhase5ColdReviewPresent_NewFormat(t *testing.T) {
	if !phase5ColdReviewPresent(validV3Content) {
		t.Error("new-format Phase 5 block (dispatch lines, Sub-phase headers) must satisfy the idle positive trace check")
	}
}

// --- YM-149: re-arm, prune, marker form, cross-layer brake ---

// The relay asks for an [x] per phase ("mark each phase in scratchpad with [x]"),
// while the state machine only read the DONE contract's Status line. An agent that
// answered the relay the way it was asked stalled in REMARK_REQUEST forever.
func TestCheckYesloopIdle_XMarkerFormAdvances(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "xmark-1", "sess-xmark", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["xmark-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest
	yesloopIdleAgentsMu.Unlock()

	s.ScratchpadWrite("testproj", "yesloop-xmark-1", `### Phase 1: ANALYZE [x]
notes

### Phase 2: PLAN [x]
notes

### Phase 3: EXECUTE [x]
notes

### Phase 4: VERIFY [x]
notes

### Phase 5: REVIEW [x]
cold-review: ses_abc

### Phase 6: DAEMON-VERIFY [x]
notes

### Phase 7: FINISH [x]
notes
`, "")

	h.checkYesloopIdle()

	if !hasIdleState("xmark-1", yesloopIdleStateCommitRequest) {
		t.Error("phases marked with [x] as the relay asked must advance to COMMIT_REQUEST")
	}
}

// "UNPROVEN" contains "PROVEN". The old substring check read a reminder that
// nothing had been proven as proof and advanced the protocol on it.
func TestCheckYesloopIdle_UnprovenSubstringIsNotAProvenMarker(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "unproven-1", "sess-unproven", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["unproven-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateSelfCheck
	yesloopIdleAgentsMu.Unlock()

	s.ScratchpadWrite("testproj", "yesloop-unproven-1", "The claim is UNPROVEN and the phases are unproven too", "")

	h.checkYesloopIdle()

	if hasIdleState("unproven-1", yesloopIdleStateRemarkRequest) {
		t.Error("a substring of PROVEN must not advance the idle protocol")
	}
}

// DONE was terminal until the next daemon restart: a loop agent that finished the
// protocol and kept working was never checked again, however long it ran.
func TestCheckYesloopIdle_DoneReArmsAfterStreamActivity(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "rearm-1", "sess-rearm", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["rearm-1"]
	state.state = yesloopIdleStateDone
	state.idleSince = time.Now().Add(-11 * time.Minute)
	yesloopIdleAgentsMu.Unlock()

	// The agent is demonstrably working again: its stream produced output.
	streamStatesMu.Lock()
	streamStates["sess-rearm"].Active = true
	streamStatesMu.Unlock()
	h.checkYesloopIdle()

	if !hasIdleState("rearm-1", yesloopIdleStateWorking) {
		t.Fatal("a completed idle protocol must re-arm once the agent works again")
	}
	yesloopIdleAgentsMu.Lock()
	zero := yesloopIdleAgents["rearm-1"].idleSince.IsZero()
	yesloopIdleAgentsMu.Unlock()
	if !zero {
		t.Error("re-arming must clear idleSince")
	}

	// And a fresh idle period must escalate again.
	streamStatesMu.Lock()
	streamStates["sess-rearm"].Active = false
	streamStatesMu.Unlock()
	h.checkYesloopIdle()
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["rearm-1"].idleSince = time.Now().Add(-11 * time.Minute)
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	if !hasIdleState("rearm-1", yesloopIdleStateSelfCheck) {
		t.Error("the re-armed guard must escalate again on a fresh idle period")
	}
}

// The in-memory map was never cleaned: every agent the daemon had ever seen kept
// an entry until the next restart.
func TestCheckYesloopIdle_PrunesStatesOfVanishedAgents(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "prune-a", "sess-prune-a", testPID, false, 11*time.Minute)
	makeYesloopAgent(t, h, s, "prune-b", "sess-prune-b", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	before := len(yesloopIdleAgents)
	yesloopIdleAgentsMu.Unlock()
	if before != 2 {
		t.Fatalf("tracked agents = %d, want 2", before)
	}

	if err := s.AgentDelete("prune-b"); err != nil {
		t.Fatalf("AgentDelete: %v", err)
	}
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	_, keptA := yesloopIdleAgents["prune-a"]
	_, keptB := yesloopIdleAgents["prune-b"]
	tracked := len(yesloopIdleAgents)
	yesloopIdleAgentsMu.Unlock()

	if !keptA {
		t.Error("a live agent lost its idle state")
	}
	if keptB || tracked != 1 {
		t.Errorf("the state of a vanished agent was kept (tracked=%d)", tracked)
	}
}

// The relay says "mark each phase in scratchpad with [x]" and prescribes no line.
// A dedicated marker line inside the phase's block is the literal answer and must
// advance the protocol — the first version of the check demanded the phase number
// on the marker's line, which would have paused exactly the agents that did what
// they were told (review round 2).
func TestCheckYesloopIdle_DedicatedMarkerLinesAdvance(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "mline-1", "sess-mline", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["mline-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest
	yesloopIdleAgentsMu.Unlock()

	s.ScratchpadWrite("testproj", "yesloop-mline-1", `### Phase 1: ANALYZE
[x]

### Phase 2: PLAN
- [x]

### Phase 3: EXECUTE
[x] Phase 3

### Phase 4: VERIFY
- [x] VERIFY

### Phase 5: REVIEW
cold-review: ses_abc
[x]

### Phase 6: DAEMON-VERIFY
[x] Phase 6

### Phase 7: FINISH
[x] Phase 7
`, "")

	h.checkYesloopIdle()

	if !hasIdleState("mline-1", yesloopIdleStateCommitRequest) {
		t.Error("dedicated marker lines per phase block must advance to COMMIT_REQUEST")
	}
}

// "- [x] done" inside a phase block is a checklist item, not a phase mark. The
// first version of the dedicated-line check accepted any single word (the
// uppercase alternative ran under (?i)), which reopened exactly the hole round 1
// closed (review round 3).
func TestCheckYesloopIdle_SingleWordCheckboxesDoNotCountAsPhases(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "singleword-1", "sess-singleword", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["singleword-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest
	yesloopIdleAgentsMu.Unlock()

	s.ScratchpadWrite("testproj", "yesloop-singleword-1", `### Phase 1: ANALYZE
- [x] done

### Phase 2: PLAN
- [x] done

### Phase 3: EXECUTE
- [x] done

### Phase 4: VERIFY
- [x] done

### Phase 5: REVIEW
cold-review: ses_abc
- [x] done

### Phase 6: DAEMON-VERIFY
- [x] done

### Phase 7: FINISH
- [x] done
`, "")

	h.checkYesloopIdle()

	if hasIdleState("singleword-1", yesloopIdleStateCommitRequest) {
		t.Error("one-word checklist items counted as completed phases")
	}
}

// Six incidental checkboxes are not six completed phases: the [x] counts only on
// a dedicated marker line of the phase's block or on its header line, and the
// phase reference on that line has to be a phase (review findings, rounds 1-3).
func TestCheckYesloopIdle_IncidentalCheckboxesDoNotCountAsPhases(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "checkbox-1", "sess-checkbox", testPID, false, 11*time.Minute)
	h.checkYesloopIdle()

	yesloopIdleAgentsMu.Lock()
	state, _ := yesloopIdleAgents["checkbox-1"]
	state.idleSince = time.Now().Add(-11 * time.Minute)
	state.state = yesloopIdleStateRemarkRequest
	yesloopIdleAgentsMu.Unlock()

	s.ScratchpadWrite("testproj", "yesloop-checkbox-1", `### Phase 1: ANALYZE
- [x] read the code

### Phase 2: PLAN
- [x] wrote the plan

### Phase 3: EXECUTE
- [x] committed

### Phase 4: VERIFY
- [x] ran the tests

### Phase 5: REVIEW
cold-review: ses_abc
- [x] ran the review

### Phase 6: DAEMON-VERIFY
- [x] sent the verified report

### Phase 7: FINISH
- [x] sent the report
`, "")

	h.checkYesloopIdle()

	if hasIdleState("checkbox-1", yesloopIdleStateCommitRequest) {
		t.Error("incidental checkboxes counted as completed phases")
	}
}

// The done-verify layer may already have settled an agent (DONE_VERIFIED) while
// its row still says running. Layer 2 then relayed into an agent the daemon itself
// had declared done (live 2026-10-15: DONE_VERIFIED at 23:36, idle relays from
// 23:42) — the two layers contradicted each other for hours.
func TestCheckYesloopIdle_SkipsAgentsSettledByDoneVerify(t *testing.T) {
	resetIdleState()
	resetDoneVerifyState()
	h, s := mustHandler(t)

	makeYesloopAgent(t, h, s, "settled-1", "sess-settled", testPID, false, 11*time.Minute)
	settledRow, err := s.AgentGet("settled-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["settled-1"] = &yesloopDoneVerifyState{
		state:     yesloopDoneVerifyStateDoneVerified,
		createdAt: settledRow.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	// First tick registers the idle window; the second one is past the 10min
	// threshold, so it is the one that would relay.
	h.checkYesloopIdle()
	yesloopIdleAgentsMu.Lock()
	if st, ok := yesloopIdleAgents["settled-1"]; ok {
		st.idleSince = time.Now().Add(-11 * time.Minute)
	}
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	if hasIdleState("settled-1", yesloopIdleStateSelfCheck) || hasIdleState("settled-1", yesloopIdleStateDone) {
		t.Error("the idle layer relayed to an agent the done-verify layer already settled")
	}
}

// A recycled PID must not enter the escalation protocol. Signal 0 says a
// process holds the number, but the guard's actions — a relay into the agent's
// socket, a pause and a DEAD_AGENT notice to the orchestrator — belong to the
// agent's OWN process (Learning #101907). Before, a dead agent whose number had
// been recycled was driven through SELF_CHECK and, after the refires, paused.
func TestCheckYesloopIdle_RecycledPIDNotTracked(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	pinProcessTable(t)
	bystander, _ := startBystander(t)
	makeYesloopAgent(t, h, s, "recycled-idle", "sess-recycled-idle", bystander.Process.Pid, false, 11*time.Minute)
	// The fixture declares its own process; here the PID holds a live bystander,
	// whose cmdline is not this agent's backend binary.
	stubProcCmdline(t, "/usr/bin/sleep\x00--recycled-idle")

	h.checkYesloopIdle()

	if hasIdleState("recycled-idle", yesloopIdleStateWorking) {
		t.Error("the PID is held by another process — this agent is gone, so the idle guard must not track it")
	}
}

// The mirror direction on a platform without a readable process table (macOS):
// identity cannot be established, and the guard only relays into the agent's own
// inject socket — a recycled PID cannot receive that relay. So the guard keeps
// the pre-identity behaviour there (signal 0 decides) instead of going dark; the
// same rule holds for the skillcheck and stagnation guards.
func TestCheckYesloopIdle_WithoutProcessTableStillTracks(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })
	makeYesloopAgent(t, h, s, "no-proc-idle", "sess-no-proc-idle", testPID, false, 11*time.Minute)

	h.checkYesloopIdle()

	if !hasIdleState("no-proc-idle", yesloopIdleStateSelfCheck) && !hasIdleState("no-proc-idle", yesloopIdleStateWorking) {
		t.Error("without a process table the idle guard went dark for a live agent")
	}
}

// --- YM-207: real progress must end the relay protocol ---

// driveYesloopIntoProgression walks an agent into SELF_CHECK the way the idle
// timeout does: the first tick registers the idle window, the second one
// transitions and sends relay 1.
func driveYesloopIntoProgression(t *testing.T, h *Handler, s *storage.Store, id string) {
	t.Helper()
	makeYesloopAgent(t, h, s, id, "sess-"+id, testPID, false, 11*time.Minute)
	h.checkYesloopIdle()
	yesloopIdleAgentsMu.Lock()
	if st, ok := yesloopIdleAgents[id]; ok {
		st.idleSince = time.Now().Add(-11 * time.Minute)
	}
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()
	if !hasIdleState(id, yesloopIdleStateSelfCheck) {
		t.Fatalf("precondition: expected SELF_CHECK after the idle timeout")
	}
}

// YM-207, live 2026-10-16 19:33: the guard escalated with "no PROVEN marker
// (refireCount=3, max=2)" and paused a loop agent that had meanwhile reported
// Phase 4 work. Only WORKING ever reset the progression, so an agent's own
// output was invisible to the refires that ran on to the pause.
//
// The answer restarts the relay count and leaves the state alone: the agent is
// still mid-pipeline, and dropping back to WORKING would throw away the gate's
// progress and re-ask the first question (consequence review, finding 1).
func TestCheckYesloopIdle_ProgressSinceRelayResetsProtocol(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "progress-1")

	// The agent answers the relay with real work: scratchpad and phase move.
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["progress-1"].lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	if err := s.ScratchpadWrite("testproj", "yesloop-progress-1",
		"### Phase 4: VERIFY\n**Status:** IN PROGRESS\nTests laufen", ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	h.handleUpdateAgentStatus(map[string]any{"id": "progress-1", "phase": "Phase 4/6 VERIFY"})

	h.checkYesloopIdle()

	if !hasIdleState("progress-1", yesloopIdleStateSelfCheck) {
		t.Error("the answer must not drop the agent out of the protocol it is answering")
	}
	if got := getIdleStateRefireCount("progress-1"); got != 0 {
		t.Errorf("refireCount = %d after real progress, want 0 (the relay was answered)", got)
	}
}

// The counter restarted, not the state: an agent that answered with work and is
// then silent for three refire intervals is still escalated, and one that does
// write the evidence advances — the ladder has to keep both directions.
func TestCheckYesloopIdle_ProgressKeepsLadderAndEscalation(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "progress-ladder")

	// Answer relay 1 with the evidence the gate asks for.
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["progress-ladder"].lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()
	if err := s.ScratchpadWrite("testproj", "yesloop-progress-ladder",
		"**PROVEN:** all phases checked\n\n### Phase 1: ANALYZE\nnotes\n\n### Phase 2: PLAN\nnotes\n\n### Phase 3: EXECUTE\nnotes\n\n"+
			"### Phase 4: VERIFY\nnotes\n\n### Phase 5: REVIEW\ncold-review: ses_abc\n\n"+
			"### Phase 6: DAEMON-VERIFY\nVorab-Prüfung: accepted\n\n### Phase 7: FINISH\nsend_to orchestrator: yes\n", ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	h.handleUpdateAgentStatus(map[string]any{"id": "progress-ladder", "phase": "Phase 6/6 FINISH"})

	h.checkYesloopIdle()

	if !hasIdleState("progress-ladder", yesloopIdleStateRemarkRequest) {
		t.Error("an agent that answered with the gate's evidence must advance through the ladder")
	}
}

// The sharp end of the same defect: a working agent was paused and reported
// DEAD_AGENT solely because time passed since the relay. Here it keeps working
// across four refire intervals — the point is that no interval elapse can
// escalate an agent that is producing output.
func TestCheckYesloopIdle_ProgressPreventsDeadAgentEscalation(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "progress-2")

	for i := 0; i < 4; i++ {
		if err := s.ScratchpadWrite("testproj", "yesloop-progress-2",
			fmt.Sprintf("### Phase 5: REVIEW\n**Status:** IN PROGRESS\nSchritt %d", i), ""); err != nil {
			t.Fatalf("ScratchpadWrite: %v", err)
		}
		yesloopIdleAgentsMu.Lock()
		if st, ok := yesloopIdleAgents["progress-2"]; ok {
			st.lastRelayAt = time.Now().Add(-91 * time.Second)
		}
		yesloopIdleAgentsMu.Unlock()
		h.checkYesloopIdle()

		if got := getIdleStateRefireCount("progress-2"); got > 1 {
			t.Fatalf("refireCount reached %d in interval %d although the agent kept working", got, i)
		}
	}

	row, err := s.AgentGet("progress-2")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("an agent that reported work must not be paused by the idle escalations, status = %q (progress=%q)",
			row.Status, row.Progress)
	}
	if !hasIdleState("progress-2", yesloopIdleStateSelfCheck) {
		t.Error("a working agent stays in the protocol's current rung until it answers it")
	}
}

// The phase an agent reports through update_agent_status is progress on its own:
// a loop agent that moves on to the next phase has answered the relay even when
// its scratchpad write is still pending (review round 2, on YM-207). The
// scratchpad is untouched here, so only the phase can restart the count.
func TestCheckYesloopIdle_PhaseChangeAloneResetsProtocol(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "progress-3")

	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["progress-3"].lastRelayAt = time.Now().Add(-91 * time.Second)
	yesloopIdleAgentsMu.Unlock()

	h.handleUpdateAgentStatus(map[string]any{"id": "progress-3", "phase": "Phase 5/6 REVIEW"})
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("progress-3"); got != 0 {
		t.Errorf("a reported phase change must restart the relay count, got refireCount = %d", got)
	}
	if !hasIdleState("progress-3", yesloopIdleStateSelfCheck) {
		t.Error("the agent stays in the rung it is answering")
	}
}

// --- YM-225: progress the scratchpad cannot show ---
//
// In Phase 3/4 the scratchpad and the reported phase stay unchanged for many
// minutes while the agent produces tool turn after tool turn. The idle guard
// read only those two, so it escalated working agents (live 2026-10-16 22:45,
// agent-20261016-24: paused as DEAD_AGENT with turns_used=83 and a live PID).

// idleSessionStub is a minimal opencode v2 service for this guard. It serves an
// assistant-turn list whose newest entry can be open, can carry a running tool,
// or can have started long before the relay. Bodies use the live response shape.
type idleSessionStub struct {
	mu          sync.Mutex
	count       int
	at          int64
	openTurn    bool
	runningTool bool
	stale       bool
}

func newIdleSessionStub(t *testing.T) (*idleSessionStub, *httptest.Server) {
	t.Helper()
	stub := &idleSessionStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/message") {
			http.NotFound(w, r)
			return
		}
		stub.mu.Lock()
		data := make([]any, 0, stub.count)
		for i := 0; i < stub.count; i++ {
			newest := i == 0
			created := stub.at - 1000
			if newest {
				// A turn that is producing started after the relay's baseline. The
				// margin keeps the comparison independent of the millisecond both
				// stamps fall into.
				created = stub.at + 1000
			}
			if newest && stub.stale {
				created = stub.at - int64(time.Hour/time.Millisecond)
			}
			completed := stub.at
			if newest && stub.openTurn {
				completed = 0
			}
			content := []any{}
			if newest && stub.runningTool {
				content = []any{map[string]any{
					"type": "tool", "name": "bash",
					"state": map[string]any{"status": "running"},
				}}
			}
			data = append(data, map[string]any{
				"id":      fmt.Sprintf("msg_%d", i),
				"type":    "assistant",
				"finish":  "tool-calls",
				"content": content,
				"time":    map[string]any{"created": created, "completed": completed},
			})
		}
		stub.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"data": data, "cursor": map[string]any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return stub, srv
}

// report sets how many assistant turns the session has, all completed now.
func (s *idleSessionStub) report(n int) {
	s.reset(n)
}

// reportStreaming serves one newest turn the model is still writing.
func (s *idleSessionStub) reportStreaming() {
	s.reset(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openTurn = true
}

// reportRunningTool serves one settled newest turn whose tool still runs: the
// shape of a long command or a subagent (the daemon's broken-toolcall guard
// reads a turn the same way).
func (s *idleSessionStub) reportRunningTool() {
	s.reset(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningTool = true
}

// reportStaleOpenTurn serves one open turn that started long before the relay.
// An aborted turn looks like this and is not activity now.
func (s *idleSessionStub) reportStaleOpenTurn() {
	s.reset(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openTurn = true
	s.stale = true
}

func (s *idleSessionStub) reset(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count = n
	s.at = time.Now().UnixMilli()
	s.openTurn = false
	s.runningTool = false
	s.stale = false
}

// driveBoundYesloopIntoProgression is driveYesloopIntoProgression for an agent
// that carries an opencode session id, so the guard can read its messages.
func driveBoundYesloopIntoProgression(t *testing.T, h *Handler, s *storage.Store, id string) {
	t.Helper()
	makeBoundYesloopAgent(t, h, s, id, "ses-"+id)
	h.checkYesloopIdle()
	yesloopIdleAgentsMu.Lock()
	if st, ok := yesloopIdleAgents[id]; ok {
		st.idleSince = time.Now().Add(-11 * time.Minute)
	}
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()
	if !hasIdleState(id, yesloopIdleStateSelfCheck) {
		t.Fatalf("precondition: expected SELF_CHECK after the idle timeout")
	}
}

// expireIdleRelay backdates the last relay past the 90s refire interval, so the
// next tick is the one that would refire or escalate.
func expireIdleRelay(id string) {
	yesloopIdleAgentsMu.Lock()
	if st, ok := yesloopIdleAgents[id]; ok {
		st.lastRelayAt = time.Now().Add(-91 * time.Second)
	}
	yesloopIdleAgentsMu.Unlock()
}

// addTurns advances the agent's turn counter the way the proxy's usage
// tracking does: one per LLM request, i.e. one per tool turn.
func addTurns(t *testing.T, s *storage.Store, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.AgentUpdateTelemetry(id, 1, 100, 50); err != nil {
			t.Fatalf("AgentUpdateTelemetry(%s): %v", id, err)
		}
	}
}

// giveIdleAgentASocket arms the agent's inject socket so the guard's relays
// actually land. Relaying is when the work baseline moves (armWorkBaseline), so
// a fixture without a socket never reaches the path that keeps relay answers
// from accumulating.
func giveIdleAgentASocket(t *testing.T, h *Handler, s *storage.Store, id string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "idle")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// The relay dials the path derived from the agent id (YM-338), so the daemon's
	// data directory has to be the one holding this listener.
	h.dataDir = dir
	sockPath := filepath.Join(dir, id+".sock")
	ln, err := net.Listen("unix", sockPath+".inject")
	if err != nil {
		t.Fatalf("listen inject: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()
	if err := s.AgentUpdate(id, map[string]any{"sock_path": sockPath}); err != nil {
		t.Fatalf("AgentUpdate(sock_path): %v", err)
	}
}

// A working agent in Phase 4 often changes neither scratchpad nor phase for
// minutes; its turns_used moves with every tool call. A single turn is the
// agent answering the relay (#82787); several are work.
func TestCheckYesloopIdle_TurnProgressKeepsTheAgentAlive(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "turns-1")

	addTurns(t, s, "turns-1", 5)
	expireIdleRelay("turns-1")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("turns-1"); got != 0 {
		t.Errorf("refireCount = %d after five new turns, want 0 (the relay was answered)", got)
	}
	if !hasIdleState("turns-1", yesloopIdleStateSelfCheck) {
		t.Error("the agent stays in the rung it is answering")
	}
	row, err := s.AgentGet("turns-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("a working agent must not be paused, status = %q (progress=%q)", row.Status, row.Progress)
	}
}

// The counter direction of the same rule: an agent that answers each relay with
// exactly one turn and does nothing else is hanging, and the ladder has to keep
// escalating it (#79819, #82787).
func TestCheckYesloopIdle_SingleRelayAnswerIsStillNoProgress(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "answer-only")
	giveIdleAgentASocket(t, h, s, "answer-only")

	addTurns(t, s, "answer-only", 1)
	for i := 0; i < 4; i++ {
		expireIdleRelay("answer-only")
		h.checkYesloopIdle()
	}

	// The escalation is still owed, but as a nudge: the agent keeps running.
	assertGuardNudged(t, s, "answer-only", "yesloop-idle")
}

// Telemetry can be missing for a row (the proxy thread never bound to it), so
// the opencode session is read as a second, independent source: several new
// assistant messages since the relay are work.
func TestCheckYesloopIdle_AssistantMessagesAreProgressWithoutTurns(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "messages-1")

	stub.report(5)
	expireIdleRelay("messages-1")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("messages-1"); got != 0 {
		t.Errorf("refireCount = %d after five new assistant turns, want 0", got)
	}
	row, err := s.AgentGet("messages-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("a working agent must not be paused, status = %q (progress=%q)", row.Status, row.Progress)
	}
}

// Same source, counter direction: the single answer message must not restart
// the count even when the turn counter stays untouched.
func TestCheckYesloopIdle_SingleAssistantMessageIsNoProgress(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "answer-msg")
	giveIdleAgentASocket(t, h, s, "answer-msg")

	stub.report(1)
	for i := 0; i < 4; i++ {
		expireIdleRelay("answer-msg")
		h.checkYesloopIdle()
	}

	// One assistant message is the relay answer, not progress: the ladder still
	// reaches its nudge, and the agent keeps running.
	assertGuardNudged(t, s, "answer-msg", "yesloop-idle")
}

// Every relay answer costs one turn, so a counter read as a plain delta would
// reach the threshold after two answers and stop the ladder for good. The
// baseline belongs to the relay that was sent, not to the state transition.
func TestCheckYesloopIdle_RepeatedRelayAnswersDoNotAccumulate(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "answer-loop")
	giveIdleAgentASocket(t, h, s, "answer-loop")

	for i := 0; i < 5; i++ {
		addTurns(t, s, "answer-loop", 1) // exactly one turn: the answer
		expireIdleRelay("answer-loop")
		h.checkYesloopIdle()
	}

	// Relay answers must not accumulate into progress: the ladder reaches its
	// nudge, and the agent keeps running.
	assertGuardNudged(t, s, "answer-loop", "yesloop-idle")
}

// The relay answer plus the one tool call it triggers are already two requests.
// An agent that stops there is hanging, not working: the threshold has to sit
// above the answer's own request chain.
func TestCheckYesloopIdle_AnswerWithOneToolCallIsStillNoProgress(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "answer-tool")
	giveIdleAgentASocket(t, h, s, "answer-tool")

	for i := 0; i < 5; i++ {
		addTurns(t, s, "answer-tool", 2) // answer + the tool call it triggers
		expireIdleRelay("answer-tool")
		h.checkYesloopIdle()
	}

	// An agent that answers and stops is still escalated, but as a nudge: the
	// row keeps running.
	assertGuardNudged(t, s, "answer-tool", "yesloop-idle")
}

// A turn the model is still writing produces no completed turn and no scratchpad
// write, but the agent is producing right now.
func TestCheckYesloopIdle_OpenTurnIsProgress(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "open-turn")
	giveIdleAgentASocket(t, h, s, "open-turn")

	stub.reportStreaming()
	expireIdleRelay("open-turn")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("open-turn"); got != 0 {
		t.Errorf("refireCount = %d while a turn is open, want 0", got)
	}
	row, err := s.AgentGet("open-turn")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("an agent with an open turn must not be paused, status = %q (progress=%q)", row.Status, row.Progress)
	}
}

// The live 2026-10-16 case: a test suite running for minutes. The turn is
// settled, but its tool still runs — the agent is working, not hung.
func TestCheckYesloopIdle_RunningToolIsProgress(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "running-tool")
	giveIdleAgentASocket(t, h, s, "running-tool")

	stub.reportRunningTool()
	expireIdleRelay("running-tool")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("running-tool"); got != 0 {
		t.Errorf("refireCount = %d while a tool runs, want 0", got)
	}
	row, err := s.AgentGet("running-tool")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("an agent running a tool must not be paused, status = %q (progress=%q)", row.Status, row.Progress)
	}
}

// An open turn that started before the relay is an aborted one: it says nothing
// about what the agent did since, so the ladder keeps escalating.
func TestCheckYesloopIdle_StaleOpenTurnIsNoProgress(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "stale-open")
	giveIdleAgentASocket(t, h, s, "stale-open")

	stub.reportStaleOpenTurn()
	for i := 0; i < 4; i++ {
		expireIdleRelay("stale-open")
		h.checkYesloopIdle()
	}

	// A turn aborted before the relay is not progress: the ladder reaches its
	// nudge, and the agent keeps running.
	assertGuardNudged(t, s, "stale-open", "yesloop-idle")
}

// The working signal is bounded: a turn that stays open for longer than
// yesloopIdleWorkingMax stops counting, so a turn that never finishes cannot hold
// the ladder off for good. The window is measured on the turn's own start, not on
// the relay: the guard's own relays move the cutoff past the start of a call that
// is still running (YM-306).
func TestCheckYesloopIdle_OpenTurnStopsCountingAfterTheBound(t *testing.T) {
	resetIdleState()
	stub, srv := newIdleSessionStub(t)
	stubV2Service(t, srv)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "stuck-open")
	giveIdleAgentASocket(t, h, s, "stuck-open")

	stub.reportStaleOpenTurn()

	expireIdleRelay("stuck-open")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("stuck-open"); got != 1 {
		t.Fatalf("refireCount = %d, want 1: a turn open past the bound must not reset the count", got)
	}

	// The turn is gone now (it ended or its process died), so the ladder has to
	// run out and nudge the agent.
	stub.report(0)
	for i := 0; i < 3; i++ {
		expireIdleRelay("stuck-open")
		h.checkYesloopIdle()
	}

	// An agent that stopped producing is still escalated, but as a nudge: the
	// row keeps running and nothing waits for the orchestrator.
	assertGuardNudged(t, s, "stuck-open", "yesloop-idle")
}
