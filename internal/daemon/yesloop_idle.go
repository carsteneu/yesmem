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
	yesloopIdleTimeout       = 10 * time.Minute
	yesloopIdleMaxRefires    = 2
	yesloopIdleRefireInterval = 90 * time.Second
)

// yesloopIdleState tracks the idle state machine for a single yesloop agent.
type yesloopIdleState struct {
	state          int
	refireCount    int
	lastRelayAt    time.Time
	idleSince      time.Time
	transitionedAt time.Time
}

// yesloopIdleAgents is the in-memory store for yesloop idle states.
// Reset on daemon restart — that is acceptable per spec.
var (
	yesloopIdleAgents   = make(map[string]*yesloopIdleState)
	yesloopIdleAgentsMu sync.Mutex
)

// resetYesloopIdleState clears the idle agent map. Used in tests.
func resetYesloopIdleState() {
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents = make(map[string]*yesloopIdleState)
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
	for _, agent := range agents {
		h.checkOneAgent(agent)
	}
}

func (h *Handler) checkOneAgent(agent storage.Agent) {
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

	// PID must be alive (otherwise crashRecovery handles it)
	if !isPIDAlive(agent.PID) {
		return
	}

	// Get stream state from in-memory tracking
	streamFields := h.getStreamFields(agent.SessionID)
	streamActive, _ := streamFields["stream_active"].(bool)

	yesloopIdleAgentsMu.Lock()
	defer yesloopIdleAgentsMu.Unlock()

	state, exists := yesloopIdleAgents[agent.ID]
	if !exists {
		state = &yesloopIdleState{state: yesloopIdleStateWorking}
		yesloopIdleAgents[agent.ID] = state
	}

	// Reset to WORKING if stream is active
	if streamActive {
		state.state = yesloopIdleStateWorking
		state.refireCount = 0
		state.idleSince = time.Time{}
		return
	}

	// Stream is inactive — set idle timestamp on first observation
	if state.idleSince.IsZero() {
		state.idleSince = time.Now()
		return
	}

	idleDuration := time.Since(state.idleSince)
	if idleDuration < yesloopIdleTimeout {
		return // not idle long enough yet
	}

	// Drive state machine
	switch state.state {
	case yesloopIdleStateWorking:
		h.transitionTo(agent, state, yesloopIdleStateSelfCheck)
		h.sendIdleRelay(agent, state, 1)

	case yesloopIdleStateSelfCheck:
		content := h.readAgentScratchpad(agent)
		if content != "" && strings.Contains(strings.ToUpper(content), "PROVEN") {
			h.transitionTo(agent, state, yesloopIdleStateRemarkRequest)
			h.sendIdleRelay(agent, state, 2)
			return
		}
		h.maybeRefire(agent, state, 1, "no PROVEN marker in scratchpad")

	case yesloopIdleStateRemarkRequest:
		content := h.readAgentScratchpad(agent)
		if content != "" && CountCompletedPhases(content) >= 6 && phase5ColdReviewPresent(content) {
			h.transitionTo(agent, state, yesloopIdleStateCommitRequest)
			h.sendIdleRelay(agent, state, 3)
			return
		}
		h.maybeRefire(agent, state, 2, "phases not completed or phase 5 cold review missing")

	case yesloopIdleStateCommitRequest:
		content := h.readAgentScratchpad(agent)
		if content != "" && strings.Contains(content, "send_to orchestrator:") {
			h.transitionTo(agent, state, yesloopIdleStateDone)
			log.Printf("[yesloop-idle] agent %s (%s) completed idle protocol", agent.ID, agent.Section)
			h.notifyOrchestrator(agent, fmt.Sprintf("Agent %s (%s) completed idle protocol DONE", agent.ID, agent.Section))
			return
		}
		h.maybeRefire(agent, state, 3, "no completion evidence")

	case yesloopIdleStateDone:
		// Terminal state — no further action
		return
	}
}

