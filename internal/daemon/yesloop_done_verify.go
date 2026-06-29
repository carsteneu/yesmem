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
// asks the agent to prove all 6 phases ran — especially Phase 5 Cold Review.
// The agent must reply with a BEWEISEN marker plus send_to evidence; otherwise
// the state machine re-fires the relay up to yesloopDoneVerifyMaxRefires times,
// then escalates to DEAD_AGENT_ESCALATION.
//
// Trigger: DONE-claim in scratchpad (not stream inactivity — that's yesloop_idle).
// States and helpers are intentionally modeled after yesloop_idle.go.

const (
	yesloopDoneVerifyStateNotDone = iota
	yesloopDoneVerifyStateVerifyRequested
	yesloopDoneVerifyStateDoneVerified
	yesloopDoneVerifyStateDeadAgentEscalation
)

var yesloopDoneVerifyStateNames = map[int]string{
	yesloopDoneVerifyStateNotDone:             "NOT_DONE",
	yesloopDoneVerifyStateVerifyRequested:     "VERIFY_REQUESTED",
	yesloopDoneVerifyStateDoneVerified:        "DONE_VERIFIED",
	yesloopDoneVerifyStateDeadAgentEscalation: "DEAD_AGENT_ESCALATION",
}

const (
	yesloopDoneVerifyMaxRefires     = 3
	yesloopDoneVerifyRefireInterval = 5 * time.Minute
)

// doneVerifyRelayMessage is the single source of truth for the verify relay.
// Metachar-free (no markdown, no backticks, no parens) because it is written
// as a single line to the agent's PTY inject socket. Package-level so tests
// can assert on the content without capturing the socket write.
const doneVerifyRelayMessage = "Hast du wirklich alle Phasen abgehakt. Vor allem Phase 5 Code Review inklusive Security Review via security-review Skill. Im Phase 5 Block muss zusaetzlich zu Stage 2 Cold Review ein Security field stehen mit Findings oder skip-reason. Falls nicht bitte durchfuehren und BEWEISEN dass alle Phasen durch sind und DANN Phase 6 Finish durchfuehren mit commit und send_to. KEIN auto-deploy."

// yesloopDoneVerifyState tracks the verify state machine for a single agent.
type yesloopDoneVerifyState struct {
	state          int
	refireCount    int
	lastRelayAt    time.Time
	transitionedAt time.Time
}

var (
	yesloopDoneVerifyAgents   = make(map[string]*yesloopDoneVerifyState)
	yesloopDoneVerifyAgentsMu sync.Mutex
)

// DONE-claim indicators. Match case-insensitively against the scratchpad.
// Trigger when ANY of these is present.
var doneClaimIndicators = []*regexp.Regexp{
	regexp.MustCompile(`(?im)^###\s+Phase\s+6\s*:`),
	regexp.MustCompile(`(?im)send_to[^\n]*\bDONE\b`),
	regexp.MustCompile(`(?im)Phase\s+6\s*/\s*6\b`),
	regexp.MustCompile(`(?im)^DONE:\s`),
}

