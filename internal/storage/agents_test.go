package storage

import "testing"

func TestAgentCreate_And_Get(t *testing.T) {
	s := newTestStore(t)

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

func TestAgentUpdate(t *testing.T) {
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

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
	s := newTestStore(t)

	// No agents yet
	id, err := s.AgentNextID("proj")
	if err != nil {
		t.Fatalf("AgentNextID: %v", err)
	}
	if id != "agent-0" {
		t.Errorf("expected agent-0, got %q", id)
	}

	// After creating some
	s.AgentCreate(Agent{ID: "agent-0", Project: "proj", Section: "sec1", Status: "running"})
	s.AgentCreate(Agent{ID: "agent-1", Project: "proj", Section: "sec2", Status: "running"})

	id, _ = s.AgentNextID("proj")
	if id != "agent-2" {
		t.Errorf("expected agent-2, got %q", id)
	}
}

func TestAgentDelete(t *testing.T) {
	s := newTestStore(t)

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
	s := newTestStore(t)
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
	s := newTestStore(t)
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

func TestAgentTelemetryAccumulates(t *testing.T) {
	s := newTestStore(t)
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
	s := newTestStore(t)

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
	s := newTestStore(t)
	now := "2006-01-02 15:04:05"

	s.AgentCreate(Agent{ID: "orch-0", SessionID: "sess-orch", Project: "p", Section: "orch", Status: "running", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-a", SessionID: "sess-a", Project: "p", Section: "a", Status: "running", CallerSession: "sess-orch", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-b", SessionID: "sess-b", Project: "p", Section: "b", Status: "running", CallerSession: "sess-orch", CreatedAt: now})
	s.AgentCreate(Agent{ID: "sub-sub", SessionID: "sess-sub", Project: "p", Section: "sub", Status: "running", CallerSession: "sess-a", CreatedAt: now})

	stopped, err := s.AgentCascadeStop("sess-orch")
	if err != nil {
		t.Fatalf("AgentCascadeStop: %v", err)
	}
	if stopped != 3 {
		t.Errorf("stopped=%d want 3 (sub-a, sub-b, sub-sub)", stopped)
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
	s := newTestStore(t)
	now := "2006-01-02 15:04:05"

	s.AgentCreate(Agent{ID: "orch-p", SessionID: "sess-orch-p", Project: "p", Section: "orch", Status: "running", CreatedAt: now})
	s.AgentCreate(Agent{ID: "pending-child", SessionID: "sess-pending", Project: "p", Section: "pc", Status: "pending", CallerSession: "sess-orch-p", CreatedAt: now})

	stopped, err := s.AgentCascadeStop("sess-orch-p")
	if err != nil {
		t.Fatalf("AgentCascadeStop: %v", err)
	}
	if stopped != 1 {
		t.Errorf("stopped=%d want 1 (pending child)", stopped)
	}

	child, _ := s.AgentGet("pending-child")
	if child.Status != "stopped" {
		t.Errorf("pending child status=%q want stopped", child.Status)
	}
}
