package daemon

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Layer 5: Stagnation Monitor ---
//
// AVO-inspired conditional intervention (arXiv 2603.24517, §3.3): detect
// agents that are actively working (stream active, PID alive) but produce
// no observable progress — no scratchpad writes, no update_agent_status
// changes — for stagnationThreshold. Covers day-scale runs where the
// 10-minute idle guard never fires because the stream stays active.
//
// The trigger is fully deterministic: the progress signal is a hash over
// (scratchpad content, agent.Progress). Any change resets tracking.
// Yes-loop discipline guarantees real progress writes SOMETHING (milestone
// scratchpad_write per SKILL.md), so an unchanged signal is evidence of
// stagnation, not of hidden work.
//
// Reset happens on PROGRESS only, never on stream blips (#82787 lesson).

const (
	stagnationStateTracking = iota
	stagnationStateRefiring
	stagnationStateNudged
)

const (
	stagnationThreshold      = 1 * time.Hour
	stagnationMaxRefires     = 3
	stagnationRefireInterval = 30 * time.Minute
)

type stagnationState struct {
	state         int
	lastSig       string
	lastContent   string
	lastChangedAt time.Time
	lastRelayAt   time.Time
	refireCount   int
	// subagentFrozenAt is when the freeze was last reported, so a frozen guard
	// reports itself on a clock instead of once per tick.
	subagentFrozenAt time.Time
}

var (
	yesloopStagnationAgents   = make(map[string]*stagnationState)
	yesloopStagnationAgentsMu sync.Mutex
)

// resetYesloopStagnationState clears the state map. Used in tests.
func resetYesloopStagnationState() {
	yesloopStagnationAgentsMu.Lock()
	defer yesloopStagnationAgentsMu.Unlock()
	yesloopStagnationAgents = make(map[string]*stagnationState)
}

// stagnationSig computes the deterministic progress signal for an agent:
// scratchpad content and the canonical progress string, concatenated. A plain
// string suffices — no hashing needed, scratchpads are small.
func stagnationSig(content, progress string) string {
	return content + "\x00" + progress
}

// buildStagnationRelayMessage constructs the relay body sent to the agent.
// Pure function — testable without socket I/O. Metachar-free (no markdown,
// no backticks) because it is written as a single line to the agent's PTY
// inject socket.
func buildStagnationRelayMessage(staleFor time.Duration) string {
	return fmt.Sprintf(
		"STAGNATION: no scratchpad update and no progress change for %s while your stream is active. Re-read your plan and scratchpad. If this phase is genuinely in progress write a status line with current evidence to the scratchpad now. If you are stuck change approach or write BLOCKED with the reason into the scratchpad.",
		staleFor.Round(time.Minute))
}

// sendStagnationRelay sends the stagnation relay via the PTY inject socket.
// Mirrors sendDoneGuardRelay.
func (h *Handler) sendStagnationRelay(agent storage.Agent, msg string) {
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[stagnation] relay to agent %s skipped: no derivable socket path", safeAgentText(agent.ID))
		return
	}
	wrapped := fmt.Sprintf("[RELAY from=stagnation-guard] %s", msg)

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[stagnation] relay to agent %s failed: %v", agent.ID, err)
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(wrapped + "\r\n")); err != nil {
		log.Printf("[stagnation] relay to agent %s write failed: %v", agent.ID, err)
	}
}

// checkYesloopStagnation is called every 30s from the heartbeat. It tracks
// running yesloop agents with an ACTIVE stream — the complement of the idle
// guard (inactive stream). DONE-claiming agents are skipped: DoneGuard and
// DoneVerify own the finished-pipeline territory.
func (h *Handler) checkYesloopStagnation() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	h.pruneStagnationStates(agents)
	for _, agent := range agents {
		if agent.Status != "running" {
			continue
		}
		if !strings.HasPrefix(agent.Section, "yesloop-") {
			continue
		}
		if agent.SessionID == "" || agent.Project == "" {
			continue
		}
		// The guard's action is a relay into the agent's socket, so it needs the
		// agent's own process: a recycled PID would be nudged on the strength of
		// a row it no longer owns (Learning #101907). Dead rows are
		// crashRecovery's case.
		if agentProcessGone(agent) {
			continue
		}

		streamFields := h.getStreamFields(agent.SessionID)
		streamActive, _ := streamFields["stream_active"].(bool)
		if !streamActive {
			continue // inactive stream → idle guard territory
		}

		content := h.readAgentScratchpad(agent)
		if content == "" {
			continue // nothing written yet — nothing to compare
		}

		// DONE territory: other guards own complete phase blocks.
		if CountCompletedPhases(content) >= donePhaseCount {
			continue
		}

		// The child gate is the expensive part (it opens opencode's session DB and
		// may bind an unbound agent), so it runs after every cheap filter. The
		// stream fields are already in hand, so only the authoritative lookup is
		// left to pay for.
		agent = *h.ensureOpencodeSession(&agent)
		inSubagent := h.agentWaitingInSubagentWith(agent, streamFields)

		h.checkOneStagnationAgent(agent, content, inSubagent)
	}
}

