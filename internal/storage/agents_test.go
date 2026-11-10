package storage

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentCreate_And_Get(t *testing.T) {
	s := mustOpen(t)

	agent := Agent{
		ID:            "agent-0",
		Project:       "test-proj",
		Section:       "research",
		SessionID:     "abc-123",
		Status:        "pending",
		CallerSession: "session-xyz",
	}

	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	got, err := s.AgentGet("agent-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if got.Project != "test-proj" {
		t.Errorf("expected project='test-proj', got %q", got.Project)
	}
	if got.Section != "research" {
		t.Errorf("expected section='research', got %q", got.Section)
	}
	if got.SessionID != "abc-123" {
		t.Errorf("expected session_id='abc-123', got %q", got.SessionID)
	}
	if got.Status != "pending" {
		t.Errorf("expected status='pending', got %q", got.Status)
	}
}

// TestAgentCreate_CreatedAtUsesRFC3339WithOffset verifies that AgentCreate stores
// created_at as RFC3339 with timezone offset (e.g. "2026-06-18T22:58:53+02:00"),
// NOT as a naked SQLite datetime('now','localtime') string. modernc-sqlite's
// pure-Go localtime resolution is broken and returns UTC, which caused the
// agent-230 misdiagnosis in learning #76675 (created_at read as UTC, interpreted
// as local, 36min real runtime mistaken for 2.5h).
func TestAgentCreate_CreatedAtUsesRFC3339WithOffset(t *testing.T) {
	s := mustOpen(t)

	agent := Agent{
		ID:      "agent-tz-0",
		Project: "p",
		Section: "s",
		Status:  "running",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	got, err := s.AgentGet("agent-tz-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	if got.CreatedAt == "" {
		t.Fatal("CreatedAt is empty — expected RFC3339 timestamp")
	}

	// RFC3339 requires a timezone offset: "+02:00", "-05:00", or "Z" (UTC).
	// A naked "YYYY-MM-DD HH:MM:SS" (no T, no offset) means the schema
	// default fired instead of Go's time.Now() — that's the bug.
	if !strings.Contains(got.CreatedAt, "T") {
		t.Errorf("CreatedAt %q: missing 'T' separator — not RFC3339. Likely schema-default fired.", got.CreatedAt)
	}
	if !strings.ContainsAny(got.CreatedAt, "+Z") || !strings.Contains(got.CreatedAt, ":") {
		t.Errorf("CreatedAt %q: missing timezone offset — RFC3339 requires +HH:MM or Z.", got.CreatedAt)
	}

	// Must be parseable as RFC3339.
	if _, err := time.Parse(time.RFC3339, got.CreatedAt); err != nil {
		t.Errorf("CreatedAt %q is not valid RFC3339: %v", got.CreatedAt, err)
	}
}

func TestAgentUpdate(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec", Status: "pending"})

	err := s.AgentUpdate("agent-0", map[string]any{
		"status":    "running",
		"pid":       12345,
		"sock_path": "/tmp/agent-0.sock",
	})
	if err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	got, _ := s.AgentGet("agent-0")
	if got.Status != "running" {
		t.Errorf("expected status='running', got %q", got.Status)
	}
	if got.PID != 12345 {
		t.Errorf("expected pid=12345, got %d", got.PID)
	}
	if got.SockPath != "/tmp/agent-0.sock" {
		t.Errorf("expected sock_path, got %q", got.SockPath)
	}
}

func TestAgentGetBySection(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "legal", Status: "finished"})
	// Push agent-0's timestamp back so agent-1 is clearly newer
	s.db.Exec("UPDATE agents SET created_at = datetime('now', '-10 seconds') WHERE id = 'agent-0'")
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "legal", Status: "running"})

	got, err := s.AgentGetBySection("proj", "legal")
	if err != nil {
		t.Fatalf("AgentGetBySection: %v", err)
	}
	if got.ID != "agent-1" {
		t.Errorf("expected agent-1 (most recent), got %q", got.ID)
	}
}

func TestAgentGetActiveBySection(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "legal", Status: "finished"})
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "legal", Status: "stopped"})

	got, err := s.AgentGetActiveBySection("proj", "legal")
	if err == nil && got != nil {
		t.Fatalf("expected no active agent, got %s", got.ID)
	}

	s.AgentCreate(Agent{ID: "agent-2", Project: "proj", Section: "legal", Status: "running"})
	got, err = s.AgentGetActiveBySection("proj", "legal")
	if err != nil {
		t.Fatalf("AgentGetActiveBySection: %v", err)
	}
	if got == nil || got.ID != "agent-2" {
		t.Fatalf("expected active agent-2, got %#v", got)
	}
}

func TestAgentGetActiveBySectionReturnsNilAfterStop(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "legal", Status: "finished"})
	s.db.Exec("UPDATE agents SET created_at = datetime('now', '-10 seconds') WHERE id = 'agent-0'")
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "legal", Status: "stopped"})
	s.db.Exec("UPDATE agents SET created_at = datetime('now', '-5 seconds') WHERE id = 'agent-1'")
	s.AgentCreate(Agent{ID: "agent-2", Project: "proj", Section: "legal", Status: "running"})

	got, err := s.AgentGetActiveBySection("proj", "legal")
	if err != nil {
		t.Fatalf("AgentGetActiveBySection: %v", err)
	}
	if got.ID != "agent-2" {
		t.Errorf("expected active agent-2, got %q", got.ID)
	}

	s.AgentUpdate("agent-2", map[string]any{"status": "stopped"})
	got, err = s.AgentGetActiveBySection("proj", "legal")
	if err == nil && got != nil {
		t.Fatalf("expected no active agent, got %q", got.ID)
	}
}

func TestAgentGetAnyBySession(t *testing.T) {
	s := mustOpen(t)

	if err := s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "legal", SessionID: "sess-1", Status: "stopped"}); err != nil {
		t.Fatalf("AgentCreate stopped: %v", err)
	}
	if err := s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "legal-2", SessionID: "sess-2", Status: "running"}); err != nil {
		t.Fatalf("AgentCreate running: %v", err)
	}

	got, err := s.AgentGetAnyBySession("sess-1")
	if err != nil {
		t.Fatalf("AgentGetAnyBySession: %v", err)
	}
	if got.ID != "agent-0" {
		t.Fatalf("expected stopped agent-0, got %q", got.ID)
	}
	if got.Status != "stopped" {
		t.Fatalf("expected stopped status, got %q", got.Status)
	}
}

func TestAgentList(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj-a", Section: "sec1", Status: "running"})
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj-b", Section: "sec2", Status: "running"})
	s.AgentCreate(Agent{ID: "agent-2", Project: "proj-a", Section: "sec3", Status: "stopped"})

	// All agents
	all, err := s.AgentList("")
	if err != nil {
		t.Fatalf("AgentList all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 agents, got %d", len(all))
	}

	// Filter by project
	projA, err := s.AgentList("proj-a")
	if err != nil {
		t.Fatalf("AgentList proj-a: %v", err)
	}
	if len(projA) != 2 {
		t.Errorf("expected 2 agents for proj-a, got %d", len(projA))
	}
}

