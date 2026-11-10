package daemon

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/orchestrator"
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
		"project":  "proj",
		"work_dir": t.TempDir(),
		"section":  "task-a",
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

	resp := h.handleSpawnAgent(map[string]any{"project": "proj", "work_dir": t.TempDir(), "section": "task-a"})
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
		"work_dir": t.TempDir(),
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
		"work_dir": t.TempDir(),
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
		"work_dir": t.TempDir(),
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
		"work_dir": t.TempDir(),
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
		"work_dir": t.TempDir(),
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
		"work_dir":     t.TempDir(),
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
		"work_dir": t.TempDir(),
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
		"work_dir": t.TempDir(),
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
	// The test process stands in for the backend: the identity gate needs its
	// command line to name it (same seam as the other identity tests).
	stubProcCmdline(t, agentProcCmdlineStub)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-agent-1", Status: "pending", Backend: "opencode",
	})

	resp := h.handleRegisterAgent(map[string]any{
		// A live pid: register_agent must not bind a process nobody holds (YM-351).
		// session_id is the row's capability — only the spawner holds it (YM-356).
		"id": "agent-1", "pid": float64(os.Getpid()), "sock_path": "/tmp/agent-1.sock",
		"session_id": "sess-agent-1",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-1")
	if agent.Status != "running" {
		t.Errorf("status = %q, want running", agent.Status)
	}
	if agent.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", agent.PID, os.Getpid())
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

// YM-338 (Los 24d): agents.sock_path is no longer the dial target, so an empty
// value is not a relay error — the path comes from the id. Only an id that cannot
// name a socket inside the data directory is refused.
func TestHandleRelayAgent_RefusesAnUnderivableSocketPath(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude",
	})
	h.dataDir = "" // nothing to derive a path from

	resp := h.handleRelayAgent(map[string]any{"to": "agent-1", "content": "hello"})
	if resp.Error == "" {
		t.Fatal("expected error for an underivable socket path")
	}
	if !strings.Contains(resp.Error, "socket path could not be derived") {
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
		"work_dir": t.TempDir(),
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

// Agents whose process is already gone (error, crashed) must be
// stoppable too: stop is how an orchestrator closes an agent for good. Without
// it an `error` agent with a restart strategy is revived by attemptRestart and
// cannot be ended through the API at all.
func TestHandleStopAgent_DormantStatusesAreStoppable(t *testing.T) {
	for _, status := range []string{"error", "crashed"} {
		t.Run(status, func(t *testing.T) {
			h, s := mustHandler(t)
			s.AgentCreate(storage.Agent{
				ID: "agent-1", Project: "proj", Section: "task",
				Status: status, Backend: "opencode",
			})

			resp := h.handleStopAgent(map[string]any{"to": "agent-1"})
			if resp.Error != "" {
				t.Fatalf("unexpected error for status %q: %s", status, resp.Error)
			}
			a, _ := s.AgentGet("agent-1")
			if a.Status != "stopped" || a.StoppedAt == "" {
				t.Errorf("persisted status=%q stopped_at=%q, want stopped with a timestamp", a.Status, a.StoppedAt)
			}
		})
	}
}

// A stopped error agent must not be revived by the supervisor.
func TestHandleStopAgent_ErrorAgentIsNotRestartedAfterStop(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "error", Backend: "claude", RestartStrategy: "permanent", MaxRestarts: 3,
	})

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}
	h.attemptRestart()

	a, _ := s.AgentGet("agent-1")
	if a.Status != "stopped" || a.RestartCount != 0 {
		t.Errorf("status=%q restart_count=%d, want stopped/0 (no restart after stop)", a.Status, a.RestartCount)
	}
}

// The PID of a dormant agent is stale by definition; it may already belong to an
// unrelated process. Stopping such an agent must not signal it.
func TestHandleStopAgent_DormantAgentDoesNotSignalStalePID(t *testing.T) {
	h, s := mustHandler(t)
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	exited := make(chan struct{})
	go func() { bystander.Wait(); close(exited) }()
	t.Cleanup(func() { bystander.Process.Kill(); <-exited })

	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "error", Backend: "claude", PID: bystander.Process.Pid,
	})

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}
	select {
	case <-exited:
		t.Error("stop sent a signal to the stale PID of a dormant agent")
	case <-time.After(300 * time.Millisecond):
	}
}

// register_agent binds a pid too, so it must write the spawn marker with it —
// otherwise a row bound this way is unverifiable and every guard reads a live
// agent as gone (the F1 failure mode, reached through a second writer).
func TestHandleRegisterAgent_WritesSpawnMarker(t *testing.T) {
	// The identity check reads the process table via /proc, which darwin does not
	// expose; isAgentProcess abstains there by design.
	if !procTableReadable {
		t.Skip("the agent identity check needs the process table")
	}
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	if err := s.AgentCreate(storage.Agent{
		ID: "reg-1", Project: "p", Section: "s1", SessionID: "sess-reg-1",
		Status: "pending", Backend: "opencode",
		CreatedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleRegisterAgent(map[string]any{
		"id": "reg-1", "pid": float64(os.Getpid()), "sock_path": "/tmp/reg.sock",
		"session_id": "sess-reg-1",
	})
	if resp.Error != "" {
		t.Fatalf("register_agent: %s", resp.Error)
	}

	a, err := s.AgentGet("reg-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "running" || a.PID != os.Getpid() || a.SockPath != "/tmp/reg.sock" {
		t.Errorf("register did not record the process: status=%q pid=%d sock=%q", a.Status, a.PID, a.SockPath)
	}
	if a.LastRestartAt == "" {
		t.Fatal("register_agent wrote a pid without the spawn marker — this row can never verify")
	}
	if !isAgentProcess(*a) {
		t.Error("a row bound by register_agent must verify as its own process")
	}
}

// --- C2.2: a restart must bind the backend process, not the launcher ---

// stubRestartSpawn makes the spawn/restart path harmless and observable: the
// bridge runs a sleeping child instead of a real LLM CLI, the display command is
// a no-op instead of a terminal window, and the returned func reports the pid
// the bridge actually created.
func stubRestartSpawn(t *testing.T) func() int {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not available: %v", err)
	}
	// The restart resolves the opencode session's directory; without this stub the
	// tests would query the developer's live opencode database.
	origDB := opencodeDBPath
	opencodeDBPath = func() string { return filepath.Join(t.TempDir(), "no-opencode.db") }
	t.Cleanup(func() { opencodeDBPath = origDB })
	var pid int
	// The stub child is sleep, so the bind's identity gate needs a process whose
	// command line names the backend the daemon asked for — otherwise the test
	// passes without ever binding (cold review F4). Derived from the name the
	// spawn passes, so it holds for every backend.
	prevCmdline := procCmdline
	t.Cleanup(func() { procCmdline = prevCmdline })
	prevBridge, prevDisplay := newAgentBridge, agentDisplayCommand
	newAgentBridge = func(name string, args []string, sock, workDir string, extraEnv ...string) (*orchestrator.AgentBridge, error) {
		procCmdline = func(int) string { return name + "\x00" }
		b, err := orchestrator.NewAgentBridge(sleepBin, []string{"30"}, sock, workDir)
		if err == nil {
			pid = b.Cmd.Process.Pid
		}
		return b, err
	}
	agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
		return trueBin, nil
	}
	t.Cleanup(func() {
		newAgentBridge, agentDisplayCommand = prevBridge, prevDisplay
		if pid != 0 {
			if p, err := os.FindProcess(pid); err == nil {
				p.Kill()
			}
		}
	})
	return func() int { return pid }
}

// attemptRestart used to remove the sockets and launch only the console client
// (agent-tty dials an existing socket; it never creates a backend), recording the
// terminal emulator's PID as the agent's. The row now has to hold the process the
// bridge created — the same invariant bindAgentProcess writes everywhere else.
func TestAttemptRestart_BindsTheBackendProcess(t *testing.T) {
	// The identity check reads the process table via /proc, which darwin does not
	// expose; isAgentProcess abstains there by design.
	if !procTableReadable {
		t.Skip("the agent identity check needs the process table")
	}
	h, s := mustHandler(t)
	h.disableAgentProcesses = false
	bridgePid := stubRestartSpawn(t)
	sockPath := filepath.Join(shortSockDir(t), "restart-bind.sock")

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-bind", Project: "p", Section: "s-rb", SessionID: "sess-rb",
		Status: "error", Backend: "opencode", OpencodeSessionID: "ses_rb",
		SockPath: sockPath, RestartStrategy: "permanent",
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.attemptRestart()

	// The bind runs in the spawn goroutine.
	var got *storage.Agent
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a, err := s.AgentGet("restart-bind"); err == nil && a != nil && a.PID != 0 {
			got = a
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("attemptRestart never bound a process to the row")
	}
	if want := bridgePid(); want == 0 {
		t.Fatal("the restart created no bridge — no backend process was started at all")
	} else if got.PID != want {
		t.Errorf("row pid = %d, want the bridge's process %d (the launcher's pid is not the agent's)", got.PID, want)
	}
	if got.LastRestartAt == "" {
		t.Error("spawn marker missing — the row's process can never be verified")
	}
}

