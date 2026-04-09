package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/orchestrator"
	"github.com/carsteneu/yesmem/internal/storage"
)

// startAgentHeartbeat runs a goroutine that polls for unread channel messages
// targeting running agent sessions and relays them via inject sockets.
// Also enforces agent limits via freeze (not kill). This enables autonomous
// multi-level agent communication without manual intervention.
func (h *Handler) startAgentHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var tick int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			h.relayAgentMessages()
			if tick%5 == 0 {
				h.crashRecovery()
				h.detectHungAgents()
			}
			h.enforceAgentLimits()
			h.detectOrphanedAgents()
			h.attemptRestart()
			if tick%150 == 0 {
				h.sendOrchestratorStatusPing()
				tick = 0
			}
		}
	}
}

// relayAgentMessages checks for unread channel messages targeting running agents
// relayAgentMessages checks for pending messages and pings agents via PTY.
// Content delivery happens through the proxy's channel injection (two-stage delivery).
// Heartbeat only sends a short notification ping to trigger a new API turn.
func (h *Handler) relayAgentMessages() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}

	for _, agent := range agents {
		if agent.Status != "running" || agent.SockPath == "" || agent.SessionID == "" {
			continue
		}

		msgs, err := h.store.GetUnnotifiedMessages(agent.SessionID)
		if err != nil || len(msgs) == 0 {
			continue
		}

		injectPath := agent.SockPath + ".inject"
		// Send ONE short ping per agent (not per message) — proxy delivers the actual content
		conn, err := net.DialTimeout("unix", injectPath, 2*time.Second)
		if err != nil {
			for _, m := range msgs {
				retries, _ := h.store.IncrementDeliveryRetry(m.ID)
				if retries >= 5 {
					h.store.MarkDeliveryFailed(m.ID)
					log.Printf("[heartbeat] message %d to agent %s delivery failed after %d retries", m.ID, agent.ID, retries)
					h.Handle(Request{
						Method: "send_to",
						Params: map[string]any{
							"target":   m.Sender,
							"content":  fmt.Sprintf("DELIVERY_FAILED: Nachricht an %s konnte nicht zugestellt werden nach %d Versuchen.", agent.Section, retries),
							"msg_type": "status",
						},
					})
				}
			}
			continue
		}
		senderList := msgs[0].Sender
		if len(msgs) > 1 {
			senderList = fmt.Sprintf("%s (+%d more)", msgs[0].Sender, len(msgs)-1)
		}
		ping := fmt.Sprintf("[New message from %s — content arrives via system prompt]\r", senderList)
		_, writeErr := conn.Write([]byte(ping))
		conn.Close()
		if writeErr != nil {
			continue
		}
		// Mark all messages as notified (read=1) — proxy will set delivered=1
		for _, m := range msgs {
			h.store.MarkMessageNotified(m.ID)
		}

		newCount, _ := h.store.AgentIncrementRelayCount(agent.ID)
		log.Printf("[heartbeat] pinged agent %s (%s) for %d pending messages, total turns: %d", agent.ID, agent.Section, len(msgs), newCount)

		// Enforce max_turns — freeze, don't kill
		maxTurns := h.agentMaxTurns
		if maxTurns == 0 {
			maxTurns = 30
		}
		if newCount >= maxTurns {
			log.Printf("[heartbeat] agent %s reached max_turns (%d), freezing", agent.ID, maxTurns)
			h.freezeAgent(agent.ID, fmt.Sprintf("max_turns %d reached", maxTurns))
		}
	}
}

