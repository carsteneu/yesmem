package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/opencode"
	"github.com/carsteneu/yesmem/internal/storage"
)

// --- State machine for yesloop agent idle detection ---
//
// The idle detection detects yesloop agents whose SSE stream has been inactive
// for >10min but whose PID is still alive. It drives them through a 4-state
// escalation protocol to force self-check, completion, and commit.

const (
	yesloopIdleStateWorking = iota
	yesloopIdleStateSelfCheck
	yesloopIdleStateRemarkRequest
	yesloopIdleStateCommitRequest
	yesloopIdleStateDone
)

var stateNames = map[int]string{
	yesloopIdleStateWorking:       "WORKING",
	yesloopIdleStateSelfCheck:     "SELF_CHECK",
	yesloopIdleStateRemarkRequest: "REMARK_REQUEST",
	yesloopIdleStateCommitRequest: "COMMIT_REQUEST",
	yesloopIdleStateDone:          "DONE",
}

const (
	yesloopIdleTimeout        = 10 * time.Minute
	yesloopIdleMaxRefires     = 2
	yesloopIdleRefireInterval = 90 * time.Second
	// yesloopIdleProgressTurns is how many turns (or completed assistant
	// messages) an agent has to add after a relay before the guard reads it as
	// work. The bar sits above the answer's own request chain: turns_used counts
	// LLM requests, so one "yes, I'll continue" that calls a tool is already two,
	// and an agent that stops there is hanging, not working (#82787). An agent
	// producing between milestones passes it easily; one long tool call is caught
	// by the open-turn check instead.
	yesloopIdleProgressTurns = 4
	// yesloopIdleMessageTimeout bounds the session read in the progress check.
	// The guard holds the idle-state lock while it runs, so it stays short.
	yesloopIdleMessageTimeout = 1 * time.Second
	// yesloopIdleWorkingMax bounds how long an unfinished turn counts as work. A
	// tool that runs for minutes (a test suite, a cold review subagent) is work,
	// but a turn that never finishes after this long is not evidence any more and
	// the ladder takes the agent back (#79819).
	yesloopIdleWorkingMax = 20 * time.Minute
	// yesloopIdleBackgroundMax bounds the same signal for a background job. A job
	// legitimately runs longer than a single turn: during the 5.3 review of this
	// ticket, 15 percent of the jobs measured on this install had run past ten
	// minutes, 3 percent past twenty, the longest 70. The job's tool metadata keeps
	// saying "running" after the job finished, so only an age bound ends the
	// signal — and the age is the newest turn's start, which is therefore not
	// evidence that the agent is still waiting for it either.
	yesloopIdleBackgroundMax = 90 * time.Minute
	// yesloopIdleTurnEpisodeMax bounds the open-turn signal per supervision
	// episode, the way yesloopIdleBackgroundMax bounds the job credit. Each turn
	// gets its own yesloopIdleWorkingMax window (two turns in a row must not share
	// one budget), but a streak of open turns that never settles must not renew
	// the signal and reset the ladder forever: measured tool runs stay well under
	// yesloopIdleWorkingMax, so a streak of this length produced no completed turn
	// at all (5.2/5.3 review). A read that reports a settled turn ends the streak.
	yesloopIdleTurnEpisodeMax = 40 * time.Minute
	// yesloopIdleMarker starts the note an idle escalation leaves. It lets the
	// relay path recognise the pause the old code wrote with the same marker:
	// relaying to such a row is the orchestrator's answer to that escalation
	// (#82787's counterpart, YM-225), and those rows have no other release.
	yesloopIdleMarker = "yesloop-idle escalation:"
	// yesloopIdleMaxRelays is the ceiling on relays per budget window. Refires are
	// counted per rung and restarted whenever the agent shows work, so without a
	// ceiling an agent that keeps producing something is relayed for as long as
	// it lives (each relay re-arms its window). The number is the protocol's own
	// worst case: three rungs, each with one relay and two refires.
	yesloopIdleMaxRelays = 9
	// yesloopIdleRelayBudgetWindow is how long a budget lasts. A window rather
	// than the agent's whole life: an agent that completes one task per hour for
	// days must not lose its supervisor after the ninth relay.
	yesloopIdleRelayBudgetWindow = time.Hour
)

// orchestratorPauseHint is appended to DEAD_AGENT escalation messages sent
// from the yesloop idle and done-verify layers to the orchestrator. Paused
// agents have a live PID and open PTY bridge — resume_agent is a no-op
// dressed up as action. The correct action is relay_agent with approval
// content so the agent picks up the relay on its next tick. Learning #81175.
const orchestratorPauseHint = " → use relay_agent with approval content, NOT resume_agent"

// yesloopIdleState tracks the idle state machine for a single yesloop agent.
type yesloopIdleState struct {
	state          int
	refireCount    int
	lastRelayAt    time.Time
	idleSince      time.Time
	transitionedAt time.Time
	// progressSig is the agent's own output (scratchpad, progress, phase) as it
	// stood when the protocol last moved on. A change means the agent answered
	// with work, which ends the protocol; without it the refires ran to the pause
	// over an agent that was demonstrably working (YM-207, live 2026-10-16).
	// Empty means "not armed yet" — a state a caller forced directly, or a
	// transition that predates this field — and is armed on the next look.
	progressSig string
	// progressTurns is the agent's turn counter when the same baseline was
	// taken. Scratchpad and phase go quiet for minutes in Phase 3/4 while the
	// agent produces tool turn after tool turn, so the counter is the signal
	// those states still send (YM-225, live 2026-10-16 22:45: pause of an agent
	// with turns_used=83 and a live PID).
	progressTurns int
	// progressAt bounds the session read: assistant turns completed after this
	// moment are new. It moves with the turn baseline — taken at a transition,
	// re-armed after detected work and moved again by every relay that is sent.
	// "Still working" deliberately does not hang off it: the guard's own relays
	// move it past the start of a tool call that is running right now (YM-306).
	progressAt time.Time
	// workingSince is when the guard first saw the newest turn working. It is the
	// absolute cap on that signal: whatever timestamp the service reports — absent,
	// far in the future, mis-scaled — one turn cannot count for longer than
	// yesloopIdleWorkingMax from here. The turn's own start is the second bound
	// (workingTurnCounts).
	workingSince time.Time
	// workingTurnStart is the start stamp of the turn workingSince bounds. The
	// bound belongs to one turn: a new working turn gets its own window, or two
	// turns in a row share a single 20-minute budget and the second one is
	// dropped as an aborted turn while it runs.
	workingTurnStart time.Time
	// turnEpisodeSince is when the current open-turn streak started. Working is
	// re-anchored per turn, so the streak needs its own credit: without it a
	// sequence of open turns that never completes would renew the signal and reset
	// the ladder forever. A read that reports a settled turn starts a new streak.
	turnEpisodeSince time.Time
	// backgroundSince is when the guard first credited a background job in this
	// supervision episode. The job credit is per episode, not per turn: an agent
	// that starts a cheap job as the last action of every turn would otherwise
	// keep the signal alive forever (5.4 review).
	backgroundSince time.Time
	// lastReadAt is the last moment the guard had a session answer: the last
	// successful read, or the start of the current rung. It bounds the abstention
	// on an unreadable session per episode — an agent must not get a fresh window
	// for every relay, and a failure after a readable stretch is its own episode.
	lastReadAt time.Time
	// relayUndeliverable records that the last relay attempt could not be
	// delivered — no socket, or a socket that refused the dial. The unreadable
	// hold grants silence so the agent can answer a relay; where no relay can
	// arrive there is nothing to answer, and the escalation text must say so. A
	// delivered relay clears the mark.
	relayUndeliverable bool
	// stoodDown records that the relay budget ended the protocol once. The state
	// survives the DONE re-arm, so the orchestrator hears about the stand-down
	// once per episode instead of once per idle cycle.
	stoodDown bool
	// subagentFrozenAt is when the freeze was last reported. It is a clock
	// rather than a once-per-episode flag because the mark outlives the layer's
	// own paths: a tick that returns at one of the early exits — a row that is no
	// longer running, a process that is gone, a verdict from the done-verify
	// layer, a broken tool call — never reaches the line that clears it, and a
	// flag then stays set for good. Every later episode, a real one with a real
	// child, reports nothing, which is exactly the state this guard's log line
	// exists to make visible (#89428). The clock bounds the silence instead: a
	// fresh episode reports itself as soon as the last report is older than
	// subagentFreezeLogEvery.
	subagentFrozenAt time.Time
}

