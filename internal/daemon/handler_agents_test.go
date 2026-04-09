package daemon

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Spawn Agent ---

func TestHandleSpawnAgent_RequiresProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSpawnAgent(map[string]any{"section": "test"})
	if resp.Error == "" {
		t.Fatal("expected error for missing project")
	}
}

func TestHandleSpawnAgent_RequiresSection(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSpawnAgent(map[string]any{"project": "proj"})
	if resp.Error == "" {
		t.Fatal("expected error for missing section")
	}
}

func TestHandleSpawnAgent_Success(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj",
		"section": "task-a",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["project"] != "proj" {
		t.Errorf("project = %q, want proj", m["project"])
	}
	if m["section"] != "task-a" {
		t.Errorf("section = %q, want task-a", m["section"])
	}
	if m["backend"] != "claude" {
		t.Errorf("backend = %q, want claude", m["backend"])
	}
	if m["status"] != "spawning" {
		t.Errorf("status = %q, want spawning", m["status"])
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	agent, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.Project != "proj" || agent.Section != "task-a" {
		t.Errorf("stored agent mismatch: project=%q section=%q", agent.Project, agent.Section)
	}
}

func TestHandleSpawnAgent_DuplicateSection(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task-a",
		Status: "running", Backend: "claude",
	})

	resp := h.handleSpawnAgent(map[string]any{"project": "proj", "section": "task-a"})
	if resp.Error == "" {
		t.Fatal("expected error for duplicate section")
	}
	if !strings.Contains(resp.Error, "already has active agent") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleSpawnAgent_MaxDepthEnforced(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()
	h.agentMaxDepth = 1

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "parent",
		SessionID: "parent-sess", Status: "running", Depth: 0, Backend: "claude",
	})

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "child", "caller_session": "parent-sess",
	})
	if resp.Error == "" {
		t.Fatal("expected error for max depth exceeded")
	}
	if !strings.Contains(resp.Error, "max spawn depth") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleSpawnAgent_BackendCodex(t *testing.T) {
	h, _ := mustHandler(t)
	h.dataDir = t.TempDir()

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "codex-task", "backend": "codex",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["backend"] != "codex" {
		t.Errorf("backend = %q, want codex", m["backend"])
	}
}

func TestHandleSpawnAgent_TokenBudgetFromParam(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "budget-task",
		"token_budget": float64(50000), "max_turns": float64(10),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	id, _ := resultMap(t, resp)["id"].(string)

	agent, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.TokenBudget != 50000 {
		t.Errorf("token_budget = %d, want 50000", agent.TokenBudget)
	}
}

func TestHandleSpawnAgent_TokenBudgetFromConfig(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()
	h.agentTokenBudget = 100000

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "default-budget",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	id, _ := resultMap(t, resp)["id"].(string)

	agent, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agent.TokenBudget != 100000 {
		t.Errorf("token_budget = %d, want 100000 (from config)", agent.TokenBudget)
	}
}

func TestHandleSpawnAgent_DefaultMaxDepth(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()
	// agentMaxDepth stays 0 — handler should default to 3

	// Depth 0 parent
	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "d0",
		SessionID: "sess-d0", Status: "running", Depth: 0, Backend: "claude",
	})
	// Depth 1
	s.AgentCreate(storage.Agent{
		ID: "agent-2", Project: "proj", Section: "d1",
		SessionID: "sess-d1", Status: "running", Depth: 1,
		CallerSession: "sess-d0", Backend: "claude",
	})
	// Depth 2
	s.AgentCreate(storage.Agent{
		ID: "agent-3", Project: "proj", Section: "d2",
		SessionID: "sess-d2", Status: "running", Depth: 2,
		CallerSession: "sess-d1", Backend: "claude",
	})

	// Depth 3 should be blocked by default max_depth=3
	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "d3", "caller_session": "sess-d2",
	})
	if resp.Error == "" {
		t.Fatal("expected error for default max depth (3) exceeded")
	}
}

// --- Register Agent ---