// crashRecovery checks running agents for dead PIDs, quarantines their sessions,
// taints scratchpad sections, and retries up to 3 times before marking failed.
// Consolidates the former detectDeadPIDs + agentHealthCheck logic.
func (h *Handler) crashRecovery() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, a := range agents {
		if a.Status != "running" || a.PID <= 0 {
			continue
		}
		p, err := os.FindProcess(a.PID)
		if err != nil {
			continue
		}
		if sigErr := p.Signal(syscall.Signal(0)); sigErr == nil {
			continue
		}

		now := time.Now().Format(time.RFC3339)
		log.Printf("[heartbeat] agent %s PID %d dead, starting crash recovery", a.ID, a.PID)

		if a.SockPath != "" {
			os.Remove(a.SockPath)
			os.Remove(a.SockPath + ".inject")
		}
		if a.SessionID != "" {
			if n, qErr := h.store.QuarantineSession(a.SessionID); qErr == nil && n > 0 {
				log.Printf("[heartbeat] quarantined %d learnings from crashed agent %s", n, a.ID)
			}
		}
		if a.Section != "" && a.Project != "" {
			taintNote := fmt.Sprintf("[TAINTED — Agent %s crashed at %s. Data may be incomplete.]", a.ID, now)
			for _, prefix := range []string{"ergebnis-", ""} {
				sectionName := prefix + a.Section
				if sections, sErr := h.store.ScratchpadRead(a.Project, sectionName); sErr == nil && len(sections) > 0 {
					h.store.ScratchpadWrite(a.Project, sectionName, taintNote+"\n\n"+sections[0].Content, sections[0].Owner)
				}
			}
		}
		if a.RetryCount < 3 {
			log.Printf("[heartbeat] agent %s crashed (retry %d/3), respawning", a.ID, a.RetryCount+1)
			h.store.AgentUpdate(a.ID, map[string]any{
				"status":      "crashed",
				"stopped_at":  now,
				"error":       fmt.Sprintf("crash #%d — respawning", a.RetryCount+1),
				"retry_count": a.RetryCount + 1,
			})
			go h.respawnAgent(a)
			continue
		}
		runtime := crashRuntime(a.CreatedAt)
		context := fmt.Sprintf("FAILED: Agent '%s' (section '%s') crashed after %d attempts. Runtime: %s.", a.ID, a.Section, a.RetryCount+1, runtime)
		if sections, err := h.store.ScratchpadRead(a.Project, a.Section); err == nil && len(sections) > 0 {
			sp := sections[0].Content
			if len(sp) > 200 {
				sp = sp[:200] + "..."
			}
			context += fmt.Sprintf(" Letzter Status: %s", sp)
		}
		h.store.AgentUpdate(a.ID, map[string]any{"status": "failed", "stopped_at": now, "error": "3 retries exhausted, final crash"})
		if a.CallerSession != "" {
			h.Handle(Request{Method: "send_to", Params: map[string]any{"target": a.CallerSession, "content": context, "msg_type": "status"}})
		}
	}
}

const livenessPingGrace = 5 * time.Minute

// detectOrphanedAgents finds running agents whose parent agent has died and
// stops them after a 5-minute grace period.
func (h *Handler) detectOrphanedAgents() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Status != "running" || agent.CallerSession == "" {
			continue
		}
		// Note: agents with status "frozen" are intentionally excluded.
		// A frozen parent is paused, not dead — its children should not be orphaned.
		parent, err := h.store.AgentGetAnyBySession(agent.CallerSession)
		if err != nil || parent == nil {
			continue // user session or lookup error — skip
		}
		parentDead := parent.Status == "stopped" || parent.Status == "error" || parent.Status == "failed"
		if !parentDead {
			if agent.LivenessPingAt != "" {
				h.store.AgentUpdate(agent.ID, map[string]any{"liveness_ping_at": ""})
			}
			continue
		}
		if agent.LivenessPingAt == "" {
			log.Printf("[heartbeat] orphan check: agent %s parent %s dead, starting 5min grace", agent.ID, parent.ID)
			h.store.AgentUpdate(agent.ID, map[string]any{
				"liveness_ping_at": time.Now().Format("2006-01-02 15:04:05"),
			})
			continue
		}
		pingAt, err := time.ParseInLocation("2006-01-02 15:04:05", agent.LivenessPingAt, time.Local)
		if err != nil || time.Since(pingAt) < livenessPingGrace {
			continue
		}
		log.Printf("[heartbeat] agent %s orphaned (parent %s dead for >%v), stopping", agent.ID, parent.ID, livenessPingGrace)
		h.store.AgentCascadeStop(agent.SessionID)
		h.store.AgentUpdate(agent.ID, map[string]any{
			"status":   "stopped",
			"progress": fmt.Sprintf("orphaned: parent %s dead", parent.ID),
		})
	}
}