// yesloopIdleAgents is the in-memory store for yesloop idle states.
// Reset on daemon restart — that is acceptable per spec.
var (
	yesloopIdleAgents   = make(map[string]*yesloopIdleState)
	yesloopIdleAgentsMu sync.Mutex
	// yesloopIdleRelays holds the relay budget per agent. It sits beside the state
	// instead of in it because resetIdleProtocol deletes the state: a relay from
	// the orchestrator answers an escalation and must not hand the guard a fresh
	// budget (YM-253, anti-flap).
	yesloopIdleRelays = make(map[string]*idleRelayBudget)
)

// idleRelayBudget caps how often the guard may relay one agent within one
// yesloopIdleRelayBudgetWindow. It refills when the window passes, so it bounds a
// flapping agent without ever switching a productive one off for good.
type idleRelayBudget struct {
	count int
	since time.Time
}

// resetYesloopIdleState clears the idle agent map. Used in tests.
func resetYesloopIdleState() {
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents = make(map[string]*yesloopIdleState)
	yesloopIdleRelays = make(map[string]*idleRelayBudget)
	yesloopIdleAgentsMu.Unlock()
}

// checkYesloopIdle is the heartbeat-driven idle detection for yesloop agents.
// Runs every 30s via h.startAgentHeartbeat. It detects agents that have been
// stream-inactive for >10min but still have a live PID, and drives them
// through the escalation protocol.
func (h *Handler) checkYesloopIdle() {
	agents, err := h.store.AgentList("")
	if err != nil {
		log.Printf("[yesloop-idle] AgentList error: %v", err)
		return
	}
	// Resolving the v2 service globs the registry and probes every candidate at a
	// one-second timeout each, so it happens at most once per sweep, lazily — only
	// when an agent actually has a session to read — and outside the idle-state
	// lock, which the relay path takes as well.
	var client *opencode.Client
	resolved := false
	for _, agent := range agents {
		if !resolved && agent.OpencodeSessionID != "" {
			client = opencodeV2Service()
			resolved = true
		}
		h.checkOneAgent(agent, client)
	}
	h.pruneIdleStates(agents)
}

// pruneIdleStates drops the bookkeeping of agents that no longer have a row. The
// map is in-memory and was never cleaned: every agent the daemon had ever seen
// kept an entry until the next daemon restart (YM-149). A row that still exists
// keeps its state — it can come back to running.
func (h *Handler) pruneIdleStates(agents []storage.Agent) {
	live := make(map[string]struct{}, len(agents))
	for i := range agents {
		live[agents[i].ID] = struct{}{}
	}
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	for id := range yesloopIdleAgents {
		if _, ok := live[id]; !ok {
			delete(yesloopIdleAgents, id)
		}
	}
	// The budget outlives resetIdleProtocol, so it has to be pruned on its own:
	// iterating the state map alone would leave one entry behind for every agent
	// whose state was reset (YM-253).
	for id := range yesloopIdleRelays {
		if _, ok := live[id]; !ok {
			delete(yesloopIdleRelays, id)
		}
	}
}

// provenMarkerRe matches the PROVEN marker the idle relay asks for. Word-bounded
// on purpose: "UNPROVEN" contains the substring, so a scratchpad that said its
// evidence was unproven advanced the protocol as if it were proof (YM-149).
var provenMarkerRe = regexp.MustCompile(`(?i)\bPROVEN\b`)

// hasProvenMarker reports whether the agent asserted the PROVEN marker.
func hasProvenMarker(content string) bool {
	return provenMarkerRe.MatchString(content)
}