// pruneStagnationStates drops the bookkeeping of agents that no longer have a
// row. Same leak as the done-guard map before YM-285: the state is in-memory and
// outlives the row it was written for.
func (h *Handler) pruneStagnationStates(agents []storage.Agent) {
	live := make(map[string]struct{}, len(agents))
	for i := range agents {
		live[agents[i].ID] = struct{}{}
	}
	yesloopStagnationAgentsMu.Lock()
	defer yesloopStagnationAgentsMu.Unlock()
	for id := range yesloopStagnationAgents {
		if _, ok := live[id]; !ok {
			delete(yesloopStagnationAgents, id)
		}
	}
}

// checkOneStagnationAgent is the per-agent tracking/refire state machine.
// Lock discipline mirrors checkOneDoneGuardAgent: mutex held for map access
// only, released around relay/pause I/O.
func (h *Handler) checkOneStagnationAgent(agent storage.Agent, content string, inSubagent bool) {
	sig := stagnationSig(content, agent.Progress)
	now := time.Now()

	yesloopStagnationAgentsMu.Lock()
	state, exists := yesloopStagnationAgents[agent.ID]

	// Terminal: already nudged. The guard writes its note into the column its own
	// signal reads, so the question here is whether the agent's content moved, not
	// whether the note is there. The asymmetry is deliberate: a phase change in
	// progress alone no longer re-arms this ladder, the scratchpad does.
	if exists && state.state == stagnationStateNudged && content == state.lastContent {
		state.lastSig = sig
		yesloopStagnationAgentsMu.Unlock()
		return
	}

	// Progress: signature changed → reset to tracking.
	if !exists || sig != state.lastSig {
		yesloopStagnationAgents[agent.ID] = &stagnationState{
			state:         stagnationStateTracking,
			lastSig:       sig,
			lastContent:   content,
			lastChangedAt: now,
		}
		yesloopStagnationAgentsMu.Unlock()
		return
	}

	// An agent blocked inside a task() subagent is working, not stagnant: the
	// child is its work. A relay would only queue behind the child (#89428), so
	// nothing is sent and nothing is counted. The windows move forward, so the
	// threshold and the refire interval restart when the child ends instead of
	// firing on the very first tick after it returns. The reachable case is the
	// child's tail: while the child runs the parent has no stream of its own, and
	// the sweep above skips an inactive stream already.
	if inSubagent {
		if now.Sub(state.subagentFrozenAt) >= subagentFreezeLogEvery {
			log.Printf("[stagnation] agent %s (%s) frozen: a task() subagent is active",
				safeAgentText(agent.ID), safeAgentText(agent.Section))
			state.subagentFrozenAt = now
		}
		state.lastChangedAt = now
		if !state.lastRelayAt.IsZero() {
			state.lastRelayAt = now
		}
		yesloopStagnationAgentsMu.Unlock()
		return
	}
	state.subagentFrozenAt = time.Time{}

	// No change yet — past threshold?
	staleFor := now.Sub(state.lastChangedAt)
	if staleFor < stagnationThreshold {
		yesloopStagnationAgentsMu.Unlock()
		return
	}

	// First relay.
	if state.state != stagnationStateRefiring {
		state.state = stagnationStateRefiring
		state.lastRelayAt = now
		yesloopStagnationAgentsMu.Unlock()
		log.Printf("[stagnation] agent %s (%s) unchanged for %s — initial relay",
			agent.ID, agent.Section, staleFor.Round(time.Minute))
		h.sendStagnationRelay(agent, buildStagnationRelayMessage(staleFor))
		return
	}

	// Refire interval gating.
	if now.Sub(state.lastRelayAt) < stagnationRefireInterval {
		yesloopStagnationAgentsMu.Unlock()
		return
	}

	state.refireCount++
	if state.refireCount > stagnationMaxRefires {
		state.state = stagnationStateNudged
		reason := fmt.Sprintf("yesloop-stagnation escalation: no progress for %s after %d relays",
			staleFor.Round(time.Minute), state.refireCount)
		log.Printf("[stagnation] agent %s (%s) ESCALATION: %s", agent.ID, agent.Section, reason)
		yesloopStagnationAgentsMu.Unlock()
		if !h.nudgeAgent(agent.ID, reason) {
			skipNudgeNote("stagnation", agent.ID, agent.Section)
			return
		}
		h.notifyOrchestrator(agent, nudgeNotice(fmt.Sprintf("Agent %s (%s) is stagnant: no progress for %s while the stream is active",
			safeAgentText(agent.ID), safeAgentText(agent.Section), staleFor.Round(time.Minute))))
		return
	}

	state.lastRelayAt = now
	yesloopStagnationAgentsMu.Unlock()
	log.Printf("[stagnation] agent %s (%s) refire %d/%d",
		agent.ID, agent.Section, state.refireCount, stagnationMaxRefires)
	h.sendStagnationRelay(agent, buildStagnationRelayMessage(staleFor))
}
