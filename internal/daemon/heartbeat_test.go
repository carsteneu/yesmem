package daemon

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestEnforceAgentLimits_TokenBudgetFreezes(t *testing.T) {
	h, s := mustHandler(t)

	_ = s.AgentCreate(storage.Agent{
		ID:          "agent-budget",
		Project:     "test",
		Section:     "work",
		Status:      "running",
		TokenBudget: 1000,
		CreatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	})
	// Push over budget: 1100 output tokens > 1000 (input is not the unit).
	_ = s.AgentUpdateTelemetry("agent-budget", 1, 600, 1100)

	h.enforceAgentLimits()

	a, err := s.AgentGet("agent-budget")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" {
		t.Errorf("status=%q want stopped", a.Status)
	}
}

func TestEnforceAgentLimits_TokenBudgetUnderLimitNotFrozen(t *testing.T) {
	h, s := mustHandler(t)

	_ = s.AgentCreate(storage.Agent{
		ID:          "agent-under",
		Project:     "test",
		Section:     "work2",
		Status:      "running",
		TokenBudget: 1000,
		CreatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	})
	// Under budget: 200 output tokens < 1000 (the 300 input tokens are not the unit)
	_ = s.AgentUpdateTelemetry("agent-under", 1, 300, 200)

	h.enforceAgentLimits()

	a, err := s.AgentGet("agent-under")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "running" {
		t.Errorf("status=%q want running", a.Status)
	}
}

func TestCrashRecovery_DeadPID_MarksFailed(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	// PID 99999999 almost certainly doesn't exist — retries exhausted → status=failed
	s.AgentCreate(storage.Agent{
		ID: "dead-pid-0", Project: "p", Section: "s",
		Status: "running", PID: 99999999, CreatedAt: now,
	})
	s.AgentUpdate("dead-pid-0", map[string]any{"retry_count": 3})

	h.crashRecovery()

	a, _ := s.AgentGet("dead-pid-0")
	if a.Status != "failed" {
		t.Errorf("status=%q want failed (PID 99999999 should be dead, retries exhausted)", a.Status)
	}
}

func TestCrashRecovery_AlivePID_Unchanged(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "live-pid-0", Project: "p", Section: "s2",
		Status: "running", PID: os.Getpid(), Backend: "opencode", CreatedAt: now,
	})

	h.crashRecovery()

	a, _ := s.AgentGet("live-pid-0")
	if a.Status != "running" {
		t.Errorf("status=%q want running (own PID is alive)", a.Status)
	}
}

func TestDetectOrphanedAgents_SetsLivenessPing(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	// Parent: stopped
	s.AgentCreate(storage.Agent{
		ID: "dead-parent-0", SessionID: "sess-dead-0",
		Project: "p", Section: "parent", Status: "stopped", CreatedAt: now,
	})
	// Child: running, parent is dead
	s.AgentCreate(storage.Agent{
		ID: "orphan-0", SessionID: "sess-orphan-0",
		Project: "p", Section: "child", Status: "running",
		CallerSession: "sess-dead-0", CreatedAt: now,
	})

	h.detectOrphanedAgents()

	child, _ := s.AgentGet("orphan-0")
	if child.LivenessPingAt == "" {
		t.Error("expected liveness_ping_at to be set after orphan detection")
	}
	// Should still be running (grace not expired yet)
	if child.Status != "running" {
		t.Errorf("child status=%q want still running during grace period", child.Status)
	}
}

func TestDetectOrphanedAgents_StopsAfterGrace(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	past := time.Now().Add(-6 * time.Minute).Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "dead-parent-1", SessionID: "sess-dead-1",
		Project: "p", Section: "parent", Status: "stopped", CreatedAt: now,
	})
	s.AgentCreate(storage.Agent{
		ID: "orphan-1", SessionID: "sess-orphan-1",
		Project: "p", Section: "child", Status: "running",
		CallerSession: "sess-dead-1", CreatedAt: now,
		LivenessPingAt: past, // grace started 6 min ago
	})

	h.detectOrphanedAgents()

	child, _ := s.AgentGet("orphan-1")
	if child.Status != "stopped" {
		t.Errorf("child status=%q want stopped after grace expired", child.Status)
	}
}

func TestAttemptRestart_SkipsTemporary(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "temp-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "temporary", CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("temp-0")
	if a.Status != "error" {
		t.Errorf("temporary agent should not be restarted, status=%q", a.Status)
	}
}

func TestAttemptRestart_FreezesOnMaxRestarts(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "max-hit-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "transient", RestartCount: 3, MaxRestarts: 3,
		CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("max-hit-0")
	if a.Status != "stopped" {
		t.Errorf("agent at max_restarts should be stopped, status=%q", a.Status)
	}
}

func TestAttemptRestart_RaceGuard(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	recentRestart := time.Now().Add(-10 * time.Second).Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "race-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "transient", MaxRestarts: 3,
		LastRestartAt: recentRestart, // 10s ago — within 30s guard
		CreatedAt:     now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("race-0")
	// Should not attempt restart (no status change, no freeze)
	if a.Status != "error" {
		t.Errorf("race guard should prevent restart, status=%q", a.Status)
	}
}

// YM-338 (Los 24d): the restart no longer reads agents.sock_path — it derives the
// path from the id. What blocks a restart is an id/dir the derivation cannot use;
// the row is closed instead of retried on every tick.
func TestAttemptRestart_StopsWhenNoSocketPathCanBeDerived(t *testing.T) {
	h, s := mustHandler(t)
	h.dataDir = "" // nothing to derive a path from
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "no-sock-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "transient", MaxRestarts: 3,
		CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("no-sock-0")
	if a.Status != "stopped" {
		t.Errorf("agent with no derivable socket path should be stopped, status=%q", a.Status)
	}
}

func TestAttemptRestart_PermanentIgnoresMaxRestarts(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "perm-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "permanent", RestartCount: 3, MaxRestarts: 3,
		SockPath:  "/tmp/perm-test-sock",
		CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("perm-0")
	if a.Status == "stopped" {
		t.Errorf("permanent agent should never be stopped, got stopped after max_restarts")
	}
}

func TestDetectOrphanedAgents_ClearsLivenessPingWhenParentRevives(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "revived-parent", SessionID: "sess-revived",
		Project: "p", Section: "parent", Status: "stopped", CreatedAt: now,
	})
	s.AgentCreate(storage.Agent{
		ID: "child-revive", SessionID: "sess-child-revive",
		Project: "p", Section: "child", Status: "running",
		CallerSession: "sess-revived", LivenessPingAt: now, CreatedAt: now,
	})

	// Verify ping is present initially when parent is stopped
	h.detectOrphanedAgents()
	child, _ := s.AgentGet("child-revive")
	if child.LivenessPingAt == "" {
		t.Fatal("liveness_ping_at should remain set when parent still stopped")
	}

	// Revive parent
	s.AgentUpdate("revived-parent", map[string]any{"status": "running"})

	// Now orphan detection should clear the liveness ping
	h.detectOrphanedAgents()
	child, _ = s.AgentGet("child-revive")
	if child.LivenessPingAt != "" {
		t.Errorf("liveness_ping_at should be cleared when parent revives, got %q", child.LivenessPingAt)
	}
}

func TestCrashRecovery_DeadPID_QuarantinesAndRetries(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "crash-dead-0", Project: "p", Section: "s",
		Status: "running", PID: 999999, CreatedAt: now,
	})
	s.AgentUpdate("crash-dead-0", map[string]any{"retry_count": 2})

	h.crashRecovery()
	time.Sleep(50 * time.Millisecond)

	a, err := s.AgentGet("crash-dead-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "crashed" {
		t.Errorf("status=%q want crashed", a.Status)
	}
	if a.RetryCount != 3 {
		t.Errorf("retry_count=%d want 3", a.RetryCount)
	}
}

func TestCrashRecovery_DeadPID_MaxRetriesExhausted(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "crash-exhaust-0", Project: "p", Section: "s",
		Status: "running", PID: 999999, CreatedAt: now,
	})
	s.AgentUpdate("crash-exhaust-0", map[string]any{"retry_count": 3})

	h.crashRecovery()

	a, err := s.AgentGet("crash-exhaust-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "failed" {
		t.Errorf("status=%q want failed", a.Status)
	}
}

func TestCrashRecovery_AlivePID_NoChange(t *testing.T) {
	h, s := mustHandler(t)
	// "own PID is alive" now means the PID verifies as this agent's process.
	stubProcCmdline(t, agentProcCmdlineStub)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "crash-alive-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), Backend: "opencode", CreatedAt: now,
	})

	h.crashRecovery()

	a, err := s.AgentGet("crash-alive-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "running" {
		t.Errorf("status=%q want running (own PID is alive)", a.Status)
	}
}

func TestCrashRecovery_SkipsNonRunning(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "crash-stopped-0", Project: "p", Section: "s",
		Status: "stopped", PID: 999999, CreatedAt: now,
	})

	h.crashRecovery()

	a, err := s.AgentGet("crash-stopped-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "stopped" {
		t.Errorf("status=%q want stopped (non-running skipped)", a.Status)
	}
}

func TestSupervisionPipelineE2E(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	// The children are live agents with real processes: crash recovery treats a
	// running row without a bound process as a crash of its own, and the orphan
	// stop ends the verified process — with the test binary's own PID the fixture
	// would kill the test.
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	childProc1, _ := startBystander(t)
	childProc2, _ := startBystander(t)

	// Setup: orchestrator with dead PID (retries exhausted) + 2 children
	s.AgentCreate(storage.Agent{
		ID: "e2e-orch", SessionID: "sess-e2e-orch",
		Project: "p", Section: "orch",
		Status: "running", PID: 99999999, CreatedAt: now,
	})
	s.AgentUpdate("e2e-orch", map[string]any{"retry_count": 3})
	s.AgentCreate(storage.Agent{
		ID: "e2e-child-1", SessionID: "sess-e2e-c1",
		Project: "p", Section: "c1",
		Status: "running", PID: childProc1.Process.Pid, Backend: "opencode",
		CallerSession: "sess-e2e-orch", CreatedAt: now,
	})
	s.AgentCreate(storage.Agent{
		ID: "e2e-child-2", SessionID: "sess-e2e-c2",
		Project: "p", Section: "c2",
		Status: "running", PID: childProc2.Process.Pid, Backend: "opencode",
		CallerSession: "sess-e2e-orch", CreatedAt: now,
	})

	// Step 1: crashRecovery marks orchestrator as failed (dead PID, retries exhausted)
	h.crashRecovery()
	orch, _ := s.AgentGet("e2e-orch")
	if orch.Status != "failed" {
		t.Fatalf("step1: orchestrator status=%q want failed", orch.Status)
	}

	// Step 2: detectOrphanedAgents starts grace period for children
	h.detectOrphanedAgents()
	c1, _ := s.AgentGet("e2e-child-1")
	c2, _ := s.AgentGet("e2e-child-2")
	if c1.LivenessPingAt == "" {
		t.Fatal("step2: child-1 liveness_ping_at not set")
	}
	if c2.LivenessPingAt == "" {
		t.Fatal("step2: child-2 liveness_ping_at not set")
	}
	if c1.Status != "running" || c2.Status != "running" {
		t.Fatal("step2: children should still be running during grace period")
	}

	// Step 3: simulate grace period expired (6 min ago)
	past := time.Now().Add(-6 * time.Minute).Format("2006-01-02 15:04:05")
	s.AgentUpdate("e2e-child-1", map[string]any{"liveness_ping_at": past})
	s.AgentUpdate("e2e-child-2", map[string]any{"liveness_ping_at": past})

	// Step 4: detectOrphanedAgents now stops both children
	h.detectOrphanedAgents()
	c1, _ = s.AgentGet("e2e-child-1")
	c2, _ = s.AgentGet("e2e-child-2")
	if c1.Status != "stopped" {
		t.Errorf("step4: child-1 status=%q want stopped", c1.Status)
	}
	if c2.Status != "stopped" {
		t.Errorf("step4: child-2 status=%q want stopped", c2.Status)
	}
}

