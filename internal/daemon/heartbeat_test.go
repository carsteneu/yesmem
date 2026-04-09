package daemon

import (
	"os"
	"testing"
	"time"

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
	// Push over budget: 600 + 500 = 1100 > 1000
	_ = s.AgentUpdateTelemetry("agent-budget", 1, 600, 500)

	h.enforceAgentLimits()

	a, err := s.AgentGet("agent-budget")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "frozen" {
		t.Errorf("status=%q want frozen", a.Status)
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
	// Under budget: 300 + 200 = 500 < 1000
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
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "live-pid-0", Project: "p", Section: "s2",
		Status: "running", PID: os.Getpid(), CreatedAt: now,
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
	if a.Status != "frozen" {
		t.Errorf("agent at max_restarts should be frozen, status=%q", a.Status)
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
		CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("race-0")
	// Should not attempt restart (no status change, no freeze)
	if a.Status != "error" {
		t.Errorf("race guard should prevent restart, status=%q", a.Status)
	}
}

func TestAttemptRestart_SkipsEmptySockPath(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "no-sock-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "transient", MaxRestarts: 3,
		CreatedAt: now,
		// SockPath intentionally empty
	})
	h.attemptRestart()
	a, _ := s.AgentGet("no-sock-0")
	if a.Status != "error" {
		t.Errorf("agent with empty SockPath should not be restarted, status=%q", a.Status)
	}
}

func TestAttemptRestart_PermanentIgnoresMaxRestarts(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	s.AgentCreate(storage.Agent{
		ID: "perm-0", Project: "p", Section: "s",
		Status: "error", PID: 99999999,
		RestartStrategy: "permanent", RestartCount: 3, MaxRestarts: 3,
		SockPath: "/tmp/perm-test-sock",
		CreatedAt: now,
	})
	h.attemptRestart()
	a, _ := s.AgentGet("perm-0")
	if a.Status == "frozen" {
		t.Errorf("permanent agent should never be frozen, got frozen after max_restarts")
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
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "crash-alive-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), CreatedAt: now,
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
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

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
		Status: "running", CallerSession: "sess-e2e-orch", CreatedAt: now,
	})
	s.AgentCreate(storage.Agent{
		ID: "e2e-child-2", SessionID: "sess-e2e-c2",
		Project: "p", Section: "c2",
		Status: "running", CallerSession: "sess-e2e-orch", CreatedAt: now,
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

func TestDetectHungAgents_StaleHeartbeat(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "hung-stale-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), CreatedAt: now,
	})
	stale := time.Now().Add(-15 * time.Minute).Format(time.RFC3339)
	s.AgentUpdate("hung-stale-0", map[string]any{"heartbeat_at": stale})

	h.detectHungAgents()

	a, _ := s.AgentGet("hung-stale-0")
	if a.Status != "frozen" {
		t.Errorf("status=%q want frozen for hung agent", a.Status)
	}
}

func TestDetectHungAgents_FreshHeartbeat_NoChange(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "hung-fresh-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), CreatedAt: now,
	})
	fresh := time.Now().Format(time.RFC3339)
	s.AgentUpdate("hung-fresh-0", map[string]any{"heartbeat_at": fresh})

	h.detectHungAgents()

	a, _ := s.AgentGet("hung-fresh-0")
	if a.Status != "running" {
		t.Errorf("status=%q want running for fresh heartbeat", a.Status)
	}
}

func TestDetectHungAgents_NoHeartbeat_Skipped(t *testing.T) {
	h, s := mustHandler(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	s.AgentCreate(storage.Agent{
		ID: "hung-none-0", Project: "p", Section: "s",
		Status: "running", PID: os.Getpid(), CreatedAt: now,
	})

	h.detectHungAgents()

	a, _ := s.AgentGet("hung-none-0")
	if a.Status != "running" {
		t.Errorf("status=%q want running (no heartbeat yet = skip)", a.Status)
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
	if got != 30*time.Minute {
		t.Errorf("getAgentMaxRuntime() = %v, want 30m", got)
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
	if got != 30*time.Minute {
		t.Errorf("getAgentMaxRuntime(0) = %v, want 30m default", got)
	}
}

func TestGetAgentMaxRuntime_NegativeMeansDefault(t *testing.T) {
	h, _ := mustHandler(t)
	h.agentMaxRuntime = -5 * time.Minute
	got := h.getAgentMaxRuntime()
	if got != 30*time.Minute {
		t.Errorf("getAgentMaxRuntime(negative) = %v, want 30m default", got)
	}
}
