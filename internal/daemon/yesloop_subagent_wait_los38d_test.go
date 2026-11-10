package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

// los38dDoneClaim is a scratchpad that carries a DONE-claim: the structural
// Phase 7 indicator is what puts an agent into the done-verify protocol.
const los38dDoneClaim = "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\n\n" +
	"### Phase 7: FINISH\n**send_to orchestrator:** yes\n"

// los38dProvenDone is a scratchpad that satisfies isVerified: seven completed
// phases, the BEWEISEN marker and the send_to evidence line.
func los38dProvenDone() string {
	s := ""
	for i := 1; i <= donePhaseCount; i++ {
		s += fmt.Sprintf("### Phase %d: X\n**Status:** COMPLETE\n\n", i)
	}
	return s + "BEWEISEN\nsend_to orchestrator: yes\n"
}

// A relay into an agent that is blocked inside a task() subagent only queues as
// a pending user message and hits it after the subagent returns (#89428). The
// stagnation guard measures progress, so it must not fire while the child runs,
// and its window must restart when the child ends instead of firing at once.
func TestLos38D_StagnationFrozenWhileSubagentRuns(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeStagnationAgent(t, h, s, "stag-sub38d", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking\n")

	// Tick 1 registers the progress baseline.
	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	_, tracked := yesloopStagnationAgents["stag-sub38d"]
	yesloopStagnationAgentsMu.Unlock()
	if !tracked {
		t.Fatalf("agent not tracked after the first tick")
	}

	// Three hours without a scratchpad change, but the agent is inside a
	// task() subagent — the child is its work, not stagnation.
	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-sub38d"].lastChangedAt = time.Now().Add(-3 * time.Hour)
	yesloopStagnationAgentsMu.Unlock()

	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	st := yesloopStagnationAgents["stag-sub38d"]
	state, changedAt, refires, frozen := st.state, st.lastChangedAt, st.refireCount, !st.subagentFrozenAt.IsZero()
	yesloopStagnationAgentsMu.Unlock()

	if state != stagnationStateTracking || refires != 0 {
		t.Errorf("no stagnation relay may fire while a task() subagent runs, got state=%d refires=%d", state, refires)
	}
	if !frozen {
		t.Error("the freeze must be recorded so the guard reports it")
	}
	if time.Since(changedAt) > time.Minute {
		t.Errorf("the stagnation window must restart while the child runs, lastChangedAt is %s old",
			time.Since(changedAt).Round(time.Second))
	}
}

// The freeze must lift once the child is gone: a genuinely stagnant agent still
// gets its relay.
func TestLos38D_StagnationResumesAfterSubagentEnds(t *testing.T) {
	resetYesloopStagnationState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeStagnationAgent(t, h, s, "stag-resume38d", "### Phase 1: ANALYZE\n**Status:** IN PROGRESS\nworking\n")
	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	yesloopStagnationAgents["stag-resume38d"].lastChangedAt = time.Now().Add(-3 * time.Hour)
	yesloopStagnationAgentsMu.Unlock()
	h.checkYesloopStagnation()

	// The child ends; the agent produces nothing for another three hours.
	stubOpencodeChildActive(t, false)
	yesloopStagnationAgentsMu.Lock()
	st := yesloopStagnationAgents["stag-resume38d"]
	st.lastChangedAt = time.Now().Add(-3 * time.Hour)
	st.subagentFrozenAt = time.Now()
	yesloopStagnationAgentsMu.Unlock()

	h.checkYesloopStagnation()

	yesloopStagnationAgentsMu.Lock()
	state, frozen := yesloopStagnationAgents["stag-resume38d"].state, !yesloopStagnationAgents["stag-resume38d"].subagentFrozenAt.IsZero()
	yesloopStagnationAgentsMu.Unlock()
	if state != stagnationStateRefiring {
		t.Errorf("after the child ended a stagnant agent must be relayed to, got state=%d", state)
	}
	if frozen {
		t.Error("the freeze mark must be cleared once the child is gone")
	}
}

// The done-verify relay asks the agent to prove its phases. Queued behind a
// running task() subagent it reaches the agent only after the subagent returns,
// where it interrupts the wrong turn (#89428).
func TestLos38D_DoneVerifyFrozenWhileSubagentRuns(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneVerifyAgent(t, h, s, "dv-sub38d", "sess-dv-sub38d", los38dDoneClaim)

	h.checkYesloopDoneVerify()

	yesloopDoneVerifyAgentsMu.Lock()
	st, tracked := yesloopDoneVerifyAgents["dv-sub38d"]
	var state int
	var frozen bool
	if tracked {
		state, frozen = st.state, !st.subagentFrozenAt.IsZero()
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	if !tracked {
		t.Fatalf("the agent row must stay known to the layer")
	}
	if state != yesloopDoneVerifyStateNotDone {
		t.Errorf("no verify relay may fire while a task() subagent runs, got state=%s", yesloopDoneVerifyStateNames[state])
	}
	if !frozen {
		t.Error("the freeze must be recorded so the guard reports it")
	}
}

// The refire machine must not escalate an agent that is busy with a subagent:
// paused by DEAD_AGENT_ESCALATION it would stop working on the very review the
// child is running.
func TestLos38D_DoneVerifyNoEscalationWhileSubagentRuns(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneVerifyAgent(t, h, s, "dv-esc38d", "sess-dv-esc38d", los38dDoneClaim)

	agent, err := s.AgentGet("dv-esc38d")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet: %v", err)
	}

	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["dv-esc38d"] = &yesloopDoneVerifyState{
		state:          yesloopDoneVerifyStateVerifyRequested,
		refireCount:    yesloopDoneVerifyMaxRefires - 1,
		lastRelayAt:    time.Now().Add(-2 * yesloopDoneVerifyRefireInterval),
		transitionedAt: time.Now().Add(-time.Hour),
		createdAt:      agent.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	h.checkYesloopDoneVerify()

	after, err := s.AgentGet("dv-esc38d")
	if err != nil || after == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status == "paused" {
		t.Errorf("an agent inside a task() subagent must not be paused by done-verify, got status=%q progress=%q",
			after.Status, after.Progress)
	}
	if rc := getDoneVerifyRefireCount("dv-esc38d"); rc != yesloopDoneVerifyMaxRefires-1 {
		t.Errorf("the refire count must not advance while the child runs, got %d", rc)
	}
}

// Settlement is not a relay: an agent that already proved its completion is
// marked DONE even while a child of its own is still streaming.
func TestLos38D_DoneVerifySettlesWhileSubagentRuns(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneVerifyAgent(t, h, s, "dv-ok38d", "sess-dv-ok38d", los38dProvenDone())

	agent, err := s.AgentGet("dv-ok38d")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["dv-ok38d"] = &yesloopDoneVerifyState{
		state:       yesloopDoneVerifyStateVerifyRequested,
		lastRelayAt: time.Now(),
		createdAt:   agent.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	h.checkYesloopDoneVerify()

	if !hasDoneVerifyState("dv-ok38d", yesloopDoneVerifyStateDoneVerified) {
		t.Error("a proven agent must be settled DONE; only the relay waits for the child")
	}
}

// After the child is gone the refire machine resumes on its own interval.
func TestLos38D_DoneVerifyResumesAfterSubagentEnds(t *testing.T) {
	resetDoneVerifyState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneVerifyAgent(t, h, s, "dv-resume38d", "sess-dv-resume38d", los38dDoneClaim)

	agent, err := s.AgentGet("dv-resume38d")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["dv-resume38d"] = &yesloopDoneVerifyState{
		state:       yesloopDoneVerifyStateVerifyRequested,
		refireCount: 1,
		lastRelayAt: time.Now().Add(-2 * yesloopDoneVerifyRefireInterval),
		createdAt:   agent.CreatedAt,
	}
	yesloopDoneVerifyAgentsMu.Unlock()

	// Frozen: nothing happens.
	h.checkYesloopDoneVerify()
	if rc := getDoneVerifyRefireCount("dv-resume38d"); rc != 1 {
		t.Fatalf("the refire count must not advance while the child runs, got %d", rc)
	}

	// Child gone, interval still elapsed → the refire runs.
	stubOpencodeChildActive(t, false)
	yesloopDoneVerifyAgentsMu.Lock()
	yesloopDoneVerifyAgents["dv-resume38d"].lastRelayAt = time.Now().Add(-2 * yesloopDoneVerifyRefireInterval)
	yesloopDoneVerifyAgentsMu.Unlock()

	h.checkYesloopDoneVerify()

	if rc := getDoneVerifyRefireCount("dv-resume38d"); rc != 2 {
		t.Errorf("after the child ended the refire must resume, got refireCount=%d", rc)
	}
}

// Same class for the DONE-guard: its relay names the missing phase fields, and
// queued behind a subagent it lands after the agent already moved on.
func TestLos38D_DoneGuardFrozenWhileSubagentRuns(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneGuardAgent(t, h, s, "dg-sub38d", "sess-dg-sub38d", allPhasesPhase4Invalid)

	h.checkYesloopDoneGuard()

	yesloopDoneGuardAgentsMu.Lock()
	st, tracked := yesloopDoneGuardAgents["dg-sub38d"]
	var frozen bool
	var refires int
	if tracked {
		frozen, refires = !st.subagentFrozenAt.IsZero(), st.refireCount
	}
	yesloopDoneGuardAgentsMu.Unlock()
	if !tracked {
		t.Fatalf("the row must stay tracked so the freeze is reported and bounded")
	}
	if !frozen {
		t.Error("the freeze must be recorded")
	}
	if refires != 0 {
		t.Errorf("no relay attempt may be counted while a task() subagent runs, got refireCount=%d", refires)
	}

	after, err := s.AgentGet("dg-sub38d")
	if err != nil || after == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" {
		t.Errorf("the agent must not be paused while its child runs, got %q", after.Status)
	}
}

// The DONE-guard's pause is the harshest of the three: it stops an agent whose
// scratchpad it could only judge after the child returned.
func TestLos38D_DoneGuardNoEscalationWhileSubagentRuns(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneGuardAgent(t, h, s, "dg-esc38d", "sess-dg-esc38d", allPhasesPhase4Invalid)

	yesloopDoneGuardAgentsMu.Lock()
	yesloopDoneGuardAgents["dg-esc38d"] = &doneGuardState{
		state:       doneGuardStateRefiring,
		refireCount: doneGuardMaxRefires,
		lastRelayAt: time.Now().Add(-2 * doneGuardRefireInterval),
	}
	yesloopDoneGuardAgentsMu.Unlock()

	h.checkYesloopDoneGuard()

	after, err := s.AgentGet("dg-esc38d")
	if err != nil || after == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status == "paused" {
		t.Errorf("an agent inside a task() subagent must not be paused by the DONE-guard, got status=%q progress=%q",
			after.Status, after.Progress)
	}
	if rc := getDoneGuardRefireCount("dg-esc38d"); rc != doneGuardMaxRefires {
		t.Errorf("the refire count must not advance while the child runs, got %d", rc)
	}
}

// The relay the guard owed the agent while its child ran must fire on the first
// tick after the child is gone, not after another refire interval.
func TestLos38D_DoneGuardResumesAfterSubagentEnds(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, true)

	makeDoneGuardAgent(t, h, s, "dg-resume38d", "sess-dg-resume38d", allPhasesPhase4Invalid)
	h.checkYesloopDoneGuard()

	// The child ends; the freeze mark and a stale relay time are what a frozen
	// episode leaves behind.
	stubOpencodeChildActive(t, false)
	yesloopDoneGuardAgentsMu.Lock()
	st := yesloopDoneGuardAgents["dg-resume38d"]
	if st == nil {
		yesloopDoneGuardAgentsMu.Unlock()
		t.Fatalf("row not tracked after the frozen tick")
	}
	st.subagentFrozenAt = time.Now()
	st.lastRelayAt = time.Now().Add(-time.Hour)
	yesloopDoneGuardAgentsMu.Unlock()

	h.checkYesloopDoneGuard()

	yesloopDoneGuardAgentsMu.Lock()
	st = yesloopDoneGuardAgents["dg-resume38d"]
	var relayedAt time.Time
	var frozen bool
	var refires int
	if st != nil {
		relayedAt, frozen, refires = st.lastRelayAt, !st.subagentFrozenAt.IsZero(), st.refireCount
	}
	yesloopDoneGuardAgentsMu.Unlock()

	if st == nil {
		t.Fatalf("the first owed relay must re-register the row")
	}
	if time.Since(relayedAt) > time.Minute {
		t.Error("the owed first relay must fire on the first tick after the child is gone")
	}
	if frozen {
		t.Error("the freeze mark must be cleared once the child is gone")
	}
	if refires != 0 {
		t.Errorf("the owed relay is the first one, got refireCount=%d", refires)
	}
}

// A freeze must not restart the relay history: a row that already sent its
// first relay keeps that fact, so the child's end produces the next relay (and
// the future escalation) instead of a second initial relay per episode.
func TestLos38D_DoneGuardKeepsRelayHistoryAcrossFreeze(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, false)

	makeDoneGuardAgent(t, h, s, "dg-hist38d", "sess-dg-hist38d", allPhasesPhase4Invalid)

	// First tick: the initial relay goes out.
	h.checkYesloopDoneGuard()
	yesloopDoneGuardAgentsMu.Lock()
	first := yesloopDoneGuardAgents["dg-hist38d"]
	yesloopDoneGuardAgentsMu.Unlock()
	if first == nil || !first.relayed || first.refireCount != 0 {
		t.Fatalf("precondition: the initial relay must be recorded, got %+v", first)
	}

	// The child runs, then ends.
	stubOpencodeChildActive(t, true)
	h.checkYesloopDoneGuard()
	stubOpencodeChildActive(t, false)
	h.checkYesloopDoneGuard()

	yesloopDoneGuardAgentsMu.Lock()
	after := yesloopDoneGuardAgents["dg-hist38d"]
	var refires int
	var relayed bool
	if after != nil {
		refires, relayed = after.refireCount, after.relayed
	}
	yesloopDoneGuardAgentsMu.Unlock()

	if after != first {
		t.Error("the row must keep its state across a freeze; a new one re-sends the initial relay")
	}
	if refires != 1 {
		t.Errorf("the relay after the child must be the next refire, got refireCount=%d", refires)
	}
	if !relayed {
		t.Error("the relay history must survive the freeze")
	}
}