// The socket paths belong to the agent id, not to one process, and a resume or a
// restart can bind a successor while the previous process is still winding down.
// The stale process's exit tail must therefore leave the successor's pid, socket
// path and status — and its socket files — alone (YM-70).
func TestSpawnAgentProcess_StaleExitTailLeavesTheSuccessorsBindingAlone(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	h.disableAgentProcesses = false
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not available: %v", err)
	}
	sockPath := filepath.Join(shortSockDir(t), "stale-exit.sock")
	prevBridge, prevDisplay := newAgentBridge, agentDisplayCommand
	newAgentBridge = func(name string, args []string, sock, workDir string, extraEnv ...string) (*orchestrator.AgentBridge, error) {
		// The child exits on its own after a moment, so the exit tail runs while
		// the test still holds the row.
		return orchestrator.NewAgentBridge(sleepBin, []string{"0.5"}, sock, workDir)
	}
	agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
		return trueBin, nil
	}
	t.Cleanup(func() { newAgentBridge, agentDisplayCommand = prevBridge, prevDisplay })

	if err := s.AgentCreate(storage.Agent{
		ID: "stale-exit", Project: "p", Section: "stale-exit-sec", SessionID: "sess-stale-exit",
		Status: "pending", Backend: "opencode", OpencodeSessionID: "ses_stale",
		SockPath: sockPath,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	go h.spawnAgentProcess("stale-exit", "sess-stale-exit", "p", "stale-exit-sec", "", sockPath,
		t.TempDir(), "opencode", "", "", 0, 0, "", true)

	var firstPID int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a, err := s.AgentGet("stale-exit"); err == nil && a != nil && a.PID != 0 {
			firstPID = a.PID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if firstPID == 0 {
		t.Fatal("the spawn never bound a process")
	}

	// A successor takes the row over while the first process is still winding
	// down — the shape a resume or a restart produces.
	const successorPID = 424242
	if err := s.AgentUpdate("stale-exit", map[string]any{
		"pid": successorPID, "sock_path": sockPath, "status": "running",
	}); err != nil {
		t.Fatalf("bind the successor: %v", err)
	}

	waitForProcessExit(t, firstPID)

	a, err := s.AgentGet("stale-exit")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.PID != successorPID || a.Status != "running" || a.SockPath != sockPath {
		t.Errorf("the stale exit tail clobbered the successor: pid=%d status=%q sock=%q, want %d/running/%q",
			a.PID, a.Status, a.SockPath, successorPID, sockPath)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Errorf("the stale process unlinked the successor's socket: %v", err)
	}
}

// The exit tail is the only writer of "finished", and the heartbeat reads a
// running row with a dead process as a crash: a clean exit has to be recorded,
// with the pid and the socket path cleared. The field set the tail passes must be
// exactly what AgentFinalizeExit accepts — the first version of the CAS refused
// the caller's map (it carried pid and sock_path) and every normal exit turned
// into a crash recovery (found in review, 5.2/5.3/5.4).
//
// Deterministic by construction (YM-420): the child is a sleep that is still
// alive when the bind runs, and the identity gate accepts it through the cmdline
// stub, so the spawn binds and no teardown signal is sent. The child then exits on
// its own with status 0 and the tail records exactly that. The earlier version
// used `true` and left the bind to refuse: whether the refused-bind teardown
// SIGTERMed a still-running `true` (waitErr "signal: terminated" -> status "error")
// or found it already gone depended on scheduling, which made this test fail under
// load about once in 300 runs.
func TestSpawnAgentProcess_ExitTailRecordsTheTerminalState(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	h.disableAgentProcesses = false
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not available: %v", err)
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	sockPath := filepath.Join(shortSockDir(t), "exit-tail.sock")
	prevBridge, prevDisplay := newAgentBridge, agentDisplayCommand
	newAgentBridge = func(name string, args []string, sock, workDir string, extraEnv ...string) (*orchestrator.AgentBridge, error) {
		// Lives past the bind, then exits 0 on its own.
		return orchestrator.NewAgentBridge(sleepBin, []string{"0.5"}, sock, workDir)
	}
	agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
		return trueBin, nil
	}
	t.Cleanup(func() { newAgentBridge, agentDisplayCommand = prevBridge, prevDisplay })

	if err := s.AgentCreate(storage.Agent{
		ID: "exit-tail", Project: "p-exit-tail", Section: "exit-tail-sec",
		SessionID: "sess-exit-tail", Status: "pending", Backend: "opencode",
		OpencodeSessionID: "ses_exit_tail", SockPath: sockPath,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.spawnAgentProcess("exit-tail", "sess-exit-tail", "p-exit-tail", "exit-tail-sec", "",
		sockPath, t.TempDir(), "opencode", "", "", 0, 0, "", true)

	a, err := s.AgentGet("exit-tail")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "finished" {
		t.Fatalf("status = %q, want finished — a clean exit was not recorded, so crashRecovery reads the row as a crash", a.Status)
	}
	if a.PID != 0 || a.SockPath != "" {
		t.Errorf("pid=%d sock_path=%q, want both cleared", a.PID, a.SockPath)
	}
	if a.StoppedAt == "" {
		t.Error("stopped_at missing")
	}
}

// waitForProcessExit waits until a pid no longer exists.
func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !isPIDAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d never exited", pid)
}

// A restart must not advertise a running agent while no process is bound: a row
// that looks running with a stale PID is exactly the shape crashRecovery now
// closes, so a tick inside the window would quarantine the session and spawn a
// second agent for the same section.
func TestAttemptRestart_DoesNotLookRunningBeforeTheBind(t *testing.T) {
	h, s := mustHandler(t)
	h.disableAgentProcesses = false
	bridgePid := stubRestartSpawn(t)
	sockPath := filepath.Join(shortSockDir(t), "restart-window.sock")

	// Hold the spawn between the bridge fork and the bind — that is the window.
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not available: %v", err)
	}
	atDisplay := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
		atDisplay <- struct{}{}
		<-release
		return trueBin, nil
	}
	// Restored by stubRestartSpawn's cleanup, which runs after this one (LIFO).
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-window", Project: "p", Section: "s-win", SessionID: "sess-win",
		Status: "error", Backend: "opencode", OpencodeSessionID: "ses_win",
		SockPath: sockPath, RestartStrategy: "permanent",
		PID:       deadPID, // stale PID from the failed process, as an error row carries it
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.attemptRestart()
	<-atDisplay

	a, err := s.AgentGet("restart-window")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "spawning" {
		t.Errorf("status = %q during the restart window, want spawning — a running row with no process is what crashRecovery closes", a.Status)
	}
	if a.PID == bridgePid() {
		t.Error("the row already holds the bridge's pid in the window; the bind should be the only writer of it")
	}

	release <- struct{}{}

	var bound *storage.Agent
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if x, err := s.AgentGet("restart-window"); err == nil && x != nil && x.PID == bridgePid() {
			bound = x
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bound == nil {
		got, _ := s.AgentGet("restart-window")
		t.Fatalf("the restart never bound the bridge's pid %d (row: status=%q pid=%v)", bridgePid(), got.Status, got.PID)
	}
	if bound.Status != "running" {
		t.Errorf("after the bind: status = %q, want running", bound.Status)
	}
}

// --- E2: a restart relaunches in the directory the row recorded ---

// restartSpawn is what a stubbed spawn hands to the bridge: the process it created
// and the directory it was launched in. The fields are guarded because the spawn
// may run in a goroutine (the production shape) while the test reads them.
type restartSpawn struct {
	mu      sync.Mutex
	pid     int
	workDir string
	args    []string
}

func (r *restartSpawn) set(pid int, workDir string) {
	r.mu.Lock()
	r.pid, r.workDir = pid, workDir
	r.mu.Unlock()
}

// setArgs records the `yesmem agent-tty` argv the spawn handed to the terminal.
func (r *restartSpawn) setArgs(args []string) {
	r.mu.Lock()
	r.args = append([]string(nil), args...)
	r.mu.Unlock()
}

func (r *restartSpawn) PID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pid
}

func (r *restartSpawn) WorkDir() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workDir
}