// The deleted hung-agent guard used to stop stale-heartbeat rows whose process
// was gone. Those rows now go through crash recovery, which must still leave a
// row alone while its own process is alive: a long tool call blocks the
// heartbeat while the agent keeps working (#73191).
func TestCrashRecovery_StaleHeartbeatWithLiveProcess_LeftAlone(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	if err := s.AgentCreate(storage.Agent{
		ID: "stale-live", Project: "p", Section: "s-stale-live",
		Status: "running", PID: os.Getpid(), Backend: "opencode",
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	setAgentStamp(t, s, "stale-live", "heartbeat_at", stampAgo(15*time.Minute))

	h.crashRecovery()

	if a := reapedAgent(t, s, "stale-live"); a.Status != "running" {
		t.Errorf("status = %q, want running — a stale heartbeat alone is not a death", a.Status)
	}
}

// The mirror direction on a platform without a readable process table: identity
// cannot be established, so "not verified" must not be read as "gone".
func TestCrashRecovery_StaleHeartbeatWithoutProcessTable_LeftAlone(t *testing.T) {
	h, s := mustHandler(t)
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })

	if err := s.AgentCreate(storage.Agent{
		ID: "stale-no-proc", Project: "p", Section: "s-stale-no-proc",
		Status: "running", PID: os.Getpid(), Backend: "opencode",
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	setAgentStamp(t, s, "stale-no-proc", "heartbeat_at", stampAgo(15*time.Minute))

	h.crashRecovery()

	if a := reapedAgent(t, s, "stale-no-proc"); a.Status != "running" {
		t.Errorf("status = %q, want running — an unverifiable PID is not a proven death", a.Status)
	}
}

func TestHandleUpdateAgentStatus_WritesHeartbeatAt(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "hb-update-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), SessionID: "sess-hb-0", CreatedAt: now,
	})

	before := time.Now().Add(-1 * time.Second)
	resp := h.Handle(Request{
		Method: "update_agent_status",
		Params: map[string]any{"id": "hb-update-0", "phase": "implementing"},
	})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}

	a, _ := s.AgentGet("hb-update-0")
	if a.HeartbeatAt == "" {
		t.Fatal("heartbeat_at not set by update_agent_status")
	}
	hbTime, err := time.Parse(time.RFC3339, a.HeartbeatAt)
	if err != nil {
		t.Fatalf("heartbeat_at not valid RFC3339: %s", a.HeartbeatAt)
	}
	if hbTime.Before(before) {
		t.Errorf("heartbeat_at too old: %v", hbTime)
	}
}

// crashRuntime uses time.Parse (returns UTC), so tests must format in UTC.
// We allow +/- 2s tolerance for test execution time.

func TestCrashRuntime_Seconds(t *testing.T) {
	ts := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05")
	got := crashRuntime(ts)
	if got != "30s" && got != "31s" {
		t.Errorf("crashRuntime(%q) = %q, want ~30s", ts, got)
	}
}

func TestCrashRuntime_MinutesAndSeconds(t *testing.T) {
	ts := time.Now().UTC().Add(-5*time.Minute - 10*time.Second).Format("2006-01-02 15:04:05")
	got := crashRuntime(ts)
	if got != "5m10s" && got != "5m11s" {
		t.Errorf("crashRuntime(%q) = %q, want ~5m10s", ts, got)
	}
}

func TestCrashRuntime_RFC3339Fallback(t *testing.T) {
	ts := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	got := crashRuntime(ts)
	if got != "2m0s" && got != "2m1s" {
		t.Errorf("crashRuntime(RFC3339) = %q, want ~2m0s", got)
	}
}

func TestCrashRuntime_InvalidFormat(t *testing.T) {
	got := crashRuntime("not-a-timestamp")
	if got != "unbekannt" {
		t.Errorf("crashRuntime(invalid) = %q, want unbekannt", got)
	}
}

func TestCrashRuntime_EmptyString(t *testing.T) {
	got := crashRuntime("")
	if got != "unbekannt" {
		t.Errorf("crashRuntime(\"\") = %q, want unbekannt", got)
	}
}

func TestCrashRuntime_ExactlyOneMinute(t *testing.T) {
	ts := time.Now().UTC().Add(-60 * time.Second).Format("2006-01-02 15:04:05")
	got := crashRuntime(ts)
	if got != "1m0s" && got != "1m1s" {
		t.Errorf("crashRuntime(60s) = %q, want ~1m0s", got)
	}
}

func TestCrashRuntime_LargeRuntime(t *testing.T) {
	ts := time.Now().UTC().Add(-2*time.Hour - 15*time.Minute).Format("2006-01-02 15:04:05")
	got := crashRuntime(ts)
	if got != "135m0s" && got != "135m1s" {
		t.Errorf("crashRuntime(2h15m) = %q, want ~135m0s", got)
	}
}

func TestGetAgentMaxRuntime_Default(t *testing.T) {
	h, _ := mustHandler(t)
	got := h.getAgentMaxRuntime()
	if got != 48*time.Hour {
		t.Errorf("getAgentMaxRuntime() = %v, want 48h", got)
	}
}

func TestGetAgentMaxRuntime_Configured(t *testing.T) {
	h, _ := mustHandler(t)
	h.agentMaxRuntime = 45 * time.Minute
	got := h.getAgentMaxRuntime()
	if got != 45*time.Minute {
		t.Errorf("getAgentMaxRuntime() = %v, want 45m", got)
	}
}

func TestGetAgentMaxRuntime_ZeroMeansDefault(t *testing.T) {
	h, _ := mustHandler(t)
	h.agentMaxRuntime = 0
	got := h.getAgentMaxRuntime()
	if got != 48*time.Hour {
		t.Errorf("getAgentMaxRuntime(0) = %v, want 48h default", got)
	}
}

func TestGetAgentMaxRuntime_NegativeMeansDefault(t *testing.T) {
	h, _ := mustHandler(t)
	h.agentMaxRuntime = -5 * time.Minute
	got := h.getAgentMaxRuntime()
	if got != 48*time.Hour {
		t.Errorf("getAgentMaxRuntime(negative) = %v, want 48h default", got)
	}
}

func TestIsPIDAlive_OwnPID(t *testing.T) {
	if !isPIDAlive(os.Getpid()) {
		t.Error("isPIDAlive(self) = false, want true")
	}
}

func TestIsPIDAlive_DeadPID(t *testing.T) {
	if isPIDAlive(99999999) {
		t.Error("isPIDAlive(99999999) = true, want false")
	}
}

func TestEnforceAgentLimits_MaxRuntime_AlivePID_NotFrozen(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	// Created 50h ago — past the 48h default max_runtime. The spawn marker is
	// refreshed with the pid (bindAgentProcess): without it the identity check
	// reads the row's own live process as a recycled PID, because the process
	// started long after the row.
	old := time.Now().Add(-50 * time.Hour).Format("2006-01-02 15:04:05")
	_ = s.AgentCreate(storage.Agent{
		ID:            "alive-over-runtime",
		Project:       "p",
		Section:       "s",
		Status:        "running",
		PID:           os.Getpid(), // the agent's own verified process
		Backend:       "opencode",
		CreatedAt:     old,
		LastRestartAt: time.Now().Format("2006-01-02 15:04:05"),
	})

	h.enforceAgentLimits()

	a, _ := s.AgentGet("alive-over-runtime")
	if a.Status != "running" {
		t.Errorf("status=%q want running (PID alive — should not freeze on max_runtime)", a.Status)
	}
}

// A recycled PID must not keep an over-budget agent running. Signal 0 answers
// "somebody holds this number"; when that somebody is not the agent's own
// process the row is dead and the stop has to fire (Learning #101907).
func TestEnforceAgentLimits_MaxRuntime_RecycledPID_Frozen(t *testing.T) {
	h, s := mustHandler(t)
	pinProcessTable(t)
	bystander, _ := startBystander(t)
	old := time.Now().Add(-50 * time.Hour).Format("2006-01-02 15:04:05")
	_ = s.AgentCreate(storage.Agent{
		ID:        "recycled-over-runtime",
		Project:   "p",
		Section:   "s-recycled",
		Status:    "running",
		PID:       bystander.Process.Pid,
		Backend:   "claude",
		CreatedAt: old,
	})

	h.enforceAgentLimits()

	a, _ := s.AgentGet("recycled-over-runtime")
	if a.Status != "stopped" {
		t.Errorf("status=%q want stopped — the PID belongs to another process, this agent is gone", a.Status)
	}
}

// The mirror direction where the process table is unreadable: identity cannot be
// established, so the agent must not be stopped on a guess.
func TestEnforceAgentLimits_MaxRuntime_WithoutProcessTable_KeepsAgent(t *testing.T) {
	h, s := mustHandler(t)
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })
	old := time.Now().Add(-50 * time.Hour).Format("2006-01-02 15:04:05")
	_ = s.AgentCreate(storage.Agent{
		ID:        "unverifiable-over-runtime",
		Project:   "p",
		Section:   "s-noproc",
		Status:    "running",
		PID:       os.Getpid(),
		Backend:   "opencode",
		CreatedAt: old,
	})

	h.enforceAgentLimits()

	a, _ := s.AgentGet("unverifiable-over-runtime")
	if a.Status != "running" {
		t.Errorf("status=%q want running — an unverifiable PID is not a proven death", a.Status)
	}
}

func TestEnforceAgentLimits_MaxRuntime_DeadPID_Frozen(t *testing.T) {
	h, s := mustHandler(t)
	old := time.Now().Add(-50 * time.Hour).Format("2006-01-02 15:04:05")
	_ = s.AgentCreate(storage.Agent{
		ID:        "dead-over-runtime",
		Project:   "p",
		Section:   "s2",
		Status:    "running",
		PID:       99999999, // dead
		CreatedAt: old,
	})

	h.enforceAgentLimits()

	a, _ := s.AgentGet("dead-over-runtime")
	if a.Status != "stopped" {
		t.Errorf("status=%q want stopped (PID dead — max_runtime stop should fire)", a.Status)
	}
}