func TestAgentDeleteOrphaned(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec1", Status: "running"})
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "sec2", Status: "pending"})
	s.AgentCreate(Agent{ID: "agent-2", Project: "proj", Section: "sec3", Status: "finished"})

	n, err := s.AgentDeleteOrphaned()
	if err != nil {
		t.Fatalf("AgentDeleteOrphaned: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 agents deleted, got %d", n)
	}

	// Verify running/pending are gone
	_, err = s.AgentGet("agent-0")
	if err == nil {
		t.Error("agent-0 should be deleted")
	}
	_, err = s.AgentGet("agent-1")
	if err == nil {
		t.Error("agent-1 should be deleted")
	}

	// finished agent should still exist
	a2, err := s.AgentGet("agent-2")
	if err != nil {
		t.Fatalf("agent-2 should still exist: %v", err)
	}
	if a2.Status != "finished" {
		t.Errorf("agent-2: expected finished, got %q", a2.Status)
	}
}

func TestAgentNextID(t *testing.T) {
	s := mustOpen(t)

	today := time.Now().UTC().Format("20060102")
	first := "agent-" + today + "-01"
	second := "agent-" + today + "-02"

	// No agents yet
	id, err := s.AgentNextID("proj")
	if err != nil {
		t.Fatalf("AgentNextID: %v", err)
	}
	if id != first {
		t.Errorf("expected %q, got %q", first, id)
	}

	// After creating some (mixed old/new format — only today's new-format IDs count)
	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec1", Status: "running"})
	s.AgentCreate(Agent{ID: first, Project: "proj", Section: "sec2", Status: "running"})

	id, _ = s.AgentNextID("proj")
	if id != second {
		t.Errorf("expected %q, got %q", second, id)
	}
}

func TestAgentDelete(t *testing.T) {
	s := mustOpen(t)

	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec", Status: "running"})
	if err := s.AgentDelete("agent-0"); err != nil {
		t.Fatalf("AgentDelete: %v", err)
	}

	_, err := s.AgentGet("agent-0")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}
}

func TestAgentUpdateTelemetry(t *testing.T) {
	s := mustOpen(t)
	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec", Status: "running"})

	err := s.AgentUpdateTelemetry("agent-0", 5, 1200, 400)
	if err != nil {
		t.Fatalf("AgentUpdateTelemetry: %v", err)
	}

	a, err := s.AgentGetBySection("proj", "sec")
	if err != nil {
		t.Fatalf("AgentGetBySection: %v", err)
	}
	if a.TurnsUsed != 5 {
		t.Errorf("TurnsUsed = %d, want 5", a.TurnsUsed)
	}
	if a.InputTokens != 1200 {
		t.Errorf("InputTokens = %d, want 1200", a.InputTokens)
	}
	if a.OutputTokens != 400 {
		t.Errorf("OutputTokens = %d, want 400", a.OutputTokens)
	}
	if a.LastActivityAt == "" {
		t.Error("LastActivityAt is empty")
	}
}

func TestAgentTelemetryMigration(t *testing.T) {
	s := mustOpen(t)
	// Drop new columns to simulate old schema
	s.db.Exec(`ALTER TABLE agents DROP COLUMN turns_used`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN input_tokens`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN output_tokens`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN last_activity_at`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN phase`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN restart_strategy`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN restart_count`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN max_restarts`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN liveness_ping_at`)
	s.db.Exec(`ALTER TABLE agents DROP COLUMN last_restart_at`)

	if err := s.MigrateAgentsSchema(); err != nil {
		t.Fatalf("MigrateAgentsSchema: %v", err)
	}
	s.AgentCreate(Agent{ID: "agent-0", Project: "p", Section: "s", Status: "running"})
	if err := s.AgentUpdateTelemetry("agent-0", 1, 100, 50); err != nil {
		t.Fatalf("AgentUpdateTelemetry after migration: %v", err)
	}
}

// TestAgentWorkDirMigration: work_dir arrived with the opencode bind fix. On an
// existing store the column has to be added before AgentCreate can persist it,
// and a second migration run must be a no-op (the daemon migrates on every
// start).
func TestAgentWorkDirMigration(t *testing.T) {
	s := mustOpen(t)
	if _, err := s.db.Exec(`ALTER TABLE agents DROP COLUMN work_dir`); err != nil {
		t.Fatalf("simulate old schema: %v", err)
	}
	// An old store cannot even write the field.
	if err := s.AgentCreate(Agent{ID: "pre", Project: "p", Section: "s0", Status: "running", WorkDir: "/wd/pre"}); err == nil {
		t.Fatal("expected a failure writing work_dir into a schema without the column")
	}

	if err := s.MigrateAgentsSchema(); err != nil {
		t.Fatalf("MigrateAgentsSchema: %v", err)
	}
	if err := s.AgentCreate(Agent{ID: "agent-0", Project: "p", Section: "s", Status: "running", WorkDir: "/wd/loop"}); err != nil {
		t.Fatalf("AgentCreate after migration: %v", err)
	}
	a, err := s.AgentGet("agent-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.WorkDir != "/wd/loop" {
		t.Errorf("WorkDir = %q, want /wd/loop", a.WorkDir)
	}
	// An empty value stays empty rather than NULL (the SELECTs COALESCE it).
	if err := s.AgentCreate(Agent{ID: "agent-1", Project: "p", Section: "s1", Status: "running"}); err != nil {
		t.Fatalf("AgentCreate without work_dir: %v", err)
	}
	if b, _ := s.AgentGet("agent-1"); b.WorkDir != "" {
		t.Errorf("WorkDir = %q, want empty", b.WorkDir)
	}

	// The daemon migrates on every start: a second run must not fail.
	if err := s.MigrateAgentsSchema(); err != nil {
		t.Fatalf("second MigrateAgentsSchema: %v", err)
	}
	if a, err = s.AgentGet("agent-0"); err != nil || a.WorkDir != "/wd/loop" {
		t.Errorf("work_dir changed across a migration re-run: %q / %v", a.WorkDir, err)
	}
}

func TestAgentTelemetryAccumulates(t *testing.T) {
	s := mustOpen(t)
	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec", Status: "running"})

	s.AgentUpdateTelemetry("agent-0", 3, 500, 200)
	s.AgentUpdateTelemetry("agent-0", 2, 300, 100)

	a, _ := s.AgentGetBySection("proj", "sec")
	if a.TurnsUsed != 5 {
		t.Errorf("TurnsUsed accumulated = %d, want 5", a.TurnsUsed)
	}
	if a.InputTokens != 800 {
		t.Errorf("InputTokens accumulated = %d, want 800", a.InputTokens)
	}
	if a.OutputTokens != 300 {
		t.Errorf("OutputTokens accumulated = %d, want 300", a.OutputTokens)
	}
}

