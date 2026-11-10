package daemon

import (
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- State machine for yesloop agent DONE-claim verification ---
//
// Layer 3 of the yesloop guarantee. When a yesloop agent emits a DONE-claim
// (Phase 6 header, send_to DONE, Status: COMPLETE on Phase 6), the heartbeat
// asks the agent to prove all 7 phases ran — especially Phase 5 Cold Review and the
// accepted pre-check in Phase 6.
// The agent must reply with a BEWEISEN marker plus send_to evidence; otherwise
// the state machine re-fires the relay up to yesloopDoneVerifyMaxRefires times,
// then nudges the orchestrator once and lets the agent run. The pause is not this
// layer's to set: a DONE is decided by the seven-phase gate at the DONE itself.
//
// Trigger: DONE-claim in scratchpad (not stream inactivity — that's yesloop_idle).
// States and helpers are intentionally modeled after yesloop_idle.go.

const (
	yesloopDoneVerifyStateNotDone = iota
	yesloopDoneVerifyStateVerifyRequested
	yesloopDoneVerifyStateDoneVerified
	yesloopDoneVerifyStateNudged
)

var yesloopDoneVerifyStateNames = map[int]string{
	yesloopDoneVerifyStateNotDone:         "NOT_DONE",
	yesloopDoneVerifyStateVerifyRequested: "VERIFY_REQUESTED",
	yesloopDoneVerifyStateDoneVerified:    "DONE_VERIFIED",
	yesloopDoneVerifyStateNudged:          "NUDGED",
}

const (
	yesloopDoneVerifyMaxRefires     = 3
	yesloopDoneVerifyRefireInterval = 5 * time.Minute
)

// doneVerifyRelayMessage is the single source of truth for the verify relay.
// Metachar-free (no markdown, no backticks, no parens) because it is written
// as a single line to the agent's PTY inject socket. Package-level so tests
// can assert on the content without capturing the socket write.
const doneVerifyRelayMessage = "Hast du wirklich alle Phasen abgehakt. Vor allem Phase 5 Code Review inklusive Security Review via security-review Skill. Im Phase 5 Block muessen die Zeilen self-review: done, cold-review: ses ID, consequence: ses ID und security: ses ID stehen — je ein eigener task subagent, der Daemon prueft die IDs gegen die opencode DB. Phase 4 braucht eine Regression baseline. Falls nicht bitte durchfuehren und BEWEISEN dass alle Phasen durch sind und DANN Phase 7 Finish durchfuehren mit commit und send_to. KEIN auto-deploy."

// yesloopDoneVerifyState tracks the verify state machine for a single agent.
type yesloopDoneVerifyState struct {
	state          int
	refireCount    int
	lastRelayAt    time.Time
	transitionedAt time.Time
	// createdAt identifies the row the verdict belongs to. Since YM-151 the
	// allocator never reuses a number, so this binding is defense in depth: a
	// hand-made or restored row that lands on an id this map still carries must
	// not inherit its verdict — both guard layers would stay silent for it
	// (review round 3).
	createdAt string
	// subagentFrozenAt is when the freeze was last reported, so a frozen guard
	// reports itself on a clock instead of once per tick.
	subagentFrozenAt time.Time
}

var (
	// yesloopDoneVerifyAgents is pruned against the live agent rows on every sweep
	// (pruneDoneVerifyStates): an entry is only added for an agent that emitted a
	// DONE-claim, and a row that no longer exists loses it. The map used to grow by
	// one small struct per DONE-claim until the daemon restarted (YM-203).
	yesloopDoneVerifyAgents   = make(map[string]*yesloopDoneVerifyState)
	yesloopDoneVerifyAgentsMu sync.Mutex
)

// DONE-claim indicators are split into two groups:
//   - structural indicators are matched against the full scratchpad content.
//     They are anchors that don't false-positive on body text.
//   - text indicators are matched against the LAST phase block that exists.
//     They would false-positive on briefing/template text elsewhere (e.g. an
//     agent's Phase 1 notes the instruction "send_to DONE when complete"). #82125.
//
// The legacy pair (`Phase 6: FINISH`, `Phase 6/6`) is deliberate: a loop that
// started before the verification became its own phase writes six blocks, and
// its sixth is the handoff. Anchoring only on Phase 7 would leave exactly those
// loops outside the contract, with nothing to bounce them.
var doneClaimIndicatorsStructural = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^###\s+Phase\s+7\s*:`),
	regexp.MustCompile(`(?im)Phase\s+7\s*/\s*7\b`),
	regexp.MustCompile(`(?im)^###\s+Phase\s+6\s*:\s*FINISH`),
	regexp.MustCompile(`(?im)Phase\s+6\s*/\s*6\b`),
}

