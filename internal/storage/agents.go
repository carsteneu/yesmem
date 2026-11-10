package storage

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
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
	max_turns       INTEGER DEFAULT 0,
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
	last_restart_at  TEXT    DEFAULT '',
	proxy_thread_id  TEXT    DEFAULT '',
	codex_session_id TEXT    DEFAULT '',
	opencode_session_id TEXT    DEFAULT '',
	permission_kick  TEXT DEFAULT '',
	model           TEXT DEFAULT '',
	theme           TEXT DEFAULT '',
	work_dir        TEXT    DEFAULT '',
	orphan_reattachable INTEGER DEFAULT 0
)`

// legacyFrozenCutoff is the day after "frozen" was retired (2026-07-03). Only
// rows created before it are closed by MigrateAgentsSchema: a newer "frozen" row
// cannot come from this binary, and silently rewriting it would discard operator
// intent (see MigrateAgentsSchema).
const legacyFrozenCutoff = "2026-07-04"

// Agent represents a spawned agent process.
type Agent struct {
	ID            string `json:"id"`
	Project       string `json:"project"`
	Section       string `json:"section"`
	SessionID     string `json:"session_id,omitempty"`
	PID           int    `json:"pid,omitempty"`
	SockPath      string `json:"sock_path,omitempty"`
	Status        string `json:"status"`
	CallerSession string `json:"caller_session,omitempty"`
	Error         string `json:"error,omitempty"`
	HeartbeatAt   string `json:"heartbeat_at,omitempty"`
	Progress      string `json:"progress,omitempty"`
	RelayCount    int    `json:"relay_count,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	TokenBudget   int    `json:"token_budget,omitempty"`
	// MaxTurns is the spawn-time relay-turn cap as requested (0 = the daemon's
	// config default). It travels on the row because a replacement cannot
	// re-derive it: without it a respawn came back on the default and an agent
	// changed its own limit by crashing (YM-244).
	MaxTurns          int    `json:"max_turns,omitempty"`
	RetryCount        int    `json:"retry_count,omitempty"`
	Backend           string `json:"backend,omitempty"`
	TurnsUsed         int    `json:"turns_used"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	LastActivityAt    string `json:"last_activity_at,omitempty"`
	Phase             string `json:"phase"`
	CreatedAt         string `json:"created_at,omitempty"`
	StoppedAt         string `json:"stopped_at,omitempty"`
	RestartStrategy   string `json:"restart_strategy,omitempty"`
	RestartCount      int    `json:"restart_count,omitempty"`
	MaxRestarts       int    `json:"max_restarts,omitempty"`
	LivenessPingAt    string `json:"liveness_ping_at,omitempty"`
	LastRestartAt     string `json:"last_restart_at,omitempty"`
	ProxyThreadID     string `json:"proxy_thread_id,omitempty"`
	CodexSessionID    string `json:"codex_session_id,omitempty"`
	OpencodeSessionID string `json:"opencode_session_id,omitempty"`
	PermissionKick    string `json:"permission_kick,omitempty"`
	// Model and Theme are the spawn parameters as requested (`--model`,
	// `--theme`). They are what a replacement has to come back with: the project
	// cannot re-derive them, and without them respawnAgent started the
	// replacement with the daemon's default model and the section's default theme
	// (YM-64).
	Model string `json:"model,omitempty"`
	Theme string `json:"theme,omitempty"`
	// WorkDir is the directory the agent's backend was spawned in. It is the
	// directory-scoped half of a late session bind: without it the search runs
	// store-wide and can only rely on the section marker plus the time window.
	WorkDir string `json:"work_dir,omitempty"`
}

// AgentCreate inserts a new agent record.
//
// The backend session columns are written only if the caller already carries
// them; every production spawn leaves them empty and attaches the session
// afterwards through the guarded binder (AgentBindSession), because creating a
// row with a session id another row owns would give the session two owners.
func (s *Store) AgentCreate(a Agent) error {
	backend := a.Backend
	if backend == "" {
		backend = "claude"
	}
	// Explicit RFC3339 timestamp with offset. SQLite's datetime('now','localtime')
	// default resolves to UTC under modernc-sqlite (pure-Go) — see learning #76675.
	// If caller pre-populates CreatedAt (e.g. in tests), honor it.
	createdAt := a.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO agents (id, project, section, session_id, pid, sock_path, status, caller_session, depth, token_budget, max_turns, backend, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, proxy_thread_id, codex_session_id, opencode_session_id, permission_kick, model, theme, work_dir, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Project, a.Section, a.SessionID, a.PID, a.SockPath, a.Status, a.CallerSession, a.Depth, a.TokenBudget, a.MaxTurns, backend, a.RestartStrategy, a.RestartCount, a.MaxRestarts, a.LivenessPingAt, a.LastRestartAt, a.ProxyThreadID, a.CodexSessionID, a.OpencodeSessionID, a.PermissionKick, a.Model, a.Theme, a.WorkDir, createdAt)
	return err
}

// agentAllowedFields is the allowlist of columns that can be updated via AgentUpdate.
var agentAllowedFields = map[string]bool{
	"pid":              true,
	"sock_path":        true,
	"status":           true,
	"caller_session":   true,
	"error":            true,
	"heartbeat_at":     true,
	"progress":         true,
	"stopped_at":       true,
	"relay_count":      true,
	"depth":            true,
	"token_budget":     true,
	"retry_count":      true,
	"backend":          true,
	"phase":            true,
	"restart_strategy": true,
	"restart_count":    true,
	"max_restarts":     true,
	"liveness_ping_at": true,
	"last_restart_at":  true,
	"proxy_thread_id":  true,
	"codex_session_id": true,
	// session_id is deliberately absent: it is the daemon's own identity for a
	// row — whoami, send_to and the yesloop guards follow it, and since the
	// subagent freeze the guards read it to decide whether a relay may be sent at
	// all. AgentCreate writes it once and nothing else ever does; allowing it
	// here would let any socket caller point its own row at another session and
	// keep the very guard that is supposed to pause it off its back (YM-344).
	// opencode_session_id is deliberately absent: it is written only by the
	// guarded binder (AgentBindSession), which refuses a session another row
	// owns. Allowing it here would let any socket caller plant a foreign session
	// on a row and have whoami, send_to and resume follow it.
	"permission_kick": true,
	// work_dir is deliberately absent: it records the directory the backend
	// actually started in, and the session bind trusts it. Making it rewritable
	// through update_agent would let a caller re-point an agent's bind scope.
}

// AgentSetWorkDir records the directory a row's backend really started in. It
// bypasses AgentUpdate's allowlist on purpose (see above): only the daemon writes
// this, and only with the directory it just handed to the process — after a
// resume or restart had to fall back to the daemon-owned run dir (YM-286).
func (s *Store) AgentSetWorkDir(id, workDir string) error {
	_, err := s.db.Exec(`UPDATE agents SET work_dir = ? WHERE id = ?`, workDir, id)
	return err
}

// agentLiveStatuses are the statuses a row can carry while it claims to own a
// process or a section. Two statements depend on the exact set — the exit tail's
// "row names no process" branch and the relaunch bind — so it lives in one place:
// a status added to one list and not the other reopens the exit-tail overwrite
// (YM-244) or makes a row invisible to the section guard.
const agentLiveStatuses = `'pending', 'spawning', 'running', 'paused'`

// agentLiveStatusesWithLegacy adds the retired "frozen" status, which an older
// binary can still have written and MigrateAgentsSchema closes on startup. The
// exit tail accepts it for the same reason the readers normalize it: a row from a
// stale binary is still a row this daemon has to be able to release.
const agentLiveStatusesWithLegacy = agentLiveStatuses + `, 'frozen'`

// AgentUpdate updates specific fields of an agent record.
// Only fields in the allowlist are accepted to prevent SQL injection.
func (s *Store) AgentUpdate(id string, fields map[string]any) error {
	setClauses, args, err := agentUpdateClauses(fields)
	if err != nil || len(setClauses) == 0 {
		return err
	}
	args = append(args, id)
	_, err = s.db.Exec("UPDATE agents SET "+strings.Join(setClauses, ", ")+" WHERE id = ?", args...)
	return err
}

// AgentUpdateIfMatches writes fields only while the row still carries the given
// status and progress text, and reports whether a row changed. A caller that
// decided on a row it read seconds earlier — the relay path sleeps between its
// two socket writes — must not overwrite a stop or another guard's pause that
// landed in between.
//
// The progress comparison coalesces: a fresh row stores NULL there and reads back
// as an empty string, and a bare equality against the column would compare with
// NULL and never match, so the first nudge or relay on a new row would silently do
// nothing.
func (s *Store) AgentUpdateIfMatches(id, status, progress string, fields map[string]any) (bool, error) {
	setClauses, args, err := agentUpdateClauses(fields)
	if err != nil || len(setClauses) == 0 {
		return false, err
	}
	args = append(args, id, status, progress)
	res, err := s.db.Exec("UPDATE agents SET "+strings.Join(setClauses, ", ")+
		" WHERE id = ? AND status = ? AND COALESCE(progress, '') = ?", args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// agentUpdateClauses builds the SET list of an agent update. Only allowlisted
// fields are accepted to prevent SQL injection.
func agentUpdateClauses(fields map[string]any) ([]string, []any, error) {
	var setClauses []string
	var args []any
	for k, v := range fields {
		if !agentAllowedFields[k] {
			return nil, nil, fmt.Errorf("disallowed field: %q", k)
		}
		// A pid and its spawn marker belong together: isAgentProcess dates a
		// process against last_restart_at/created_at, so a pid written without
		// refreshing the marker leaves the row's own process unverifiable — and
		// then every guard treats a live agent as gone (crashRecovery closes it
		// "failed" without recovery). The store stamps the marker here instead of
		// trusting every caller to remember it; a caller that passes one keeps it.
		// pid 0 is the unbind written by the stop and spawn tails and carries no
		// marker.
		if k == "pid" && !pidValueIsEmpty(v) {
			if _, hasMarker := fields["last_restart_at"]; !hasMarker {
				setClauses = append(setClauses, "last_restart_at = ?")
				args = append(args, time.Now().Format("2006-01-02 15:04:05"))
			}
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	return setClauses, args, nil
}

// pidValueIsEmpty reports whether a pid field value means "this row holds no
// process" — 0, or a nil a caller passed to clear it.
func pidValueIsEmpty(v any) bool {
	switch p := v.(type) {
	case nil:
		return true
	case int:
		return p == 0
	case int64:
		return p == 0
	case float64:
		return p == 0
	}
	return false
}

// AgentBindSession writes a backend session id onto one agent row in a single
// statement, refusing when another row already owns that session.
//
// Check and write have to be one statement. Bind calls arrive concurrently from
// the spawn goroutine, the heartbeat and request handlers, and a plain
// read-then-write lets two of them pass the same ownership test and leave one
// session id on two rows — which then resolves to an arbitrary owner. The
// predicate also refuses a row that already carries a *different* session, so a
// bind can never silently replace an existing one; re-binding the same id stays
// possible.
//
// clearProgressPrefix empties a progress value that starts with it, in the same
// write. The prefix is evaluated against the stored value rather than the
// caller's snapshot: the spawn-time capture writes the "unresumable" marker from
// its own goroutine, and a bind that arrived just before that write must not
// leave the marker on a bound, resumable agent. An empty prefix leaves progress
// untouched.
func (s *Store) AgentBindSession(id, sid, clearProgressPrefix string) (bool, error) {
	// The id may arrive with the "opencode:"/"codex:" prefix the MCP layer
	// attaches. Readers strip it (see stripAgentPrefix), so the writer has to as
	// well: otherwise the row would store the prefixed form and no lookup — and
	// no later bind — could ever resolve it.
	sid = stripAgentPrefix(sid)
	if id == "" || sid == "" {
		return false, nil
	}
	set := "opencode_session_id = ?, progress = CASE WHEN ? <> '' AND progress LIKE ? THEN '' ELSE progress END"
	args := []any{sid, clearProgressPrefix, clearProgressPrefix + "%"}
	args = append(args, id, sid, id, sid)
	res, err := s.db.Exec(
		"UPDATE agents SET "+set+
			" WHERE id = ? AND (opencode_session_id = '' OR opencode_session_id = ?)"+
			" AND NOT EXISTS (SELECT 1 FROM agents o WHERE o.id <> ? AND o.opencode_session_id = ?)",
		args...)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentOwnedByOtherSession returns the id of the agent row that already holds
// this opencode session, when that row is not id.
//
// The question is "does *any* other row own it", so it is asked directly: the
// lookup helpers order by created_at, which is written with second granularity
// and therefore cannot decide between two rows created in the same second. The
// session id carries the same "opencode:"/"codex:" prefix tolerance as those
// helpers, because the MCP layer attaches it.
//
// The answer is advisory — callers that must act on it write through
// AgentBindSession, which carries the ownership test in the statement itself.
// A read failure therefore reports "no other owner" and leaves the decision to
// that write.
func (s *Store) AgentOwnedByOtherSession(sid, id string) (string, bool) {
	sid = stripAgentPrefix(sid)
	if sid == "" {
		return "", false
	}
	var owner string
	err := s.readerDB().QueryRow(
		`SELECT id FROM agents WHERE id <> ? AND opencode_session_id != '' AND opencode_session_id = ? LIMIT 1`,
		id, sid).Scan(&owner)
	if err != nil {
		return "", false
	}
	return owner, true
}

// AgentFinalizeStop writes a stop onto one agent row, but only while the row still
// holds expectedPID — the PID the caller's decision was made on.
//
// The relaunch goroutine binds a process after its own "spawning" write, so a stop
// that read the row before that bind must not overwrite the binding it never saw:
// the refusal tells the caller that a new process is there, so it can end it. PID
// and status move in one statement, so no bind can slip between reading and writing
// them. A NULL pid column (the schema allows it) counts as 0, so such a row stays
// as stoppable as every other. Reports whether the row was written.
func (s *Store) AgentFinalizeStop(id string, expectedPID int, stoppedAt, progress string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'stopped', stopped_at = ?, progress = ?, pid = 0, sock_path = ''
		 WHERE id = ? AND COALESCE(pid, 0) = ?`,
		stoppedAt, progress, id, expectedPID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentCloseUnboundSpawn closes a row whose process bind was refused while the