func TestAgentSchemaSupervision(t *testing.T) {
	s := mustOpen(t)

	err := s.AgentCreate(Agent{
		ID: "agent-sup-0", Project: "p", Section: "s",
		Status: "running", CreatedAt: "2006-01-02 15:04:05",
		RestartStrategy: "transient", MaxRestarts: 3,
	})
	if err != nil {
		t.Fatalf("AgentCreate with supervision fields: %v", err)
	}

	a, err := s.AgentGet("agent-sup-0")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.RestartStrategy != "transient" {
		t.Errorf("RestartStrategy=%q want transient", a.RestartStrategy)
	}
	if a.MaxRestarts != 3 {
		t.Errorf("MaxRestarts=%d want 3", a.MaxRestarts)
	}
	if a.LivenessPingAt != "" {
		t.Errorf("LivenessPingAt should be empty initially")
	}
}

func TestAgentCascadeStop(t *testing.T) {
	s := mustOpen(t)
	now := "2006-01-02 15:04:05"

	s.AgentCreate(Agent{ID: "orch-0", SessionID: "sess-orch", Project: "p", Section: "orch", Status: "running", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-a", SessionID: "sess-a", Project: "p", Section: "a", Status: "running", CallerSession: "sess-orch", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-b", SessionID: "sess-b", Project: "p", Section: "b", Status: "running", CallerSession: "sess-orch", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-sub", SessionID: "sess-sub", Project: "p", Section: "sub", Status: "running", CallerSession: "sess-a", CreatedAt: now})

	stopped, err := s.AgentCascadeStop("sess-orch")
	if err != nil {
		t.Fatalf("AgentCascadeStop: %v", err)
	}
	if len(stopped) != 3 {
		t.Errorf("stopped=%d want 3 (sub-a, sub-b, sub-sub)", len(stopped))
	}

	orch, _ := s.AgentGet("orch-0")
	if orch.Status != "running" {
		t.Errorf("orchestrator should not be stopped by cascade, got %q", orch.Status)
	}
	subA, _ := s.AgentGet("sub-a")
	if subA.Status != "stopped" {
		t.Errorf("sub-a status=%q want stopped", subA.Status)
	}
	subSub, _ := s.AgentGet("sub-sub")
	if subSub.Status != "stopped" {
		t.Errorf("sub-sub status=%q want stopped", subSub.Status)
	}
}

func TestAgentCascadeStop_StopsPendingChild(t *testing.T) {
	s := mustOpen(t)
	now := "2006-01-02 15:04:05"

	s.AgentCreate(Agent{ID: "orch-p", SessionID: "sess-orch-p", Project: "p", Section: "orch", Status: "running", CreatedAt: now})
	s.AgentCreate(Agent{ID: "pending-child", SessionID: "sess-pending", Project: "p", Section: "pc", Status: "pending", CallerSession: "sess-orch-p", CreatedAt: now})

	stopped, err := s.AgentCascadeStop("sess-orch-p")
	if err != nil {
		t.Fatalf("AgentCascadeStop: %v", err)
	}
	if len(stopped) != 1 {
		t.Errorf("stopped=%d want 1 (pending child)", len(stopped))
	}

	child, _ := s.AgentGet("pending-child")
	if child.Status != "stopped" {
		t.Errorf("pending child status=%q want stopped", child.Status)
	}
}

func TestAgentGetByProxyThreadID(t *testing.T) {
	s := mustOpen(t)
	s.AgentCreate(Agent{ID: "agent-pt-1", Project: "proj", Section: "sec", SessionID: "uuid-1", Status: "running", ProxyThreadID: "thread-hash-abc"})
	s.AgentCreate(Agent{ID: "agent-pt-2", Project: "proj", Section: "sec2", SessionID: "uuid-2", Status: "running", ProxyThreadID: "thread-hash-def"})

	// Match by proxy_thread_id
	a, err := s.AgentGetByProxyThreadID("thread-hash-abc")
	if err != nil {
		t.Fatalf("AgentGetByProxyThreadID: %v", err)
	}
	if a.ID != "agent-pt-1" {
		t.Errorf("ID = %q, want agent-pt-1", a.ID)
	}

	// Non-match returns nil
	a, err = s.AgentGetByProxyThreadID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent proxy_thread_id")
	}
}

func TestAgentGetRunningInProject(t *testing.T) {
	s := mustOpen(t)
	// Agent without proxy_thread_id — should be found
	s.AgentCreate(Agent{ID: "agent-rp-1", Project: "proj", Section: "sec", SessionID: "uuid-1", Status: "running"})
	// Agent with proxy_thread_id — should NOT be found
	s.AgentCreate(Agent{ID: "agent-rp-2", Project: "proj", Section: "sec2", SessionID: "uuid-2", Status: "running", ProxyThreadID: "already-mapped"})

	a, err := s.AgentGetRunningInProject("proj")
	if err != nil {
		t.Fatalf("AgentGetRunningInProject: %v", err)
	}
	if a.ID != "agent-rp-1" {
		t.Errorf("ID = %q, want agent-rp-1 (unmapped running agent)", a.ID)
	}

	// No unmapped agent in a project with only mapped agents
	_, err = s.AgentGetRunningInProject("nonexistent")
	if err == nil {
		t.Error("expected error when no agents in project")
	}
}

func TestAgentUpdateProxyThreadID(t *testing.T) {
	s := mustOpen(t)
	s.AgentCreate(Agent{ID: "agent-upd", Project: "proj", Section: "sec", SessionID: "uuid-1", Status: "running"})

	err := s.AgentUpdate("agent-upd", map[string]any{"proxy_thread_id": "new-thread-hash"})
	if err != nil {
		t.Fatalf("AgentUpdate proxy_thread_id: %v", err)
	}

	a, _ := s.AgentGet("agent-upd")
	if a.ProxyThreadID != "new-thread-hash" {
		t.Errorf("ProxyThreadID = %q, want new-thread-hash", a.ProxyThreadID)
	}
}

// --- Legacy "frozen" status normalization on read ---

func TestAgentGet_NormalizesLegacyFrozenStatus(t *testing.T) {
	s := mustOpen(t)
	// Insert raw "frozen" status (legacy DB row from before the paused/stopped split)
	if err := s.AgentCreate(Agent{
		ID: "legacy-frozen", Project: "p", Section: "s", Status: "frozen",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	a, err := s.AgentGet("legacy-frozen")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.Status != "paused" {
		t.Errorf("legacy frozen status should normalize to paused, got %q", a.Status)
	}
}

func TestAgentList_NormalizesLegacyFrozenStatus(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "list-frozen", Project: "p", Section: "s", Status: "frozen",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	agents, err := s.AgentList("p")
	if err != nil {
		t.Fatalf("AgentList: %v", err)
	}
	for _, a := range agents {
		if a.ID == "list-frozen" && a.Status != "paused" {
			t.Errorf("legacy frozen in list should normalize to paused, got %q", a.Status)
		}
	}
}

// --- Backend-session lookup (opencode_session_id / codex_session_id) ---
// whoami() resolves the caller via AgentGetBySession / AgentGetAnyBySession.
// Spawned opencode/codex agents carry a synthetic session_id (never matches)
// and persist their real backend session in opencode_session_id / codex_session_id.
// The lookup MUST fall back to those columns or whoami returns is_agent=false.

func TestAgentGetBySession_MatchesOpenCodeSessionID(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-oc", Project: "proj", Section: "sec",
		SessionID: "synthetic-uuid-1", OpencodeSessionID: "ses_real_oc_1",
		Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// Bare lookup (no prefix)
	got, err := s.AgentGetBySession("ses_real_oc_1")
	if err != nil {
		t.Fatalf("AgentGetBySession(bare opencode id): %v", err)
	}
	if got.ID != "agent-oc" {
		t.Errorf("bare: expected agent-oc, got %q", got.ID)
	}
	// Prefixed lookup (storage-layer stripAgentPrefix boundary contract)
	got, err = s.AgentGetBySession("opencode:ses_real_oc_1")
	if err != nil {
		t.Fatalf("AgentGetBySession(prefixed opencode id): %v", err)
	}
	if got.ID != "agent-oc" {
		t.Errorf("prefixed: expected agent-oc, got %q", got.ID)
	}
}

func TestAgentGetBySession_MatchesCodexSessionID(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-cx", Project: "proj", Section: "sec",
		SessionID: "synthetic-uuid-2", CodexSessionID: "ses_real_cx_2",
		Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	got, err := s.AgentGetBySession("ses_real_cx_2")
	if err != nil {
		t.Fatalf("AgentGetBySession(codex id): %v", err)
	}
	if got.ID != "agent-cx" {
		t.Errorf("expected agent-cx, got %q", got.ID)
	}
}

func TestAgentGetAnyBySession_MatchesOpenCodeSessionID(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-oc-any", Project: "proj", Section: "sec",
		SessionID: "synthetic-uuid-3", OpencodeSessionID: "ses_real_oc_3",
		Status: "stopped",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// Bare lookup
	got, err := s.AgentGetAnyBySession("ses_real_oc_3")
	if err != nil {
		t.Fatalf("AgentGetAnyBySession(bare opencode id): %v", err)
	}
	if got.ID != "agent-oc-any" {
		t.Errorf("bare: expected agent-oc-any, got %q", got.ID)
	}
	// Prefixed lookup (storage-layer stripAgentPrefix boundary contract)
	got, err = s.AgentGetAnyBySession("opencode:ses_real_oc_3")
	if err != nil {
		t.Fatalf("AgentGetAnyBySession(prefixed opencode id): %v", err)
	}
	if got.ID != "agent-oc-any" {
		t.Errorf("prefixed: expected agent-oc-any, got %q", got.ID)
	}
}

func TestStripAgentPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"opencode:ses_abc", "ses_abc"},
		{"codex:thread_xyz", "thread_xyz"},
		{"plain-sess", "plain-sess"},
		{"opencode:", ""},
		{"", ""},
		{"opencode:ses_with:colon", "ses_with:colon"},
	}
	for _, c := range cases {
		got := stripAgentPrefix(c.in)
		if got != c.want {
			t.Errorf("stripAgentPrefix(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgentGetAnyBySession_MatchesCodexSessionID(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-cx-any", Project: "proj", Section: "sec",
		SessionID: "synthetic-uuid-4", CodexSessionID: "ses_real_cx_4",
		Status: "stopped",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	got, err := s.AgentGetAnyBySession("ses_real_cx_4")
	if err != nil {
		t.Fatalf("AgentGetAnyBySession(codex id): %v", err)
	}
	if got.ID != "agent-cx-any" {
		t.Errorf("expected agent-cx-any, got %q", got.ID)
	}
}

// --- AgentGetByPID (PID-based resolution for parallel-safe whoami) ---
// resolveSessionID in internal/daemon/handler_dialogs.go falls back to the
// _caller_pid sent by the MCP wrapper (= os.Getppid() = backend PID). The
// agents.pid column stores bridge.Cmd.Process.Pid which equals the spawned
// backend's PID. Restricted to status='running' so PID reuse after process
// death cannot resurrect a stale mapping.

func TestAgentGetByPID(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-pid", Project: "proj-pid", Section: "sec-pid",
		SessionID: "synthetic-pid-1", PID: 4242,
		Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	got, err := s.AgentGetByPID(4242)
	if err != nil {
		t.Fatalf("AgentGetByPID: %v", err)
	}
	if got.ID != "agent-pid" {
		t.Errorf("expected agent-pid, got %q", got.ID)
	}
	if got.SessionID != "synthetic-pid-1" {
		t.Errorf("expected synthetic-pid-1, got %q", got.SessionID)
	}
}

func TestAgentGetByPID_SkipsStoppedAgents(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{
		ID: "agent-pid-stopped", Project: "proj", Section: "sec",
		SessionID: "sess-stopped", PID: 7373,
		Status: "stopped",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	got, err := s.AgentGetByPID(7373)
	if err == nil && got != nil {
		t.Errorf("expected no match for stopped agent (PID reuse protection), got %q", got.ID)
	}
}

func TestAgentGetByPID_NoMatch(t *testing.T) {
	s := mustOpen(t)
	got, err := s.AgentGetByPID(99999)
	if err == nil && got != nil {
		t.Errorf("expected no match for unknown PID, got %q", got.ID)
	}
}

// --- C5: legacy "frozen" rows are closed by the migration ---

// TestMigrateAgentsSchema_ClosesLegacyFrozenRows pins the one-time
// normalisation of the pre-2026-07-03 "frozen" status. normalizeAgentStatus
// reads "frozen" as "paused", so those rows were treated as live agents: their
// stale PID was signalable by stop_agent and the paused-row guards (permission
// kick, DONE-guard) kept watching them.
func TestMigrateAgentsSchema_ClosesLegacyFrozenRows(t *testing.T) {
	s := mustOpen(t)

	// A frozen row as an old daemon left it: stale PID, stale socket path, no
	// stopped_at.
	if _, err := s.db.Exec(`INSERT INTO agents (id, project, section, status, pid, sock_path, created_at)
		VALUES ('agent-frozen', 'p', 'sec-frozen', 'frozen', 4242, '/tmp/frozen.sock', '2026-05-01T10:00:00+02:00')`); err != nil {
		t.Fatalf("insert frozen row: %v", err)
	}
	// A frozen row that already carries a stopped_at — the migration must keep
	// the original timestamp instead of overwriting it.
	if _, err := s.db.Exec(`INSERT INTO agents (id, project, section, status, pid, sock_path, stopped_at, created_at)
		VALUES ('agent-frozen-stamped', 'p', 'sec-frozen-2', 'frozen', 4243, '/tmp/frozen2.sock', '2026-06-01T10:00:00+02:00', '2026-05-01T10:00:00+02:00')`); err != nil {
		t.Fatalf("insert stamped frozen row: %v", err)
	}
	// A frozen row created after the cutoff: no binary in this tree emits
	// "frozen" any more, so this row cannot be ours to close. The bound keeps the
	// destructive clause from outliving its precondition.
	if _, err := s.db.Exec(`INSERT INTO agents (id, project, section, status, pid, sock_path, created_at)
		VALUES ('agent-frozen-new', 'p', 'sec-frozen-3', 'frozen', 4244, '/tmp/frozen3.sock', '2026-09-01T10:00:00+02:00')`); err != nil {
		t.Fatalf("insert post-cutoff frozen row: %v", err)
	}

	// A frozen row with no usable stamp: created_at has a default and every
	// writer sets it, so this shape is hand-made — and leaving it "frozen" would
	// keep the exact state this migration exists to remove.
	if _, err := s.db.Exec(`INSERT INTO agents (id, project, section, status, pid, sock_path, created_at)
		VALUES ('agent-frozen-undated', 'p', 'sec-frozen-4', 'frozen', 4245, '/tmp/frozen4.sock', '')`); err != nil {
		t.Fatalf("insert undated frozen row: %v", err)
	}

	if err := s.MigrateAgentsSchema(); err != nil {
		t.Fatalf("MigrateAgentsSchema: %v", err)
	}

	a, err := s.AgentGet("agent-frozen")
	if err != nil {
		t.Fatalf("AgentGet(agent-frozen): %v", err)
	}
	if a.Status != "stopped" {
		t.Errorf("status = %q, want stopped", a.Status)
	}
	if a.PID != 0 {
		t.Errorf("pid = %d, want 0 (stale PID cleared)", a.PID)
	}
	if a.SockPath != "" {
		t.Errorf("sock_path = %q, want empty (stale socket cleared)", a.SockPath)
	}
	if a.Progress != "migrated: legacy frozen" {
		t.Errorf("progress = %q, want %q", a.Progress, "migrated: legacy frozen")
	}
	if a.StoppedAt == "" {
		t.Error("stopped_at not set")
	}

	b, err := s.AgentGet("agent-frozen-stamped")
	if err != nil {
		t.Fatalf("AgentGet(agent-frozen-stamped): %v", err)
	}
	if b.StoppedAt != "2026-06-01T10:00:00+02:00" {
		t.Errorf("stopped_at = %q, want the original timestamp preserved", b.StoppedAt)
	}

	var frozen int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE status='frozen'`).Scan(&frozen); err != nil {
		t.Fatalf("count frozen: %v", err)
	}
	if frozen != 1 {
		t.Errorf("%d frozen rows survived the migration, want 1 (only the post-cutoff row)", frozen)
	}
	kept, err := s.AgentGet("agent-frozen-new")
	if err != nil {
		t.Fatalf("AgentGet(agent-frozen-new): %v", err)
	}
	if kept.PID != 4244 || kept.SockPath != "/tmp/frozen3.sock" {
		t.Errorf("post-cutoff row was rewritten: pid=%d sock=%q", kept.PID, kept.SockPath)
	}
	if kept.Status != "paused" {
		t.Errorf("status = %q, want paused (normalizeAgentStatus reads frozen as paused)", kept.Status)
	}
	undated, err := s.AgentGet("agent-frozen-undated")
	if err != nil {
		t.Fatalf("AgentGet(agent-frozen-undated): %v", err)
	}
	if undated.Status != "stopped" || undated.PID != 0 {
		t.Errorf("undated frozen row: status=%q pid=%d, want stopped/0", undated.Status, undated.PID)
	}

	// Second run is a no-op: the WHERE clause finds nothing, so the stamps and
	// the progress line stay exactly as the first run left them.
	if err := s.MigrateAgentsSchema(); err != nil {
		t.Fatalf("MigrateAgentsSchema (idempotent rerun): %v", err)
	}
	again, err := s.AgentGet("agent-frozen")
	if err != nil {
		t.Fatalf("AgentGet after rerun: %v", err)
	}
	if again.StoppedAt != a.StoppedAt || again.Progress != a.Progress || again.Status != a.Status {
		t.Errorf("second run changed the row: stopped_at %q->%q, progress %q->%q, status %q->%q",
			a.StoppedAt, again.StoppedAt, a.Progress, again.Progress, a.Status, again.Status)
	}
}

// normalizeAgentStatus stays as the read-side safety net for rows that appear
// outside a migration run (a fixture, an older binary writing to the same DB).
func TestNormalizeAgentStatus_KeepsFrozenMapping(t *testing.T) {
	if got := normalizeAgentStatus("frozen"); got != "paused" {
		t.Errorf("normalizeAgentStatus(frozen) = %q, want paused", got)
	}
	if got := normalizeAgentStatus("stopped"); got != "stopped" {
		t.Errorf("normalizeAgentStatus(stopped) = %q, want stopped", got)
	}
}

// --- AgentBindSession (atomic backend-session bind) ---

// createAgentForBind stores one agent row and fails the test on error.
func createAgentForBind(t *testing.T, s *Store, id, sid string) {
	t.Helper()
	if err := s.AgentCreate(Agent{
		ID: id, Project: "p", Section: "s-" + id, SessionID: "uuid-" + id,
		Status: "running", Backend: "opencode", OpencodeSessionID: sid,
	}); err != nil {
		t.Fatalf("AgentCreate(%s): %v", id, err)
	}
}

// TestAgentBindSession_RefusesAnOwnedSession: the ownership test lives in the
// same statement as the write, so no caller can talk it out of the guard.
func TestAgentBindSession_RefusesAnOwnedSession(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "owner", "ses_taken")
	createAgentForBind(t, s, "other", "")

	bound, err := s.AgentBindSession("other", "ses_taken", "")
	if err != nil {
		t.Fatalf("AgentBindSession: %v", err)
	}
	if bound {
		t.Error("a session owned by another row must not be bound")
	}
	if a, _ := s.AgentGet("other"); a.OpencodeSessionID != "" {
		t.Errorf("other's session = %q, want empty", a.OpencodeSessionID)
	}
	if a, _ := s.AgentGet("owner"); a.OpencodeSessionID != "ses_taken" {
		t.Errorf("owner's session = %q, want ses_taken", a.OpencodeSessionID)
	}
}

// TestAgentBindSession_DoesNotReplaceAnExistingSession: a row that already
// carries a session keeps it — a bind may fill a gap, never rewrite history.
// Re-binding the same id stays possible (a retry after a partial failure).
func TestAgentBindSession_DoesNotReplaceAnExistingSession(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "a", "ses_old")

	if bound, err := s.AgentBindSession("a", "ses_new", ""); err != nil || bound {
		t.Fatalf("replacing an existing session must be refused (bound=%v err=%v)", bound, err)
	}
	if a, _ := s.AgentGet("a"); a.OpencodeSessionID != "ses_old" {
		t.Errorf("session = %q, want ses_old", a.OpencodeSessionID)
	}
	if bound, err := s.AgentBindSession("a", "ses_old", ""); err != nil || !bound {
		t.Errorf("rebinding the same id must succeed (bound=%v err=%v)", bound, err)
	}
}

// TestAgentBindSession_ClearsProgress: the bind that clears the unresumable
// marker does it in the same write, so the row is never left claiming the agent
// cannot be resumed while it carries a session.
func TestAgentBindSession_ClearsProgress(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "a", "")
	if err := s.AgentUpdate("a", map[string]any{"progress": "unresumable: no session"}); err != nil {
		t.Fatal(err)
	}

	if bound, err := s.AgentBindSession("a", "ses_new", "unresumable"); err != nil || !bound {
		t.Fatalf("bind with clearProgress: bound=%v err=%v", bound, err)
	}
	a, _ := s.AgentGet("a")
	if a.OpencodeSessionID != "ses_new" || a.Progress != "" {
		t.Errorf("session = %q progress = %q, want ses_new / empty", a.OpencodeSessionID, a.Progress)
	}

	// Without the flag the progress survives.
	createAgentForBind(t, s, "b", "")
	s.AgentUpdate("b", map[string]any{"progress": "Phase 3/6 EXECUTE"})
	if bound, _ := s.AgentBindSession("b", "ses_b", ""); !bound {
		t.Fatal("bind for b was refused")
	}
	if b, _ := s.AgentGet("b"); b.Progress != "Phase 3/6 EXECUTE" {
		t.Errorf("progress = %q, want it untouched", b.Progress)
	}

	// An empty prefix means "leave progress alone", even when the marker is
	// present — the resume path attaches a session without touching it.
	createAgentForBind(t, s, "c", "")
	s.AgentUpdate("c", map[string]any{"progress": "unresumable: no session"})
	if bound, _ := s.AgentBindSession("c", "ses_c", ""); !bound {
		t.Fatal("bind for c was refused")
	}
	if c, _ := s.AgentGet("c"); c.Progress != "unresumable: no session" {
		t.Errorf("progress = %q, want an empty prefix to leave it untouched", c.Progress)
	}
}

// TestAgentBindSession_RefusesEmptyInputs: a path that reports "no match" must
// not be able to blank a session column.
func TestAgentBindSession_RefusesEmptyInputs(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "a", "ses_keep")

	for _, tc := range []struct{ id, sid string }{{"a", ""}, {"", "ses_x"}} {
		bound, err := s.AgentBindSession(tc.id, tc.sid, "")
		if err != nil || bound {
			t.Errorf("AgentBindSession(%q, %q) = %v/%v, want false/nil", tc.id, tc.sid, bound, err)
		}
	}
	if a, _ := s.AgentGet("a"); a.OpencodeSessionID != "ses_keep" {
		t.Errorf("session = %q, want ses_keep", a.OpencodeSessionID)
	}
}

// TestAgentBindSession_OneWinnerUnderConcurrency: the bind calls arrive from the
// spawn goroutine, the heartbeat and request handlers at once. Read-then-write
// would let two of them pass the same ownership test; the single guarded
// statement leaves exactly one owner.
func TestAgentBindSession_OneWinnerUnderConcurrency(t *testing.T) {
	s := mustOpen(t)
	const workers = 8
	for i := 0; i < workers; i++ {
		createAgentForBind(t, s, fmt.Sprintf("agent-%d", i), "")
	}

	var wg sync.WaitGroup
	var wins int32
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bound, err := s.AgentBindSession(fmt.Sprintf("agent-%d", i), "ses_shared", "")
			errs[i] = err
			if bound {
				atomic.AddInt32(&wins, 1)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	if wins != 1 {
		t.Errorf("%d agents bound the same session, want exactly 1", wins)
	}
}

// TestAgentUpdate_RefusesOpencodeSessionID: the column decides which session a
// row owns, so it must not be writable through the generic update — a caller
// that could plant a foreign session there would have whoami, send_to and resume
// follow it. AgentBindSession is the only writer.
func TestAgentUpdate_RefusesOpencodeSessionID(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "a", "")

	if err := s.AgentUpdate("a", map[string]any{"opencode_session_id": "ses_injected"}); err == nil {
		t.Fatal("opencode_session_id must not be settable through AgentUpdate")
	}
	if a, _ := s.AgentGet("a"); a.OpencodeSessionID != "" {
		t.Errorf("session = %q, want the row untouched", a.OpencodeSessionID)
	}
	// The guarded writer still works.
	if bound, err := s.AgentBindSession("a", "ses_ok", ""); err != nil || !bound {
		t.Fatalf("AgentBindSession: bound=%v err=%v", bound, err)
	}
	if a, _ := s.AgentGet("a"); a.OpencodeSessionID != "ses_ok" {
		t.Errorf("session = %q, want ses_ok", a.OpencodeSessionID)
	}
}

// TestAgentBindSession_StripsTheSessionPrefix: the MCP layer hands over ids like
// "opencode:ses_x" while the row stores bare ids and every reader strips the
// prefix. A writer that stored the prefixed form would produce a row that no
// lookup — and no later bind — can ever resolve.
func TestAgentBindSession_StripsTheSessionPrefix(t *testing.T) {
	s := mustOpen(t)
	createAgentForBind(t, s, "owner", "ses_x")
	createAgentForBind(t, s, "other", "")

	bound, err := s.AgentBindSession("other", "opencode:ses_x", "")
	if err != nil {
		t.Fatalf("AgentBindSession: %v", err)
	}
	if bound {
		t.Error("a prefixed form of an owned session must be refused")
	}

	if bound, err = s.AgentBindSession("other", "opencode:ses_y", ""); err != nil || !bound {
		t.Fatalf("binding a free prefixed session: bound=%v err=%v", bound, err)
	}
	a, _ := s.AgentGet("other")
	if a.OpencodeSessionID != "ses_y" {
		t.Errorf("stored %q, want the bare ses_y", a.OpencodeSessionID)
	}
	if found, _ := s.AgentGetAnyBySession("opencode:ses_y"); found == nil || found.ID != "other" {
		t.Errorf("the bound row is not resolvable by its prefixed address")
	}
}

// The process bind is conditional: the relaunch runs in a goroutine, so a stop
// can land between the row's "spawning" write and the bind. A terminal row must
// not be revived by it, and the caller learns from the false that the process it
// just created has no owner.
func TestAgentBindProcessIfLive_RefusesAFinalizedRow(t *testing.T) {
	s := mustOpen(t)
	now := time.Now().Format("2006-01-02 15:04:05")

	for _, status := range []string{"pending", "spawning", "running", "paused"} {
		id := "live-" + status
		if err := s.AgentCreate(Agent{ID: id, Project: "p", Section: "s-" + id, Status: status, CreatedAt: now}); err != nil {
			t.Fatalf("AgentCreate(%s): %v", id, err)
		}
		bound, err := s.AgentBindProcessIfLive(id, 4242, "/tmp/"+id+".sock", now, "")
		if err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
		if !bound {
			t.Errorf("status %q: bind refused, want the row bound", status)
		}
		a, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet(%s): %v", id, err)
		}
		if a.Status != "running" || a.PID != 4242 || a.LastRestartAt == "" {
			t.Errorf("status %q: after the bind status=%q pid=%d marker=%q, want running/4242/set",
				status, a.Status, a.PID, a.LastRestartAt)
		}
	}

	for _, status := range []string{"stopped", "finished", "failed", "error", "crashed"} {
		id := "dead-" + status
		if err := s.AgentCreate(Agent{ID: id, Project: "p", Section: "s-" + id, Status: status, CreatedAt: now}); err != nil {
			t.Fatalf("AgentCreate(%s): %v", id, err)
		}
		bound, err := s.AgentBindProcessIfLive(id, 4243, "/tmp/"+id+".sock", now, "")
		if err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
		if bound {
			t.Errorf("status %q: bind written, want refused — a finalized row must not be revived", status)
		}
		a, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet(%s): %v", id, err)
		}
		if a.Status != status || a.PID == 4243 {
			t.Errorf("status %q: after the refused bind status=%q pid=%d, want %q/unchanged", status, a.Status, a.PID, status)
		}
	}
}

// A pid and its spawn marker belong together: the identity check dates a process
// against the marker, so a row that gains a pid without refreshing it reads as
// "not this agent's process" — and every guard then treats a live agent as gone.
// The store stamps the marker instead of trusting the caller.
func TestAgentUpdate_StampsTheSpawnMarkerWithAPID(t *testing.T) {
	s := mustOpen(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	if err := s.AgentCreate(Agent{ID: "marker", Project: "p", Section: "s-marker", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}

	if err := s.AgentUpdate("marker", map[string]any{"pid": 4242}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}
	a, err := s.AgentGet("marker")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.PID != 4242 || a.LastRestartAt == "" {
		t.Errorf("pid=%d marker=%q, want 4242 with a stamped marker", a.PID, a.LastRestartAt)
	}

	// A marker the caller supplies is used as given (the bind writes its own).
	given := time.Now().Add(-time.Hour).Format("2006-01-02 15:04:05")
	if err := s.AgentUpdate("marker", map[string]any{"pid": 4243, "last_restart_at": given}); err != nil {
		t.Fatalf("AgentUpdate with marker: %v", err)
	}
	if a, _ = s.AgentGet("marker"); a.LastRestartAt != given {
		t.Errorf("marker = %q, want the caller's %q", a.LastRestartAt, given)
	}

	// Unbinding (pid 0) is what the stop and spawn tails write and needs no marker.
	if err := s.AgentUpdate("marker", map[string]any{"pid": 0}); err != nil {
		t.Fatalf("AgentUpdate unbind: %v", err)
	}
	if a, _ = s.AgentGet("marker"); a.PID != 0 {
		t.Errorf("pid = %d after the unbind, want 0", a.PID)
	}
}

// The stop's write is a compare-and-set on the PID the decision was made on: a
// relaunch that bound a process in between must not be overwritten silently — the
// caller has to see the refusal and end the process it displaced.
func TestAgentFinalizeStop_RefusesAMovedPID(t *testing.T) {
	s := mustOpen(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	if err := s.AgentCreate(Agent{ID: "stop-cas", Project: "p", Section: "s-cas", Status: "spawning", PID: 111, CreatedAt: now}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// The relaunch binds the new process (the row was still spawning).
	bound, err := s.AgentBindProcessIfLive("stop-cas", 222, "/tmp/stop-cas.sock", now, "")
	if err != nil || !bound {
		t.Fatalf("bind: bound=%v err=%v", bound, err)
	}

	// A stop whose snapshot carries the pre-bind PID is refused, and the row keeps
	// the binding it never saw.
	written, err := s.AgentFinalizeStop("stop-cas", 111, now, "stopped")
	if err != nil {
		t.Fatalf("AgentFinalizeStop(moved): %v", err)
	}
	if written {
		t.Error("the finalize overwrote a binding it never saw — the process would be left running with no owner")
	}
	a, err := s.AgentGet("stop-cas")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if a.PID != 222 || a.Status != "running" {
		t.Errorf("pid=%d status=%q, want the binding untouched (222/running)", a.PID, a.Status)
	}

	// Retried on the PID that displaced the snapshot, the write lands.
	written, err = s.AgentFinalizeStop("stop-cas", 222, now, "stopped")
	if err != nil {
		t.Fatalf("AgentFinalizeStop(current): %v", err)
	}
	if !written {
		t.Fatal("the finalize on the current PID was refused")
	}
	if a, _ = s.AgentGet("stop-cas"); a.Status != "stopped" || a.PID != 0 || a.SockPath != "" {
		t.Errorf("status=%q pid=%d sock=%q, want stopped/0/empty", a.Status, a.PID, a.SockPath)
	}
}

// The pid column is nullable, and a NULL reads back as 0. The CAS has to match such
// a row too, otherwise it can never be finalized (every retry is refused) and the
// agent is unstoppable.
func TestAgentFinalizeStop_MatchesANullPID(t *testing.T) {
	s := mustOpen(t)
	now := time.Now().Format("2006-01-02 15:04:05")
	if err := s.AgentCreate(Agent{ID: "stop-null", Project: "p", Section: "s-null", Status: "pending", PID: 0, CreatedAt: now}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if _, err := s.db.Exec("UPDATE agents SET pid = NULL WHERE id = ?", "stop-null"); err != nil {
		t.Fatalf("force NULL pid: %v", err)
	}

	written, err := s.AgentFinalizeStop("stop-null", 0, now, "stopped")
	if err != nil {
		t.Fatalf("AgentFinalizeStop(NULL pid): %v", err)
	}
	if !written {
		t.Error("a row whose pid is NULL was not finalized — the CAS can never match it, so the agent is unstoppable")
	}
	if a, _ := s.AgentGet("stop-null"); a.Status != "stopped" || a.PID != 0 {
		t.Errorf("status=%q pid=%d, want stopped/0", a.Status, a.PID)
	}
}

// AgentUpdateIfMatches is the compare-and-set the relay path uses: it decided on
// a row it read before delivering, so it must not overwrite what landed since.
func TestAgentUpdateIfMatches_OnlyWritesTheExpectedRow(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{ID: "cas-1", Project: "p", Section: "s", Status: "paused"}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if err := s.AgentUpdate("cas-1", map[string]any{"progress": "paused: yesloop-idle escalation: no PROVEN marker"}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	changed, err := s.AgentUpdateIfMatches("cas-1", "paused", "paused: something else", map[string]any{"status": "running"})
	if err != nil {
		t.Fatalf("AgentUpdateIfMatches: %v", err)
	}
	if changed {
		t.Error("a row with different progress text must not be written")
	}
	if got, _ := s.AgentGet("cas-1"); got.Status != "paused" {
		t.Errorf("status = %q, want paused (the mismatch must be a no-op)", got.Status)
	}

	changed, err = s.AgentUpdateIfMatches("cas-1", "stopped", "paused: yesloop-idle escalation: no PROVEN marker",
		map[string]any{"status": "running"})
	if err != nil {
		t.Fatalf("AgentUpdateIfMatches: %v", err)
	}
	if changed {
		t.Error("a row with a different status must not be written")
	}

	changed, err = s.AgentUpdateIfMatches("cas-1", "paused", "paused: yesloop-idle escalation: no PROVEN marker",
		map[string]any{"status": "running"})
	if err != nil {
		t.Fatalf("AgentUpdateIfMatches: %v", err)
	}
	if !changed {
		t.Fatal("a matching row must be written")
	}
	got, err := s.AgentGet("cas-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status = %q, want running", got.Status)
	}
}

// A fresh row stores no progress at all, and the caller read that as "". A bare
// equality against the column would compare against NULL and never match, so the
// first nudge or relay on a new row would look like "the row moved" and do
// nothing.
func TestAgentUpdateIfMatches_MatchesAnEmptyProgressOnAFreshRow(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{ID: "cas-fresh", Project: "p", Section: "s", Status: "running"}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	row, err := s.AgentGet("cas-fresh")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Progress != "" {
		t.Fatalf("precondition: a fresh row reads back an empty progress, got %q", row.Progress)
	}

	changed, err := s.AgentUpdateIfMatches("cas-fresh", "running", row.Progress,
		map[string]any{"progress": "guard nudge: something"})
	if err != nil {
		t.Fatalf("AgentUpdateIfMatches: %v", err)
	}
	if !changed {
		t.Error("a fresh row must take the write: the empty progress is what the caller read")
	}
}

func TestAgentUpdateIfMatches_RejectsDisallowedFields(t *testing.T) {
	s := mustOpen(t)
	if err := s.AgentCreate(Agent{ID: "cas-2", Project: "p", Section: "s", Status: "paused"}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if _, err := s.AgentUpdateIfMatches("cas-2", "paused", "", map[string]any{"opencode_session_id": "ses_x"}); err == nil {
		t.Fatal("a field outside the allowlist must be refused")
	}
}

// A crashed row is closable only while it still claims to be crashed: stop_agent
// accepts a crashed row, and an unconditional close would revive that deliberate
// stop as "failed" with a reason that no longer applies.
func TestAgentCloseIfCrashed_OnlyTouchesCrashedRows(t *testing.T) {
	s := mustOpen(t)

	for _, a := range []Agent{
		{ID: "close-crashed", Project: "p", Section: "s1", Status: "crashed"},
		{ID: "close-stopped", Project: "p", Section: "s2", Status: "stopped"},
	} {
		if err := s.AgentCreate(a); err != nil {
			t.Fatalf("AgentCreate(%s): %v", a.ID, err)
		}
	}

	written, err := s.AgentCloseIfCrashed("close-crashed", "no replacement started")
	if err != nil {
		t.Fatalf("AgentCloseIfCrashed(crashed): %v", err)
	}
	if !written {
		t.Fatal("a crashed row was not written")
	}
	if a, _ := s.AgentGet("close-crashed"); a.Status != "failed" || a.Error != "no replacement started" {
		t.Errorf("status=%q error=%q, want failed with the note", a.Status, a.Error)
	}

	written, err = s.AgentCloseIfCrashed("close-stopped", "no replacement started")
	if err != nil {
		t.Fatalf("AgentCloseIfCrashed(stopped): %v", err)
	}
	if written {
		t.Error("a stopped row was overwritten — the deliberate stop must win")
	}
	if a, _ := s.AgentGet("close-stopped"); a.Status != "stopped" || a.Error != "" {
		t.Errorf("status=%q error=%q, want the stopped row untouched", a.Status, a.Error)
	}
}

// The cascade reports the rows it closed together with the PID their process
// held: a caller that only learns "3 stopped" cannot end those processes, and the
// rows alone leave them running with no relay and no limit (YM-66).
func TestAgentCascadeStop_ReturnsTheClosedRowsWithTheirPIDs(t *testing.T) {
	s := mustOpen(t)
	now := "2006-01-02 15:04:05"

	s.AgentCreate(Agent{ID: "orch-pids", SessionID: "sess-orch-pids", Project: "p", Section: "orch", Status: "running", CreatedAt: now})
	s.AgentCreate(Agent{ID: "child-pid", SessionID: "sess-child-pid", Project: "p", Section: "c", Status: "running", PID: 4242, SockPath: "/tmp/child-pid.sock", CallerSession: "sess-orch-pids", CreatedAt: now})

	stopped, err := s.AgentCascadeStop("sess-orch-pids")
	if err != nil {
		t.Fatalf("AgentCascadeStop: %v", err)
	}
	if len(stopped) != 1 {
		t.Fatalf("stopped=%d want 1", len(stopped))
	}
	if stopped[0].ID != "child-pid" || stopped[0].PID != 4242 {
		t.Errorf("row = %s/pid %d, want child-pid/4242", stopped[0].ID, stopped[0].PID)
	}
}

// TestAgentNextIDConcurrent_Unique (YM-151): AgentNextID read MAX()+1 through
// the reader connection and persisted nothing, so two spawns that asked at the
// same moment received the SAME id and the second AgentCreate failed on the
// primary key. The allocation must be atomic.
func TestAgentNextIDConcurrent_Unique(t *testing.T) {
	s := mustOpen(t)

	const n = 20
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.AgentNextID("proj")
			if err != nil {
				ids <- "error: " + err.Error()
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]bool, n)
	for id := range ids {
		if strings.HasPrefix(id, "error: ") {
			t.Fatalf("AgentNextID: %s", id)
		}
		if seen[id] {
			t.Errorf("duplicate agent id %q handed out to two concurrent spawns", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("expected %d distinct ids, got %d", n, len(seen))
	}
}

// TestAgentNextIDSeedContinuesAboveExistingDay (YM-151): the sequence table is
// created additively on an existing installation. Its seed must start every day
// above the highest id that day already carries, otherwise the first spawn after
// the upgrade collides with an existing row.
func TestAgentNextIDSeedContinuesAboveExistingDay(t *testing.T) {
	s := mustOpen(t)
	today := time.Now().UTC().Format("20060102")

	if err := s.AgentCreate(Agent{
		ID: "agent-" + today + "-07", Project: "proj", Section: "seed1", Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	// A database written before the sequence existed has rows but no counter row.
	if _, err := s.db.Exec("DELETE FROM agent_id_seq WHERE day = ?", today); err != nil {
		t.Fatalf("clear sequence: %v", err)
	}
	if err := s.createSchema(); err != nil {
		t.Fatalf("createSchema (re-open path): %v", err)
	}

	id, err := s.AgentNextID("proj")
	if err != nil {
		t.Fatalf("AgentNextID: %v", err)
	}
	if want := "agent-" + today + "-08"; id != want {
		t.Errorf("first id after the upgrade = %q, want %q (seed must continue above the existing day)", id, want)
	}
}

// TestAgentNextIDSeedHealsBehindCounter (YM-151 review): an older binary writes
// ids with MAX()+1 and no counter row, so a rollback can leave the counter below
// ids that already exist. The seed must raise it on the next open — INSERT OR
// IGNORE never would, and the next spawn would fail on the primary key.
func TestAgentNextIDSeedHealsBehindCounter(t *testing.T) {
	s := mustOpen(t)
	today := time.Now().UTC().Format("20060102")

	if err := s.AgentCreate(Agent{
		ID: "agent-" + today + "-12", Project: "proj", Section: "heal1", Status: "running",
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO agent_id_seq (day, next) VALUES (?, 10)
		 ON CONFLICT(day) DO UPDATE SET next = 10`, today,
	); err != nil {
		t.Fatalf("seed stale counter: %v", err)
	}
	if err := s.createSchema(); err != nil {
		t.Fatalf("createSchema (re-open path): %v", err)
	}

	id, err := s.AgentNextID("proj")
	if err != nil {
		t.Fatalf("AgentNextID: %v", err)
	}
	if want := "agent-" + today + "-13"; id != want {
		t.Errorf("id after heal = %q, want %q (counter must be raised above existing ids)", id, want)
	}
}
