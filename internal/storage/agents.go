package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// tableAgents defines the agents table schema for agent orchestration.
const tableAgents = `CREATE TABLE IF NOT EXISTS agents (
	id              TEXT PRIMARY KEY,
	project         TEXT NOT NULL,
	section         TEXT NOT NULL,
	session_id      TEXT,
	pid             INTEGER,
	sock_path       TEXT,
	status          TEXT DEFAULT 'pending',
	caller_session  TEXT,
	error           TEXT,
	heartbeat_at    TEXT,
	progress        TEXT,
	relay_count     INTEGER DEFAULT 0,
	depth           INTEGER DEFAULT 0,
	token_budget    INTEGER DEFAULT 0,
	retry_count     INTEGER DEFAULT 0,
	backend         TEXT DEFAULT 'claude',
	turns_used       INTEGER DEFAULT 0,
	input_tokens     INTEGER DEFAULT 0,
	output_tokens    INTEGER DEFAULT 0,
	last_activity_at TEXT,
	phase            TEXT DEFAULT 'idle',
	created_at      TEXT DEFAULT (datetime('now', 'localtime')),
	stopped_at      TEXT,
	restart_strategy TEXT    DEFAULT 'temporary',
	restart_count    INTEGER DEFAULT 0,
	max_restarts     INTEGER DEFAULT 3,
	liveness_ping_at TEXT    DEFAULT '',
	last_restart_at  TEXT    DEFAULT ''
)`

// Agent represents a spawned agent process.
type Agent struct {
	ID             string `json:"id"`
	Project        string `json:"project"`
	Section        string `json:"section"`
	SessionID      string `json:"session_id,omitempty"`
	PID            int    `json:"pid,omitempty"`
	SockPath       string `json:"sock_path,omitempty"`
	Status         string `json:"status"`
	CallerSession  string `json:"caller_session,omitempty"`
	Error          string `json:"error,omitempty"`
	HeartbeatAt    string `json:"heartbeat_at,omitempty"`
	Progress       string `json:"progress,omitempty"`
	RelayCount     int    `json:"relay_count,omitempty"`
	Depth          int    `json:"depth,omitempty"`
	TokenBudget    int    `json:"token_budget,omitempty"`
	RetryCount     int    `json:"retry_count,omitempty"`
	Backend        string `json:"backend,omitempty"`
	TurnsUsed      int    `json:"turns_used"`
	InputTokens    int    `json:"input_tokens"`
	OutputTokens   int    `json:"output_tokens"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
	Phase           string `json:"phase"`
	CreatedAt       string `json:"created_at,omitempty"`
	StoppedAt       string `json:"stopped_at,omitempty"`
	RestartStrategy string `json:"restart_strategy,omitempty"`
	RestartCount    int    `json:"restart_count,omitempty"`
	MaxRestarts     int    `json:"max_restarts,omitempty"`
	LivenessPingAt  string `json:"liveness_ping_at,omitempty"`
	LastRestartAt   string `json:"last_restart_at,omitempty"`
}

// AgentCreate inserts a new agent record.
func (s *Store) AgentCreate(a Agent) error {
	backend := a.Backend
	if backend == "" {
		backend = "claude"
	}
	_, err := s.db.Exec(`INSERT INTO agents (id, project, section, session_id, pid, sock_path, status, caller_session, depth, token_budget, backend, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Project, a.Section, a.SessionID, a.PID, a.SockPath, a.Status, a.CallerSession, a.Depth, a.TokenBudget, backend, a.RestartStrategy, a.RestartCount, a.MaxRestarts, a.LivenessPingAt, a.LastRestartAt)
	return err
}

