package daemon

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
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
	h.agentDefaultBackend = "claude" // explicit — deterministisch, kein exec.LookPath

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

func TestHandleSpawnAgent_BackendFromConfig(t *testing.T) {
	h, _ := mustHandler(t)
	h.dataDir = t.TempDir()
	h.agentDefaultBackend = "opencode"

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "config-task",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["backend"] != "opencode" {
		t.Errorf("backend = %q, want opencode (from config)", m["backend"])
	}
}

func TestHandleSpawnAgent_BackendParamOverridesConfig(t *testing.T) {
	h, _ := mustHandler(t)
	h.dataDir = t.TempDir()
	h.agentDefaultBackend = "opencode"

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "override-task", "backend": "codex",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["backend"] != "codex" {
		t.Errorf("backend = %q, want codex (param overrides config)", m["backend"])
	}
}

func TestHandleSpawnAgent_DefaultBackendFallback(t *testing.T) {
	h, _ := mustHandler(t)
	h.dataDir = t.TempDir()
	// agentDefaultBackend nicht gesetzt — Fallback auf "claude"

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "fallback-task",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["backend"] != "claude" {
		t.Errorf("backend = %q, want claude (fallback)", m["backend"])
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
		t.Fatal("expected error for stopped agent")
	}
	if !strings.Contains(resp.Error, "stopped") {
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
	for _, status := range []string{"running", "paused", "spawning"} {
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
	s.AgentCreate(storage.Agent{ID: "a2", Project: "proj", Section: "s2", Status: "paused", Backend: "claude"})
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
		t.Errorf("stopped = %v, want 3 (running+paused+spawning)", stopped)
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

func TestHandleResumeAgent_CodexNowSupported(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "stopped", Backend: "codex",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("expected success for codex backend (now supported), got error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["status"] != "resuming" {
		t.Errorf("status = %q, want resuming", m["status"])
	}
	if m["agent_id"] != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", m["agent_id"])
	}
	// Note: spawned goroutine may race to set status "pending" → "error" when
	// findCodexSessionID fails in test (no ~/.codex/sessions/). We only verify
	// handleResumeAgent returned success — the goroutine behavior is verified
	// implicitly by the response shape.
	_ = s // keep reference alive
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

func TestHandleResumeAgent_PausedIsResumable(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "paused", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestHandleResumeAgent_FinishedIsResumable(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "finished", Backend: "claude",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("finished agents should be resumable (PID gone but session_id intact), got error: %s", resp.Error)
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

// --- L4: opencode unresumable marker (empty OpencodeSessionID) ---

// TestHandleResumeAgent_OpencodeEmptySession_Blocked: resume of an opencode
// agent whose OpencodeSessionID was never captured must return a clear error
// instead of falling back to the daemon UUID (which opencode rejects with
// exit 1 — Learning #80228).
func TestHandleResumeAgent_OpencodeEmptySession_Blocked(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "paused", Backend: "opencode",
		// OpencodeSessionID intentionally empty — simulates failed poll.
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error == "" {
		t.Fatal("expected error for opencode agent with empty OpencodeSessionID")
	}
	if !strings.Contains(resp.Error, opencodeUnresumableMsg) {
		t.Errorf("error should contain %q, got %q", opencodeUnresumableMsg, resp.Error)
	}
}

// TestHandleResumeAgent_OpencodeWithSession_Ok: opencode agent WITH a captured
// OpencodeSessionID resumes normally — the unresumable check is gated on empty.
func TestHandleResumeAgent_OpencodeWithSession_Ok(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "paused", Backend: "opencode",
		OpencodeSessionID: "ses_opencode_abc",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("opencode agent WITH OpencodeSessionID should pass the unresumable check, got: %s", resp.Error)
	}
}

// TestSpawnAgentProcess_OpencodeResume_EmptySession_MarksError: the defensive
// layer in spawnAgentProcess catches direct callers (attemptRestart, etc.)
// that bypass handleResumeAgent. The agent must be marked error, not spawned
// with the daemon UUID.
func TestSpawnAgentProcess_OpencodeResume_EmptySession_MarksError(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "daemon-uuid-36-chars-xxxxxxxxx", Status: "pending", Backend: "opencode",
		// OpencodeSessionID intentionally empty.
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// resume=true + backend=opencode + empty OpencodeSessionID → defensive block.
	h.spawnAgentProcess("agent-1", "daemon-uuid-36-chars-xxxxxxxxx", "proj", "task",
		"", "/nonexistent/agent-1.sock", t.TempDir(), "opencode", "", 0, true)

	agent, err := s.AgentGet("agent-1")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet failed: %v", err)
	}
	if agent.Status != "error" {
		t.Errorf("expected status=error, got %q (progress=%q)", agent.Status, agent.Progress)
	}
	if !strings.Contains(agent.Error, opencodeUnresumableMsg) {
		t.Errorf("error field should contain %q, got %q", opencodeUnresumableMsg, agent.Error)
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

func TestHandleTrackUsage_ProxyThreadIDMatch(t *testing.T) {
	h, s := mustHandler(t)

	// Create an agent with a proxy_thread_id set
	s.AgentCreate(storage.Agent{
		ID: "agent-pt", Project: "proj", Section: "task",
		SessionID: "daemon-uuid", Status: "running", Backend: "claude",
		ProxyThreadID: "proxy-thread-abc",
	})

	// Track usage with thread_id matching proxy_thread_id (not session_id)
	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "proxy-thread-abc",
		"input_tokens":  float64(500),
		"output_tokens": float64(100),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	agent, err := s.AgentGet("agent-pt")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if agent.TurnsUsed != 1 {
		t.Errorf("TurnsUsed = %d, want 1 (telemetry should update via proxy_thread_id)", agent.TurnsUsed)
	}
	if agent.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", agent.InputTokens)
	}
	if agent.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want 100", agent.OutputTokens)
	}
}

func TestHandleTrackUsage_SessionIDMatch(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-sid", Project: "proj", Section: "task",
		SessionID: "daemon-uuid-match", Status: "running", Backend: "claude",
	})

	// thread_id matches session_id directly — existing behavior
	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "daemon-uuid-match",
		"input_tokens":  float64(300),
		"output_tokens": float64(50),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-sid")
	if agent.TurnsUsed != 1 {
		t.Errorf("TurnsUsed = %d, want 1", agent.TurnsUsed)
	}
}

func TestHandleTrackUsage_LazyMapping(t *testing.T) {
	h, s := mustHandler(t)

	// Create a running agent WITHOUT proxy_thread_id set
	s.AgentCreate(storage.Agent{
		ID: "agent-lazy", Project: "proj", Section: "task",
		SessionID: "daemon-uuid-lazy", Status: "running", Backend: "claude",
	})

	// First _track_usage call — should lazy-map the thread_id to this agent
	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "new-proxy-thread-xyz",
		"project":       "proj",
		"input_tokens":  float64(200),
		"output_tokens": float64(80),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-lazy")
	if agent.TurnsUsed != 1 {
		t.Errorf("TurnsUsed = %d, want 1 (lazy mapping should update telemetry)", agent.TurnsUsed)
	}
	if agent.ProxyThreadID != "new-proxy-thread-xyz" {
		t.Errorf("ProxyThreadID = %q, want new-proxy-thread-xyz (should be set by lazy mapping)", agent.ProxyThreadID)
	}

	// Second call with same thread_id — should match via proxy_thread_id
	resp2 := h.handleTrackUsage(map[string]any{
		"thread_id":     "new-proxy-thread-xyz",
		"input_tokens":  float64(100),
		"output_tokens": float64(20),
	})
	if resp2.Error != "" {
		t.Fatalf("error on second call: %s", resp2.Error)
	}

	agent, _ = s.AgentGet("agent-lazy")
	if agent.TurnsUsed != 2 {
		t.Errorf("TurnsUsed = %d, want 2 (second call should accumulate)", agent.TurnsUsed)
	}
	if agent.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300 (accumulated)", agent.InputTokens)
	}
}