// row still claims to be spawning. The CLI that registers itself stops the child
// and moves on, so the daemon's exit tail never runs for this case: the row would
// stay "spawning" with pid 0, every guard skips it, and AgentGetActiveBySection
// still counts it as the section's owner — the next spawn for that section is
// refused. Conditional on the spawn states so a stop or a fresh bind that landed
// in between wins, and on ownerSession so a caller cannot close a row it does not
// own (YM-356). Reports whether the row was written.
func (s *Store) AgentCloseUnboundSpawn(id, ownerSession, reason, stoppedAt string) (bool, error) {
	if id == "" {
		return false, nil
	}
	// The owner is part of the statement unconditionally: the only caller is
	// register_agent, which refuses a request without a session id before it gets
	// here, and a spawn that never bound a process has no exit tail to close its
	// own row.
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'error', error = ?, stopped_at = ?
		 WHERE id = ? AND status IN ('spawning', 'pending') AND session_id = ?`,
		reason, stoppedAt, id, ownerSession)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentClaimCrash takes ownership of a crash recovery in one statement: the row
// must still be running and must still hold the PID the snapshot saw. The
// recovery then quarantines the session, taints the scratchpad and starts a
// replacement — none of which may happen for a row a stop closed in the meantime,
// and a compare-on-read cannot close that window because the stop can land
// between the read and the write. Whoever wins this claim is the only actor; the
// loser does nothing (YM-356, orchestrator review).
//
// The retry count moves with the claim so a refused claim cannot bump it.
// Reports whether the row was claimed.
func (s *Store) AgentClaimCrash(id string, expectedPID int, stoppedAt, reason string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'crashed', stopped_at = ?, error = ?, retry_count = retry_count + 1
		 WHERE id = ? AND status = 'running' AND COALESCE(pid, 0) = ?`,
		stoppedAt, reason, id, expectedPID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentClaimRespawn takes ownership of a crash replacement in one statement: the row
// must still be crashed, and a claim that is already there is refused unless it is
// older than staleBefore (a daemon that died mid-respawn leaves one behind). The stop
// path accepts a claimed row as dormant, so the stop and the replacement cannot both
// win: whoever moves the row first is the only actor (YM-397).
//
// The claim deliberately does not use an active status: AgentGetActiveBySection only
// counts running/pending/spawning/paused, and a claim that owned the section would make
// the replacement's own spawn fail with "section already has active agent".
func (s *Store) AgentClaimRespawn(id, note, claimedAt, staleBefore string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'respawn_claimed', progress = ?, last_restart_at = ?
		 WHERE id = ? AND (status = 'crashed'
		   OR (status = 'respawn_claimed' AND COALESCE(last_restart_at, '') < ?))`,
		note, claimedAt, id, staleBefore)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentReleaseRespawn returns a claimed row to crashed, so the next recovery pass can
// take it again. Reports whether the row was still claimed.
func (s *Store) AgentReleaseRespawn(id, note string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'crashed', progress = ? WHERE id = ? AND status = 'respawn_claimed'`,
		note, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentCloseReplaced closes a claimed row as replaced by newID. It reports false when the
// claim is gone, which means a stop (or a fresh claim) moved the row while the
// replacement was starting: the caller then decides about the replacement, it does not
// write over the other actor's decision (YM-397).
func (s *Store) AgentCloseReplaced(id, newID, stoppedAt string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'stopped', stopped_at = ?, progress = ?
		 WHERE id = ? AND status = 'respawn_claimed'`,
		stoppedAt, "stopped: replaced by "+newID, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentCloseIfUnsupervised closes a row that no supervisor can act on any more: a crash
// whose replacement never appeared, or a respawn claim whose claimer died. Both states
// exist only between two steps of the recovery, so a row still holding one long afterwards
// has lost its only owner (YM-397, cold review Los 56h). Reports whether the row was
// written.
func (s *Store) AgentCloseIfUnsupervised(id, note string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE agents SET status = 'failed', error = ? WHERE id = ? AND status IN ('crashed', 'respawn_claimed')`, note, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentClosePausedIfDead closes a paused row only while it is still paused and
// still carries the PID the caller's decision was made on.
//
// A PID-only predicate is not enough for a caller that frees a section: a resume
// writes "pending" (with pid 0) before it binds, so a closure that compared only
// the PID could overwrite a takeover already under way, and a second spawn would
// then find the row closed and create a duplicate for the same section. Both
// conjuncts move in one statement, so no transition can slip between them.
// Reports whether the row was written.
func (s *Store) AgentClosePausedIfDead(id string, expectedPID int, stoppedAt, progress string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE agents SET status = 'stopped', stopped_at = ?, progress = ?, pid = 0, sock_path = ''
		 WHERE id = ? AND status = 'paused' AND COALESCE(pid, 0) = ?`,
		stoppedAt, progress, id, expectedPID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentFinalizeExit applies a spawn's exit state to the row, but only while the
// row does not name a different process. The pid and the socket path belong to
// the agent id, not to one process: a resume or a restart binds a successor while
// the previous process is still winding down, and an unconditional exit tail then
// cleared the live agent's pid, socket path and status (YM-70).
//
// A row that names no process at all is still ours to close — that is the spawn
// whose bind was refused, and leaving it "pending" would keep the section owned
// by a row with no process. pid and sock_path are cleared unconditionally with
// the pid check, so fields must not carry them. Reports whether the row was
// written.
func (s *Store) AgentFinalizeExit(id string, expectedPID int, fields map[string]any) (bool, error) {
	if id == "" || expectedPID <= 0 {
		return false, nil
	}
	// Fixed column order so the statement is deterministic (map iteration is not).
	// Only these three columns move: pid and sock_path are cleared by the pid check
	// itself, and every other key is REFUSED rather than dropped — this write
	// records that an agent ended, and a key this function cannot move must not
	// look like a successful exit.
	var setClauses []string
	var args []any
	for _, k := range []string{"status", "error", "stopped_at"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	for k := range fields {
		switch k {
		case "status", "error", "stopped_at":
		default:
			return false, fmt.Errorf("disallowed field for the exit state: %q", k)
		}
	}
	// The pid and the socket path are cleared in every case: the statement is the
	// process release, and an exit that leaves the row pointing at a pid nobody
	// owns hands a recycled pid to AgentGetByPID. A terminal row (status failed,
	// stopped_at set) therefore still loses its pid here, exactly as it did when
	// the exit tail wrote the fields unconditionally.
	//
	// The "row names no process" branch is limited to the live statuses for the
	// other direction: a stop clears the pid deliberately, and an unbounded
	// "pid is 0" also matched that closed row — a late exit then flipped "stopped"
	// to "failed" and wrote an error onto an agent the operator had ended (YM-244).
	setClauses = append(setClauses, "pid = 0", "sock_path = ''")
	args = append(args, id, expectedPID)
	res, err := s.db.Exec(`UPDATE agents SET `+strings.Join(setClauses, ", ")+
		` WHERE id = ? AND (COALESCE(pid, 0) = ?
		   OR (COALESCE(pid, 0) = 0 AND status IN (`+agentLiveStatusesWithLegacy+`)))`, args...)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentBindProcessIfLive binds a process (pid + socket + spawn marker) to an
// agent row in one statement, but only while the row still claims to be live.
//
// The relaunch runs in its own goroutine, so a stop_agent can land between the
// row's "spawning" write and this bind. An unconditional write flipped the
// stopped row back to "running" and left a backend running that the orchestrator
// believed it had ended — and which no row owned any more. Terminal statuses are
// therefore refused, and the caller has to deal with the process it created.
//
// ownerSession is the row's identity: the session id the spawn caller received
// (neither list_agents nor get_agent return it). With it the statement is also
// bound to that owner, so a caller cannot take over a row it did not name
// (YM-356). It is an integrity guard, not a security boundary: the same value
// also reaches the spawned child as its session identity (YESMEM_SESSION_ID /
// --session-id), so a same-uid process can read it from /proc/<pid>/environ. What
// it stops is a blind or wrong-row bind, not a local process that is willing to
// inspect the agent it is targeting (cold review 5.4).
//
// An empty ownerSession (daemonOwnsTheBind in the daemon) marks the daemon's own
// spawn path, which binds the child it just created and needs no row identity —
// the identity check for that PID happens in Handler.bindAgentProcess.
// Reports whether the row was written.
func (s *Store) AgentBindProcessIfLive(id string, pid int, sockPath, lastRestartAt, ownerSession string) (bool, error) {
	if id == "" {
		return false, nil
	}
	query := `UPDATE agents SET pid = ?, sock_path = ?, status = 'running', last_restart_at = ?
		 WHERE id = ? AND status IN (` + agentLiveStatuses + `)`
	args := []any{pid, sockPath, lastRestartAt, id}
	if ownerSession != "" {
		query += ` AND session_id = ?`
		args = append(args, ownerSession)
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentSetProgressIfUnbound records progress only while the agent has no backend
// session, in one statement. The spawn-time capture writes its "unresumable"
// marker up to 150s after the spawn, and a bind arriving from another path in
// between would otherwise leave a bound, resumable agent labelled unresumable.
// Reports whether the row was written.
func (s *Store) AgentSetProgressIfUnbound(id, progress string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec("UPDATE agents SET progress = ? WHERE id = ? AND opencode_session_id = ''", progress, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentGet returns an agent by ID.
func (s *Store) AgentGet(id string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE id = ?`, id))
}

// AgentGetBySection returns the most recent agent for a project+section combo.
func (s *Store) AgentGetBySection(project, section string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE project = ? AND section = ? ORDER BY created_at DESC LIMIT 1`, project, section))
}

// AgentGetActiveBySection returns the most recent active agent for a project+section combo.
// Active means the agent still owns the section and blocks spawning a new one.
func (s *Store) AgentGetActiveBySection(project, section string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents
		WHERE project = ? AND section = ? AND status IN ('running', 'pending', 'spawning', 'paused', 'frozen')
		ORDER BY created_at DESC LIMIT 1`, project, section))
}