func TestHandleRegisterAgent_RequiresID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRegisterAgent(map[string]any{"pid": float64(1234), "sock_path": "/tmp/x.sock"})
	if resp.Error == "" {
		t.Fatal("expected error for missing id")
	}
}

func TestHandleRegisterAgent_RequiresPID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRegisterAgent(map[string]any{"id": "agent-1", "sock_path": "/tmp/x.sock"})
	if resp.Error == "" {
		t.Fatal("expected error for missing pid")
	}
}

func TestHandleRegisterAgent_RequiresSockPath(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRegisterAgent(map[string]any{"id": "agent-1", "pid": float64(1234)})
	if resp.Error == "" {
		t.Fatal("expected error for missing sock_path")
	}
}

func TestHandleRegisterAgent_Success(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "pending", Backend: "claude",
	})

	resp := h.handleRegisterAgent(map[string]any{
		"id": "agent-1", "pid": float64(9999), "sock_path": "/tmp/agent-1.sock",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-1")
	if agent.Status != "running" {
		t.Errorf("status = %q, want running", agent.Status)
	}
	if agent.PID != 9999 {
		t.Errorf("pid = %d, want 9999", agent.PID)
	}
	if agent.SockPath != "/tmp/agent-1.sock" {
		t.Errorf("sock_path = %q, want /tmp/agent-1.sock", agent.SockPath)
	}
}

// --- Update Agent ---

func TestHandleUpdateAgent_RequiresID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdateAgent(map[string]any{"fields": map[string]any{"status": "finished"}})
	if resp.Error == "" {
		t.Fatal("expected error for missing id")
	}
}

func TestHandleUpdateAgent_RequiresFields(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdateAgent(map[string]any{"id": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing fields")
	}
}

func TestHandleUpdateAgent_EmptyFields(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdateAgent(map[string]any{"id": "agent-1", "fields": map[string]any{}})
	if resp.Error == "" {
		t.Fatal("expected error for empty fields")
	}
}

func TestHandleUpdateAgent_Success(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})

	resp := h.handleUpdateAgent(map[string]any{
		"id":     "agent-1",
		"fields": map[string]any{"status": "finished", "progress": "done"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-1")
	if agent.Status != "finished" {
		t.Errorf("status = %q, want finished", agent.Status)
	}
	if agent.Progress != "done" {
		t.Errorf("progress = %q, want done", agent.Progress)
	}
}

// --- Relay Agent ---

func TestHandleRelayAgent_RequiresTo(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRelayAgent(map[string]any{"content": "hello"})
	if resp.Error == "" {
		t.Fatal("expected error for missing to")
	}
}

func TestHandleRelayAgent_RequiresContent(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRelayAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing content")
	}
}

func TestHandleRelayAgent_AgentNotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRelayAgent(map[string]any{"to": "nonexistent", "content": "hello"})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestHandleRelayAgent_AgentNotRunning(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "stopped", SockPath: "/tmp/agent-1.sock", Backend: "claude",
	})

	resp := h.handleRelayAgent(map[string]any{"to": "agent-1", "content": "hello"})
	if resp.Error == "" {
		t.Fatal("expected error for non-running agent")
	}
	if !strings.Contains(resp.Error, "not running") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleRelayAgent_NoSockPath(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})

	resp := h.handleRelayAgent(map[string]any{"to": "agent-1", "content": "hello"})
	if resp.Error == "" {
		t.Fatal("expected error for missing socket path")
	}
	if !strings.Contains(resp.Error, "no socket path") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleRelayAgent_ResolveBySection(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task-x",
		Status: "stopped", Backend: "claude",
	})

	resp := h.handleRelayAgent(map[string]any{
		"to": "task-x", "project": "proj", "content": "hello",
	})
	// Should resolve by section, then fail because it's not running
	if resp.Error == "" {
		t.Fatal("expected error for non-running agent")
	}
	if strings.Contains(resp.Error, "no agent found") {
		t.Fatalf("should resolve by section, but got not-found error: %s", resp.Error)
	}
}

