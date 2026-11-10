package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/channeltext"
	"github.com/carsteneu/yesmem/internal/orchestrator"
	"github.com/carsteneu/yesmem/internal/storage"
)

// opencodeUnresumableMsg is the agent-record signal written when an opencode
// backend agent has no OpencodeSessionID. Resume in that state would fall
// back to the daemon's 36-char UUID, which opencode rejects with exit 1
// (Learning #80228). Written to both error (terminal signal at resume time)
// and progress (live signal at poll-failure time so the orchestrator can see
// the agent will not be resumable later).
const opencodeUnresumableMsg = "unresumable: opencode session id never reported"

// promptFieldUnsafe reports whether a caller-supplied value cannot be rendered
// into the spawn prompt. The prompt is typed into the agent's TUI, so quotes end
// the quoted argument it embeds (project='%s', section='%s', target="%s"),
// newlines and other control characters submit extra lines, and bidi overrides
// let a value read as something else than it is (YM-211). The rule itself lives in
// orchestrator.PromptFieldUnsafe, shared with the CLI's prompt builder and the
// relay prefix.
func promptFieldUnsafe(s string) bool {
	return orchestrator.PromptFieldUnsafe(s)
}

// promptLabel renders a caller-supplied name into a line that is typed into another
// agent's TUI. It is a label, not identity, so an unusable value is replaced
// instead of refused: the message it announces is already stored and must still be
// delivered (YM-255, same class as the relay prefix). The value that survives goes
// through channeltext.Field — the same flatten-and-bound rule every rendered
// header field uses (YM-264).
func promptLabel(s string) string {
	if s == "" || orchestrator.PromptFieldUnsafe(s) || strings.ContainsAny(s, "[]") {
		return "another session"
	}
	// Field can come back empty for a value made only of invisible characters; the
	// line is typed into a terminal, so it must not read "from  —".
	if label := channeltext.Field(s); label != "" {
		return label
	}
	return "another session"
}

// spawnLabelLimit caps a label that is derived from caller input and rendered to
// a terminal. A section or project name is display text there, not identity.
const spawnLabelLimit = 64

// agentSpawnLabel renders a project or section into a value that may be handed to
// a terminal: the control, C1 and bidi characters safeAgentText strips would
// otherwise be executed as terminal commands or forge a window title, and the
// length is capped so a long path cannot crowd the window list (YM-416). The
// stored row keeps the raw value — section lookups, scratchpad sections and the
// agent marker in the spawn prompt all depend on it.
func agentSpawnLabel(s string) string {
	clean := safeAgentText(s)
	if r := []rune(clean); len(r) > spawnLabelLimit {
		return string(r[:spawnLabelLimit])
	}
	return clean
}

// takeOverDeadPause closes a paused row whose process is provably gone so its
// section can be spawned again. Reports whether the row was closed.
//
// Only a decided death qualifies (pidDecidedlyDead): a live PID that merely does
// not identify as this agent may be the agent itself, and freeing its section
// would start a second process for it — the same reasoning that makes
// crashRecovery withhold recovery for that case. reapGoneAgents stays the periodic
// path that closes such a row after its one-day grace; this is the on-demand path a
// caller needs when it wants the section now.
func (h *Handler) takeOverDeadPause(a storage.Agent) bool {
	if a.Status != "paused" || !pidDecidedlyDead(a.PID) {
		return false
	}
	reason := "auto-stopped: the pause never had a process"
	if a.PID != 0 {
		reason = fmt.Sprintf("auto-stopped: process %d is gone", a.PID)
	}
	// Conditional on the pause still being the one this decision was made on — both
	// the status and the PID move in one statement (AgentClosePausedIfDead), so a
	// resume that is taking the row over wins over this closure. The write also
	// clears the pid and socket path, which belong to a process that is gone.
	closed, err := h.store.AgentClosePausedIfDead(a.ID, a.PID, time.Now().Format(time.RFC3339), reason)
	if err != nil {
		log.Printf("[spawn] closing the dead pause of %s failed: %v", a.ID, err)
		return false
	}
	if !closed {
		return false
	}
	// The section is free now. Any children of the closed row report to a session
	// with no process; detectOrphanedAgents treats a stopped parent as dead and
	// stops them after its grace — the policy for every dead parent.
	log.Printf("[spawn] section %q was owned by paused agent %s with no process (PID %d) — row closed, section taken over", a.Section, a.ID, a.PID)
	return true
}

// handleSpawnAgent creates a DB record and starts the full PTY bridge + terminal.
// The CLI uses these to create the actual PTY bridge + terminal.
func (h *Handler) handleSpawnAgent(params map[string]any) Response {
	project, _ := params["project"].(string)
	section, _ := params["section"].(string)
	callerSession, _ := params["caller_session"].(string)
	backend, _ := params["backend"].(string)
	if backend == "" {
		if h.agentDefaultBackend != "" {
			backend = h.agentDefaultBackend
		} else {
			backend = "claude"
		}
	}
	if !allowedAgentBackends[backend] {
		return errorResponse(fmt.Sprintf("backend %q is not supported (claude|codex|opencode)", backend))
	}
	tokenBudget := 0
	if tb, ok := params["token_budget"].(float64); ok && tb > 0 {
		tokenBudget = int(tb)
	}
	maxTurns := 0
	if mt, ok := params["max_turns"].(float64); ok && mt > 0 {
		maxTurns = int(mt)
	}
	// The restart strategy decides whether attemptRestart (heartbeat) brings the
	// agent back after a failed spawn. No path wrote this column, so every row
	// read back as "" and the guard skipped all of them (YM-68).
	//
	// "permanent" is deliberately not accepted here: attemptRestart exempts that
	// strategy from MaxRestarts, and nothing above it caps a failing spawn loop
	// (the limits only look at running rows), so a caller-supplied flag could
	// restart an agent forever. A caller gets bounded policies; the uncapped one
	// stays a supervisor decision on the row.
	restartStrategy, _ := params["restart_strategy"].(string)
	switch restartStrategy {
	case "":
		restartStrategy = "temporary"
	case "temporary", "transient":
	default:
		return errorResponse(fmt.Sprintf("restart_strategy %q is not temporary|transient (permanent is a supervisor-only policy)", restartStrategy))
	}
	maxRestarts := 0
	if mr, ok := params["max_restarts"].(float64); ok && mr > 0 {
		maxRestarts = int(mr)
	}
	// A transient agent without a cap would be stopped on its first failure
	// (restart_count 0 >= max_restarts 0), so the cap defaults to 3. It is written
	// for every strategy: the INSERT overrides the column default, and a row that
	// later flips to transient through update_agent would otherwise carry a cap of
	// zero.
	if maxRestarts == 0 {
		maxRestarts = 3
	}
	model, _ := params["model"].(string)
	theme, _ := params["theme"].(string)
	workDir, _ := params["work_dir"].(string)
	// A loop worktree that is gone is the shape of a merged section: its branch is in
	// main and the orchestrator removed the tree. A process started there comes up in
	// the daemon's run directory without a repository, redoes phases of a finished task
	// and is reachable by nobody (observed 18 times on 18./20.10.2026, YM-385). Only a
	// worktree-shaped path is refused: a plain project directory may be created later.
	// A checkout the orchestrator placed outside a ".worktrees" directory passes this
	// gate; the heartbeat's removed-tree reaper covers it by observation instead.
	if workDir != "" && filepath.IsAbs(workDir) && strings.Contains(workDir, "/.worktrees/") {
		if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
			return errorResponse(fmt.Sprintf("work_dir %q does not exist — the section's worktree is gone (already merged?); create it before the spawn", workDir))
		}
	}
	permissionKick, _ := params["permission_kick"].(string)
	if permissionKick != "" && permissionKick != "on" && permissionKick != "off" {
		permissionKick = "" // ungültige Werte erben vom Config-Default
	}
	parentPid := callerPidParam(params)
	// Read before the resolveSessionID fallback below: only an authoritative
	// session id may select the caller's window.
	parentWindow := h.callerWindowFromParams(params)

	// Auto-resolve caller session from MCP context if not explicitly set
	if callerSession == "" {
		callerSession = h.resolveSessionID(params, "caller_session")
	}

	// The three values below are rendered into the spawn prompt, which is typed
	// into the agent's TUI as keyboard input: a quote ends the quoted argument and
	// a newline submits the line, so either would let the caller append
	// instructions to the new agent (YM-211, same class as YM-195). They are
	// refused rather than silently rewritten — a caller passing them has a bug.
	for _, f := range []struct{ name, value string }{
		{"caller_session", callerSession},
		{"project", project},
		{"section", section},
	} {
		if promptFieldUnsafe(f.value) {
			return errorResponse(fmt.Sprintf("%s contains characters that cannot be rendered into the agent prompt (quotes, control or bidi characters)", f.name))
		}
	}

	if project == "" {
		return errorResponse("project required")
	}
	if section == "" {
		return errorResponse("section required")
	}
	if existing, err := h.store.AgentGetActiveBySection(project, section); err == nil && existing != nil {
		// "paused" means a live process behind an open PTY bridge, waiting for a
		// relay (Learning #81181), and a paused row counts as the owner of its
		// section. When that process is provably gone the row cannot hold the
		// section: take it over here instead of leaving the caller blocked until
		// reapGoneAgents' one-day grace expires (YM-255).
		if !h.takeOverDeadPause(*existing) {
			return errorResponse(fmt.Sprintf("section %q already has active agent %s (status=%s)", section, existing.ID, existing.Status))
		}
	}

	id, err := h.store.AgentNextID(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("generate agent ID: %v", err))
	}

	sessionID := generateAgentUUID()

	// Calculate depth: if caller is an agent, inherit depth+1
	depth := 0
	if callerSession != "" {
		if callerAgent, err := h.store.AgentGetBySession(callerSession); err == nil {
			depth = callerAgent.Depth + 1
		}
	}

	// Enforce max_depth
	maxDepth := h.agentMaxDepth
	if maxDepth == 0 {
		maxDepth = 3
	}
	if depth >= maxDepth {
		return errorResponse(fmt.Sprintf("max spawn depth %d reached (current depth: %d)", maxDepth, depth))
	}

	// Token budget: only an explicit value counts. 0 means "no limit" — the
	// heartbeat's check skips TokenBudget <= 0 (D18). It is not a default that can
	// be applied here: a budget is a supervisor's decision per agent, and the unit
	// (output tokens) cannot be guessed from a context size.
	if tokenBudget == 0 && h.agentTokenBudget > 0 {
		tokenBudget = h.agentTokenBudget
	}

	// Resolve the backend's working directory before the row exists so the spawn
	// persists it: the late and on-demand session binds resolve an unbound
	// agent's session by this directory, and the project root alone is not
	// enough — a loop agent's session lives in its worktree.
	workDir = h.resolveAgentWorkDir(project, workDir)
	if workDir == "" {
		// The daemon's own cwd is not the agent's project, and inventing a
		// directory is how a second checkout came into existence (YM-286). The
		// caller can name one explicitly or pass a full project path.
		return errorResponse(fmt.Sprintf("project %q has no working directory: pass work_dir or an absolute project path", project))
	}

	agent := storage.Agent{
		ID:            id,
		Project:       project,
		Section:       section,
		SessionID:     sessionID,
		Status:        "pending",
		CallerSession: callerSession,
		Depth:         depth,
		TokenBudget:   tokenBudget,
		// The turn cap travels on the row for the same reason as the budget: a
		// replacement has to come back with the limit it was spawned with, and a
		// row that carries none falls back to the daemon's config default (YM-244).
		MaxTurns:       maxTurns,
		Backend:        backend,
		PermissionKick: permissionKick,
		// The restart policy is a spawn parameter like model and theme: the
		// supervisor needs it on the row to decide whether a failed spawn comes
		// back, and a replacement has to inherit it (respawnAgent).
		RestartStrategy: restartStrategy,
		MaxRestarts:     maxRestarts,
		// The requested spawn parameters are persisted so a replacement can come
		// back with them (respawnAgent) — the project cannot re-derive them.
		Model:   model,
		Theme:   theme,
		WorkDir: workDir,
	}

	if err := h.store.AgentCreate(agent); err != nil {
		return errorResponse(fmt.Sprintf("create agent: %v", err))
	}

	// Build agent prompt (backend-aware: codex gets softer preamble to prevent tool-loop)
	var prompt string
	switch backend {
	case "codex":
		prompt = fmt.Sprintf(
			"You are working on project '%s', section '%s'. "+
				"FIRST ACTION: Call whoami(). If codex_session_id is empty, retry 3 times with 5s sleep; if still empty note 'session id missing' in scratchpad and proceed. "+
				"Read scratchpad_read(project=\"%s\", section=\"%s\") for context, then act on it. "+
				"Write your results with scratchpad_write(project=\"%s\", section=\"%s\", content=...).",
			project, section, project, section, project, section,
		)
	default:
		prompt = fmt.Sprintf(
			"You are working on project '%s', agent '%s', section '%s'. "+
				"FIRST ACTION: Call whoami(). If opencode_session_id is empty, retry 3 times with 5s sleep; if still empty note 'session id missing' in scratchpad and proceed. "+
				"Then scratchpad_append(project=\"%s\", section=\"%s\", content=\"Status: started\") so the orchestrator sees you are working. "+
				"Read scratchpad_read(project=\"%s\", section=\"%s\") for context and work through the task. "+
				"Write your results with scratchpad_write(project=\"%s\", section=\"%s\", content=...).",
			project, id, section, project, section, project, section, project, section,
		)
	}
	if callerSession != "" {
		prompt += fmt.Sprintf(
			" When you are DONE, send send_to(target=\"%s\", content=\"DONE: Section '%s' in project '%s' is complete.\") to notify the main agent.",
			callerSession, section, project,
		)
	}
	if tokenBudget > 0 {
		prompt += fmt.Sprintf(" BUDGET: Max %d output tokens for this task — work efficiently, keep it concise.", tokenBudget)
	}

	// Start PTY bridge + terminal in background goroutine. The path goes through
	// agentSockPath for the same reason as every cleanup site: the unlink in the
	// bridge must only ever see a path the daemon derived (YM-287).
	sockPath, pathResp := h.agentSockPathForSpawn(id)
	if pathResp.Error != "" {
		return pathResp
	}
	if !h.disableAgentProcesses {
		go h.spawnAgentProcess(id, sessionID, project, section, prompt, sockPath, workDir, backend, model, theme, maxTurns, parentPid, parentWindow, false)
	}

	return jsonResponse(map[string]any{
		"id":         id,
		"session_id": sessionID,
		"project":    project,
		"section":    section,
		"backend":    backend,
		"status":     "spawning",
	})
}