// AgentCreate writes created_at as RFC3339 when the caller leaves it empty —
// that is the production format, and max_runtime must still fire on it.
func TestEnforceAgentLimits_MaxRuntime_DeadPID_Frozen_RFC3339(t *testing.T) {
	h, s := mustHandler(t)
	old := time.Now().Add(-50 * time.Hour).Format(time.RFC3339)
	_ = s.AgentCreate(storage.Agent{
		ID:        "dead-over-runtime-rfc",
		Project:   "p",
		Section:   "s3",
		Status:    "running",
		PID:       99999999, // dead
		CreatedAt: old,
	})

	h.enforceAgentLimits()

	a, _ := s.AgentGet("dead-over-runtime-rfc")
	if a.Status != "stopped" {
		t.Errorf("status=%q want stopped (RFC3339 created_at — max_runtime stop should fire)", a.Status)
	}
}

// yesloopPartialScratchpad is a yesloop agent that completed Phases 1-3 but
// never reached Phase 6 (typical dead-agent scenario).
const yesloopPartialScratchpad = `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Fix issue with X
**Codebase explored:** internal/daemon/

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** done_gate.go, heartbeat.go
**Test strategy:** unit tests

### Phase 3: EXECUTE
**Status:** COMPLETE
**Plan items:** 4 total

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** go test ./... → exit 0

### Phase 5: REVIEW
**Status:** IN PROGRESS
**Stage 1: Self-Review**`

func TestCrashRecovery_YesloopDeadAgent_PartialPhase_Escalates(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "dead-yesloop-0", Project: "test", Section: "yesloop-fix-issue",
		Status: "running", PID: 99999999, CreatedAt: now,
		CallerSession: "sess-orch",
	})
	s.AgentUpdate("dead-yesloop-0", map[string]any{"retry_count": 3})
	s.ScratchpadWrite("test", "yesloop-fix-issue", yesloopPartialScratchpad, "")

	// Should not panic — escalation path fires for yesloop agents with partial phase work
	h.crashRecovery()

	a, err := s.AgentGet("dead-yesloop-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "failed" {
		t.Errorf("status=%q want failed", a.Status)
	}
}

func TestCrashRecovery_YesloopDeadAgent_AllComplete_NoPanic(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "dead-yesloop-complete-0", Project: "test", Section: "yesloop-done-task",
		Status: "running", PID: 99999999, CreatedAt: now,
	})
	s.AgentUpdate("dead-yesloop-complete-0", map[string]any{"retry_count": 3})
	s.ScratchpadWrite("test", "yesloop-done-task", validV3Content, "")

	// All 6 phases COMPLETE → escalation not triggered (result.Compliant == true)
	h.crashRecovery()

	a, err := s.AgentGet("dead-yesloop-complete-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "failed" {
		t.Errorf("status=%q want failed", a.Status)
	}
}

func TestCrashRecovery_NonYesloopAgent_SkipsYesloopEscalation(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	// Non-yesloop section — should not trigger DEAD_AGENT escalation
	s.AgentCreate(storage.Agent{
		ID: "dead-regular-0", Project: "test", Section: "regular-work",
		Status: "running", PID: 99999999, CreatedAt: now,
	})
	s.AgentUpdate("dead-regular-0", map[string]any{"retry_count": 3})
	s.ScratchpadWrite("test", "regular-work", yesloopPartialScratchpad, "")

	h.crashRecovery()

	a, err := s.AgentGet("dead-regular-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "failed" {
		t.Errorf("status=%q want failed", a.Status)
	}
}

// --- pauseAgent / stopAgent semantics (replaces freezeAgent) ---

func TestStopAgent_SetsStatusStopped(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{ID: "stop-1", Project: "p", Section: "s", Status: "running"})
	h.stopAgent("stop-1", "test reason")
	a, _ := s.AgentGet("stop-1")
	if a.Status != "stopped" {
		t.Errorf("stopAgent: status=%q want stopped", a.Status)
	}
	if !strings.HasPrefix(a.Progress, "stopped:") {
		t.Errorf("stopAgent: progress=%q want prefix 'stopped:'", a.Progress)
	}
}

func TestPauseAgent_SetsStatusPaused(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{ID: "pause-1", Project: "p", Section: "s", Status: "running"})
	h.pauseAgent("pause-1", "idle escalation")
	a, _ := s.AgentGet("pause-1")
	if a.Status != "paused" {
		t.Errorf("pauseAgent: status=%q want paused", a.Status)
	}
	if !strings.HasPrefix(a.Progress, "paused:") {
		t.Errorf("pauseAgent: progress=%q want prefix 'paused:'", a.Progress)
	}
}

func TestStopAgent_Idempotent(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{ID: "stop-idem", Project: "p", Section: "s", Status: "running"})
	h.stopAgent("stop-idem", "first")
	h.stopAgent("stop-idem", "second")
	a, _ := s.AgentGet("stop-idem")
	if a.Status != "stopped" {
		t.Errorf("double stopAgent: status=%q want stopped", a.Status)
	}
}

func TestPauseAgent_Idempotent(t *testing.T) {
	h, s := mustHandler(t)
	s.AgentCreate(storage.Agent{ID: "pause-idem", Project: "p", Section: "s", Status: "running"})
	h.pauseAgent("pause-idem", "first")
	h.pauseAgent("pause-idem", "second")
	a, _ := s.AgentGet("pause-idem")
	if a.Status != "paused" {
		t.Errorf("double pauseAgent: status=%q want paused", a.Status)
	}
}

// TestAttemptRestart_OpencodeEmptySession_SkipsAndStops: the L4 guard in
// attemptRestart prevents restart of an opencode agent with empty
// OpencodeSessionID once the late-bind attempt has also failed. Without this
// guard, --resume would fall back to the daemon UUID and opencode would exit 1
// (Learning #80228). Cold-review I2.
func TestAttemptRestart_OpencodeEmptySession_SkipsAndStops(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	// Isolate HOME: the late-bind attempt must not reach the developer's live
	// opencode services, and the v1 database must not be there either.
	isolateHome(t)
	withoutV2Service(t)

	s.AgentCreate(storage.Agent{
		ID: "restart-oc", Project: "p", Section: "s",
		Status: "error", PID: 99999999, CreatedAt: now,
		Backend: "opencode", SockPath: "/tmp/restart-oc.sock",
		RestartStrategy: "permanent", SessionID: "daemon-uuid-36-chars",
		// OpencodeSessionID intentionally empty.
	})

	h.attemptRestart()

	a, _ := s.AgentGet("restart-oc")
	if a.Status != "stopped" {
		t.Errorf("opencode agent with empty OpencodeSessionID should be stopped not restarted, got status=%q", a.Status)
	}
	if !strings.Contains(a.Progress, opencodeUnresumableMsg) {
		t.Errorf("stopped agent progress should mention %q, got %q", opencodeUnresumableMsg, a.Progress)
	}
}

// TestAttemptRestart_OpencodeWithSession_Allowed: an opencode agent WITH a
// valid OpencodeSessionID proceeds through the restart path (it will fail
// later due to a bad binary path, but the L4 guard must not stop it).
func TestAttemptRestart_OpencodeWithSession_Allowed(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "restart-oc2", Project: "p", Section: "s2",
		Status: "error", PID: 99999999, CreatedAt: now,
		Backend: "opencode", SockPath: "/tmp/restart-oc2.sock",
		RestartStrategy: "permanent", SessionID: "daemon-uuid-36-chars",
		OpencodeSessionID: "ses_oc_valid",
	})

	h.attemptRestart()

	a, _ := s.AgentGet("restart-oc2")
	// Agent should NOT be stopped by the L4 guard (it has a valid session).
	// It may be running (exec started) or error (exec failed) — either way,
	// not "stopped" with the unresumable message.
	if a.Status == "stopped" && strings.Contains(a.Progress, opencodeUnresumableMsg) {
		t.Errorf("opencode agent WITH OpencodeSessionID should NOT be stopped by L4 guard, got status=%q progress=%q", a.Status, a.Progress)
	}
}

// --- C1: agent process identity ---

// stubProcCmdline swaps the /proc cmdline reader for one test. A test process
// cannot present a cmdline containing "claude"/"opencode", so the binary-name
// conjunct would otherwise be unreachable from a unit test (same seam pattern
// as var procPPID in internal/terminals/placement.go).
func stubProcCmdline(t *testing.T, cmdline string) {
	t.Helper()
	prev := procCmdline
	procCmdline = func(int) string { return cmdline }
	t.Cleanup(func() { procCmdline = prev })
}

// stubProcStartTime dates a process for one test. A test can spawn a child but
// not one that has been running for days, which is exactly the case C4 must
// leave alone.
func stubProcStartTime(t *testing.T, start time.Time) {
	t.Helper()
	prev := procStartTime
	procStartTime = func(int) (time.Time, bool) { return start, true }
	t.Cleanup(func() { procStartTime = prev })
}

// --- F1 (cold review, Critical): the spawn marker must move with the pid ---