// --- Stop Agent ---

func TestHandleStopAgent_RequiresTo(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleStopAgent(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing to")
	}
}

func TestHandleStopAgent_AgentNotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleStopAgent(map[string]any{"to": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestHandleStopAgent_NotStoppable(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "finished", Backend: "claude",
	})

	resp := h.handleStopAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for finished agent")
	}
	if !strings.Contains(resp.Error, "not stoppable") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleStopAgent_Success(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})

	resp := h.handleStopAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["status"] != "stopped" {
		t.Errorf("status = %q, want stopped", m["status"])
	}
	if m["agent_id"] != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", m["agent_id"])
	}

	agent, _ := s.AgentGet("agent-1")
	if agent.Status != "stopped" {
		t.Errorf("persisted status = %q, want stopped", agent.Status)
	}
	if agent.StoppedAt == "" {
		t.Error("expected stopped_at to be set")
	}
}

func TestHandleStopAgent_StoppableStatuses(t *testing.T) {
	for _, status := range []string{"running", "frozen", "spawning"} {
		t.Run(status, func(t *testing.T) {
			h, s := mustHandler(t)

			s.AgentCreate(storage.Agent{
				ID: "agent-1", Project: "proj", Section: "task",
				Status: status, Backend: "claude",
			})

			resp := h.handleStopAgent(map[string]any{"to": "agent-1"})
			if resp.Error != "" {
				t.Fatalf("unexpected error for status %q: %s", status, resp.Error)
			}
		})
	}
}

func TestHandleStopAgentCascade(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "orch-c", SessionID: "sess-orch-c",
		Project: "p", Section: "orch", Status: "running",
	})
	s.AgentCreate(storage.Agent{
		ID: "child-1", SessionID: "sess-child-1",
		Project: "p", Section: "c1", Status: "running",
		CallerSession: "sess-orch-c",
	})
	s.AgentCreate(storage.Agent{
		ID: "child-2", SessionID: "sess-child-2",
		Project: "p", Section: "c2", Status: "running",
		CallerSession: "sess-orch-c",
	})

	result := h.handleStopAgent(map[string]any{"to": "orch-c", "project": "p"})
	if result.Error != "" {
		t.Fatalf("handleStopAgent returned error: %s", result.Error)
	}

	c1, _ := s.AgentGet("child-1")
	c2, _ := s.AgentGet("child-2")
	if c1.Status != "stopped" {
		t.Errorf("child-1 status=%q want stopped", c1.Status)
	}
	if c2.Status != "stopped" {
		t.Errorf("child-2 status=%q want stopped", c2.Status)
	}
}

// --- Stop All Agents ---

func TestHandleStopAllAgents_RequiresProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleStopAllAgents(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing project")
	}
}