// handleOpenAgentTerminal starts a terminal agent session as a managed agent:
// a PTY bridge + terminal window (like spawn_agent) for existing (session_id)
// or new (empty session_id) sessions. Managed sessions are then reachable via
// relay_agent/send_to, exactly like swarm agents.
func (h *Handler) handleOpenAgentTerminal(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string) // resume target; empty = new session
	workDir, _ := params["work_dir"].(string)
	backend, _ := params["backend"].(string)
	if backend == "" {
		backend = "opencode"
	}
	if !allowedAgentBackends[backend] {
		return errorResponse(fmt.Sprintf("backend %q is not supported (claude|codex|opencode)", backend))
	}
	project, _ := params["project"].(string)
	// Project als Absolutpfad aus dem Workdir auflösen. Short-Names können
	// mehrdeutig sein (ResolveProjectShort wirft AmbiguousProjectError, wenn
	// mehrere Kandidaten existieren — z.B. mehrere "opencode"-Worktrees).
	if project == "" || !strings.HasPrefix(project, "/") {
		project = h.store.ResolveProjectShort(workDir)
	}
	if project == "" {
		project = filepath.Base(workDir)
	}
	if project == "" {
		return errorResponse("project required")
	}

	id, err := h.store.AgentNextID(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("generate agent ID: %v", err))
	}
	agentSessionID := sessionID
	if agentSessionID == "" {
		agentSessionID = generateAgentUUID()
	}
	parentPid := callerPidParam(params)
	parentWindow := h.callerWindowFromParams(params)
	section := fmt.Sprintf("term-%s", strings.TrimPrefix(id, "agent-"))
	// Resolved before the row exists so it can be persisted: a terminal agent's
	// prompt is empty, so it can never match a session by marker, and the session
	// marker in FindSession is the only identity a bind accepts (YM-419). A row
	// opened without a session id therefore stays unbound: whoami reports
	// is_agent=false, send_to warns instead of delivering, telemetry stays
	// unattributed and resume refuses it. That is the accepted trade — the old
	// uniqueness rule ("the only session in the workdir") is exactly what swapped
	// two rows on 21.10.2026 — and the way out is to hand the row its session
	// (explicit --session adoption above) instead of guessing at one.
	wd := h.resolveAgentWorkDir(project, workDir)
	if wd == "" {
		// A terminal agent's prompt is empty, so nothing could ever name its
		// session: without a directory there is not even the row's own work_dir to
		// scope a scan on.
		return errorResponse(fmt.Sprintf("project %q has no working directory: pass work_dir or an absolute project path", project))
	}

	agent := storage.Agent{
		ID:        id,
		Project:   project,
		Section:   section,
		SessionID: agentSessionID,
		Status:    "pending",
		Backend:   backend,
		WorkDir:   wd,
	}
	if err := h.store.AgentCreate(agent); err != nil {
		return errorResponse(fmt.Sprintf("create agent: %v", err))
	}
	if backend == "opencode" && sessionID != "" {
		// Adopting an existing session is a bind, not a field assignment: two
		// rows holding one session id resolve to an arbitrary one of them
		// (whoami, send_to, resume), and the real owner could never bind again.
		// The ownership test therefore has to be the statement that writes —
		// a plain check would let two concurrent adopts both pass it. The
		// advisory read below only supplies the message.
		if owner, held := h.store.AgentOwnedByOtherSession(sessionID, id); held {
			h.store.AgentDelete(id)
			// Logged because the caller discards the error: a window that lost
			// its managed agent would otherwise be indistinguishable from one
			// that never had one.
			log.Printf("[open_agent_terminal] refusing to adopt session %s: it belongs to agent %s", sessionID, owner)
			return errorResponse(fmt.Sprintf("session %s already belongs to agent %s — resume that agent, or open a session without --session", sessionID, owner))
		}
		if !h.bindAgentSession(&agent, sessionID, "adopt") {
			if err := h.store.AgentDelete(id); err != nil {
				log.Printf("[open_agent_terminal] agent %s: rolling back the refused adoption failed: %v", id, err)
			}
			log.Printf("[open_agent_terminal] refusing to adopt session %s: another agent owns it", sessionID)
			return errorResponse(fmt.Sprintf("session %s could not be adopted (another agent owns it)", sessionID))
		}
	}

	sockPath, pathResp := h.agentSockPathForSpawn(id)
	if pathResp.Error != "" {
		return pathResp
	}
	if !h.disableAgentProcesses {
		go h.spawnAgentProcess(id, agentSessionID, project, section, "", sockPath, wd, backend, "", "", 0, parentPid, parentWindow, sessionID != "")
	}
	return jsonResponse(map[string]any{
		"status":   "starting",
		"agent_id": id,
		"section":  section,
		"resume":   sessionID != "",
	})
}

// codexSessionFile holds a path and modification time for sorting.
type codexSessionFile struct {
	path    string
	modTime time.Time
}

// findCodexSessionID scans ~/.codex/sessions/ for the most recent session
// whose CWD matches workDir. Returns the Codex session ID (without "codex:" prefix).
func findCodexSessionID(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	sessionsRoot := filepath.Join(home, ".codex", "sessions")

	var files []codexSessionFile
	walkErr := filepath.Walk(sessionsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if info.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		files = append(files, codexSessionFile{path: path, modTime: info.ModTime()})
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("walk sessions: %w", walkErr)
	}

	// Most recent first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	for _, f := range files {
		file, err := os.Open(f.path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var line struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				continue
			}
			if line.Type != "session_meta" {
				continue
			}
			var meta struct {
				ID  string `json:"id"`
				CWD string `json:"cwd"`
			}
			if err := json.Unmarshal(line.Payload, &meta); err != nil {
				continue
			}
			if meta.CWD == workDir && meta.ID != "" {
				file.Close()
				return meta.ID, nil
			}
			// Skip non-matching session_meta and continue scanning the file;
			// codex may write multiple session_meta lines in future versions.
		}
		file.Close()
	}

	return "", fmt.Errorf("no codex session found for workDir %s", workDir)
}