// A resumed agent keeps its original row: created_at stays weeks old while a new
// process takes the pid. The spawn marker has to move with the pid, or
// isAgentProcess rejects the agent forever — and then every consumer reads a live
// process as gone: stop_agent finalizes the row "stopped" without signalling,
// leaving an unreachable process behind, and C2/C3/C4 stop watching it.
func TestBindAgentProcess_MarkerMovesWithThePid(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	// Force the process table on: this test pins the marker invariant, which is
	// platform-independent. Without it the test would pass vacuously where the
	// table is unreadable, because isAgentProcess returns before conjunct 2.
	prevTable := procTableReadable
	procTableReadable = true
	t.Cleanup(func() { procTableReadable = prevTable })

	if err := s.AgentCreate(storage.Agent{
		ID: "resumed", Project: "p", Section: "s1", SessionID: "sess-r",
		Status: "paused", Backend: "opencode", PID: 0,
		CreatedAt: time.Now().Add(-72 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	bound, err := h.bindAgentProcess("resumed", os.Getpid(), "/tmp/resumed.sock", "sess-r")
	if err != nil {
		t.Fatalf("bindAgentProcess: %v", err)
	}
	if !bound {
		t.Fatal("a paused row is live — the bind must write it")
	}

	a, err := s.AgentGet("resumed")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.PID != os.Getpid() || a.Status != "running" || a.SockPath != "/tmp/resumed.sock" {
		t.Errorf("bind did not record the process: pid=%d status=%q sock=%q", a.PID, a.Status, a.SockPath)
	}
	if a.LastRestartAt == "" {
		t.Fatal("spawn marker missing — isAgentProcess can never verify this agent, so a live process would be treated as gone")
	}
	if !isAgentProcess(*a) {
		t.Error("a row bound by bindAgentProcess must verify as its own process (row created 72h ago, process bound now)")
	}
}

// --- C2.4: crashRecovery decides on the agent's own process, not on signal 0 ---

// seedCrashCandidate writes a running agent with its retry budget exhausted, so
// recovery takes the terminal path (failed) instead of respawning.
func seedCrashCandidate(t *testing.T, s *storage.Store, id string, pid int, backend string) {
	t.Helper()
	if err := s.AgentCreate(storage.Agent{
		ID: id, Project: "p", Section: "sec-" + id, SessionID: "sess-" + id,
		Status: "running", Backend: backend, PID: pid,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate(%s): %v", id, err)
	}
	// retry_count is not part of AgentCreate's INSERT.
	if err := s.AgentUpdate(id, map[string]any{"retry_count": 3}); err != nil {
		t.Fatalf("set retry_count(%s): %v", id, err)
	}
}

// A recycled PID must not hide a crashed agent: signal 0 says alive, the cmdline
// says the process is not this agent's. Before, such a row stayed "running"
// forever with no recovery.
func TestCrashRecovery_ReusedPIDDoesNotHideTheCrash(t *testing.T) {
	h, s := mustHandler(t)
	pinProcessTable(t)
	bystander, _ := startBystander(t)
	seedCrashCandidate(t, s, "crash-reused", bystander.Process.Pid, "claude")

	h.crashRecovery()

	if a := reapedAgent(t, s, "crash-reused"); a.Status != "failed" {
		t.Errorf("status = %q, want failed — a PID held by another process is not this agent's process", a.Status)
	}
}

// A zombie answers signal 0 too, and its cmdline is empty: it is not a running
// agent, so recovery may proceed — and here it proceeds fully, because a zombie
// is a decided death.
func TestCrashRecovery_ZombiePIDDoesNotHideTheCrash(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	pid := startZombie(t)
	seedCrashCandidate(t, s, "crash-zombie", pid, "claude")

	h.crashRecovery()

	if a := reapedAgent(t, s, "crash-zombie"); a.Status != "failed" {
		t.Errorf("status = %q, want failed — a zombie is not a running agent", a.Status)
	}
}

// startZombie spawns a child that exits immediately and stays unreaped, so it
// answers signal 0 while the kernel has already blanked its cmdline: exactly the
// shape the identity check has to read as a decided death (Learning #101907).
// The readiness check waits for the zombie state itself: a freshly forked child
// has an empty cmdline *before* it execs, so "cmdline empty" alone is not proof.
func startZombie(t *testing.T) int {
	t.Helper()
	zombie := exec.Command("sleep", "0")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start zombie candidate: %v", err)
	}
	pid := zombie.Process.Pid
	t.Cleanup(func() { zombie.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !isProcessZombie(strconv.Itoa(pid)) {
		time.Sleep(5 * time.Millisecond)
	}
	if !isProcessZombie(strconv.Itoa(pid)) {
		t.Fatalf("child %d never reached the zombie state", pid)
	}
	if !isPIDAlive(pid) {
		t.Fatalf("isPIDAlive(zombie %d) = false, want true — signal 0 says alive", pid)
	}
	return pid
}

// The two halves of the recovery decision, pinned against each other: a zombie is
// a decided death, so the full recovery runs and the row is retried (crashed,
// retry_count advanced, replacement spawned). The ambiguous case — a live PID that
// merely fails to identify as this agent — is covered by
// TestCrashRecovery_AmbiguousPIDDoesNotRespawn and must not reach this path.
func TestCrashRecovery_ZombieGetsTheFullRecovery(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	seedCrashCandidate(t, s, "crash-zombie-retry", startZombie(t), "claude")
	if err := s.AgentUpdate("crash-zombie-retry", map[string]any{"retry_count": 0}); err != nil {
		t.Fatalf("reset retry_count: %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "crash-zombie-retry")
	if a.Status != "crashed" {
		t.Errorf("status = %q, want crashed — a zombie is a decided death, so the retry runs", a.Status)
	}
	if a.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", a.RetryCount)
	}
}

// The mirror direction: the agent's own verified process must never be declared
// crashed, however stale its row looks.
func TestCrashRecovery_LiveOwnProcessNotCrashed(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	seedCrashCandidate(t, s, "crash-own", os.Getpid(), "opencode")

	h.crashRecovery()

	if a := reapedAgent(t, s, "crash-own"); a.Status != "running" {
		t.Errorf("status = %q, want running — the PID verifies as this agent's own process", a.Status)
	}
}

// The recovery decision has two shapes. A decided death — the PID is free, or it
// holds a zombie — gets the full recovery: quarantine, taint, retry. A live PID
// that merely fails to identify as this agent may be a recycled number or a live
// agent whose row is mislabelled; the row must stop claiming to be running, but
// quarantining that agent's session or starting a second process for its section
// would be wrong, and the two causes cannot be told apart from the row.
func TestCrashRecovery_AmbiguousPIDDoesNotRespawn(t *testing.T) {
	h, s := mustHandler(t)
	pinProcessTable(t)
	bystander, _ := startBystander(t)
	seedCrashCandidate(t, s, "crash-amb", bystander.Process.Pid, "claude")
	// Below the retry limit, so the decided-death path would spawn a replacement.
	if err := s.AgentUpdate("crash-amb", map[string]any{"retry_count": 0}); err != nil {
		t.Fatalf("reset retry_count: %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "crash-amb")
	if a.Status != "failed" {
		t.Errorf("status = %q, want failed — the row must stop claiming to be running", a.Status)
	}
	if a.Error == "" {
		t.Error("the withheld recovery is not recorded on the row")
	}
	agents, err := s.AgentList("p")
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	if len(agents) != 1 {
		t.Errorf("recovery created %d rows for a section whose process may still be alive, want 1", len(agents))
	}
}

// The mirror of the ambiguous case, and the one that pins the split: a decided
// death with retries left must run the full recovery — the row is retried and a
// replacement is created. Without the split this passes too, so it exists to keep
// the other direction from being closed by accident.
func TestCrashRecovery_DecidedDeathStillRetries(t *testing.T) {
	h, s := mustHandler(t)
	seedCrashCandidate(t, s, "crash-decided", deadPID, "claude")
	if err := s.AgentUpdate("crash-decided", map[string]any{"retry_count": 0}); err != nil {
		t.Fatalf("reset retry_count: %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "crash-decided")
	if a.Status != "crashed" {
		t.Errorf("status = %q, want crashed — a free PID is a decided death", a.Status)
	}
	if a.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", a.RetryCount)
	}
}

// --- agentBinaryName must mirror the spawn switch ---

// spawnAgentProcess starts claude for every backend value that is not opencode or
// codex. agentBinaryName has to be the inverse of that switch: returning the
// backend verbatim would leave a row with a typo'd backend permanently
// unverifiable even though its process is a perfectly good claude.
func TestAgentBinaryName_MirrorsSpawnSwitch(t *testing.T) {
	for backend, want := range map[string]string{
		"opencode": "opencode",
		"codex":    "codex",
		"claude":   "claude",
		"":         "claude",
		"Claude":   "claude",
		"sonnet":   "claude",
	} {
		if got := agentBinaryName(backend); got != want {
			t.Errorf("agentBinaryName(%q) = %q, want %q", backend, got, want)
		}
	}
}

func TestIsAgentProcess_UnknownBackendMeansClaude(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")
	a := storage.Agent{ID: "a-1", Backend: "Claude", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if !isAgentProcess(a) {
		t.Error("isAgentProcess(backend \"Claude\", claude cmdline) = false, want true — spawnAgentProcess starts claude for that value")
	}
}

// --- platform without a process table ---

// Where the process table cannot be read (macOS), identity cannot be
// established — isAgentProcess must say so rather than guess, because the
// callers that act on a positive answer (stop_agent, stop-all, killAgent) would
// otherwise SIGTERM whatever live process happens to hold a recycled PID.
func TestIsAgentProcess_WithoutProcessTableIsNotVerified(t *testing.T) {
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })

	if isAgentProcess(storage.Agent{ID: "a-1", Backend: "opencode", PID: os.Getpid()}) {
		t.Error("without a process table identity is not established; a live PID must not count as this agent's process")
	}
}

// The mirrored direction: the consumers that act on a *negative* answer (the
// reaper, the DONE-guard skip) must abstain there instead of reading "cannot
// verify" as "process gone" — otherwise the first tick after deploy would close
// every paused row.
func TestAgentProcessGone_WithoutProcessTableAbstains(t *testing.T) {
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })

	if agentProcessGone(storage.Agent{ID: "a-1", Backend: "opencode", PID: os.Getpid()}) {
		t.Error("without a process table a live PID must not be reported as gone")
	}
	if !agentProcessGone(storage.Agent{ID: "a-2", Backend: "opencode", PID: 99999999}) {
		t.Error("a dead PID is gone regardless of the process table")
	}
}

// agentProcessGone is not the negation of isAgentProcess: it answers the
// question the row-closing consumers actually ask.
func TestAgentProcessGone_ReadsBothDirections(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	live := storage.Agent{ID: "a-1", Backend: "opencode", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if agentProcessGone(live) {
		t.Error("a verified live process must not be reported as gone")
	}
	recycled := storage.Agent{
		ID: "a-2", Backend: "opencode", PID: os.Getpid(),
		CreatedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	}
	if !agentProcessGone(recycled) {
		t.Error("for this row the PID belongs to a process started long after it — that process is not the agent's")
	}
}

func TestReapGoneAgents_WithoutProcessTableKeepsLiveAgent(t *testing.T) {
	h, s := mustHandler(t)
	prev := procTableReadable
	procTableReadable = false
	t.Cleanup(func() { procTableReadable = prev })
	seedPausedAgent(t, s, "reap-no-proc", os.Getpid(), time.Now().Add(-30*time.Hour))

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-no-proc"); a.Status != "paused" {
		t.Errorf("status = %q, want paused (no process table: the reaper must abstain)", a.Status)
	}
}

// --- bounded cmdline read ---

// A colliding PID can present a multi-megabyte argv; only the leading arguments
// can carry the binary name (same bound as internal/setup/mcp_procs.go).
func TestReadProcCmdline_BoundsHugeArgv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "4242"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "4242", "cmdline"), []byte(strings.Repeat("A", 1<<20)), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	prev := procRoot
	procRoot = dir
	t.Cleanup(func() { procRoot = prev })

	got := readProcCmdline(4242)
	if len(got) == 0 {
		t.Fatal("fixture cmdline read returned nothing")
	}
	if len(got) > maxCmdlineBytes {
		t.Errorf("read %d bytes, want at most %d", len(got), maxCmdlineBytes)
	}
}

// --- killAgent: the last ungated signal in heartbeat.go ---

func TestKillAgent_DoesNotSignalBystanderPID(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)
	if err := s.AgentCreate(storage.Agent{
		ID: "ka-1", Project: "p", Section: "s", Status: "running",
		Backend: "claude", PID: bystander.Process.Pid,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	a, err := s.AgentGet("ka-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	h.killAgent(a)

	if _, err := s.AgentGet("ka-1"); err == nil {
		t.Error("killAgent did not delete the row")
	}
	select {
	case <-exited:
		t.Error("killAgent signalled a PID that belongs to an unrelated process")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestIsPIDAlive_ZombieCountsAsAlive pins the hole that isAgentProcess closes:
// signal 0 succeeds for a zombie child that was never reaped, so a row whose
// backend process is dead still reads as "alive" (Learning #101907). Its
// cmdline is empty — that is what the identity check can see and signal 0
// cannot.
func TestIsPIDAlive_ZombieCountsAsAlive(t *testing.T) {
	zombie := exec.Command("sleep", "0")
	if err := zombie.Start(); err != nil {
		t.Fatalf("start zombie candidate: %v", err)
	}
	pid := zombie.Process.Pid
	// Deliberately NOT reaped until cleanup: that is what keeps it a zombie.
	t.Cleanup(func() { zombie.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(readProcFile(pid, "cmdline")) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if !isPIDAlive(pid) {
		t.Fatalf("isPIDAlive(zombie %d) = false, want true — signal 0 succeeds for zombies", pid)
	}
}

// readProcFile is a test-local /proc reader so the characterization test above
// compiles against the unpatched tree.
func readProcFile(pid int, name string) []byte {
	data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/" + name)
	return data
}

func TestProcStartTime_OwnPID(t *testing.T) {
	requireProcTable(t)
	start, ok := procStartTime(os.Getpid())
	if !ok {
		t.Fatal("procStartTime(self) not ok, want a parsed start time")
	}
	if start.IsZero() {
		t.Error("procStartTime(self) = zero time, want the process start")
	}
	if start.After(time.Now()) {
		t.Errorf("procStartTime(self) = %v, in the future", start)
	}
	if age := time.Since(start); age > 24*time.Hour {
		t.Errorf("procStartTime(self) age = %v, implausible for a test process", age)
	}
}

func TestProcStartTime_DeadPID(t *testing.T) {
	if _, ok := procStartTime(99999999); ok {
		t.Error("procStartTime(99999999) ok = true, want false")
	}
}

// The own PID is the only PID a test reliably owns: alive, with a real kernel
// start time. Only the cmdline is stubbed, so the start-time conjunct still
// reads real /proc.
func TestIsAgentProcess_OwnPIDWithMatchingCmdline(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode\x00--theme\x00yesmem-loop")
	a := storage.Agent{ID: "a-1", Backend: "opencode", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if !isAgentProcess(a) {
		t.Error("isAgentProcess(own pid, matching cmdline, fresh row) = false, want true")
	}
}

func TestIsAgentProcess_RejectsForeignCmdline(t *testing.T) {
	stubProcCmdline(t, "/usr/bin/firefox\x00--new-window")
	a := storage.Agent{ID: "a-1", Backend: "opencode", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(own pid, foreign cmdline) = true, want false")
	}
}

func TestIsAgentProcess_RejectsBackendMismatch(t *testing.T) {
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode\x00--theme\x00yesmem-loop")
	a := storage.Agent{ID: "a-1", Backend: "claude", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(opencode cmdline, claude backend) = true, want false")
	}
}

// A recycled PID: the process started long after the row was written, so it
// cannot be the process the row refers to. 48h makes the assertion independent
// of how long the test binary itself has been running.
func TestIsAgentProcess_RejectsProcessNewerThanRow(t *testing.T) {
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode\x00--theme\x00yesmem-loop")
	a := storage.Agent{
		ID: "a-1", Backend: "opencode", PID: os.Getpid(),
		CreatedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(process started 48h after the row) = true, want false")
	}
}

// last_restart_at is the newer spawn marker and wins over created_at, so a
// restarted agent is not rejected by its own old creation timestamp.
func TestIsAgentProcess_LastRestartAtIsTheReference(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode\x00--theme\x00yesmem-loop")
	a := storage.Agent{
		ID: "a-1", Backend: "opencode", PID: os.Getpid(),
		CreatedAt:     time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		LastRestartAt: time.Now().Add(-time.Minute).Format("2006-01-02 15:04:05"),
	}
	if !isAgentProcess(a) {
		t.Error("isAgentProcess(restarted 1min ago, created 48h ago) = false, want true")
	}
}

func TestIsAgentProcess_DeadPID(t *testing.T) {
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode")
	a := storage.Agent{ID: "a-1", Backend: "opencode", PID: 99999999, CreatedAt: time.Now().Format(time.RFC3339)}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(dead pid) = true, want false")
	}
}

func TestIsAgentProcess_ZeroPID(t *testing.T) {
	stubProcCmdline(t, "/home/tester/.opencode/bin/opencode")
	a := storage.Agent{ID: "a-1", Backend: "opencode", PID: 0, CreatedAt: time.Now().Format(time.RFC3339)}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(pid 0) = true, want false")
	}
}

// An agent row with no backend column reads back as "claude" (AgentCreate
// default); the empty string must resolve the same way here.
func TestIsAgentProcess_EmptyBackendMeansClaude(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")
	a := storage.Agent{ID: "a-1", PID: os.Getpid(), CreatedAt: time.Now().Format(time.RFC3339)}
	if !isAgentProcess(a) {
		t.Error("isAgentProcess(empty backend, claude cmdline) = false, want true")
	}
}

// An unparseable creation stamp leaves the start-time conjunct unverifiable; the
// cmdline conjunct still has to hold, otherwise the row would be unkillable.
func TestIsAgentProcess_UnparseableCreatedAtFallsBackToCmdline(t *testing.T) {
	requireProcTable(t)
	stubProcCmdline(t, "/usr/local/bin/claude\x00--session-id\x00x")
	matching := storage.Agent{ID: "a-1", Backend: "claude", PID: os.Getpid(), CreatedAt: "not a timestamp"}
	if !isAgentProcess(matching) {
		t.Error("isAgentProcess(unparseable created_at, matching cmdline) = false, want true")
	}
	stubProcCmdline(t, "/usr/bin/firefox")
	foreign := storage.Agent{ID: "a-2", Backend: "claude", PID: os.Getpid(), CreatedAt: "not a timestamp"}
	if isAgentProcess(foreign) {
		t.Error("isAgentProcess(unparseable created_at, foreign cmdline) = true, want false")
	}
}

// startNamedProcess spawns a real child whose argv carries name, so the kernel's own
// cmdline is what isAgentProcess reads — no stub. isAgentProcess looks for the
// backend's name anywhere in the argv, so a script whose path carries the name is
// enough.
//
// A script, not a symlink to sleep: an image that ships sleep as one applet of a
// multicall binary dispatches on argv[0]'s basename, so a symlink under another name
// answers "unknown applet" and exits at once — the test then reads a live process as
// gone (ubuntu-26 runner, 2026-11-12).
func startNamedProcess(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	script := filepath.Join(t.TempDir(), name)
	// No exec: the shell must stay the process under the name the test asked for.
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+sleepBin+" 30\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", script, err)
	}
	cmd := exec.Command(script)
	if err := cmd.Start(); err != nil {
		t.Skipf("start %s: %v", script, err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func TestIsAgentProcess_RealProcessWithBackendNameInCmdline(t *testing.T) {
	requireProcTable(t)
	cmd := startNamedProcess(t, "claude")
	a := storage.Agent{
		ID: "a-1", Backend: "claude", PID: cmd.Process.Pid,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	// The argv of a just-started process can still be empty (YM-234), so the
	// assertion waits for it instead of reading the process table once.
	if cmdline := waitForProcCmdline(t, cmd.Process.Pid); !strings.Contains(cmdline, "claude") {
		t.Fatalf("precondition: /proc/%d/cmdline = %q, want the claude argv", cmd.Process.Pid, cmdline)
	}
	if !isAgentProcess(a) {
		t.Errorf("isAgentProcess(real %q process) = false, want true (cmdline: %q)",
			"claude", readProcCmdline(cmd.Process.Pid))
	}
}

func TestIsAgentProcess_RealProcessWithWrongBackend(t *testing.T) {
	requireProcTable(t)
	cmd := startNamedProcess(t, "claude")
	a := storage.Agent{
		ID: "a-1", Backend: "opencode", PID: cmd.Process.Pid,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	// Without a readable argv the test would pass for the wrong reason.
	if cmdline := waitForProcCmdline(t, cmd.Process.Pid); !strings.Contains(cmdline, "claude") {
		t.Fatalf("precondition: /proc/%d/cmdline = %q, want the claude argv", cmd.Process.Pid, cmdline)
	}
	if isAgentProcess(a) {
		t.Error("isAgentProcess(real claude process, opencode backend) = true, want false")
	}
}

// --- C3: the DONE-guard must not act on a paused agent whose process is gone ---

// seedGoneDoneGuardAgent creates a paused, DONE-GUARD-marked yesloop agent whose
// process does not exist, with a scratchpad that has all 6 phase blocks but
// fails validation — exactly the row shape left behind by a daemon restart
// (agent-20261008-02, whose DONE-GUARD notices re-fired on 2026-10-15).
// The progress field is written with a second call: AgentCreate's INSERT does
// not carry the progress column, so setting it in the struct is a no-op.
func seedGoneDoneGuardAgent(t *testing.T, s *storage.Store, id string, pid int) {
	t.Helper()
	if err := s.AgentCreate(storage.Agent{
		ID: id, Project: "testproj", Section: "yesloop-" + id,
		SessionID: "sess-" + id, PID: pid, Status: "paused", Backend: "claude",
		CallerSession: "caller-" + id,
	}); err != nil {
		t.Fatalf("AgentCreate(%s): %v", id, err)
	}
	if err := s.AgentUpdate(id, map[string]any{
		"progress": "paused: DONE-GUARD: phase validation failed — Phase5:security: subagent session ses_x has agent \"general\", not in allowed set [reviewer]",
	}); err != nil {
		t.Fatalf("AgentUpdate(%s): %v", id, err)
	}
	if err := s.ScratchpadWrite("testproj", "yesloop-"+id, allPhasesPhase4Invalid, ""); err != nil {
		t.Fatalf("ScratchpadWrite(%s): %v", id, err)
	}
}

// TestCheckYesloopDoneGuard_SkipsDeadPausedAgent: DONE-guard state is in-memory,
// so after every daemon restart the guard re-ran its full relay/refire/
// escalation cycle for *every* paused loop agent — including 29 whose process
// had been dead for weeks. The row is the only thing a restart preserves, so
// the process check has to be the gate.
func TestCheckYesloopDoneGuard_SkipsDeadPausedAgent(t *testing.T) {
	resetDoneGuardState()
	t.Cleanup(resetDoneGuardState)
	h, s := mustHandler(t)

	seedGoneDoneGuardAgent(t, s, "dg-gone", 0)

	h.checkYesloopDoneGuard()

	if doneGuardTracked("dg-gone") {
		t.Error("DONE-guard tracked a paused agent whose process is gone — it would relay and escalate again after every restart")
	}
}

// A reused PID must not revive the row either: the process is alive but belongs
// to someone else, which is the case the DONE-guard cannot see with signal 0.
func TestCheckYesloopDoneGuard_SkipsPausedAgentBehindBystanderPID(t *testing.T) {
	requireProcTable(t)
	resetDoneGuardState()
	t.Cleanup(resetDoneGuardState)
	h, s := mustHandler(t)

	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	t.Cleanup(func() { bystander.Process.Kill(); bystander.Wait() })

	seedGoneDoneGuardAgent(t, s, "dg-bystander", bystander.Process.Pid)

	h.checkYesloopDoneGuard()

	if doneGuardTracked("dg-bystander") {
		t.Error("DONE-guard tracked a paused agent whose PID belongs to a bystander process")
	}
}

// The skip is about the process, not about being paused: a paused DONE-GUARD
// agent that is genuinely still working must stay in the recovery path.
func TestCheckYesloopDoneGuard_KeepsLivePausedAgent(t *testing.T) {
	resetDoneGuardState()
	t.Cleanup(resetDoneGuardState)
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	seedGoneDoneGuardAgent(t, s, "dg-live", os.Getpid())
	if err := s.AgentUpdate("dg-live", map[string]any{"backend": "opencode"}); err != nil {
		t.Fatalf("update backend: %v", err)
	}

	h.checkYesloopDoneGuard()

	if !doneGuardTracked("dg-live") {
		t.Error("DONE-guard skipped a paused agent whose process is verified alive — the recovery path is gone")
	}
}

// --- C4: reaping paused agents whose process is gone ---

const deadPID = 99999999

func stampAgo(d time.Duration) string {
	return time.Now().Add(-d).Format(time.RFC3339)
}

// setAgentStamp writes a column AgentCreate cannot carry (heartbeat_at,
// last_activity_at, progress).
func setAgentStamp(t *testing.T, s *storage.Store, id, column, value string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE agents SET `+column+` = ? WHERE id = ?`, value, id); err != nil {
		t.Fatalf("set %s on %s: %v", column, id, err)
	}
}

func seedPausedAgent(t *testing.T, s *storage.Store, id string, pid int, createdAt time.Time) {
	t.Helper()
	if err := s.AgentCreate(storage.Agent{
		ID: id, Project: "testproj", Section: "sec-" + id, SessionID: "sess-" + id,
		PID: pid, Status: "paused", Backend: "claude",
		CreatedAt: createdAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate(%s): %v", id, err)
	}
}

func reapedAgent(t *testing.T, s *storage.Store, id string) *storage.Agent {
	t.Helper()
	a, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet(%s): %v", id, err)
	}
	return a
}

// A paused row whose process has been gone for a day is a zombie: nothing will
// end it, and every guard that reads paused rows keeps acting on a process that
// does not exist.
func TestReapGoneAgents_StopsLongGonePausedAgent(t *testing.T) {
	h, s := mustHandler(t)
	seedPausedAgent(t, s, "reap-old", deadPID, time.Now().Add(-25*time.Hour))
	if err := s.AgentUpdate("reap-old", map[string]any{"sock_path": "/tmp/reap-old.sock"}); err != nil {
		t.Fatalf("set sock_path: %v", err)
	}

	h.reapGoneAgents()

	a := reapedAgent(t, s, "reap-old")
	if a.Status != "stopped" {
		t.Errorf("status = %q, want stopped", a.Status)
	}
	if a.Progress != "auto-stopped: process gone" {
		t.Errorf("progress = %q, want %q", a.Progress, "auto-stopped: process gone")
	}
	// Closed the way every other stop path closes a row: a stopped row must not
	// keep advertising a pid and a socket for a process that is gone.
	if a.PID != 0 {
		t.Errorf("pid = %d, want 0", a.PID)
	}
	if a.SockPath != "" {
		t.Errorf("sock_path = %q, want empty", a.SockPath)
	}
	if a.StoppedAt == "" {
		t.Error("stopped_at not set")
	}
}

// The 24h window is what keeps the reaper away from agents that are merely
// between turns: an hour of silence is normal for a long tool call (#73191).
func TestReapGoneAgents_KeepsRecentlyGonePausedAgent(t *testing.T) {
	h, s := mustHandler(t)
	seedPausedAgent(t, s, "reap-fresh", deadPID, time.Now().Add(-time.Hour))

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-fresh"); a.Status != "paused" {
		t.Errorf("status = %q, want paused (gone for only 1h)", a.Status)
	}
}

// A genuinely long-running agent that is paused with its process still alive is
// not a zombie: the row is older than 24h *and* the process is old, so the
// start-time conjunct holds. This is the case the reaper must never touch.
func TestReapGoneAgents_KeepsLivePausedAgent(t *testing.T) {
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	stubProcStartTime(t, time.Now().Add(-30*time.Hour))
	seedPausedAgent(t, s, "reap-live", os.Getpid(), time.Now().Add(-25*time.Hour))
	if err := s.AgentUpdate("reap-live", map[string]any{"backend": "opencode"}); err != nil {
		t.Fatalf("update backend: %v", err)
	}

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-live"); a.Status != "paused" {
		t.Errorf("status = %q, want paused (verified process alive since 30h)", a.Status)
	}
}

// The mirror image of the live case: the same row and a live PID, but the spawn
// marker dates the process as far newer than the row says it can be — so the PID
// was recycled onto an unrelated process (C1 + C4). A *resumed* agent is never
// this shape: bindAgentProcess moves the marker with the pid.
func TestReapGoneAgents_ReapsPausedAgentWhosePIDWasRecycled(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	stubProcStartTime(t, time.Now().Add(-time.Minute))
	seedPausedAgent(t, s, "reap-recycled", os.Getpid(), time.Now().Add(-25*time.Hour))
	if err := s.AgentUpdate("reap-recycled", map[string]any{"backend": "opencode"}); err != nil {
		t.Fatalf("update backend: %v", err)
	}

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-recycled"); a.Status != "stopped" {
		t.Errorf("status = %q, want stopped (process started long after the row)", a.Status)
	}
}

// A bystander on a recycled PID must not keep the row alive: signal 0 says
// "alive", the cmdline says "not claude" (C1 + C4).
func TestReapGoneAgents_ReapsPausedAgentBehindBystanderPID(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	t.Cleanup(func() { bystander.Process.Kill(); bystander.Wait() })

	seedPausedAgent(t, s, "reap-bystander", bystander.Process.Pid, time.Now().Add(-25*time.Hour))

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-bystander"); a.Status != "stopped" {
		t.Errorf("status = %q, want stopped (PID belongs to a bystander)", a.Status)
	}
}

// Only paused rows are in scope: running agents belong to crashRecovery, and
// terminal statuses are already closed.
func TestReapGoneAgents_IgnoresNonPausedStatuses(t *testing.T) {
	h, s := mustHandler(t)
	for _, status := range []string{"running", "error", "finished", "stopped"} {
		id := "reap-status-" + status
		if err := s.AgentCreate(storage.Agent{
			ID: id, Project: "testproj", Section: "sec-" + id, SessionID: "sess-" + id,
			PID: deadPID, Status: status, Backend: "claude",
			CreatedAt: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("AgentCreate(%s): %v", id, err)
		}
	}

	h.reapGoneAgents()

	for _, status := range []string{"running", "error", "finished", "stopped"} {
		if a := reapedAgent(t, s, "reap-status-"+status); a.Status != status {
			t.Errorf("agent with status %q was rewritten to %q, want untouched", status, a.Status)
		}
	}
}

// "Gone for 24h" is measured from the newest lifecycle stamp: a row created two
// days ago but active an hour ago is not a zombie.
func TestReapGoneAgents_UsesNewestStamp(t *testing.T) {
	h, s := mustHandler(t)
	seedPausedAgent(t, s, "reap-active", deadPID, time.Now().Add(-48*time.Hour))
	setAgentStamp(t, s, "reap-active", "last_activity_at", time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05"))
	seedPausedAgent(t, s, "reap-heartbeat", deadPID, time.Now().Add(-48*time.Hour))
	setAgentStamp(t, s, "reap-heartbeat", "heartbeat_at", stampAgo(26*time.Hour))

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-active"); a.Status != "paused" {
		t.Errorf("status = %q, want paused (last_activity_at is 1h old)", a.Status)
	}
	if a := reapedAgent(t, s, "reap-heartbeat"); a.Status != "stopped" {
		t.Errorf("status = %q, want stopped (newest stamp is 26h old)", a.Status)
	}
}

// A row that cannot be dated at all is left alone: without a stamp there is no
// evidence the process has been gone, only that somebody stopped reporting.
func TestReapGoneAgents_IgnoresUndatedRow(t *testing.T) {
	h, s := mustHandler(t)
	seedPausedAgent(t, s, "reap-undated", deadPID, time.Now())
	setAgentStamp(t, s, "reap-undated", "created_at", "")

	h.reapGoneAgents()

	if a := reapedAgent(t, s, "reap-undated"); a.Status != "paused" {
		t.Errorf("status = %q, want paused (no parseable stamp)", a.Status)
	}
}

// --- YM-234: a freshly started process reports an empty cmdline ---

// waitForProcCmdline returns a process's argv, waiting a bounded moment for the
// kernel to have one. A child that was just forked (or that has not reached
// execve yet) has no cmdline, and a single read turned a load-dependent race
// into a red test: two of five full-suite runs read "" for a freshly started
// probe process while the test was green in isolation (Learning #102201).
func waitForProcCmdline(t *testing.T, pid int) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cmdline := procCmdline(pid); cmdline != "" {
			return cmdline
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The helper is what makes the real-process test deterministic, so it gets its
// own test against the process-table seam: the first reads come back empty and
// only a reading that waits reaches the argv.
func TestWaitForProcCmdline_PollsUntilTheKernelHasTheArgv(t *testing.T) {
	reads := 0
	prev := procCmdline
	procCmdline = func(int) string {
		reads++
		if reads < 3 {
			return ""
		}
		return "/usr/local/bin/claude\x00--serve"
	}
	t.Cleanup(func() { procCmdline = prev })

	got := waitForProcCmdline(t, 42)
	if !strings.Contains(got, "claude") {
		t.Fatalf("waitForProcCmdline = %q, want the argv after the empty reads", got)
	}
	if reads < 3 {
		t.Errorf("read the cmdline %d times, want at least 3 (it must wait, not read once)", reads)
	}
}

// And it gives up: a process that never reports an argv must not hang the test.
func TestWaitForProcCmdline_GivesUpOnAnEmptyCmdline(t *testing.T) {
	prev := procCmdline
	procCmdline = func(int) string { return "" }
	t.Cleanup(func() { procCmdline = prev })

	if got := waitForProcCmdline(t, 42); got != "" {
		t.Errorf("waitForProcCmdline = %q, want empty after the deadline", got)
	}
}

// --- YM-63: the crash quarantine must cover every session address ---

// quarantinedRows counts the learnings of one session_id that carry a quarantine
// stamp. The store exposes no reader for the column, and the assertion is about
// the row state, not about a count the quarantine reported.
func quarantinedRows(t *testing.T, s *storage.Store, sessionID string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM learnings WHERE session_id = ? AND quarantined_at IS NOT NULL`,
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count quarantined learnings of %q: %v", sessionID, err)
	}
	return n
}

// A daemon-spawned opencode agent's learnings are keyed by the address its proxy
// reported ("opencode:<ses>", older writers stored the bare id), never by the
// synthetic daemon UUID the row carries as session_id. Quarantining only that
// UUID left every learning of a crashed agent searchable.
func TestCrashRecovery_QuarantinesEverySessionAddressOfTheAgent(t *testing.T) {
	h, s := mustHandler(t)
	const backendSession = "ses_crashaddress0000000000001"

	if err := s.AgentCreate(storage.Agent{
		ID: "crash-addr", Project: "p", Section: "s-crash-addr",
		Status: "running", PID: deadPID, Backend: "opencode",
		SessionID: "daemon-uuid-crash-addr", OpencodeSessionID: backendSession,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdate("crash-addr", map[string]any{"retry_count": 3}); err != nil {
		t.Fatalf("set retry_count: %v", err)
	}

	addresses := []string{"opencode:" + backendSession, backendSession}
	for _, addr := range addresses {
		if _, err := s.InsertLearning(&models.Learning{
			Content: "crashed agent learning under " + addr, Category: "gotcha", SessionID: addr,
		}); err != nil {
			t.Fatalf("InsertLearning(%s): %v", addr, err)
		}
	}

	h.crashRecovery()

	for _, addr := range addresses {
		if n := quarantinedRows(t, s, addr); n != 1 {
			t.Errorf("learnings of %q not quarantined after the crash (quarantined=%d, want 1)", addr, n)
		}
	}
}

// --- YM-72: a finished or crashed parent is a dead parent ---

// detectOrphanedAgents counted only stopped, error and failed as a dead parent.
// A parent that finished or crashed is just as gone, so its children stayed
// running with nobody to report to.
func TestDetectOrphanedAgents_CountsFinishedAndCrashedParentAsDead(t *testing.T) {
	for _, status := range []string{"finished", "crashed"} {
		t.Run(status, func(t *testing.T) {
			h, s := mustHandler(t)
			now := time.Now().Format("2006-01-02 15:04:05")
			past := time.Now().Add(-6 * time.Minute).Format("2006-01-02 15:04:05")

			if err := s.AgentCreate(storage.Agent{
				ID: "parent-" + status, SessionID: "sess-parent-" + status,
				Project: "p", Section: "parent", Status: status, CreatedAt: now,
			}); err != nil {
				t.Fatalf("AgentCreate(parent): %v", err)
			}
			if err := s.AgentCreate(storage.Agent{
				ID: "child-" + status, SessionID: "sess-child-" + status,
				Project: "p", Section: "child", Status: "running",
				CallerSession: "sess-parent-" + status, LivenessPingAt: past, CreatedAt: now,
			}); err != nil {
				t.Fatalf("AgentCreate(child): %v", err)
			}

			h.detectOrphanedAgents()

			child := reapedAgent(t, s, "child-"+status)
			if child.Status != "stopped" {
				t.Errorf("child status = %q, want stopped — a %s parent is dead and the grace period expired", child.Status, status)
			}
		})
	}
}

// --- YM-65: crashed rows are never collected ---

// crashRecovery writes "crashed" and respawnAgent replaces the row two seconds
// later. A row that is still crashed long after that had no replacement coming,
// yet no guard looks at crashed rows: they stayed in the table as neither
// running nor finished. Live instance on 2026-10-17: four such rows, the oldest
// from 2026-04-28.
func TestCrashRecovery_ClosesAbandonedCrashedRow(t *testing.T) {
	h, s := mustHandler(t)
	stamp := stampAgo(10 * time.Minute)

	if err := s.AgentCreate(storage.Agent{
		ID: "abandoned-crash", Project: "p", Section: "s-abandoned",
		Status: "crashed", PID: deadPID, RetryCount: 1,
		SessionID: "sess-abandoned-crash",
		CreatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	setAgentStamp(t, s, "abandoned-crash", "stopped_at", stamp)

	h.crashRecovery()

	a := reapedAgent(t, s, "abandoned-crash")
	if a.Status != "failed" {
		t.Errorf("status = %q, want failed — a crashed row with no replacement for 10m is abandoned", a.Status)
	}
	if !strings.Contains(a.Error, "replacement") {
		t.Errorf("error = %q, want it to name the missing replacement", a.Error)
	}
}

// Closing an abandoned row must not claim the section is empty when the crash
// already produced a replacement: the notice would send the orchestrator looking
// for a problem that is not there.
func TestCrashRecovery_AbandonedRowWithLiveReplacementDoesNotClaimAnEmptySection(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	if err := s.AgentCreate(storage.Agent{
		ID: "abandoned-replaced", Project: "p", Section: "s-replaced",
		Status: "crashed", PID: deadPID, RetryCount: 1,
		SessionID: "sess-abandoned-replaced", CallerSession: "sess-caller",
		CreatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate(crashed): %v", err)
	}
	setAgentStamp(t, s, "abandoned-replaced", "stopped_at", stampAgo(10*time.Minute))
	if err := s.AgentCreate(storage.Agent{
		ID: "successor-live", Project: "p", Section: "s-replaced",
		Status: "running", PID: os.Getpid(), Backend: "opencode",
		SessionID: "sess-successor-live",
		// The replacement is freshly spawned, so the spawn marker dates its
		// process (isAgentProcess rejects a process newer than marker+grace).
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate(replacement): %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "abandoned-replaced")
	if a.Status != "failed" {
		t.Fatalf("status = %q, want failed", a.Status)
	}
	// The successor's own id, not the word "replacement": the note already contains
	// "no replacement started", so a substring check on that word proves nothing.
	if !strings.Contains(a.Error, "successor-live") {
		t.Errorf("error = %q, want the running successor named by id", a.Error)
	}
	msgs, err := s.GetUnnotifiedMessages([]string{"sess-caller"})
	if err != nil {
		t.Fatalf("GetUnnotifiedMessages: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "AGENT_ABANDONED") {
			t.Errorf("caller was told the agent is abandoned although %q owns the section: %q", "successor-live", m.Content)
		}
	}
}

// The 2s respawn window must survive: closing the row before respawnAgent looks
// at it would silently cancel the recovery of every crashed agent.
func TestCrashRecovery_LeavesFreshCrashedRowForItsRespawn(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "fresh-crash", Project: "p", Section: "s-fresh",
		Status: "crashed", PID: deadPID, RetryCount: 1,
		SessionID: "sess-fresh-crash",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	setAgentStamp(t, s, "fresh-crash", "stopped_at", stampAgo(2*time.Second))

	h.crashRecovery()

	if a := reapedAgent(t, s, "fresh-crash"); a.Status != "crashed" {
		t.Errorf("status = %q, want crashed — the replacement has not had its 2s yet", a.Status)
	}
}

// --- YM-69: a running row with no process ---

// crashRecovery skipped rows with no bound PID, and detectHungAgents could not
// reach them either once crashRecovery had cleared every row that did have one.
// Such a row claims to run and owns nothing — it is a crash, and the recovery
// path is where it belongs.
func TestCrashRecovery_RecoversRunningRowWithoutAProcess(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "unbound-row", Project: "p", Section: "s-unbound",
		Status: "running", PID: 0, SessionID: "sess-unbound-row",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "unbound-row")
	if a.Status != "crashed" {
		t.Errorf("status = %q, want crashed — a running row with no process gets the ordinary recovery", a.Status)
	}
	if a.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", a.RetryCount)
	}
}

// detectHungAgents reported hung agents by heartbeat age; folding it into crash
// recovery must not lose that fact — the final failure is the only place an
// operator reads why the agent is gone.
func TestCrashRecovery_FinalFailureCarriesTheHeartbeatStaleness(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "stale-heartbeat", Project: "p", Section: "s-stale",
		Status: "running", PID: deadPID, SessionID: "sess-stale-heartbeat",
		CreatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdate("stale-heartbeat", map[string]any{"retry_count": 3}); err != nil {
		t.Fatalf("set retry_count: %v", err)
	}
	setAgentStamp(t, s, "stale-heartbeat", "heartbeat_at", stampAgo(20*time.Minute))

	h.crashRecovery()

	a := reapedAgent(t, s, "stale-heartbeat")
	if !strings.Contains(a.Error, "heartbeat") {
		t.Errorf("error = %q, want the stale heartbeat named — that is the only trace the former hung-agent guard left", a.Error)
	}
}

// --- YM-66: a limit must end the process, not only the row ---

// The limit paths wrote the row and left the process running: the agent kept
// working on a section every guard had already closed, with the orchestrator
// unable to see it. The stop must reach the verified process.
func TestEnforceAgentLimits_TokenBudgetEndsTheProcess(t *testing.T) {
	h, s := mustHandler(t)
	child, exited := startBystander(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	stubProcStartTime(t, time.Now().Add(-time.Minute))

	if err := s.AgentCreate(storage.Agent{
		ID: "budget-process", Project: "p", Section: "s-budget",
		Status: "running", PID: child.Process.Pid, Backend: "opencode",
		SessionID: "sess-budget-process", TokenBudget: 1000,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdateTelemetry("budget-process", 1, 600, 1100); err != nil {
		t.Fatalf("AgentUpdateTelemetry: %v", err)
	}

	h.enforceAgentLimits()

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after the token budget stop — the limit only closed the row")
	}
	a := reapedAgent(t, s, "budget-process")
	if a.Status != "stopped" {
		t.Errorf("status = %q, want stopped", a.Status)
	}
	if !strings.Contains(a.Progress, "token_budget") {
		t.Errorf("progress = %q, want the limit named", a.Progress)
	}
}

// A limit stops the agent, so the process ends here; a bystander PID that does
// not identify as the agent must not be signalled by that path either.
func TestStopAgentForLimit_DoesNotSignalABystander(t *testing.T) {
	h, s := mustHandler(t)
	bystander, exited := startBystander(t)
	pinProcessTable(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "budget-bystander", Project: "p", Section: "s-budget-b",
		Status: "running", PID: bystander.Process.Pid, Backend: "claude",
		SessionID: "sess-budget-bystander",
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	h.stopAgentForLimit("budget-bystander", "token_budget 10 exceeded")

	select {
	case <-exited:
		t.Fatal("stopAgentForLimit signalled a PID that does not identify as the agent's own process")
	case <-time.After(200 * time.Millisecond):
	}
	if a := reapedAgent(t, s, "budget-bystander"); a.Status != "stopped" {
		t.Errorf("status = %q, want stopped — the row is closed without a signal", a.Status)
	}
}

// --- YM-68: restart_strategy is never set, so attemptRestart never fires ---

// No spawn path writes restart_strategy, and the INSERT carries the empty struct
// field over the column default: every row read back as either "" or
// 'temporary', both of which attemptRestart skips. The strategy the caller asks
// for has to be persisted.
func TestHandleSpawnAgent_PersistsTheRestartStrategy(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{Method: "spawn_agent", Params: map[string]any{
		"project": "proj", "section": "sec-restart-strategy",
		"work_dir": t.TempDir(),
		"backend":  "claude", "restart_strategy": "transient",
	}})
	if resp.Error != "" {
		t.Fatalf("spawn_agent: %s", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	id, _ := result["id"].(string)
	if id == "" {
		t.Fatalf("spawn returned no agent id: %s", resp.Result)
	}

	a := reapedAgent(t, s, id)
	if a.RestartStrategy != "transient" {
		t.Errorf("restart_strategy = %q, want transient", a.RestartStrategy)
	}
	if a.MaxRestarts != 3 {
		t.Errorf("max_restarts = %d, want 3 (the default for a transient agent)", a.MaxRestarts)
	}
}

// Omitting the strategy must not leave the column empty: an empty value is what
// made the restart guard look dead (it treats "" and "temporary" alike).
func TestHandleSpawnAgent_DefaultsTheRestartStrategyToTemporary(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.Handle(Request{Method: "spawn_agent", Params: map[string]any{
		"project": "proj", "section": "sec-restart-default", "backend": "claude",
		"work_dir": t.TempDir(),
	}})
	if resp.Error != "" {
		t.Fatalf("spawn_agent: %s", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	id, _ := result["id"].(string)

	if a := reapedAgent(t, s, id); a.RestartStrategy != "temporary" {
		t.Errorf("restart_strategy = %q, want temporary (the column default, written explicitly)", a.RestartStrategy)
	} else if a.MaxRestarts != 3 {
		t.Errorf("max_restarts = %d, want 3 — the INSERT overrides the column default, so a row must not carry 0", a.MaxRestarts)
	}
}

// "permanent" is exempt from max_restarts in attemptRestart and nothing above it
// caps a failing spawn loop, so a spawn caller may not ask for it.
func TestHandleSpawnAgent_RejectsAnUnknownOrUnboundedRestartStrategy(t *testing.T) {
	for _, strategy := range []string{"sometimes", "permanent"} {
		t.Run(strategy, func(t *testing.T) {
			h, _ := mustHandler(t)

			resp := h.Handle(Request{Method: "spawn_agent", Params: map[string]any{
				"project": "proj", "section": "sec-restart-invalid",
				"work_dir": t.TempDir(),
				"backend":  "claude", "restart_strategy": strategy,
			}})
			if resp.Error == "" {
				t.Fatalf("spawn_agent accepted restart_strategy=%s", strategy)
			}
		})
	}
}

// The chain the audit found broken end to end: a strategy set at spawn must make
// attemptRestart act on the row a failed spawn leaves behind.
func TestAttemptRestart_AppliesTheStrategyPersistedAtSpawn(t *testing.T) {
	h, s := mustHandler(t)
	spawn := stubRestartSpawnSync(t, "0.3")
	h.disableAgentProcesses = false

	resp := h.Handle(Request{Method: "spawn_agent", Params: map[string]any{
		"project": "proj", "section": "sec-restart-chain",
		"work_dir": t.TempDir(),
		"backend":  "claude", "restart_strategy": "transient",
	}})
	if resp.Error != "" {
		t.Fatalf("spawn_agent: %s", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	id, _ := result["id"].(string)
	if id == "" {
		t.Fatalf("spawn returned no agent id: %s", resp.Result)
	}

	// The row a failed spawn leaves behind: error, live socket path, no restart yet.
	if err := s.AgentUpdate(id, map[string]any{
		"status":    "error",
		"sock_path": filepath.Join(shortSockDir(t), "restart-chain.sock"),
	}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	h.attemptRestart()

	if a := reapedAgent(t, s, id); a.RestartCount != 1 {
		t.Errorf("restart_count = %d, want 1 — the strategy persisted at spawn did not reach attemptRestart", a.RestartCount)
	}
	deadline := time.Now().Add(5 * time.Second)
	for spawn.PID() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if spawn.PID() == 0 {
		t.Fatal("no backend process was launched for the restart")
	}
}

// --- Consequence review: a stop must end the process, in the cascade too ---

// An orphan stop closed the child's row and left its process working with no
// relay, no limit and no owner: the same defect YM-66 names, one level down.
func TestDetectOrphanedAgents_EndsTheOrphanProcess(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	child, exited := startBystander(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	now := time.Now().Format("2006-01-02 15:04:05")
	past := time.Now().Add(-6 * time.Minute).Format("2006-01-02 15:04:05")

	if err := s.AgentCreate(storage.Agent{
		ID: "orphan-parent", SessionID: "sess-orphan-parent",
		Project: "p", Section: "parent", Status: "finished", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AgentCreate(parent): %v", err)
	}
	if err := s.AgentCreate(storage.Agent{
		ID: "orphan-proc", SessionID: "sess-orphan-proc",
		Project: "p", Section: "child", Status: "running",
		PID: child.Process.Pid, Backend: "opencode",
		CallerSession: "sess-orphan-parent", LivenessPingAt: past, CreatedAt: now,
	}); err != nil {
		t.Fatalf("AgentCreate(child): %v", err)
	}

	h.detectOrphanedAgents()

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("orphan process still running after its row was closed")
	}
	if a := reapedAgent(t, s, "orphan-proc"); a.Status != "stopped" {
		t.Errorf("status = %q, want stopped", a.Status)
	}
}

// stop_agent cascades the supervision tree. The cascade wrote the children's rows
// and left their processes running, so a stopped orchestrator left live children
// that nothing relayed to and no limit watched.
func TestHandleStopAgent_CascadeEndsChildProcesses(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	child, exited := startBystander(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)
	now := time.Now().Format("2006-01-02 15:04:05")

	if err := s.AgentCreate(storage.Agent{
		ID: "cascade-parent", SessionID: "sess-cascade-parent",
		Project: "p", Section: "parent", Status: "running",
		PID: deadPID, Backend: "claude", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AgentCreate(parent): %v", err)
	}
	if err := s.AgentCreate(storage.Agent{
		ID: "cascade-child", SessionID: "sess-cascade-child",
		Project: "p", Section: "child", Status: "running",
		PID: child.Process.Pid, Backend: "opencode",
		CallerSession: "sess-cascade-parent", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AgentCreate(child): %v", err)
	}

	if resp := h.handleStopAgent(map[string]any{"to": "cascade-parent"}); resp.Error != "" {
		t.Fatalf("stop_agent: %s", resp.Error)
	}

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("cascaded child process still running after the tree was stopped")
	}
	if a := reapedAgent(t, s, "cascade-child"); a.Status != "stopped" {
		t.Errorf("child status = %q, want stopped", a.Status)
	}
}

// A section whose replacement already finished is not an empty section: telling
// the caller to restart it sends them after work that is done.
func TestCrashRecovery_AbandonedRowWithFinishedSuccessorDoesNotAskForARestart(t *testing.T) {
	h, s := mustHandler(t)

	if err := s.AgentCreate(storage.Agent{
		ID: "abandoned-done", Project: "p", Section: "s-done",
		Status: "crashed", PID: deadPID, RetryCount: 1,
		SessionID: "sess-abandoned-done", CallerSession: "sess-caller-done",
		CreatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate(crashed): %v", err)
	}
	setAgentStamp(t, s, "abandoned-done", "stopped_at", stampAgo(10*time.Minute))
	// The replacement ran and finished inside the grace period.
	if err := s.AgentCreate(storage.Agent{
		ID: "successor-done", Project: "p", Section: "s-done",
		Status: "finished", SessionID: "sess-successor-done",
		CreatedAt: time.Now().Add(-9 * time.Minute).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("AgentCreate(successor): %v", err)
	}

	h.crashRecovery()

	a := reapedAgent(t, s, "abandoned-done")
	if a.Status != "failed" {
		t.Fatalf("status = %q, want failed", a.Status)
	}
	if !strings.Contains(a.Error, "successor-done") {
		t.Errorf("error = %q, want the successor named", a.Error)
	}
	msgs, err := s.GetUnnotifiedMessages([]string{"sess-caller-done"})
	if err != nil {
		t.Fatalf("GetUnnotifiedMessages: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "AGENT_ABANDONED") {
			t.Errorf("caller was told to restart a section whose replacement finished: %q", m.Content)
		}
	}
}

// A successful write into the inject socket is not proof that the agent stopped:
// a TUI mid-turn reads the request only at the end of its turn. The process has to
// end anyway — a row that says stopped while the agent keeps working is the
// invisible agent YM-66 is about, and no guard looks at closed rows.
func TestHandleStopAgent_EndsTheProcessEvenWhenTheInjectWriteSucceeds(t *testing.T) {
	requireProcTable(t)
	h, s := mustHandler(t)
	child, exited := startBystander(t)
	pinProcessTable(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	sockDir := shortSockDir(t)
	h.dataDir = sockDir
	sockPath := filepath.Join(sockDir, "inject-ok.sock")
	ln, err := net.Listen("unix", sockPath+".inject")
	if err != nil {
		t.Fatalf("listen on the inject socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan struct{})
	go func() {
		if conn, err := ln.Accept(); err == nil {
			conn.Close()
			close(accepted)
		}
	}()

	if err := s.AgentCreate(storage.Agent{
		ID: "inject-ok", Project: "p", Section: "s-inject",
		Status: "running", PID: child.Process.Pid, Backend: "opencode",
		// A foreign column proves the stop does not dial it (YM-338).
		SockPath:  filepath.Join(shortSockDir(t), "victim.sock"),
		SessionID: "sess-inject-ok",
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	if resp := h.handleStopAgent(map[string]any{"to": "inject-ok"}); resp.Error != "" {
		t.Fatalf("stop_agent: %s", resp.Error)
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("the graceful exit was never requested")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after a stop whose inject write succeeded")
	}
}

// The daemon is the only writer that may ask for the unbounded policy, and
// spawn_agent refuses it, so a replacement asks for the bounded one.
func TestRespawnRestartStrategy_BoundsTheUnboundedPolicy(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, // an empty strategy is the spawn default, mapped there
		{"temporary", "temporary"},
		{"transient", "transient"},
		{"permanent", "transient"},
	} {
		if got := respawnRestartStrategy(tc.in); got != tc.want {
			t.Errorf("respawnRestartStrategy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