func TestHandleResumeAgent_OpenCodeNowSupported(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = t.TempDir()

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "stopped", Backend: "opencode",
		// OpencodeSessionID is required for resume (L4) — without it the
		// daemon-UUID fall-back makes opencode exit 1.
		OpencodeSessionID: "ses_oc_test",
	})

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("expected success for opencode backend (now supported), got error: %s", resp.Error)
	}

	m := resultMap(t, resp)
	if m["status"] != "resuming" {
		t.Errorf("status = %q, want resuming", m["status"])
	}
	_ = s
}

// --- findCodexSessionID ---

func TestFindCodexSessionID_Success(t *testing.T) {
	sessionsRoot := t.TempDir()
	workDir := "/tmp/test-workdir"

	jsonl := filepath.Join(sessionsRoot, "session-abc.jsonl")
	if err := os.WriteFile(jsonl, []byte(
		`{"type":"session_meta","payload":{"id":"abc-123","cwd":"/tmp/test-workdir"}}`+"\n"+
			`{"type":"message","payload":{}}`+"\n",
	), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// findCodexSessionID uses os.UserHomeDir() → join .codex → sessions
	// Override HOME to point to a dir with .codex/sessions/
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex", "sessions")
	os.MkdirAll(codexDir, 0755)
	// Copy the test session file to the fake codex dir
	src, _ := os.ReadFile(jsonl)
	os.WriteFile(filepath.Join(codexDir, "session-abc.jsonl"), src, 0644)
	t.Setenv("HOME", homeDir)

	sid, err := findCodexSessionID(workDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sid != "abc-123" {
		t.Errorf("session ID = %q, want abc-123", sid)
	}
}

func TestFindCodexSessionID_NoMatch(t *testing.T) {
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex", "sessions")
	os.MkdirAll(codexDir, 0755)
	os.WriteFile(filepath.Join(codexDir, "session-abc.jsonl"), []byte(
		`{"type":"session_meta","payload":{"id":"abc-123","cwd":"/other/dir"}}`+"\n",
	), 0644)
	t.Setenv("HOME", homeDir)

	_, err := findCodexSessionID("/no/match")
	if err == nil {
		t.Fatal("expected error for non-matching CWD")
	}
	if !strings.Contains(err.Error(), "no codex session found") {
		t.Errorf("unexpected error text: %s", err)
	}
}

func TestFindCodexSessionID_EmptyDir(t *testing.T) {
	homeDir := t.TempDir()
	os.MkdirAll(filepath.Join(homeDir, ".codex", "sessions"), 0755)
	t.Setenv("HOME", homeDir)

	_, err := findCodexSessionID("/tmp/work")
	if err == nil {
		t.Fatal("expected error for empty sessions dir")
	}
}

func TestFindCodexSessionID_PicksMostRecent(t *testing.T) {
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex", "sessions")
	os.MkdirAll(codexDir, 0755)

	// Older session for same workDir
	os.WriteFile(filepath.Join(codexDir, "older.jsonl"), []byte(
		`{"type":"session_meta","payload":{"id":"old-456","cwd":"/tmp/work"}}`+"\n",
	), 0644)
	// Newer session for same workDir
	os.WriteFile(filepath.Join(codexDir, "newer.jsonl"), []byte(
		`{"type":"session_meta","payload":{"id":"new-789","cwd":"/tmp/work"}}`+"\n",
	), 0644)
	t.Setenv("HOME", homeDir)

	sid, err := findCodexSessionID("/tmp/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sid != "new-789" {
		t.Errorf("session ID = %q, want new-789 (most recent)", sid)
	}
}

func TestFindCodexSessionID_SkipsNonMatchingSessionMeta(t *testing.T) {
	homeDir := t.TempDir()
	codexDir := filepath.Join(homeDir, ".codex", "sessions")
	os.MkdirAll(codexDir, 0755)

	// File with two session_meta lines: first wrong CWD, second correct
	os.WriteFile(filepath.Join(codexDir, "multi.jsonl"), []byte(
		`{"type":"session_meta","payload":{"id":"wrong-1","cwd":"/other"}}`+"\n"+
			`{"type":"session_meta","payload":{"id":"right-2","cwd":"/tmp/work"}}`+"\n",
	), 0644)
	t.Setenv("HOME", homeDir)

	sid, err := findCodexSessionID("/tmp/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sid != "right-2" {
		t.Errorf("session ID = %q, want right-2 (should skip non-matching session_meta and continue scanning)", sid)
	}
}

// --- get_agent recommended_action field ---

func TestGetAgent_RecommendedAction_Paused(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "rec-paused", Project: "p", Section: "s",
		Status: "paused", Backend: "claude",
	})
	resp := h.handleGetAgent(map[string]any{"to": "rec-paused"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["recommended_action"] != "relay_agent" {
		t.Errorf("paused agent recommended_action=%v want relay_agent", m["recommended_action"])
	}
}

func TestGetAgent_RecommendedAction_Stopped(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "rec-stopped", Project: "p", Section: "s",
		Status: "stopped", Backend: "claude",
	})
	resp := h.handleGetAgent(map[string]any{"to": "rec-stopped"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["recommended_action"] != "manual restart" {
		t.Errorf("stopped agent recommended_action=%v want 'manual restart'", m["recommended_action"])
	}
}

func TestGetAgent_RecommendedAction_Running(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "rec-running", Project: "p", Section: "s",
		Status: "running", Backend: "claude",
	})
	resp := h.handleGetAgent(map[string]any{"to": "rec-running"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["recommended_action"] != "monitor" {
		t.Errorf("running agent recommended_action=%v want monitor", m["recommended_action"])
	}
}

// --- relay_agent: paused allowed, stopped blocked ---

func TestRelayAgent_PausedAllowed(t *testing.T) {
	h, s := mustHandler(t)
	// Use /tmp explicitly; on macOS t.TempDir() returns /var/folders/...
	// paths >100 chars, exceeding UNIX_PATH_MAX for .sock.inject listener.
	sockDir, err := os.MkdirTemp("/tmp", "rl")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "agent.sock")
	injectPath := sockPath + ".inject"
	ln, err := net.Listen("unix", injectPath)
	if err != nil {
		t.Fatalf("listen inject: %v", err)
	}
	defer ln.Close()
	go func() {
		// Drain the listener so the dial in handleRelayAgent succeeds.
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()

	s.AgentCreate(storage.Agent{
		ID: "relay-paused", Project: "p", Section: "s",
		Status: "paused", Backend: "claude", SockPath: sockPath,
	})
	resp := h.handleRelayAgent(map[string]any{
		"to":      "relay-paused",
		"content": "hello paused agent",
	})
	if resp.Error != "" {
		t.Fatalf("relay to paused agent should succeed, got error: %s", resp.Error)
	}
}

func TestRelayAgent_StoppedBlocked(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "relay-stopped", Project: "p", Section: "s",
		Status: "stopped", Backend: "claude", SockPath: "/tmp/whatever.sock",
	})
	resp := h.handleRelayAgent(map[string]any{
		"to":      "relay-stopped",
		"content": "hello stopped agent",
	})
	if resp.Error == "" {
		t.Fatal("relay to stopped agent should be blocked, got no error")
	}
	if !strings.Contains(resp.Error, "not running") && !strings.Contains(resp.Error, "stopped") {
		t.Errorf("relay to stopped agent error should mention 'stopped' or 'not running', got: %s", resp.Error)
	}
}

