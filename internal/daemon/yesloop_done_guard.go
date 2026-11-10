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

// --- State machine for yesloop DONE-guard refire logic ---
//
// Layer 4 of the yesloop guarantee. When a yesloop agent has all 6 phase
// headers in its scratchpad but ValidatePhaseBlocks reports field errors,
// the guard does NOT immediately pause. Instead it relays the concrete
// missing field to the agent and re-fires up to doneGuardMaxRefires times
// at doneGuardRefireInterval intervals. Only after the max is exceeded is
// the agent paused. This gives cooperative agents a chance to fix cosmetic
// format errors (e.g. **Build:** instead of **Tests run:**) without being
// killed. State is in-memory and lost on daemon restart — acceptable per
// spec, mirrors yesloop_idle.go.

const (
	doneGuardStateRefiring = iota
	doneGuardStatePaused
)

const (
	doneGuardMaxRefires     = 2
	doneGuardRefireInterval = 90 * time.Second
)

// doneGuardState tracks the refire state for a single agent.
type doneGuardState struct {
	state       int
	refireCount int
	lastRelayAt time.Time
	// subagentFrozenAt is when the freeze was last reported, so a frozen guard
	// reports itself on a clock instead of once per tick.
	subagentFrozenAt time.Time
	// relayed records that a relay was already sent for this row. The refire
	// counter cannot answer that: the freeze branch registers a row before any
	// relay went out, so refireCount 0 does not mean the first relay is owed.
	relayed bool
}

var (
	yesloopDoneGuardAgents   = make(map[string]*doneGuardState)
	yesloopDoneGuardAgentsMu sync.Mutex
)

// resetDoneGuardState clears the done-guard state map. Used in tests.
func resetDoneGuardState() {
	yesloopDoneGuardAgentsMu.Lock()
	yesloopDoneGuardAgents = make(map[string]*doneGuardState)
	yesloopDoneGuardAgentsMu.Unlock()
}

// pruneDoneGuardStates drops the bookkeeping of agents that no longer have a row.
// The map is in-memory and is only written for agents that failed validation, so
// before YM-285 one entry per such agent stayed until the daemon restarted. A row
// that still exists keeps its entry — a paused DONE-GUARD row is revalidated
// against it on every tick.
func (h *Handler) pruneDoneGuardStates(agents []storage.Agent) {
	live := make(map[string]struct{}, len(agents))
	for i := range agents {
		live[agents[i].ID] = struct{}{}
	}
	yesloopDoneGuardAgentsMu.Lock()
	defer yesloopDoneGuardAgentsMu.Unlock()
	for id := range yesloopDoneGuardAgents {
		if _, ok := live[id]; !ok {
			delete(yesloopDoneGuardAgents, id)
		}
	}
}

