package daemon

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/orchestrator"
	"github.com/carsteneu/yesmem/internal/storage"
)

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
	tokenBudget := 0
	if tb, ok := params["token_budget"].(float64); ok && tb > 0 {
		tokenBudget = int(tb)
	}
	maxTurns := 0
	if mt, ok := params["max_turns"].(float64); ok && mt > 0 {
		maxTurns = int(mt)
	}
	model, _ := params["model"].(string)
	workDir, _ := params["work_dir"].(string)

	// Auto-resolve caller session from MCP context if not explicitly set
	if callerSession == "" {
		callerSession = h.resolveSessionID(params, "caller_session")
	}

	if project == "" {
		return errorResponse("project required")
	}
	if section == "" {
		return errorResponse("section required")
	}
	if existing, err := h.store.AgentGetActiveBySection(project, section); err == nil && existing != nil {
		return errorResponse(fmt.Sprintf("section %q already has active agent %s (status=%s)", section, existing.ID, existing.Status))
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

	// Default token_budget from config if not explicitly set
	if tokenBudget == 0 && h.agentTokenBudget > 0 {
		tokenBudget = h.agentTokenBudget
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
		Backend:       backend,
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
				"Read scratchpad_read(project=\"%s\", section=\"%s\") for context, then act on it. "+
				"Write your results with scratchpad_write(project=\"%s\", section=\"%s\", content=...).",
			project, section, project, section, project, section,
		)
	default:
		prompt = fmt.Sprintf(
			"You are working on project '%s', section '%s'. "+
				"FIRST ACTION: Write scratchpad_write(project=\"%s\", section=\"%s\", content=\"Status: started\") immediately so the main agent sees you are working. "+
				"Then read scratchpad_read(project=\"%s\", section=\"%s\") for context and work through the task. "+
				"Write your results with scratchpad_write(project=\"%s\", section=\"%s\", content=...).",
			project, section, project, section, project, section, project, section,
		)
	}
	if callerSession != "" {
		prompt += fmt.Sprintf(
			" When you are DONE, send send_to(target=\"%s\", content=\"DONE: Section '%s' in project '%s' is complete.\") to notify the main agent.",
			callerSession, section, project,
		)
	}
	if tokenBudget > 0 {
		prompt += fmt.Sprintf(" BUDGET: Max %d tokens for this task — work efficiently, keep it concise.", tokenBudget)
	}

	// Start PTY bridge + terminal in background goroutine
	sockPath := filepath.Join(h.dataDir, fmt.Sprintf("%s.sock", id))
	workDir = h.resolveAgentWorkDir(project, workDir, backend)
	go h.spawnAgentProcess(id, sessionID, project, section, prompt, sockPath, workDir, backend, model, maxTurns, false)

	return jsonResponse(map[string]any{
		"id":         id,
		"session_id": sessionID,
		"project":    project,
		"section":    section,
		"backend":    backend,
		"status":     "spawning",
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
// workDir and returns its session ID. Returns "" on timeout.
func pollCodexSessionID(workDir string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sid, err := findCodexSessionID(workDir)
		if err == nil && sid != "" {
			return sid
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
}

// pollOpencodeSessionID queries the opencode database via sqlite3 CLI for
// the most recent session matching workDir and returns its session ID.
// Returns "" on timeout or error.
func pollOpencodeSessionID(workDir string, timeout time.Duration) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// sqlite3 CLI does not support ? placeholders; escape the workDir manually.
		escaped := strings.ReplaceAll(workDir, "'", "''")
		query := fmt.Sprintf("SELECT id FROM session WHERE directory = '%s' ORDER BY time_created DESC LIMIT 1", escaped)
		out, err := exec.Command("sqlite3", dbPath, query).Output()
		if err == nil && len(out) > 0 {
			sid := strings.TrimSpace(string(out))
			if sid != "" {
				return sid
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
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

// spawnAgentProcess creates a PTY bridge, opens a terminal, and waits for the agent to finish.
func (h *Handler) spawnAgentProcess(id, sessionID, project, section, prompt, sockPath, workDir, backend, model string, maxTurns int, resume bool) {
	var agentBin string
	var agentArgs []string

	switch backend {
	case "opencode":
		agentBin = resolveAgentBinary(backend)
		if resume {
			// Prefer stored per-agent opencode session ID.
			sid := sessionID
			if agent, _ := h.store.AgentGet(id); agent != nil && agent.OpencodeSessionID != "" {
				sid = agent.OpencodeSessionID
			}
			agentArgs = []string{"-s", sid}
		} else {
			agentArgs = []string{}
		}
		if model != "" {
			if strings.Contains(model, "/") {
				agentArgs = append(agentArgs, "--model", model)
			} else {
				agentArgs = append(agentArgs, "--model", fmt.Sprintf("deepseek/%s", model))
			}
		}
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
		agentBin = "claude"
		if resume {
			agentArgs = []string{"--resume", sessionID}
		} else {
			agentArgs = []string{
				"--session-id", sessionID,
				"--name", fmt.Sprintf("%s-%s", project, section),
				"--allowedTools", "mcp__yesmem__*,Read(*),Write(*),Edit(*),Glob(*),Grep(*),Bash(*),Agent(*),WebSearch(*),WebFetch(*)",
			}
			if model != "" {
				agentArgs = append(agentArgs, "--model", model)
			}
			if maxTurns > 0 {
				agentArgs = append(agentArgs, "--max-turns", fmt.Sprintf("%d", maxTurns))
			}
		}
	}

	// Load codex auth for env injection (codex CLI needs OPENAI_API_KEY in environment)
	var extraEnv []string
	if backend == "codex" {
		if key, err := loadCodexAuthEnv(); err == nil && key != "" {
			extraEnv = append(extraEnv, key)
		}
	}

	bridge, err := orchestrator.NewAgentBridge(agentBin, agentArgs, sockPath, workDir, extraEnv...)
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
	title := fmt.Sprintf("yesmem-%s #%s", section, strings.TrimPrefix(id, "agent-"))
	bin, spawnArgs := orchestrator.BuildSpawnCommand(terminal, binPath, title, "agent-tty", "--sock", sockPath)
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

	h.store.AgentUpdate(id, map[string]any{
		"pid":       bridge.Cmd.Process.Pid,
		"sock_path": sockPath,
		"status":    "running",
	})

	// Inject initial prompt via PTY — all backends, initial spawn only
	if !resume {
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
				}
			}
			// Capture opencode session ID for per-agent tracking.
			// OpenCode creates its session in opencode.db within ~2s of TUI start.
			if backend == "opencode" {
				ocID := pollOpencodeSessionID(workDir, 30*time.Second)
				if ocID != "" {
					h.store.AgentUpdate(id, map[string]any{
						"opencode_session_id": ocID,
					})
				}
			}
		}()
	}

	// Wait for agent to finish
	waitErr := bridge.Cmd.Wait()
	bridge.Close()

	// Clean up socket files
	os.Remove(sockPath)
	os.Remove(sockPath + ".inject")

	agent, err := h.store.AgentGet(id)
	if err != nil || agent == nil {
		return
	}

	fields := map[string]any{
		"pid":       0,
		"sock_path": "",
	}
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
	h.store.AgentUpdate(id, fields)
}

// handleRegisterAgent updates a pending agent with PID and socket path (called by CLI after bridge creation).
func (h *Handler) handleRegisterAgent(params map[string]any) Response {
	id, _ := params["id"].(string)
	if id == "" {
		return errorResponse("id required")
	}

	pid, _ := params["pid"].(float64) // JSON numbers are float64
	sockPath, _ := params["sock_path"].(string)

	if pid == 0 {
		return errorResponse("pid required")
	}
	if sockPath == "" {
		return errorResponse("sock_path required")
	}

	err := h.store.AgentUpdate(id, map[string]any{
		"pid":       int(pid),
		"sock_path": sockPath,
		"status":    "running",
	})
	if err != nil {
		return errorResponse(fmt.Sprintf("register agent: %v", err))
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

	if err := h.store.AgentUpdate(id, fieldsRaw); err != nil {
		return errorResponse(fmt.Sprintf("update agent: %v", err))
	}

	// Clean up socket files when agent finishes or errors
	if status, _ := fieldsRaw["status"].(string); status == "finished" || status == "error" {
		agent, err := h.store.AgentGet(id)
		if err == nil && agent.SockPath != "" {
			os.Remove(agent.SockPath)
			os.Remove(agent.SockPath + ".inject")
		}
	}

	return jsonResponse(map[string]any{"status": "ok", "id": id})
}

// handleRelayAgent injects content into a running agent's PTY via its inject socket.
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

	if agent.Status != "running" {
		return errorResponse(fmt.Sprintf("agent %s is %s, not running", agent.ID, agent.Status))
	}
	if agent.SockPath == "" {
		return errorResponse(fmt.Sprintf("agent %s has no socket path (not registered yet?)", agent.ID))
	}

	// Wrap content with RELAY prefix so agent can identify the source
	caller, _ := params["caller_session"].(string)
	if caller == "" {
		caller = "orchestrator"
	}
	// Newlines in content would be interpreted as Enter keypresses in the PTY,
	// splitting the message into fragments. Escape them.
	sanitized := strings.ReplaceAll(content, "\n", "\\n")
	sanitized = strings.ReplaceAll(sanitized, "\r", "")
	wrappedContent := fmt.Sprintf("[RELAY from=%s] %s", caller, sanitized)

	injectPath := agent.SockPath + ".inject"

	// Two separate connections to avoid bracketed-paste swallowing the \r.
	// First connection: write content only (no trailing \r).
	// Then 3s later, second connection: write only \r to submit.
	conn1, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		return errorResponse(fmt.Sprintf("connect to inject socket: %v (agent may have crashed)", err))
	}
	if _, err := conn1.Write([]byte(wrappedContent)); err != nil {
		conn1.Close()
		return errorResponse(fmt.Sprintf("write to inject socket: %v", err))
	}
	conn1.Close()

	time.Sleep(3 * time.Second)

	conn2, err := net.DialTimeout("unix", injectPath, 3*time.Second)
	if err != nil {
		return errorResponse(fmt.Sprintf("connect to inject socket for \\r: %v", err))
	}
	conn2.Write([]byte("\r\n"))
	conn2.Close()

	return jsonResponse(map[string]any{
		"status":   "injected",
		"agent_id": agent.ID,
		"section":  agent.Section,
	})
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

	if agent.Status != "running" && agent.Status != "frozen" && agent.Status != "spawning" {
		return errorResponse(fmt.Sprintf("agent %s is %s, not stoppable", agent.ID, agent.Status))
	}

	// Try graceful exit via inject socket
	stopped := false
	if agent.SockPath != "" && agent.Backend != "codex" {
		injectPath := agent.SockPath + ".inject"
		conn, err := net.DialTimeout("unix", injectPath, 3*time.Second)
		if err == nil {
			exitCmd := "/exit\r"
			if agent.Backend == "opencode" {
				exitCmd = "\x03" // Ctrl+C for opencode (no /exit command)
			}
			_, writeErr := conn.Write([]byte(exitCmd))
			conn.Close()
			stopped = writeErr == nil
		}
	}

	// Codex: inject socket Ctrl+C doesn't work in raw-mode PTY;
	// SIGTERM triggers clean exit + session save (verified E2E).
	if agent.Backend == "codex" && agent.PID > 0 {
		syscall.Kill(agent.PID, syscall.SIGTERM)
		stopped = true
	}

	// Fallback: SIGTERM for other backends
	if !stopped && agent.PID > 0 {
		syscall.Kill(agent.PID, syscall.SIGTERM)
	}

	now := time.Now().Format(time.RFC3339)
	if err := h.store.AgentUpdate(agent.ID, map[string]any{
		"status":     "stopped",
		"pid":        0,
		"sock_path":  "",
		"stopped_at": now,
		"progress":   "stopped",
	}); err != nil {
		return errorResponse(fmt.Sprintf("stop agent: %v", err))
	}

	// Cascade: stop all child agents in the supervision tree
	if n, err := h.store.AgentCascadeStop(agent.SessionID); err != nil {
		log.Printf("[stop_agent] cascade stop failed for %s: %v", agent.ID, err)
	} else if n > 0 {
		log.Printf("[stop_agent] cascade stopped %d child agent(s) of %s", n, agent.ID)
	}

	// Clean up socket files
	if agent.SockPath != "" {
		os.Remove(agent.SockPath)
		os.Remove(agent.SockPath + ".inject")
	}

	return jsonResponse(map[string]any{
		"status":   "stopped",
		"agent_id": agent.ID,
		"section":  agent.Section,
	})
}

// handleStopAllAgents stops all running agents in a project.
func (h *Handler) handleStopAllAgents(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}

	agents, err := h.store.AgentList(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("list agents: %v", err))
	}

	stopped := 0
	for _, a := range agents {
		if a.Status != "running" && a.Status != "frozen" && a.Status != "spawning" {
			continue
		}
		// Try graceful exit
		if a.SockPath != "" {
			injectPath := a.SockPath + ".inject"
			if conn, err := net.DialTimeout("unix", injectPath, 2*time.Second); err == nil {
				exitCmd := "/exit\r"
				if a.Backend == "opencode" {
					exitCmd = "\x03" // Ctrl+C for opencode
				}
				conn.Write([]byte(exitCmd))
				conn.Close()
			}
		}
		// Fallback: SIGTERM
		if a.PID > 0 {
			syscall.Kill(a.PID, syscall.SIGTERM)
		}
		h.store.AgentUpdate(a.ID, map[string]any{
			"status":     "stopped",
			"pid":        0,
			"sock_path":  "",
			"stopped_at": time.Now().Format(time.RFC3339),
			"progress":   "stopped",
		})
		if a.SockPath != "" {
			os.Remove(a.SockPath)
			os.Remove(a.SockPath + ".inject")
		}
		stopped++
	}

	return jsonResponse(map[string]any{
		"project": project,
		"stopped": stopped,
	})
}