// The RECOVERY path must survive a running child: the unpause is a correction of
// the row's status, not a relay into the agent.
func TestLos38D_DoneGuardRecoverySurvivesChild(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// Covers the format guard's recovery against a running child. The
	// verification stage that a compliant DONE claim enters is
	// done_gate_verify_test.go's subject.
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})
	stubOpencodeChildActive(t, true)

	makeDoneGuardAgent(t, h, s, "dg-recv38d", "sess-dg-recv38d", validV3Content)
	if err := s.AgentUpdate("dg-recv38d", map[string]any{
		"status":   "paused",
		"progress": "paused: DONE-GUARD: phase validation failed — Phase4 missing required field",
	}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	h.checkYesloopDoneGuard()

	after, err := s.AgentGet("dg-recv38d")
	if err != nil || after == nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" {
		t.Errorf("the recovery unpause must not wait for a child, got status=%q", after.Status)
	}
}

// The marker is the second signal: it covers a backend that leaves no child
// session row behind (YM-253).
func TestLos38D_MarkerAloneFreezesTheGuard(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubOpencodeChildActive(t, false)

	makeDoneGuardAgent(t, h, s, "dg-marker38d", "sess-dg-marker38d", allPhasesPhase4Invalid)
	streamStatesMu.Lock()
	streamStates["sess-dg-marker38d"] = &StreamState{Active: false, IsSubagent: true, StartedAt: time.Now()}
	streamStatesMu.Unlock()
	sessionToThreadMu.Lock()
	sessionToThread["sess-dg-marker38d"] = "sess-dg-marker38d"
	sessionToThreadMu.Unlock()
	t.Cleanup(func() {
		streamStatesMu.Lock()
		delete(streamStates, "sess-dg-marker38d")
		streamStatesMu.Unlock()
		sessionToThreadMu.Lock()
		delete(sessionToThread, "sess-dg-marker38d")
		sessionToThreadMu.Unlock()
	})

	h.checkYesloopDoneGuard()

	if !hasDoneGuardState("dg-marker38d", doneGuardStateRefiring) {
		t.Fatalf("the marker alone must put the guard into the freeze")
	}
	yesloopDoneGuardAgentsMu.Lock()
	frozen := !yesloopDoneGuardAgents["dg-marker38d"].subagentFrozenAt.IsZero()
	yesloopDoneGuardAgentsMu.Unlock()
	if !frozen {
		t.Error("the freeze must be recorded")
	}
}

// The DONE-guard does not filter SessionID (heartbeat.go), so the gate must not
// die on a row whose agent session is empty but whose opencode session is bound.
func TestLos38D_ChildGateWithoutAgentSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	stubOpencodeChildActive(t, true)

	agent := storage.Agent{ID: "dg-nosess38d", Backend: "opencode", OpencodeSessionID: "ses-nosess38d"}
	if !h.agentWaitingInSubagent(agent) {
		t.Error("a bound opencode session must gate the guard even without agent.session_id")
	}

	stubOpencodeChildActive(t, false)
	if h.agentWaitingInSubagent(agent) {
		t.Error("without a signal the gate must be open")
	}
}
