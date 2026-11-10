package daemon

import (
	"encoding/json"
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

// --- YM-306 (Los 38k): three things the idle guard read as standstill ---
//
//  1. a running tool call: its assistant turn started before the newest relay
//     baseline, so "did the turn start after the cutoff" answered no although
//     the agent was working (live 17.10. 20:57-21:02, one go test);
//  2. an unreadable session: a failed GET /api/session/<id>/message was charged
//     to the refire counter as if the agent had produced nothing (live 22:27,
//     22:29);
//  3. the first relay asked a mid-pipeline agent whether all six phases were
//     done, which invites ticking phases that were never done.

// los38kSessionStub serves the opencode v2 message endpoint. Its newest
// assistant turn either carries a tool that has been running for minutes, or is
// a settled quiet turn, and the whole read can fail.
type los38kSessionStub struct {
	mu          sync.Mutex
	fail        bool
	longTool    bool
	toolStarted time.Time
	openTurn    bool
	openCreated time.Time
}

func (s *los38kSessionStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/message") {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		fail, longTool, started := s.fail, s.longTool, s.toolStarted
		openTurn, openCreated := s.openTurn, s.openCreated
		s.mu.Unlock()
		if fail {
			http.Error(w, `{"error":"service busy"}`, http.StatusInternalServerError)
			return
		}
		now := time.Now().UnixMilli()
		var msg map[string]any
		switch {
		case longTool:
			msg = map[string]any{
				"id": "msg_tool", "type": "assistant", "finish": "tool-calls",
				"content": []any{map[string]any{
					"type": "tool", "name": "bash",
					"state": map[string]any{"status": "running"},
				}},
				"time": map[string]any{
					"created":   started.UnixMilli(),
					"completed": started.Add(2 * time.Second).UnixMilli(),
				},
			}
		case openTurn:
			msg = map[string]any{
				"id": "msg_open", "type": "assistant",
				"time": map[string]any{"created": openCreated.UnixMilli(), "completed": 0},
			}
		default:
			msg = map[string]any{
				"id": "msg_quiet", "type": "assistant", "finish": "stop",
				"time": map[string]any{"created": now - 3000, "completed": now - 2000},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{msg}, "cursor": map[string]any{}})
	})
	return mux
}

func (s *los38kSessionStub) setFail(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = v
}

// startLongTool serves a settled turn whose bash tool still runs, started at the
// given moment.
func (s *los38kSessionStub) startLongTool(started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.longTool = true
	s.toolStarted = started
}

// startOpenTurnIn serves one open turn whose model is still writing, created at
// now+d. A positive d is a stamp the service could never honestly report.
func (s *los38kSessionStub) startOpenTurnIn(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openTurn = true
	s.openCreated = time.Now().Add(d)
}

func newLos38kSessionStub(t *testing.T) *los38kSessionStub {
	t.Helper()
	stub := &los38kSessionStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	stubV2Service(t, srv)
	return stub
}

// los38kCaptureRelay arms the agent's inject socket and hands back every relay
// body written to it.
func los38kCaptureRelay(t *testing.T, h *Handler, s *storage.Store, id string) <-chan string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "los38k")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// The relay dials the path derived from the agent id (YM-338), so the data
	// directory has to be the one holding this listener.
	h.dataDir = dir
	sockPath := filepath.Join(dir, id+".sock")
	ln, err := net.Listen("unix", sockPath+".inject")
	if err != nil {
		t.Fatalf("listen inject: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	relays := make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			data, _ := io.ReadAll(c)
			c.Close()
			relays <- string(data)
		}
	}()
	if err := s.AgentUpdate(id, map[string]any{"sock_path": sockPath}); err != nil {
		t.Fatalf("AgentUpdate(sock_path): %v", err)
	}
	return relays
}

func los38kAwaitRelay(t *testing.T, relays <-chan string) string {
	t.Helper()
	select {
	case body := <-relays:
		return body
	case <-time.After(2 * time.Second):
		return ""
	}
}

