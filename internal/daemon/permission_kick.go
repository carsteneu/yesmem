package daemon

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// Permission-kick: agents occasionally hang waiting on an opencode permission
// dialog (no LLM stream running, PID alive). enter confirms the dialog's
// default selection ("Allow once"); on an empty input line it is a no-op.
// The checker sends a double-enter per the two-connection pattern from
// handleRelayAgent (bracketed paste swallows \r within a single write).
//
// Scope (C2): agents in a live status whose PID is *verified* as their own
// backend process. Design B (2026-09-08) kicked every agent with a live PID
// "regardless of status" so a late permission dialog could not strand a
// guard-paused agent — but isPIDAlive alone cannot tell the agent's own process
// from a zombie or a recycled PID, so agents stopped weeks earlier kept being
// kicked. The reachability intent survives: paused is still in scope, terminal
// statuses are not. See isAgentProcess (heartbeat.go).
//
// Enter reaches whatever the agent's PTY fronts — an agent inside an
// interactive child process that reads stdin (build tool prompt, input())
// has its defaults confirmed on kick. The no-op guarantee only holds for the
// TUI's empty input line.

const (
	defaultPermKickDelay    = 3 * time.Minute // stream-idle before the first kick
	defaultPermKickInterval = 5 * time.Minute // re-kick interval (flood guard)
)

// permKickStatuses are the statuses whose process may be kicked: the agent
// still owns it, so a stalled permission dialog can still be resolved. Terminal
// statuses (stopped, finished, failed, crashed, error) are out of scope — their
// PID may already belong to an unrelated process.
var permKickStatuses = map[string]bool{
	"running": true,
	"paused":  true,
	"pending": true,
}

// permKickPause separates the two enter connections so the TUI treats them
// as two keypresses, not one bracketed paste block. Var for testability.
var permKickPause = 2 * time.Second

// heldByVerification reports whether the row is paused on purpose by the DONE
// gate's verification stage. Such a pause is a decision, not a stalled dialog:
// the agent is meant to stay off until a verdict arrives, so a live stream must
// not flip it back (unpauseAgent would also overwrite the progress the gate
// reads back after a restart).
func heldByVerification(progress string) bool {
	return strings.HasPrefix(progress, "paused: DONE-PENDING-VERIFY:") ||
		strings.HasPrefix(progress, "paused: DONE-VERIFY-BLOCKED")
}

// permKickState tracks one agent's stream activity and kick history.
// In-memory only, reset on daemon restart — the first observation seeds
// lastStreamActiveAt with "now", giving agents a grace window after restart.
type permKickState struct {
	lastStreamActiveAt time.Time
	lastKickAt         time.Time
}

var (
	permKickAgents   = make(map[string]*permKickState)
	permKickAgentsMu sync.Mutex
)

// resetPermissionKickState clears the kick state map. Used in tests.
func resetPermissionKickState() {
	permKickAgentsMu.Lock()
	permKickAgents = make(map[string]*permKickState)
	permKickAgentsMu.Unlock()
}

// checkPermissionKick is the heartbeat-driven kick for permission-stuck agents.
// Runs every 30s via h.startAgentHeartbeat. It selects the agents in scope —
// live status and a verified backend process — and hands each to
// checkPermissionKickAgent.
func (h *Handler) checkPermissionKick() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	live := make(map[string]bool, len(agents))
	for _, agent := range agents {
		// Polarity: the kick acts when the process is *not definitively gone*.
		// Its only action is an Enter keypress into the agent's own socket
		// (SockPath+".inject"), which a recycled PID cannot redirect into a
		// stranger's PTY, so a platform that cannot establish identity keeps the
		// pre-C2 gate (signal 0) instead of losing the kick altogether.
		if !permKickStatuses[agent.Status] || agentProcessGone(agent) {
			continue
		}
		live[agent.ID] = true
		h.checkPermissionKickAgent(agent)
	}
	// Entries of agents whose process is gone have no future — drop them.
	permKickAgentsMu.Lock()
	for id := range permKickAgents {
		if !live[id] {
			delete(permKickAgents, id)
		}
	}
	permKickAgentsMu.Unlock()
}

// checkPermissionKickAgent evaluates one in-scope agent. The caller verified
// its status and process identity.
func (h *Handler) checkPermissionKickAgent(agent storage.Agent) {
	if agent.SessionID == "" || h.agentInjectPath(agent.ID) == "" {
		return
	}
	if !h.permissionKickEnabled(agent) {
		return
	}

	streamFields := h.getStreamFields(agent.SessionID)
	streamActive, _ := streamFields["stream_active"].(bool)

	delay := h.agentPermissionKickDelay
	if delay <= 0 {
		delay = defaultPermKickDelay
	}
	interval := h.agentPermissionKickInterval
	if interval <= 0 {
		interval = defaultPermKickInterval
	}

	permKickAgentsMu.Lock()
	state, exists := permKickAgents[agent.ID]
	if !exists {
		state = &permKickState{lastStreamActiveAt: time.Now()}
		permKickAgents[agent.ID] = state
	}
	if streamActive {
		state.lastStreamActiveAt = time.Now()
		permKickAgentsMu.Unlock()
		// A live stream proves the agent works again — flip a guard-paused
		// agent back to running so the other guards resume watching it.
		if agent.Status == "paused" && !heldByVerification(agent.Progress) {
			h.unpauseAgent(agent.ID, "PERM-KICK", "stream recovered after kick")
		}
		return
	}
	kickDue := false
	if state.lastKickAt.IsZero() {
		kickDue = time.Since(state.lastStreamActiveAt) >= delay
	} else {
		kickDue = time.Since(state.lastKickAt) >= interval
	}
	permKickAgentsMu.Unlock()

	// Kick outside the mutex — the send sleeps ~2s and must not hold
	// the state lock against other map readers.
	if kickDue {
		h.sendPermissionKick(agent)
		// Arm the interval gate regardless of dial success: a dead socket
		// must not turn the checker into a per-tick retry storm (#89414).
		permKickAgentsMu.Lock()
		state.lastKickAt = time.Now()
		permKickAgentsMu.Unlock()
	}
}

// permissionKickEnabled resolves the effective setting: the per-agent override
// wins ("on"/"off"), an empty column inherits the config default.
func (h *Handler) permissionKickEnabled(agent storage.Agent) bool {
	switch agent.PermissionKick {
	case "on":
		return true
	case "off":
		return false
	default:
		return h.agentPermissionKick
	}
}

// sendPermissionKick sends enter on two separate connections: first \r
// (confirms "Allow once" in the dialog, no-op on an empty input line),
// then after a pause another \r in case bracketed paste swallowed the first.
func (h *Handler) sendPermissionKick(agent storage.Agent) bool {
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		return false
	}

	conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[perm-kick] agent %s (%s) kick failed: %v", agent.ID, agent.Section, err)
		return false
	}
	conn.Write([]byte("\r"))
	conn.Close()

	time.Sleep(permKickPause)

	conn2, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		log.Printf("[perm-kick] agent %s (%s) kick (second enter) failed: %v", agent.ID, agent.Section, err)
		return false
	}
	conn2.Write([]byte("\r"))
	conn2.Close()

	log.Printf("[perm-kick] agent %s (%s) kicked (double enter)", agent.ID, agent.Section)
	return true
}