var doneClaimIndicatorsText = []*regexp.Regexp{
	regexp.MustCompile(`(?im)send_to[^\n]*\bDONE\b`),
	regexp.MustCompile(`(?im)^DONE:\s`),
}

// hasDoneClaim reports whether content carries any DONE-claim indicator.
// Structural indicators are matched against full content. Text indicators are
// scoped to the handoff blocks (Phase 7, or the legacy Phase 6): the handoff is
// the source of truth for a real DONE claim, while earlier phases mention the
// word only as an instruction (#82125).
func hasDoneClaim(content string) bool {
	for _, re := range doneClaimIndicatorsStructural {
		if re.MatchString(content) {
			return true
		}
	}
	phases := splitPhases(content)
	for _, number := range []int{donePhaseCount - 1, donePhaseCount} {
		block, ok := phases[number]
		if !ok {
			continue
		}
		for _, re := range doneClaimIndicatorsText {
			if re.MatchString(block) {
				return true
			}
		}
	}
	return false
}

var bewiesenMarkerRe = regexp.MustCompile(`(?i)\bBEWEISEN\b`)

// hasBewiesenMarker reports whether the agent has asserted BEWEISEN in the
// scratchpad. Case-insensitive per spec. Word-boundary match prevents
// false positives like "aufBEWEISEN".
func hasBewiesenMarker(content string) bool {
	return bewiesenMarkerRe.MatchString(content)
}

// hasSendToOrchestrator reports whether the content contains a
// "send_to orchestrator:" line, which is the Phase-6 completion evidence.
func hasSendToOrchestrator(content string) bool {
	return strings.Contains(content, "send_to orchestrator:")
}

// isVerified reports whether the agent has produced full DONE evidence:
// BEWEISEN marker AND send_to orchestrator line AND 7 phases COMPLETE.
func isVerified(content string) bool {
	return hasBewiesenMarker(content) &&
		hasSendToOrchestrator(content) &&
		CountCompletedPhases(content) >= donePhaseCount
}

// checkYesloopDoneVerify is the heartbeat-driven DONE-claim verifier for
// yesloop agents. Runs every 5min via startAgentHeartbeat.
func (h *Handler) checkYesloopDoneVerify() {
	agents, err := h.store.AgentList("")
	if err != nil {
		log.Printf("[yesloop-done-verify] AgentList error: %v", err)
		return
	}
	for _, agent := range agents {
		h.checkOneDoneVerify(agent)
	}
	h.pruneDoneVerifyStates(agents)
}

// pruneDoneVerifyStates drops the bookkeeping of agents that no longer have a
// row. The map is in-memory; before YM-203 it kept one entry per agent that had
// ever emitted a DONE-claim until the daemon restarted. A row that still exists
// keeps its verdict — doneVerifySettled (idle guard) reads it, and the idle layer
// must stay silent for an agent whose completion this layer verified.
func (h *Handler) pruneDoneVerifyStates(agents []storage.Agent) {
	live := make(map[string]struct{}, len(agents))
	for i := range agents {
		live[agents[i].ID] = struct{}{}
	}
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	for id := range yesloopDoneVerifyAgents {
		if _, ok := live[id]; !ok {
			delete(yesloopDoneVerifyAgents, id)
		}
	}
}