// AgentCloseIfCrashed closes a crashed row only while it still claims to be
// crashed. The supervision reads a snapshot and writes later, and stop_agent
// accepts a crashed row (it is dormant), so an unconditional write would revive a
// deliberate stop as "failed" and attach a reason that no longer applies. Reports
// whether the row was written.
func (s *Store) AgentCloseIfCrashed(id, note string) (bool, error) {
	if id == "" {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE agents SET status = 'failed', error = ? WHERE id = ? AND status = 'crashed'`, note, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// AgentList returns all agents, optionally filtered by project.
func (s *Store) AgentList(project string) ([]Agent, error) {
	var rows *sql.Rows
	var err error
	const q = `SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0) FROM agents`
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

// stripAgentPrefix removes the "opencode:" / "codex:" / "claude:" prefix that
// the MCP layer (resolveClientSessionID in internal/mcp/server.go) and the proxy
// persist (active_session_opencode). The agents table stores bare ids in
// opencode_session_id / codex_session_id, so any caller-side prefixed value
// must be stripped here at the storage boundary.
func stripAgentPrefix(sid string) string {
	if s, ok := strings.CutPrefix(sid, "opencode:"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(sid, "codex:"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(sid, "claude:"); ok {
		return s
	}
	return sid
}

// AgentGetBySession returns the running agent for a given session ID.
// Matches session_id OR opencode_session_id OR codex_session_id so spawned
// non-claude agents (synthetic session_id, real backend id in the dedicated
// columns) are resolvable. The sessionID parameter may carry an
// "opencode:"/"codex:" prefix (as attached by the MCP layer and proxy); the
// prefix is stripped here so callers do not need to know the storage layout.
func (s *Store) AgentGetBySession(sessionID string) (*Agent, error) {
	lookupID := stripAgentPrefix(sessionID)
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE (session_id = ? OR (opencode_session_id != '' AND opencode_session_id = ?) OR (codex_session_id != '' AND codex_session_id = ?)) AND status = 'running' ORDER BY created_at DESC LIMIT 1`, lookupID, lookupID, lookupID))
}