// checkOneDoneGuardAgent is the per-agent refire state machine. Called from
// heartbeat.checkYesloopDoneGuard with the agent's current validation result.
// The separate signature lets tests drive the machine without going through
// the AgentList loop.
//
// Lock discipline: the state map mutex is held for map reads/writes only.
// It is released around pauseAgent/notifyOrchestrator/sendDoneGuardRelay
// (which may block on DB or socket I/O) to avoid blocking concurrent agents.
func (h *Handler) checkOneDoneGuardAgent(agent storage.Agent, result ValidationResult) {
	// Phase 5 evidence verification: when the format is compliant, verify the
	// reported review subagent session ids against opencode's own session DB.
	// An agent whose session was never captured would skip verification
	// silently, so an unbound opencode agent is bound here first — the evidence
	// check is worthless if it is skipped exactly for the agents the bind fix
	// exists for. A failure downgrades to a non-compliant result that feeds the
	// same refire/pause machine as a format failure, naming the invalid field.
	var verifyHandled bool
	if result.Compliant && len(result.Phase5Evidence) > 0 {
		agent = *h.ensureOpencodeSession(&agent)
	}
	if result.Compliant && len(result.Phase5Evidence) > 0 {
		if evErrs := h.verifyPhaseEvidence(agent, result.Phase5Evidence); len(evErrs) > 0 {
			result = ValidationResult{
				Compliant:      false,
				PhaseCount:     donePhaseCount,
				FieldErrors:    evErrs,
				Phase5Evidence: result.Phase5Evidence,
			}
		}
	}

	// Read before the lock: the child lookup hits opencode's session table. Only
	// a failing validation leads to a relay, so the lookup is skipped for the
	// compliant (and the recovery) path. An opencode agent whose session id is
	// not bound yet has no child signal at all, so it is bound on demand here —
	// rate-limited per agent, like the evidence check above.
	if result.Compliant {
		// The format is fine, so the report is the only thing left to doubt: the
		// verification stage takes over here and holds the agent until its
		// verdict. It is called before the state lock, because it reads the
		// scratchpad, writes the agent row and enqueues a job. False means the
		// claim is out of scope, and the recovery path runs unchanged.
		verifyHandled = h.gateVerifyCompliantDone(agent)
	}
	if !result.Compliant {
		agent = *h.ensureOpencodeSession(&agent)
	}
	inSubagent := !result.Compliant && h.agentWaitingInSubagent(agent)

	yesloopDoneGuardAgentsMu.Lock()

	state, exists := yesloopDoneGuardAgents[agent.ID]

	// Agent fixed the format issue → clear state and stop tracking.
	// Additionally, if the agent was paused by DONE-GUARD but the scratchpad
	// became compliant post-pause (e.g. agent continued working, daemon
	// restart lost in-memory state), unpause it so the status reflects reality.
	if result.Compliant {
		if exists {
			log.Printf("[done-guard] agent %s (%s) now compliant — clearing refire state",
				safeAgentText(agent.ID), safeAgentText(agent.Section))
			delete(yesloopDoneGuardAgents, agent.ID)
		}
		if verifyHandled {
			// The verification state machine owns the agent from here: releasing
			// it belongs to the verdict, not to the format check.
			yesloopDoneGuardAgentsMu.Unlock()
			return
		}
		if agent.Status == "paused" && strings.HasPrefix(agent.Progress, "paused: DONE-GUARD:") {
			log.Printf("[done-guard] agent %s (%s) RECOVERED: scratchpad now compliant — unpausing (was: %s)",
				safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(agent.Progress))
			yesloopDoneGuardAgentsMu.Unlock()
			h.unpauseAgent(agent.ID, "DONE-GUARD", fmt.Sprintf("scratchpad now compliant (was: %s)", safeAgentText(agent.Progress)))
			h.notifyOrchestrator(agent, fmt.Sprintf(
				"RECOVERED: agent %s (%s) unpaused by DONE-GUARD — scratchpad now compliant (was: %s)",
				safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(agent.Progress)))
			return
		}
		yesloopDoneGuardAgentsMu.Unlock()
		return
	}

	// A relay into an agent blocked inside a task() subagent only queues as a
	// pending user message and hits it after the subagent returns (#89428), and a
	// pause would stop an agent in the middle of the review its child is running.
	// Nothing is relayed and nothing is counted while the child runs; the refire
	// window restarts when it ends, and the relay the guard owes the agent then
	// fires on the first tick after the child is gone.
	if inSubagent {
		if !exists {
			state = &doneGuardState{state: doneGuardStateRefiring}
			yesloopDoneGuardAgents[agent.ID] = state
			exists = true
		}
		if now := time.Now(); now.Sub(state.subagentFrozenAt) >= subagentFreezeLogEvery {
			log.Printf("[done-guard] agent %s (%s) frozen: a task() subagent is active",
				safeAgentText(agent.ID), safeAgentText(agent.Section))
			state.subagentFrozenAt = now
		}
		state.lastRelayAt = time.Now()
		yesloopDoneGuardAgentsMu.Unlock()
		return
	}
	if exists && !state.subagentFrozenAt.IsZero() {
		state.subagentFrozenAt = time.Time{}
		if !state.relayed {
			// Nothing was relayed for this row yet: the first relay belongs to the
			// initial-detection path below, with its own counter.
			delete(yesloopDoneGuardAgents, agent.ID)
			exists = false
		} else {
			// The next relay, or the escalation its refire budget already earned,
			// is owed; do not wait for the interval.
			state.lastRelayAt = time.Time{}
		}
	}

	// First detection: register state and send initial relay (refireCount=0).
	if !exists {
		state = &doneGuardState{
			state:       doneGuardStateRefiring,
			lastRelayAt: time.Now(),
			relayed:     true,
		}
		yesloopDoneGuardAgents[agent.ID] = state
		log.Printf("[done-guard] agent %s (%s) validation failed — sending initial relay (errors: %s)",
			safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(summarizeErrors(result)))
		yesloopDoneGuardAgentsMu.Unlock()
		h.sendDoneGuardRelay(agent, result)
		return
	}

	if state.state != doneGuardStateRefiring {
		yesloopDoneGuardAgentsMu.Unlock()
		return
	}

	// Interval-gated: not enough time since last relay.
	if time.Since(state.lastRelayAt) < doneGuardRefireInterval {
		yesloopDoneGuardAgentsMu.Unlock()
		return
	}

	// Interval elapsed — increment refire count.
	state.refireCount++
	if state.refireCount > doneGuardMaxRefires {
		// Pause. Terminal state for this agent's tracking.
		state.state = doneGuardStatePaused
		reason := fmt.Sprintf("DONE-GUARD: phase validation failed — %s", safeAgentText(summarizeErrors(result)))
		log.Printf("[done-guard] agent %s (%s) ESCALATION: pausing after %d refires (errors: %s)",
			safeAgentText(agent.ID), safeAgentText(agent.Section), state.refireCount, safeAgentText(summarizeErrors(result)))
		yesloopDoneGuardAgentsMu.Unlock()
		h.pauseAgent(agent.ID, reason)
		h.notifyOrchestrator(agent, fmt.Sprintf(
			"DEAD_AGENT: Agent %s (%s) paused by DONE-GUARD after %d relay attempts — phase validation failed: %s%s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), state.refireCount,
			safeAgentText(summarizeErrors(result)), orchestratorPauseHint))
		return
	}

	// Refire: send relay again, update timestamp.
	log.Printf("[done-guard] agent %s (%s) refire %d/%d (errors: %s)",
		safeAgentText(agent.ID), safeAgentText(agent.Section), state.refireCount, doneGuardMaxRefires, safeAgentText(summarizeErrors(result)))
	state.lastRelayAt = time.Now()
	state.relayed = true
	yesloopDoneGuardAgentsMu.Unlock()
	h.sendDoneGuardRelay(agent, result)
}