// driveLos38kIntoProgression walks an agent without a bound session into
// SELF_CHECK, sends relay 1 on the way, and returns that delivery together with
// the channel the later deliveries land on.
func driveLos38kIntoProgression(t *testing.T, h *Handler, s *storage.Store, id, scratchpad string) (string, <-chan string) {
	t.Helper()
	makeYesloopAgent(t, h, s, id, "sess-"+id, testPID, false, 11*time.Minute)
	if scratchpad != "" {
		if err := s.ScratchpadWrite("testproj", "yesloop-"+id, scratchpad, ""); err != nil {
			t.Fatalf("ScratchpadWrite: %v", err)
		}
	}
	relays := los38kCaptureRelay(t, h, s, id)
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
	return los38kAwaitRelay(t, relays), relays
}

// The live case of 17.10. 20:57:44-21:02:40: a single go test runs for five
// minutes. Every relay the guard sends moves the work baseline past the turn's
// start, so a start-time comparison stops seeing it after the first tick and the
// refires escalate a working agent. A tool that still runs is work as long as it
// is younger than yesloopIdleWorkingMax.
func TestLos38k_IdleGuardLongToolCallStaysWork(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-tool")
	giveIdleAgentASocket(t, h, s, "los38k-tool")

	stub.startLongTool(time.Now().Add(-5 * time.Minute))

	for i := 0; i < 4; i++ {
		expireIdleRelay("los38k-tool")
		h.checkYesloopIdle()
	}

	if got := getIdleStateRefireCount("los38k-tool"); got != 0 {
		t.Errorf("refireCount = %d while a tool was running for five minutes, want 0", got)
	}
	row, err := s.AgentGet("los38k-tool")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("an agent whose tool still runs must not be paused, status = %q (progress=%q)",
			row.Status, row.Progress)
	}
}

// The live case of 17.10. 22:27/22:29: the session read fails twice, and the
// guard counts the failure as "the agent produced nothing". A failed read is
// "cannot tell" and must leave the refire counter alone.
func TestLos38k_IdleGuardUnreadableSessionDoesNotRefire(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-unreadable")
	giveIdleAgentASocket(t, h, s, "los38k-unreadable")

	stub.setFail(true)

	for i := 0; i < 4; i++ {
		expireIdleRelay("los38k-unreadable")
		h.checkYesloopIdle()
	}

	if got := getIdleStateRefireCount("los38k-unreadable"); got != 0 {
		t.Errorf("refireCount = %d after four unreadable session reads, want 0", got)
	}
	row, err := s.AgentGet("los38k-unreadable")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("an unreadable session must not pause the agent, status = %q (progress=%q)",
			row.Status, row.Progress)
	}
}

// The other direction: "cannot tell" cannot hold the ladder off for good. The
// window is per episode: after it has passed, a session that stays unreadable no
// longer protects the agent.
func TestLos38k_IdleGuardUnreadableSessionReleasesAfterTheBound(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-unread-long")
	giveIdleAgentASocket(t, h, s, "los38k-unread-long")

	stub.setFail(true)
	expireIdleRelay("los38k-unread-long")
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount("los38k-unread-long"); got != 0 {
		t.Fatalf("refireCount = %d inside the unreadable window, want 0", got)
	}

	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["los38k-unread-long"].lastReadAt = time.Now().Add(-(yesloopIdleWorkingMax + time.Minute))
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("los38k-unread-long"); got != 1 {
		t.Errorf("refireCount = %d after the unreadable window passed, want 1", got)
	}
}