func TestRelayAgent_RunningAllowed(t *testing.T) {
	h, s := mustHandler(t)
	sockDir, err := os.MkdirTemp("/tmp", "rl")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "agent.sock")
	injectPath := sockPath + ".inject"
	ln, err := net.Listen("unix", injectPath)
	if err != nil {
		t.Fatalf("listen inject: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()

	s.AgentCreate(storage.Agent{
		ID: "relay-running", Project: "p", Section: "s",
		Status: "running", Backend: "claude", SockPath: sockPath,
	})
	resp := h.handleRelayAgent(map[string]any{
		"to":      "relay-running",
		"content": "hello running agent",
	})
	if resp.Error != "" {
		t.Fatalf("relay to running agent should succeed, got error: %s", resp.Error)
	}
}

// --- buildAgentExtraEnv (parallel-safe agent identity injection) ---
// spawnAgentProcess builds extraEnv for the spawned backend's environment.
// For opencode/codex we inject YESMEM_SOURCE_AGENT + YESMEM_SESSION_ID so the
// yesmem-mcp child (spawned by the backend) can resolve its own session id
// without relying on the global active_session_opencode proxy-state, which is
// Last-Writer-Wins across parallel agents. Codex additionally gets
// CODEX_THREAD_ID because internal/mcp/server.go resolveClientSessionID reads
// CODEX_THREAD_ID for the codex path (not YESMEM_SESSION_ID).

func containsEnv(kv []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range kv {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

func TestBuildAgentExtraEnv_OpenCode(t *testing.T) {
	env := buildAgentExtraEnv("opencode", "synthetic-oc-uuid")
	if v, ok := containsEnv(env, "YESMEM_SOURCE_AGENT"); !ok || v != "opencode" {
		t.Errorf("YESMEM_SOURCE_AGENT: want opencode, got %q (present=%v)", v, ok)
	}
	if v, ok := containsEnv(env, "YESMEM_SESSION_ID"); !ok || v != "synthetic-oc-uuid" {
		t.Errorf("YESMEM_SESSION_ID: want synthetic-oc-uuid, got %q (present=%v)", v, ok)
	}
	if _, ok := containsEnv(env, "CODEX_THREAD_ID"); ok {
		t.Error("CODEX_THREAD_ID must NOT be set for opencode backend")
	}
	if _, ok := containsEnv(env, "OPENAI_API_KEY"); ok {
		t.Error("OPENAI_API_KEY must NOT be set for opencode backend (codex-only)")
	}
}

func TestBuildAgentExtraEnv_Codex(t *testing.T) {
	env := buildAgentExtraEnv("codex", "synthetic-cx-uuid")
	if v, ok := containsEnv(env, "YESMEM_SOURCE_AGENT"); !ok || v != "codex" {
		t.Errorf("YESMEM_SOURCE_AGENT: want codex, got %q (present=%v)", v, ok)
	}
	if v, ok := containsEnv(env, "YESMEM_SESSION_ID"); !ok || v != "synthetic-cx-uuid" {
		t.Errorf("YESMEM_SESSION_ID: want synthetic-cx-uuid, got %q (present=%v)", v, ok)
	}
	// resolveClientSessionID in internal/mcp/server.go reads CODEX_THREAD_ID
	// for the codex branch, so we must mirror the value there.
	if v, ok := containsEnv(env, "CODEX_THREAD_ID"); !ok || v != "synthetic-cx-uuid" {
		t.Errorf("CODEX_THREAD_ID: want synthetic-cx-uuid, got %q (present=%v)", v, ok)
	}
}

func TestBuildAgentExtraEnv_ClaudeNoInjection(t *testing.T) {
	// Claude backend owns its session via --session-id flag; no ENV needed.
	env := buildAgentExtraEnv("claude", "claude-sess")
	for _, key := range []string{"YESMEM_SOURCE_AGENT", "YESMEM_SESSION_ID", "CODEX_THREAD_ID"} {
		if v, ok := containsEnv(env, key); ok {
			t.Errorf("%s must not be set for claude backend, got %q", key, v)
		}
	}
}

func TestBuildAgentExtraEnv_CodexAuthKeyMerged(t *testing.T) {
	// The helper reads ~/.codex/auth.json when backend=="codex". If the file
	// is absent (typical CI/sandbox), loadCodexAuthEnv returns an error and
	// the helper continues without OPENAI_API_KEY — but the identity vars
	// must still be present so the MCP child can self-identify.
	env := buildAgentExtraEnv("codex", "uuid-with-no-auth")
	if v, ok := containsEnv(env, "YESMEM_SOURCE_AGENT"); !ok || v != "codex" {
		t.Errorf("YESMEM_SOURCE_AGENT missing even without auth file: %q (ok=%v)", v, ok)
	}
	if v, ok := containsEnv(env, "CODEX_THREAD_ID"); !ok || v != "uuid-with-no-auth" {
		t.Errorf("CODEX_THREAD_ID missing without auth file: %q (ok=%v)", v, ok)
	}
}

func TestBuildAgentExtraEnv_EmptySessionID(t *testing.T) {
	// Defensive: empty sessionID must not inject empty YESMEM_SESSION_ID=, as
	// that would make resolveClientSessionID return "" with sa="opencode" —
	// causing resolveSessionID to skip the MCP _session_id path and fall
	// through to proxy-state, defeating the fix.
	env := buildAgentExtraEnv("opencode", "")
	if _, ok := containsEnv(env, "YESMEM_SESSION_ID"); ok {
		t.Errorf("YESMEM_SESSION_ID must be omitted when sessionID is empty, got %v", env)
	}
	if _, ok := containsEnv(env, "YESMEM_SOURCE_AGENT"); ok {
		t.Errorf("YESMEM_SOURCE_AGENT must also be omitted when sessionID is empty (no partial identity)")
	}
}

func TestHandleOpenAgentTerminal_Resume(t *testing.T) {
	h, _ := mustHandler(t)
	h.disableAgentProcesses = true
	resp := h.Handle(Request{Method: "open_agent_terminal", Params: map[string]any{
		"session_id": "ses_abc123",
		"work_dir":   "/home/chief/projects/deepseek",
		"project":    "deepseek",
		"backend":    "opencode",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var out map[string]any
	json.Unmarshal(resp.Result, &out)
	if out["resume"] != true {
		t.Fatalf("resume = %v, want true", out["resume"])
	}
	agentID, _ := out["agent_id"].(string)
	if agentID == "" {
		t.Fatal("no agent_id returned")
	}
	agent, err := h.store.AgentGet(agentID)
	if err != nil || agent == nil {
		t.Fatalf("agent %s not created: %v", agentID, err)
	}
	if agent.OpencodeSessionID != "ses_abc123" {
		t.Fatalf("OpencodeSessionID = %q, want ses_abc123", agent.OpencodeSessionID)
	}
	if agent.Status == "" {
		t.Fatal("empty status")
	}
}

func TestHandleOpenAgentTerminal_New(t *testing.T) {
	h, _ := mustHandler(t)
	h.disableAgentProcesses = true
	resp := h.Handle(Request{Method: "open_agent_terminal", Params: map[string]any{
		"work_dir": "/home/chief/projects/greenwebsite",
		"project":  "greenwebsite",
		"backend":  "opencode",
	}})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var out map[string]any
	json.Unmarshal(resp.Result, &out)
	if out["resume"] != false {
		t.Fatalf("resume = %v, want false", out["resume"])
	}
	agentID, _ := out["agent_id"].(string)
	agent, err := h.store.AgentGet(agentID)
	if err != nil || agent == nil {
		t.Fatalf("agent not created: %v", err)
	}
	if agent.OpencodeSessionID != "" {
		t.Fatalf("OpencodeSessionID = %q, want empty for new session", agent.OpencodeSessionID)
	}
}
