package daemon

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- YM-253: idle-guard remainder from the 16.10. audit ---
//
// Four defects, one test each:
//  1. the guard relays into an agent that is blocked inside a task() subagent,
//  2. it holds the idle-state mutex across session reads and relays,
//  3. every relay buys the agent a new window, without a ceiling (anti-flap),
//  4. relay_agent sleeps three seconds on the caller's path before it submits.

// ym253InjectSink is an inject socket that records one payload per connection,
// so a test can count relays and inspect the content/submit split.
type ym253InjectSink struct {
	mu       sync.Mutex
	payloads []string
}

func newYM253InjectSink(t *testing.T, h *Handler, id string) (*ym253InjectSink, string) {
	t.Helper()
	dir := shortSockDir(t)
	h.dataDir = dir
	sockPath := filepath.Join(dir, id+".sock")
	ln, err := net.Listen("unix", sockPath+".inject")
	if err != nil {
		t.Fatalf("listen inject: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	sink := &ym253InjectSink{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			data, _ := io.ReadAll(conn)
			conn.Close()
			sink.mu.Lock()
			sink.payloads = append(sink.payloads, string(data))
			sink.mu.Unlock()
		}
	}()
	return sink, sockPath
}

func (s *ym253InjectSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payloads)
}

func (s *ym253InjectSink) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.payloads))
	copy(out, s.payloads)
	return out
}