func TestHandleStopAllAgents_Success(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{ID: "agent-1", Project: "proj", Section: "a", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "agent-2", Project: "proj", Section: "b", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "agent-3", Project: "proj", Section: "c", Status: "finished", Backend: "claude"})

	resp := h.handleStopAllAgents(map[string]any{"project": "proj"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	stopped, _ := m["stopped"].(float64)
	if stopped != 2 {
		t.Errorf("stopped = %v, want 2", stopped)
	}

	for _, id := range []string{"agent-1", "agent-2"} {
		agent, _ := s.AgentGet(id)
		if agent.Status != "stopped" {
			t.Errorf("agent %s status = %q, want stopped", id, agent.Status)
		}
	}
	agent3, _ := s.AgentGet("agent-3")
	if agent3.Status != "finished" {
		t.Errorf("agent-3 status = %q, want finished (untouched)", agent3.Status)
	}
}

func TestHandleStopAllAgents_NoRunningAgents(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleStopAllAgents(map[string]any{"project": "empty-proj"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	stopped, _ := m["stopped"].(float64)
	if stopped != 0 {
		t.Errorf("stopped = %v, want 0", stopped)
	}
}

func TestHandleStopAllAgents_MixedStatuses(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{ID: "a1", Project: "proj", Section: "s1", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "a2", Project: "proj", Section: "s2", Status: "frozen", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "a3", Project: "proj", Section: "s3", Status: "spawning", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "a4", Project: "proj", Section: "s4", Status: "stopped", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "a5", Project: "proj", Section: "s5", Status: "error", Backend: "claude"})

	resp := h.handleStopAllAgents(map[string]any{"project": "proj"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	stopped, _ := m["stopped"].(float64)
	if stopped != 3 {
		t.Errorf("stopped = %v, want 3 (running+frozen+spawning)", stopped)
	}
}

// --- Resume Agent ---

func TestHandleResumeAgent_RequiresTo(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleResumeAgent(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing to")
	}
}

func TestHandleResumeAgent_AgentNotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleResumeAgent(map[string]any{"to": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestHandleResumeAgent_NotResumable(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "running", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for running agent (not resumable)")
	}
	if !strings.Contains(resp.Error, "not resumable") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleResumeAgent_CodexNotSupported(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "stopped", Backend: "codex",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for codex backend")
	}
	if !strings.Contains(resp.Error, "only supported for claude") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleResumeAgent_NoSessionID(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "stopped", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing session_id")
	}
	if !strings.Contains(resp.Error, "no session_id") {
		t.Errorf("unexpected error text: %s", resp.Error)
	}
}

func TestHandleResumeAgent_Success(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "stopped", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["status"] != "resuming" {
		t.Errorf("status = %q, want resuming", m["status"])
	}
	if m["agent_id"] != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", m["agent_id"])
	}
}

func TestHandleResumeAgent_FrozenIsResumable(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "frozen", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleResumeAgentRejectsActiveSuccessor(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-0", Project: "proj", Section: "sec",
		SessionID: "sess-old", Status: "stopped", Backend: "claude",
	}); err != nil {
		t.Fatalf("create stopped agent: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE agents SET created_at = datetime('now', '-10 seconds') WHERE id = 'agent-0'"); err != nil {
		t.Fatalf("backdate stopped agent: %v", err)
	}
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "sec",
		SessionID: "sess-new", Status: "running", Backend: "claude",
	}); err != nil {
		t.Fatalf("create running agent: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-0"})
	if resp.Error == "" {
		t.Fatal("expected resume conflict error")
	}
	if !strings.Contains(resp.Error, `section "sec" already has active agent agent-1`) {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

// --- Update Agent Status ---

func TestHandleUpdateAgentStatus_RequiresIDOrSession(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdateAgentStatus(map[string]any{"phase": "implementing"})
	if resp.Error == "" {
		t.Fatal("expected error for missing id")
	}
}

func TestHandleUpdateAgentStatus_RequiresPhase(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUpdateAgentStatus(map[string]any{"id": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing phase")
	}
}

func TestHandleUpdateAgentStatus_ByID(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-0", Project: "proj", Section: "sec",
		Status: "running",
	})

	resp := h.handleUpdateAgentStatus(map[string]any{
		"id": "agent-0", "phase": "implementing auth module",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["id"] != "agent-0" {
		t.Errorf("id = %q, want agent-0", m["id"])
	}

	a, _ := s.AgentGetBySection("proj", "sec")
	if a.Phase != "implementing auth module" {
		t.Errorf("phase=%q want %q", a.Phase, "implementing auth module")
	}
	if a.HeartbeatAt == "" {
		t.Error("expected heartbeat_at to be set")
	}
}

func TestHandleUpdateAgentStatus_BySessionID(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-abc", Status: "running", Backend: "claude",
	})

	resp := h.handleUpdateAgentStatus(map[string]any{
		"_session_id": "sess-abc", "phase": "idle",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["id"] != "agent-1" {
		t.Errorf("id = %q, want agent-1", m["id"])
	}
}

// --- Track Usage ---

func TestHandleTrackUsage_RequiresThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleTrackUsage(map[string]any{
		"input_tokens": float64(100), "output_tokens": float64(50),
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing thread_id")
	}
}

func TestHandleTrackUsage_SkipsZeroTokens(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleTrackUsage(map[string]any{"thread_id": "thread-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "skipped" {
		t.Errorf("status = %q, want skipped", m["status"])
	}
}

func TestHandleTrackUsage_Success(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleTrackUsage(map[string]any{
		"thread_id": "thread-1", "input_tokens": float64(1000), "output_tokens": float64(500),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ok" {
		t.Errorf("status = %q, want ok", m["status"])
	}
}

func TestHandleTrackUsage_ForkSource(t *testing.T) {
	h, s := mustHandler(t)
	// fork columns are added via ALTER TABLE migration which runs before CREATE TABLE
	// in in-memory DBs — manually add them so the test exercises the fork path
	s.DB().Exec("ALTER TABLE token_usage ADD COLUMN fork_input_tokens INTEGER DEFAULT 0")
	s.DB().Exec("ALTER TABLE token_usage ADD COLUMN fork_output_tokens INTEGER DEFAULT 0")
	s.DB().Exec("ALTER TABLE token_usage ADD COLUMN fork_request_count INTEGER DEFAULT 0")

	resp := h.handleTrackUsage(map[string]any{
		"thread_id": "thread-1", "input_tokens": float64(2000),
		"output_tokens": float64(1000), "source": "fork",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["status"] != "ok" {
		t.Errorf("status = %q, want ok", m["status"])
	}
}

func TestHandleTrackUsage_UpdatesAgentTelemetry(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "agent-sess-1", Status: "running", Backend: "claude",
	})

	resp := h.handleTrackUsage(map[string]any{
		"thread_id": "agent-sess-1", "input_tokens": float64(500), "output_tokens": float64(200),
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-1")
	if agent.TurnsUsed != 1 {
		t.Errorf("turns_used = %d, want 1", agent.TurnsUsed)
	}
	if agent.InputTokens != 500 {
		t.Errorf("input_tokens = %d, want 500", agent.InputTokens)
	}
	if agent.OutputTokens != 200 {
		t.Errorf("output_tokens = %d, want 200", agent.OutputTokens)
	}
}

func TestHandleTrackUsage_AccumulatesMultipleCalls(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "agent-sess-1", Status: "running", Backend: "claude",
	})

	h.handleTrackUsage(map[string]any{
		"thread_id": "agent-sess-1", "input_tokens": float64(100), "output_tokens": float64(50),
	})
	h.handleTrackUsage(map[string]any{
		"thread_id": "agent-sess-1", "input_tokens": float64(200), "output_tokens": float64(100),
	})

	agent, _ := s.AgentGet("agent-1")
	if agent.TurnsUsed != 2 {
		t.Errorf("turns_used = %d, want 2", agent.TurnsUsed)
	}
	if agent.InputTokens != 300 {
		t.Errorf("input_tokens = %d, want 300", agent.InputTokens)
	}
	if agent.OutputTokens != 150 {
		t.Errorf("output_tokens = %d, want 150", agent.OutputTokens)
	}
}

// --- List Agents ---

func TestHandleListAgents_Empty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleListAgents(map[string]any{})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	count, _ := m["count"].(float64)
	if count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
}

func TestHandleListAgents_FilterByProject(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{ID: "agent-1", Project: "proj-a", Section: "a", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "agent-2", Project: "proj-b", Section: "b", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "agent-3", Project: "proj-a", Section: "c", Status: "stopped", Backend: "claude"})

	resp := h.handleListAgents(map[string]any{"project": "proj-a"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	count, _ := m["count"].(float64)
	if count != 2 {
		t.Errorf("count = %v, want 2 (agents in proj-a)", count)
	}
}

func TestHandleListAgents_AllProjects(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{ID: "agent-1", Project: "proj-a", Section: "a", Status: "running", Backend: "claude"})
	s.AgentCreate(storage.Agent{ID: "agent-2", Project: "proj-b", Section: "b", Status: "running", Backend: "claude"})

	resp := h.handleListAgents(map[string]any{})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	count, _ := m["count"].(float64)
	if count != 2 {
		t.Errorf("count = %v, want 2 (all agents)", count)
	}
}

// --- Get Agent ---

func TestHandleGetAgent_RequiresTo(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetAgent(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing to")
	}
}

func TestHandleGetAgent_NotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetAgent(map[string]any{"to": "nonexistent"})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestHandleGetAgent_ByID(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})

	resp := h.handleGetAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["id"] != "agent-1" {
		t.Errorf("id = %q, want agent-1", m["id"])
	}
	if m["project"] != "proj" {
		t.Errorf("project = %q, want proj", m["project"])
	}
}

func TestHandleGetAgent_BySection(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "my-section",
		Status: "running", Backend: "claude",
	})

	resp := h.handleGetAgent(map[string]any{"to": "my-section", "project": "proj"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["id"] != "agent-1" {
		t.Errorf("id = %q, want agent-1", m["id"])
	}
	if m["section"] != "my-section" {
		t.Errorf("section = %q, want my-section", m["section"])
	}
}

// --- Generate Agent UUID ---

func TestHandleAgentUUID_Format(t *testing.T) {
	uuid := generateAgentUUID()
	if len(uuid) != 36 {
		t.Errorf("UUID length = %d, want 36", len(uuid))
	}
	if uuid[14] != '4' {
		t.Errorf("UUID version byte = %c, want 4", uuid[14])
	}
	variant := uuid[19]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Errorf("UUID variant byte = %c, want 8/9/a/b", variant)
	}
}

func TestHandleAgentUUID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		uuid := generateAgentUUID()
		if seen[uuid] {
			t.Fatalf("duplicate UUID generated: %s", uuid)
		}
		seen[uuid] = true
	}
}

// --- Handle() dispatch ---

func TestHandleAgentDispatch_SpawnViaHandle(t *testing.T) {
	h, _ := mustHandler(t)
	h.dataDir = t.TempDir()

	resp := h.Handle(Request{
		Method: "spawn_agent",
		Params: map[string]any{"project": "proj", "section": "dispatch-test"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["section"] != "dispatch-test" {
		t.Errorf("section = %q, want dispatch-test", m["section"])
	}
}

func TestHandleAgentDispatch_ListViaHandle(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "list_agents",
		Params: map[string]any{},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleAgentDispatch_GetViaHandle(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})

	resp := h.Handle(Request{
		Method: "get_agent",
		Params: map[string]any{"to": "agent-1"},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleAgentDispatch_TrackUsageViaHandle(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "_track_usage",
		Params: map[string]any{
			"thread_id": "t-1", "input_tokens": float64(10), "output_tokens": float64(5),
		},
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleTrackUsage_WithCacheFields(t *testing.T) {
	h, s := mustHandler(t)
	resp := h.handleTrackUsage(map[string]any{
		"thread_id":          "t1",
		"input_tokens":       float64(1000),
		"output_tokens":      float64(200),
		"cache_read_tokens":  float64(800),
		"cache_write_tokens": float64(100),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
	in, out, err := s.GetTokenUsage("t1")
	if err != nil {
		t.Fatal(err)
	}
	if in != 1000 {
		t.Errorf("input = %d, want 1000", in)
	}
	if out != 200 {
		t.Errorf("output = %d, want 200", out)
	}
}

func TestHandleTrackUsage_WithRateLimits(t *testing.T) {
	h, s := mustHandler(t)
	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "t1",
		"input_tokens":  float64(100),
		"output_tokens": float64(50),
		"rate_limits":   `{"unified_5h_utilization":0.42,"is_subscription":true}`,
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
	val, err := s.GetProxyState("rate_limits")
	if err != nil {
		t.Fatal(err)
	}
	if val == "" {
		t.Error("rate_limits should be persisted in proxy_state")
	}
}