// pollCodexSessionID waits for a codex session file to appear for the given
// workDir and returns its session ID. Returns "" on timeout and logs the
// last lookup error so silent capture failures stay visible.
func pollCodexSessionID(workDir string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		sid, err := findCodexSessionID(workDir)
		if err == nil && sid != "" {
			return sid
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("[agent_capture] codex session capture timed out after %s for workDir %s (last error: %v)", timeout, workDir, lastErr)
	return ""
}

// opencodeV1OnlyTables is the pre-v2 store alone. A table query cannot read a
// session's messages, so it has no identity mechanism at all; a caller that has a
// v2 service (and thus a marker-based answer one line above) must not fall back to
// it over session_v2 rows — those are exactly the sessions the marker scan just
// refused, and binding one by uniqueness is the sibling mis-bind of YM-419. The
// fallback exists for a v1 binary, and a v1 binary writes nothing but `session`.
var opencodeV1OnlyTables = []string{"session"}

// pollOpencodeSessionID queries the opencode session tables for the *only* root
// session matching workDir whose creation time falls inside the optional
// [since, until] window, and returns its id. Zero bounds drop that bound; an
// empty workDir matches nothing (there is no directory to look in).
//
// Uniqueness is the match: this query reads no messages, so it cannot see the
// section marker, and "the newest session in the workdir" is a guess when the
// workdir holds more than one session inside the window. A wrong guess resumes
// the agent into somebody else's conversation, so two candidates leave the agent
// unresumable instead. (A migrated session exists in both tables, which must not
// read as two candidates — see uniqueSessionID.)
//
// Returns "" on timeout or error; every failure path logs its reason so a
// missing capture (→ whoami is_agent=false) can be diagnosed from the logs.
// opencodeDBPath resolves to the opencode CLI's session database; overridable
// in tests.
var opencodeDBPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// opencodeSessionDir returns the working directory of an opencode session by
// its bare session id, or "" when the session is unknown. Used to bootstrap
// agents from ses_* ids that reference no yesmem agent row. Both session tables
// are consulted, so v2 sessions resolve as well.
func opencodeSessionDir(sessionID string) string {
	// The id may carry the "opencode:" prefix the MCP layer attaches; the table
	// stores bare ids, so a prefixed lookup would silently find nothing.
	sessionID = stripSessionPrefix(sessionID)
	if sessionID == "" {
		return ""
	}
	escaped := strings.ReplaceAll(sessionID, "'", "''")
	ctx, cancel := context.WithTimeout(context.Background(), sessionTableQueryTimeout)
	defer cancel()
	row, err := queryFirstSessionRow(ctx, opencodeDBPath(), func(table string) string {
		return fmt.Sprintf("SELECT directory FROM %s WHERE id = '%s' LIMIT 1", table, escaped)
	})
	if err != nil {
		return ""
	}
	return row
}

// pollOpencodeSessionID is pollOpencodeSessionIDIn over both session tables.
func pollOpencodeSessionID(workDir string, since, until time.Time, timeout time.Duration) string {
	return pollOpencodeSessionIDIn(opencodeSessionTables, workDir, since, until, timeout)
}

// pollOpencodeSessionIDIn is pollOpencodeSessionID against an explicit table list,
// for callers that must not read one of the stores (see opencodeV1OnlyTables).
func pollOpencodeSessionIDIn(tables []string, workDir string, since, until time.Time, timeout time.Duration) string {
	if workDir == "" {
		return ""
	}
	dbPath := opencodeDBPath()
	if dbPath == "" {
		log.Printf("[agent_capture] opencode session capture: cannot resolve home dir")
		return ""
	}
	if _, err := os.Stat(dbPath); err != nil {
		log.Printf("[agent_capture] opencode session capture: opencode.db not found at %s: %v", dbPath, err)
		return ""
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	ambiguous := false
	for time.Now().Before(deadline) {
		// Each probe gets at most what is left of the budget, so a slow query
		// cannot make this call outlive its timeout.
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		rows, err := querySessionTablesIn(ctx, dbPath, tables, func(table string) string {
			return fmt.Sprintf("SELECT id || '|' || directory FROM %s WHERE directory = '%s'%s%s ORDER BY time_created DESC LIMIT 20",
				table, strings.ReplaceAll(workDir, "'", "''"), timeWindowClause(since, until), rootOnlyClause())
		})
		cancel()
		// A partial read cannot prove uniqueness — but a complete one can prove
		// the opposite, which is what this path needs.
		if err != nil {
			lastErr = err
		} else if len(rows) > 0 {
			if sid, ok := uniqueSessionID(rows, func(dir string) bool { return dir == workDir }); ok {
				return sid
			}
			ambiguous = true
		}
		if !sleepWithin(deadline, 500*time.Millisecond) {
			break
		}
	}
	// A second root session in the workdir cannot be resolved by any later
	// attempt either (the window is anchored to the agent row), so the reason
	// has to be visible in the log — "no session" would look like a timing
	// problem.
	why := ""
	if ambiguous {
		why = "; more than one root session in the workdir inside the window — leaving the agent unbound rather than guessing"
	}
	// Timeout: log the workDir plus the most recent session directories so a
	// workDir mismatch (e.g. worktree vs main checkout) is visible at a glance.
	// The diagnostic gets its own small budget: it must not extend the overrun
	// it explains, but a short-timeout caller still needs the hint.
	recent := ""
	dctx, dcancel := context.WithTimeout(context.Background(), diagnosticQueryTimeout)
	if dirs := recentRootSessionDirs(dctx, dbPath); len(dirs) > 0 {
		recent = strings.Join(dirs, ", ")
	}
	dcancel()
	log.Printf("[agent_capture] opencode session capture timed out after %s: no session for workDir %s (last error: %v%s; recent session dirs: %s)",
		timeout, workDir, lastErr, why, recent)
	return ""
}

// pollOpencodeAnyDirSessionID looks for exactly one root session created inside
// the window whose directory sits strictly below dirPrefix.
//
// It is the last resort for an agent whose spawn-time capture failed while no
// v2 service was reachable. Its directory is unknown (a row from before
// work_dir was persisted) and the section marker lives in the session's
// messages, which this table query cannot read — so uniqueness inside the
// agent's own creation window stands in for the match. The project prefix is
// what keeps a foreign repository's session out: a session outside the agent's
// own tree is never its session, however well it fits the window. The project
// root itself is excluded too: an orchestrator or a user session lives exactly
// there, and it is the one foreign session that would otherwise always be in
// range.
//
// Zero or more than one *distinct* session returns "": an ambiguity must leave
// the agent unresumable rather than resume somebody else's conversation. Rows
// are counted as distinct ids, because a migrated v1 session exists in both
// tables and would otherwise look like two candidates.
func pollOpencodeAnyDirSessionID(dirPrefix string, since, until time.Time, timeout time.Duration) string {
	if since.IsZero() || dirPrefix == "" {
		return ""
	}
	dbPath := opencodeDBPath()
	if dbPath == "" {
		return ""
	}
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		rows, err := querySessionTables(ctx, dbPath, func(table string) string {
			return fmt.Sprintf("SELECT id || '|' || directory FROM %s WHERE 1=1%s%s ORDER BY time_created DESC LIMIT 20",
				table, timeWindowClause(since, until), rootOnlyClause())
		})
		cancel()
		if err != nil {
			// A partial read cannot prove uniqueness.
			log.Printf("[agent_capture] opencode any-directory capture under %s: session lookup failed: %v", dirPrefix, err)
			return ""
		}
		if sid, ok := uniqueSessionID(rows, func(dir string) bool {
			return dir != dirPrefix && underDir(dir, dirPrefix)
		}); ok {
			return sid
		}
		if !sleepWithin(deadline, 500*time.Millisecond) {
			break
		}
	}
	log.Printf("[agent_capture] opencode any-directory capture under %s found no unique session in [%s, %s]",
		dirPrefix, since.Format(time.RFC3339), until.Format(time.RFC3339))
	return ""
}

// underDir reports whether dir is prefix itself or lies inside it. An agent's
// session lives either in its project root or in a worktree below it.
func underDir(dir, prefix string) bool {
	if dir == "" || prefix == "" {
		return false
	}
	if dir == prefix {
		return true
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(prefix, "/")+"/")
}

// uniqueSessionID returns the single distinct id among rows of the form
// "<id>|<directory>" whose directory satisfies keep, and false when there is no
// row or more than one. Rows are counted by distinct id because a migrated v1
// session exists in both tables and would otherwise look like two candidates.
//
// Callers that cannot read the session's messages have no marker to match on, so
// uniqueness inside the agent's own window has to stand in for the match: a
// guess between two candidates would resume somebody else's conversation.
func uniqueSessionID(rows []string, keep func(dir string) bool) (string, bool) {
	candidates := map[string]bool{}
	for _, row := range rows {
		id, dir, ok := strings.Cut(row, "|")
		if !ok || id == "" || !keep(dir) {
			continue
		}
		candidates[id] = true
	}
	if len(candidates) != 1 {
		return "", false
	}
	for id := range candidates {
		return id, true
	}
	return "", false
}

// rootOnlyClause restricts a session-table query to root sessions. A task()
// subagent session is never the agent's own session, so binding one would hand
// the agent a foreign conversation on resume.
func rootOnlyClause() string {
	return " AND (parent_id IS NULL OR parent_id = '')"
}

// timeWindowClause renders the optional creation-time bounds. Both session
// tables store time_created in Unix milliseconds.
func timeWindowClause(since, until time.Time) string {
	clause := ""
	if !since.IsZero() {
		clause += fmt.Sprintf(" AND time_created >= %d", since.UnixMilli())
	}
	if !until.IsZero() {
		clause += fmt.Sprintf(" AND time_created <= %d", until.UnixMilli())
	}
	return clause
}

// loadCodexAuthEnv reads ~/.codex/auth.json and returns "OPENAI_API_KEY=..." if present.
func loadCodexAuthEnv() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	authPath := filepath.Join(home, ".codex", "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return "", err
	}
	var auth struct {
		OPENAI_API_KEY string `json:"OPENAI_API_KEY"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", err
	}
	if auth.OPENAI_API_KEY == "" {
		return "", fmt.Errorf("no OPENAI_API_KEY in auth.json")
	}
	return "OPENAI_API_KEY=" + auth.OPENAI_API_KEY, nil
}

// buildAgentExtraEnv returns the extra environment variables to inject into
// the spawned agent's process. For opencode/codex backends we set
// YESMEM_SOURCE_AGENT + YESMEM_SESSION_ID so the yesmem-mcp child (spawned by
// the backend) can identify itself to the daemon without relying on the
// global active_session_opencode proxy-state, which is Last-Writer-Wins
// across parallel agents. Codex additionally gets CODEX_THREAD_ID because
// internal/mcp/server.go resolveClientSessionID reads that variable (not
// YESMEM_SESSION_ID) for the codex branch.
//
// NOTE: despite the name this helper is not pure — for the codex backend it
// reads ~/.codex/auth.json via loadCodexAuthEnv to inject OPENAI_API_KEY.
// The pre-refactor inline code did the same; preserved here.
func buildAgentExtraEnv(backend, sessionID string) []string {
	var env []string
	if backend == "codex" {
		if k, err := loadCodexAuthEnv(); err == nil && k != "" {
			env = append(env, k)
		}
	}
	// Identity vars only make sense when we have a sessionID to advertise.
	// Without one, resolveClientSessionID returns ("", sourceAgent) and the
	// MCP wrapper skips the _session_id param — defeat the purpose.
	if sessionID == "" {
		return env
	}
	switch backend {
	case "opencode":
		env = append(env,
			"YESMEM_SOURCE_AGENT=opencode",
			"YESMEM_SESSION_ID="+sessionID,
		)
	case "codex":
		env = append(env,
			"YESMEM_SOURCE_AGENT=codex",
			"YESMEM_SESSION_ID="+sessionID,
			// Mirror to CODEX_THREAD_ID so resolveClientSessionID's codex
			// branch (which reads CODEX_THREAD_ID, not YESMEM_SESSION_ID)
			// picks it up. Verified codex CLI does not consume
			// CODEX_THREAD_ID for its own thread selection (it polls the
			// filesystem ~2s after spawn in pollCodexSessionID); only
			// yesmem-mcp children read it.
			"CODEX_THREAD_ID="+sessionID,
		)
	}
	return env
}

// callerPidParam returns the PID the MCP layer injected for the calling
// process, or 0 when absent.
func callerPidParam(params map[string]any) int {
	if p, ok := params["_caller_pid"].(float64); ok && p > 0 {
		return int(p)
	}
	return 0
}

// callerWindowFromParams returns the terminal window of the caller, or "".
// Only authoritative session ids are consulted; the global
// active_session_opencode fallback of resolveSessionID is skipped because it
// is last-writer-wins across sessions and would pick another session's
// window. The per-call _session_id (injected by the MCP layer / opencode
// plugin) says where the caller really is and wins. caller_session, the reply
// target, is the fallback for callers without a window of their own, e.g. an
// orchestrator agent behind the daemon's PTY bridge spawning on behalf of the
// user's session.
func (h *Handler) callerWindowFromParams(params map[string]any) string {
	// One window-manager call at most, and only if a window is registered.
	var live map[string]LiveWindow
	known, asked := false, false
	// A leaked environment id names another session, so its window is not this
	// caller's; caller_session stays available as the reply target.
	leaked := h.envDerivedSessionUntrusted(params)
	for _, key := range []string{"_session_id", "caller_session"} {
		if key == "_session_id" && leaked {
			continue
		}
		sid, ok := params[key].(string)
		if !ok || sid == "" {
			continue
		}
		w := h.callerWindow(sid)
		if w == "" {
			continue
		}
		if !asked {
			live, known = h.liveSnapshot()
			asked = true
		}
		if known {
			if _, alive := windowAlive(w, h.windowPidOf(sid), live); !alive {
				continue // closed, or the id now belongs to another client
			}
		}
		return w
	}
	return ""
}

// x11WindowID matches a well-formed X11 window id ("0x" + 1..16 hex digits).
var x11WindowID = regexp.MustCompile(`^0[xX][0-9a-fA-F]{1,16}$`)

// callerWindow returns the terminal window registered for a session via
// register_window (fed by the opencode TUI plugin), or "" when none is known.
// register_window accepts any string, so only a well-formed X11 id is passed
// on. agent-tty resolves its desktop at placement time, so a window moved
// since registration is still placed correctly.
func (h *Handler) callerWindow(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	h.windowMapMu.Lock()
	wid := h.windowMap[sessionID]
	h.windowMapMu.Unlock()
	if !x11WindowID.MatchString(wid) {
		return ""
	}
	return wid
}

// callerPlacement resolves the placement sources of a session that is not the
// current request's caller: the PID of the agent process that owns it, when that
// process is still the caller's backend, and the terminal window registered for
// it. The restart path relaunches a row outside any MCP request, so neither
// _caller_pid nor _session_id is available to it; the session recorded on the row
// is all it has (YM-458). An unknown session yields 0 and "", which keeps the
// default placement instead of failing. The crash-recovery path does not come
// through here: it spawns over spawn_agent with the caller session and therefore
// has the registered window, but no pid.
func (h *Handler) callerPlacement(sessionID string) (parentPid int, parentWindow string) {
	if sessionID == "" {
		return 0, ""
	}
	// The window goes through the same resolver the resume fallback uses, so the
	// liveness check (a closed window, or an id the X server handed to another
	// client) applies here too.
	parentWindow = h.callerWindowFromParams(map[string]any{"caller_session": sessionID})
	if row, err := h.store.AgentGetAnyBySession(sessionID); err == nil && row != nil && isAgentProcess(*row) {
		parentPid = row.PID
	}
	return parentPid, parentWindow
}

// shouldPlaceAgentWindow reports whether a freshly spawned agent window should
// be moved to the caller's desktop. The tmux backend splits into an existing
// tmux session and opens no window of its own; without a caller PID or a
// registered caller window there is no desktop to inherit.
func shouldPlaceAgentWindow(terminal string, parentPid int, parentWindow string) bool {
	return terminal != "tmux" && (parentPid > 0 || parentWindow != "")
}

// opencodeSpawnPlan assembles the argv and environment for an opencode agent:
// the resume flag, the model override, and the TUI theme (see
// themeSpawnOverride). Kept as one unit so the theme reaches argv and env
// together — a spawn that silently drops one of the two is exactly the failure
// this feature cannot afford.
func (h *Handler) opencodeSpawnPlan(id, sessionID string, resume bool, model, theme, section, bin string, environ []string) (args, env []string) {
	if resume {
		// Prefer stored per-agent opencode session ID.
		sid := sessionID
		if agent, _ := h.store.AgentGet(id); agent != nil && agent.OpencodeSessionID != "" {
			sid = agent.OpencodeSessionID
		}
		args = []string{"-s", sid}
	} else {
		args = []string{}
		// Name the session here instead of guessing it from prompt text later.
		// opencode creates a session with the given -s id when it does not exist
		// (verified 27.10.2026 against the running v2 server), so the daemon knows
		// the id before the process starts and binds it itself. The capture that
		// reads the first user message stays as the fallback for resumes and for
		// agents started outside this path.
		//
		// A handler without a store (unit tests) keeps the previous argv: it cannot
		// bind, and naming a session it will not record would only create orphans.
		if h.store != nil {
			if a, err := h.store.AgentGet(id); err == nil && a != nil {
				if sid := newOpencodeSessionID(); sid != "" && h.bindAgentSession(a, sid, "spawn id") {
					args = append(args, "-s", sid)
				} else {
					log.Printf("[agent_bind] agent %s: could not claim a fresh session id, falling back to the spawn capture", id)
				}
			}
		}
	}
	if resolved := h.resolvedAgentModel(model, "opencode"); resolved != "" {
		args = append(args, "--model", resolved)
	}
	themeFlags, themeEnv := h.themeSpawnOverride("opencode", bin, theme, section, environ)
	return append(args, themeFlags...), themeEnv
}

// newOpencodeSessionID mints a session id opencode accepts. The server requires
// the "ses" prefix ("Expected a string starting with \"ses\""), and the rest only
// has to be unique: a collision would continue an existing conversation instead
// of starting a new one, which is why this uses crypto/rand rather than a
// counter.
func newOpencodeSessionID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return fmt.Sprintf("ses_ym%x", b)
}

// buildAgentTTYArgs assembles the `yesmem agent-tty` argv run inside the new
// terminal window, including the placement sources when there are any.
func buildAgentTTYArgs(sockPath, terminal string, parentPid int, parentWindow string) []string {
	args := []string{"agent-tty", "--sock", sockPath}
	if !shouldPlaceAgentWindow(terminal, parentPid, parentWindow) {
		return args
	}
	// agent-tty moves its own window onto the desktop the caller's terminal
	// sits on instead of leaving it on the focused one.
	if parentPid > 0 {
		args = append(args, "--parent-pid", strconv.Itoa(parentPid))
	}
	if parentWindow != "" {
		args = append(args, "--parent-window", parentWindow)
	}
	return args
}

// spawnAgentProcess creates a PTY bridge, opens a terminal, and waits for the agent to finish.
// theme is the explicit per-spawn theme override (empty = the agent type's default from config).
// spawnAgentProcess starts the backend process for a row. maxTurns reaches the
// backend's argv only in the claude non-resume branch (--max-turns); the relay cap
// the daemon enforces reads agents.max_turns (heartbeat.relayAgentMessages), and
// every other backend is bounded by that alone (YM-244).
func (h *Handler) spawnAgentProcess(id, sessionID, project, section, prompt, sockPath, workDir, backend, model, theme string, maxTurns, parentPid int, parentWindow string, resume bool) {
	// L4 defensive: handleResumeAgent catches this first, but defend direct
	// callers too. Without this guard the opencode resume branch falls back to
	// the daemon's 36-char UUID and opencode exits 1 (Learning #80228).
	// attemptRestart (heartbeat.go) reaches this same function, so the restart
	// path is covered by the guard below.
	if backend == "opencode" && resume {
		if a, _ := h.store.AgentGet(id); a == nil || a.OpencodeSessionID == "" {
			log.Printf("[agent_capture] agent %s: blocking opencode resume — no opencode_session_id (would fall back to daemon UUID)", id)
			h.store.AgentUpdate(id, map[string]any{
				"status":     "error",
				"error":      opencodeUnresumableMsg,
				"stopped_at": time.Now().Format(time.RFC3339),
			})
			return
		}
	}

	var agentBin string
	var agentArgs []string
	var themeEnv []string

	switch backend {
	case "opencode":
		agentBin = resolveAgentBinary(backend)
		agentArgs, themeEnv = h.opencodeSpawnPlan(id, sessionID, resume, model, theme, section, agentBin, os.Environ())
		serverArgs, serverEnv, err := prepareOpencodeAgentServer(agentBin)
		if err != nil {
			h.store.AgentUpdate(id, map[string]any{
				"status": "error", "error": err.Error(),
				"stopped_at": time.Now().Format(time.RFC3339),
			})
			return
		}
		agentArgs = append(agentArgs, serverArgs...)
		themeEnv = append(themeEnv, serverEnv...)
	case "codex":
		agentBin = resolveAgentBinary(backend)
		if resume {
			// Prefer stored per-agent session ID over workDir heuristic.
			sessionID := ""
			if agent, _ := h.store.AgentGet(id); agent != nil && agent.CodexSessionID != "" {
				sessionID = agent.CodexSessionID
			}
			if sessionID == "" {
				var err error
				sessionID, err = findCodexSessionID(workDir)
				if err != nil {
					h.store.AgentUpdate(id, map[string]any{
						"status":     "error",
						"error":      fmt.Sprintf("codex session lookup: %v", err),
						"stopped_at": time.Now().Format(time.RFC3339),
					})
					return
				}
			}
			agentArgs = []string{
				"resume", sessionID,
				"--cd", workDir,
				"--no-alt-screen",
				"-c", fmt.Sprintf("approval_policy={granular={mcp_elicitations=true,sandbox_approval=true,rules=true,request_permissions=true,skill_approval=true}}"),
			}
		} else {
			agentArgs = []string{
				"--cd", workDir,
				"--no-alt-screen",
				"-c", fmt.Sprintf("approval_policy={granular={mcp_elicitations=true,sandbox_approval=true,rules=true,request_permissions=true,skill_approval=true}}"),
			}
		}
	default: // "claude"
		// Keep this switch in sync with agentBinaryName (heartbeat.go): the
		// identity check derives the expected binary name from it, so a backend
		// added here without a case there becomes permanently unverifiable.
		agentBin = "claude"
		if resume {
			// The grant travels on the command line in both branches: the daemon no
			// longer writes a .claude/settings.local.json into the project, so a
			// resume that carried none would prompt for every tool and end as a
			// timeout (YM-300).
			agentArgs = []string{"--resume", sessionID, "--allowedTools", claudeAgentAllowedTools}
		} else {
			agentArgs = []string{
				"--session-id", sessionID,
				"--name", fmt.Sprintf("%s-%s", agentSpawnLabel(project), agentSpawnLabel(section)),
				"--allowedTools", claudeAgentAllowedTools,
			}
			if model != "" {
				agentArgs = append(agentArgs, "--model", model)
			}
			if maxTurns > 0 {
				agentArgs = append(agentArgs, "--max-turns", fmt.Sprintf("%d", maxTurns))
			}
		}
	}

	// Build extraEnv: codex OPENAI_API_KEY + identity vars for non-claude
	// backends so the yesmem-mcp child can resolve its own session without
	// the global active_session_opencode proxy-state (Last-Writer-Wins across
	// parallel agents). See resolveClientSessionID in internal/mcp/server.go.
	extraEnv := buildAgentExtraEnv(backend, sessionID)
	extraEnv = append(extraEnv, themeEnv...)

	bridge, err := newAgentBridge(agentBin, agentArgs, sockPath, workDir, extraEnv...)
	if err != nil {
		h.store.AgentUpdate(id, map[string]any{
			"status":     "error",
			"error":      fmt.Sprintf("bridge: %v", err),
			"stopped_at": time.Now().Format(time.RFC3339),
		})
		return
	}
	go bridge.Serve()

	binPath, _ := os.Executable()
	terminal := h.agentTerminal
	if terminal == "" {
		terminal = orchestrator.DetectTerminal()
	}
	if terminal == "unknown" {
		// Daemon läuft headless (systemd) — DetectTerminal findet keinen
		// Emulator. Ghostty ist auf diesem Host das Standard-Terminal und
		// wird via +new-window zuverlässig geöffnet.
		terminal = "ghostty"
	}
	title := fmt.Sprintf("yesmem-%s #%s", agentSpawnLabel(section), strings.TrimPrefix(id, "agent-"))
	agentTTYArgs := buildAgentTTYArgs(sockPath, terminal, parentPid, parentWindow)
	bin, spawnArgs := agentDisplayCommand(terminal, binPath, title, agentTTYArgs...)
	termCmd := exec.Command(bin, spawnArgs...)
	if err := termCmd.Start(); err != nil {
		bridge.Close()
		h.store.AgentUpdate(id, map[string]any{
			"status":     "error",
			"error":      fmt.Sprintf("terminal: %v", err),
			"stopped_at": time.Now().Format(time.RFC3339),
		})
		return
	}
	// Reap terminal process on exit to prevent zombie accumulation.
	// gnome-terminal forks itself; the immediate parent exits quickly,
	// but without Wait() it stays as a zombie and eventually blocks
	// gnome-terminal-server's DBus activation for future spawns.
	go func() { termCmd.Wait() }()

	// No ownerSession: this is the daemon's own spawn, binding the child it just
	// created. The argument of spawnAgentProcess is the BACKEND session (it is the
	// explicit claude session on a resume), not the row's session_id — passing it
	// as the ownership key would refuse every resume that presents one (cold
	// review F1, YM-356).
	bound, bindErr := h.bindAgentProcess(id, bridge.Cmd.Process.Pid, sockPath, daemonOwnsTheBind)
	if bindErr != nil {
		// A write error is not a refusal: the row may be perfectly live (SQLITE_BUSY
		// under concurrent writers), so it is retried once before the process is
		// treated as unowned.
		log.Printf("[spawn_agent] agent %s: bind process: %v (retrying once)", safeAgentText(id), bindErr)
		bound, bindErr = h.bindAgentProcess(id, bridge.Cmd.Process.Pid, sockPath, daemonOwnsTheBind)
		if bindErr != nil {
			log.Printf("[spawn_agent] agent %s: bind process failed again: %v", id, bindErr)
		}
	}
	if !bound {
		// Nothing owns this process: either the row no longer claims to be live (a
		// stop landed between the "spawning" write and this bind) or it could not be
		// written at all. It must not keep running behind the orchestrator's back —
		// the end of this function reaps it and removes the socket files of its id.
		// The console window that was opened for it goes with it (the display start
		// precedes the bind, so there is always one in this window).
		//
		// Residual, pre-existing (YM-70): this teardown unlinks the per-id socket
		// paths while the row names another process — the ownership question is only
		// answered after the process exited, and this process is the non-owner here.
		log.Printf("[spawn_agent] agent %s: no live row to bind the process to — shutting the new process down", safeAgentText(id))
		bridge.Close()
		bridge.Cmd.Process.Signal(syscall.SIGTERM)
		if termCmd.Process != nil {
			termCmd.Process.Signal(syscall.SIGTERM)
		}
	}

	// Inject initial prompt via PTY — all backends, initial spawn only. A refused
	// bind skips it too: that process is on its way out, and the capture below
	// writes to the row (a stale backend session id, the unresumable marker) that
	// nothing owns any more.
	if !resume && bound {
		// The opencode session the TUI is about to create must postdate this
		// agent row; the capture uses it to reject a previous run's session.
		spawnedAt := time.Now()
		if t, ok := h.agentCreatedAt(id); ok {
			spawnedAt = t
		}
		go func() {
			injectPath := sockPath + ".inject"

			// Opencode TUI needs ~15s to fully start (system prompt, MCP connect,
			// UI init). Claude starts faster but the extra wait is harmless.
			time.Sleep(15 * time.Second)

			// Accept MCP server trust prompt (Enter on default option "1. Use this and all future...")
			if conn, err := net.DialTimeout("unix", injectPath, 3*time.Second); err == nil {
				conn.Write([]byte("\r"))
				conn.Close()
			}

			// Send prompt body, then a separate \r after 300ms so the TUI
			// does NOT treat it as part of a bracketed-paste block.
			time.Sleep(300 * time.Millisecond)
			if conn, err := net.DialTimeout("unix", injectPath, 3*time.Second); err == nil {
				conn.Write([]byte(prompt))
				conn.Close()
			}
			time.Sleep(300 * time.Millisecond)
			if conn, err := net.DialTimeout("unix", injectPath, 3*time.Second); err == nil {
				conn.Write([]byte("\r"))
				conn.Close()
			}

			// Capture codex session ID for per-agent tracking.
			// Codex writes session_meta to ~/.codex/sessions/ within ~2s of TUI start.
			if backend == "codex" {
				codexID := pollCodexSessionID(workDir, 30*time.Second)
				if codexID != "" {
					h.store.AgentUpdate(id, map[string]any{
						"codex_session_id": codexID,
					})
				} else {
					log.Printf("[agent_capture] agent %s: codex_session_id stays empty — whoami will not resolve this agent by backend session", id)
				}
			}
			// Capture opencode session ID for per-agent tracking.
			// v2 sessions are created in the v2 server's store (opencode.db
			// table session_v2) and are found through the server API; the
			// session tables only serve v1 binaries.
			if backend == "opencode" {
				// A spawn that named its own session (opencodeSpawnPlan) is bound
				// before the process starts. The capture would only re-find it after
				// a scan of up to 150s, and when the first user message carries no
				// marker it would log a false "stays empty" for a bound agent.
				if a, err := h.store.AgentGet(id); err == nil && a != nil && a.OpencodeSessionID != "" {
					log.Printf("[agent_capture] agent %s: bound to %s before start, skipping the capture", id, a.OpencodeSessionID)
				} else {
					ocID := captureOpencodeSessionID(workDir, id, section, project, spawnedAt, opencodeSpawnCaptureTimeout)
					if ocID != "" {
						// Routed through the one bind function like the late and
						// on-demand binds, so the ownership guard applies here too.
						if a, err := h.store.AgentGet(id); err == nil && a != nil {
							h.bindAgentSession(a, ocID, "spawn capture")
						} else {
							log.Printf("[agent_capture] agent %s: captured session %s but the agent row is unreadable: %v", id, ocID, err)
						}
					} else {
						log.Printf("[agent_capture] agent %s: opencode_session_id stays empty — whoami will not resolve this agent by backend session", id)
						h.markUnresumableIfUnbound(id)
					}
				}
			}
		}()
	}

	// Wait for agent to finish
	ourPID := bridge.Cmd.Process.Pid
	waitErr := bridge.Cmd.Wait()

	// The pid and the socket paths belong to the agent id, not to this process: a
	// resume or a restart can bind a successor while this one is still winding
	// down. Only a row that still names this process — or names none at all, which
	// is this spawn's own refused bind — may be closed and cleaned up here. A
	// stale process otherwise overwrote the live agent's pid, socket path and
	// status, and unlinked the sockets its successor is listening on (YM-70).
	agent, err := h.store.AgentGet(id)
	// Only a row that still names our process proves that the socket files at
	// these paths are ours. A row naming a successor must keep them: its own
	// teardown removes them (YM-70).
	//
	// Residual, pre-existing: the unlink is not coupled to the CAS below, so a
	// restart binding a successor between the read and this removal can lose its
	// socket file; a PID recycled onto a successor in the same instant passes the
	// pid comparison. Both windows are narrower than the unconditional removal
	// they replace, and closing them needs the file's inode as the ownership proof.
	ours := err == nil && agent != nil && agent.PID == ourPID
	if ours {
		bridge.Close()
		h.removeAgentSocketFiles(id)
	} else {
		bridge.CloseKeepingSockets()
	}
	// A row that names no process at all is still this spawn's to close — its bind
	// write failed, and leaving it "pending" would keep the section owned by a row
	// with no process (a refused bind means the row was not live, so this branch is
	// the write-error one). A row that names a successor is not ours to touch.
	if err != nil || agent == nil || (!ours && (agent.PID != 0 || bound)) {
		return
	}

	fields := map[string]any{}
	now := time.Now().Format(time.RFC3339)
	switch agent.Status {
	case "running", "pending", "spawning":
		fields["stopped_at"] = now
		if waitErr != nil {
			fields["status"] = "error"
			fields["error"] = fmt.Sprintf("exit: %v", waitErr)
		} else {
			fields["status"] = "finished"
		}
	default:
		if agent.StoppedAt == "" {
			fields["stopped_at"] = now
		}
	}
	// The write is a compare-and-set on our pid, like the stop's (see
	// AgentFinalizeExit): a resume can bind a successor between the read above and
	// this write.
	if written, err := h.store.AgentFinalizeExit(id, ourPID, fields); err != nil {
		log.Printf("[spawn_agent] agent %s: recording the exit state failed: %v", id, err)
	} else if !written {
		log.Printf("[spawn_agent] agent %s: a resume or a restart bound another process in the meantime — its binding was left alone", id)
	}
}

// markUnresumableIfUnbound records that the agent has no backend session, so an
// orchestrator reading get_agent sees it cannot be resumed later. Status stays
// "running" — the agent can still work, it just cannot be resumed once it stops.
//
// The write is conditioned on the session column in the store, because another
// path (get_agent, send_to, resume) can bind the agent while the spawn-time
// capture is still polling, and a bound agent is resumable — the marker would
// then be a false alarm on a healthy agent.
func (h *Handler) markUnresumableIfUnbound(id string) {
	if _, err := h.store.AgentSetProgressIfUnbound(id, opencodeUnresumableMsg); err != nil {
		log.Printf("[agent_capture] agent %s: recording the unresumable marker failed: %v", id, err)
	}
}

// handleRegisterAgent updates a pending agent with PID and socket path (called by CLI after bridge creation).
func (h *Handler) handleRegisterAgent(params map[string]any) Response {
	id, _ := params["id"].(string)
	if id == "" {
		return errorResponse("id required")
	}

	pid, _ := params["pid"].(float64) // JSON numbers are float64
	sockPath, _ := params["sock_path"].(string)
	// The session id spawn handed to this caller identifies the row. It is not
	// returned by list_agents or get_agent, so a caller cannot bind or close a row
	// it never got (YM-356). It is an integrity guard against blind or wrong-row
	// writes, not a boundary against a same-uid process: the value also reaches
	// the spawned child as its session identity, so it can be read from /proc.
	ownerSession, _ := params["session_id"].(string)

	if pid == 0 {
		return errorResponse("pid required")
	}
	if sockPath == "" {
		return errorResponse("sock_path required")
	}
	if ownerSession == "" {
		// The capability is what separates the caller that spawned the row from
		// any other process that can reach the socket. Requiring it here (rather
		// than only in the store) makes the refusal explicit for a caller that
		// never received one.
		return errorResponse("session_id required: register_agent binds a row only on behalf of the session that spawned it")
	}

	// The pid and its spawn marker belong together — see bindAgentProcess. This
	// path could write both itself, but routing it through the same helper keeps
	// the invariant in one place (and the row's marker consistent if the row it
	// binds is older than the CLI thinks).
	bound, err := h.bindAgentProcess(id, int(pid), sockPath, ownerSession)
	if err != nil {
		return errorResponse(fmt.Sprintf("register agent: %v", err))
	}
	if !bound {
		// Two different causes, and the caller has to be able to tell: the row is
		// no longer live (a stop landed, or it was closed deliberately), the
		// caller does not own it, or the pid it named is not this agent's own
		// backend process. Either way the row must not stay "spawning": the CLI
		// stops its child and moves on without running the daemon's exit tail, so
		// the row would own its section forever (YM-351). The close is owner-bound
		// too, so a foreign caller leaves the row untouched.
		written, closeErr := h.store.AgentCloseUnboundSpawn(id, ownerSession,
			"register_agent: the process could not be bound (no live process, no matching owner or no live row)",
			time.Now().Format(time.RFC3339))
		if closeErr != nil {
			log.Printf("[agent_bind] agent %s: closing the unbound spawn row failed: %v", safeAgentText(id), closeErr)
		} else if written {
			log.Printf("[agent_bind] agent %s: closed the spawn row after the refused bind", safeAgentText(id))
		}
		return errorResponse(fmt.Sprintf("agent %s could not be bound: the row is no longer live, this caller does not own it (session_id), or PID %d is not its backend process — register_agent does not revive a stopped, finished or failed row", safeAgentText(id), int(pid)))
	}

	return jsonResponse(map[string]any{"status": "ok", "id": id})
}

// handleUpdateAgent updates arbitrary allowed fields on an agent (called by CLI for status transitions).
func (h *Handler) handleUpdateAgent(params map[string]any) Response {
	id, _ := params["id"].(string)
	if id == "" {
		return errorResponse("id required")
	}

	fieldsRaw, ok := params["fields"].(map[string]any)
	if !ok || len(fieldsRaw) == 0 {
		return errorResponse("fields required")
	}

	// The socket path is not a caller's to name here: it is the dial target of
	// every guard relay and the precondition of the idle guard's unreadable hold,
	// so a caller that can write it redirects its own deliveries and widens its
	// own supervision window (YM-335). The daemon derives the path from the agent
	// id (agentSockPath); the only other value it writes through this endpoint is
	// the empty one a resume uses to take the row over. register_agent still takes
	// a caller-supplied path and writes it unchecked — that is YM-274's remainder,
	// not closed here.
	if raw, ok := fieldsRaw["sock_path"]; ok {
		got, isString := raw.(string)
		want := ""
		if isString && got != "" {
			want = h.agentSockPath(id)
		}
		// A non-string is refused too: stored as it came, it would leave the row
		// with a value no later read can bind, which hides the agent from the
		// guards just as effectively as a foreign path. The reply names no path: it
		// goes to the caller, and the derived one is the daemon's own layout.
		if !isString || got != want {
			return errorResponse("sock_path must be empty or the daemon-derived path")
		}
	}

	// respawn_claimed is a token, not a status a caller reports: the daemon takes it to
	// coordinate a crash replacement with a stop, and a row carrying it looks unowned to
	// AgentGetActiveBySection while the abandoned-claim closer may end it without a signal.
	// A caller that wrote it would hand its own row to that closer, so the value is refused
	// here, the same way sock_path is (security review Los 56h, Learning #105186).
	if raw, ok := fieldsRaw["status"]; ok {
		status, isString := raw.(string)
		if !isString || status == "respawn_claimed" {
			return errorResponse("status must be one of the caller-reportable states (respawn_claimed is the daemon's own token)")
		}
	}

	// A write that names the PID it came from is the exit of a process, and it is a
	// compare-and-set: a resume or a restart can bind a successor to the row while
	// this process winds down, and an unconditional write would erase the
	// successor's binding, its status and its socket path (YM-202; mirrors the
	// daemon's own spawn exit tail). The reply says whether the row was ours, which
	// is what tells the caller whether the socket files are its own to remove.
	if raw, ok := params["expected_pid"]; ok {
		pid, isNumber := raw.(float64)
		if !isNumber || pid <= 0 {
			return errorResponse("expected_pid must be a positive number when given")
		}
		written, err := h.store.AgentFinalizeExit(id, int(pid), fieldsRaw)
		if err != nil {
			return errorResponse(fmt.Sprintf("finalize agent exit: %v", err))
		}
		return jsonResponse(map[string]any{"status": "ok", "id": id, "written": written})
	}

	// A PID is written by bindAgentProcess (which also writes the spawn marker and
	// checks that the process is the row's backend) and by the exit tails that
	// clear it. Letting this endpoint set one bypasses the ownership check
	// register_agent enforces: update_agent(id, {pid, status:"running"}) takes a
	// row over in one call, and the store's automatic spawn marker then satisfies
	// the identity gate (cold review 5.4).
	if raw, ok := fieldsRaw["pid"]; ok && !pidFieldIsClear(raw) {
		return errorResponse("pid is written by the daemon only — a spawn binds its process through register_agent, an exit clears it")
	}

	if err := h.store.AgentUpdate(id, fieldsRaw); err != nil {
		return errorResponse(fmt.Sprintf("update agent: %v", err))
	}

	// Clean up socket files when agent finishes or errors
	if status, _ := fieldsRaw["status"].(string); status == "finished" || status == "error" {
		// The column is only the precondition that this row ever bound a socket:
		// a spawn creates the file before it writes the column, so an unconditional
		// cleanup here would take the file of a spawn still in flight.
		if agent, err := h.store.AgentGet(id); err == nil && agent.SockPath != "" {
			h.removeAgentSocketFiles(agent.ID)
		}
	}

	return jsonResponse(map[string]any{"status": "ok", "id": id})
}

// handleRelayAgent injects content into a running agent's PTY via its inject
// socket and, for a paused agent, may lift an idle-guard pause.
//
// relayDialTimeout bounds the connect to an agent's inject socket, and
// relaySubmitDelay is how long the content write gets to settle in the PTY
// before the submit key follows on its own connection: a \r inside the same
// write is swallowed by bracketed paste (Learning #91520).
const (
	relayDialTimeout = 3 * time.Second
	relaySubmitDelay = 3 * time.Second
)

func (h *Handler) handleRelayAgent(params map[string]any) Response {
	to, _ := params["to"].(string)
	content, _ := params["content"].(string)
	project, _ := params["project"].(string)

	if to == "" {
		return errorResponse("to required")
	}
	if content == "" {
		return errorResponse("content required")
	}

	agent, err := h.resolveAgent(to, project)
	if err != nil {
		return errorResponse(err.Error())
	}

	if agent.Status == "stopped" {
		return errorResponse(fmt.Sprintf("agent %s is stopped, not relayable", agent.ID))
	}
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		return errorResponse(errNoAgentSocket.Error())
	}

	// Wrap content with RELAY prefix so agent can identify the source
	caller, _ := params["caller_session"].(string)
	if caller == "" {
		caller = "orchestrator"
	}
	// The prefix is typed into the target agent's TUI, so the caller value must
	// not be able to end the bracket and append instructions (YM-255, same class
	// as the spawn prompt's fields in YM-211). Refused rather than rewritten: a
	// caller passing this has a bug.
	if promptFieldUnsafe(caller) || strings.ContainsAny(caller, "[]") {
		return errorResponse("caller_session contains characters that cannot be rendered into the relay prompt (quotes, brackets, control or bidi characters)")
	}
	// Newlines in content would be interpreted as Enter keypresses in the PTY,
	// splitting the message into fragments. Escape them.
	sanitized := strings.ReplaceAll(content, "\n", "\\n")
	sanitized = strings.ReplaceAll(sanitized, "\r", "")
	wrappedContent := fmt.Sprintf("[RELAY from=%s] %s", caller, sanitized)

	// Two separate connections to avoid bracketed-paste swallowing the \r: the
	// content goes out on the first, the submit key on the second after
	// relaySubmitDelay. Only the content write stays on the caller's path, because
	// its error is the one worth reporting; the delay and the submit run in the
	// background, so a relay no longer costs the caller three seconds (YM-253).
	contentWritten := make(chan error, 1)
	go deliverRelay(injectPath, wrappedContent, contentWritten)
	if err := <-contentWritten; err != nil {
		return errorResponse(err.Error())
	}

	// A row the retired pauses left behind: the guards no longer stop an agent,
	// but rows paused by the old code are still in the database and no layer
	// watches a paused row, so a relay is the only thing that releases them.
	//
	// The write is conditional on the row still carrying exactly that pause: the
	// row was read before the delivery, and a stop or another guard's pause may
	// have landed since. The prefix is matched, not searched.
	if agent.Status == "paused" && matchesRetiredGuardPause(agent.Progress) {
		changed, err := h.store.AgentUpdateIfMatches(agent.ID, "paused", agent.Progress, map[string]any{
			"status": "running",
			"progress": fmt.Sprintf("recovered by RELAY at %s: relay delivered to a retired guard pause",
				time.Now().Format("2006-01-02 15:04:05")),
		})
		if err != nil {
			log.Printf("[relay] unpause of %s failed: %v", agent.ID, err)
		} else if changed {
			resetIdleProtocol(agent.ID)
		} else {
			// The row moved while the relay was delivered (a stop, or another
			// guard's pause). Its status is the newer decision and stays.
			log.Printf("[relay] unpause of %s skipped: the row changed during delivery", agent.ID)
		}
	}

	return jsonResponse(map[string]any{
		"status":   "injected",
		"agent_id": agent.ID,
		"section":  agent.Section,
	})
}

// deliverRelay writes a relay's content and, after relaySubmitDelay, the submit
// key on a second connection. The caller waits only for the first result; a
// failed submit is logged, not returned.
func deliverRelay(injectPath, content string, contentWritten chan<- error) {
	// Deliveries to one socket are serialised. Without this, two relays that
	// arrive back to back would interleave content and submit and reach the agent
	// as one merged prompt.
	unlock := lockRelayPath(injectPath)
	defer unlock()

	conn, err := net.DialTimeout("unix", injectPath, relayDialTimeout)
	if err != nil {
		contentWritten <- fmt.Errorf("connect to inject socket: %v (agent may have crashed)", err)
		return
	}
	if _, err := conn.Write([]byte(content)); err != nil {
		conn.Close()
		contentWritten <- fmt.Errorf("write to inject socket: %v", err)
		return
	}
	conn.Close()
	contentWritten <- nil

	// The lock stays held across the delay, so the next relay's content cannot
	// land between this one's content and its submit.
	time.Sleep(relaySubmitDelay)

	submit, err := net.DialTimeout("unix", injectPath, relayDialTimeout)
	if err != nil {
		log.Printf("[relay] submit to %s failed: %v", injectPath, err)
		return
	}
	submit.Write([]byte("\r\n"))
	submit.Close()
}

// relayPathLock serialises deliveries to one inject socket. refs keeps the map
// bounded: an entry is dropped when its last holder leaves.
type relayPathLock struct {
	mu   sync.Mutex
	refs int
}

var (
	relayPathLocksMu sync.Mutex
	relayPathLocks   = map[string]*relayPathLock{}
)

// lockRelayPath takes the delivery lock for one inject socket and returns its
// release function.
func lockRelayPath(path string) func() {
	lock := acquireRelayPathLock(path)
	lock.mu.Lock()
	return func() { releaseRelayPathLock(path, lock) }
}

// tryLockRelayPath takes the delivery lock only if it is free right now, so a
// periodic caller never waits out another delivery's submit delay (the idle
// guard runs on the shared heartbeat goroutine).
func tryLockRelayPath(path string) (func(), bool) {
	lock := acquireRelayPathLock(path)
	if !lock.mu.TryLock() {
		// The mutex was never taken here, so only the reference goes back.
		releaseRelayPathRef(path, lock)
		return nil, false
	}
	return func() { releaseRelayPathLock(path, lock) }, true
}

// acquireRelayPathLock returns the lock for a path with the caller counted as
// holder or waiter, creating the entry if needed.
func acquireRelayPathLock(path string) *relayPathLock {
	relayPathLocksMu.Lock()
	defer relayPathLocksMu.Unlock()
	lock, ok := relayPathLocks[path]
	if !ok {
		lock = &relayPathLock{}
		relayPathLocks[path] = lock
	}
	lock.refs++
	return lock
}

// releaseRelayPathLock drops one holder: it unlocks the mutex and gives the
// reference back.
func releaseRelayPathLock(path string, lock *relayPathLock) {
	lock.mu.Unlock()
	releaseRelayPathRef(path, lock)
}

// releaseRelayPathRef gives one reference back, and with the last one the map
// entry, so the map stays bounded by concurrent socket paths.
func releaseRelayPathRef(path string, lock *relayPathLock) {
	relayPathLocksMu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(relayPathLocks, path)
	}
	relayPathLocksMu.Unlock()
}

// handleStopAgent gracefully stops a running agent.
func (h *Handler) handleStopAgent(params map[string]any) Response {
	to, _ := params["to"].(string)
	project, _ := params["project"].(string)

	if to == "" {
		return errorResponse("to required")
	}

	agent, err := h.resolveAgent(to, project)
	if err != nil {
		return errorResponse(err.Error())
	}
	// The moment the stop was decided. Everything the stop later reports about
	// "still working" is measured against it: a process or a turn that appeared
	// before it says nothing about whether the stop rested the agent.
	requestedAt := time.Now()

	// Live agents have a process to end. Dormant agents (error, crashed) have
	// none, but stopping them is still how an orchestrator closes them for good:
	// otherwise attemptRestart revives an `error` agent with a restart strategy
	// and a pending respawn replaces a `crashed` one.
	//
	// pending belongs here for the same reason: it is the row a spawn writes
	// before it binds a process, so a daemon death in that window leaves a row
	// that agentGetActiveBySection counts as an owner of its section while every
	// guard skips it (they all want running/spawning). stop_agent is the only way
	// to close it, and if a spawn is still in flight the conditional bind refuses
	// afterwards and tears its process down.
	//
	// respawn_claimed is dormant for the same reason and one more: it is the token a
	// crash replacement holds (YM-397), and a stop that arrives while the replacement
	// starts must be able to revoke it. It is not in the live set on purpose, so it
	// owns no section.
	live := agent.Status == "running" || agent.Status == "paused" || agent.Status == "spawning" || agent.Status == "pending"
	dormant := agent.Status == "error" || agent.Status == "crashed" || agent.Status == "respawn_claimed"
	if !live && !dormant {
		return errorResponse(fmt.Sprintf("agent %s is %s, not stoppable", agent.ID, agent.Status))
	}

	// Try graceful exit via inject socket. Dormant agents are never signalled:
	// their PID is stale and may already belong to an unrelated process. The same
	// holds for a live-status agent whose PID does not verify as its own backend
	// process (C6) — the status is a claim about the past, the process check is
	// about now, and signaling a recycled PID reaches a stranger.
	verified := live && isAgentProcess(*agent)
	graceful := false
	injectPath := h.agentInjectPath(agent.ID)
	if verified && injectPath != "" && agent.Backend != "codex" {
		conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
		if err == nil {
			exitCmd := "/exit\r"
			if agent.Backend == "opencode" {
				// double ctrl+c — a single press doesn't exit this opencode version
				exitCmd = "\x03\x03"
			}
			_, writeErr := conn.Write([]byte(exitCmd))
			conn.Close()
			graceful = writeErr == nil
		}
	}

	// Codex: inject socket Ctrl+C doesn't work in raw-mode PTY;
	// SIGTERM triggers clean exit + session save (verified E2E). Routed through the
	// seam like every other agent signal, so a test that suppresses signals sees
	// none from this branch either; endAgentProcess repeats the SIGTERM right after
	// the exit grace, which is harmless for codex.
	if verified && agent.Backend == "codex" && agent.PID > 0 {
		agentProcSignal(agent.PID, syscall.SIGTERM)
		graceful = true
	}

	// A verified process ends here, and a successful graceful request is not proof
	// that it did: a TUI mid-turn reads /exit only at the end of its turn, so the
	// row would say "stopped" while the process kept working — and no guard looks at
	// closed rows (crashRecovery wants running, reapGoneAgents paused, attemptRestart
	// error). That is exactly the invisible agent YM-66 is about. A short grace lets
	// an idle backend finish the clean exit it was asked for; the SIGTERM and its
	// escalation below (endAgentProcess) end the rest.
	if verified && agent.PID > 0 && graceful {
		deadline := time.Now().Add(agentExitGrace)
		for time.Now().Before(deadline) && isPIDAlive(agent.PID) {
			time.Sleep(agentExitPoll)
		}
	}

	// A signal is a request, not an outcome (incident 18.10.2026: a row read
	// "stopped" while the session kept committing). Where a verified process was
	// signalled, the stop waits bounded for it to disappear and escalates to
	// SIGKILL if it does not (endAgentProcess); where nothing was signalled there
	// is nothing to wait for. The escalation is NOT gated on the caller wanting the
	// report: ending the process is not a probe, and a survivor behind a closed row
	// has no other owner. It costs time only when the process actually ignores
	// SIGTERM. What the guards skip is the report below (the residual probe and the
	// session read): they run inside the one-second supervision tick and read only
	// the error, so paying that verification per stopped agent would stall the
	// relays that tick carries.
	//
	// What the verification costs a caller that keeps it, worst case: the 3s inject
	// dial, the 300ms exit grace, the 1s window below plus the 1s kill grace, the
	// service discovery (up to its own 1s budget, internal/opencode) and the 1.5s
	// probe — about 7.8s. That is the price of an honest answer on an interactive
	// stop, and it is why the guards do not pay it.
	verify := !stopVerificationSkipped(params)
	signalled := verified && agent.PID > 0
	if signalled {
		// Not gated on verify: the grace costs time only when the process survives
		// SIGTERM, and ending it is not a probe — a survivor behind a closed row has
		// no other owner (YM-244).
		h.endAgentProcess(*agent, agentKillGrace)
	}
	if signalled && verify {
		deadline := time.Now().Add(stopExitVerifyWindow)
		for time.Now().Before(deadline) && pidHeldByAProcess(agent.PID) {
			time.Sleep(agentExitPoll)
		}
	}

	// A live-status agent whose PID no longer verifies is finalized like a
	// dormant one (C6), which is indistinguishable from a clean stop unless it is
	// recorded: an operator who reads "stopped" expects the process to be gone.
	stopReason := "stopped"
	if live && !verified {
		stopReason = "stopped: pid not verified (no signal sent)"
		log.Printf("[stop_agent] agent %s (%s) pid %d is not verified as its own process — finalizing without a signal",
			safeAgentText(agent.ID), safeAgentText(agent.Section), agent.PID)
	}

	// The report is a claim about the moment AFTER the signal, so it is only built
	// where the stop actually looked. A skipped verification (the heartbeat's own
	// stops) must not write a snapshot taken microseconds after SIGTERM into the
	// row: the process it just signalled would read as "still held", and the durable
	// field would turn into timing noise on the most frequent stop path.
	processGone := true
	sessionActive := "unknown"
	residual := ""
	if verify {
		processGone = agentProcessGone(*agent)
		sessionActive = h.agentSessionStillWorking(*agent, requestedAt)
		residual = h.stopResidual(*agent, signalled, processGone, sessionActive)
		if residual != "" {
			// Kept in the "stopped: <reason>" shape every other stop writer uses,
			// so a reader that strips the prefix still finds a reason.
			if stopReason == "stopped" {
				stopReason = "stopped: " + residual
			} else {
				stopReason += "; " + residual
			}
			log.Printf("[stop_agent] agent %s (%s) is closed but not resting: %s",
				safeAgentText(agent.ID), safeAgentText(agent.Section), residual)
		}
	}

	// The write is a compare-and-set on the PID this decision was made on: the
	// relaunch goroutine binds a process after its own "spawning" write, and a stop
	// that read the row before that bind would otherwise erase the binding and leave
	// the process running with no owner (see finalizeStop).
	written, err := h.finalizeStop(*agent, stopReason)
	if err != nil {
		return errorResponse(fmt.Sprintf("stop agent: %v", err))
	}
	if !written {
		return errorResponse(fmt.Sprintf("agent %s is changing under the stop (its pid moved twice) — retry stop_agent", agent.ID))
	}

	// Cascade: stop all child agents in the supervision tree. The cascade closes
	// their rows, so their processes have to end here as well — a stopped
	// orchestrator otherwise left live children that nothing relayed to and no
	// limit watched (YM-66).
	if stopped, err := h.store.AgentCascadeStop(agent.SessionID); err != nil {
		log.Printf("[stop_agent] cascade stop failed for %s: %v", agent.ID, err)
	} else if len(stopped) > 0 {
		log.Printf("[stop_agent] cascade stopped %d child agent(s) of %s", len(stopped), agent.ID)
		h.endCascadedProcesses(stopped)
	}

	// Clean up socket files, but only for a row that ever bound one (see
	// endCascadedProcesses for the same precondition).
	if agent.SockPath != "" {
		h.removeAgentSocketFiles(agent.ID)
	}

	// The row is closed either way: stop_agent's contract is the row, and a
	// residual process or session is reported instead of pretended away.
	response := map[string]any{
		"status":   "stopped",
		"agent_id": agent.ID,
		"section":  agent.Section,
	}
	if verify {
		response["process_gone"] = processGone
		response["session_active"] = sessionActive
		if residual != "" {
			response["residual"] = residual
		}
	}
	return jsonResponse(response)
}

// stopVerificationSkipped reports whether the caller asked for a stop without the
// post-signal verification. The heartbeat's own limit and orphan stops pass it:
// they run inside the one-second supervision tick, read only the error of the
// reply, and the verification would stall the relays that tick carries. The
// parameter carries the leading underscore that marks a daemon-internal field, so
// no MCP client mistakes it for part of the tool's surface.
func stopVerificationSkipped(params map[string]any) bool {
	v, ok := params["_verify"].(bool)
	return ok && !v
}

// stopExitVerifyWindow is how long a stop waits for the process it signalled to
// disappear before it reports that it is still there. Short enough for an
// interactive call, long enough for a TUI to act on SIGTERM.
const stopExitVerifyWindow = 1 * time.Second

// stopSessionProbeTimeout bounds the session read on the stop path. The read
// decides the wording of a reply, so it must not delay the stop itself. The
// service discovery that precedes it has its own budget (internal/opencode) and is
// not covered by this one.
const stopSessionProbeTimeout = 1500 * time.Millisecond

// agentSessionStillWorking answers whether the agent's backend session produced
// work after the given moment: "yes", "no", or "unknown" where the daemon cannot
// ask at all (no opencode backend, no bound session, no reachable service, an
// unreadable answer). Unknown is a separate answer on purpose: silence from a
// service that is not running is no evidence that nothing works.
func (h *Handler) agentSessionStillWorking(agent storage.Agent, since time.Time) string {
	if agent.Backend != "opencode" || agent.OpencodeSessionID == "" {
		return "unknown"
	}
	client := opencodeV2Service()
	if client == nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopSessionProbeTimeout)
	defer cancel()
	activity, err := client.AssistantActivitySince(ctx, agent.OpencodeSessionID, since)
	if err != nil {
		log.Printf("[stop_agent] agent %s (%s) session read failed: %v", safeAgentText(agent.ID), safeAgentText(agent.Section), err)
		return "unknown"
	}
	if activity.Since > 0 {
		return "yes"
	}
	// An open turn means the model is writing right now only while the turn is
	// young; an aborted turn stays open forever. The bounds are the idle guard's,
	// not new numbers: a single turn and a background job age differently, and the
	// client hands both over with the start time precisely so the caller applies
	// its own limit.
	if activity.Working {
		bound := yesloopIdleWorkingMax
		if activity.Background {
			bound = yesloopIdleBackgroundMax
		}
		if activity.WorkingSince.IsZero() || time.Since(activity.WorkingSince) < bound {
			return "yes"
		}
	}
	return "no"
}

// stopResidual names what a stop left behind, or returns "" when the agent really
// rests. It is the text half of process_gone/session_active, and it ends up in the
// row and in the reply, so it names the concrete holder of the PID and the session
// rather than just the fact.
func (h *Handler) stopResidual(agent storage.Agent, signalled, processGone bool, sessionActive string) string {
	var parts []string
	if !processGone && agent.PID > 0 {
		if signalled {
			parts = append(parts, fmt.Sprintf("PID %d is still held after the stop (SIGTERM is a request, not an outcome)", agent.PID))
		} else {
			parts = append(parts, fmt.Sprintf("PID %d is still held and could not be verified as this agent's process, so no signal was sent", agent.PID))
		}
	}
	if sessionActive == "yes" {
		parts = append(parts, fmt.Sprintf("backend session %s produced a turn after the stop, so the work continues outside the row", safeAgentText(agent.OpencodeSessionID)))
	}
	return strings.Join(parts, "; ")
}

// endCascadedProcesses ends the processes of agents a cascade closed. Their rows
// were written by the cascade, so nothing else would end them, and a live process
// with a closed row is exactly the invisible agent YM-66 is about. Each signal
// keeps the identity gate every other signal in this package uses: a PID that does
// not verify as the agent's own process is left alone.
func (h *Handler) endCascadedProcesses(stopped []storage.Agent) {
	var signalled []storage.Agent
	for i := range stopped {
		a := &stopped[i]
		// The socket files are removed HERE for every closed child, not left to its
		// exit tail: the cascade clears the row's pid, so the tail no longer
		// recognizes the row as its own (`ours` compares the pid) and keeps the
		// files. A dead socket at a per-id path is harmless but it accumulates
		// (cold review, YM-244).
		if a.SockPath != "" {
			h.removeAgentSocketFiles(a.ID)
		}
		if isAgentProcess(*a) {
			// The signal ends the process; its own exit tail removes the socket
			// files of the id once it is gone.
			log.Printf("[stop_agent] ending cascaded child %q (PID %d) — its row was closed with the tree", a.ID, a.PID)
			agentProcSignal(a.PID, syscall.SIGTERM)
			signalled = append(signalled, *a)
		}
	}
	h.escalateSurvivors(signalled)
}

// escalateSurvivors gives a batch of signalled agents one shared window and then
// SIGKILLs the ones that ignored SIGTERM. One window for the batch: a per-agent
// grace would multiply by the tree size, and every survivor is a process behind a
// closed row that no guard watches (YM-244). The identity gate is re-run before the
// SIGKILL: a pid that no longer identifies as its agent's process may be recycled.
func (h *Handler) escalateSurvivors(signalled []storage.Agent) {
	if len(signalled) == 0 {
		return
	}
	deadline := time.Now().Add(agentKillGrace)
	for time.Now().Before(deadline) && anyAgentProcessAlive(signalled) {
		time.Sleep(agentExitPoll)
	}
	for i := range signalled {
		a := &signalled[i]
		if !isPIDAlive(a.PID) || !isAgentProcess(*a) {
			continue
		}
		log.Printf("[stop_agent] agent %q (PID %d) ignored SIGTERM for %s, sending SIGKILL",
			safeAgentText(a.ID), a.PID, agentKillGrace)
		agentProcSignal(a.PID, syscall.SIGKILL)
	}
}

// anyAgentProcessAlive reports whether any of the rows still names a live process.
func anyAgentProcessAlive(agents []storage.Agent) bool {
	for i := range agents {
		if isPIDAlive(agents[i].PID) {
			return true
		}
	}
	return false
}

// pidFieldIsClear reports whether a pid value from update_agent means "this row
// holds no process". Only clearing is allowed there — see handleUpdateAgent.
func pidFieldIsClear(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case float64:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	case string:
		s := strings.TrimSpace(t)
		return s == "" || s == "0"
	}
	return false
}

// agentSockPath returns the socket path the daemon creates for an agent id, or
// "" when the id cannot name a plain file inside the data directory.
//
// The path is NOT read from agents.sock_path: h.dataDir is the daemon's whole
// state directory, so following that column would let a caller delete
// agents.db or config.yaml (YM-274), and the supervisor paths did exactly that
// (YM-287).
func (h *Handler) agentSockPath(agentID string) string {
	if h.dataDir == "" || agentID == "" {
		return ""
	}
	// The id names the file, so it has to stay a plain path element: a separator
	// would walk the unlink through a symlinked directory that sits inside the
	// data directory, which no lexical check on the joined path can see.
	if agentID != filepath.Base(agentID) {
		log.Printf("[agent_socket] refusing socket path for agent id %q", agentID)
		return ""
	}
	// The daemon's own sockets are never an agent's to clean up.
	if name := agentID + ".sock"; name == sockName || name == jobSockName {
		log.Printf("[agent_socket] refusing socket path for agent id %q: infrastructure socket", agentID)
		return ""
	}
	return filepath.Join(h.dataDir, agentID+".sock")
}

// agentInjectPath returns the inject socket the daemon dials for an agent, or ""
// when the id cannot name one. Like the unlink paths, the dial target is derived
// from the id and not read from agents.sock_path: that column is writable through
// update_agent and register_agent, so following it would let a caller point a
// guard relay, a stop's /exit or a permission kick at a foreign socket (audit
// 16.10.2026, YM-338).
func (h *Handler) agentInjectPath(agentID string) string {
	sockPath := h.agentSockPath(agentID)
	if sockPath == "" {
		return ""
	}
	return sockPath + ".inject"
}

// errNoAgentSocket is returned when no inject socket path can be derived from an
// agent id.
var errNoAgentSocket = errors.New("socket path could not be derived from the agent id")

// removeAgentSocketFiles unlinks the socket files the daemon created for an
// agent, derived from the agent id (see agentSockPath).
func (h *Handler) removeAgentSocketFiles(agentID string) {
	sockPath := h.agentSockPath(agentID)
	if sockPath == "" {
		return
	}
	os.Remove(sockPath)
	os.Remove(sockPath + ".inject")
}

// agentSockPathForSpawn derives the socket path a spawn binds and closes the row
// when the id cannot name one. An empty path is not an error for net.Listen:
// "unix" with an empty address binds an abstract socket, so the spawn would come
// up on a path neither the console nor the relay can reach (YM-287).
func (h *Handler) agentSockPathForSpawn(id string) (string, Response) {
	sockPath := h.agentSockPath(id)
	if sockPath != "" {
		return sockPath, Response{}
	}
	msg := "socket path could not be derived from the agent id"
	if err := h.store.AgentUpdate(id, map[string]any{
		"status":     "error",
		"error":      msg,
		"stopped_at": time.Now().Format(time.RFC3339),
	}); err != nil {
		// A failed close leaves the row in spawning/pending, which is the one
		// state that blocks the section. Storage errors only, but it must be
		// visible in the log.
		log.Printf("[agent_socket] agent %s: closing the row after the refused spawn failed: %v", id, err)
	}
	return "", errorResponse(msg)
}

// finalizeStop ends an agent row's life as a stop — status stopped, no process — in
// a compare-and-set on the PID the caller's snapshot carries.
//
// The CAS is what makes the restart window safe on both sides. The relaunch
// goroutine writes "spawning" and then binds the new process; a stop that read the
// row before that bind would write the stopped status over a binding it never saw,
// and the process would keep working on a section everything else already treats as
// stopped. A refused write therefore means exactly that: a process is there which
// the snapshot did not carry, so it is ended through the same identity gate as every
// other signal in this file, and the stop is retried on the PID that displaced it.
// Two attempts suffice in a window this narrow; a row that moves twice is reported
// to the caller instead of being overwritten silently. Reports whether the row was
// written.
func (h *Handler) finalizeStop(agent storage.Agent, stopReason string) (bool, error) {
	now := time.Now().Format(time.RFC3339)
	expected := agent.PID
	for attempt := 0; attempt < 2; attempt++ {
		written, err := h.store.AgentFinalizeStop(agent.ID, expected, now, stopReason)
		if err != nil {
			return false, err
		}
		if written {
			return true, nil
		}
		fresh, err := h.store.AgentGet(agent.ID)
		if err != nil || fresh == nil {
			return false, fmt.Errorf("the row vanished while it was being stopped")
		}
		if fresh.PID == expected {
			// The write was refused although the PID is unchanged — nothing a retry
			// could act on. Report it instead of looping.
			return false, nil
		}
		// A bind is the only in-tree writer of a PID on an existing row, and it
		// installs a process this row's backend started — but the signal keeps the
		// same identity gate as every other one, so a PID that does not identify as
		// this agent's own process is left alone. It gets the same escalation as the
		// main stop path: a bound process that ignores SIGTERM would otherwise
		// outlive the stop that displaced it (YM-244). The grace is paid here too,
		// including from the supervision tick: it costs time only when the process
		// actually survives SIGTERM, and an unescalated survivor is the orphan no
		// guard can see. What the tick skips is the answer probe, not this.
		if fresh.PID > 0 {
			h.endAgentProcess(*fresh, agentKillGrace)
		}
		expected = fresh.PID
	}
	return false, nil
}

// agentExitGrace is how long a stop waits for the graceful exit it asked for
// before ending the process itself. Long enough for an idle TUI to act, short
// enough not to stall the heartbeat tick that stops a limited agent.
const (
	agentExitGrace = 300 * time.Millisecond
	agentExitPoll  = 50 * time.Millisecond
)

// agentKillGrace bounds the wait between SIGTERM and SIGKILL. SIGTERM is a
// request; a process that ignores it keeps its pid, its session and its section
// behind a row that already reads "stopped", and no guard looks at closed rows —
// so nothing else would ever escalate (YM-244). Bounded short: it sits between an
// interactive stop and its answer.
const agentKillGrace = 1 * time.Second

// agentProcSignal delivers a signal to an agent process. A var for the same
// reason as daemonProcSignal in lifecycle.go: a test drives the SIGTERM→SIGKILL
// order and the residual report without a real process having to die or survive.
var agentProcSignal = func(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// endAgentProcess ends the process a row names: SIGTERM, a bounded wait, and —
// only while the pid still identifies as this agent's own process — SIGKILL. The
// identity gate is the one every other signal in this file uses (isAgentProcess):
// a pid that no longer verifies may have been recycled and is left alone. A grace
// of 0 sends the request without waiting, which is what the supervision tick does
// (it must not sit still for one agent).
//
// Reports whether the process was escalated to SIGKILL.
func (h *Handler) endAgentProcess(a storage.Agent, grace time.Duration) bool {
	if a.PID <= 0 || !isAgentProcess(a) {
		return false
	}
	agentProcSignal(a.PID, syscall.SIGTERM)
	if grace <= 0 {
		return false
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && isPIDAlive(a.PID) {
		time.Sleep(agentExitPoll)
	}
	if !isPIDAlive(a.PID) || !isAgentProcess(a) {
		return false
	}
	log.Printf("[stop_agent] agent %s (%s) ignored SIGTERM for %s, sending SIGKILL",
		safeAgentText(a.ID), safeAgentText(a.Section), grace)
	agentProcSignal(a.PID, syscall.SIGKILL)
	return true
}

// stopAllPause is the pause between two agent exits in the stop-all run:
// every process death tears down its terminal surface, and in a mass teardown
// the GTK/Mesa main loop of a single-instance terminal deadlocks
// (incident 2026-09-02 09:24). Individual teardown runs do not trigger this.
const stopAllPause = 250 * time.Millisecond

// handleStopAllAgents stops all running agents in a project.
//
// It signals in one pass, waits once for the whole set, and only then writes the
// rows — with the same process/session report handleStopAgent gives per agent.
// A bulk stop that returned nothing but a count let the incident symptom
// reappear: rows read "stopped" while a surviving process or a still-writing
// backend session kept working (YM-356).
//
// Cost: the process exit window is paid once for the whole run, but the session
// probe is per agent (agentSessionStillWorking, up to stopSessionProbeTimeout
// each) and only for opencode rows with a bound session. A project with many such
// rows therefore takes noticeably longer than one — the honest answer per agent is
// worth more than a shared budget that would report "unknown" for the majority.
// stop_all_agents is not in isFastMCPMethod, so a caller with a short deadline may
// give up while the rows are already being written.
func (h *Handler) handleStopAllAgents(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}

	agents, err := h.store.AgentList(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("list agents: %v", err))
	}
	// signalledAt is the moment THIS target was signalled: the session probe asks
	// what the backend did after the stop, and in a bulk run the signal pass takes
	// time, so the call-wide timestamp would count work a later target did while
	// waiting its turn (cold review 5.3).
	type stopAllTarget struct {
		agent       storage.Agent
		verified    bool
		signalled   bool
		signalledAt time.Time
		baseReason  string
	}
	var targets []stopAllTarget

	stopped := 0
	var signalled []storage.Agent
	for i, a := range agents {
		if a.Status != "running" && a.Status != "paused" && a.Status != "spawning" && a.Status != "pending" {
			continue
		}
		// Same gate as handleStopAgent (C6): a status is a claim about the past,
		// so only a PID verified as the agent's own process may be reached. One
		// stop-all call otherwise walks every PID in the project, recycled or not.
		verified := isAgentProcess(a)
		// Try graceful exit
		injectPath := h.agentInjectPath(a.ID)
		if verified && injectPath != "" {
			if conn, err := net.DialTimeout("unix", injectPath, 2*time.Second); err == nil {
				exitCmd := "/exit\r"
				if a.Backend == "opencode" {
					// double ctrl+c — a single press does not exit this opencode
					// version (see handleStopAgent)
					exitCmd = "\x03\x03"
				}
				conn.Write([]byte(exitCmd))
				conn.Close()
			}
		}
		// Fallback: SIGTERM, through the seam and with the same escalation as the
		// single stop — a bulk teardown that leaves a SIGTERM-ignoring process
		// behind was exactly the invisible agent YM-244 is about. The escalation
		// runs once for the whole batch below, not per agent.
		if verified && a.PID > 0 {
			agentProcSignal(a.PID, syscall.SIGTERM)
			signalled = append(signalled, a)
		}
		if !verified {
			log.Printf("[stop_all_agents] agent %s (%s) pid %d is not verified as its own process — finalizing without a signal",
				safeAgentText(a.ID), safeAgentText(a.Section), a.PID)
		}
		signalledAt := time.Now()

		baseReason := "stopped"
		if !verified && (a.Status == "running" || a.Status == "paused") {
			// Same wording as handleStopAgent: a caller who reads "stopped" expects
			// the process to be gone, so the missing signal is recorded.
			baseReason = "stopped: pid not verified (no signal sent)"
		}
		// The row stops claiming "running" WITH the signal, not after the whole
		// verification pass. crashRecovery reads running rows every 5s and treats a
		// dead PID as a crash: it would quarantine the session, taint the
		// scratchpad and start a replacement for an agent that was deliberately
		// stopped — irreversibly, even though the later finalize wins the row
		// (cold review 5.3).
		written, err := h.finalizeStop(a, baseReason)
		if err != nil {
			log.Printf("[stop_all_agents] agent %s: %v", safeAgentText(a.ID), err)
			continue
		}
		if !written {
			log.Printf("[stop_all_agents] agent %s: the row is changing under the stop — left for a retry", safeAgentText(a.ID))
			continue
		}
		if a.SockPath != "" {
			h.removeAgentSocketFiles(a.ID)
		}
		stopped++

		targets = append(targets, stopAllTarget{
			agent:       a,
			verified:    verified,
			signalled:   verified && a.PID > 0,
			signalledAt: signalledAt,
			baseReason:  baseReason,
		})

		// Pause between stops: the next surface teardown must be processed
		// individually by the terminal, see stopAllPause.
		if i < len(agents)-1 {
			time.Sleep(stopAllPause)
		}
	}

	h.escalateSurvivors(signalled)

	// One exit window for the whole set: signals were sent above, so waiting once
	// afterwards is the same observation handleStopAgent makes for a single
	// agent, at a fraction of the cost.
	deadline := time.Now().Add(stopExitVerifyWindow)
	for time.Now().Before(deadline) {
		held := false
		for _, target := range targets {
			if target.signalled && pidHeldByAProcess(target.agent.PID) {
				held = true
				break
			}
		}
		if !held {
			break
		}
		time.Sleep(agentExitPoll)
	}

	reports := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		a := target.agent
		processGone := agentProcessGone(a)
		sessionActive := h.agentSessionStillWorking(a, target.signalledAt)
		residual := h.stopResidual(a, target.signalled, processGone, sessionActive)
		stopReason := target.baseReason
		if residual != "" {
			// Same shape as handleStopAgent: "stopped: <reason>" when the row had
			// nothing else to say, otherwise appended to the existing reason.
			if stopReason == "stopped" {
				stopReason = "stopped: " + residual
			} else {
				stopReason += "; " + residual
			}
			log.Printf("[stop_all_agents] agent %s (%s) is closed but not resting: %s",
				safeAgentText(a.ID), safeAgentText(a.Section), residual)
			// The row was closed with the signal; the residual is appended only if
			// the row is still the one this run wrote. A resume or a later stop that
			// moved it wins (compare-and-set on status + progress), and the answer
			// still carries the residual.
			if _, err := h.store.AgentUpdateIfMatches(a.ID, "stopped", target.baseReason,
				map[string]any{"progress": stopReason}); err != nil {
				log.Printf("[stop_all_agents] agent %s: recording the residual failed: %v", safeAgentText(a.ID), err)
			}
		}

		report := map[string]any{
			"agent_id":       a.ID,
			"section":        a.Section,
			"process_gone":   processGone,
			"session_active": sessionActive,
		}
		if residual != "" {
			report["residual"] = residual
		}
		reports = append(reports, report)
	}

	return jsonResponse(map[string]any{
		"project": project,
		"stopped": stopped,
		"agents":  reports,
	})
}

// handleResumeAgent restarts a stopped/paused agent using its existing session.
func (h *Handler) handleResumeAgent(params map[string]any) Response {
	to, _ := params["to"].(string)
	project, _ := params["project"].(string)
	explicitSession, _ := params["session_id"].(string)

	if to == "" && explicitSession == "" {
		return errorResponse("to or session_id required")
	}

	// An explicit session id (claude session, opencode ses_*, codex session)
	// resolves via AgentGetAnyBySession — it is often the only identifier still
	// known once an agent stopped.
	var agent *storage.Agent
	var resolveErr error
	// Placement sources for the new window, resolved the way a spawn resolves
	// them: the caller's own window when it has one (the per-call _session_id),
	// and the reply target as the fallback. YM-193.
	parentPid := callerPidParam(params)
	parentWindow := h.callerWindowFromParams(params)
	if explicitSession != "" {
		if a, err := h.store.AgentGetAnyBySession(explicitSession); err == nil && a != nil {
			agent = a
		}
	}
	if agent == nil && to != "" {
		a, err := h.resolveAgent(to, project)
		if err == nil {
			agent = a
		} else {
			resolveErr = err
		}
	}
	if agent == nil {
		// Bare opencode session id with no agent row: bootstrap a fresh agent
		// when opencode's own DB knows the session — the ses_* id is often the
		// only surviving identifier once agent rows are gone.
		target := explicitSession
		if target == "" {
			target = to
		}
		if dir := opencodeSessionDir(target); dir != "" {
			return h.bootstrapOpencodeResume(target, dir, project, parentPid, parentWindow)
		}
		if resolveErr != nil {
			return errorResponse(resolveErr.Error())
		}
		if to == "" {
			return errorResponse(fmt.Sprintf("no agent found for session_id %q", explicitSession))
		}
		return errorResponse(fmt.Sprintf("no agent found matching %q (project=%q)", to, project))
	}

	if agent.Status != "stopped" && agent.Status != "paused" && agent.Status != "finished" {
		return errorResponse(fmt.Sprintf("agent %s is %s, not resumable", agent.ID, agent.Status))
	}
	// A paused row is a live process behind an open PTY bridge — the agent is
	// waiting for input (see AgentBindProcessIfLive and normalizeAgentStatus), so
	// resuming it would start a SECOND process for the same session. The row is
	// rebound to the new one, and when the first process finally exits, its exit
	// tail clears the successor's pid, socket path and status and unlinks its
	// sockets (YM-70). relay_agent is the way to reach a paused agent
	// (Learning #81175). Only a paused row whose process is proven gone (or which
	// never carried one) falls through to a resume.
	if agent.Status == "paused" && isAgentProcess(*agent) {
		return errorResponse(fmt.Sprintf(
			"agent %s is paused with a live process (PID %d) — use relay_agent to reach it, resume_agent would start a second process",
			agent.ID, agent.PID))
	}
	if agent.Backend == "" {
		agent.Backend = "claude"
	}
	if agent.SessionID == "" && explicitSession == "" {
		return errorResponse(fmt.Sprintf("agent %s has no session_id to resume", agent.ID))
	}
	// L4: opencode resume requires a captured OpencodeSessionID. An explicitly
	// passed ses_* is accepted and persisted below; without either, reject so
	// spawnAgentProcess never falls back to the daemon UUID (opencode exit 1,
	// Learning #80228). A session that appeared after the spawn-time capture
	// window is bound on demand here rather than rejecting the agent — a resumable
	// agent that is merely late must not be declared unresumable.
	if agent.Backend == "opencode" && agent.OpencodeSessionID == "" && explicitSession == "" {
		agent = h.ensureOpencodeSession(agent)
	}
	if agent.Backend == "opencode" && agent.OpencodeSessionID == "" && explicitSession == "" {
		return errorResponse(fmt.Sprintf("agent %s is %s", agent.ID, opencodeUnresumableMsg))
	}
	if active, err := h.store.AgentGetActiveBySection(agent.Project, agent.Section); err == nil && active != nil && active.ID != agent.ID {
		return errorResponse(fmt.Sprintf("section %q already has active agent %s (status=%s)", agent.Section, active.ID, active.Status))
	}

	// No ownership check is needed for an explicitly passed session: it is
	// resolved through AgentGetAnyBySession above, so the request lands on the
	// row that already owns the id and can never attach it to a second row.
	//
	sockPath, pathResp := h.agentSockPathForSpawn(agent.ID)
	if pathResp.Error != "" {
		return pathResp
	}
	// The recorded work dir decides where the backend starts. A resume must not
	// invent a location: the same gate the restart paths use (agentRestartWorkDir)
	// refuses a recorded tree that is gone unless the backend session names a
	// replacement, because the project-path/run-dir fallback below would start the
	// agent outside any repository (YM-356, YM-286).
	workDir, refusal := h.agentRestartWorkDir(*agent)
	if refusal != "" {
		return errorResponse(fmt.Sprintf("agent %s: %s", safeAgentText(agent.ID), safeAgentText(refusal)))
	}
	workDir = h.resolveAgentWorkDir(agent.Project, workDir)
	if workDir == "" {
		// A row from before the project carried a path (or whose tree is gone) has
		// no directory to resume into. The daemon's own cwd is not the agent's
		// project, and inventing one is how a second checkout came into existence
		// (YM-286); the daemon-owned run dir is the defined fallback.
		if dir, err := EnsureRunWorkDir(h.dataDir, agent.ID); err == nil {
			log.Printf("[agents] agent %s: project %q has no work dir, resuming in %s", agent.ID, agent.Project, dir)
			workDir = dir
		} else {
			return errorResponse(fmt.Sprintf("agent %s: project %q has no working directory to resume into", agent.ID, agent.Project))
		}
	}
	update := map[string]any{
		"status":      "pending",
		"pid":         0,
		"sock_path":   "",
		"relay_count": 0,
		"progress":    "resuming",
		"error":       "",
		"stopped_at":  "",
	}
	// Persist an explicitly passed backend session id so spawnAgentProcess's
	// resume branch (which prefers the stored dedicated column) uses it. The
	// opencode column is written by the guarded binder rather than the generic
	// update, so no caller can plant a session on a row through update_agent.
	switch agent.Backend {
	case "opencode":
		if agent.OpencodeSessionID == "" && explicitSession != "" {
			bound, err := h.store.AgentBindSession(agent.ID, explicitSession, "")
			if err != nil {
				return errorResponse(fmt.Sprintf("attach session %s: %v", explicitSession, err))
			}
			if !bound {
				return errorResponse(fmt.Sprintf("session %s could not be attached (another agent owns it)", explicitSession))
			}
			agent.OpencodeSessionID = stripSessionPrefix(explicitSession)
		}
	case "codex":
		if agent.CodexSessionID == "" && explicitSession != "" {
			update["codex_session_id"] = explicitSession
		}
	}
	if err := h.store.AgentUpdate(agent.ID, update); err != nil {
		return errorResponse(fmt.Sprintf("resume agent: %v", err))
	}
	if agent.WorkDir != workDir {
		// A row whose project resolves to no directory resumes in the daemon-owned
		// run dir; the row has to name the directory its process really got, or a
		// later bind cannot tell this agent's sessions apart (YM-286).
		if err := h.store.AgentSetWorkDir(agent.ID, workDir); err != nil {
			return errorResponse(fmt.Sprintf("resume agent: record work dir: %v", err))
		}
	}
	// claude resumes via the session-id CLI arg; an explicit session wins.
	resumeSession := agent.SessionID
	if agent.Backend == "claude" && explicitSession != "" {
		resumeSession = explicitSession
	}
	// The new window belongs on the caller's desktop. A caller without a window
	// of its own — the heartbeat, a rebind, an agent resuming another agent —
	// falls back to the window of the session that spawned the resumed agent,
	// exactly as callerWindowFromParams does for spawn_agent (YM-193).
	if parentWindow == "" && agent.CallerSession != "" {
		placement := make(map[string]any, len(params)+1)
		for k, v := range params {
			placement[k] = v
		}
		placement["caller_session"] = agent.CallerSession
		parentWindow = h.callerWindowFromParams(placement)
	}
	// The guard the other spawn routes carry (spawn_agent, open_terminal,
	// bootstrap resume): in the suite it keeps the background goroutine from
	// starting a real backend process and a terminal window, which then writes
	// into the test's store and temp dir after the test returned.
	if !h.disableAgentProcesses {
		go h.spawnAgentProcess(agent.ID, resumeSession, agent.Project, agent.Section, "", sockPath, workDir, agent.Backend, agent.Model, agent.Theme, agent.MaxTurns, parentPid, parentWindow, true)
	}

	return jsonResponse(map[string]any{
		"status":   "resuming",
		"agent_id": agent.ID,
		"section":  agent.Section,
	})
}

// existingWorkDir returns dir when it is still a directory on disk, and "" when
// it is empty or gone. A bind query is scoped by the stored work_dir, and a
// resume should restart the backend in the tree its session belongs to — but a
// worktree is removed once its branch is merged, and resuming into a directory
// that no longer exists would fail the spawn instead of falling back.
func (h *Handler) existingWorkDir(dir string) string {
	if dir == "" {
		return ""
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

// bootstrapOpencodeResume creates a fresh agent row for a bare opencode
// session id that references no yesmem row, using the session directory from
// opencode's DB as the agent workDir.
func (h *Handler) bootstrapOpencodeResume(sessionID, workDir, project string, parentPid int, parentWindow string) Response {
	if project == "" {
		project = workDir
	}
	short := strings.TrimPrefix(sessionID, "ses_")
	if len(short) > 8 {
		short = short[:8]
	}
	section := "resumed-" + short
	if existing, err := h.store.AgentGetActiveBySection(project, section); err == nil && existing != nil {
		// Session already bootstrapped before — resume that row instead.
		return h.handleResumeAgent(map[string]any{"to": existing.ID, "session_id": sessionID})
	}

	id, err := h.store.AgentNextID(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("generate agent ID: %v", err))
	}
	agent := storage.Agent{
		ID:        id,
		Project:   project,
		Section:   section,
		SessionID: generateAgentUUID(),
		Status:    "pending",
		Backend:   "opencode",
		WorkDir:   workDir,
	}
	if err := h.store.AgentCreate(agent); err != nil {
		return errorResponse(fmt.Sprintf("create agent: %v", err))
	}
	// The session is attached through the guarded writer like every other bind:
	// bootstrap is only reached when no row owns the id (handleResumeAgent
	// resolves by session first), but two concurrent boots of the same session
	// would otherwise both write it.
	if !h.bindAgentSession(&agent, sessionID, "bootstrap") {
		if err := h.store.AgentDelete(id); err != nil {
			log.Printf("[bootstrap_resume] agent %s: rolling back the failed attach failed: %v", id, err)
		}
		return errorResponse(fmt.Sprintf("session %s could not be attached (another agent owns it)", sessionID))
	}
	if !h.disableAgentProcesses {
		sockPath, pathResp := h.agentSockPathForSpawn(id)
		if pathResp.Error != "" {
			return pathResp
		}
		go h.spawnAgentProcess(id, agent.SessionID, project, section, "", sockPath, workDir, "opencode", "", "", 0, parentPid, parentWindow, true)
	}
	return jsonResponse(map[string]any{
		"status":       "resuming",
		"agent_id":     id,
		"section":      section,
		"work_dir":     workDir,
		"bootstrapped": true,
	})
}

// handleUpdateAgentStatus lets an agent report its current phase (semantic state).
// Mechanical metrics (turns, tokens) are tracked by the proxy automatically.
func (h *Handler) handleUpdateAgentStatus(params map[string]any) Response {
	id, _ := params["id"].(string)
	phase, _ := params["phase"].(string)

	if id == "" {
		sessionID := h.resolveSessionID(params, "session_id")
		if sessionID != "" {
			if agent, err := h.store.AgentGetAnyBySession(sessionID); err == nil && agent != nil {
				id = agent.ID
			}
		}
	}
	if id == "" {
		return errorResponse("id or session_id required")
	}
	if phase == "" {
		return errorResponse("nothing to update")
	}
	if err := h.store.AgentUpdate(id, map[string]any{
		"phase":        phase,
		"heartbeat_at": time.Now().Format(time.RFC3339),
	}); err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(map[string]any{"status": "ok", "id": id})
}

// handleListAgents returns all agents, optionally filtered by project.
// handleTrackUsage records token usage reported by the proxy (internal RPC, not exposed via MCP).
func (h *Handler) handleTrackUsage(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)
	if threadID == "" {
		return errorResponse("thread_id required")
	}
	inputTokens := 0
	if v, ok := params["input_tokens"].(float64); ok {
		inputTokens = int(v)
	}
	outputTokens := 0
	if v, ok := params["output_tokens"].(float64); ok {
		outputTokens = int(v)
	}
	cacheReadTokens := 0
	if v, ok := params["cache_read_tokens"].(float64); ok {
		cacheReadTokens = int(v)
	}
	cacheWriteTokens := 0
	if v, ok := params["cache_write_tokens"].(float64); ok {
		cacheWriteTokens = int(v)
	}
	if inputTokens == 0 && outputTokens == 0 {
		return jsonResponse(map[string]any{"status": "skipped"})
	}

	source, _ := params["source"].(string)
	if source == "fork" {
		if err := h.store.TrackForkTokenUsage(threadID, inputTokens, outputTokens); err != nil {
			return errorResponse(fmt.Sprintf("track fork usage: %v", err))
		}
	} else {
		if err := h.store.TrackTokenUsage(threadID, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens); err != nil {
			return errorResponse(fmt.Sprintf("track usage: %v", err))
		}
	}

	// Persist rate-limit snapshot if provided
	if rlJSON, ok := params["rate_limits"].(string); ok && rlJSON != "" {
		_ = h.store.SetProxyState("rate_limits", rlJSON)
	}

	// Update agent telemetry if this thread belongs to an agent session.
	// Two match strategies, both exact:
	//  1. session match (daemon UUID, opencode_session_id or codex_session_id)
	//  2. proxy_thread_id match (set by an earlier exact mapping)
	// An unknown thread is not attached to "the newest agent in the project":
	// that guess bound non-agent sessions (the orchestrator's own thread) to
	// freshly spawned agents and fed them foreign telemetry.
	if threadID != "" {
		if agent, err := h.store.AgentGetAnyBySession(threadID); err == nil && agent != nil {
			h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
			RegisterSessionThread(agent.SessionID, threadID)
		} else if agent, err := h.store.AgentGetByProxyThreadID(threadID); err == nil && agent != nil {
			h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
		} else if agent := h.bindAgentForOpencodeThread(threadID); agent != nil {
			h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
			RegisterSessionThread(agent.SessionID, threadID)
		}
	}

	return jsonResponse(map[string]any{"status": "ok", "thread_id": threadID})
}

// handlePersistRateLimits stores rate-limit data from the proxy (internal RPC, not exposed via MCP).
// Separated from _track_usage so rate limits are persisted even when threadID is empty.
func (h *Handler) handlePersistRateLimits(params map[string]any) Response {
	rlJSON, _ := params["rate_limits"].(string)
	if rlJSON == "" {
		return errorResponse("rate_limits required")
	}
	_ = h.store.SetProxyState("rate_limits", rlJSON)
	return jsonResponse(map[string]any{"status": "ok"})
}

// handleListAgents returns all agents, optionally filtered by project.
func (h *Handler) handleListAgents(params map[string]any) Response {
	project, _ := params["project"].(string)

	agents, err := h.store.AgentList(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("list agents: %v", err))
	}

	// The same projection get_agent uses: serializing the raw rows returned the
	// ownership capability (agents.session_id) to every caller (cold review F2).
	rows := make([]map[string]any, 0, len(agents))
	for i := range agents {
		rows = append(rows, agentToMap(&agents[i]))
	}

	return jsonResponse(map[string]any{
		"agents": rows,
		"count":  len(rows),
	})
}

// handleGetAgent returns detailed info about a specific agent.
func (h *Handler) handleGetAgent(params map[string]any) Response {
	to, _ := params["to"].(string)
	project, _ := params["project"].(string)

	if to == "" {
		return errorResponse("to required")
	}

	agent, err := h.resolveAgent(to, project)
	if err != nil {
		return errorResponse(err.Error())
	}
	agent = h.ensureOpencodeSession(agent)

	// Enrich with stream tracking fields
	result := agentToMap(agent)
	result = h.enrichAgentWithStreamFields(result)

	return jsonResponse(result)
}

// agentToMap converts an Agent struct to a map[string]any for enrichment.
//
// session_id is deliberately absent: it is the row's identity for
// AgentBindProcessIfLive / AgentCloseUnboundSpawn, and the read APIs must not hand
// out what a caller needs to claim a row it was never given (cold review F2,
// YM-356). The caller that spawned the row receives it in the spawn reply; the
// backend sessions stay visible as opencode_session_id and codex_session_id.
func agentToMap(a *storage.Agent) map[string]any {
	return map[string]any{
		"id":                  a.ID,
		"project":             a.Project,
		"section":             a.Section,
		"pid":                 a.PID,
		"sock_path":           a.SockPath,
		"status":              a.Status,
		"recommended_action":  recommendedActionForStatus(a.Status),
		"caller_session":      a.CallerSession,
		"error":               a.Error,
		"heartbeat_at":        a.HeartbeatAt,
		"progress":            a.Progress,
		"relay_count":         a.RelayCount,
		"depth":               a.Depth,
		"token_budget":        a.TokenBudget,
		"retry_count":         a.RetryCount,
		"backend":             a.Backend,
		"model":               a.Model,
		"theme":               a.Theme,
		"proxy_thread_id":     a.ProxyThreadID,
		"work_dir":            a.WorkDir,
		"turns_used":          a.TurnsUsed,
		"input_tokens":        a.InputTokens,
		"output_tokens":       a.OutputTokens,
		"last_activity_at":    a.LastActivityAt,
		"phase":               a.Phase,
		"created_at":          a.CreatedAt,
		"stopped_at":          a.StoppedAt,
		"restart_strategy":    a.RestartStrategy,
		"restart_count":       a.RestartCount,
		"max_restarts":        a.MaxRestarts,
		"liveness_ping_at":    a.LivenessPingAt,
		"last_restart_at":     a.LastRestartAt,
		"permission_kick":     a.PermissionKick,
		"opencode_session_id": a.OpencodeSessionID,
		"codex_session_id":    a.CodexSessionID,
	}
}

// recommendedActionForStatus maps an agent status to the single MCP action an
// orchestrator should take next. Empty string for statuses with no clear
// recommendation (orchestrator decides).
func recommendedActionForStatus(status string) string {
	switch status {
	case "running":
		return "monitor"
	case "paused":
		return "relay_agent"
	case "stopped", "failed", "error":
		return "manual restart"
	case "spawning", "pending":
		return "wait"
	case "finished":
		return "archive"
	default:
		return ""
	}
}

// resolveAgent finds an agent by ID or by section within a project.
func (h *Handler) resolveAgent(idOrSection, project string) (*storage.Agent, error) {
	// Try by ID first
	agent, err := h.store.AgentGet(idOrSection)
	if err == nil && agent != nil {
		return agent, nil
	}

	// Try by section (needs project)
	if project != "" {
		agent, err = h.store.AgentGetBySection(project, idOrSection)
		if err == nil && agent != nil {
			return agent, nil
		}
	}

	// Fall back to a session identifier (claude session, opencode ses_*, codex
	// session — plain or opencode:/codex:-prefixed). Often the only handle left
	// once an agent stopped.
	if agent, err := h.store.AgentGetAnyBySession(idOrSection); err == nil && agent != nil {
		return agent, nil
	}

	return nil, fmt.Errorf("no agent found matching %q (project=%q)", idOrSection, project)
}

// generateAgentUUID returns a random UUID v4 string.
func generateAgentUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// claudeAgentAllowedTools is the grant every spawned claude agent runs with. It
// reaches the backend as --allowedTools instead of a project-local
// .claude/settings.local.json: that file lands in the user's real repository,
// outlives the run and widens their own interactive sessions there, and through a
// symlinked .claude it was refused outright, leaving the run without any allowlist
// until its deadline (YM-295, YM-300).
const claudeAgentAllowedTools = "mcp__yesmem__*,Read(*),Write(*),Edit(*),Glob(*),Grep(*),Bash(*),Agent(*),WebSearch(*),WebFetch(*)"

// allowedAgentBackends is the set spawnAgentProcess can start. The backend value
// comes from the caller, and spawnAgentProcess maps anything it does not know to
// claude — so without this check a row could claim a backend its process never
// runs, and an operator reading the row would draw the wrong conclusion (YM-267).
// The three names are the documented contract of the spawn_agent tool.
var allowedAgentBackends = map[string]bool{
	"claude":   true,
	"opencode": true,
	"codex":    true,
}

// resolveAgentBinary returns the binary for a backend. Only the two backends that
// have their own binary reach the PATH lookup; every other value is spawned as
// claude by spawnAgentProcess, so the lookup never sees a caller-supplied name.
func resolveAgentBinary(backend string) string {
	if path, err := exec.LookPath(backend); err == nil {
		return path
	}
	homeDir, _ := os.UserHomeDir()
	switch backend {
	case "opencode":
		return filepath.Join(homeDir, ".opencode", "bin", "opencode")
	case "codex":
		if path, err := exec.LookPath("node"); err == nil {
			if dir := filepath.Dir(path); strings.Contains(dir, ".nvm") {
				return filepath.Join(dir, "codex")
			}
		}
		// Fallback: scan NVM versions directly (daemon may not have node in PATH)
		nvmBase := filepath.Join(homeDir, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(nvmBase); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				candidate := filepath.Join(nvmBase, e.Name(), "bin", "codex")
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
		return "codex"
	default:
		return backend
	}
}

func (h *Handler) resolveAgentWorkDir(project, workDir string) string {
	if workDir == "" && project != "" {
		resolved, err := h.store.ResolveProjectPathStrict(project)
		if err != nil {
			// An ambiguous short name: picking one of the candidates, or inventing
			// a third directory below $HOME/projects, is how a second checkout came
			// into existence (YM-286). Refuse instead of guessing.
			log.Printf("[agents] project %q is ambiguous, no working directory: %v", project, err)
			return ""
		}
		workDir = resolved
	}
	if homeDir, _ := os.UserHomeDir(); workDir == homeDir || workDir == "" {
		if !filepath.IsAbs(project) {
			// A short name that resolved to nothing: there is no directory to use.
			// "$HOME/projects/<name>" would be a third location beside the real
			// checkouts, invented by the daemon and read by nobody (YM-286). The
			// caller decides what to do; nothing is created here.
			return ""
		}
		// The project IS its path (project ids are absolute since v0.65). Joining
		// it onto $HOME/projects built a nested path that belongs to no repository
		// and created it (YM-286).
		workDir = filepath.Clean(project)
	}
	// The session bind compares the directory opencode reports byte for byte
	// against this value, so a trailing slash or a redundant "." would hide the
	// agent's own session from it. Cleaning here covers every caller.
	workDir = filepath.Clean(workDir)
	return workDir
}
