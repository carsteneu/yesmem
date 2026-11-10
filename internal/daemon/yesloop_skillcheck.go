package daemon

import (
	"fmt"
	"log"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- State machine for yesloop skill-reload / context-compaction check ---
//
// Layer 4 of the yesloop guarantee. Long yesloop runs risk losing the pipeline
// skill text when their context is compacted. This machine watches cumulative
// agent work volume (output tokens, falls back to turns) and, past a threshold
// since the last reset baseline, relays a reminder to re-load the yesloop skill
// and confirm via a per-round SKILL-RELOAD marker. States and helpers are
// modeled after yesloop_done_verify.go.
//
// Trigger: running yesloop agent with live PID whose output-token delta since
// baseline >= 100k OR turn delta >= 50. Confirmation resets the baseline so the
// gate measures the next compaction window.
//
// Round-scoped marker: every window's relay embeds a fresh round token
// (SKILL-RELOAD-<N>: yes). Only a marker matching the CURRENT round confirms —
// a stale marker from an earlier window cannot auto-ack later windows, so the
// notification stays reachable after the first confirmation.
//
// This layer never pauses an agent. Past the last refire it reports the round
// once to the orchestrator, closes the round the way a confirmation would
// (baseline reset) and starts the next window from there: the pipeline contract
// is checked at DONE by the seven-phase validator, and a pause here turned the
// orchestrator into the resume button without a single case that needed it.

const (
	yesloopSkillCheckStateTracking = iota
	yesloopSkillCheckStateRemind
	yesloopSkillCheckStateConfirmed
)

var yesloopSkillCheckStateNames = map[int]string{
	yesloopSkillCheckStateTracking:  "TRACKING",
	yesloopSkillCheckStateRemind:    "REMIND",
	yesloopSkillCheckStateConfirmed: "CONFIRMED",
}

const (
	yesloopSkillCheckMaxRefires     = 3
	yesloopSkillCheckRefireInterval = 5 * time.Minute
	// skillCheckOutputTokenThreshold is the accumulated output-token delta (since
	// the last reset baseline) that triggers the compaction reminder.
	skillCheckOutputTokenThreshold = 100_000
	// skillCheckTurnThreshold is the fallback: if token counters are not
	// maintained, this many turns since baseline fires the reminder.
	skillCheckTurnThreshold = 50
)

// skillCheckRelayTemplate is the relay body. Metachar-free (no markdown, no
// backticks, no parens) because it is written as a single line to the agent's
// PTY inject socket. %d is the per-window round token.
const skillCheckRelayTemplate = "Context compaction risk: verify the yesloop skill is still fully loaded. If the pipeline instructions are missing from your context reload the skill now via the skill tool or the SKILL.md file. Confirm by writing SKILL-RELOAD-%d: yes to your scratchpad."

// skillCheckRelayMessage renders the relay for a specific round. Returns the
// empty string when the input contains characters that would break the
// metachar-free contract (only digits are ever substituted).
func skillCheckRelayMessage(round int) string {
	return fmt.Sprintf(skillCheckRelayTemplate, round)
}

// skillReloadMarkerRe matches an agent's SKILL-RELOAD confirmation carrying the
// exact round token of the current window. Line-anchored and round-scoped so a
// stale marker (or one pasted mid-paragraph) cannot auto-ack a later window.
var skillReloadMarkerRe = regexp.MustCompile(`(?m)^\s*\**SKILL-RELOAD-(\d+)\s*:\s*\**\s*yes\b`)

// hasSkillReloadMarker reports whether the agent's scratchpad carries a
// SKILL-RELOAD confirmation for exactly the given round. Every marker line is
// checked: an earlier round's confirmation usually sits above the current one in
// the same section, and matching only the first line made every round after the
// first look unconfirmed, which escalated agents that had in fact reloaded.
func hasSkillReloadMarker(content string, round int) bool {
	want := strconv.Itoa(round)
	for _, m := range skillReloadMarkerRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 && m[1] == want {
			return true
		}
	}
	return false
}