// Args returns the recorded agent-tty argv, or nil when no window was asked for.
func (r *restartSpawn) Args() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.args...)
}

// flagValue returns the value that follows flag in args, or "" when the flag is
// absent.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// stubRestartSpawnSync drives a restart synchronously: the bridge runs a child
// that lives for childLife (the spawn waits for it, so the call returns when the
// child is gone) and the result reports what the spawn passed on.
func stubRestartSpawnSync(t *testing.T, childLife string) *restartSpawn {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true not available: %v", err)
	}
	// A restart resolves the opencode session's directory; without this stub the
	// test would query the developer's live opencode database.
	origDB := opencodeDBPath
	opencodeDBPath = func() string { return filepath.Join(t.TempDir(), "no-opencode.db") }
	t.Cleanup(func() { opencodeDBPath = origDB })
	spawn := &restartSpawn{}
	// The stub child is sleep, so the bind's identity gate needs a process whose
	// command line names the backend the daemon asked for — otherwise the test
	// passes without ever binding (cold review F4). Derived from the name the
	// spawn passes, so it holds for every backend.
	prevCmdline := procCmdline
	t.Cleanup(func() { procCmdline = prevCmdline })
	prevBridge, prevDisplay := newAgentBridge, agentDisplayCommand
	newAgentBridge = func(name string, args []string, sock, workDir string, extraEnv ...string) (*orchestrator.AgentBridge, error) {
		procCmdline = func(int) string { return name + "\x00" }
		b, err := orchestrator.NewAgentBridge(sleepBin, []string{childLife}, sock, workDir)
		if err == nil {
			spawn.set(b.Cmd.Process.Pid, workDir)
		}
		return b, err
	}
	agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
		spawn.setArgs(innerArgs)
		return trueBin, nil
	}
	t.Cleanup(func() { newAgentBridge, agentDisplayCommand = prevBridge, prevDisplay })
	return spawn
}

// A restart must relaunch the agent where its row says it ran. attemptRestart
// resolved the work dir from agents.project alone, which lands on the repository
// root — a loop agent came back in the wrong tree, where its own skill guard
// fails. handleResumeAgent already reads the recorded work_dir; the restart path
// now does the same.
func TestRestartAgentProcess_UsesTheRecordedWorkDir(t *testing.T) {
	h, s := mustHandler(t)
	spawn := stubRestartSpawnSync(t, "0.3")
	workDir := t.TempDir()

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-wd", Project: "p", Section: "s-restart-wd", SessionID: "sess-restart-wd",
		Status: "error", Backend: "claude", RestartStrategy: "permanent", WorkDir: workDir,
		SockPath:  filepath.Join(shortSockDir(t), "restart-wd.sock"),
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	stored, err := s.AgentGet("restart-wd")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if stored.WorkDir != workDir {
		t.Fatalf("fixture: work_dir = %q, want %q", stored.WorkDir, workDir)
	}

	h.restartAgentProcess(*stored)

	if got := spawn.WorkDir(); got != workDir {
		t.Errorf("restart launched the backend in %q, want the row's work_dir %q — a worktree agent must come back in its own tree", got, workDir)
	}
}

// A work_dir that no longer exists must not hand the spawn a path the backend
// cannot start in — and since YM-351 the restart is refused outright instead of
// falling back to the project resolver or the daemon's run dir. Rationale: a row
// whose recorded tree is gone is the shape of a finished task (a loop worktree is
// removed after its branch is merged). Observed 18.10.2026: the replacement came
// up in ~/.claude/yesmem-run/workdir, without a repository, and redid the phases
// of a merged task. The row is closed with the reason instead, so the
// orchestrator sees that nothing is running there.
func TestRestartAgentProcess_VanishedWorkDirIsRefused(t *testing.T) {
	h, s := mustHandler(t)
	spawn := stubRestartSpawnSync(t, "0.3")
	vanished := filepath.Join(t.TempDir(), "removed-worktree")

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-gone-wd", Project: "p", Section: "s-restart-gone", SessionID: "sess-restart-gone",
		Status: "error", Backend: "claude", RestartStrategy: "permanent", WorkDir: vanished,
		SockPath:  filepath.Join(shortSockDir(t), "restart-gone.sock"),
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	stored, err := s.AgentGet("restart-gone-wd")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.restartAgentProcess(*stored)

	if pid := spawn.PID(); pid != 0 {
		t.Errorf("the restart started a backend with PID %d for a tree that is gone", pid)
	}
	after, err := s.AgentGet("restart-gone-wd")
	if err != nil || after == nil {
		t.Fatalf("AgentGet after the refused restart: %v", err)
	}
	if after.Status != "stopped" {
		t.Errorf("status = %q, want stopped (the refusal closes the row)", after.Status)
	}
	if !strings.Contains(after.Progress, "removed-worktree") {
		t.Errorf("progress = %q, want the vanished directory named", after.Progress)
	}
}

// --- E3: the restart window ---

// TestRestartAgentProcess_PlacesTheWindowOnTheCallersDesktop (YM-458): the restart
// path passed parentPid=0 and parentWindow="", so buildAgentTTYArgs emitted no
// placement flags and the replacement window opened on whatever desktop happened
// to be focused. The resume routes resolve the caller's window; a restart has no
// request to read, only the session recorded on the row, and now resolves that one
// the same way.
func TestRestartAgentProcess_PlacesTheWindowOnTheCallersDesktop(t *testing.T) {
	h, s := mustHandler(t)
	h.agentTerminal = "ghostty"
	spawn := stubRestartSpawnSync(t, "0.3")
	workDir := t.TempDir()
	register(t, h, "opencode:ses_orch", "0x06800004", 0)

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-place", Project: "p", Section: "s-restart-place", SessionID: "sess-restart-place",
		Status: "error", Backend: "claude", RestartStrategy: "permanent", WorkDir: workDir,
		CallerSession: "opencode:ses_orch",
		SockPath:      filepath.Join(shortSockDir(t), "restart-place.sock"),
		CreatedAt:     time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	stored, err := s.AgentGet("restart-place")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.restartAgentProcess(*stored)

	args := spawn.Args()
	if got := flagValue(args, "--parent-window"); got != "0x06800004" {
		t.Errorf("agent-tty argv = %q, want --parent-window 0x06800004 — without it the replacement window opens on the focused desktop instead of the caller's", args)
	}
}

// A caller that is gone must not turn into an error: the restart keeps today's
// behaviour (no placement flags, window on the focused desktop). Losing the
// window placement is a cosmetic degradation, losing the agent is not.
func TestRestartAgentProcess_UnknownCallerKeepsTheDefaultPlacement(t *testing.T) {
	h, s := mustHandler(t)
	h.agentTerminal = "ghostty"
	spawn := stubRestartSpawnSync(t, "0.3")
	workDir := t.TempDir()

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-nocaller", Project: "p", Section: "s-restart-nocaller", SessionID: "sess-restart-nocaller",
		Status: "error", Backend: "claude", RestartStrategy: "permanent", WorkDir: workDir,
		CallerSession: "opencode:ses_long_gone",
		SockPath:      filepath.Join(shortSockDir(t), "restart-nocaller.sock"),
		CreatedAt:     time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	stored, err := s.AgentGet("restart-nocaller")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.restartAgentProcess(*stored)

	if got := flagValue(spawn.Args(), "--parent-window"); got != "" {
		t.Errorf("agent-tty argv = %q, want no --parent-window for a caller without a registered window", spawn.Args())
	}
	after, err := s.AgentGet("restart-nocaller")
	if err != nil || after == nil {
		t.Fatalf("AgentGet after the restart: %v", err)
	}
	if after.Status == "stopped" {
		t.Errorf("the restart was closed (progress=%q) because the caller session is unknown", after.Progress)
	}
}

