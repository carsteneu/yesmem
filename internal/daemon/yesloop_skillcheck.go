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
// escalation path stays reachable after the first confirmation.

const (
	yesloopSkillCheckStateTracking = iota
	yesloopSkillCheckStateRemind
	yesloopSkillCheckStateConfirmed
	yesloopSkillCheckStateDeadAgentEscalation
)

var yesloopSkillCheckStateNames = map[int]string{
	yesloopSkillCheckStateTracking:            "TRACKING",
	yesloopSkillCheckStateRemind:              "REMIND",
	yesloopSkillCheckStateConfirmed:           "CONFIRMED",
	yesloopSkillCheckStateDeadAgentEscalation: "DEAD_AGENT_ESCALATION",
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
// SKILL-RELOAD confirmation for exactly the given round.
func hasSkillReloadMarker(content string, round int) bool {
	m := skillReloadMarkerRe.FindStringSubmatch(content)
	if len(m) < 2 {
		return false
	}
	return m[1] == strconv.Itoa(round)
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
// only and released around socket/DB I/O (relay, pause, notify) so a stuck
// agent socket cannot block the checks of concurrent agents.
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
	// PID must be alive (otherwise crashRecovery handles it).
	if !isPIDAlive(agent.PID) {
		return
	}

	content := h.readSkillCheckScratchpad(agent)

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

	// Decisions are computed under the lock; I/O is deferred past the unlock.
	var sendRelay bool
	var escalate bool
	switch state.state {
	case yesloopSkillCheckStateTracking:
		outDelta := agent.OutputTokens - state.baselineOut
		turnDelta := agent.TurnsUsed - state.baselineTurns
		if outDelta >= skillCheckOutputTokenThreshold || turnDelta >= skillCheckTurnThreshold {
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
			escalate = h.maybeSkillCheckRefireLocked(agent, state, "no current-round SKILL-RELOAD marker in scratchpad")
			if !escalate {
				sendRelay = true
			}
		}

	case yesloopSkillCheckStateConfirmed:
		// Confirmation received — reset the baseline to current counters and
		// resume tracking for the next compaction window.
		state.baselineOut = agent.OutputTokens
		state.baselineTurns = agent.TurnsUsed
		h.transitionSkillCheckLocked(agent, state, yesloopSkillCheckStateTracking)

	case yesloopSkillCheckStateDeadAgentEscalation:
		// Terminal — no further action.
		yesloopSkillCheckAgentsMu.Unlock()
		return
	}
	yesloopSkillCheckAgentsMu.Unlock()

	if escalate {
		h.pauseAgent(agent.ID, fmt.Sprintf("yesloop-skillcheck escalation: no current-round SKILL-RELOAD marker"))
		h.notifyOrchestrator(agent, fmt.Sprintf(
			"DEAD_AGENT: Agent %s (%s) skillcheck escalation — no current-round SKILL-RELOAD marker%s",
			agent.ID, agent.Section, orchestratorPauseHint))
		return
	}
	if sendRelay {
		h.sendSkillCheckRelay(agent, state.round)
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

// maybeSkillCheckRefireLocked re-fires the reminder decision: increments the
// refire counter when the interval has elapsed and reports whether the max has
// been reached (escalation). Caller must hold yesloopSkillCheckAgentsMu.
func (h *Handler) maybeSkillCheckRefireLocked(agent storage.Agent, state *yesloopSkillCheckState, reason string) bool {
	if time.Since(state.lastRelayAt) < yesloopSkillCheckRefireInterval {
		return false
	}
	state.refireCount++
	if state.refireCount >= yesloopSkillCheckMaxRefires {
		log.Printf("[yesloop-skillcheck] agent %s (%s) ESCALATION: %s (refireCount=%d, max=%d)",
			agent.ID, agent.Section, reason, state.refireCount, yesloopSkillCheckMaxRefires)
		state.state = yesloopSkillCheckStateDeadAgentEscalation
		state.transitionedAt = time.Now()
		return true
	}
	state.lastRelayAt = time.Now()
	return false
}

// sendSkillCheckRelay sends the skill-check relay for the current round to a
// yesloop agent via the inject socket. Single metachar-free write.
func (h *Handler) sendSkillCheckRelay(agent storage.Agent, round int) {
	if agent.SockPath == "" {
		log.Printf("[yesloop-skillcheck] relay to agent %s skipped: no sock_path", agent.ID)
		return
	}

	injectPath := agent.SockPath + ".inject"
	wrapped := fmt.Sprintf("[RELAY from=yesloop-skillcheck] %s", skillCheckRelayMessage(round))

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[yesloop-skillcheck] relay to agent %s failed: %v", agent.ID, err)
		return
	}
	defer conn.Close()

	conn.Write([]byte(wrapped + "\r\n"))
}