// buildDoneGuardRelayMessage constructs the relay body sent to the agent.
// Pure function — testable without socket I/O. The message is metachar-free
// (no markdown, no backticks) because it is written as a single line to the
// agent's PTY inject socket.
func buildDoneGuardRelayMessage(result ValidationResult) string {
	return fmt.Sprintf(
		"DONE-GUARD: phase validation failed. Missing or invalid fields: %s. "+
			"Fix the listed fields in your scratchpad phase blocks and call update_agent_status when done.",
		safeAgentText(summarizeErrors(result)))
}

// sendDoneGuardRelay sends the validation-failure relay to a yesloop agent
// via the PTY inject socket. Mirrors sendDoneVerifyRelay: callers set
// state.lastRelayAt BEFORE calling (or rely on the initial-relay path) so a
// failed dial still counts as a relay attempt for refire-gating purposes
// (prevents rapid escalation when sockets are flaky).
func (h *Handler) sendDoneGuardRelay(agent storage.Agent, result ValidationResult) {
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[done-guard] relay to agent %s skipped: no derivable socket path", safeAgentText(agent.ID))
		return
	}
	wrapped := fmt.Sprintf("[RELAY from=done-guard] %s", safeAgentText(buildDoneGuardRelayMessage(result)))

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[done-guard] relay to agent %s failed: %v", safeAgentText(agent.ID), safeAgentText(err.Error()))
		return
	}
	defer conn.Close()
	conn.Write([]byte(wrapped + "\r\n"))
}