// A stop that lands between the restart's "spawning" write and its bind must win.
// The bind wrote status and pid unconditionally, so the stopped row came back as
// "running" with a live backend: the orchestrator had finalized it and nothing
// owned that process any more.
func TestRestartWindow_StopIsNotOverwrittenByTheBind(t *testing.T) {
	h, s := mustHandler(t)
	// The child outlives the test by far: only the spawn's own teardown can
	// explain a dead process, which is what the second half of the fix claims.
	spawn := stubRestartSpawnSync(t, "60")
	t.Cleanup(func() {
		if p := spawn.PID(); p != 0 {
			if proc, err := os.FindProcess(p); err == nil {
				proc.Kill()
			}
		}
	})
	sockPath := filepath.Join(shortSockDir(t), "restart-race.sock")

	if err := s.AgentCreate(storage.Agent{
		ID: "restart-race", Project: "p", Section: "s-restart-race", SessionID: "sess-restart-race",
		// "spawning" with a stale pid is the row attemptRestart leaves behind
		// before the relaunch goroutine binds the new process.
		Status: "spawning", Backend: "claude", RestartStrategy: "permanent",
		PID: deadPID, SockPath: sockPath, CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	// The stop lands inside the window.
	if resp := h.handleStopAgent(map[string]any{"to": "restart-race"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}
	stored, err := s.AgentGet("restart-race")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	// The relaunch runs in its own goroutine in production; the same shape is
	// needed here to observe the row while the new backend is still alive.
	go h.restartAgentProcess(*stored)

	waitFor := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitFor) && spawn.PID() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	pid := spawn.PID()
	if pid == 0 {
		t.Fatal("the restart created no process at all")
	}

	// Through the whole teardown the row must stay the orchestrator's (stopped,
	// without a process), and the process must disappear — it was created after
	// the stop and no row owns it.
	gone := time.Now().Add(10 * time.Second)
	for time.Now().Before(gone) && isPIDAlive(pid) && !isProcessZombie(strconv.Itoa(pid)) {
		a, err := s.AgentGet("restart-race")
		if err != nil {
			t.Fatalf("AgentGet: %v", err)
		}
		if a.Status != "stopped" || a.PID != 0 {
			t.Fatalf("status=%q pid=%d, want stopped/0 — the bind revived a row the orchestrator had stopped", a.Status, a.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if isPIDAlive(pid) && !isProcessZombie(strconv.Itoa(pid)) {
		t.Fatalf("the process %d of a spawn stopped in the window is still running — nothing owns it (the child sleeps 60s, so it cannot have exited by itself)", pid)
	}

	a, err := s.AgentGet("restart-race")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("after the spawn: status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("the stopped spawn left its socket behind (stat err=%v)", err)
	}
}

// A "spawning" row is what a daemon death inside the window leaves behind: no
// guard touches it and nothing retries it, so stop_agent is the only way out. It
// must close the row without signalling the stale PID it carries.
func TestHandleStopAgent_SpawningRowLeftByADeadDaemonIsClosable(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "spawn-left", Project: "proj", Section: "yesloop-spawn-left",
		Status: "spawning", Backend: "claude", PID: bystander.Process.Pid,
		SockPath: filepath.Join(shortSockDir(t), "spawn-left.sock"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleStopAgent(map[string]any{"to": "spawn-left"})
	if resp.Error != "" {
		t.Fatalf("a spawning row must be closable by hand: %s", resp.Error)
	}
	a, err := s.AgentGet("spawn-left")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
	select {
	case <-exited:
		t.Error("stop signalled the stale PID of a spawning row")
	case <-time.After(300 * time.Millisecond):
	}
}

// --- C6: stop signals only verified agent processes ---

// startBystander spawns a process that dies when signalled, plus a channel that
// closes when it exits — the observable that proves no signal was sent.
func startBystander(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	exited := make(chan struct{})
	go func() { bystander.Wait(); close(exited) }()
	t.Cleanup(func() { bystander.Process.Kill(); <-exited })
	return bystander, exited
}

// A live-status agent whose PID now belongs to an unrelated process is finalized
// without a signal. The status is a claim about the past; signal 0 only says
// "some process holds this PID" (Learning #101907).
func TestHandleStopAgent_LiveAgentWithBystanderPIDNotSignalled(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "paused", Backend: "claude", PID: bystander.Process.Pid,
		SockPath: filepath.Join(t.TempDir(), "gone.sock"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}

	a, _ := s.AgentGet("agent-1")
	if a.Status != "stopped" || a.PID != 0 || a.StoppedAt == "" {
		t.Errorf("status=%q pid=%d stopped_at=%q, want stopped/0/timestamp", a.Status, a.PID, a.StoppedAt)
	}
	// The withheld signal must be visible: an operator reading "stopped" expects
	// the process to be gone.
	if !strings.Contains(a.Progress, "pid not verified") {
		t.Errorf("progress = %q, want it to record that no signal was sent", a.Progress)
	}
	select {
	case <-exited:
		t.Error("stop_agent signalled a PID that belongs to an unrelated process")
	case <-time.After(300 * time.Millisecond):
	}
}

// The positive control for the gate: a *verified* agent process is still
// signalled. The child is a real sleep, declared to be a claude process.
func TestHandleStopAgent_SignalsVerifiedAgentProcess(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude", PID: bystander.Process.Pid,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Error("stop_agent did not signal a verified agent process")
	}
}

// isPIDAlive counts a zombie as alive, so neither a status check nor signal 0
// can tell this row from a live agent. Its cmdline is empty — which is what the
// identity check sees. The row must therefore take the dormant path: no
// inject-socket exit, no signal, only finalization. The fake socket makes that
// observable, because a gate that trusted signal 0 would have dialled it.
func TestHandleStopAgent_ZombiePIDTakesDormantPath(t *testing.T) {
	h, s := mustHandler(t)
	sockPath := filepath.Join(shortSockDir(t), "zombie.sock")
	rec := startFakeInjectSocket(t, sockPath+".inject")

	zombie := exec.Command("sleep", "0")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start zombie candidate: %v", err)
	}
	pid := zombie.Process.Pid
	// Deliberately not reaped until cleanup: that keeps it a zombie.
	t.Cleanup(func() { zombie.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(readProcFile(pid, "cmdline")) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if !isPIDAlive(pid) {
		t.Fatalf("isPIDAlive(zombie %d) = false, want true — signal 0 says alive", pid)
	}

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "running", Backend: "claude", PID: pid, SockPath: sockPath,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}

	a, _ := s.AgentGet("agent-1")
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped with pid 0", a.Status, a.PID)
	}
	awaitNoConnection(t, rec)
}

// The same gate on the stop-all path, which signals every PID in the project in
// one call: without it a single stop-all reaches every recycled PID there.
func TestHandleStopAllAgents_BystanderNotSignalled(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "sa-bystander", Project: "proj", Section: "s1",
		Status: "running", Backend: "claude", PID: bystander.Process.Pid,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleStopAllAgents(map[string]any{"project": "proj"})
	if resp.Error != "" {
		t.Fatalf("stop-all: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if stopped, _ := m["stopped"].(float64); stopped != 1 {
		t.Errorf("stopped = %v, want 1 (row must still be finalized)", stopped)
	}
	a, _ := s.AgentGet("sa-bystander")
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped with pid 0", a.Status, a.PID)
	}
	select {
	case <-exited:
		t.Error("stop-all signalled a PID that belongs to an unrelated process")
	case <-time.After(300 * time.Millisecond):
	}
}

// A crashed agent that is stopped during the respawn cooldown must stay
// stopped: the delayed respawn must not bring a replacement up anyway.
// The replacement must come back as the same agent. Backend, model and theme are
// spawn parameters that the project cannot re-derive, and the row is the only
// place they survive a crash: respawnAgent handed none of them to spawn_agent, so
// an opencode agent with an explicit model came back as a default-backend agent
// with the default model (YM-64).
func TestRespawnAgent_KeepsBackendModelAndTheme(t *testing.T) {
	h, s := mustHandler(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "respawn-params", Project: "respawn-params-proj", Section: "respawn-params-sec",
		Status: "crashed", Backend: "opencode", Model: "zai/glm-5.2", Theme: "tokyonight",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	crashed, err := s.AgentGet("respawn-params")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.respawnAgent(*crashed)

	agents, err := s.AgentList("respawn-params-proj")
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	var replacement *storage.Agent
	for i := range agents {
		if agents[i].ID != "respawn-params" {
			replacement = &agents[i]
		}
	}
	if replacement == nil {
		t.Fatal("respawnAgent created no replacement agent")
	}
	if replacement.Backend != "opencode" {
		t.Errorf("replacement backend = %q, want opencode — the replacement started with the default backend", replacement.Backend)
	}
	if replacement.Model != "zai/glm-5.2" {
		t.Errorf("replacement model = %q, want zai/glm-5.2 — the explicit model was lost in the respawn", replacement.Model)
	}
	if replacement.Theme != "tokyonight" {
		t.Errorf("replacement theme = %q, want tokyonight — the explicit theme was lost in the respawn", replacement.Theme)
	}
	// The parameters have to survive the round trip through the row, not just the
	// in-memory struct: handleSpawnAgent persists them and the replacement is read
	// back from the store here.
	if replacement.ID == "" {
		t.Error("replacement row was not read back from the store")
	}
}

// respawnAgent must read the id out of the spawn response. handleSpawnAgent
// returns "id"; the code read "agent_id", so the type assertion failed silently:
// the replacement agent never inherited retry_count and the "respawned X as Y"
// line never appeared, leaving a crash loop unbounded (retry_count stays 0).
func TestRespawnAgent_InheritsRetryCountFromSpawnResponse(t *testing.T) {
	h, s := mustHandler(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "respawn-1", Project: "respawn-proj", Section: "respawn-sec",
		Status: "crashed", Backend: "opencode", RetryCount: 2,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// AgentCreate's INSERT does not carry retry_count, so the struct field above
	// is dropped — write it the way the daemon does.
	if err := s.AgentUpdate("respawn-1", map[string]any{"retry_count": 2}); err != nil {
		t.Fatalf("set retry_count: %v", err)
	}
	crashed, err := s.AgentGet("respawn-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.respawnAgent(*crashed)

	agents, err := s.AgentList("respawn-proj")
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	var replacement *storage.Agent
	for i := range agents {
		if agents[i].ID != "respawn-1" {
			replacement = &agents[i]
		}
	}
	if replacement == nil {
		t.Fatal("respawnAgent created no replacement agent")
	}
	if replacement.RetryCount != 3 {
		t.Errorf("replacement retry_count = %d, want 3 (crashed row had 2) — the spawn response key was not read", replacement.RetryCount)
	}
}

// A crashed agent that is stopped during the respawn cooldown must stay
// stopped: the delayed respawn must not bring a replacement up anyway.
func TestRespawnAgent_SkipsAgentStoppedDuringCooldown(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		Status: "crashed", Backend: "claude",
	})
	crashed, _ := s.AgentGet("agent-1")

	if resp := h.handleStopAgent(map[string]any{"to": "agent-1"}); resp.Error != "" {
		t.Fatalf("stop: %s", resp.Error)
	}
	h.respawnAgent(*crashed)

	agents, _ := s.AgentList("proj")
	if len(agents) != 1 {
		t.Errorf("agents in project = %d, want 1 (no respawn after stop)", len(agents))
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

func TestHandleStopAllAgents_SequentialPause(t *testing.T) {
	requireProcTable(t)
	// Drei Agenten mit echten Prozessen: Der Stop-Run muss nacheinander
	// stopen (Pause zwischen den Exits), damit das Terminal jeden
	// Surface-Teardown einzeln verarbeitet statt als Massen-Teardown
	// (GTK/Mesa-Deadlock bei gleichzeitiger close-Welle).
	h, s := mustHandler(t)

	var cmds []*exec.Cmd
	for _, id := range []string{"seq-1", "seq-2", "seq-3"} {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		defer func() { _ = cmd.Process.Kill() }()
		cmds = append(cmds, cmd)
		// The sleeps stand in for agent processes: declare them opencode so the
		// C6 identity gate verifies them and the SIGTERM still happens.
		stubProcCmdline(t, agentProcCmdlineStub)
		s.AgentCreate(storage.Agent{
			ID: id, Project: "proj", Section: id, Status: "running",
			PID: cmd.Process.Pid, Backend: "opencode",
		})
	}

	start := time.Now()
	resp := h.handleStopAllAgents(map[string]any{"project": "proj"})
	elapsed := time.Since(start)
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	if want := 2 * stopAllPause; elapsed < want {
		t.Errorf("stop-all returned after %v; expected sequential pause of at least %v between stops", elapsed, want)
	}

	for i, cmd := range cmds {
		err := cmd.Wait()
		if err == nil {
			t.Fatalf("agent %d: process exited cleanly, want SIGTERM", i)
		}
		if !strings.Contains(err.Error(), "signal: terminated") {
			t.Fatalf("agent %d: exit = %v, want signal: terminated", i, err)
		}
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

// A paused row is a live process behind an open PTY bridge, not a dead one: the
// agent is waiting for input. resume_agent used to spawn a second process for the
// same session, and the row was rebound to it — so when the first process finally
// exited, its exit tail cleared the successor's pid, socket path and status and
// unlinked its sockets (YM-70). relay_agent is the way to reach a paused agent.
func TestHandleResumeAgent_PausedWithLiveProcessIsRefused(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	if err := s.AgentCreate(storage.Agent{
		ID: "paused-live", Project: "p", Section: "paused-live-sec",
		SessionID: "sess-paused-live", Status: "paused", Backend: "opencode",
		OpencodeSessionID: "ses_pausedlive",
		PID:               os.Getpid(), SockPath: filepath.Join(shortSockDir(t), "paused-live.sock"),
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "paused-live"})
	if resp.Error == "" {
		t.Fatal("a paused agent with a live process must not be resumed — that starts a second process")
	}
	if !strings.Contains(resp.Error, "relay_agent") {
		t.Errorf("the refusal must name the way to reach a paused agent, got: %s", resp.Error)
	}

	// The refusal must not have touched the row: the live process still owns it.
	a, err := s.AgentGet("paused-live")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "paused" || a.PID != os.Getpid() {
		t.Errorf("status=%q pid=%d, want paused/%d — a refused resume must leave the row alone", a.Status, a.PID, os.Getpid())
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

// TestHandleSpawnAgent_TrimsTheWorkDir: the bind compares the directory opencode
// reports byte for byte against the stored value, so a trailing slash or a
// redundant "." would hide the agent's own session from a late bind.
func TestHandleSpawnAgent_TrimsTheWorkDir(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "loop-wd2", "backend": "opencode",
		"work_dir": "/wd/loop-wd2//./",
	})
	if resp.Error != "" {
		t.Fatalf("spawn_agent: %s", resp.Error)
	}
	id, _ := resultMap(t, resp)["id"].(string)
	a, err := s.AgentGet(id)
	if err != nil || a == nil {
		t.Fatalf("AgentGet(%s): %v", id, err)
	}
	if a.WorkDir != "/wd/loop-wd2" {
		t.Errorf("WorkDir = %q, want /wd/loop-wd2", a.WorkDir)
	}
}

// TestHandleOpenAgentTerminal_PersistsWorkDir: a terminal agent's prompt is
// empty, so it can never match a session by marker — the persisted directory is
// the only evidence a later bind has for it.
func TestHandleOpenAgentTerminal_PersistsWorkDir(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.handleOpenAgentTerminal(map[string]any{
		"project": "proj", "backend": "opencode", "work_dir": "/wd/term",
	})
	if resp.Error != "" {
		t.Fatalf("open_agent_terminal: %s", resp.Error)
	}
	id, _ := resultMap(t, resp)["agent_id"].(string)
	if id == "" {
		t.Fatal("open_agent_terminal did not return an agent id")
	}
	a, err := s.AgentGet(id)
	if err != nil || a == nil {
		t.Fatalf("AgentGet(%s): %v", id, err)
	}
	if a.WorkDir != "/wd/term" {
		t.Errorf("WorkDir = %q, want /wd/term", a.WorkDir)
	}
}

// TestExistingWorkDir: the resume directory is the stored one only while it
// still exists — resuming into a removed worktree would fail the spawn, so the
// caller falls back to the project root instead.
func TestExistingWorkDir(t *testing.T) {
	h, _ := mustHandler(t)
	dir := t.TempDir()

	if got := h.existingWorkDir(dir); got != dir {
		t.Errorf("existingWorkDir(%q) = %q, want the directory", dir, got)
	}
	if got := h.existingWorkDir(""); got != "" {
		t.Errorf("existingWorkDir(\"\") = %q, want empty", got)
	}
	if got := h.existingWorkDir(dir + "/gone"); got != "" {
		t.Errorf("a missing directory must not be used, got %q", got)
	}
}

// TestMarkUnresumableIfUnbound: the spawn-time capture keeps polling for up to
// 150s, during which another path can bind the agent. A bound agent is
// resumable, so the marker must not be written onto it.
func TestMarkUnresumableIfUnbound(t *testing.T) {
	h, s := mustHandler(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-unbound", Project: "p", Section: "s1", SessionID: "u1",
		Status: "running", Backend: "opencode",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-bound", Project: "p", Section: "s2", SessionID: "u2",
		Status: "running", Backend: "opencode", OpencodeSessionID: "ses_have",
	}); err != nil {
		t.Fatal(err)
	}

	h.markUnresumableIfUnbound("agent-unbound")
	h.markUnresumableIfUnbound("agent-bound")
	h.markUnresumableIfUnbound("agent-missing")

	if a, _ := s.AgentGet("agent-unbound"); a.Progress != opencodeUnresumableMsg {
		t.Errorf("unbound agent progress = %q, want the unresumable marker", a.Progress)
	}
	if a, _ := s.AgentGet("agent-bound"); a.Progress != "" {
		t.Errorf("bound agent progress = %q, want it untouched", a.Progress)
	}
}

// TestOpenAgentTerminal_RefusesAnOwnedSession: adopting an existing session must
// not create a second owner. Two rows holding one session id resolve to an
// arbitrary one of them (whoami, relay, resume), and the real owner can never
// bind again once the store's guard sees the duplicate.
func TestOpenAgentTerminal_RefusesAnOwnedSession(t *testing.T) {
	h, s := mustHandler(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-owner", Project: "proj", Section: "term-1", SessionID: "uuid-1",
		Status: "running", Backend: "opencode", OpencodeSessionID: "ses_adopted",
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.handleOpenAgentTerminal(map[string]any{
		"project": "proj", "backend": "opencode", "work_dir": "/wd/adopt",
		"session_id": "ses_adopted",
	})
	if resp.Error == "" {
		t.Fatal("adopting a session that another agent owns must be refused")
	}
	if !strings.Contains(resp.Error, "already belongs to agent agent-owner") {
		t.Errorf("error = %q, want it to name the owner", resp.Error)
	}
}

// TestHandleResumeAgent_ExplicitSessionCannotBeAttachedToASecondRow: an
// explicitly passed session id is resolved through AgentGetAnyBySession, so the
// request lands on the row that already owns it. A second agent can therefore
// never end up holding another agent's session through the resume path.
// resume_agent resolved no placement source at all, so the window opened on the
// focused desktop instead of the caller's — spawn_agent has placed its window
// since Loop E. The caller's own window wins; a caller without one (the
// heartbeat, a rebind, an agent resuming another agent) falls back to the window
// of the session that spawned the resumed agent (YM-193).
func TestHandleResumeAgent_PlacesTheWindowLikeASpawn(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"the caller's own window wins", map[string]any{"_session_id": "opencode:ses-caller"}, "0x04c00005"},
		{"a caller without a window falls back to the agent's caller session", map[string]any{}, "0x04c00004"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := mustHandler(t)
			h.disableAgentProcesses = false
			h.windowMapMu.Lock()
			h.windowMap["opencode:ses-caller"] = "0x04c00005"
			h.windowMap["opencode:caller-193"] = "0x04c00004"
			h.windowMapMu.Unlock()

			trueBin, err := exec.LookPath("true")
			if err != nil {
				t.Skipf("true not available: %v", err)
			}
			placed := make(chan []string, 1)
			prevBridge, prevDisplay := newAgentBridge, agentDisplayCommand
			newAgentBridge = func(name string, args []string, sock, workDir string, extraEnv ...string) (*orchestrator.AgentBridge, error) {
				return orchestrator.NewAgentBridge(trueBin, nil, sock, workDir)
			}
			agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
				select {
				case placed <- innerArgs:
				default:
				}
				return trueBin, nil
			}
			t.Cleanup(func() { newAgentBridge, agentDisplayCommand = prevBridge, prevDisplay })

			if err := s.AgentCreate(storage.Agent{
				ID: "res-193", Project: "p-193", Section: "sec-193", SessionID: "sess-193",
				Status: "stopped", Backend: "opencode", OpencodeSessionID: "ses_193",
				CallerSession: "opencode:caller-193",
			}); err != nil {
				t.Fatalf("AgentCreate: %v", err)
			}

			params := map[string]any{"to": "res-193"}
			for k, v := range tc.params {
				params[k] = v
			}
			if resp := h.handleResumeAgent(params); resp.Error != "" {
				t.Fatalf("resume: %s", resp.Error)
			}

			var args []string
			select {
			case args = <-placed:
			case <-time.After(5 * time.Second):
				t.Fatal("the resumed agent's window was never opened")
			}
			got := ""
			for i, a := range args {
				if a == "--parent-window" && i+1 < len(args) {
					got = args[i+1]
				}
			}
			if got != tc.want {
				t.Errorf("--parent-window = %q, want %q (args: %v)", got, tc.want, args)
			}
		})
	}
}

func TestHandleResumeAgent_ExplicitSessionCannotBeAttachedToASecondRow(t *testing.T) {
	h, s := mustHandler(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-owner", Project: "proj", Section: "loop-a", SessionID: "uuid-owner",
		Status: "running", Backend: "opencode", OpencodeSessionID: "ses_taken",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentCreate(storage.Agent{
		ID: "agent-other", Project: "proj", Section: "loop-b", SessionID: "uuid-other",
		Status: "paused", Backend: "opencode",
	}); err != nil {
		t.Fatal(err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-other", "session_id": "ses_taken"})
	if !strings.Contains(resp.Error, "agent-owner is running") {
		t.Fatalf("the session must resolve to its owner, got error %q", resp.Error)
	}
	if other, _ := s.AgentGet("agent-other"); other.OpencodeSessionID != "" {
		t.Errorf("agent-other bound %q, want it untouched", other.OpencodeSessionID)
	}
	if owner, _ := s.AgentGet("agent-owner"); owner.OpencodeSessionID != "ses_taken" {
		t.Errorf("the owner's session changed to %q", owner.OpencodeSessionID)
	}
}

// --- L4: opencode unresumable marker (empty OpencodeSessionID) ---

// TestHandleSpawnAgent_PersistsWorkDir: the directory the backend starts in is
// the only evidence that later scopes an unbound agent's session search to its
// own tree. It has to survive the spawn on the row, because the capture runs in
// a goroutine that may outlive this request and the late bind runs hours later
// in the heartbeat.
func TestHandleSpawnAgent_PersistsWorkDir(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.handleSpawnAgent(map[string]any{
		"project": "proj", "section": "loop-wd", "backend": "opencode",
		"work_dir": "/wd/loop-wd",
	})
	if resp.Error != "" {
		t.Fatalf("spawn_agent: %s", resp.Error)
	}
	id, _ := resultMap(t, resp)["id"].(string)
	if id == "" {
		t.Fatal("spawn_agent did not return an agent id")
	}
	a, err := s.AgentGet(id)
	if err != nil || a == nil {
		t.Fatalf("AgentGet(%s): %v", id, err)
	}
	if a.WorkDir != "/wd/loop-wd" {
		t.Errorf("WorkDir = %q, want /wd/loop-wd", a.WorkDir)
	}
}

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

// TestHandleResumeAgent_BindsTheSessionOnDemand: an opencode agent whose session
// appeared after the spawn-time capture window (or whose capture the daemon
// restarted through) is resumable — rejecting it as "unresumable" throws away a
// recoverable loop. The resume path binds on demand before deciding.
func TestHandleResumeAgent_BindsTheSessionOnDemand(t *testing.T) {
	h, s := mustHandler(t)
	stubOpencodeFind(t, "ses_late")

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "loop-late",
		SessionID: "sess-1", Status: "paused", Backend: "opencode",
		// The row needs a work dir that exists: a recorded tree that is gone is
		// refused by the resume gate (YM-356), which would mask the bind this test
		// is about.
		WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1"})
	if resp.Error != "" {
		t.Fatalf("resume must bind the late session instead of rejecting the agent, got: %s", resp.Error)
	}
	a, _ := s.AgentGet("agent-1")
	if a.OpencodeSessionID != "ses_late" {
		t.Errorf("OpencodeSessionID = %q, want ses_late", a.OpencodeSessionID)
	}
}

// --- L4: resume by explicit session id (session_id/opencode/codex lookup + backfill) ---

// TestHandleResumeAgent_ResolveByOpenCodeSessionID: resume_agent accepts an
// opencode session id (ses_*) as the only target — no agent id needed — and
// resolves the agent via AgentGetAnyBySession.
func TestHandleResumeAgent_ResolveByOpenCodeSessionID(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "paused", Backend: "opencode",
		OpencodeSessionID: "ses_opencode_abc",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"session_id": "ses_opencode_abc"})
	if resp.Error != "" {
		t.Fatalf("resume by opencode session id should resolve the agent, got: %s", resp.Error)
	}
	agent, err := s.AgentGet("agent-1")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet failed: %v", err)
	}
	if agent.Status != "pending" {
		t.Errorf("expected agent status pending after resume, got %q", agent.Status)
	}
}

// TestHandleResumeAgent_BackfillsOpenCodeSessionID: an opencode agent whose
// OpencodeSessionID was never captured can still be resumed if the caller
// passes the ses_* id explicitly — it is persisted before spawning so
// spawnAgentProcess's -s picks it up instead of the daemon UUID.
func TestHandleResumeAgent_BackfillsOpenCodeSessionID(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "paused", Backend: "opencode",
		// OpencodeSessionID intentionally empty — simulates failed poll.
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1", "session_id": "ses_opencode_abc"})
	if resp.Error != "" {
		t.Fatalf("explicit session_id should unblock opencode resume, got: %s", resp.Error)
	}
	agent, err := s.AgentGet("agent-1")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet failed: %v", err)
	}
	if agent.OpencodeSessionID != "ses_opencode_abc" {
		t.Errorf("OpencodeSessionID should be backfilled, got %q", agent.OpencodeSessionID)
	}
}

// TestHandleResumeAgent_BackfillsCodexSessionID: same backfill for codex agents
// with an empty CodexSessionID — resume uses stored CodexSessionID over the
// workDir heuristic.
func TestHandleResumeAgent_BackfillsCodexSessionID(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "agent-1", Project: "proj", Section: "task",
		SessionID: "sess-1", Status: "stopped", Backend: "codex",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleResumeAgent(map[string]any{"to": "agent-1", "session_id": "sess-codex-123"})
	if resp.Error != "" {
		t.Fatalf("explicit session_id should unblock codex resume, got: %s", resp.Error)
	}
	agent, err := s.AgentGet("agent-1")
	if err != nil || agent == nil {
		t.Fatalf("AgentGet failed: %v", err)
	}
	if agent.CodexSessionID != "sess-codex-123" {
		t.Errorf("CodexSessionID should be backfilled, got %q", agent.CodexSessionID)
	}
}

// TestHandleResumeAgent_MissingTarget: neither to nor session_id → clear error.
func TestHandleResumeAgent_MissingTarget(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleResumeAgent(map[string]any{"project": "proj"})
	if resp.Error == "" {
		t.Fatal("expected error when neither to nor session_id is given")
	}
	if !strings.Contains(resp.Error, "to or session_id required") {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

// --- L4: bootstrap fresh agent rows from bare opencode session ids ---

// TestHandleResumeAgent_BootstrapFromBareOpenCodeSession: a ses_* id that
// matches no agent row but exists in opencode's own DB bootstraps a fresh
// agent whose workDir is the session directory.
func TestHandleResumeAgent_BootstrapFromBareOpenCodeSession(t *testing.T) {
	h, s := mustHandler(t)

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	if out, err := exec.Command("sqlite3", dbPath, "CREATE TABLE session (id TEXT, directory TEXT, time_created INTEGER); INSERT INTO session VALUES ('ses_fc1162f8affeJ7Omab43lZD4Ol', '/var/www/html/GreenWashProjekt/greenMarkting', 1);").CombinedOutput(); err != nil {
		t.Fatalf("create opencode.db: %v (%s)", err, out)
	}
	orig := opencodeDBPath
	opencodeDBPath = func() string { return dbPath }
	t.Cleanup(func() { opencodeDBPath = orig })

	resp := h.handleResumeAgent(map[string]any{"session_id": "ses_fc1162f8affeJ7Omab43lZD4Ol", "project": "proj"})
	if resp.Error != "" {
		t.Fatalf("bare opencode session should bootstrap an agent, got: %s", resp.Error)
	}

	var id, section, status, backend, ocSession string
	err := s.DB().QueryRow(`SELECT id, section, status, COALESCE(backend,''), COALESCE(opencode_session_id,'') FROM agents WHERE opencode_session_id = 'ses_fc1162f8affeJ7Omab43lZD4Ol'`).Scan(&id, &section, &status, &backend, &ocSession)
	if err != nil {
		t.Fatalf("bootstrapped agent row missing: %v", err)
	}
	if status != "pending" {
		t.Errorf("bootstrapped agent status should be pending, got %q", status)
	}
	if backend != "opencode" {
		t.Errorf("bootstrapped agent backend should be opencode, got %q", backend)
	}
	if !strings.HasPrefix(section, "resumed-") {
		t.Errorf("bootstrapped section should be prefixed resumed-, got %q", section)
	}
}

// TestHandleResumeAgent_BootstrapUnknownSessionRejected: a ses_* id unknown to
// both the agents table and opencode's DB yields the plain lookup error — no
// agent is created for garbage ids.
func TestHandleResumeAgent_BootstrapUnknownSessionRejected(t *testing.T) {
	h, s := mustHandler(t)

	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	if out, err := exec.Command("sqlite3", dbPath, "CREATE TABLE session (id TEXT, directory TEXT, time_created INTEGER);").CombinedOutput(); err != nil {
		t.Fatalf("create opencode.db: %v (%s)", err, out)
	}
	orig := opencodeDBPath
	opencodeDBPath = func() string { return dbPath }
	t.Cleanup(func() { opencodeDBPath = orig })

	resp := h.handleResumeAgent(map[string]any{"session_id": "ses_unknown12345678"})
	if resp.Error == "" {
		t.Fatal("expected error for unknown session id")
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n)
	if n != 0 {
		t.Errorf("no agent row should be created for unknown sessions, got %d", n)
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
		"", "/nonexistent/agent-1.sock", t.TempDir(), "opencode", "", "", 0, 0, "", true)

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
	h, _ := mustHandler(t)
	// Fork columns come from the token_usage CREATE constant; do not add them here.

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
		Params: map[string]any{"project": "proj", "section": "dispatch-test", "work_dir": t.TempDir()},
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

// A thread the daemon cannot identify must not be attached to whichever agent
// happens to run in the same project: that guess bound the orchestrator's own
// thread to a freshly spawned loop agent (2026-10-15) and fed it foreign
// telemetry, which in turn produced false DEAD_AGENT alarms.
func TestHandleTrackUsage_UnknownThreadIsNotGuessed(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-unmapped", Project: "proj", Section: "task",
		SessionID: "daemon-uuid-unmapped", Status: "running", Backend: "opencode",
	})

	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "opencode:ses_someone_else",
		"project":       "proj",
		"input_tokens":  float64(200),
		"output_tokens": float64(80),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-unmapped")
	if agent.TurnsUsed != 0 {
		t.Errorf("TurnsUsed = %d, want 0 (foreign thread must not count for this agent)", agent.TurnsUsed)
	}
	if agent.ProxyThreadID != "" {
		t.Errorf("ProxyThreadID = %q, want empty (no guessing)", agent.ProxyThreadID)
	}
}

// Once the backend id is captured, the agent's own thread is found directly.
func TestHandleTrackUsage_OpencodeBackendIDMatch(t *testing.T) {
	h, s := mustHandler(t)

	s.AgentCreate(storage.Agent{
		ID: "agent-oc", Project: "proj", Section: "task",
		SessionID: "daemon-uuid-oc", Status: "running", Backend: "opencode",
		OpencodeSessionID: "ses_own",
	})

	resp := h.handleTrackUsage(map[string]any{
		"thread_id":     "opencode:ses_own",
		"project":       "proj",
		"input_tokens":  float64(200),
		"output_tokens": float64(80),
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	agent, _ := s.AgentGet("agent-oc")
	if agent.TurnsUsed != 1 {
		t.Errorf("TurnsUsed = %d, want 1 (own backend thread)", agent.TurnsUsed)
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
	isolateHomeAt(t, homeDir)

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
	isolateHomeAt(t, homeDir)

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
	isolateHomeAt(t, homeDir)

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
	isolateHomeAt(t, homeDir)

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
	isolateHomeAt(t, homeDir)

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
	sockPath := los24dAgentSock(t, h, "relay-paused")
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
	sockPath := los24dAgentSock(t, h, "relay-running")
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
		"work_dir":   "/home/tester/projects/deepseek",
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
		"work_dir": "/home/tester/projects/greenwebsite",
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

// --- review fixes: the neighbouring paths of E2/E3 ---

// The crash replacement must come back where the crashed agent ran — the same
// defect as E2, one path over: spawn_agent without a work_dir resolves the project
// root, so a loop agent's replacement starts outside its worktree and its own
// skill guard fails there.
func TestRespawnAgent_UsesTheRecordedWorkDir(t *testing.T) {
	h, s := mustHandler(t)
	h.disableAgentProcesses = true
	workDir := t.TempDir()
	if err := s.AgentCreate(storage.Agent{
		ID: "crash-wd", Project: "p", Section: "s-crash-wd", SessionID: "sess-crash-wd",
		Status: "crashed", Backend: "claude", WorkDir: workDir,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	crashed, err := s.AgentGet("crash-wd")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.respawnAgent(*crashed)

	agents, err := s.AgentList("p")
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	var replacement *storage.Agent
	for i := range agents {
		if agents[i].ID != "crash-wd" {
			replacement = &agents[i]
		}
	}
	if replacement == nil {
		t.Fatal("respawnAgent created no replacement row")
	}
	if replacement.WorkDir != workDir {
		t.Errorf("replacement work_dir = %q, want the crashed agent's %q", replacement.WorkDir, workDir)
	}
}

// The "pending" twin of the row a dead daemon leaves behind: a spawn writes
// "pending" before it binds a process, and if the daemon dies in that window no
// guard touches the row (they all want running/spawning), resume refuses it
// (stopped/paused/finished only) and agentGetActiveBySection still counts it as
// the section's owner. stop_agent is the only way out — without signalling the
// stale PID the row carries.
func TestHandleStopAgent_PendingRowLeftByADeadDaemonIsClosable(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "pending-left", Project: "proj", Section: "yesloop-pending-left",
		Status: "pending", Backend: "claude", PID: bystander.Process.Pid,
		SockPath: filepath.Join(shortSockDir(t), "pending-left.sock"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	resp := h.handleStopAgent(map[string]any{"to": "pending-left"})
	if resp.Error != "" {
		t.Fatalf("a pending row must be closable by hand: %s", resp.Error)
	}
	a, err := s.AgentGet("pending-left")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
	select {
	case <-exited:
		t.Error("stop signalled the stale PID of a pending row")
	case <-time.After(300 * time.Millisecond):
	}
}

// The mirror of TestRestartWindow_StopIsNotOverwrittenByTheBind, at the level where
// the race is actually decided: the relaunch bound a process after the stop had read
// the row, so the stop's write is refused and the process the bind installed has to
// be ended — through the same identity gate as every other signal.
func TestFinalizeStop_SignalsTheProcessThatDisplacedTheSnapshot(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")
	bystander, exited := startBystander(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "stop-mirror", Project: "p", Section: "s-mirror", Status: "spawning",
		Backend: "claude", PID: bystander.Process.Pid,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// What the stop read before the bind landed: the row still carried its stale PID.
	snapshot := storage.Agent{ID: "stop-mirror", Status: "spawning", PID: deadPID}

	written, err := h.finalizeStop(snapshot, "stopped")
	if err != nil {
		t.Fatalf("finalizeStop: %v", err)
	}
	if !written {
		t.Fatal("the stop was not finalized on the PID that displaced the snapshot")
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Error("the process the bind installed was not signalled — it keeps working on a section everybody believes is stopped")
	}
	a, err := s.AgentGet("stop-mirror")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
}

// The counter-direction: when the snapshot still matches the row, the write lands
// immediately and nothing is signalled — finalizeStop only ends a process that
// displaced the decision it was called with.
func TestFinalizeStop_LeavesAMatchingPIDToTheCaller(t *testing.T) {
	h, s := mustHandler(t)
	pinProcessTable(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")
	bystander, exited := startBystander(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "stop-match", Project: "p", Section: "s-match", Status: "running",
		Backend: "claude", PID: bystander.Process.Pid,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	stored, err := s.AgentGet("stop-match")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	written, err := h.finalizeStop(*stored, "stopped")
	if err != nil {
		t.Fatalf("finalizeStop: %v", err)
	}
	if !written {
		t.Fatal("a snapshot that matches the row must be finalized in the first attempt")
	}
	select {
	case <-exited:
		t.Error("finalizeStop signalled a PID the caller had already decided on")
	case <-time.After(300 * time.Millisecond):
	}
	if a, _ := s.AgentGet("stop-match"); a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
}

// --- YM-225: a relay lifts the idle-guard pause ---
//
// The idle guard pauses an agent as DEAD_AGENT, the orchestrator relays
// approval, the agent works on — and the row stayed `paused`, where every guard
// skips it (checkOneAgent wants running). Only the permission kick ever
// unpaused. A relay is the orchestrator's answer to exactly this pause, so it
// puts the row back under supervision.
func TestRelayAgent_LiftsTheIdleGuardPause(t *testing.T) {
	h, s := mustHandler(t)
	sockPath := los24dAgentSock(t, h, "idle-paused")
	ln, err := net.Listen("unix", sockPath+".inject")
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

	if err := s.AgentCreate(storage.Agent{
		ID: "idle-paused", Project: "p", Section: "yesloop-idle-paused",
		Status: "paused", Backend: "claude", SockPath: sockPath,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdate("idle-paused", map[string]any{
		"progress": "paused: yesloop-idle escalation: no PROVEN marker in scratchpad (refireCount=3, max=2)",
	}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}
	// The escalation left the state machine at DONE. That rung ignores the agent
	// until its stream becomes active again, so a relay has to drop it.
	resetIdleState()
	t.Cleanup(resetIdleState)
	yesloopIdleAgentsMu.Lock()
	yesloopIdleAgents["idle-paused"] = &yesloopIdleState{state: yesloopIdleStateDone, refireCount: 3}
	yesloopIdleAgentsMu.Unlock()

	if resp := h.handleRelayAgent(map[string]any{
		"to": "idle-paused", "content": "approved, continue with Phase 4",
	}); resp.Error != "" {
		t.Fatalf("relay to the idle-paused agent failed: %s", resp.Error)
	}

	row, err := s.AgentGet("idle-paused")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("a relay must lift the idle-guard pause, status = %q", row.Status)
	}
	yesloopIdleAgentsMu.Lock()
	_, tracked := yesloopIdleAgents["idle-paused"]
	yesloopIdleAgentsMu.Unlock()
	if tracked {
		t.Error("the answered escalation must not stay at DONE, where the guard ignores the agent")
	}
}

// The three other guards stopped pausing, but rows they paused before the change
// are still in the database. A relay has to release those too: no layer watches a
// paused row, so without this they would stay paused forever.
func TestRelayAgent_ReleasesTheRetiredGuardPauses(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)

	cases := []struct{ id, progress string }{
		{"retired-idle", "paused: yesloop-idle escalation: no PROVEN marker in scratchpad"},
		{"retired-stag", "paused: yesloop-stagnation escalation: no progress for 3h0m0s after 4 relays"},
		{"retired-dv", "paused: yesloop-done-verify escalation: no BEWEISEN/send_to/7-phases evidence"},
		{"retired-bt", "paused: yesloop-broken-toolcall escalation: empty on tool \"bash\" unanswered after 3 delivered relays"},
	}
	// One socket directory for all four: the helper sets h.dataDir, and the path
	// each row carries has to live under the directory the handler derives.
	dir := filepath.Dir(los24dAgentSock(t, h, cases[0].id))
	for _, c := range cases {
		sockPath := filepath.Join(dir, c.id+".sock")
		ln, err := net.Listen("unix", sockPath+".inject")
		if err != nil {
			t.Fatalf("listen inject for %s: %v", c.id, err)
		}
		defer ln.Close()
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				io.Copy(io.Discard, conn)
				conn.Close()
			}
		}()
		if err := s.AgentCreate(storage.Agent{
			ID: c.id, Project: "p", Section: "yesloop-" + c.id,
			Status: "paused", Backend: "claude", SockPath: sockPath,
		}); err != nil {
			t.Fatalf("AgentCreate: %v", err)
		}
		if err := s.AgentUpdate(c.id, map[string]any{"progress": c.progress}); err != nil {
			t.Fatalf("AgentUpdate: %v", err)
		}
	}

	for _, c := range cases {
		if resp := h.handleRelayAgent(map[string]any{
			"to": c.id, "content": "approved, continue",
		}); resp.Error != "" {
			t.Fatalf("relay to %s failed: %s", c.id, resp.Error)
		}
		row, err := s.AgentGet(c.id)
		if err != nil {
			t.Fatalf("AgentGet(%s): %v", c.id, err)
		}
		if row.Status != "running" {
			t.Errorf("a relay must release the retired pause of %s, status = %q", c.id, row.Status)
		}
	}
}

// Any other pause keeps its status: the DONE-guard lifts its own pause when the
// scratchpad becomes compliant, and a relay must not take that decision away.
func TestRelayAgent_LeavesForeignPausesPaused(t *testing.T) {
	h, s := mustHandler(t)
	sockPath := los24dAgentSock(t, h, "doneguard-paused")
	ln, err := net.Listen("unix", sockPath+".inject")
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

	if err := s.AgentCreate(storage.Agent{
		ID: "doneguard-paused", Project: "p", Section: "yesloop-doneguard-paused",
		Status: "paused", Backend: "claude", SockPath: sockPath,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdate("doneguard-paused", map[string]any{
		"progress": "paused: DONE-GUARD: phase validation failed — Phase 5 cold-review missing",
	}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	if resp := h.handleRelayAgent(map[string]any{
		"to": "doneguard-paused", "content": "please finish Phase 5",
	}); resp.Error != "" {
		t.Fatalf("relay to the paused agent failed: %s", resp.Error)
	}

	row, err := s.AgentGet("doneguard-paused")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "paused" {
		t.Errorf("a DONE-GUARD pause is lifted by its own recovery, not by a relay: status = %q", row.Status)
	}
}