// hasDoneClaim reports whether content carries any DONE-claim indicator.
func hasDoneClaim(content string) bool {
	for _, re := range doneClaimIndicators {
		if re.MatchString(content) {
			return true
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
// BEWEISEN marker AND send_to orchestrator line AND 6 phases COMPLETE.
func isVerified(content string) bool {
	return hasBewiesenMarker(content) &&
		hasSendToOrchestrator(content) &&
		CountCompletedPhases(content) >= 6
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

	yesloopDoneVerifyAgentsMu.Lock()
	defer yesloopDoneVerifyAgentsMu.Unlock()

	state, exists := yesloopDoneVerifyAgents[agent.ID]
	if !exists {
		// No state yet. Only start tracking when a DONE-claim appears.
		if content == "" || !hasDoneClaim(content) {
			return
		}
		state = &yesloopDoneVerifyState{state: yesloopDoneVerifyStateNotDone}
		yesloopDoneVerifyAgents[agent.ID] = state
	}

	switch state.state {
	case yesloopDoneVerifyStateNotDone:
		if hasDoneClaim(content) {
			h.transitionDoneVerify(agent, state, yesloopDoneVerifyStateVerifyRequested)
			h.sendDoneVerifyRelay(agent, state)
		}

	case yesloopDoneVerifyStateVerifyRequested:
		if isVerified(content) {
			h.transitionDoneVerify(agent, state, yesloopDoneVerifyStateDoneVerified)
			log.Printf("[yesloop-done-verify] agent %s (%s) verified DONE", agent.ID, agent.Section)
			h.notifyOrchestrator(agent, fmt.Sprintf(
				"Agent %s (%s) verified DONE — BEWEISEN + send_to + 6 phases COMPLETE",
				agent.ID, agent.Section))
			return
		}
		h.maybeDoneVerifyRefire(agent, state, "no BEWEISEN/send_to/6-phases evidence")

	case yesloopDoneVerifyStateDoneVerified:
		// Terminal — no further action.
		return

	case yesloopDoneVerifyStateDeadAgentEscalation:
		// Terminal — no further action.
		return
	}
}

// transitionDoneVerify moves the agent to a new verify state.
func (h *Handler) transitionDoneVerify(agent storage.Agent, state *yesloopDoneVerifyState, newState int) {
	oldName := yesloopDoneVerifyStateNames[state.state]
	newName := yesloopDoneVerifyStateNames[newState]
	log.Printf("[yesloop-done-verify] agent %s (%s) state %s -> %s",
		agent.ID, agent.Section, oldName, newName)
	state.state = newState
	state.refireCount = 0
	state.transitionedAt = time.Now()
}

// maybeDoneVerifyRefire checks if a re-fire is needed and escalates if the
// max re-fire count is exceeded.
func (h *Handler) maybeDoneVerifyRefire(agent storage.Agent, state *yesloopDoneVerifyState, reason string) {
	if time.Since(state.lastRelayAt) < yesloopDoneVerifyRefireInterval {
		return
	}
	state.refireCount++
	if state.refireCount >= yesloopDoneVerifyMaxRefires {
		log.Printf("[yesloop-done-verify] agent %s (%s) ESCALATION: %s (refireCount=%d, max=%d)",
			agent.ID, agent.Section, reason, state.refireCount, yesloopDoneVerifyMaxRefires)
		state.state = yesloopDoneVerifyStateDeadAgentEscalation
		state.transitionedAt = time.Now()
		h.freezeAgent(agent.ID, fmt.Sprintf("yesloop-done-verify escalation: %s", reason))
		h.notifyOrchestrator(agent, fmt.Sprintf(
			"DEAD_AGENT: Agent %s (%s) done-verify escalation — %s",
			agent.ID, agent.Section, reason))
		return
	}
	h.sendDoneVerifyRelay(agent, state)
}

// sendDoneVerifyRelay sends the verify relay to a yesloop agent via the inject
// socket. Single metachar-free write (no markdown, no backticks, no parens).
func (h *Handler) sendDoneVerifyRelay(agent storage.Agent, state *yesloopDoneVerifyState) {
	if agent.SockPath == "" {
		log.Printf("[yesloop-done-verify] relay to agent %s skipped: no sock_path", agent.ID)
		return
	}

	injectPath := agent.SockPath + ".inject"
	wrapped := fmt.Sprintf("[RELAY from=yesloop-done-verify] %s", doneVerifyRelayMessage)

	// Record the attempt before dialing — a failed dial still counts as a
	// relay attempt for refire-gating purposes, preventing rapid escalation.
	state.lastRelayAt = time.Now()

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[yesloop-done-verify] relay to agent %s failed: %v", agent.ID, err)
		return
	}
	defer conn.Close()

	conn.Write([]byte(wrapped + "\r\n"))
}