// enforceAgentLimits checks running agents against configured limits and freezes violators.
func (h *Handler) enforceAgentLimits() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}

	maxRuntime := h.getAgentMaxRuntime()

	for _, agent := range agents {
		if agent.Status != "running" {
			continue
		}

		// Check runtime limit
		created, err := time.Parse("2006-01-02 15:04:05", agent.CreatedAt)
		if err == nil && time.Since(created) > maxRuntime {
			log.Printf("[heartbeat] agent %s exceeded max_runtime (%v), freezing", agent.ID, maxRuntime)
			h.freezeAgent(agent.ID, fmt.Sprintf("max_runtime %v exceeded", maxRuntime))
			continue
		}

		// Check token budget — use cumulative tokens from agents table (updated via AgentUpdateTelemetry)
		if agent.TokenBudget > 0 {
			total := agent.InputTokens + agent.OutputTokens
			if total >= agent.TokenBudget {
				log.Printf("[heartbeat] agent %s exceeded token_budget (%d/%d), freezing", agent.ID, total, agent.TokenBudget)
				h.freezeAgent(agent.ID, fmt.Sprintf("token_budget %d exceeded (used %d)", agent.TokenBudget, total))
			}
		}
	}
}

// freezeAgent stops relaying messages to an agent without killing it.
// The agent process stays alive, messages queue up. Can be resumed via resume_agent.
func (h *Handler) freezeAgent(id, reason string) {
	h.store.AgentUpdate(id, map[string]any{
		"status":   "frozen",
		"progress": "frozen: " + reason,
	})
}

// getAgentMaxRuntime returns the configured max runtime for agents.
func (h *Handler) getAgentMaxRuntime() time.Duration {
	if h.agentMaxRuntime > 0 {
		return h.agentMaxRuntime
	}
	return 30 * time.Minute
}

// attemptRestart checks running agents with a restart strategy and relaunches
// dead processes via a new terminal bridge using --resume.
func (h *Handler) attemptRestart() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Status != "error" {
			continue
		}
		if agent.RestartStrategy == "" || agent.RestartStrategy == "temporary" {
			continue
		}

		// GAP2: 30s race-condition guard
		if agent.LastRestartAt != "" {
			lastRestart, err := time.ParseInLocation("2006-01-02 15:04:05", agent.LastRestartAt, time.Local)
			if err == nil && time.Since(lastRestart) < 30*time.Second {
				continue
			}
		}

		// agent failed during spawn (bridge or terminal error in handler_agents.go)
		// "permanent" restarts indefinitely — only "transient" is capped by MaxRestarts
		if agent.RestartStrategy != "permanent" && agent.RestartCount >= agent.MaxRestarts {
			log.Printf("[heartbeat] agent %s max_restarts %d exceeded, freezing", agent.ID, agent.MaxRestarts)
			h.store.AgentUpdate(agent.ID, map[string]any{
				"status":   "frozen",
				"progress": fmt.Sprintf("max_restarts %d exceeded", agent.MaxRestarts),
			})
			continue
		}

		if agent.SockPath == "" {
			log.Printf("[heartbeat] agent %s has no SockPath, skipping restart", agent.ID)
			continue
		}

		log.Printf("[heartbeat] restarting agent %s (strategy=%s, attempt %d/%d)", agent.ID, agent.RestartStrategy, agent.RestartCount+1, agent.MaxRestarts)

		// GAP3: remove stale sockets
		os.Remove(agent.SockPath)
		os.Remove(agent.SockPath + ".inject")

		// spawn new bridge with --resume
		binPath, _ := os.Executable()
		terminal := h.agentTerminal
		if terminal == "" {
			terminal = orchestrator.DetectTerminal()
		}
		title := fmt.Sprintf("yesmem-%s (restart %d)", agent.Section, agent.RestartCount+1)
		bin, args := orchestrator.BuildSpawnCommand(terminal, binPath, title, "agent-tty", "--sock", agent.SockPath, "--resume", agent.SessionID)
		cmd := exec.Command(bin, args...)
		if err := cmd.Start(); err != nil {
			log.Printf("[heartbeat] restart failed for %s: %v", agent.ID, err)
			continue
		}

		now := time.Now().Format("2006-01-02 15:04:05")
		h.store.AgentUpdate(agent.ID, map[string]any{
			"pid":             cmd.Process.Pid,
			"restart_count":   agent.RestartCount + 1,
			"status":          "running",
			"last_restart_at": now,
		})

		// GAP4: notify parent via inject socket (best-effort)
		if agent.CallerSession != "" {
			parent, err := h.store.AgentGetAnyBySession(agent.CallerSession)
			if err == nil && parent != nil && parent.Status == "running" && parent.SockPath != "" {
				conn, err := net.DialTimeout("unix", parent.SockPath+".inject", 2*time.Second)
				if err == nil {
					msg := fmt.Sprintf("[SYSTEM] agent %s (%s) restarted (attempt %d/%d)\r", agent.ID, agent.Section, agent.RestartCount+1, agent.MaxRestarts)
					conn.Write([]byte(msg))
					conn.Close()
				}
			}
		}
	}
}