// phaseDoneMarkRes matches the per-phase [x] the idle relay asks for. The relay
// says "mark each phase in scratchpad with [x]" and prescribes no line, so the
// forms a literal answer produces are accepted: a dedicated marker line inside the
// phase's block ("- [x]", "[x]", "- [x] Phase 1", "- [x] ANALYZE") or the phase's
// header line ending in the marker ("### Phase 1: ANALYZE [x]").
//
// The dedicated line keeps two failures out: a checklist item inside the block
// ("- [x] fixed the parser") carries text of its own and is not a phase mark (six
// incidental boxes passed for six completed phases — review round 1), while
// demanding the phase NUMBER on the marker's line was stricter than the relay that
// asks for it, and paused exactly the agents that did what they were told
// (review round 2).
var (
	phaseHeaderMarkRes = func() [donePhaseCount + 1]*regexp.Regexp {
		var out [donePhaseCount + 1]*regexp.Regexp
		for n := 1; n <= donePhaseCount; n++ {
			out[n] = regexp.MustCompile(fmt.Sprintf(`(?m)^#{1,6}[ \t]*(?i:phase)[ \t]*%d\b[^\n]*\[[xX]\][ \t]*$`, n))
		}
		return out
	}()
	// The phase reference is matched case-insensitively but has to BE a phase
	// reference: an earlier version accepted any word of three letters or more and
	// let "- [x] done" in a phase block count as that phase being done
	// (review round 3).
	dedicatedMarkRe = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+][ \t]+)?\[[xX]\][ \t]*(?:[-:][ \t]*)?(?:\b(?i:phase)[ \t]*\d+\b|\b(?i:` + phaseNamesAlternation() + `)\b)?[ \t]*$`)
)

// phaseNamesAlternation lists the phase names of the contract for the marker regex.
func phaseNamesAlternation() string {
	names := make([]string, 0, len(phaseValidations))
	for _, pv := range phaseValidations {
		if pv.Name != "" {
			names = append(names, regexp.QuoteMeta(pv.Name))
		}
	}
	return strings.Join(names, "|")
}

// phaseMarkedDoneByX reports whether phase n carries the [x] marker.
func phaseMarkedDoneByX(content string, n int) bool {
	if phaseHeaderMarkRes[n].MatchString(content) {
		return true
	}
	if block, ok := splitPhases(content)[n]; ok {
		return dedicatedMarkRe.MatchString(block)
	}
	return false
}

// countMarkedDonePhases counts the phases marked done in either form: the DONE
// contract's Status line, or the [x] the relay asks for.
func countMarkedDonePhases(content string) int {
	phases := splitPhases(content)
	count := 0
	for _, pv := range phaseValidations {
		if block, ok := phases[pv.Number]; ok && completedPhaseRe.MatchString(block) {
			count++
			continue
		}
		if phaseMarkedDoneByX(content, pv.Number) {
			count++
		}
	}
	return count
}

func (h *Handler) checkOneAgent(agent storage.Agent, client *opencode.Client) {
	// Only running yesloop agents
	if agent.Status != "running" {
		return
	}
	if !strings.HasPrefix(agent.Section, "yesloop-") {
		return
	}
	if agent.SessionID == "" || agent.Project == "" {
		return
	}

	// The guard's actions — a relay into the agent's socket, and later a pause
	// plus a DEAD_AGENT notice — belong to the agent's own process. Signal 0
	// alone would drive a dead row whose PID was recycled through the whole
	// escalation protocol, and where identity cannot be established the relay
	// must not be attempted at all (Learning #101907). Dead rows are
	// crashRecovery's case.
	if agentProcessGone(agent) {
		return
	}

	// A verdict from the done-verify layer outranks this one: it may have declared
	// the agent DONE_VERIFIED (or escalated it) while the row still says running,
	// and relaying the idle protocol into that agent contradicts the daemon's own
	// verdict (live 2026-10-15: DONE_VERIFIED at 23:36, idle relays from 23:42).
	// The verification stage's verdict counts the same way: an accepted report
	// releases the agent, and a blocked one holds it on purpose.
	if doneVerifySettled(agent) || h.doneGateVerifySettled(agent) {
		return
	}

	// Everything the decision needs is gathered before the lock. The scratchpad
	// read is a DB query, the child lookup a session-table query, the session read
	// an HTTP request — and the relay path takes the same mutex, so holding it
	// across them stalled every relay and every other idle decision for seconds
	// per agent (YM-253).
	streamFields := h.getStreamFields(agent.SessionID)
	streamActive, _ := streamFields["stream_active"].(bool)
	// Only the quiet phases need the freeze: a streaming agent is never relayed
	// (the state machine returns before any relay), and freezing here would also
	// skip the stream-active transitions — including the DONE re-arm of YM-149.
	inSubagent := !streamActive && h.agentInSubagent(agent.OpencodeSessionID, streamFields)

	// Register the state before any freeze check: a freeze that ran first would
	// let the first post-freeze tick re-baseline and silently absorb the work the
	// agent did inside the subagent (#89428).
	yesloopIdleAgentsMu.Lock()
	state, exists := yesloopIdleAgents[agent.ID]
	if !exists {
		state = &yesloopIdleState{state: yesloopIdleStateWorking}
		yesloopIdleAgents[agent.ID] = state
	}

	// An agent whose last turn died on a tool call is the broken-toolcall
	// guard's case: its relay names the failure and the tool, while this guard
	// would overlay that with the generic six-phase text and the agent would
	// never learn why it stopped (YM-196). The suppression lifts as soon as the
	// turn is read as healthy again (or after its own bound).
	//
	// The idle window is reset rather than merely frozen: the suppression can
	// end at any time — a turn becomes readable again, or the bound lapses — and
	// a stale idleSince (plus a stale lastRelayAt) would then drive the state
	// machine into a relay on the very next tick, without the fresh idle period
	// the protocol is built on.
	if brokenToolcallPending(agent) {
		state.refireCount = 0
		state.idleSince = time.Time{}
		clearEpisode(state)
		yesloopIdleAgentsMu.Unlock()
		return
	}

	// A relay into an agent that is blocked inside a task() subagent only queues
	// as a pending user message and hits it after the subagent returns (#89428).
	// The windows move forward so the protocol restarts its interval when the
	// child ends; the progress baselines deliberately do not, so work done inside
	// the child still counts as work on the next look.
	if inSubagent {
		if now := time.Now(); now.Sub(state.subagentFrozenAt) >= subagentFreezeLogEvery {
			log.Printf("[yesloop-idle] agent %s (%s) frozen: a task() subagent is active", safeAgentText(agent.ID), safeAgentText(agent.Section))
			state.subagentFrozenAt = now
		}
		if !state.idleSince.IsZero() {
			state.idleSince = time.Now()
		}
		if !state.lastRelayAt.IsZero() {
			state.lastRelayAt = time.Now()
		}
		yesloopIdleAgentsMu.Unlock()
		return
	}
	state.subagentFrozenAt = time.Time{}

	progressAt := state.progressAt
	// The session read only serves the three protocol rungs: WORKING decides
	// without it and DONE never acts, so reading there would cost one HTTP request
	// per tick for a resting agent.
	needsActivity := state.state == yesloopIdleStateSelfCheck ||
		state.state == yesloopIdleStateRemarkRequest ||
		state.state == yesloopIdleStateCommitRequest
	yesloopIdleAgentsMu.Unlock()

	// The scratchpad is read once per tick, after the freeze: every gate and the
	// progress signature are computed from this snapshot.
	content := h.readAgentScratchpad(agent)

	// The session read is the only input that needs the network, so it runs
	// without the lock.
	var reading idleSessionReading
	if needsActivity && client != nil && agent.OpencodeSessionID != "" && !progressAt.IsZero() {
		reading = h.sessionActivity(agent, progressAt, client)
	}

	h.runIdleAction(agent, h.decideIdleAction(agent, content, streamActive, reading))
}

// idleAction is what one tick decided to do. The decision is made under the
// idle-state lock; the action — a relay into the agent's socket, a pause plus a
// DEAD_AGENT notice, or a status message — runs after it is released (YM-253).
type idleAction struct {
	relay int
	// relayText overrides the rung's default wording for this delivery. The first
	// rung asks the six-phase question only of an agent that has reached Phase 6
	// or claims DONE; a mid-pipeline agent gets its own text (YM-306).
	relayText string
	// relayAfter is the lastRelayAt the decision was based on. The write
	// re-validates it under the lock: the decision and the relay are no longer
	// atomic, and a relay from the orchestrator that landed in between already
	// answered the agent.
	relayAfter       time.Time
	escalationReason string
	stopNote         string
	completed        bool
}

// decideIdleAction drives the state machine. It is pure state work: every input
// it needs was read by the caller, so the lock is held for microseconds.
func (h *Handler) decideIdleAction(agent storage.Agent, content string, streamActive bool, reading idleSessionReading) idleAction {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()

	state, exists := yesloopIdleAgents[agent.ID]
	if !exists {
		// A relay from the orchestrator lifted an idle-guard pause while the
		// session was being read (resetIdleProtocol). The agent is supervised
		// again and starts from a fresh state.
		state = &yesloopIdleState{state: yesloopIdleStateWorking}
		yesloopIdleAgents[agent.ID] = state
	}

	// Reset to WORKING if stream is active — but only while WORKING. Once the
	// state machine is progressing (SELF_CHECK/REMARK_REQUEST/COMMIT_REQUEST),
	// a stream blip from answering a relay must NOT reset the progression
	// (#82787): it would restart the cycle every time the agent responds, and the
	// idle window keeps running from the transition, so those states stay on the
	// 90s re-fire cadence rather than waiting for a fresh 10min idle period.
	// DONE is not terminal any more: a loop agent that finished the protocol and
	// kept working was never checked again until the daemon restarted, so stream
	// activity re-arms it with a fresh window (YM-149).
	if streamActive {
		switch state.state {
		case yesloopIdleStateWorking:
			state.refireCount = 0
			state.idleSince = time.Time{}
		case yesloopIdleStateDone:
			state.state = yesloopIdleStateWorking
			state.refireCount = 0
			state.idleSince = time.Time{}
			state.transitionedAt = time.Now()
			// A new supervision episode: the windows of the old one must not carry
			// over, or the agent's next working turn reads as an aborted one (YM-309).
			clearEpisode(state)
		}
		return idleAction{}
	}

	// Stream is inactive — set idle timestamp on first observation
	if state.idleSince.IsZero() {
		state.idleSince = time.Now()
		return idleAction{}
	}

	idleDuration := time.Since(state.idleSince)
	if idleDuration < yesloopIdleTimeout {
		return idleAction{} // not idle long enough yet
	}

	// --- real progress ends the protocol's patience, not its state ---
	//
	// A relay answer is only "no answer" when nothing changed: the agent's own
	// output — scratchpad and the phase it reports — is the evidence that it
	// worked. Before this check the refires counted time instead of silence and
	// ran to the pause over an agent that had answered (YM-207, live 2026-10-16
	// 19:33: "no PROVEN marker (refireCount=3, max=2)" for an agent reporting
	// Phase 4 work). A stream blip is not progress and still must not reset
	// anything (#82787) — what counts is what the agent produced.

	// Anti-flap ceiling (YM-253): the refire counter is per rung and a relay that
	// was answered restarts it, so the protocol has no ceiling of its own and
	// every relay re-arms the agent's window. At the cap the guard stands down
	// instead of pausing: an agent that produced work this often is more likely
	// working than dead, and the done and stagnation guards still watch it. The
	// stand-down is announced once per episode, not once per idle cycle, and it
	// lasts for the budget window — not until the stream happens to become active
	// again, which for an idle agent may be never.
	budgetLeft := idleRelayBudgetLeftLocked(agent.ID)
	if !budgetLeft {
		state.state = yesloopIdleStateDone
		if state.stoodDown {
			return idleAction{}
		}
		state.stoodDown = true
		return idleAction{stopNote: fmt.Sprintf(
			"stood down for %s after %d relays without completion; the agent keeps its status and stays under the done and stagnation guards",
			yesloopIdleRelayBudgetWindow, yesloopIdleMaxRelays)}
	}
	if state.stoodDown {
		state.stoodDown = false
		if state.state == yesloopIdleStateDone {
			log.Printf("[yesloop-idle] agent %s (%s) stand-down over: budget window passed, protocol re-armed", safeAgentText(agent.ID), safeAgentText(agent.Section))
			state.state = yesloopIdleStateWorking
			state.transitionedAt = time.Now()
			state.idleSince = time.Now()
			clearEpisode(state)
			return idleAction{}
		}
	}

	// Drive state machine
	switch state.state {
	case yesloopIdleStateWorking:
		h.transitionTo(agent, state, yesloopIdleStateSelfCheck, content)
		return idleAction{relay: 1, relayAfter: state.lastRelayAt, relayText: idleRelayOneText(content, agent.Phase)}

	case yesloopIdleStateSelfCheck:
		if content != "" && hasProvenMarker(content) {
			h.transitionTo(agent, state, yesloopIdleStateRemarkRequest, content)
			return idleAction{relay: 2, relayAfter: state.lastRelayAt}
		}
		if h.answeredSinceLastRelay(agent, state, content, reading) {
			return idleAction{}
		}
		if h.withholdRefireOnUnreadableSession(agent, state, reading) {
			return idleAction{}
		}
		a := h.maybeRefireLocked(agent, state, 1, unreadableReason(reading, "no PROVEN marker in scratchpad"))
		if a.relay == 1 {
			// The wording is decided per delivery, not per rung: a refire goes to an
			// agent that has had time to reach the end of the pipeline, and the
			// six-phase question is the one it can answer by then (YM-306).
			a.relayText = idleRelayOneText(content, agent.Phase)
		}
		return a

	case yesloopIdleStateRemarkRequest:
		if content != "" && countMarkedDonePhases(content) >= donePhaseCount && phase5ColdReviewPresent(content) {
			h.transitionTo(agent, state, yesloopIdleStateCommitRequest, content)
			return idleAction{relay: 3, relayAfter: state.lastRelayAt}
		}
		if h.answeredSinceLastRelay(agent, state, content, reading) {
			return idleAction{}
		}
		if h.withholdRefireOnUnreadableSession(agent, state, reading) {
			return idleAction{}
		}
		return h.maybeRefireLocked(agent, state, 2, unreadableReason(reading, "phases not completed or phase 5 cold review missing"))

	case yesloopIdleStateCommitRequest:
		if content != "" && strings.Contains(content, "send_to orchestrator:") {
			h.transitionTo(agent, state, yesloopIdleStateDone, content)
			return idleAction{completed: true}
		}
		if h.answeredSinceLastRelay(agent, state, content, reading) {
			return idleAction{}
		}
		if h.withholdRefireOnUnreadableSession(agent, state, reading) {
			return idleAction{}
		}
		return h.maybeRefireLocked(agent, state, 3, unreadableReason(reading, "no completion evidence"))

	case yesloopIdleStateDone:
		// No further action while the stream stays inactive: the protocol ran to
		// its end (or escalated). Stream activity re-arms it above.
		return idleAction{}
	}
	return idleAction{}
}

// runIdleAction carries out what the decision asked for. Everything here is I/O
// — a socket write, a DB write, a status push — and must stay outside the
// idle-state lock (YM-253).
func (h *Handler) runIdleAction(agent storage.Agent, a idleAction) {
	if a.escalationReason != "" {
		// The escalation is a nudge. The protocol check stays: an orchestrator
		// relay that landed between decision and execution already answered it,
		// and a second notice for an answered escalation is noise.
		if !h.idleProtocolPresent(agent.ID) {
			log.Printf("[yesloop-idle] agent %s (%s) escalation skipped: the protocol moved after the decision", safeAgentText(agent.ID), safeAgentText(agent.Section))
			return
		}
		if !h.nudgeAgent(agent.ID, fmt.Sprintf("%s %s", yesloopIdleMarker, a.escalationReason)) {
			skipNudgeNote("yesloop-idle", agent.ID, agent.Section)
			return
		}
		h.notifyOrchestrator(agent, nudgeNotice(fmt.Sprintf("Agent %s (%s) idle escalation: %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), a.escalationReason)))
		return
	}
	if a.completed {
		log.Printf("[yesloop-idle] agent %s (%s) completed idle protocol", safeAgentText(agent.ID), safeAgentText(agent.Section))
		h.notifyOrchestrator(agent, fmt.Sprintf("Agent %s (%s) completed idle protocol DONE", safeAgentText(agent.ID), safeAgentText(agent.Section)))
		return
	}
	if a.stopNote != "" {
		log.Printf("[yesloop-idle] agent %s (%s) %s", safeAgentText(agent.ID), safeAgentText(agent.Section), a.stopNote)
		h.notifyOrchestrator(agent, fmt.Sprintf("Idle guard stood down: agent %s (%s) %s", safeAgentText(agent.ID), safeAgentText(agent.Section), a.stopNote))
		return
	}
	if a.relay > 0 {
		h.sendIdleRelay(agent, a.relay, a.relayText, a.relayAfter)
	}
}

// idleProtocolPresent reports whether the agent still has idle bookkeeping. A
// relay from the orchestrator lifts an idle-guard pause and deletes it
// (resetIdleProtocol), so its absence means "the agent was answered". Caller does
// not hold the lock.
func (h *Handler) idleProtocolPresent(agentID string) bool {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	_, ok := yesloopIdleAgents[agentID]
	return ok
}

// agentInSubagent reports whether the agent is blocked inside a task() subagent.
// Two sources, because no single one covers every backend: an opencode child
// session (authoritative, session DB) and the proxy's stream marker, which says
// that the newest stream on the agent's own thread belonged to a subagent —
// Claude Code subagents are recognised in the request body, so that source
// survives a backend with no session API at all (the case the audit names).
//
// The marker covers the quiet phases of the subagent (the model thinking, a tool
// running) and stays set until the parent's next own stream starts, so it is
// bounded by yesloopIdleWorkingMax: a crash right after a subagent cannot hold
// the ladder off for good. The caller only consults it for an inactive stream.
func (h *Handler) agentInSubagent(opencodeSessionID string, fields map[string]any) bool {
	if opencodeChildActiveFn(opencodeSessionID) {
		return true
	}
	if isSub, _ := fields["stream_is_subagent"].(bool); isSub {
		started, _ := fields["stream_started"].(string)
		if at, err := time.Parse(time.RFC3339, started); err == nil && time.Since(at) < yesloopIdleWorkingMax {
			return true
		}
	}
	return false
}

// idleRelayBudgetLeftLocked reports whether the agent may still be relayed, and
// drops a budget whose window has passed. Caller holds yesloopIdleAgentsMu.
func idleRelayBudgetLeftLocked(agentID string) bool {
	budget, ok := yesloopIdleRelays[agentID]
	if !ok {
		return true
	}
	if time.Since(budget.since) >= yesloopIdleRelayBudgetWindow {
		delete(yesloopIdleRelays, agentID)
		return true
	}
	return budget.count < yesloopIdleMaxRelays
}

// transitionTo moves the agent to a new idle state and logs the transition. The
// content is the scratchpad snapshot the caller already read; the progress
// signature is derived from it.
func (h *Handler) transitionTo(agent storage.Agent, state *yesloopIdleState, newState int, content string) {
	oldName := stateNames[state.state]
	newName := stateNames[newState]
	log.Printf("[yesloop-idle] agent %s (%s) state %s -> %s", safeAgentText(agent.ID), safeAgentText(agent.Section), oldName, newName)
	state.state = newState
	state.refireCount = 0
	state.transitionedAt = time.Now()
	// A new rung is a fresh unknown window: the relay that follows is the agent's
	// chance to answer, and an unreadable session must not be charged to it before
	// that chance has run its course.
	state.lastReadAt = time.Now()
	// Baseline of the agent's own output: work seen from here on is the answer to
	// the relay that follows, not the state the agent was already in. The
	// signature (scratchpad plus reported phase) is the deterministic evidence
	// that an agent worked; Progress is deliberately not part of it, because that
	// column is daemon bookkeeping (pause, stop, orphan notices) and a guard
	// moving the agent would otherwise read as the agent having worked.
	h.armProgressBaseline(agent, state, stagnationSig(content, agent.Phase))
}

// armProgressBaseline fixes both progress sources at the current state: the
// agent's own output (scratchpad plus reported phase) and everything that
// measures work between milestones (turns, session messages).
func (h *Handler) armProgressBaseline(agent storage.Agent, state *yesloopIdleState, sig string) {
	state.progressSig = sig
	h.armWorkBaseline(agent, state)
}

// armWorkBaseline fixes the two counters that measure work rather than output.
// The baseline belongs to the relay that was just sent: the answer to it costs
// one turn, and a counter compared against the state transition instead would
// let two answers pile up into "work" and stall the ladder for good.
func (h *Handler) armWorkBaseline(agent storage.Agent, state *yesloopIdleState) {
	state.progressTurns = agent.TurnsUsed
	state.progressAt = time.Now()
}

// answeredSinceLastRelay reports whether the agent produced work since the
// protocol's baseline, and if so restarts the relay accounting. It is consulted
// only when a gate found no evidence, so an agent that did answer the specific
// question still advances through the ladder.
//
// The state is deliberately kept. Resetting to WORKING here would throw away the
// gate's progress and re-ask the first question from the start — the ladder
// would never reach its later rungs (found by the consequence review: with the
// reset in front of the gates, a complying agent looped on relay 1 and never
// advanced). What has to stop is the escalation, not the state machine: a relay
// that was answered restarts the count, so only an agent that produced nothing
// since the last relay can be escalated (YM-207).
func (h *Handler) answeredSinceLastRelay(agent storage.Agent, state *yesloopIdleState, content string, reading idleSessionReading) bool {
	worked, evidence := h.workedSinceLastRelay(agent, state, content, reading)
	if !worked {
		return false
	}
	log.Printf("[yesloop-idle] agent %s (%s) worked since the last relay (%s) — refire count restarted",
		safeAgentText(agent.ID), safeAgentText(agent.Section), evidence)
	h.armProgressBaseline(agent, state, stagnationSig(content, agent.Phase))
	state.refireCount = 0
	state.lastRelayAt = time.Now()
	return true
}

// idleSessionReading is the outcome of the session read that decides the idle
// guard's third progress source. Three states, because "the agent produced
// nothing" and "the session could not be read" are different statements and only
// the first one may be charged to the agent (YM-306, live 2026-10-17 22:29).
type idleSessionReading struct {
	activity opencode.SessionActivity
	// known means the read succeeded and the activity is the session's answer.
	known bool
	// unreadable means the read was attempted and failed. The caller treats it as
	// "cannot tell": it neither counts as work nor as silence.
	unreadable bool
}

// workedSinceLastRelay reports whether the agent did more than answer the relay,
// and names the evidence for the log. Four sources, all deterministic:
//
//   - its own output: scratchpad or reported phase changed;
//   - its turn counter: several turns since the last relay, above the answer's
//     own request chain;
//   - an unfinished turn in its session: the model is still writing, or a tool of
//     the newest turn still runs. That is a long command or a cold-review
//     subagent (the live 2026-10-16 case), and it is what the agent looks like
//     while it works but produces no new turn;
//   - several completed assistant turns since the last relay, which is the same
//     statement as the counter when telemetry never reached the agent's row.
//
// The opposite direction stays intact (#79819): an agent that hangs while its
// process lives has a finished turn, no new output and no new turns since the
// relay, and is still escalated — an unfinished turn only counts while it is
// younger than yesloopIdleWorkingMax. That bound is measured on the turn itself,
// never on the relay baseline: the guard's own relays move the baseline past the
// start of a call that is still running (YM-306).
//
// The session reading is passed in already made: it is the one source that needs
// the network, so the caller performs it outside the idle-state lock.
func (h *Handler) workedSinceLastRelay(agent storage.Agent, state *yesloopIdleState, content string, reading idleSessionReading) (bool, string) {
	if reading.known {
		// A readable session ends an unreadable episode: the next failure gets a
		// window of its own instead of inheriting an aged one.
		state.lastReadAt = time.Now()
	}
	sig := stagnationSig(content, agent.Phase)
	if state.progressSig == "" {
		h.armProgressBaseline(agent, state, sig)
		return false, ""
	}
	if sig != state.progressSig {
		return true, "scratchpad or reported phase changed"
	}
	if turns := agent.TurnsUsed - state.progressTurns; turns >= yesloopIdleProgressTurns {
		return true, fmt.Sprintf("%d turns since the relay", turns)
	}
	if reading.known {
		if reading.activity.Working {
			bound := yesloopIdleWorkingMax
			if reading.activity.Background {
				bound = yesloopIdleBackgroundMax
			}
			if workingTurnCounts(state, reading.activity.WorkingSince, bound) &&
				(!reading.activity.Background || backgroundCreditLeft(state)) {
				// The two signals are told apart in the log: the guard's strings are
				// read as post-mortem evidence, and a job the agent waits for is not
				// the same observation as an open turn (5.2 finding 5).
				if reading.activity.Background {
					return true, "a background job of the newest turn is still running"
				}
				return true, "the newest turn is still working"
			}
		}
		if !reading.activity.Working {
			clearWorkingWindow(state)
		}
		if reading.activity.Since >= yesloopIdleProgressTurns {
			return true, fmt.Sprintf("%d completed assistant turns since the relay", reading.activity.Since)
		}
	}
	return false, ""
}

// workingTurnCounts reports whether a turn the session still reports as working
// may count as work, and keeps the guard's own window on it. Two bounds, both
// have to hold, and both are measured in bound — the turn bound for an open turn
// or a running tool, the longer job bound for a background job:
//
//   - the turn's own start: an open turn that has been open for longer than bound
//     is an aborted one. That is what makes the signal independent of the relay
//     baseline, which the guard moves itself (YM-306);
//   - the first observation: whatever stamp the service reports — absent, far in
//     the future, mis-scaled — a turn cannot count for longer than bound from the
//     moment the guard first saw it working. An implausible stamp would otherwise
//     hold the ladder off for good, and the guard would stop supervising exactly
//     the agent whose session it cannot read correctly;
//   - the streak: both bounds restart with every new turn, so a chain of open
//     turns that never settles is additionally capped at
//     yesloopIdleTurnEpisodeMax per episode. Without it an agent that mints one
//     unfinished turn after the other would renew the signal and reset the ladder
//     forever (5.2/5.3 review).
//
// Caller holds yesloopIdleAgentsMu.
func workingTurnCounts(state *yesloopIdleState, started time.Time, bound time.Duration) bool {
	// The window belongs to one turn. A new turn gets its own, or two turns in a
	// row share a single budget and the second one is dropped as aborted while it
	// runs (YM-309). Two consecutive turns without a start stamp are
	// indistinguishable, so those keep the window they opened.
	if state.workingSince.IsZero() || !state.workingTurnStart.Equal(started) {
		state.workingSince = time.Now()
		state.workingTurnStart = started
	}
	// The streak of open turns is bounded per episode on top of that: a turn never
	// settles, the next open turn renews the signal, and the ladder would reset
	// forever on an agent that produces nothing but unfinished turns.
	if state.turnEpisodeSince.IsZero() {
		state.turnEpisodeSince = time.Now()
	}
	if time.Since(state.turnEpisodeSince) >= yesloopIdleTurnEpisodeMax {
		return false
	}
	if time.Since(state.workingSince) >= bound {
		return false
	}
	if started.IsZero() {
		return true
	}
	return time.Since(started) < bound
}

// clearWorkingWindow forgets the working turn the age bound hangs on. It runs
// where a new supervision episode starts: the bound belongs to one turn of one
// episode, and a stamp carried over makes the next working turn look aborted
// (YM-309). A read that reports a settled turn runs it too, which is what ends
// the open-turn streak's credit: the agent produced a completed turn, so its next
// working turn starts a new streak.
//
// Caller holds yesloopIdleAgentsMu.
func clearWorkingWindow(state *yesloopIdleState) {
	state.workingSince = time.Time{}
	state.workingTurnStart = time.Time{}
	state.turnEpisodeSince = time.Time{}
}

// clearEpisode forgets the per-episode bookkeeping at the start of a new
// supervision episode: both working windows, the open-turn streak's credit, the
// background job's episode credit (which would otherwise be spent before the
// agent ever starts a job, 5.4 review) and the abstention window on an unreadable
// session. Carrying the last one over hands the new episode a spent budget, so a
// re-armed agent that demonstrably worked is escalated on the first failed read
// while a freshly registered one gets the full window (5.2 finding 2).
//
// Caller holds yesloopIdleAgentsMu.
func clearEpisode(state *yesloopIdleState) {
	clearWorkingWindow(state)
	state.backgroundSince = time.Time{}
	state.lastReadAt = time.Time{}
}

// backgroundCreditLeft reports whether the episode's job credit still runs. The
// first job the guard sees in an episode opens it; every later job of the same
// episode shares the remaining time, so the total background credit an agent can
// hold is yesloopIdleBackgroundMax per episode and not one window per turn.
//
// Caller holds yesloopIdleAgentsMu.
func backgroundCreditLeft(state *yesloopIdleState) bool {
	if state.backgroundSince.IsZero() {
		state.backgroundSince = time.Now()
		return true
	}
	return time.Since(state.backgroundSince) < yesloopIdleBackgroundMax
}

// withholdRefireOnUnreadableSession reports whether this rung has to wait instead
// of escalating, because the session read failed and the agent's work is
// therefore unknown. It is neither work nor silence: the refire counter stays
// where it is.
//
// The wait is bounded per episode, not per relay: the window runs from the last
// moment the guard had an answer (lastReadAt), so a broken service delays the
// escalation by at most yesloopIdleWorkingMax however many relays and rungs the
// episode spans. A per-relay window would multiply by the number of rungs and
// leave the agent unsupervised for the better part of an hour (YM-306 review).
// Without any answer in this rung the first failed read opens the window.
//
// Caller holds yesloopIdleAgentsMu.
func (h *Handler) withholdRefireOnUnreadableSession(agent storage.Agent, state *yesloopIdleState, reading idleSessionReading) bool {
	if !reading.unreadable {
		return false
	}
	// Without an inject socket the relay this hold protects can never reach the
	// agent. Waiting orders nothing: the agent stays unsupervised for the whole
	// window without a single delivery, so the escalation has to go ahead
	// (YM-309).
	//
	// A socket path that refused the dial does NOT remove the hold: the bridge is
	// armed when it starts listening, so a missing listener is transient, and
	// treating it as permanent would pause an agent that is merely still starting
	// up — the false-escalation class this guard exists to avoid. The refusal is
	// remembered for the escalation text instead, so the orchestrator does not read
	// silence as an ignored prompt (5.2 finding 1, 5.4 review).
	if h.agentInjectPath(agent.ID) == "" {
		return false
	}
	if state.lastReadAt.IsZero() {
		state.lastReadAt = time.Now()
		log.Printf("[yesloop-idle] agent %s (%s) session unreadable — escalation held for at most %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), yesloopIdleWorkingMax)
		return true
	}
	return time.Since(state.lastReadAt) < yesloopIdleWorkingMax
}

// unreadableReason names the failing read in an escalation that had to go ahead
// without a session answer. Where the hold still applies the counter is not
// charged to the agent, so the orchestrator should not read the pause as an
// observed standstill either; where no relay could be delivered the counter is
// charged like any other refire, which undeliverableReason states.
func unreadableReason(reading idleSessionReading, reason string) string {
	if reading.unreadable {
		return reason + " (the session read kept failing, so the agent's work is unknown)"
	}
	return reason
}

// undeliverableReason names a missing or dead inject socket in an escalation that
// could not deliver a single relay. Without it the orchestrator reads the pause as
// "the agent ignored three prompts" and the prescribed relay_agent cannot reach
// the row either (5.3 finding 2).
func (h *Handler) undeliverableReason(agent storage.Agent, state *yesloopIdleState, reason string) string {
	if h.agentInjectPath(agent.ID) == "" {
		return reason + " (no inject socket path can be derived from the agent id, so no relay could be delivered)"
	}
	if state.relayUndeliverable {
		return reason + " (the agent's inject socket refused every relay, so none could be delivered)"
	}
	return reason
}

// sessionActivity reads the agent's opencode session. The failure states are
// kept apart from an empty result: no bound session, no v2 service and no
// baseline mean there is nothing to read (the turn counter still decides, and a
// missing service must not make the guard stricter than it was), while a failed
// request means the agent's work is unknown.
//
// The client is resolved once per sweep by the caller, and the call runs without
// the idle-state lock: resolving it is a discovery over the service registry with
// its own probes, and the request itself is bounded by
// yesloopIdleMessageTimeout — seconds that the relay path must not wait for.
func (h *Handler) sessionActivity(agent storage.Agent, since time.Time, client *opencode.Client) idleSessionReading {
	if client == nil || agent.OpencodeSessionID == "" || since.IsZero() {
		return idleSessionReading{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), yesloopIdleMessageTimeout)
	defer cancel()
	activity, err := client.AssistantActivitySince(ctx, agent.OpencodeSessionID, since)
	if err != nil {
		log.Printf("[yesloop-idle] agent %s (%s) session read failed: %v", safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(err.Error()))
		return idleSessionReading{unreadable: true}
	}
	return idleSessionReading{activity: activity, known: true}
}

// maybeRefire checks if a re-fire is needed and escalates when max re-fires are
// exceeded. The nudge and its notice are returned as an action instead of
// executed here: this runs under the idle-state lock.
func (h *Handler) maybeRefireLocked(agent storage.Agent, state *yesloopIdleState, relayNum int, reason string) idleAction {
	if time.Since(state.lastRelayAt) < yesloopIdleRefireInterval {
		return idleAction{}
	}
	state.refireCount++
	if state.refireCount > yesloopIdleMaxRefires {
		state.state = yesloopIdleStateDone
		reason = h.undeliverableReason(agent, state, reason)
		log.Printf("[yesloop-idle] agent %s (%s) ESCALATION: %s (refireCount=%d, max=%d)",
			safeAgentText(agent.ID), safeAgentText(agent.Section), reason, state.refireCount, yesloopIdleMaxRefires)
		return idleAction{escalationReason: reason}
	}
	return idleAction{relay: relayNum, relayAfter: state.lastRelayAt}
}

// rollbackIdleRefire gives back the refire slot that maybeRefireLocked took for a
// relay that was never delivered (deferred by a busy socket, or invalidated by a
// protocol reset). Without it a relay that never left could escalate into a
// DEAD_AGENT notice.
func (h *Handler) rollbackIdleRefire(agentID string) {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	if current, ok := yesloopIdleAgents[agentID]; ok && current.refireCount > 0 {
		current.refireCount--
	}
}

// markRelayAttempted stamps an attempt that could not be delivered (no socket, or
// a socket that refused the dial). The attempt still counts as a refire, but the
// 90-second interval is measured from here: without the stamp lastRelayAt stays
// zero, the interval never gates, and every 30-second tick burns a slot, so three
// attempts collapse into 90 seconds instead of 3 x 90 seconds.
//
// undeliverable records the failure for the rest of the episode: the unreadable
// hold and the escalation text both have to know that silence means nothing here.
// A deferred relay is not stamped: it rolls its slot back and is retried on the
// next tick.
func (h *Handler) markRelayAttempted(agentID string, undeliverable bool) {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	if current, ok := yesloopIdleAgents[agentID]; ok {
		current.lastRelayAt = time.Now()
		if undeliverable {
			current.relayUndeliverable = true
		}
	}
}

// safeAgentText strips the characters that would end or forge a line from a
// caller-supplied agent value before it reaches a log line, a relay body or an
// orchestrator message. Newlines and tabs split the line; the C1 range holds NEL,
// which some readers treat as a line end; the bidi controls reorder text so a
// value can read as something other than it is — the same set
// orchestrator.PromptFieldUnsafe rejects, minus the quoting characters, which do
// not split a line and would mangle legitimate text.
func safeAgentText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		case r == 0x2028 || r == 0x2029:
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f:
			return -1
		}
		return r
	}, s)
}

// relayDialError renders a failed inject-socket dial without the raw path. The
// path derives from agents.sock_path, and net.OpError prints it unquoted, so a
// value carrying a newline would split the log line it is reported in — quoting
// at the printf is too late (#102738, the same shape as archive's errWithPath).
// The path is quoted and only the OS reason is kept: the wrapped error is still
// in the chain for errors.Is, but the *net.OpError itself is not — no caller
// inspects that type, and the reason is what a reader needs.
func relayDialError(sockPath string, err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("dial %q: %w", sockPath, opErr.Err)
	}
	return fmt.Errorf("dial %q: %w", sockPath, err)
}

// readAgentScratchpad returns the scratchpad content for an agent's section.
func (h *Handler) readAgentScratchpad(agent storage.Agent) string {
	sections, err := h.store.ScratchpadRead(agent.Project, agent.Section)
	if err != nil {
		log.Printf("[yesloop-idle] scratchpad read error for %s: %v", safeAgentText(agent.ID), safeAgentText(err.Error()))
		return ""
	}
	if len(sections) == 0 {
		return ""
	}
	return sections[0].Content
}

// dispatchVetoRe matches a dispatch FIELD whose value is "blocked" — the
// canonical "Stage 2 did not run" signal in the modern field contract.
var dispatchVetoRe = regexp.MustCompile(`(?im)^\*\*(?:task\(\) dispatched|consequence dispatched):\*{0,2}\s+blocked`)

// phase5ColdReviewPresent validates that Phase 5's scratchpad block actually
// contains a Cold Review / Stage 2 trace — not just a Status: COMPLETE header
// the agent wrote without doing the work. Catches the rationalization pattern
// where agents claim COMPLETE but document "Stage 2: Blocked" in the body.
func phase5ColdReviewPresent(content string) bool {
	phases := splitPhases(content)
	block, ok := phases[5]
	if !ok {
		return false
	}
	// Hard veto: a dispatch field carrying "blocked" means Stage 2/3 did not
	// complete — the canonical non-run signal. Prose occurrences of "blocked"
	// (template quotes in findings text, idiom discussions) must NOT veto:
	// they froze a fully completed agent in REMARK_REQUEST (Learning #87847).
	if dispatchVetoRe.MatchString(block) {
		return false
	}
	// Phase 5 block must contain at least one of these positive trace markers:
	// the legacy Stage-2 vocabulary or the new sub-phase dispatch lines
	// (cold-review/consequence/security: ses_<id>, "Sub-phase 5.2: Cold Code Review").
	stage2Re := regexp.MustCompile(`(?i)\b(stage 2|cold review|cold code review|cold-review|consequence|security|task\(\)|subagent)\b`)
	return stage2Re.MatchString(block)
}

// idleRelayMessage returns the relay body for one rung of the idle protocol.
// Package-level so tests can assert the wording without capturing the socket
// write. The text is metachar-free (no markdown, no backticks, no parens): it is
// written as a single line to the agent's PTY inject socket.
func idleRelayMessage(relayNum int) string {
	switch relayNum {
	case 1:
		return "Have you completed all 7 phases? If not do it now. For each phase prove you have done each. IF you have proven, mark each phase as done: put [x] at the end of the phase header line, or on its own line inside the phase block with just the marker and optionally the phase number or phase name, for example [x] Phase 1 or [x] ANALYZE. The Status line from the phase template with the value COMPLETE also counts. MANDATORY: Make sure that you have also done phase 5 with all code reviews including Sub-phase 5.2 cold review, 5.3 consequence check and 5.4 security review via task subagents. REVIEW BLOCKED without subagent trace is not acceptable. Mandatory: only mark as PROVEN if it IS proven."
	case 2:
		return "Mark all 7 phases as done in scratchpad: put [x] at the end of the phase header line, or on a dedicated marker line inside the phase block. The Status line from the phase template with the value COMPLETE also counts."
	case 3:
		return "If 1 through 7 are ok commit and send_to to caller."
	}
	return ""
}

// idleRelayOneText picks the first rung's wording for one agent. The six-phase
// question belongs to an agent that has reached Phase 6 or already claims DONE;
// a mid-pipeline agent reads it as a licence to tick phases it never did (live
// 2026-10-17: agents in Phase 3 were asked whether all six phases were done,
// YM-306). The empty result means "rung 1's own wording".
//
// Both the scratchpad and the phase the agent reports count: an agent that has
// just reported Phase 6/6 through update_agent_status has not necessarily written
// the header yet, and the six-phase question is the one it can answer there.
func idleRelayOneText(content, reportedPhase string) string {
	if hasDoneClaim(content) || phaseSixReportRe.MatchString(reportedPhase) {
		return ""
	}
	return idleMidPipelineRelayMessage()
}

// phaseSixReportRe matches the phase an agent reports once it has reached the end
// of the pipeline ("Phase 6/6 FINISH").
var phaseSixReportRe = regexp.MustCompile(`(?i)\bphase[ \t]*(6|7)\b`)

// idleMidPipelineRelayMessage is the first rung's wording for an agent that is
// still inside the pipeline. It asks for the work in the phase the agent is in
// and names the one thing the six-phase text got wrong there: nothing is marked
// done before it is done.
//
// The ladder stays answerable from here: this text asks for progress, and the
// delivery after the agent reaches Phase 6 switches to the six-phase question by
// itself (idleRelayOneText), which is the rung that then carries PROVEN and the
// cold-review checklist. Metachar-free for the same reason as idleRelayMessage.
func idleMidPipelineRelayMessage() string {
	return "Your session showed no activity for a while. Continue the phase you are working on: do the remaining work, then write your progress and evidence into the scratchpad. Do not mark a phase as done that is not done."
}

// sendIdleRelay sends a relay message to a yesloop agent via the inject socket.
// Uses a single-write approach (without 3s delay) since the agent is already idle
// and the message doesn't require bracketed-paste splitting.
//
// The socket write runs outside the idle-state lock; only the bookkeeping that the
// write implies takes the lock again (YM-253).
func (h *Handler) sendIdleRelay(agent storage.Agent, relayNum int, text string, relayAfter time.Time) {
	msg := text
	if msg == "" {
		msg = idleRelayMessage(relayNum)
	}

	// Re-validate the decision right before the write. The decision and the relay
	// are no longer atomic (the write had to leave the lock, YM-253), and an
	// orchestrator relay that landed in between already answered this agent: it
	// must not get a second prompt on top. A reset protocol deletes the state, and
	// a relay that was sent moves lastRelayAt — both are visible here. This runs
	// before the socket check: an attempt for a decision that is already stale must
	// not stamp a fresh episode's lastRelayAt either.
	yesloopIdleAgentsMu.Lock()
	current, ok := yesloopIdleAgents[agent.ID]
	stale := !ok || !current.lastRelayAt.Equal(relayAfter)
	yesloopIdleAgentsMu.Unlock()
	if stale {
		// Same reasoning as the deferral below: the relay never left, so it must
		// not count towards an escalation.
		h.rollbackIdleRefire(agent.ID)
		log.Printf("[yesloop-idle] relay %d to agent %s skipped: the protocol moved after the decision", relayNum, safeAgentText(agent.ID))
		return
	}

	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[yesloop-idle] relay %d to agent %s skipped: no derivable socket path", relayNum, safeAgentText(agent.ID))
		h.markRelayAttempted(agent.ID, true)
		return
	}

	wrapped := fmt.Sprintf("[RELAY from=yesloop-idle] %s", msg)

	// The same per-socket lock the relay_agent path takes: a guard relay must not
	// land between another delivery's content and its submit. Taken as a try-lock
	// — a sweep runs on the shared heartbeat goroutine and must not wait out
	// another delivery's submit delay; the next tick retries.
	unlock, locked := tryLockRelayPath(injectPath)
	if !locked {
		// A deferral is not a relay that failed to reach the agent, so it must not
		// burn a refire slot: two deferrals in a row would escalate into a
		// DEAD_AGENT notice for an agent that never received anything.
		h.rollbackIdleRefire(agent.ID)
		log.Printf("[yesloop-idle] relay %d to agent %s deferred: the socket is inside another delivery's submit window", relayNum, safeAgentText(agent.ID))
		return
	}
	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		unlock()
		log.Printf("[yesloop-idle] relay %d to agent %s failed: %v", relayNum, safeAgentText(agent.ID), relayDialError(injectPath, err))
		h.markRelayAttempted(agent.ID, true)
		return
	}
	// Single write: content + submit in one go. The agent's PTY buffers this.
	conn.Write([]byte(wrapped + "\r\n"))
	conn.Close()
	unlock()

	yesloopIdleAgentsMu.Lock()
	// What the agent does from here is the answer to this relay, so the work
	// baseline moves with it (see armWorkBaseline). A state that an orchestrator
	// relay removed meanwhile is not resurrected: that agent is supervised again
	// and starts from a fresh window.
	if current, ok := yesloopIdleAgents[agent.ID]; ok {
		current.lastRelayAt = time.Now()
		// The socket just accepted a write, so the silence this guard may observe
		// from here can be answered again.
		current.relayUndeliverable = false
		h.armWorkBaseline(agent, current)
	}
	budget, ok := yesloopIdleRelays[agent.ID]
	if !ok || time.Since(budget.since) >= yesloopIdleRelayBudgetWindow {
		budget = &idleRelayBudget{since: time.Now()}
		yesloopIdleRelays[agent.ID] = budget
	}
	budget.count++
	yesloopIdleAgentsMu.Unlock()
}

// resetIdleProtocol forgets an agent's idle bookkeeping. A relay that lifts a
// retired guard pause answers the escalation, and the state machine must not stay
// at DONE — there the guard ignores the agent until its stream becomes active
// again, which is exactly the supervision the agent was just returned to
// (YM-225). The next tick starts a fresh idle window.
func resetIdleProtocol(agentID string) {
	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()
	delete(yesloopIdleAgents, agentID)
}

// notifyOrchestrator sends a status message to the agent's caller_session.
// notifyOrchestrator sends one notice to the agent's caller and reports whether
// the call itself was accepted. A caller without an address cannot be told
// anything, and a store that refuses the message has told nobody: the caller of
// this function decides whether that is a retry (the hold watchdog) or a log.
func (h *Handler) notifyOrchestrator(agent storage.Agent, content string) bool {
	if agent.CallerSession == "" {
		return false
	}
	resp := h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target":   agent.CallerSession,
			"content":  content,
			"msg_type": "status",
			"sender":   daemonSender("yesloop"),
		},
	})
	return resp.Error == ""
}