// highestSkillReloadRound returns the largest SKILL-RELOAD round token present in
// content (0 when there is none).
func highestSkillReloadRound(content string) int {
	highest := 0
	for _, m := range skillReloadMarkerRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// yesloopSkillCheckState tracks the skill-check state machine for one agent.
// baselineOut/baselineTurns record the work-counters at the last reset, so the
// threshold is measured as a delta since confirmation (or since tracking began).
type yesloopSkillCheckState struct {
	state          int
	refireCount    int
	lastRelayAt    time.Time
	baselineOut    int
	baselineTurns  int
	round          int
	transitionedAt time.Time
}

// yesloopSkillCheckAgents is the in-memory store for skill-check states.
// Reset on daemon restart — that is acceptable per spec.
var (
	yesloopSkillCheckAgents   = make(map[string]*yesloopSkillCheckState)
	yesloopSkillCheckAgentsMu sync.Mutex
)

// resetYesloopSkillCheckState clears the skill-check agent map. Used in tests.
func resetYesloopSkillCheckState() {
	yesloopSkillCheckAgentsMu.Lock()
	yesloopSkillCheckAgents = make(map[string]*yesloopSkillCheckState)
	yesloopSkillCheckAgentsMu.Unlock()
}

// readSkillCheckScratchpad returns the scratchpad content for an agent's section,
// tagged with this layer's name for diagnostics (M-8).
func (h *Handler) readSkillCheckScratchpad(agent storage.Agent) string {
	sections, err := h.store.ScratchpadRead(agent.Project, agent.Section)
	if err != nil {
		log.Printf("[yesloop-skillcheck] scratchpad read error for %s: %v", agent.ID, err)
		return ""
	}
	if len(sections) == 0 {
		return ""
	}
	return sections[0].Content
}

// checkYesloopSkillCheck is the heartbeat-driven compaction-risk check for
// yesloop agents. Runs every 30s via startAgentHeartbeat.
func (h *Handler) checkYesloopSkillCheck() {
	agents, err := h.store.AgentList("")
	if err != nil {
		log.Printf("[yesloop-skillcheck] AgentList error: %v", err)
		return
	}
	for _, agent := range agents {
		h.checkOneSkillCheck(agent)
	}
}

// checkOneSkillCheck drives a single agent through the skill-check state machine.
// Lock discipline (done_guard L60-62): the mutex is held for map reads/writes
// only and released around socket/DB I/O (relay, notify) so a stuck agent
// socket cannot block the checks of concurrent agents.
func (h *Handler) checkOneSkillCheck(agent storage.Agent) {
	if agent.Status != "running" {
		h.pruneSkillCheckState(agent.ID)
		return
	}
	if !strings.HasPrefix(agent.Section, "yesloop-") {
		return
	}
	if agent.SessionID == "" || agent.Project == "" {
		return
	}
	// The guard relays into the agent's socket, so it needs the agent's own
	// process — not just "somebody holds this PID" (Learning #101907). Dead rows
	// are crashRecovery's case.
	if agentProcessGone(agent) {
		return
	}

	// Register the tracking state BEFORE the freeze check so the token/turn
	// baseline covers volume produced while frozen — otherwise the first
	// tick after the subagent returns would re-baseline and silently absorb
	// that work.
	yesloopSkillCheckAgentsMu.Lock()
	state, exists := yesloopSkillCheckAgents[agent.ID]
	if !exists {
		// Start tracking from the current counters so the threshold measures
		// work done from this point onward.
		state = &yesloopSkillCheckState{
			state:         yesloopSkillCheckStateTracking,
			baselineOut:   agent.OutputTokens,
			baselineTurns: agent.TurnsUsed,
		}
		yesloopSkillCheckAgents[agent.ID] = state
	}
	yesloopSkillCheckAgentsMu.Unlock()

	// Freeze while the agent is blocked in a task() subagent: opencode child
	// sessions queue injected relays as pending user messages, so no relay,
	// refire count, notice or interval advancement may happen this tick.
	// The freeze shifts lastRelayAt forward by the tick, so the refire
	// interval restarts when the subagent returns.
	if opencodeChildActiveFn(agent.OpencodeSessionID) {
		yesloopSkillCheckAgentsMu.Lock()
		if state.state == yesloopSkillCheckStateRemind {
			state.lastRelayAt = time.Now()
		}
		yesloopSkillCheckAgentsMu.Unlock()
		return
	}

	content := h.readSkillCheckScratchpad(agent)

	// Decisions are computed under the lock; I/O is deferred past the unlock.
	yesloopSkillCheckAgentsMu.Lock()
	var sendRelay bool
	var notify bool
	switch state.state {
	case yesloopSkillCheckStateTracking:
		outDelta := agent.OutputTokens - state.baselineOut
		turnDelta := agent.TurnsUsed - state.baselineTurns
		if outDelta >= skillCheckOutputTokenThreshold || turnDelta >= skillCheckTurnThreshold {
			// Start above any marker already in the section. The counter is
			// in-memory — it resets on daemon restart and whenever the row leaves
			// "running" — so a reused low token would let a marker from an earlier
			// window confirm this one without the agent having reloaded anything.
			if highest := highestSkillReloadRound(content); highest > state.round {
				state.round = highest
			}
			state.round++ // fresh token for this window
			h.transitionSkillCheckLocked(agent, state, yesloopSkillCheckStateRemind)
			state.lastRelayAt = time.Now()
			sendRelay = true
		}

	case yesloopSkillCheckStateRemind:
		if hasSkillReloadMarker(content, state.round) {
			h.transitionSkillCheckLocked(agent, state, yesloopSkillCheckStateConfirmed)
			log.Printf("[yesloop-skillcheck] agent %s (%s) confirmed SKILL-RELOAD round %d",
				agent.ID, agent.Section, state.round)
		} else {
			// Relay ONLY when a refire is actually counted (interval elapsed,
			// max not reached). Ticks inside the interval must send nothing,
			// otherwise every ~30s heartbeat floods the agent.
			sendRelay, notify = h.maybeSkillCheckRefireLocked(agent, state, "no current-round SKILL-RELOAD marker in scratchpad")
		}

	case yesloopSkillCheckStateConfirmed:
		// Round closed — by a marker or by the notice after the last refire.
		// Reset the baseline to current counters and resume tracking for the next
		// compaction window.
		state.baselineOut = agent.OutputTokens
		state.baselineTurns = agent.TurnsUsed
		h.transitionSkillCheckLocked(agent, state, yesloopSkillCheckStateTracking)
	}
	// Relay metadata is read under the lock; state is NOT read after unlock.
	relayRound := state.round
	yesloopSkillCheckAgentsMu.Unlock()

	if notify {
		h.notifySkillCheckNotice(agent, relayRound)
		return
	}
	if sendRelay {
		h.sendSkillCheckRelay(agent, relayRound)
	}
}

// pruneSkillCheckState drops the tracking entry for an agent that is no longer
// running, keeping the in-memory map bounded across daemon uptime (M-7).
func (h *Handler) pruneSkillCheckState(agentID string) {
	yesloopSkillCheckAgentsMu.Lock()
	delete(yesloopSkillCheckAgents, agentID)
	yesloopSkillCheckAgentsMu.Unlock()
}

// transitionSkillCheckLocked moves the agent to a new skill-check state.
// Caller must hold yesloopSkillCheckAgentsMu.
func (h *Handler) transitionSkillCheckLocked(agent storage.Agent, state *yesloopSkillCheckState, newState int) {
	oldName := yesloopSkillCheckStateNames[state.state]
	newName := yesloopSkillCheckStateNames[newState]
	log.Printf("[yesloop-skillcheck] agent %s (%s) state %s -> %s",
		agent.ID, agent.Section, oldName, newName)
	state.state = newState
	state.refireCount = 0
	state.transitionedAt = time.Now()
}

// maybeSkillCheckRefireLocked re-fires the reminder decision: counts a refire
// when the interval has elapsed and reports (refired, notify). Ticks inside the
// interval report neither — no relay may be sent for them. Reaching the refire
// max reports the round once (notify) and closes it like a confirmation; the
// agent keeps running. Caller must hold yesloopSkillCheckAgentsMu.
func (h *Handler) maybeSkillCheckRefireLocked(agent storage.Agent, state *yesloopSkillCheckState, reason string) (refired bool, notify bool) {
	if time.Since(state.lastRelayAt) < yesloopSkillCheckRefireInterval {
		return false, false
	}
	state.refireCount++
	if state.refireCount >= yesloopSkillCheckMaxRefires {
		log.Printf("[yesloop-skillcheck] agent %s (%s) NOTICE: %s (refireCount=%d, max=%d) — agent keeps running, round closed",
			agent.ID, agent.Section, reason, state.refireCount, yesloopSkillCheckMaxRefires)
		// Close the round the way a confirmation would. The baseline resets on
		// the next tick, so a later window of work opens a fresh round instead of
		// leaving the reminder dead for the rest of the run.
		h.transitionSkillCheckLocked(agent, state, yesloopSkillCheckStateConfirmed)
		return false, true
	}
	state.lastRelayAt = time.Now()
	return true, false
}

// notifySkillCheckNotice tells the agent's caller once per round that the agent
// works on without a skill confirmation. It is information, not a gate: no
// pause, no status change, nothing the orchestrator has to do. The phase
// contract is checked at DONE. The round token is in the text so two notices
// from the same run are distinguishable.
func (h *Handler) notifySkillCheckNotice(agent storage.Agent, round int) {
	content := fmt.Sprintf(
		"INFO: Agent %s (%s) works on after %d skill reload reminders without SKILL-RELOAD-%d: yes. No action needed: the skill check never pauses an agent, and the phase contract is checked at DONE.",
		safeAgentText(agent.ID), safeAgentText(agent.Section), yesloopSkillCheckMaxRefires, round)
	if !h.notifyOrchestrator(agent, content) {
		log.Printf("[yesloop-skillcheck] notice for agent %s was not accepted by the caller channel", safeAgentText(agent.ID))
	}
}

// sendSkillCheckRelay sends the skill-check relay for the current round to a
// yesloop agent via the inject socket. Single metachar-free write.
func (h *Handler) sendSkillCheckRelay(agent storage.Agent, round int) {
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[yesloop-skillcheck] relay to agent %s skipped: no derivable socket path", safeAgentText(agent.ID))
		return
	}

	wrapped := fmt.Sprintf("[RELAY from=yesloop-skillcheck] %s", skillCheckRelayMessage(round))

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[yesloop-skillcheck] relay to agent %s failed: %v", agent.ID, err)
		return
	}
	defer conn.Close()

	conn.Write([]byte(wrapped + "\r\n"))
}