// killAgent terminates an agent process and removes it from the DB.
func (h *Handler) killAgent(agent *storage.Agent) {
	if agent.PID > 0 {
		syscall.Kill(agent.PID, syscall.SIGTERM)
	}
	h.store.AgentDelete(agent.ID)
	if agent.SockPath != "" {
		os.Remove(agent.SockPath)
		os.Remove(agent.SockPath + ".inject")
	}
}

// crashRuntime calculates how long an agent ran before crashing.
func crashRuntime(createdAt string) string {
	t, err := time.Parse("2006-01-02 15:04:05", createdAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339, createdAt)
	}
	if err != nil {
		return "unbekannt"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// respawnAgent creates a new agent process with the same config as the crashed one.
func (h *Handler) respawnAgent(crashed storage.Agent) {
	time.Sleep(2 * time.Second) // Brief cooldown before retry
	resp := h.Handle(Request{
		Method: "spawn_agent",
		Params: map[string]any{
			"project":        crashed.Project,
			"section":        crashed.Section,
			"caller_session": crashed.CallerSession,
		},
	})
	if resp.Error != "" {
		log.Printf("[heartbeat] respawn failed for %s: %s", crashed.ID, resp.Error)
	} else {
		var result map[string]any
		if err := json.Unmarshal(resp.Result, &result); err == nil {
			if newID, ok := result["agent_id"].(string); ok {
				log.Printf("[heartbeat] respawned %s as %s", crashed.ID, newID)
				h.store.AgentUpdate(newID, map[string]any{
					"retry_count": crashed.RetryCount + 1,
				})
			}
		}
	}
}

const hungAgentThreshold = 10 * time.Minute

// detectHungAgents freezes running agents whose heartbeat_at is older than hungAgentThreshold.
// Agents without a heartbeat_at are skipped (they haven't reported yet).
func (h *Handler) detectHungAgents() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Status != "running" || agent.HeartbeatAt == "" {
			continue
		}
		hbTime, err := time.Parse(time.RFC3339, agent.HeartbeatAt)
		if err != nil {
			continue
		}
		staleness := time.Since(hbTime)
		if staleness > hungAgentThreshold {
			reason := fmt.Sprintf("unresponsive: last heartbeat %s ago", staleness.Round(time.Second))
			log.Printf("[heartbeat] agent %s %s, freezing", agent.ID, reason)
			h.freezeAgent(agent.ID, reason)
		}
	}
}

// sendOrchestratorStatusPing sends a status summary to orchestrator agents every 5 minutes.
// An orchestrator is an agent whose session_id is referenced as caller_session by other agents.
func (h *Handler) sendOrchestratorStatusPing() {
	agents, err := h.store.AgentList("")
	if err != nil || len(agents) == 0 {
		return
	}

	// Index running agents by session_id
	bySession := map[string]*storage.Agent{}
	for i := range agents {
		if agents[i].SessionID != "" && agents[i].Status == "running" {
			bySession[agents[i].SessionID] = &agents[i]
		}
	}

	// Group children by their orchestrator (caller_session → parent)
	type projectInfo struct {
		orchestrator *storage.Agent
		children     []storage.Agent
	}
	projects := map[string]*projectInfo{}
	for i := range agents {
		a := &agents[i]
		if a.CallerSession == "" {
			continue
		}
		parent, ok := bySession[a.CallerSession]
		if !ok {
			continue
		}
		key := parent.ID
		if projects[key] == nil {
			projects[key] = &projectInfo{orchestrator: parent}
		}
		projects[key].children = append(projects[key].children, *a)
	}

	for _, info := range projects {
		if info.orchestrator == nil || len(info.children) == 0 {
			continue
		}
		running, done, failed := 0, 0, 0
		for _, c := range info.children {
			switch c.Status {
			case "running", "spawning":
				running++
			case "finished":
				done++
			case "failed", "crashed", "error":
				failed++
			}
		}
		msg := fmt.Sprintf("STATUS_PING [%s]: %d running, %d done, %d failed (von %d total)", info.orchestrator.Project, running, done, failed, len(info.children))
		h.Handle(Request{
			Method: "send_to",
			Params: map[string]any{
				"target":   info.orchestrator.SessionID,
				"content":  msg,
				"msg_type": "status",
			},
		})
		log.Printf("[heartbeat] status ping → %s: %s", info.orchestrator.ID, msg)
	}
}