// The window is a total per episode, not one window per relay: a refire inside
// the episode must not buy a second window.
func TestLos38k_IdleGuardUnreadableWindowDoesNotRestartPerRelay(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-unread-episode")
	giveIdleAgentASocket(t, h, s, "los38k-unread-episode")

	stub.setFail(true)
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["los38k-unread-episode"].lastReadAt = time.Now().Add(-(yesloopIdleWorkingMax - 2*time.Minute))
	yesloopIdleAgentsMu.Unlock()

	expireIdleRelay("los38k-unread-episode")
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount("los38k-unread-episode"); got != 0 {
		t.Fatalf("refireCount = %d inside the window, want 0", got)
	}

	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["los38k-unread-episode"].lastReadAt = time.Now().Add(-(yesloopIdleWorkingMax + time.Minute))
	yesloopIdleAgentsMu.Unlock()
	expireIdleRelay("los38k-unread-episode")
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount("los38k-unread-episode"); got != 1 {
		t.Errorf("refireCount = %d after the episode aged out, want 1", got)
	}
}

// A readable stretch between two outages ends the first episode: the second one
// gets its own window instead of inheriting an aged one, or every outage after
// the first would be charged to the agent again — the defect this ticket fixes.
func TestLos38k_IdleGuardSecondUnreadableEpisodeGetsItsOwnWindow(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-unread-again")
	giveIdleAgentASocket(t, h, s, "los38k-unread-again")
	const id = "los38k-unread-again"

	stub.setFail(true)
	expireIdleRelay(id)
	h.checkYesloopIdle()
	// The outage ages out, as it would if the service had been gone for a while.
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents[id].lastReadAt = time.Now().Add(-(yesloopIdleWorkingMax + time.Minute))
	yesloopIdleAgentsMu.Unlock()

	// The service is back and the agent answers with work.
	stub.setFail(false)
	addTurns(t, s, id, yesloopIdleProgressTurns)
	expireIdleRelay(id)
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount(id); got != 0 {
		t.Fatalf("refireCount = %d after the agent answered, want 0", got)
	}

	// The service fails again: that is a new episode and must not escalate at once.
	stub.setFail(true)
	expireIdleRelay(id)
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount(id); got != 0 {
		t.Errorf("refireCount = %d in the second outage, want 0 (a readable read opens a new window)", got)
	}
}

// A stamp the service reports implausibly (here: a turn created a century from
// now) must not hold the guard off forever. The guard's own window caps it.
func TestLos38k_IdleGuardImplausibleTurnStampCannotStallTheGuard(t *testing.T) {
	resetIdleState()
	stub := newLos38kSessionStub(t)
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "los38k-future-stamp")
	giveIdleAgentASocket(t, h, s, "los38k-future-stamp")

	stub.startOpenTurnIn(100 * 365 * 24 * time.Hour)

	expireIdleRelay("los38k-future-stamp")
	h.checkYesloopIdle()
	if got := getIdleStateRefireCount("los38k-future-stamp"); got != 0 {
		t.Fatalf("refireCount = %d while the turn is young, want 0", got)
	}

	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["los38k-future-stamp"].workingSince = time.Now().Add(-(yesloopIdleWorkingMax + time.Minute))
	yesloopIdleAgentsMu.Unlock()
	expireIdleRelay("los38k-future-stamp")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("los38k-future-stamp"); got != 1 {
		t.Errorf("refireCount = %d after the guard's own window passed, want 1", got)
	}
}

// A mid-pipeline agent must not be asked whether all six phases are done: the
// question invites marking phases that were never done (live 17.10., agents in
// Phase 3).
func TestLos38k_IdleGuardMidPipelineRelayIsNotTheSixPhaseQuestion(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	body, _ := driveLos38kIntoProgression(t, h, s, "los38k-midpipe",
		"### Phase 3: EXECUTE\n**Status:** IN PROGRESS\nMilestone 1 laeuft")

	if body == "" {
		t.Fatal("no relay reached the agent socket")
	}
	if strings.Contains(strings.ToLower(body), "completed all 7 phases") {
		t.Errorf("a Phase 3 agent must not get the six-phase question, got: %s", body)
	}
	if !strings.Contains(body, "Continue the phase you are working on") {
		t.Errorf("the mid-pipeline relay must ask the agent to continue its phase, got: %s", body)
	}
}