// transitionTo moves the agent to a new idle state and logs the transition.
func (h *Handler) transitionTo(agent storage.Agent, state *yesloopIdleState, newState int) {
	oldName := stateNames[state.state]
	newName := stateNames[newState]
	log.Printf("[yesloop-idle] agent %s (%s) state %s -> %s", agent.ID, agent.Section, oldName, newName)
	state.state = newState
	state.refireCount = 0
	state.transitionedAt = time.Now()
}

// maybeRefire checks if a re-fire is needed and escalates to DEAD_AGENT if
// max re-fires are exceeded.
func (h *Handler) maybeRefire(agent storage.Agent, state *yesloopIdleState, relayNum int, reason string) {
	if time.Since(state.lastRelayAt) < yesloopIdleRefireInterval {
		return
	}
	state.refireCount++
	if state.refireCount > yesloopIdleMaxRefires {
		state.state = yesloopIdleStateDone
		log.Printf("[yesloop-idle] agent %s (%s) ESCALATION: %s (refireCount=%d, max=%d)",
			agent.ID, agent.Section, reason, state.refireCount, yesloopIdleMaxRefires)
		h.freezeAgent(agent.ID, fmt.Sprintf("yesloop-idle escalation: %s", reason))
		h.notifyOrchestrator(agent, fmt.Sprintf("DEAD_AGENT: Agent %s (%s) idle escalation - %s", agent.ID, agent.Section, reason))
		return
	}
	h.sendIdleRelay(agent, state, relayNum)
}

// readAgentScratchpad returns the scratchpad content for an agent's section.
func (h *Handler) readAgentScratchpad(agent storage.Agent) string {
	sections, err := h.store.ScratchpadRead(agent.Project, agent.Section)
	if err != nil {
		log.Printf("[yesloop-idle] scratchpad read error for %s: %v", agent.ID, err)
		return ""
	}
	if len(sections) == 0 {
		return ""
	}
	return sections[0].Content
}

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
	lower := strings.ToLower(block)
	// Hard veto: any "blocked" in the Phase 5 block means the agent did not
	// complete Stage 2 — regardless of what else the block says.
	if strings.Contains(lower, "blocked") {
		return false
	}
	// Phase 5 block must contain at least one of these positive trace markers.
	stage2Re := regexp.MustCompile(`(?i)\b(stage 2|cold review|task\(\)|subagent)\b`)
	return stage2Re.MatchString(block)
}

// sendIdleRelay sends a relay message to a yesloop agent via the inject socket.
// Uses a single-write approach (without 3s delay) since the agent is already idle
// and the message doesn't require bracketed-paste splitting.
func (h *Handler) sendIdleRelay(agent storage.Agent, state *yesloopIdleState, relayNum int) {
	var msg string
	switch relayNum {
	case 1:
		msg = "Have you completed all 6 phases? If not do it now. For each phase prove you have done each, IF you have proven mark each phase in scratchpad with [x] showing it is done. MANDATORY: Make sure that you have also done phase 5 with all code reviews including Stage 2 cold review via task subagent. REVIEW BLOCKED without subagent trace is not acceptable. Mandatory: only mark as PROVEN if it IS proven."
	case 2:
		msg = "Mark all 6 phases as done with x in scratchpad."
	case 3:
		msg = "If 1 through 6 are ok commit and send_to to caller."
	}

	if agent.SockPath == "" {
		log.Printf("[yesloop-idle] relay %d to agent %s skipped: no sock_path", relayNum, agent.ID)
		return
	}

	injectPath := agent.SockPath + ".inject"
	wrapped := fmt.Sprintf("[RELAY from=yesloop-idle] %s", msg)

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[yesloop-idle] relay %d to agent %s failed: %v", relayNum, agent.ID, err)
		return
	}
	defer conn.Close()

	// Single write: content + submit in one go. The agent's PTY buffers this.
	conn.Write([]byte(wrapped + "\r\n"))
	state.lastRelayAt = time.Now()
}

// notifyOrchestrator sends a status message to the agent's caller_session.
func (h *Handler) notifyOrchestrator(agent storage.Agent, content string) {
	if agent.CallerSession == "" {
		return
	}
	h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target":   agent.CallerSession,
			"content":  content,
			"msg_type": "status",
		},
	})
}