// agentAllowedFields is the allowlist of columns that can be updated via AgentUpdate.
var agentAllowedFields = map[string]bool{
	"session_id":     true,
	"pid":            true,
	"sock_path":      true,
	"status":         true,
	"caller_session": true,
	"error":          true,
	"heartbeat_at":   true,
	"progress":       true,
	"stopped_at":     true,
	"relay_count":    true,
	"depth":          true,
	"token_budget":   true,
	"retry_count":    true,
	"backend":        true,
	"phase":          true,
	"restart_strategy": true,
	"restart_count":    true,
	"max_restarts":     true,
	"liveness_ping_at": true,
	"last_restart_at":  true,
}

// AgentUpdate updates specific fields of an agent record.
// Only fields in the allowlist are accepted to prevent SQL injection.
func (s *Store) AgentUpdate(id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	var setClauses []string
	var args []any
	for k, v := range fields {
		if !agentAllowedFields[k] {
			return fmt.Errorf("disallowed field: %q", k)
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, err := s.db.Exec("UPDATE agents SET "+strings.Join(setClauses, ", ")+" WHERE id = ?", args...)
	return err
}

// AgentGet returns an agent by ID.
func (s *Store) AgentGet(id string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at
		FROM agents WHERE id = ?`, id))
}

// AgentGetBySection returns the most recent agent for a project+section combo.
func (s *Store) AgentGetBySection(project, section string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at
		FROM agents WHERE project = ? AND section = ? ORDER BY created_at DESC LIMIT 1`, project, section))
}