// The refire is the delivery that lands when the agent ignored the first nudge, so
// it must not silently switch back to the six-phase question. And the wording has
// to follow the agent: once it has reached Phase 6, the next delivery is the
// six-phase question, which is the rung that can be answered and advances the
// ladder.
func TestLos38k_IdleGuardRefireKeepsThePhaseCorrectText(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	const id = "los38k-refire"

	first, relays := driveLos38kIntoProgression(t, h, s, id,
		"### Phase 3: EXECUTE\n**Status:** IN PROGRESS\nMilestone 1 laeuft")
	if strings.Contains(strings.ToLower(first), "completed all 7 phases") {
		t.Fatalf("precondition: the first delivery was already the six-phase question: %s", first)
	}

	expireIdleRelay(id)
	h.checkYesloopIdle()
	refire := los38kAwaitRelay(t, relays)
	if refire == "" {
		t.Fatal("the refire never reached the agent socket")
	}
	if strings.Contains(strings.ToLower(refire), "completed all 7 phases") {
		t.Errorf("the refire of a mid-pipeline agent must keep the mid-pipeline text, got: %s", refire)
	}

	// The agent reaches Phase 6. That write is itself work, so it resets the relay
	// accounting without a delivery; the delivery after it is the six-phase rung.
	if err := s.ScratchpadWrite("testproj", "yesloop-"+id,
		"### Phase 6: FINISH\n**Status:** IN PROGRESS\nsend_to orchestrator: pending", ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	expireIdleRelay(id)
	h.checkYesloopIdle()
	if got := los38kAwaitRelay(t, relays); got != "" {
		t.Fatalf("a scratchpad write must not be answered with a relay, got: %s", got)
	}

	expireIdleRelay(id)
	h.checkYesloopIdle()
	atPhaseSix := los38kAwaitRelay(t, relays)
	if !strings.Contains(atPhaseSix, idleRelayMessage(1)) {
		t.Errorf("an agent that has reached Phase 6 must get relay 1 verbatim, got: %s", atPhaseSix)
	}
}

// The six-phase question stays for the agents it is meant for: Phase 6 reached
// or a DONE-claim in the scratchpad.
func TestLos38k_IdleGuardPhaseSixAgentStillGetsTheSixPhaseQuestion(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	body, _ := driveLos38kIntoProgression(t, h, s, "los38k-phase6",
		"### Phase 6: FINISH\n**Status:** IN PROGRESS\nsend_to orchestrator: pending")

	if body == "" {
		t.Fatal("no relay reached the agent socket")
	}
	if !strings.Contains(body, idleRelayMessage(1)) {
		t.Errorf("an agent at Phase 6 must get relay 1 verbatim, got: %s", body)
	}
}

// An agent that reports Phase 6/6 through update_agent_status before it writes
// the Phase 6 header is at the end of the pipeline too: the phase field alone has
// to switch the wording, or the agent cannot reach the rung that asks for PROVEN.
func TestLos38k_IdleGuardReportedPhaseSixGetsTheSixPhaseQuestion(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	const id = "los38k-phase-report"

	first, relays := driveLos38kIntoProgression(t, h, s, id,
		"### Phase 3: EXECUTE\n**Status:** IN PROGRESS\nMilestone 1 laeuft")
	if strings.Contains(strings.ToLower(first), "completed all 7 phases") {
		t.Fatalf("precondition: the first delivery was already the six-phase question: %s", first)
	}

	// The report itself is work, so that tick sends nothing; the delivery after it
	// decides the wording from the reported phase.
	h.handleUpdateAgentStatus(map[string]any{"id": id, "phase": "Phase 6/6 FINISH"})
	expireIdleRelay(id)
	h.checkYesloopIdle()
	if got := los38kAwaitRelay(t, relays); got != "" {
		t.Fatalf("a phase report must not be answered with a relay, got: %s", got)
	}

	expireIdleRelay(id)
	h.checkYesloopIdle()
	body := los38kAwaitRelay(t, relays)
	if !strings.Contains(body, idleRelayMessage(1)) {
		t.Errorf("an agent reporting Phase 6/6 must get relay 1 verbatim, got: %s", body)
	}
}
