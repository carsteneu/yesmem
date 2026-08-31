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
	stagnationStatePaused
)

const (
	stagnationThreshold      = 1 * time.Hour
	stagnationMaxRefires     = 3
	stagnationRefireInterval = 30 * time.Minute
)

type stagnationState struct {
	state         int
	lastSig       string
	lastChangedAt time.Time
	lastRelayAt   time.Time
	refireCount   int
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
	if agent.SockPath == "" {
		log.Printf("[stagnation] relay to agent %s skipped: no sock_path", agent.ID)
		return
	}
	injectPath := agent.SockPath + ".inject"
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
		if !isPIDAlive(agent.PID) {
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
		if CountCompletedPhases(content) >= 6 {
			continue
		}

		h.checkOneStagnationAgent(agent, content)
	}
}

// checkOneStagnationAgent is the per-agent tracking/refire state machine.
// Lock discipline mirrors checkOneDoneGuardAgent: mutex held for map access
// only, released around relay/pause I/O.
func (h *Handler) checkOneStagnationAgent(agent storage.Agent, content string) {
	sig := stagnationSig(content, agent.Progress)
	now := time.Now()

	yesloopStagnationAgentsMu.Lock()
	state, exists := yesloopStagnationAgents[agent.ID]

	// Progress: signature changed → reset to tracking.
	if !exists || sig != state.lastSig {
		yesloopStagnationAgents[agent.ID] = &stagnationState{
			state:         stagnationStateTracking,
			lastSig:       sig,
			lastChangedAt: now,
		}
		yesloopStagnationAgentsMu.Unlock()
		return
	}

	// Terminal: already escalated and paused — don't re-escalate on a later
	// unpause with an unchanged signal.
	if state.state == stagnationStatePaused {
		yesloopStagnationAgentsMu.Unlock()
		return
	}

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
		state.state = stagnationStatePaused
		reason := fmt.Sprintf("yesloop-stagnation escalation: no progress for %s after %d relays",
			staleFor.Round(time.Minute), state.refireCount)
		log.Printf("[stagnation] agent %s (%s) ESCALATION: %s", agent.ID, agent.Section, reason)
		yesloopStagnationAgentsMu.Unlock()
		h.pauseAgent(agent.ID, reason)
		h.notifyOrchestrator(agent, fmt.Sprintf(
			"DEAD_AGENT: Agent %s (%s) paused by STAGNATION — no progress for %s while stream active.%s",
			agent.ID, agent.Section, staleFor.Round(time.Minute), orchestratorPauseHint))
		return
	}

	state.lastRelayAt = now
	yesloopStagnationAgentsMu.Unlock()
	log.Printf("[stagnation] agent %s (%s) refire %d/%d",
		agent.ID, agent.Section, state.refireCount, stagnationMaxRefires)
	h.sendStagnationRelay(agent, buildStagnationRelayMessage(staleFor))
}
