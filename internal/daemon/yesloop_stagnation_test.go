package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

func TestStagnationSigChangesOnProgress(t *testing.T) {
	sig1 := stagnationSig("content a", "Phase 1/6 ANALYZE")
	sig2 := stagnationSig("content b", "Phase 1/6 ANALYZE")
	sig3 := stagnationSig("content a", "Phase 2/6 PLAN")
	if sig1 == sig2 {
		t.Error("scratchpad change must change the signal")
	}
	if sig1 == sig3 {
		t.Error("progress change must change the signal")
	}
}

func TestBuildStagnationRelayMessage(t *testing.T) {
	msg := buildStagnationRelayMessage(2*time.Hour + 3*time.Minute + 45*time.Second)
	if !strings.Contains(msg, "STAGNATION") {
		t.Errorf("relay must carry STAGNATION marker, got: %s", msg)
	}
	if strings.ContainsAny(msg, "`*#") {
		t.Errorf("relay must be metachar-free for PTY injection, got: %s", msg)
	}
	if !strings.Contains(msg, "2h4m") {
		t.Errorf("relay must contain rounded stale duration (2h3m45s → 2h4m), got: %s", msg)
	}
}

func makeStagnationAgent(t *testing.T, h *Handler, s *storage.Store, id string, content string) {
	t.Helper()
	// The fixture's PID is this test process, so the row declares the backend
	// whose cmdline it reports: the guard gates on isAgentProcess (a test binary
	// has no opencode cmdline of its own).
	stubProcCmdline(t, agentProcCmdlineStub)
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     "sess-" + id,
		PID:           testPID,
		Status:        "running",
		Backend:       "opencode",
		SockPath:      "/nonexistent/" + id + ".sock",
		CallerSession: "caller-" + id,
		Progress:      "Phase 1/6 ANALYZE",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.ScratchpadWrite("testproj", "yesloop-"+id, content, "test"); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	// Stream active — otherwise the idle guard would own this case.
	streamStatesMu.Lock()
	streamStates["sess-"+id] = &StreamState{Active: true, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-"+id] = "sess-" + id
	sessionToThreadMu.Unlock()
}

func TestStagnation_TracksAndEscalates(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)

	makeStagnationAgent(t, h, s, "stag-1", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking\n")

	// Tick 1: registers snapshot, no relay possible yet.
	h.checkYesloopStagnation()

	// Simulate 3h without change.
	yesloopStagnationAgentsMu.Lock()
	st := yesloopStagnationAgents["stag-1"]
	if st != nil {
		st.lastChangedAt = time.Now().Add(-3 * time.Hour)
	}
	yesloopStagnationAgentsMu.Unlock()
	// The failure must not happen under the lock: t.Fatalf runs Goexit, so a
	// leaked mutex would deadlock every later test in the package.
	if st == nil {
		t.Fatalf("agent not tracked")
	}

	// Tick 2: threshold exceeded — initial relay fires (socket is a dead
	// path in tests, but the state machine must advance).
	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	state := yesloopStagnationAgents["stag-1"].state
	refires := yesloopStagnationAgents["stag-1"].refireCount
	yesloopStagnationAgentsMu.Unlock()
	if state != stagnationStateRefiring || refires != 0 {
		t.Errorf("expected refiring state at refireCount 0, got state=%d refires=%d", state, refires)
	}

	// Progress (content change) resets to tracking.
	if err := s.ScratchpadWrite("testproj", "yesloop-stag-1", "### Phase 1: ANALYZE\n**Status:** COMPLETE\nchanged\n", "test"); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	state = yesloopStagnationAgents["stag-1"].state
	yesloopStagnationAgentsMu.Unlock()
	if state != stagnationStateTracking {
		t.Errorf("progress must reset tracking, got state=%d", state)
	}
}

func TestStagnation_SkipsDoneAndInactive(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)

	// All 7 phases COMPLETE → DONE territory, skip.
	doneContent := ""
	for i := 1; i <= donePhaseCount; i++ {
		doneContent += fmt.Sprintf("### Phase %d: X\n**Status:** COMPLETE\n\n", i)
	}
	makeStagnationAgent(t, h, s, "stag-done", doneContent)
	h.checkYesloopStagnation()
	if len(yesloopStagnationAgents) != 0 {
		t.Errorf("DONE agents must not be tracked")
	}

	// Inactive stream → idle guard territory, skip.
	resetYesloopStagnationState()
	agent := storage.Agent{
		ID: "stag-idle", Project: "testproj", Section: "yesloop-stag-idle",
		SessionID: "sess-stag-idle", PID: testPID, Status: "running",
		SockPath: "/nonexistent/sock", Progress: "Phase 1/6",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.ScratchpadWrite("testproj", "yesloop-stag-idle", "content", "test"); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	streamStatesMu.Lock()
	streamStates["sess-stag-idle"] = &StreamState{Active: false, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-stag-idle"] = "sess-stag-idle"
	sessionToThreadMu.Unlock()
	h.checkYesloopStagnation()
	if len(yesloopStagnationAgents) != 0 {
		t.Errorf("inactive-stream agents must not be tracked")
	}
}

func TestStagnation_EscalatesAfterRefires(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)

	makeStagnationAgent(t, h, s, "stag-esc", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking\n")

	// Tick 1: register snapshot.
	h.checkYesloopStagnation()

	// Simulate >2h without change.
	yesloopStagnationAgentsMu.Lock()
	st := yesloopStagnationAgents["stag-esc"]
	st.lastChangedAt = time.Now().Add(-3 * time.Hour)
	yesloopStagnationAgentsMu.Unlock()

	// Tick 2: initial relay (refireCount stays 0).
	h.checkYesloopStagnation()

	// Refire 1.
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-esc"].lastRelayAt = time.Now().Add(-31 * time.Minute)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation()

	// Refire 2.
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-esc"].lastRelayAt = time.Now().Add(-31 * time.Minute)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation()

	// Refire 3 (refireCount reaches maxRefires — still not escalated).
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-esc"].lastRelayAt = time.Now().Add(-31 * time.Minute)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation()

	agent, err := s.AgentGet("stag-esc")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "running" {
		t.Fatalf("at maxRefires the agent must still be running, got %s", agent.Status)
	}

	// Refire 4 (refireCount > maxRefires) → escalation.
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-esc"].lastRelayAt = time.Now().Add(-31 * time.Minute)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation()

	// The escalation is a nudge: the agent keeps running and the finding sits on
	// the row.
	assertGuardNudged(t, s, "stag-esc", "yesloop-stagnation")

	yesloopStagnationAgentsMu.Lock()
	state := yesloopStagnationAgents["stag-esc"].state
	yesloopStagnationAgentsMu.Unlock()
	if state != stagnationStateNudged {
		t.Errorf("expected the terminal nudge state, got state=%d", state)
	}
}

// The guard writes its note into the very column its signal reads, so the
// terminal state has to survive that write. Otherwise the nudge re-arms itself and
// the same standstill produces a new notice every window.
func TestStagnation_NudgeSurvivesItsOwnNote(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)
	makeStagnationAgent(t, h, s, "stag-once", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking\n")

	driveStagnationToNudge(t, h)

	row := assertGuardNudged(t, s, "stag-once", "yesloop-stagnation")

	// The next tick must keep the terminal state despite the note in progress.
	h.checkYesloopStagnation()
	yesloopStagnationAgentsMu.Lock()
	state := yesloopStagnationAgents["stag-once"].state
	yesloopStagnationAgentsMu.Unlock()
	if state != stagnationStateNudged {
		t.Errorf("the guard's own note must not re-arm the ladder, state = %d", state)
	}

	// And a second nudge cycle must not start from a stale window either: the
	// timestamp did not move, so nothing new is claimed.
	after, err := s.AgentGet("stag-once")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" || after.Progress != row.Progress {
		t.Errorf("a tick after the nudge must change nothing, status = %q progress = %q", after.Status, after.Progress)
	}
}

// driveStagnationToNudge walks the ladder from a fresh state to its nudge: one
// registration tick, the initial relay, then the refires past the cap.
func driveStagnationToNudge(t *testing.T, h *Handler) {
	t.Helper()
	h.checkYesloopStagnation() // register snapshot
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-once"].lastChangedAt = time.Now().Add(-3 * time.Hour)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation() // initial relay
	for i := 0; i < 4; i++ {
		yesloopStagnationAgentsMu.Lock()
		if st, ok := yesloopStagnationAgents["stag-once"]; ok {
			st.lastRelayAt = time.Now().Add(-31 * time.Minute)
		}
		yesloopStagnationAgentsMu.Unlock()
		h.checkYesloopStagnation()
	}
}

func TestStagnation_SkipsNonRunningNonYesloopEmpty(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)

	// Non-running (paused) → skip.
	if err := s.AgentCreate(storage.Agent{
		ID: "stag-paused", Project: "testproj", Section: "yesloop-stag-paused",
		SessionID: "sess-stag-paused", PID: testPID, Status: "paused",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// Non-yesloop section → skip.
	if err := s.AgentCreate(storage.Agent{
		ID: "stag-gen", Project: "testproj", Section: "general-task",
		SessionID: "sess-stag-gen", PID: testPID, Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// Running yesloop agent with active stream but empty scratchpad → skip.
	if err := s.AgentCreate(storage.Agent{
		ID: "stag-empty", Project: "testproj", Section: "yesloop-stag-empty",
		SessionID: "sess-stag-empty", PID: testPID, Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	streamStatesMu.Lock()
	streamStates["sess-stag-empty"] = &StreamState{Active: true, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-stag-empty"] = "sess-stag-empty"
	sessionToThreadMu.Unlock()

	h.checkYesloopStagnation()

	if len(yesloopStagnationAgents) != 0 {
		t.Errorf("non-running/non-yesloop/empty-scratchpad agents must not be tracked, got %d", len(yesloopStagnationAgents))
	}
}

// A recycled PID must not enter the stagnation protocol. The guard's action is a
// relay into the agent's socket, which belongs to the agent's own process
// (Learning #101907); the row's own scratchpad content is not evidence that its
// process is still the one the row names.
func TestStagnation_RecycledPIDNotTracked(t *testing.T) {
	h, s := mustHandler(t)
	pinProcessTable(t)
	bystander, _ := startBystander(t)
	// The row declares an opencode process; the PID holds a live bystander with a
	// foreign cmdline — a recycled number, not this agent's process.
	stubProcCmdline(t, "/usr/bin/sleep\x00--recycled-stagnation")
	agent := storage.Agent{
		ID: "stag-recycled", Project: "testproj", Section: "yesloop-stag-recycled",
		SessionID: "sess-stag-recycled", PID: bystander.Process.Pid,
		Status: "running", Backend: "opencode", Progress: "Phase 1/6 ANALYZE",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.ScratchpadWrite("testproj", "yesloop-stag-recycled", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\n", "test"); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	streamStatesMu.Lock()
	streamStates["sess-stag-recycled"] = &StreamState{Active: true, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-stag-recycled"] = "sess-stag-recycled"
	sessionToThreadMu.Unlock()

	h.checkYesloopStagnation()

	if _, tracked := yesloopStagnationAgents["stag-recycled"]; tracked {
		t.Error("the PID is held by another process — this agent is gone, so the stagnation guard must not track it")
	}
}

// Without a readable process table (macOS) the stagnation guard keeps tracking a
// live agent instead of going dark.
func TestStagnation_WithoutProcessTableStillTracks(t *testing.T) {
	h, s := mustHandler(t)
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })
	agent := storage.Agent{
		ID: "stag-no-proc", Project: "testproj", Section: "yesloop-stag-no-proc",
		SessionID: "sess-stag-no-proc", PID: testPID,
		Status: "running", Backend: "opencode", Progress: "Phase 1/6 ANALYZE",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.ScratchpadWrite("testproj", "yesloop-stag-no-proc", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\n", "test"); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	streamStatesMu.Lock()
	streamStates["sess-stag-no-proc"] = &StreamState{Active: true, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-stag-no-proc"] = "sess-stag-no-proc"
	sessionToThreadMu.Unlock()

	h.checkYesloopStagnation()

	if _, tracked := yesloopStagnationAgents["stag-no-proc"]; !tracked {
		t.Error("without a process table the stagnation guard went dark for a live agent")
	}
}