// handleResumeAgent restarts a stopped/frozen agent using its existing session.
func (h *Handler) handleResumeAgent(params map[string]any) Response {
	to, _ := params["to"].(string)
	project, _ := params["project"].(string)

	if to == "" {
		return errorResponse("to required")
	}

	agent, err := h.resolveAgent(to, project)
	if err != nil {
		return errorResponse(err.Error())
	}

	if agent.Status != "stopped" && agent.Status != "frozen" {
		return errorResponse(fmt.Sprintf("agent %s is %s, not resumable", agent.ID, agent.Status))
	}
	if agent.Backend == "" {
		agent.Backend = "claude"
	}
	if agent.SessionID == "" {
		return errorResponse(fmt.Sprintf("agent %s has no session_id to resume", agent.ID))
	}
	if active, err := h.store.AgentGetActiveBySection(agent.Project, agent.Section); err == nil && active != nil && active.ID != agent.ID {
		return errorResponse(fmt.Sprintf("section %q already has active agent %s (status=%s)", agent.Section, active.ID, active.Status))
	}

	sockPath := filepath.Join(h.dataDir, fmt.Sprintf("%s.sock", agent.ID))
	workDir := h.resolveAgentWorkDir(agent.Project, "", agent.Backend)
	if err := h.store.AgentUpdate(agent.ID, map[string]any{
		"status":      "pending",
		"pid":         0,
		"sock_path":   "",
		"relay_count": 0,
		"progress":    "resuming",
		"error":       "",
		"stopped_at":  "",
	}); err != nil {
		return errorResponse(fmt.Sprintf("resume agent: %v", err))
	}
	go h.spawnAgentProcess(agent.ID, agent.SessionID, agent.Project, agent.Section, "", sockPath, workDir, agent.Backend, "", 0, true)

	return jsonResponse(map[string]any{
		"status":   "resuming",
		"agent_id": agent.ID,
		"section":  agent.Section,
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
	// Try three match strategies in order:
	//  1. Direct session_id match (daemon-generated UUID == proxy's threadID)
	//  2. proxy_thread_id match (lazy-mapped from a prior _track_usage call)
	//  3. Lazy init: find a running agent in the same project with no proxy_thread_id yet
	if threadID != "" {
		if agent, err := h.store.AgentGetAnyBySession(threadID); err == nil && agent != nil {
			h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
			RegisterSessionThread(agent.SessionID, threadID)
		} else if agent, err := h.store.AgentGetByProxyThreadID(threadID); err == nil && agent != nil {
			h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
		} else if project, ok := params["project"].(string); ok && project != "" {
			if agent, err := h.store.AgentGetRunningInProject(project); err == nil && agent != nil {
				h.store.AgentUpdate(agent.ID, map[string]any{"proxy_thread_id": threadID})
				h.store.AgentUpdateTelemetry(agent.ID, 1, inputTokens, outputTokens)
			}
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

	return jsonResponse(map[string]any{
		"agents": agents,
		"count":  len(agents),
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

	// Enrich with stream tracking fields
	result := agentToMap(agent)
	result = h.enrichAgentWithStreamFields(result)

	return jsonResponse(result)
}

// agentToMap converts an Agent struct to a map[string]any for enrichment.
func agentToMap(a *storage.Agent) map[string]any {
	return map[string]any{
		"id":               a.ID,
		"project":          a.Project,
		"section":          a.Section,
		"session_id":       a.SessionID,
		"pid":              a.PID,
		"sock_path":        a.SockPath,
		"status":           a.Status,
		"caller_session":   a.CallerSession,
		"error":            a.Error,
		"heartbeat_at":     a.HeartbeatAt,
		"progress":         a.Progress,
		"relay_count":      a.RelayCount,
		"depth":            a.Depth,
		"token_budget":     a.TokenBudget,
		"retry_count":      a.RetryCount,
		"backend":          a.Backend,
		"turns_used":       a.TurnsUsed,
		"input_tokens":     a.InputTokens,
		"output_tokens":    a.OutputTokens,
		"last_activity_at": a.LastActivityAt,
		"phase":            a.Phase,
		"created_at":       a.CreatedAt,
		"stopped_at":       a.StoppedAt,
		"restart_strategy": a.RestartStrategy,
		"restart_count":    a.RestartCount,
		"max_restarts":     a.MaxRestarts,
		"liveness_ping_at": a.LivenessPingAt,
		"last_restart_at":  a.LastRestartAt,
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

// ensureAgentPermissions creates a minimal .claude/settings.local.json so Claude Code
// doesn't prompt for built-in tool permissions. MCP tool approval is handled by injecting
// "1\r" before the actual prompt.
func ensureAgentPermissions(workDir string) {
	claudeDir := filepath.Join(workDir, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.local.json")
	if _, err := os.Stat(settingsPath); err == nil {
		return
	}
	os.MkdirAll(claudeDir, 0755)
	settings := `{"permissions":{"allow":["Bash(*)","Read(*)","Write(*)","Edit(*)","Glob(*)","Grep(*)","WebSearch(*)","WebFetch(*)","Agent(*)"]},"disabledMcpjsonServers":[]}`
	os.WriteFile(settingsPath, []byte(settings), 0644)
}

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

func (h *Handler) resolveAgentWorkDir(project, workDir, backend string) string {
	if workDir == "" {
		workDir = h.store.ResolveProjectPath(project)
	}
	if homeDir, _ := os.UserHomeDir(); workDir == homeDir || workDir == "" {
		workDir = filepath.Join(homeDir, "projects", project)
		os.MkdirAll(workDir, 0755)
	}
	if backend == "claude" {
		ensureAgentPermissions(workDir)
	}
	return workDir
}