// AgentGetActiveBySection returns the most recent active agent for a project+section combo.
// Active means the agent still owns the section and blocks spawning a new one.
func (s *Store) AgentGetActiveBySection(project, section string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at
		FROM agents
		WHERE project = ? AND section = ? AND status IN ('running', 'pending', 'spawning', 'frozen')
		ORDER BY created_at DESC LIMIT 1`, project, section))
}

// AgentList returns all agents, optionally filtered by project.
func (s *Store) AgentList(project string) ([]Agent, error) {
	var rows *sql.Rows
	var err error
	const q = `SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at FROM agents`
	if project != "" {
		rows, err = s.readerDB().Query(q+` WHERE project = ? ORDER BY created_at DESC`, project)
	} else {
		rows, err = s.readerDB().Query(q + ` ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		a, err := s.scanAgentRow(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// AgentUpdateBySessionID updates fields on an agent matched by session_id instead of id.
// Used for heartbeat updates where the caller only knows the session UUID.
func (s *Store) AgentUpdateBySessionID(sessionID string, fields map[string]any) error {
	if len(fields) == 0 || sessionID == "" {
		return nil
	}
	var setClauses []string
	var args []any
	for k, v := range fields {
		if !agentAllowedFields[k] {
			return fmt.Errorf("disallowed field: %q", k)
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	args = append(args, sessionID)
	_, err := s.db.Exec("UPDATE agents SET "+strings.Join(setClauses, ", ")+" WHERE session_id = ? AND status = 'running'", args...)
	return err
}

// AgentDelete removes an agent record.
func (s *Store) AgentDelete(id string) error {
	_, err := s.db.Exec("DELETE FROM agents WHERE id = ?", id)
	return err
}

// AgentIncrementRelayCount atomically increments the relay_count for an agent and returns the new value.
func (s *Store) AgentIncrementRelayCount(id string) (int, error) {
	_, err := s.db.Exec("UPDATE agents SET relay_count = relay_count + 1 WHERE id = ?", id)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.readerDB().QueryRow("SELECT relay_count FROM agents WHERE id = ?", id).Scan(&count)
	return count, err
}

// AgentGetBySession returns the agent for a given session ID.
func (s *Store) AgentGetBySession(sessionID string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at
		FROM agents WHERE session_id = ? AND status = 'running' LIMIT 1`, sessionID))
}

// AgentGetAnyBySession returns the most recent agent for a given session ID,
// regardless of status. Useful for resume flows that need stopped agents.
func (s *Store) AgentGetAnyBySession(sessionID string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at
		FROM agents WHERE session_id = ? ORDER BY created_at DESC LIMIT 1`, sessionID))
}

// AgentDeleteOrphaned deletes all running/pending agents (daemon restart recovery).
// Deprecated: Use AgentRecoverOrphaned for graceful recovery that preserves living agents.
func (s *Store) AgentDeleteOrphaned() (int64, error) {
	result, err := s.db.Exec(`DELETE FROM agents WHERE status IN ('running', 'pending', 'spawning')`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// AgentRecoverOrphaned checks all running/pending/spawning agents after daemon restart.
// Living agents (PID still alive) are kept. Dead agents are deleted.
// Returns (reconnected, deleted) counts.
func (s *Store) AgentRecoverOrphaned(isAlive func(pid int) bool) (int, int, error) {
	rows, err := s.readerDB().Query(
		`SELECT id, pid, status FROM agents WHERE status IN ('running', 'pending', 'spawning')`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var reconnected, deleted int
	var toDelete []string
	for rows.Next() {
		var id string
		var pid int
		var status string
		if err := rows.Scan(&id, &pid, &status); err != nil {
			continue
		}
		if pid > 0 && isAlive(pid) {
			reconnected++
		} else {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
		deleted++
	}
	return reconnected, deleted, nil
}

// AgentUpdateTelemetry atomically increments telemetry counters for an agent.
func (s *Store) AgentUpdateTelemetry(id string, turnsInc, inputTokensInc, outputTokensInc int) error {
	_, err := s.db.Exec(`
		UPDATE agents SET
			turns_used       = turns_used + ?,
			input_tokens     = input_tokens + ?,
			output_tokens    = output_tokens + ?,
			last_activity_at = datetime('now', 'localtime')
		WHERE id = ?`, turnsInc, inputTokensInc, outputTokensInc, id)
	return err
}

// MigrateAgentsSchema adds telemetry columns to existing agents tables (idempotent).
func (s *Store) MigrateAgentsSchema() error {
	migrations := []string{
		`ALTER TABLE agents ADD COLUMN turns_used INTEGER DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN input_tokens INTEGER DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN output_tokens INTEGER DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN last_activity_at TEXT`,
		`ALTER TABLE agents ADD COLUMN phase TEXT DEFAULT 'idle'`,
		`ALTER TABLE agents ADD COLUMN restart_strategy  TEXT    DEFAULT 'temporary'`,
		`ALTER TABLE agents ADD COLUMN restart_count     INTEGER DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN max_restarts      INTEGER DEFAULT 3`,
		`ALTER TABLE agents ADD COLUMN liveness_ping_at  TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN last_restart_at   TEXT    DEFAULT ''`,
	}
	for _, m := range migrations {
		_, err := s.db.Exec(m)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration %q: %w", m, err)
		}
	}
	return nil
}

// AgentNextID returns the next available agent ID (globally unique, e.g. "agent-3").
func (s *Store) AgentNextID(project string) (string, error) {
	var maxNum sql.NullInt64
	err := s.readerDB().QueryRow(
		`SELECT MAX(CAST(REPLACE(id, 'agent-', '') AS INTEGER)) FROM agents WHERE id LIKE 'agent-%'`).Scan(&maxNum)
	if err != nil {
		return "agent-0", nil
	}
	if !maxNum.Valid {
		return "agent-0", nil
	}
	return fmt.Sprintf("agent-%d", maxNum.Int64+1), nil
}

// AgentCascadeStop stops all agents in the supervision tree rooted at parentSessionID.
// Uses BFS over CallerSession links. Does NOT stop the parent itself.
// Returns the number of agents stopped.
func (s *Store) AgentCascadeStop(parentSessionID string) (int, error) {
	all, err := s.AgentList("")
	if err != nil {
		return 0, err
	}

	stopped := 0
	queue := []string{parentSessionID}
	visited := map[string]bool{parentSessionID: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, a := range all {
			if a.CallerSession != current || visited[a.SessionID] || a.SessionID == "" {
				continue
			}
			visited[a.SessionID] = true
			if a.Status == "running" || a.Status == "frozen" || a.Status == "spawning" || a.Status == "pending" {
				if err := s.AgentUpdate(a.ID, map[string]any{"status": "stopped"}); err == nil {
					stopped++
				}
			}
			queue = append(queue, a.SessionID)
		}
	}
	return stopped, nil
}

// scanAgent scans a single agent row from a *sql.Row.
func (s *Store) scanAgent(row *sql.Row) (*Agent, error) {
	a := &Agent{}
	var sessionID, sockPath, callerSession, errStr, heartbeat, progress, stoppedAt, lastActivityAt, restartStrategy, livenessPingAt, lastRestartAt sql.NullString
	var pid sql.NullInt64
	var restartCount, maxRestarts sql.NullInt64
	err := row.Scan(&a.ID, &a.Project, &a.Section, &sessionID, &pid, &sockPath,
		&a.Status, &callerSession, &errStr, &heartbeat, &progress,
		&a.RelayCount, &a.Depth, &a.TokenBudget, &a.RetryCount, &a.Backend,
		&a.TurnsUsed, &a.InputTokens, &a.OutputTokens, &lastActivityAt, &a.Phase,
		&a.CreatedAt, &stoppedAt, &restartStrategy, &restartCount, &maxRestarts, &livenessPingAt, &lastRestartAt)
	if err != nil {
		return nil, err
	}
	a.SessionID = sessionID.String
	a.PID = int(pid.Int64)
	a.SockPath = sockPath.String
	a.CallerSession = callerSession.String
	a.Error = errStr.String
	a.HeartbeatAt = heartbeat.String
	a.Progress = progress.String
	a.LastActivityAt = lastActivityAt.String
	a.StoppedAt = stoppedAt.String
	a.RestartStrategy = restartStrategy.String
	a.RestartCount = int(restartCount.Int64)
	a.MaxRestarts = int(maxRestarts.Int64)
	a.LivenessPingAt = livenessPingAt.String
	a.LastRestartAt = lastRestartAt.String
	return a, nil
}

// scanAgentRow scans a single agent from a *sql.Rows.
func (s *Store) scanAgentRow(rows *sql.Rows) (Agent, error) {
	var a Agent
	var sessionID, sockPath, callerSession, errStr, heartbeat, progress, stoppedAt, lastActivityAt, restartStrategy, livenessPingAt, lastRestartAt sql.NullString
	var pid sql.NullInt64
	var restartCount, maxRestarts sql.NullInt64
	err := rows.Scan(&a.ID, &a.Project, &a.Section, &sessionID, &pid, &sockPath,
		&a.Status, &callerSession, &errStr, &heartbeat, &progress,
		&a.RelayCount, &a.Depth, &a.TokenBudget, &a.RetryCount, &a.Backend,
		&a.TurnsUsed, &a.InputTokens, &a.OutputTokens, &lastActivityAt, &a.Phase,
		&a.CreatedAt, &stoppedAt, &restartStrategy, &restartCount, &maxRestarts, &livenessPingAt, &lastRestartAt)
	if err != nil {
		return a, err
	}
	a.SessionID = sessionID.String
	a.PID = int(pid.Int64)
	a.SockPath = sockPath.String
	a.CallerSession = callerSession.String
	a.Error = errStr.String
	a.HeartbeatAt = heartbeat.String
	a.Progress = progress.String
	a.LastActivityAt = lastActivityAt.String
	a.StoppedAt = stoppedAt.String
	a.RestartStrategy = restartStrategy.String
	a.RestartCount = int(restartCount.Int64)
	a.MaxRestarts = int(maxRestarts.Int64)
	a.LivenessPingAt = livenessPingAt.String
	a.LastRestartAt = lastRestartAt.String
	return a, nil
}