// checkOneDoneVerify drives a single agent through the verify state machine.
func (h *Handler) checkOneDoneVerify(agent storage.Agent) {
	if agent.Status != "running" {
		return
	}
	if !strings.HasPrefix(agent.Section, "yesloop-") {
		return
	}
	if agent.SessionID == "" || agent.Project == "" {
		return
	}

	content := h.readAgentScratchpad(agent)
	claim := content != "" && hasDoneClaim(content)
	// Only an agent this layer tracks or one that just claimed DONE can act here;
	// every other running yesloop agent would pay for the child lookup every tick
	// (two sqlite3 spawns) without a decision hanging on it.
	if !claim && !doneVerifyHasState(agent.ID) {
		return
	}
	// An opencode agent whose session id is not bound yet has no child signal at
	// all; the on-demand bind is rate-limited per agent.
	agent = *h.ensureOpencodeSession(&agent)
	// Read before the lock: the child lookup hits opencode's session table.
	inSubagent := h.agentWaitingInSubagent(agent)

	action, reason := h.advanceDoneVerify(agent, content, claim, inSubagent)

	// The state mutex is released again; everything below is I/O — a daemon
	// send_to that pushes into an opencode inbox, a pause write, or a dial the
	// agent's socket may not answer for seconds. Holding the map mutex across
	// those stalls every other agent's tick (YM-344).
	switch action {
	case doneVerifyActionVerified:
		log.Printf("[yesloop-done-verify] agent %s (%s) verified DONE",
			safeAgentText(agent.ID), safeAgentText(agent.Section))
		h.notifyOrchestrator(agent, fmt.Sprintf(
			"Agent %s (%s) verified DONE — BEWEISEN + send_to + 7 phases COMPLETE",
			safeAgentText(agent.ID), safeAgentText(agent.Section)))

	case doneVerifyActionRelay:
		h.sendDoneVerifyRelay(agent)

	case doneVerifyActionNudge:
		// The layer reports once and lets go: a DONE is decided by the
		// seven-phase gate that runs at the DONE itself, not by this ladder.
		if !h.nudgeAgent(agent.ID, fmt.Sprintf("yesloop-done-verify escalation: %s", reason)) {
			skipNudgeNote("yesloop-done-verify", agent.ID, agent.Section)
			return
		}
		h.notifyOrchestrator(agent, nudgeNotice(fmt.Sprintf(
			"Agent %s (%s) claimed DONE without proof and its relay ladder ran out: %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(reason))))
	}
}

// doneVerifyAction is the I/O one tick owes the agent. The state machine decides
// it under the mutex; the caller runs it after the mutex is released.
type doneVerifyAction int

const (
	doneVerifyActionNone doneVerifyAction = iota
	doneVerifyActionRelay
	doneVerifyActionNudge
	doneVerifyActionVerified
)

// advanceDoneVerify drives the verify state machine for one agent and returns the
// action the caller must carry out. Everything that touches the network or
// another handler stays out of this function: the mutex it holds is the one
// every other agent's tick needs.
func (h *Handler) advanceDoneVerify(agent storage.Agent, content string, claim, inSubagent bool) (doneVerifyAction, string) {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()

	state, exists := yesloopDoneVerifyAgents[agent.ID]
	if !exists {
		// No state yet. Only start tracking when a DONE-claim appears.
		if !claim {
			return doneVerifyActionNone, ""
		}
		state = &yesloopDoneVerifyState{state: yesloopDoneVerifyStateNotDone, createdAt: agent.CreatedAt}
		yesloopDoneVerifyAgents[agent.ID] = state
	}

	// Terminal verdicts need no relay: reporting a freeze for them would point a
	// log reader at an agent this layer has nothing left to say to.
	if state.state == yesloopDoneVerifyStateDoneVerified ||
		state.state == yesloopDoneVerifyStateNudged {
		return doneVerifyActionNone, ""
	}

	// Settlement is bookkeeping, not a relay: an agent that already proved its
	// completion must not wait for a child it is no longer waiting on.
	if state.state == yesloopDoneVerifyStateVerifyRequested && isVerified(content) {
		state.subagentFrozenAt = time.Time{}
		h.transitionDoneVerify(agent, state, yesloopDoneVerifyStateDoneVerified)
		return doneVerifyActionVerified, ""
	}

	// A relay into an agent blocked inside a task() subagent only queues as a
	// pending user message and hits it after the subagent returns (#89428). No
	// transition, no refire and no escalation while the child runs; the relay
	// interval restarts when the child ends.
	if inSubagent {
		if now := time.Now(); now.Sub(state.subagentFrozenAt) >= subagentFreezeLogEvery {
			log.Printf("[yesloop-done-verify] agent %s (%s) frozen: a task() subagent is active",
				safeAgentText(agent.ID), safeAgentText(agent.Section))
			state.subagentFrozenAt = now
		}
		state.lastRelayAt = time.Now()
		return doneVerifyActionNone, ""
	}
	state.subagentFrozenAt = time.Time{}

	switch state.state {
	case yesloopDoneVerifyStateNotDone:
		if claim {
			h.transitionDoneVerify(agent, state, yesloopDoneVerifyStateVerifyRequested)
			// Stamped here rather than in the relay: a failed dial still counts as
			// a relay attempt for the refire gate, and the caller must not write
			// the state without the mutex.
			state.lastRelayAt = time.Now()
			return doneVerifyActionRelay, ""
		}

	case yesloopDoneVerifyStateVerifyRequested:
		return h.refireOrNudge(agent, state)
	}
	return doneVerifyActionNone, ""
}

// refireOrNudge advances the refire ladder. Called with the state mutex held;
// the caller performs the returned action after releasing it.
func (h *Handler) refireOrNudge(agent storage.Agent, state *yesloopDoneVerifyState) (doneVerifyAction, string) {
	const reason = "no BEWEISEN/send_to/7-phases evidence"
	if time.Since(state.lastRelayAt) < yesloopDoneVerifyRefireInterval {
		return doneVerifyActionNone, ""
	}
	state.refireCount++
	if state.refireCount >= yesloopDoneVerifyMaxRefires {
		log.Printf("[yesloop-done-verify] agent %s (%s) ESCALATION: %s (refireCount=%d, max=%d)",
			safeAgentText(agent.ID), safeAgentText(agent.Section), reason, state.refireCount, yesloopDoneVerifyMaxRefires)
		state.state = yesloopDoneVerifyStateNudged
		state.transitionedAt = time.Now()
		return doneVerifyActionNudge, reason
	}
	state.lastRelayAt = time.Now()
	return doneVerifyActionRelay, ""
}

// doneVerifyHasState reports whether the layer already holds state for this
// agent. The child lookup and the scratchpad-driven transitions only matter for
// a tracked agent or one that just claimed DONE.
func doneVerifyHasState(agentID string) bool {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	_, ok := yesloopDoneVerifyAgents[agentID]
	return ok
}

// doneVerifySettled reports whether the done-verify layer has already reached a
// verdict for this agent: DONE_VERIFIED means the agent proved its completion, and
// the idle layer must not relay into it — two layers of one daemon contradicting
// each other was the reported symptom (YM-149: DONE_VERIFIED at 23:36, idle relays
// from 23:42).
//
// The nudge state does not settle anything. It says "claimed DONE without proof",
// and an agent that then goes quiet is exactly what the idle ladder is for, so
// that layer keeps watching instead of joining the silence.
//
// The verdict is bound to the row it was reached for: a new agent must not
// inherit a silence it never earned. Since YM-151 the allocator does not reuse
// ids, so the createdAt comparison is defense in depth rather than the only
// guard.
func doneVerifySettled(agent storage.Agent) bool {
	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()
	st, ok := yesloopDoneVerifyAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt {
		return false
	}
	return st.state == yesloopDoneVerifyStateDoneVerified
}

// transitionDoneVerify moves the agent to a new verify state.
func (h *Handler) transitionDoneVerify(agent storage.Agent, state *yesloopDoneVerifyState, newState int) {
	oldName := yesloopDoneVerifyStateNames[state.state]
	newName := yesloopDoneVerifyStateNames[newState]
	log.Printf("[yesloop-done-verify] agent %s (%s) state %s -> %s",
		safeAgentText(agent.ID), safeAgentText(agent.Section), oldName, newName)
	state.state = newState
	state.refireCount = 0
	state.transitionedAt = time.Now()
}

// doneVerifyDial is the dial the relay uses. A variable so a test can hold the
// dial open and observe what the guard does while it waits — the wait itself is
// the reason the caller must not hold the state mutex.
var doneVerifyDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, timeout)
}

// sendDoneVerifyRelay sends the verify relay to a yesloop agent via the inject
// socket. Single metachar-free write (no markdown, no backticks, no parens).
// The caller stamps state.lastRelayAt before calling: it owns the state mutex,
// and this function runs with the mutex released.
func (h *Handler) sendDoneVerifyRelay(agent storage.Agent) {
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[yesloop-done-verify] relay to agent %s skipped: no derivable socket path", safeAgentText(agent.ID))
		return
	}
	wrapped := fmt.Sprintf("[RELAY from=yesloop-done-verify] %s", doneVerifyRelayMessage)

	conn, err := doneVerifyDial("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[yesloop-done-verify] relay to agent %s failed: %v", safeAgentText(agent.ID), safeAgentText(err.Error()))
		return
	}
	defer conn.Close()

	conn.Write([]byte(wrapped + "\r\n"))
}