// AgentGetAnyBySession returns the most recent agent for a given session ID,
// regardless of status. Matches session_id OR opencode_session_id OR
// codex_session_id and tolerates "opencode:"/"codex:" prefixes — see
// AgentGetBySession rationale.
func (s *Store) AgentGetAnyBySession(sessionID string) (*Agent, error) {
	lookupID := stripAgentPrefix(sessionID)
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE session_id = ? OR (opencode_session_id != '' AND opencode_session_id = ?) OR (codex_session_id != '' AND codex_session_id = ?) ORDER BY created_at DESC LIMIT 1`, lookupID, lookupID, lookupID))
}

// AgentGetByPID returns the running agent whose spawned backend PID matches.
// Used by resolveSessionID's _caller_pid fallback: the MCP wrapper sends
// os.Getppid() (= backend PID) with every request, and agents.pid stores
// bridge.Cmd.Process.Pid which is exactly that backend's PID. Restricted to
// status='running' so PIDs cannot be reused by the OS to resurrect a stale
// mapping after an agent process exits.
func (s *Store) AgentGetByPID(pid int) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE pid = ? AND status = 'running' ORDER BY created_at DESC LIMIT 1`, pid))
}

// AgentGetByProxyThreadID returns the most recent running agent whose proxy_thread_id matches.
// Used by _track_usage to resolve agent telemetry when the proxy's threadID
// differs from the daemon-generated session_id.
func (s *Store) AgentGetByProxyThreadID(proxyThreadID string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE proxy_thread_id = ? AND status = 'running' ORDER BY created_at DESC LIMIT 1`, proxyThreadID))
}

// AgentGetRunningInProject returns the first running agent in a project with an empty proxy_thread_id.
// Used for lazy initialization: when _track_usage sees a new threadID, it maps it to the
// most recently created running agent in the same project that hasn't been mapped yet.
func (s *Store) AgentGetRunningInProject(project string) (*Agent, error) {
	return s.scanAgent(s.readerDB().QueryRow(
		`SELECT id, project, section, session_id, pid, sock_path, status, caller_session, error, heartbeat_at, progress, relay_count, depth, token_budget, retry_count, COALESCE(backend, 'claude') as backend, COALESCE(turns_used, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(last_activity_at, ''), COALESCE(phase, 'idle'), created_at, stopped_at, restart_strategy, restart_count, max_restarts, liveness_ping_at, last_restart_at, COALESCE(proxy_thread_id, ''), COALESCE(codex_session_id, ''), COALESCE(opencode_session_id, ''), COALESCE(permission_kick, ''), COALESCE(model, ''), COALESCE(theme, ''), COALESCE(work_dir, ''), COALESCE(max_turns, 0)
		FROM agents WHERE project = ? AND status = 'running' AND (proxy_thread_id IS NULL OR proxy_thread_id = '') ORDER BY created_at DESC LIMIT 1`, project))
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

// PruneStoppedAgents deletes the closed agent rows that no process owns any more
// and whose closure lies before the cutoff, and returns how many it removed.
//
// Three predicates are deliberate. status IN ('stopped','failed') keeps every live
// state out. COALESCE(pid, 0) = 0 keeps rows that still name a process — the stop
// paths clear pid, so in practice this catches legacy and crash-recovered rows, not
// rows whose process outlived a normal close. COALESCE(orphan_reattachable, 0) = 0
// keeps the rows AgentRecoverOrphaned closed at daemon start: those are the only
// handle relay, stop, whoami and send_to have on a backend session that still
// exists (YM-297). That set is unbounded by design — a host that keeps
// crash-restarting accumulates them — so the marker needs a field no caller can
// rewrite. It used to live in progress, which a later stop of such a row
// overwrites (and update_agent accepts from any socket caller), and it sat in a
// field the operator reads; orphan_reattachable is written only by the recovery
// sweep and read only here (YM-333). The age comparison runs
// through julianday() over a coalesce of stopped_at (falling back to created_at):
// the table carries two timestamp shapes (the schema default datetime('now','localtime')
// without an offset, and RFC3339 with offset from the stop paths), and a lexical
// comparison sorts both wrongly. A row whose timestamp parses in neither shape
// stays.
func (s *Store) PruneStoppedAgents(before time.Time) (int64, error) {
	result, err := s.db.Exec(
		`DELETE FROM agents
		 WHERE status IN ('stopped', 'failed')
		   AND COALESCE(pid, 0) = 0
		   AND COALESCE(orphan_reattachable, 0) = 0
		   AND julianday(COALESCE(NULLIF(stopped_at, ''), created_at)) < julianday(?)`,
		before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// AgentPruneRetention is how long a closed agent row stays visible before the
// periodic cleanup drops it: long enough to read the outcome of a loop, short
// enough that the table does not grow with every probe and crash.
const AgentPruneRetention = 30 * 24 * time.Hour

// AgentRecoverOrphaned checks all running/pending/spawning agents after daemon restart.
//
// A living process (isAlive) keeps its row as is. A row whose process is gone
// but whose backend session still exists for its own work dir (sessionResumable,
// keyed by opencode_session_id plus work_dir) is closed as stopped and kept: the
// session keeps running in the backend's own service, and deleting the row drops
// it out of supervision — relay, stop, whoami and send_to all resolve through it
// (YM-297). Only a row with neither a process nor a session is deleted; a lookup
// that fails or runs out of budget keeps the row, because an unreadable store is
// no evidence that the session is gone.
//
// Returns (reconnected, reattachable, deleted) counts.
func (s *Store) AgentRecoverOrphaned(isAlive func(pid int) bool, sessionResumable func(opencodeSessionID, workDir string) (bool, error)) (int, int, int, error) {
	rows, err := s.readerDB().Query(
		`SELECT id, pid, COALESCE(opencode_session_id, ''), COALESCE(work_dir, '') FROM agents WHERE status IN ('running', 'pending', 'spawning')`)
	if err != nil {
		return 0, 0, 0, err
	}

	type closeRow struct {
		id     string
		reason string
	}
	var reconnected, reattachable, deleted int
	var toDelete []string
	var toClose []closeRow
	// One budget for the whole sweep: the lookup shells out to the sqlite3 CLI,
	// and this runs before the socket exists, so a slow store must not hold the
	// port hostage. Rows the budget cuts off are kept, not deleted.
	budget := time.Now().Add(recoverySessionBudget)
	for rows.Next() {
		var id, sid, workDir string
		var pid int
		if err := rows.Scan(&id, &pid, &sid, &workDir); err != nil {
			continue
		}
		if pid > 0 && isAlive(pid) {
			reconnected++
			continue
		}
		if sid == "" {
			toDelete = append(toDelete, id)
			continue
		}
		if sessionResumable == nil {
			// No way to ask: keep the row. Deleting on a missing dependency is
			// the harm this recovery exists to avoid.
			toClose = append(toClose, closeRow{id, agentOrphanReasonUnverified})
			continue
		}
		if time.Now().After(budget) {
			toClose = append(toClose, closeRow{id, agentOrphanReasonUnverified})
			continue
		}
		resumable, lerr := sessionResumable(sid, workDir)
		if lerr != nil {
			toClose = append(toClose, closeRow{id, agentOrphanReasonUnverified})
			continue
		}
		if !resumable {
			toDelete = append(toDelete, id)
			continue
		}
		toClose = append(toClose, closeRow{id, agentOrphanReasonVerified})
	}
	// The cursor is closed before the writes: on a single-connection DB a write
	// inside an open cursor deadlocks.
	rows.Close()

	now := time.Now().Format(time.RFC3339)
	for _, r := range toClose {
		// Conditional on the status this sweep read: a stop or a fresh spawn that
		// landed in between must not be overwritten.
		res, err := s.db.Exec(`UPDATE agents SET status = 'stopped', pid = 0, sock_path = '', stopped_at = ?, progress = ?, error = ?, orphan_reattachable = 1
			WHERE id = ? AND status IN ('running', 'pending', 'spawning')`,
			now, agentOrphanProgress, r.reason, r.id)
		if err != nil {
			log.Printf("storage: agent recovery could not close %q: %v", r.id, err)
			continue
		}
		// The guard can match 0 rows (a stop or a fresh spawn landed in between);
		// that is not a kept row, so it must not be counted as one.
		if n, nerr := res.RowsAffected(); nerr == nil && n == 0 {
			continue
		}
		reattachable++
	}
	for _, id := range toDelete {
		if _, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id); err != nil {
			log.Printf("storage: agent recovery could not delete %q: %v", id, err)
			continue
		}
		deleted++
	}
	return reconnected, reattachable, deleted, nil
}

// Recovery keeps a row it cannot verify: the status and progress below say "no
// process owns this row" without claiming the work ended, because the backend
// session may still be answering turns. The durable half of that statement is the
// orphan_reattachable column — progress is a human-readable field that stop paths
// and update_agent rewrite.
const (
	agentOrphanProgress         = "orphaned-reattachable"
	agentOrphanReasonVerified   = "daemon restart: process gone, backend session still on disk — resume_agent reattaches while no other agent owns the section"
	agentOrphanReasonUnverified = "daemon restart: process gone, session state unverifiable — row kept, resume_agent reattaches while no other agent owns the section"
)

// recoverySessionBudget bounds the whole session lookup sweep at daemon start.
const recoverySessionBudget = 10 * time.Second

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

// MigrateAgentsSchema adds telemetry columns to existing agents tables and
// closes legacy "frozen" rows (idempotent).
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
		`ALTER TABLE agents ADD COLUMN proxy_thread_id   TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN codex_session_id   TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN opencode_session_id TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN permission_kick    TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN model             TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN theme             TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN work_dir          TEXT    DEFAULT ''`,
		`ALTER TABLE agents ADD COLUMN max_turns         INTEGER DEFAULT 0`,
		`ALTER TABLE agents ADD COLUMN orphan_reattachable INTEGER DEFAULT 0`,
	}
	for _, m := range migrations {
		_, err := s.db.Exec(m)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migration %q: %w", m, err)
		}
	}

	// Carry the reattach marker over from the field that used to hold it. Before
	// orphan_reattachable existed, AgentRecoverOrphaned wrote the marker into
	// progress; the ALTER leaves those rows at 0 and the recovery sweep only looks
	// at live rows, so without this backfill the first prune after the upgrade
	// deletes exactly the rows the marker protects (YM-333, found by the cold
	// review). Narrow on purpose: only terminal rows, only the old marker value,
	// and only where the column is still unset — a re-run changes nothing.
	if _, err := s.db.Exec(`UPDATE agents
		SET orphan_reattachable = 1
		WHERE COALESCE(orphan_reattachable, 0) = 0
		  AND status IN ('stopped', 'failed')
		  AND COALESCE(progress, '') LIKE 'orphaned-%'`); err != nil {
		return fmt.Errorf("carry over the reattach marker: %w", err)
	}

	// Close the legacy "frozen" rows. The status was split into paused/stopped,
	// but normalizeAgentStatus still reads "frozen" as "paused" — so those rows
	// were treated as live: their stale PID was signalable by stop_agent and the
	// paused-row guards kept watching a process that no longer exists. The
	// created_at bound keeps the destructive clause from outliving its
	// precondition: no writer emits "frozen" any more, so a row stamped at or
	// after the cutoff can only come from a stale binary, and that row is not ours
	// to close. A row with no usable stamp is closed: created_at has a default and
	// every writer sets it, so an empty value means a hand-made row, and leaving it
	// "frozen" would keep the very state this migration exists to remove.
	// Idempotent by construction: after the UPDATE no matching row carries
	// "frozen". normalizeAgentStatus stays as the read-side safety net for rows
	// written outside a migration run.
	if _, err := s.db.Exec(`UPDATE agents
		SET status = 'stopped',
		    pid = 0,
		    sock_path = '',
		    stopped_at = COALESCE(NULLIF(stopped_at, ''), ?),
		    progress = 'migrated: legacy frozen'
		WHERE status = 'frozen'
		  AND (created_at IS NULL OR created_at = '' OR created_at < ?)`,
		time.Now().Format(time.RFC3339), legacyFrozenCutoff); err != nil {
		return fmt.Errorf("migrate legacy frozen agents: %w", err)
	}
	return nil
}

// seedAgentIDSeq sets every existing day's counter above the highest id that day
// already carries. The upsert keeps it monotonic: it is idempotent on repeated
// opens and repairs a counter that an older binary or a restore left behind,
// which a plain INSERT OR IGNORE would never raise (YM-151 review).
const seedAgentIDSeq = `INSERT INTO agent_id_seq (day, next)
	SELECT substr(id, 7, 8), MAX(CAST(substr(id, 16) AS INTEGER)) + 1
	FROM agents
	WHERE id LIKE 'agent-________-%'
	GROUP BY substr(id, 7, 8)
	ON CONFLICT(day) DO UPDATE SET next = MAX(next, excluded.next)`

// AgentNextID returns a globally-unique agent ID of form "agent-<YYYYMMDD>-NN"
// where NN is a per-day counter (zero-padded to 2 digits). IDs are never
// recycled — removing an agent does not free its slot. The daily counter
// resets at UTC midnight.
//
// The counter lives in agent_id_seq and is advanced by one atomic statement: the
// earlier MAX()+1 over the reader connection handed the same id to two concurrent
// spawns, and the loser of the race failed on the primary key (YM-151). The
// project argument is unused; it stays for the caller signature.
func (s *Store) AgentNextID(project string) (string, error) {
	today := time.Now().UTC().Format("20060102")
	var n int64
	err := s.db.QueryRow(
		`INSERT INTO agent_id_seq (day, next) VALUES (?, 2)
		 ON CONFLICT(day) DO UPDATE SET next = next + 1
		 RETURNING next - 1`,
		today,
	).Scan(&n)
	if err != nil {
		return "", fmt.Errorf("allocate agent id for %s: %w", today, err)
	}
	return fmt.Sprintf("agent-%s-%02d", today, n), nil
}

// AgentCascadeStop stops all agents in the supervision tree rooted at parentSessionID.
// Uses BFS over CallerSession links. Does NOT stop the parent itself.
// Returns the rows it closed, each still carrying the PID its process held, so the
// caller can end those processes: a row-only cascade left them running with no
// relay and no limit watching them (YM-66).
func (s *Store) AgentCascadeStop(parentSessionID string) ([]Agent, error) {
	// An empty root names no parent. The tree link is caller_session, and "no
	// caller" is what every root agent of the installation carries — a cascade on
	// "" therefore closed all of them. stop_agent of a row without a session id
	// passes exactly that value here, so the guard belongs in the store rather
	// than in one caller (YM-244).
	if parentSessionID == "" {
		return nil, nil
	}
	all, err := s.AgentList("")
	if err != nil {
		return nil, err
	}

	now := time.Now().Format(time.RFC3339)
	var stopped []Agent
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
			if a.Status == "running" || a.Status == "paused" || a.Status == "spawning" || a.Status == "pending" {
				// The pid and the socket path belong to the process this row names,
				// and that process is ending here. A closed row that keeps them hands
				// a recycled pid to AgentGetByPID and to every signal gate (YM-244).
				// The snapshot appended below keeps its pid: it is the caller's
				// signal target (see endCascadedProcesses).
				if err := s.AgentUpdate(a.ID, map[string]any{
					"status":     "stopped",
					"pid":        0,
					"sock_path":  "",
					"stopped_at": now,
				}); err == nil {
					stopped = append(stopped, a)
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
	var sessionID, sockPath, callerSession, errStr, heartbeat, progress, stoppedAt, lastActivityAt, restartStrategy, livenessPingAt, lastRestartAt, proxyThreadID, codexSessionID, opencodeSessionID, workDir sql.NullString
	var pid sql.NullInt64
	var restartCount, maxRestarts, maxTurns sql.NullInt64
	var permKick sql.NullString
	var model, theme sql.NullString
	err := row.Scan(&a.ID, &a.Project, &a.Section, &sessionID, &pid, &sockPath,
		&a.Status, &callerSession, &errStr, &heartbeat, &progress,
		&a.RelayCount, &a.Depth, &a.TokenBudget, &a.RetryCount, &a.Backend,
		&a.TurnsUsed, &a.InputTokens, &a.OutputTokens, &lastActivityAt, &a.Phase,
		&a.CreatedAt, &stoppedAt, &restartStrategy, &restartCount, &maxRestarts, &livenessPingAt, &lastRestartAt, &proxyThreadID, &codexSessionID, &opencodeSessionID, &permKick, &model, &theme, &workDir, &maxTurns)
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
	a.ProxyThreadID = proxyThreadID.String
	a.CodexSessionID = codexSessionID.String
	a.OpencodeSessionID = opencodeSessionID.String
	a.PermissionKick = permKick.String
	a.Model = model.String
	a.Theme = theme.String
	a.WorkDir = workDir.String
	a.MaxTurns = int(maxTurns.Int64)
	a.Status = normalizeAgentStatus(a.Status)
	return a, nil
}

// scanAgentRow scans a single agent from a *sql.Rows.
func (s *Store) scanAgentRow(rows *sql.Rows) (Agent, error) {
	var a Agent
	var sessionID, sockPath, callerSession, errStr, heartbeat, progress, stoppedAt, lastActivityAt, restartStrategy, livenessPingAt, lastRestartAt, proxyThreadID, codexSessionID, opencodeSessionID, workDir sql.NullString
	var pid sql.NullInt64
	var restartCount, maxRestarts, maxTurns sql.NullInt64
	var permKick sql.NullString
	var model, theme sql.NullString
	err := rows.Scan(&a.ID, &a.Project, &a.Section, &sessionID, &pid, &sockPath,
		&a.Status, &callerSession, &errStr, &heartbeat, &progress,
		&a.RelayCount, &a.Depth, &a.TokenBudget, &a.RetryCount, &a.Backend,
		&a.TurnsUsed, &a.InputTokens, &a.OutputTokens, &lastActivityAt, &a.Phase,
		&a.CreatedAt, &stoppedAt, &restartStrategy, &restartCount, &maxRestarts, &livenessPingAt, &lastRestartAt, &proxyThreadID, &codexSessionID, &opencodeSessionID, &permKick, &model, &theme, &workDir, &maxTurns)
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
	a.ProxyThreadID = proxyThreadID.String
	a.CodexSessionID = codexSessionID.String
	a.OpencodeSessionID = opencodeSessionID.String
	a.PermissionKick = permKick.String
	a.Model = model.String
	a.Theme = theme.String
	a.WorkDir = workDir.String
	a.MaxTurns = int(maxTurns.Int64)
	a.Status = normalizeAgentStatus(a.Status)
	return a, nil
}

// normalizeAgentStatus translates the legacy "frozen" status to "paused" on read.
// "frozen" was split into "paused" (process alive, waiting) and "stopped"
// (process dead/permanently failed). Old DB rows that still carry "frozen" are
// conservatively treated as "paused" — better to allow relay on an uncertain
// row than to wrongly declare an agent dead.
func normalizeAgentStatus(s string) string {
	if s == "frozen" {
		return "paused"
	}
	return s
}