// waitCount returns the payload count, polling until it reaches want or the
// timeout passes: the sink records on its own goroutine, so a count read right
// after a relay is a race against the accept loop.
func (s *ym253InjectSink) waitCount(want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		if n := s.count(); n >= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestIdleGuard_YM253_SubagentIsNotRelayed: the audit's first finding. A relay
// into an agent that sits inside a task() subagent queues as a pending user
// message and hits it afterwards (#89428), so the guard must freeze — the same
// skip yesloop_broken_toolcall.go already makes.
func TestIdleGuard_YM253_SubagentIsNotRelayed(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveBoundYesloopIntoProgression(t, h, s, "ym253-sub")

	stubOpencodeChildActive(t, true)
	expireIdleRelay("ym253-sub")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("ym253-sub"); got != 0 {
		t.Fatalf("a relay was counted while a subagent was active (refireCount=%d)", got)
	}
	if !hasIdleState("ym253-sub", yesloopIdleStateSelfCheck) {
		t.Error("the agent must stay in the rung it is answering, not advance or reset")
	}
}

// TestIdleGuard_YM253_SubagentStreamMarkerIsTheOnlySignal: the second half of
// the first finding. A claude-backed agent has no session source at all (no
// opencode session id, no child rows), so the only trace of its task() subagent
// is the proxy's stream marker on the agent's own thread. The guard must freeze
// on that marker too.
func TestIdleGuard_YM253_SubagentStreamMarkerIsTheOnlySignal(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-claude")

	// The thread's newest stream belonged to a subagent and ended recently: the
	// agent is waiting inside task(), which has no session row to consult.
	streamStatesMu.Lock()
	st, ok := streamStates["sess-ym253-claude"]
	if !ok {
		streamStatesMu.Unlock()
		t.Fatal("precondition: the fixture must register a stream state for the agent's thread")
	}
	st.mu.Lock()
	st.IsSubagent = true
	st.StartedAt = time.Now().Add(-2 * time.Minute)
	st.mu.Unlock()
	streamStatesMu.Unlock()

	expireIdleRelay("ym253-claude")
	h.checkYesloopIdle()

	if got := getIdleStateRefireCount("ym253-claude"); got != 0 {
		t.Fatalf("a relay was counted while the only signal said subagent (refireCount=%d)", got)
	}
	if !hasIdleState("ym253-claude", yesloopIdleStateSelfCheck) {
		t.Error("the agent must stay in the rung it is answering")
	}
}

// TestIdleGuard_YM253_SessionReadHoldsNoLock: the audit's second finding. The
// idle-state mutex is taken by the relay path as well, so holding it across the
// session read (up to 1s) and the inject dial blocks unrelated work. While the
// read is in flight the lock must be free.
func TestIdleGuard_YM253_SessionReadHoldsNoLock(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		time.Sleep(600 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "cursor": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	stubV2Service(t, srv)

	driveBoundYesloopIntoProgression(t, h, s, "ym253-lock")

	sweepDone := make(chan struct{})
	go func() {
		h.checkYesloopIdle()
		close(sweepDone)
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the guard never read the session")
	}

	start := time.Now()
	yesloopIdleAgentsMu.Lock()
	waited := time.Since(start)
	yesloopIdleAgentsMu.Unlock()

	<-sweepDone

	if waited > 150*time.Millisecond {
		t.Fatalf("the idle-state mutex was held for %v while the session read ran; reads and relays must happen outside it", waited)
	}
}

// TestIdleGuard_YM253_RelayBudgetStopsWithoutPause: the audit's third finding.
// Work detection resets the refire counter, so an agent that keeps producing
// something can be relayed forever — each relay re-arms its window. A hard
// ceiling must end the protocol, and it must not pause an agent that may well
// be working.
func TestIdleGuard_YM253_RelayBudgetStopsWithoutPause(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-budget")

	sink, _ := newYM253InjectSink(t, h, "ym253-budget")

	// Each round: work (which restarts the refire count and consumes no relay),
	// then an expired interval (which sends one). Without a ceiling this loop
	// relays every single round, indefinitely.
	for i := 0; i < yesloopIdleMaxRelays+3; i++ {
		addTurns(t, s, "ym253-budget", 5)
		h.checkYesloopIdle()
		expireIdleRelay("ym253-budget")
		h.checkYesloopIdle()
	}

	if got := sink.waitCount(yesloopIdleMaxRelays, time.Second); got != yesloopIdleMaxRelays {
		t.Errorf("relays sent = %d, want the ceiling of %d", got, yesloopIdleMaxRelays)
	}
	agent, err := s.AgentGet("ym253-budget")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "running" {
		t.Errorf("the guard must stand down without pausing, got status %q (%s)", agent.Status, agent.Progress)
	}
	if !hasIdleState("ym253-budget", yesloopIdleStateDone) {
		t.Error("the protocol must be closed after the ceiling")
	}
}

// TestRelayAgent_YM253_NoSubmitDelayOnCallerPath: the audit's fourth finding.
// The two-connection contract (content, then the submit key) stays; the three
// second wait between them must not run on the caller's path.
func TestRelayAgent_YM253_NoSubmitDelayOnCallerPath(t *testing.T) {
	h, s := mustHandler(t)
	sink, _ := newYM253InjectSink(t, h, "ym253-relay")

	s.AgentCreate(storage.Agent{
		ID: "ym253-relay", Project: "p", Section: "s",
		Status: "running", Backend: "claude",
	})

	start := time.Now()
	resp := h.handleRelayAgent(map[string]any{
		"to":      "ym253-relay",
		"content": "approve ym253",
	})
	elapsed := time.Since(start)
	if resp.Error != "" {
		t.Fatalf("relay failed: %s", resp.Error)
	}
	if elapsed > time.Second {
		t.Fatalf("relay_agent took %v; the submit delay must not block the caller", elapsed)
	}

	deadline := time.Now().Add(relaySubmitDelay + 2*time.Second)
	for sink.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	payloads := sink.all()
	if len(payloads) != 2 {
		t.Fatalf("inject connections = %d, want 2 (content, then submit): %q", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], "approve ym253") {
		t.Errorf("first connection must carry the content, got %q", payloads[0])
	}
	if strings.TrimSpace(payloads[1]) != "" {
		t.Errorf("second connection must carry only the submit key, got %q", payloads[1])
	}
}

// TestIdleGuard_YM253_StreamActivityStillRearmsWhileASubagentRuns: the freeze
// must not swallow the stream-active transitions. A child session can be fresh
// while the parent's own stream is alive; in that window the DONE re-arm of
// YM-149 has to run, otherwise an agent that went back to work after finishing
// the protocol is never supervised again.
func TestIdleGuard_YM253_StreamActivityStillRearmsWhileASubagentRuns(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-rearm")

	stubOpencodeChildActive(t, true)
	streamStatesMu.Lock()
	st := streamStates["sess-ym253-rearm"]
	st.mu.Lock()
	st.Active = true
	st.mu.Unlock()
	streamStatesMu.Unlock()

	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["ym253-rearm"].state = yesloopIdleStateDone
	yesloopIdleAgentsMu.Unlock()

	h.checkYesloopIdle()

	if hasIdleState("ym253-rearm", yesloopIdleStateDone) {
		t.Error("a live stream must re-arm the protocol even while a child session is active")
	}
}

// TestIdleGuard_YM253_BudgetSurvivesAProtocolReset: an orchestrator relay answers
// an escalation, so it resets the protocol. It must not hand the guard a fresh
// relay budget, or the flap the ceiling exists for comes back through that door.
func TestIdleGuard_YM253_BudgetSurvivesAProtocolReset(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-reset")

	sink, _ := newYM253InjectSink(t, h, "ym253-reset")
	for i := 0; i < yesloopIdleMaxRelays+1; i++ {
		addTurns(t, s, "ym253-reset", 5)
		h.checkYesloopIdle()
		expireIdleRelay("ym253-reset")
		h.checkYesloopIdle()
	}
	if got := sink.waitCount(yesloopIdleMaxRelays, time.Second); got != yesloopIdleMaxRelays {
		t.Fatalf("precondition: relays sent = %d, want %d", got, yesloopIdleMaxRelays)
	}

	resetIdleProtocol("ym253-reset")

	yesloopIdleAgentsMu.Lock()
	_, kept := yesloopIdleRelays["ym253-reset"]
	yesloopIdleAgentsMu.Unlock()
	if !kept {
		t.Fatal("the relay budget must survive a protocol reset")
	}

	h.checkYesloopIdle()
	yesloopIdleAgentsMu.Lock()
	if st, ok := yesloopIdleAgents["ym253-reset"]; ok {
		st.idleSince = time.Now().Add(-11 * time.Minute)
	}
	yesloopIdleAgentsMu.Unlock()
	h.checkYesloopIdle()

	if got := sink.waitCount(yesloopIdleMaxRelays+1, 300*time.Millisecond); got != yesloopIdleMaxRelays {
		t.Errorf("a protocol reset re-opened the budget: relays sent = %d, want %d", got, yesloopIdleMaxRelays)
	}
}

// TestIdleGuard_YM253_BudgetWindowRecovers: the ceiling is per window, not per
// agent lifetime. An agent that burned its budget an hour ago must be relayed
// again instead of losing its supervisor for good.
func TestIdleGuard_YM253_BudgetWindowRecovers(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-window")

	sink, _ := newYM253InjectSink(t, h, "ym253-window")

	yesloopIdleAgentsMu.Lock()
	yesloopIdleRelays["ym253-window"] = &idleRelayBudget{
		count: yesloopIdleMaxRelays,
		since: time.Now().Add(-2 * yesloopIdleRelayBudgetWindow),
	}
	yesloopIdleAgentsMu.Unlock()

	expireIdleRelay("ym253-window")
	h.checkYesloopIdle()

	if got := sink.waitCount(1, time.Second); got != 1 {
		t.Errorf("relays sent = %d, want 1: an expired budget window must hand out a fresh budget", got)
	}
}

// TestIdleGuard_YM253_StandDownEndsWithItsWindow: the stand-down at the relay
// ceiling must not be permanent. An idle agent's stream may never become active
// again, so waiting for that would leave it unsupervised for good; the budget
// window is what ends the stand-down.
func TestIdleGuard_YM253_StandDownEndsWithItsWindow(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-standdown")

	yesloopIdleAgentsMu.Lock()
	yesloopIdleRelays["ym253-standdown"] = &idleRelayBudget{count: yesloopIdleMaxRelays, since: time.Now()}
	yesloopIdleAgentsMu.Unlock()

	h.checkYesloopIdle()
	if !hasIdleState("ym253-standdown", yesloopIdleStateDone) {
		t.Fatal("precondition: an exhausted budget must close the protocol")
	}

	yesloopIdleAgentsMu.Lock()
	yesloopIdleRelays["ym253-standdown"].since = time.Now().Add(-2 * yesloopIdleRelayBudgetWindow)
	yesloopIdleAgentsMu.Unlock()

	h.checkYesloopIdle()

	if !hasIdleState("ym253-standdown", yesloopIdleStateWorking) {
		t.Error("the stand-down must end when its budget window has passed, even without stream activity")
	}
}

// TestIdleGuard_YM253_DeferredWhileTheSocketIsBusy: a sweep shares the heartbeat
// goroutine, so a socket that is inside another delivery's submit window must
// defer the guard relay instead of waiting it out.
func TestIdleGuard_YM253_DeferredWhileTheSocketIsBusy(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-busy")

	sink, sockPath := newYM253InjectSink(t, h, "ym253-busy")

	unlock, ok := tryLockRelayPath(sockPath + ".inject")
	if !ok {
		t.Fatal("precondition: the delivery lock must be free")
	}

	expireIdleRelay("ym253-busy")
	done := make(chan struct{})
	go func() {
		h.checkYesloopIdle()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep waited for a busy delivery socket instead of deferring the relay")
	}

	if got := sink.count(); got != 0 {
		t.Errorf("relays sent = %d, want 0: a busy socket defers the relay to the next tick", got)
	}
	if got := getIdleStateRefireCount("ym253-busy"); got != 0 {
		t.Errorf("a deferred relay burned a refire slot (refireCount=%d)", got)
	}

	// The deferral is a postponement, not a drop: once the socket is free, the
	// next sweep delivers the relay.
	unlock()
	expireIdleRelay("ym253-busy")
	h.checkYesloopIdle()
	if got := sink.waitCount(1, time.Second); got != 1 {
		t.Errorf("relays sent after the socket freed = %d, want 1", got)
	}
}

// TestIdleGuard_YM253_EscalationSkipsWhenTheProtocolWasReset: if the orchestrator
// answered the escalation while the decision was being executed, the protocol is
// already gone. A notice for an answered escalation would be noise, so nothing is
// written and nothing is sent.
func TestIdleGuard_YM253_EscalationSkipsWhenTheProtocolWasReset(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	driveYesloopIntoProgression(t, h, s, "ym253-esc")

	resetIdleProtocol("ym253-esc")

	h.runIdleAction(storage.Agent{
		ID: "ym253-esc", Project: "testproj", Section: "yesloop-ym253-esc",
		CallerSession: "caller-ym253-esc",
	}, idleAction{escalationReason: "no completion evidence"})

	agent, err := s.AgentGet("ym253-esc")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.Status != "running" {
		t.Errorf("an escalation must not stop an agent whose protocol was reset, status = %q", agent.Status)
	}
	if strings.HasPrefix(agent.Progress, guardNudgeProgressPrefix) {
		t.Errorf("an answered escalation must leave no note, progress=%q", agent.Progress)
	}
	msgs, err := s.GetChannelMessages([]string{"caller-ym253-esc"})
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, guardNudgeNoticePrefix) {
			t.Errorf("an answered escalation must send no notice, got %q", m.Content)
		}
	}
}
