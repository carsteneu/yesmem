package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/carsteneu/yesmem/internal/buildinfo"
	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Server wraps the MCP server that proxies all calls to the daemon.
type Server struct {
	srv     *mcpserver.MCPServer
	proxy   *ProxyClient
	dataDir string
	mu      sync.Mutex
}

// contentLangDirective is appended to descriptions of content-writing tools.
// Keeps generated content English-consistent regardless of conversation language.
const contentLangDirective = " LANGUAGE: write content in English, regardless of conversation language. Technical tokens (JSON fields, code, commands, identifiers) remain verbatim — never translate them."

// Description constants for common filter parameters. Using DRY constants
// ensures consistent guidance across all MCP tools.
const (
	projectFilterDesc   = "Full working directory path (e.g., /home/user/projects/my-app). Always set this to your current working directory — short names like 'my-app' may be ambiguous across multiple projects and trigger a cwd-based tiebreaker in the daemon. Leave empty only for cross-project searches."
	projectRequiredDesc = "Full working directory path (e.g., /home/user/projects/my-app). Always set this to your current working directory — short names like 'my-app' are ambiguous across projects."
	sinceDesc           = "ISO 8601 date-time lower bound (inclusive), e.g. '2026-07-01' or '2026-07-01T00:00:00Z'. Items at or after this timestamp."
	beforeDesc          = "ISO 8601 date-time upper bound (exclusive), e.g. '2026-07-07'. Items strictly before this timestamp."
	limitDesc           = "Maximum number of results to return. Default depends on tool (typically 10-100)."
)

// New creates an MCP server. Connection to daemon is established lazily
// on first tool call, so the MCP server always starts successfully.
func New(dataDir string) (*Server, error) {
	s := &Server{dataDir: dataDir}
	initErrorLog(dataDir)

	s.srv = mcpserver.NewMCPServer(
		"yesmem",
		buildinfo.Version,
		mcpserver.WithToolCapabilities(false),
	)

	// Try connecting eagerly but don't fail if daemon isn't ready
	proxy, err := NewProxy(dataDir)
	if err != nil {
		log.Printf("Daemon not ready yet — will retry on first tool call")
		logMCPError("startup_connect_failed", "", err)
	} else {
		s.proxy = proxy
	}

	s.registerTools()
	return s, nil
}

// getProxy returns the proxy, connecting lazily if needed.
func (s *Server) getProxy() (*ProxyClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.proxy != nil {
		return s.proxy, nil
	}

	proxy, err := NewProxy(s.dataDir)
	if err != nil {
		return nil, fmt.Errorf("daemon unavailable: %w", err)
	}
	s.proxy = proxy
	return s.proxy, nil
}

// serveStdio is indirected so tests can run ServeStdio without a real stdio
// server on the other end.
var serveStdio = mcpserver.ServeStdio

// ServeStdio starts the MCP server on stdin/stdout. The stdio mode is the only
// way this server runs, so the parent watch belongs here. originPpid is the
// client's pid as captured by the caller before anything that can block — see
// startParentWatch for why it may not be read at this point.
func (s *Server) ServeStdio(originPpid int) error {
	defer func() {
		if s.proxy != nil {
			s.proxy.Close()
		}
	}()
	startParentWatch(originPpid)
	return serveStdio(s.srv)
}

// sessionIDSourceEnv marks an _session_id that the MCP layer read from its own
// environment. The daemon's resolveSessionID may only trust such an id for
// opencode when the caller PID is a daemon-spawned agent's: under opencode v2
// the `yesmem mcp` children belong to the shared service process, so their
// environment carries the id of whichever session was created last.
const sessionIDSourceEnv = "env"

func buildProxyParams(arguments map[string]any, withThreadID bool) map[string]any {
	params := make(map[string]any, len(arguments)+7)
	for key, value := range arguments {
		// The underscore namespace is NOT filtered here, deliberately: the opencode
		// plugin injects the authoritative per-call _session_id into the tool
		// arguments (tool.execute.before), and that channel is indistinguishable from
		// a model-written argument at this point. Filtering it would take the
		// attribution away from the agents that legitimately carry it (YM-271 cold
		// review; the label's residual same-UID exposure is documented at
		// daemon.broadcastSender instead).
		params[key] = value
	}

	if _, exists := params["_caller_pid"]; !exists {
		params["_caller_pid"] = float64(os.Getppid())
	}

	sessionID, sourceAgent := resolveClientSessionID()
	if _, exists := params["_session_id"]; !exists && sessionID != "" {
		params["_session_id"] = sessionID
		params["_session_id_source"] = sessionIDSourceEnv
	}
	if _, exists := params["_source_agent"]; !exists && sourceAgent != "" && sourceAgent != "claude" {
		params["_source_agent"] = sourceAgent
	}
	if value, ok := params["_source_agent"].(string); ok && value != "" {
		sourceAgent = value
	}
	if _, exists := params["_client_model"]; !exists {
		if model := currentClientModel(); model != "" {
			params["_client_model"] = model
		}
	}
	if _, exists := params["_cwd"]; !exists {
		if cwd := callerCWD(sourceAgent); cwd != "" {
			params["_cwd"] = cwd
		}
	}
	if withThreadID {
		if _, exists := params["thread_id"]; !exists {
			if sessionID, ok := params["_session_id"].(string); ok && sessionID != "" {
				params["thread_id"] = sessionID
			}
		}
	}
	return params
}

func (s *Server) proxyCall(method string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		proxy, err := s.getProxy()
		if err != nil {
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		params := buildProxyParams(req.Params.Arguments.(map[string]any), false)

		result, err := proxy.Call(method, params)
		if err != nil {
			// Only reset proxy on connection errors — daemon-level errors mean
			// the daemon is alive and responding, just the query had an issue.
			if !isDaemonError(err) {
				logMCPError("call_failed_after_retry", method, err)
				s.mu.Lock()
				s.proxy = nil
				s.mu.Unlock()
			}
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		return mcplib.NewToolResultText(string(result)), nil
	}
}

// currentClientModel resolves the calling agent's model from environment.
// Priority: YESMEM_MODEL_ID (explicit yesmem-native declaration, wins over
// vendor vars — gives direct-MCP clients like opencode a clean injection point),
// then vendor-specific CODEX_MODEL / OPENAI_MODEL / ANTHROPIC_MODEL / CLAUDE_MODEL,
// then generic MODEL as last resort. Empty string if none set.
func currentClientModel() string {
	for _, key := range []string{
		"YESMEM_MODEL_ID",
		"CODEX_MODEL",
		"OPENAI_MODEL",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
		"MODEL",
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// resolveClientSessionID detects the calling agent from environment and returns
// a normalized session ID with the agent prefix. Also returns the source agent label.
//
// Detection order (first match wins):
//  1. YESMEM_SOURCE_AGENT=opencode + YESMEM_SESSION_ID  → opencode:<id>, "opencode"
//  2. YESMEM_SOURCE_AGENT=codex + CODEX_THREAD_ID       → codex:<id>,    "codex"
//  3. CODEX_THREAD_ID (legacy, no source_agent marker)  → codex:<id>,    "codex"
//  4. CLAUDE_SESSION_ID                                 → raw id,        "claude"  (unchanged)
//
// If YESMEM_SOURCE_AGENT is set but the corresponding session ID is missing,
// the function returns empty sessionID with the detected source agent.
//
// MCP tool descriptions are NOT modified — they remain Claude-specific for all clients.
func resolveClientSessionID() (sessionID, sourceAgent string) {
	sa := strings.TrimSpace(os.Getenv("YESMEM_SOURCE_AGENT"))
	switch sa {
	case "opencode":
		if sid := strings.TrimSpace(os.Getenv("YESMEM_SESSION_ID")); sid != "" {
			return "opencode:" + sid, "opencode"
		}
		return "", "opencode"
	case "codex":
		if tid := strings.TrimSpace(os.Getenv("CODEX_THREAD_ID")); tid != "" {
			return "codex:" + tid, "codex"
		}
		return "", "codex"
	}

	// Fallback: codex might use CODEX_THREAD_ID even without YESMEM_SOURCE_AGENT
	if tid := strings.TrimSpace(os.Getenv("CODEX_THREAD_ID")); tid != "" {
		return "codex:" + tid, "codex"
	}
	// Default: Claude path (unchanged — no prefix, no namespace)
	if sid := strings.TrimSpace(os.Getenv("CLAUDE_SESSION_ID")); sid != "" {
		return sid, "claude"
	}
	// Claude Code 2.1.132+ exports CLAUDE_CODE_SESSION_ID instead of CLAUDE_SESSION_ID.
	if sid := strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID")); sid != "" {
		return sid, "claude"
	}

	// Auto-detect: OPENCODE=1 is set by OpenCode on all worker processes.
	// Check AFTER explicit Claude vars so they take precedence on shared hosts.
	if strings.TrimSpace(os.Getenv("OPENCODE")) == "1" {
		return "", "opencode"
	}

	return "", "claude"
}

// formatSearchResult converts verbose JSON search results into a compact,
// token-efficient format for LLM consumption.
func formatSearchResult(raw json.RawMessage) string {
	var data struct {
		Message string `json:"message"`
		Hint    string `json:"hint"`
		Results []struct {
			ID        string  `json:"id"`
			Content   string  `json:"content"`
			Project   string  `json:"project"`
			Score     float64 `json:"score"`
			Source    string  `json:"source"`
			Snippet   string  `json:"snippet"`
			CreatedAt string  `json:"created_at"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw) // fallback to raw JSON
	}
	if len(data.Results) == 0 {
		msg := data.Message + "\n(no results)"
		if data.Hint != "" {
			msg = data.Hint + "\n\n" + msg
		}
		return msg
	}

	var sb strings.Builder
	if data.Hint != "" {
		sb.WriteString(data.Hint)
		sb.WriteByte('\n')
		sb.WriteByte('\n')
	}
	sb.WriteString(data.Message)
	sb.WriteByte('\n')
	for _, r := range data.Results {
		content := r.Content
		if content == "" {
			content = r.Snippet
		}
		proj := r.Project
		if proj == "" {
			proj = "—"
		}
		source := r.Source
		if source == "" {
			source = relativeTime(r.CreatedAt)
		}
		sb.WriteString(fmt.Sprintf("[#%s %s %.3f %s] %s\n", r.ID, proj, r.Score, source, content))
	}
	return sb.String()
}

// formatSimpleMessage extracts the "message" field from JSON and returns it as plain text.
func formatSimpleMessage(raw json.RawMessage) string {
	var data struct {
		Message string `json:"message"`
		ID      int64  `json:"id"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		return data.Message
	}
	return string(raw)
}

// formatRemember renders a structured box for remember() responses.
func formatRemember(raw json.RawMessage) string {
	var data struct {
		ID               int64   `json:"id"`
		Category         string  `json:"category"`
		Project          string  `json:"project"`
		Content          string  `json:"content"`
		ModelUsed        string  `json:"model_used"`
		SupersedesID     int64   `json:"supersedes_id"`
		SupersedeOutcome string  `json:"supersede_outcome"`
		SupersedeTrust   float64 `json:"supersede_trust"`
		SupersedeError   string  `json:"supersede_error"`
		Message          string  `json:"message"`
		Attribution      string  `json:"attribution"`
		Deduplicated     bool    `json:"deduplicated"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Deduplicated {
		// Soft-dedup response carries only id + message — render the real
		// reason instead of a misleading "saved" box (match #85112).
		if data.Message != "" {
			return data.Message
		}
		return fmt.Sprintf("Already known: learning #%d already covers this (%s).", data.ID, data.Category)
	}
	if data.ID == 0 {
		// Error or unexpected format — fallback
		if data.Message != "" {
			return data.Message
		}
		return string(raw)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✓ Learning #%d saved\n", data.ID))
	sb.WriteString(fmt.Sprintf("  Category:   %s\n", data.Category))
	if data.Project != "" {
		sb.WriteString(fmt.Sprintf("  Project:    %s\n", data.Project))
	}
	if data.ModelUsed != "" {
		sb.WriteString(fmt.Sprintf("  Model:      %s\n", data.ModelUsed))
	}
	if data.SupersedesID > 0 {
		switch data.SupersedeOutcome {
		case "blocked":
			// A refused supersede is not a replacement. Saying so plainly is the
			// whole point: the caller would otherwise keep trusting stale knowledge.
			sb.WriteString(fmt.Sprintf("  Supersedes:  #%d BLOCKED (trust %.1f): the old learning stays active. Retire it with force_supersede plus supersede_reason, and only after the user or orchestrator confirmed the old learning is wrong.\n", data.SupersedesID, data.SupersedeTrust))
		case "failed":
			// The write did not happen. The store's reason must reach the caller,
			// otherwise it looks like a refused-by-trust case and invites the
			// very retry the caller already performed.
			reason := data.SupersedeError
			if reason == "" {
				reason = "the store rejected the write"
			}
			sb.WriteString(fmt.Sprintf("  Supersedes:  #%d FAILED: nothing was replaced (%s).\n", data.SupersedesID, reason))
		case "not_found":
			reason := data.SupersedeError
			if reason == "" {
				reason = "the learning could not be loaded"
			}
			sb.WriteString(fmt.Sprintf("  Supersedes:  #%d not found: nothing was replaced (%s).\n", data.SupersedesID, reason))
		case "duplicate":
			sb.WriteString(fmt.Sprintf("  Supersedes:  #%d (duplicate: not performed)\n", data.SupersedesID))
		case "replaced", "forced":
			sb.WriteString(fmt.Sprintf("  Supersedes:  #%d (%s)\n", data.SupersedesID, data.SupersedeOutcome))
		default:
			// Older daemon without the outcome field: keep the historical line
			// rather than inventing a verdict.
			sb.WriteString(fmt.Sprintf("  Supersedes: #%d\n", data.SupersedesID))
		}
	}
	if data.Attribution != "" {
		sb.WriteString(fmt.Sprintf("  Source:     %s\n", data.Attribution))
	}
	sb.WriteString(fmt.Sprintf("  Content:    %s", data.Content))
	return sb.String()
}

// formatPin renders a structured box for pin() responses.
func formatPin(raw json.RawMessage) string {
	var data struct {
		ID      int64  `json:"id"`
		Scope   string `json:"scope"`
		Project string `json:"project"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.ID == 0 {
		var msg struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &msg) == nil && msg.Message != "" {
			return msg.Message
		}
		return string(raw)
	}
	scope := data.Scope
	if scope == "" {
		scope = "session"
	}
	return fmt.Sprintf("Pin #%d (%s) saved: %s", data.ID, scope, data.Content)
}

// formatResolve renders a structured box for resolve/resolve_by_text responses.
// When nothing was resolved, it prints the reason and the candidates so the
// caller can pick one by id instead of guessing.
func formatResolve(raw json.RawMessage) string {
	var data struct {
		ID         int64  `json:"id"`
		Category   string `json:"category"`
		Project    string `json:"project"`
		Content    string `json:"content"`
		Reason     string `json:"reason"`
		Message    string `json:"message"`
		Candidates []struct {
			ID      int64  `json:"id"`
			Content string `json:"content"`
			Project string `json:"project"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.ID == 0 {
		if len(data.Candidates) == 0 {
			if data.Message != "" {
				return data.Message
			}
			return string(raw)
		}
		var sb strings.Builder
		if data.Message != "" {
			sb.WriteString(data.Message + "\n")
		}
		sb.WriteString("Candidates:\n")
		for _, c := range data.Candidates {
			sb.WriteString(fmt.Sprintf("  #%d  %s\n", c.ID, shortenForList(c.Content, 100)))
		}
		return strings.TrimRight(sb.String(), "\n")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✓ Learning #%d resolved\n", data.ID))
	if data.Category != "" {
		sb.WriteString(fmt.Sprintf("  Category:   %s\n", data.Category))
	}
	if data.Project != "" {
		sb.WriteString(fmt.Sprintf("  Project:    %s\n", data.Project))
	}
	if data.Reason != "" {
		sb.WriteString(fmt.Sprintf("  Reason:     %s\n", data.Reason))
	}
	if data.Content != "" {
		sb.WriteString(fmt.Sprintf("  Content:    %s", data.Content))
	}
	return sb.String()
}

// shortenForList collapses whitespace and cuts long content for one-line lists.
// The cut is rune-based so a multi-byte character at the limit stays intact.
func shortenForList(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "..."
}

// formatLearnings converts a JSON array of learnings into compact one-liners with relative time.
func formatLearnings(raw json.RawMessage) string {
	var items []struct {
		ID              int64    `json:"id"`
		Content         string   `json:"content"`
		Category        string   `json:"category"`
		Project         string   `json:"project"`
		Confidence      float64  `json:"confidence"`
		HitCount        int      `json:"hit_count"`
		UseCount        int      `json:"use_count"`
		InjectCount     int      `json:"inject_count"`
		Source          string   `json:"source"`
		CreatedAt       string   `json:"created_at"`
		Entities        []string `json:"entities,omitempty"`
		TriggerRule     string   `json:"trigger_rule,omitempty"`
		TaskType        string   `json:"task_type,omitempty"`
		SessionID       string   `json:"session_id,omitempty"`
		SupersedeReason string   `json:"supersede_reason,omitempty"`
		Attribution     string   `json:"attribution,omitempty"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		// Maybe it's wrapped in an object
		var wrapped struct {
			Learnings json.RawMessage `json:"learnings"`
			Message   string          `json:"message"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && wrapped.Learnings != nil {
			if json.Unmarshal(wrapped.Learnings, &items) != nil {
				return string(raw)
			}
		} else {
			return string(raw)
		}
	}
	if len(items) == 0 {
		return "(no learnings)"
	}

	var sb strings.Builder
	for _, l := range items {
		proj := l.Project
		if proj == "" {
			proj = "—"
		}
		age := relativeTime(l.CreatedAt)
		cat := l.Category
		if l.TaskType != "" {
			cat = l.Category + ":" + l.TaskType
		}
		sb.WriteString(fmt.Sprintf("[#%d %s %s %s u:%d/i:%d] %s\n", l.ID, proj, cat, age, l.UseCount, l.InjectCount, l.Content))
		if l.Attribution != "" {
			sb.WriteString(fmt.Sprintf("  source: %s\n", l.Attribution))
		}
		if l.SessionID != "" {
			sb.WriteString(fmt.Sprintf("  session: %s\n", l.SessionID))
		}
		if l.SupersedeReason != "" {
			sb.WriteString(fmt.Sprintf("  superseded: %s\n", l.SupersedeReason))
		}
		// V2 metadata lines (only shown if present)
		if len(l.Entities) > 0 {
			sb.WriteString(fmt.Sprintf("  entities: %s\n", strings.Join(l.Entities, ", ")))
		}
		if l.TriggerRule != "" {
			sb.WriteString(fmt.Sprintf("  trigger: %s\n", l.TriggerRule))
		}
	}
	return sb.String()
}

// relativeTime converts an ISO timestamp to a human-readable relative time string.
func relativeTime(ts string) string {
	if ts == "" {
		return ""
	}
	// Try common formats
	var t time.Time
	var err error
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05+01:00", "2006-01-02 15:04:05"} {
		t, err = time.Parse(layout, ts)
		if err == nil {
			break
		}
	}
	if err != nil {
		return ""
	}

	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}

// formatProjects converts list_projects JSON into compact lines.
func formatProjects(raw json.RawMessage) string {
	var items []struct {
		Project      string `json:"project"`
		SessionCount int    `json:"session_count"`
		LastActivity string `json:"last_activity"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return string(raw)
	}
	if len(items) == 0 {
		return "(no projects)"
	}

	var sb strings.Builder
	for _, p := range items {
		last := p.LastActivity
		if len(last) > 10 {
			last = last[:10] // date only
		}
		sb.WriteString(fmt.Sprintf("%s (%d sessions, last: %s)\n", p.Project, p.SessionCount, last))
	}
	return sb.String()
}

// formatRelated converts related_to_file / get_coverage JSON into compact lines.
func formatRelated(raw json.RawMessage) string {
	// Try sessions format (related_to_file)
	var sessData struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Project   string `json:"project"`
			CreatedAt string `json:"created_at"`
		} `json:"sessions"`
	}
	if json.Unmarshal(raw, &sessData) == nil && len(sessData.Sessions) > 0 {
		var sb strings.Builder
		for _, s := range sessData.Sessions {
			created := s.CreatedAt
			if len(created) > 10 {
				created = created[:10]
			}
			sb.WriteString(fmt.Sprintf("%s [%s] %s\n", s.SessionID, s.Project, created))
		}
		return sb.String()
	}

	// Try files format (get_coverage)
	var fileData struct {
		Files []struct {
			Path    string `json:"path"`
			Count   int    `json:"count"`
			LastHit string `json:"last_hit"`
		} `json:"files"`
	}
	if json.Unmarshal(raw, &fileData) == nil && len(fileData.Files) > 0 {
		var sb strings.Builder
		for _, f := range fileData.Files {
			sb.WriteString(fmt.Sprintf("%s (%dx, last: %s)\n", f.Path, f.Count, f.LastHit))
		}
		return sb.String()
	}

	return string(raw)
}

// formatPersona converts get_persona JSON into readable trait lines.
func formatPersona(raw json.RawMessage) string {
	var data struct {
		Directive string `json:"directive"`
		Traits    []struct {
			Dimension  string  `json:"dimension"`
			TraitKey   string  `json:"trait_key"`
			TraitValue string  `json:"trait_value"`
			Confidence float64 `json:"confidence"`
			Source     string  `json:"source"`
		} `json:"traits"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}

	var sb strings.Builder
	if data.Directive != "" {
		sb.WriteString("Directive: ")
		sb.WriteString(data.Directive)
		sb.WriteString("\n\n")
	}

	type traitEntry struct {
		key, val, src string
		conf          float64
	}
	grouped := make(map[string][]traitEntry)
	for _, t := range data.Traits {
		grouped[t.Dimension] = append(grouped[t.Dimension], traitEntry{t.TraitKey, t.TraitValue, t.Source, t.Confidence})
	}
	for _, dim := range []string{"expertise", "communication", "workflow", "boundaries", "context", "learning_style"} {
		traits, ok := grouped[dim]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("[%s]\n", dim))
		for _, t := range traits {
			sb.WriteString(fmt.Sprintf("  %s: %s (%.1f, %s)\n", t.key, t.val, t.conf, t.src))
		}
	}
	return sb.String()
}

// proxyCallFormat wraps proxyCall with a custom formatter function.
// proxyCallWithThreadID is like proxyCallFormat but injects thread_id from CLAUDE_SESSION_ID.
func (s *Server) proxyCallWithThreadID(method string, formatter func(json.RawMessage) string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		proxy, err := s.getProxy()
		if err != nil {
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		params := buildProxyParams(req.Params.Arguments.(map[string]any), true)

		result, err := proxy.Call(method, params)
		if err != nil {
			logMCPError("call_failed_after_retry", method, err)
			s.mu.Lock()
			s.proxy = nil
			s.mu.Unlock()
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}
		if formatter != nil {
			return mcplib.NewToolResultText(formatter(result)), nil
		}
		return mcplib.NewToolResultText(string(result)), nil
	}
}

// formatScratchpadWrite renders the response from scratchpad_write.
func formatScratchpadWrite(raw json.RawMessage) string {
	var data struct {
		Project string `json:"project"`
		Section string `json:"section"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		return data.Message
	}
	if data.Project != "" && data.Section != "" {
		return fmt.Sprintf("✓ Wrote section '%s' in project '%s'", data.Section, data.Project)
	}
	return string(raw)
}

// formatScratchpadAppend renders the response from scratchpad_append.
// Daemon returns {status, project, section, appended} or {message}.
func formatScratchpadAppend(raw json.RawMessage) string {
	var data struct {
		Status   string `json:"status"`
		Project  string `json:"project"`
		Section  string `json:"section"`
		Message  string `json:"message"`
		Appended bool   `json:"appended"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		return data.Message
	}
	if data.Project != "" && data.Section != "" {
		return fmt.Sprintf("✓ Appended to section '%s' in project '%s'", data.Section, data.Project)
	}
	return string(raw)
}

// formatScratchpadRead renders the response from scratchpad_read.
func formatScratchpadRead(raw json.RawMessage) string {
	var data struct {
		Sections []struct {
			Section string `json:"section"`
			Content string `json:"content"`
		} `json:"sections"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if len(data.Sections) == 0 {
		if data.Message != "" {
			return data.Message
		}
		return "(no sections)"
	}
	var sb strings.Builder
	for _, s := range data.Sections {
		sb.WriteString(fmt.Sprintf("## %s\n%s\n\n", s.Section, s.Content))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatScratchpadList renders the response from scratchpad_list.
func formatScratchpadList(raw json.RawMessage) string {
	var data struct {
		Projects []struct {
			Project  string   `json:"project"`
			Sections []string `json:"sections"`
		} `json:"projects"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if len(data.Projects) == 0 {
		if data.Message != "" {
			return data.Message
		}
		return "(no scratchpad projects)"
	}
	var sb strings.Builder
	for _, p := range data.Projects {
		sb.WriteString(fmt.Sprintf("%s (%d sections)", p.Project, len(p.Sections)))
		if len(p.Sections) > 0 {
			sb.WriteString(": ")
			sb.WriteString(strings.Join(p.Sections, ", "))
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatScratchpadDelete renders the response from scratchpad_delete.
// The count is read from deleted_count, which is what the daemon returns
// (handleScratchpadDelete); a daemon error arrives as a Go error from the socket
// client and never reaches this function. The formatter used to look for
// "deleted", so every successful deletion was reported as zero entries (YM-299).
// A pointer keeps an absent field distinguishable from a deletion that removed
// nothing.
func formatScratchpadDelete(raw json.RawMessage) string {
	var data struct {
		DeletedCount *int `json:"deleted_count"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.DeletedCount == nil {
		return string(raw)
	}
	return fmt.Sprintf("✓ Deleted %d entries", *data.DeletedCount)
}

func (s *Server) proxyCallFormat(method string, formatter func(json.RawMessage) string) mcpserver.ToolHandlerFunc {
	var withParams func(json.RawMessage, map[string]any) string
	if formatter != nil {
		withParams = func(raw json.RawMessage, _ map[string]any) string { return formatter(raw) }
	}
	return s.proxyCallFormatWithParams(method, withParams)
}

// proxyCallFormatWithParams is proxyCallFormat for a formatter that also reads
// the request parameters: send_to advises the caller about its own client, which
// only the resolved _source_agent tells apart.
func (s *Server) proxyCallFormatWithParams(method string, formatter func(json.RawMessage, map[string]any) string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		proxy, err := s.getProxy()
		if err != nil {
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		params := buildProxyParams(req.Params.Arguments.(map[string]any), false)

		result, err := proxy.Call(method, params)
		if err != nil {
			// Only reset proxy on connection errors — a daemon-level error means
			// the daemon is alive and answering, just the query had an issue.
			if !isDaemonError(err) {
				logMCPError("call_failed_after_retry", method, err)
				s.mu.Lock()
				s.proxy = nil
				s.mu.Unlock()
			}
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}
		if formatter != nil {
			return mcplib.NewToolResultText(formatter(result, params)), nil
		}
		return mcplib.NewToolResultText(string(result)), nil
	}
}

// withMaxResultSize annotates a CallToolResult with _meta maxResultSizeChars
// so Claude Code (v2.1.91+) allows larger tool results without truncation.
func withMaxResultSize(result *mcplib.CallToolResult, maxChars int) *mcplib.CallToolResult {
	result.Meta = &mcplib.Meta{
		AdditionalFields: map[string]any{
			"anthropic/maxResultSizeChars": maxChars,
		},
	}
	return result
}

// proxyCallLarge wraps proxyCall with a 500K result size annotation for large payloads.
func (s *Server) proxyCallLarge(method string) mcpserver.ToolHandlerFunc {
	inner := s.proxyCall(method)
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		result, err := inner(ctx, req)
		if err != nil {
			return result, err
		}
		return withMaxResultSize(result, 500000), nil
	}
}

// proxyCallFormatLarge wraps proxyCallFormat with a 500K result size annotation.
func (s *Server) proxyCallFormatLarge(method string, formatter func(json.RawMessage) string) mcpserver.ToolHandlerFunc {
	inner := s.proxyCallFormat(method, formatter)
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		result, err := inner(ctx, req)
		if err != nil {
			return result, err
		}
		return withMaxResultSize(result, 500000), nil
	}
}

// --- Dialog Formatters ---

func formatStartDialog(raw json.RawMessage) string {
	var r struct {
		DialogID int64  `json:"dialog_id"`
		Status   string `json:"status"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	return fmt.Sprintf("Dialog #%d started (status: %s). Partner will be notified on next prompt.\nIMPORTANT: Start polling: CronCreate(cron='*/1 * * * *', prompt='.', recurring=true)", r.DialogID, r.Status)
}

// neutralizePlanBlockClose keeps text from closing the block it sits in: a plan
// or message line that spells out the closing marker would end the block early,
// and everything after it renders as a top-level instruction instead of as block
// content. The proxy neutralizes the same marker for its plan checkpoint
// (internal/proxy/plan_inject.go); only the closing marker is touched, because a
// plan routinely contains paths and regexes that must come back unchanged.
func neutralizePlanBlockClose(text string) string {
	return strings.ReplaceAll(text, "[/Active Plan]", "[ /Active Plan]")
}

// planItemsSchema is the item shape of a structured plan, mirroring
// daemon.PlanItem. Status reuses the todowrite vocabulary so a display needs no
// translation, depth carries nesting, and reason is the field todowrite lacks.
func planItemsSchema() mcplib.PropertyOption {
	return mcplib.Items(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{"type": "string", "description": "One step, one line"},
			"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
			"reason":  map[string]any{"type": "string", "description": "Why this step is on the plan"},
			"depth":   map[string]any{"type": "integer", "minimum": 0, "maximum": 2, "description": "0 = top level, 1 = sub-step, 2 = detail; three levels is the maximum"},
		},
		"required": []string{"content"},
	})
}

// planItemWire mirrors the item shape the daemon returns. The MCP server must not
// import the daemon — it talks to it over the socket — so the wire shape is
// declared here.
type planItemWire struct {
	Content string `json:"content"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Depth   int    `json:"depth"`
}

func formatPlanResult(raw json.RawMessage) string {
	var r struct {
		Message string         `json:"message"`
		Plan    string         `json:"plan"`
		Status  string         `json:"status"`
		Exists  bool           `json:"exists"`
		Items   []planItemWire `json:"items"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	// The outline wins over the text: it carries the per-step status, and for a
	// plan written with both the text is the caller's own words from the same
	// call. Nothing is lost — the text still arrives in the JSON response.
	planText := neutralizePlanBlockClose(r.Plan)
	if len(r.Items) > 0 {
		planText = formatPlanItems(r.Items)
	}
	if r.Message != "" && planText != "" {
		return fmt.Sprintf("%s\n\n[Active Plan]\n%s\n[/Active Plan]", neutralizePlanBlockClose(r.Message), planText)
	}
	if planText != "" {
		status := r.Status
		if status == "" {
			status = "active"
		}
		return fmt.Sprintf("[Active Plan] (status: %s)\n%s\n[/Active Plan]", status, planText)
	}
	if !r.Exists {
		return "No active plan. Use set_plan() to create one."
	}
	if r.Message != "" {
		return r.Message
	}
	return string(raw)
}

// formatPlanItems renders the outline as a checklist. Depth carries the nesting
// and the marker the status; a value outside the two supported levels is clamped
// so a hand-written plan cannot indent a line into the margin.
func formatPlanItems(items []planItemWire) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		depth := it.Depth
		if depth < 0 {
			depth = 0
		}
		if depth > 1 {
			depth = 1
		}
		marker := "[ ]"
		switch it.Status {
		case "completed":
			marker = "[x]"
		case "in_progress":
			marker = "[>]"
		case "cancelled":
			marker = "[-]"
		}
		b.WriteString(strings.Repeat("  ", depth) + "- " + marker + " " + planItemText(it.Content))
		if it.Reason != "" {
			b.WriteString(" (" + planItemText(it.Reason) + ")")
		}
	}
	return b.String()
}

// planItemText flattens one field for print: it becomes a single line, control
// characters become spaces, and the block-closing markers are defused. Without
// the flattening a stored item could forge its own bullet line in the plan the
// model reads back.
func planItemText(text string) string {
	stripped := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, text)
	return neutralizePlanBlockClose(strings.Join(strings.Fields(stripped), " "))
}

func formatSendTo(raw json.RawMessage, params map[string]any) string {
	var r struct {
		MessageID  int64  `json:"message_id"`
		Target     string `json:"target"`
		AckDropped bool   `json:"ack_dropped"`
		// Warning is set when the daemon could not address the target yet (an
		// opencode agent whose session is still unknown): the message is stored,
		// but nothing polls the address it was stored under.
		Warning string `json:"warning"`
		// DeliveredVia names the transport the daemon used: "api" when the
		// message was pushed into the target's opencode inbox and the target
		// reacts without polling, "stored" when it waits for the target's own
		// next request, "unconfirmed" when the push was attempted and the
		// service neither took nor refused it in time.
		DeliveredVia string `json:"delivered_via"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	if r.AckDropped {
		return fmt.Sprintf("⚠ ACK dropped to avoid an acknowledgement loop — nothing was sent to %s", r.Target)
	}
	if r.Warning != "" {
		// Report the warning instead of a plain success: the target cannot be
		// reached right now, and "sent" would hide an undelivered message.
		return fmt.Sprintf("⚠ Message #%d stored for %s but not delivered yet — %s", r.MessageID, r.Target, r.Warning)
	}
	switch r.DeliveredVia {
	case "api":
		return fmt.Sprintf("✓ Message #%d delivered to %s.", r.MessageID, r.Target)
	case "unconfirmed":
		// Do not promise a delivery that was never acknowledged, and do not
		// advise a resend either: the message stays claimed, so a resend would
		// be the duplicate this path works to avoid.
		return fmt.Sprintf("⚠ Message #%d to %s: the delivery could not be confirmed — the target may already have it, so do not resend it.", r.MessageID, r.Target)
	}
	return fmt.Sprintf("✓ Message #%d stored for %s — it arrives with the target's next request.%s", r.MessageID, r.Target, pollAdvice(sourceAgentOf(params)))
}

// pollAdvice names how the caller can follow up on a stored message. The hint
// names a Claude Code tool, so it is only offered to a client that has it: an
// opencode or codex session cannot call CronCreate (YM-73).
func pollAdvice(sourceAgent string) string {
	switch sourceAgent {
	case "opencode", "codex":
		return ""
	}
	return " Start polling if needed: CronCreate(cron='*/1 * * * *', prompt='.', recurring=true)"
}

// sourceAgentOf reads the client backend buildProxyParams resolved, or "" when
// the caller could not be identified (the formatter then keeps the Claude Code
// wording, which the daemon's own callers and the v1 layout share).
func sourceAgentOf(params map[string]any) string {
	sa, _ := params["_source_agent"].(string)
	return sa
}

func formatEndDialog(raw json.RawMessage) string {
	var r struct {
		DialogID int64  `json:"dialog_id"`
		Status   string `json:"status"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	return fmt.Sprintf("Dialog #%d beendet.", r.DialogID)
}

func formatBroadcast(raw json.RawMessage) string {
	var r struct {
		MessageID int64  `json:"message_id"`
		Status    string `json:"status"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	return fmt.Sprintf("Broadcast #%d sent", r.MessageID)
}

func (s *Server) registerTools() {
	s.srv.AddTool(
		mcplib.NewTool("search",
			mcplib.WithDescription("Full-text search across conversation logs. Use when you need specific phrases, commands, or error messages from past sessions. Returns matching messages with session ID and timestamp. For semantic search use hybrid_search, for raw conversation content use deep_search."),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Query in your working language.")),
			mcplib.WithString("query_en", mcplib.Description("English translation of query.")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("since", mcplib.Description(sinceDesc)),
			mcplib.WithString("before", mcplib.Description(beforeDesc)),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCallFormat("search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("remember",
			mcplib.WithDescription("Save a lasting learning to persistent memory. Use after discovering a gotcha, making a decision, identifying a pattern, or receiving user feedback. Include structured metadata (entities, actions, trigger, anticipated_queries) for better retrieval. Model auto-resolved via param/YESMEM_MODEL_ID/proxy_state."+contentLangDirective),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("Content to remember")),
			mcplib.WithString("category", mcplib.Description("gotcha|decision|pattern|preference|explicit_teaching|strategic")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("model", mcplib.Description("Optional override; auto-resolved when omitted.")),
			mcplib.WithString("source", mcplib.Description("user_stated|agreed_upon|claude_suggested")),
			mcplib.WithString("origin", mcplib.Description("Origin tag for trust scoring (e.g. manual, repl_command)")),
			mcplib.WithNumber("supersedes", mcplib.Description("ID of learning this replaces")),
			mcplib.WithBoolean("force_supersede", mcplib.Description("Explicit override of the trust gate: retire a high-trust learning. Requires supersedes and supersede_reason. Only pass when the user or orchestrator explicitly confirmed the old learning is wrong.")),
			mcplib.WithString("supersede_reason", mcplib.Description("Justification for a forced supersede; stored in the supersede chain. Required with force_supersede.")),
			mcplib.WithArray("entities", mcplib.WithStringItems(), mcplib.Description("Files, systems, people affected (required for query_facts by entity)")),
			mcplib.WithArray("actions", mcplib.WithStringItems(), mcplib.Description("Commands or operations involved")),
			mcplib.WithString("trigger", mcplib.Description("When to surface. Deadline syntax 'before YYYY-MM-DD' auto-sets ExpiresAt.")),
			mcplib.WithArray("anticipated_queries", mcplib.WithStringItems(), mcplib.Description("(retrieval-critical) 3-8 phrases for hybrid_search.")),
			mcplib.WithString("context", mcplib.Description("Why/when is this relevant?")),
			mcplib.WithString("task_type", mcplib.Description("For unfinished: task|idea|blocked|stale")),
			mcplib.WithString("attribution", mcplib.Description("External source: e.g. 'Fullerenes (competitor)', 'mem0 docs'. Empty = own experience.")),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general (default: code)")),
		), s.proxyCallFormat("remember", formatRemember))

	s.srv.AddTool(
		mcplib.NewTool("pin",
			mcplib.WithDescription("Pin a persistent instruction visible in every turn. Use for rules, constraints, or context that must survive context collapse. Scope: session (temporary) or permanent. Remove with unpin."+contentLangDirective),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Instruction to pin")),
			mcplib.WithString("scope", mcplib.Description("session|permanent")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallFormat("pin", formatPin))

	s.srv.AddTool(
		mcplib.NewTool("unpin",
			mcplib.WithDescription("Remove a pinned instruction by ID. Use get_pins to list active pins and their IDs first. Scope must match the pin's scope (session or permanent)."),
			mcplib.WithNumber("id", mcplib.Required(), mcplib.Description("Pin ID")),
			mcplib.WithString("scope", mcplib.Description("session|permanent")),
		), s.proxyCallFormat("unpin", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_pins",
			mcplib.WithDescription("List all active pinned instructions. Use to review current pins before adding or removing. Filter by project."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCall("get_pins"))

	// Agent-to-Agent Channel Messaging (fire-and-forget, no dialog state)
	s.srv.AddTool(
		mcplib.NewTool("send_to",
			mcplib.WithDescription("Send a message to another session: a session id (claude/codex/opencode), an agent id, or a named channel. A unique id prefix of at least 8 characters resolves to the full address; ambiguous or unknown short forms are rejected. Message types: command, response, ack, status."),
			mcplib.WithString("target", mcplib.Required(), mcplib.Description("Target session ID")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message content")),
			mcplib.WithString("msg_type", mcplib.Description("command|response|ack|status")),
		), s.proxyCallFormatWithParams("send_to", formatSendTo))

	s.srv.AddTool(
		mcplib.NewTool("whoami",
			mcplib.WithDescription("Get your own session ID and agent metadata. Use at startup to discover your identity for send_to callbacks and to confirm your agent context (section, project, backend session ID). Also returns the terminal window your session is shown in (window) and the desktop it currently sits on (desktop), when known."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallWithThreadID("whoami", nil))

	s.srv.AddTool(
		mcplib.NewTool("broadcast",
			mcplib.WithDescription("Send a message to all sessions in a project. Use for announcements, status updates, or coordination across all active sessions."),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
		), s.proxyCallFormat("broadcast", formatBroadcast))

	s.srv.AddTool(
		mcplib.NewTool("deep_search",
			mcplib.WithDescription("Raw conversation history with ±3 messages of surrounding context. Use when you need the full untruncated content of past exchanges, including thinking blocks and command outputs. For semantic search across learnings use hybrid_search, for exact phrase matching use search."),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Query in your working language.")),
			mcplib.WithString("query_en", mcplib.Description("English translation of query.")),
			mcplib.WithBoolean("include_thinking", mcplib.Description("Include thinking blocks")),
			mcplib.WithBoolean("include_commands", mcplib.Description("Include command outputs")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("since", mcplib.Description(sinceDesc)),
			mcplib.WithString("before", mcplib.Description(beforeDesc)),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCallFormatLarge("deep_search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("get_session",
			mcplib.WithDescription("Load a full session by ID. Use to explore past conversations in detail. Modes: summary (overview), recent (latest messages), paginated (chunked), full (complete). For searching across sessions use search or deep_search."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
			mcplib.WithString("mode", mcplib.Description("summary|recent|paginated|full")),
			mcplib.WithNumber("offset", mcplib.Description("Offset")),
			mcplib.WithNumber("limit", mcplib.Description("Max messages")),
		), s.proxyCallLarge("get_session"))

	s.srv.AddTool(
		mcplib.NewTool("list_projects",
			mcplib.WithDescription("List all known projects with session counts and last activity. Use for cross-project navigation and to discover active projects."),
		), s.proxyCallFormat("list_projects", formatProjects))

	s.srv.AddTool(
		mcplib.NewTool("project_summary",
			mcplib.WithDescription("Get a chronological project summary with recent sessions and activity. Use to understand what's been happening in a project recently."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithNumber("limit", mcplib.Description("Max sessions")),
		), s.proxyCallFormat("project_summary", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_learnings",
			mcplib.WithDescription("Retrieve saved learnings by category, project, date range, or ID. Use to recall past decisions, gotchas, patterns, and user preferences. For full-text search across raw conversation logs use search, for semantic search use hybrid_search. Pass history=true with an ID to view the full version chain."),
			mcplib.WithNumber("id", mcplib.Description("Get by ID (ignores other filters)")),
			mcplib.WithBoolean("history", mcplib.Description("When true with id, returns full version chain (oldest-first)")),
			mcplib.WithString("category", mcplib.Description("gotcha|decision|pattern|preference|explicit_teaching|strategic|narrative")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("since", mcplib.Description(sinceDesc)),
			mcplib.WithString("before", mcplib.Description(beforeDesc)),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
			mcplib.WithString("task_type", mcplib.Description("task|idea|blocked|stale")),
		), s.proxyCallFormat("get_learnings", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("get_caps",
			mcplib.WithDescription("Load saved capability definitions. Without 'name': compact metadata list (no script bodies). With 'name': full definition including scripts. Filter also by tag or project."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("name", mcplib.Description("Get specific cap by name")),
			mcplib.WithString("tag", mcplib.Description("Filter by tag")),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCallFormat("get_caps", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("save_cap",
			mcplib.WithDescription("Save an executable capability (tool definition) that persists across sessions. Use to create reusable tools from working REPL snippets, bash commands, or multi-step workflows. Auto-supersedes existing caps with the same name. Scripts array supports tool (REPL) and handler (bash/JS) kinds."+contentLangDirective),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Capability name (e.g. 'reddit_fetch')")),
			mcplib.WithString("description", mcplib.Description("What the cap does")),
			mcplib.WithString("scripts", mcplib.Required(), mcplib.Description("JSON array of script objects (Cap-Spec v1.1). Each: {name, kind: 'tool'|'handler', runtime: 'repl'|'bash', body, schema?, sandbox?, allowed_hosts?}. At least one kind='tool' script required. allowed_hosts is the egress allowlist for the standard sandbox profile under ai-jail 2.x (an array of bare hostnames; empty means no network access at all).")),
			mcplib.WithString("tags", mcplib.Description("Comma-separated tags (e.g. 'web,reddit,fetch')")),
			mcplib.WithString("project", mcplib.Description("Project scope (omit for global)")),
			mcplib.WithBoolean("tested", mcplib.Description("Whether the handler has been verified working")),
			mcplib.WithString("test_date", mcplib.Description("ISO date when last tested")),
			mcplib.WithBoolean("auto_active", mcplib.Description("When true, this cap is activated automatically for every new Claude-Code thread — no explicit activate_cap() call needed. Use for caps that should be universally available (e.g. reddit_fetch, git_log_oneline).")),
			mcplib.WithString("requires", mcplib.Description("Comma-separated adapter declarations the cap needs, e.g. 'store,llm,sh'. Pass it to keep a CAP.md-declared list: an omitted value is re-derived from the script bodies, which does not see an adapter a script only reaches indirectly.")),
			mcplib.WithString("actions", mcplib.Description("JSON object mapping action names to instruction text (e.g. {\"setup\": \"Ask for bot token...\", \"teardown\": \"Remove jobs...\"})")),
		), s.proxyCallFormat("save_cap", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("cap_proposal_decide",
			mcplib.WithDescription("Accept or reject an auto-corrected cap proposal. Use after reviewing a proposed cap correction — accept to apply the changes, reject to keep the active cap unchanged. The proposal must be in category='cap_proposed'. On accept, the proposed bash body is applied to the active cap via save_cap with source='auto_correct_accepted'. On reject, the proposal transitions to 'cap_proposed_rejected' and the active cap is left untouched."),
			mcplib.WithNumber("id", mcplib.Required(), mcplib.Description("learnings.id of the cap_proposed row")),
			mcplib.WithString("decision", mcplib.Required(), mcplib.Description("'accept' or 'reject'")),
			mcplib.WithString("notes", mcplib.Description("Optional reviewer note appended to the proposal content")),
		), s.proxyCallFormat("cap_proposal_decide", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("list_cap_proposals",
			mcplib.WithDescription("List cap-correction proposals. Use to find pending auto-correct suggestions before reviewing and deciding with cap_proposal_decide. Defaults to status='cap_proposed' (pending review). Pass 'cap_proposed_accepted', 'cap_proposed_rejected', or 'all' to see history."),
			mcplib.WithString("status", mcplib.Description("'cap_proposed' (default), 'cap_proposed_accepted', 'cap_proposed_rejected', or 'all'")),
			mcplib.WithString("project", mcplib.Description("Filter by project (omit for all projects)")),
			mcplib.WithNumber("limit", mcplib.Description("Max rows per status (default 100)")),
		), s.proxyCallFormat("list_cap_proposals", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("register_caps",
			mcplib.WithDescription("Generate JavaScript registerTool() code for saved caps. Use to make caps available as native REPL tools. Execute the returned code in the REPL. Filter by project or tag."),
			mcplib.WithString("project", mcplib.Description("Filter by project (omit for all)")),
			mcplib.WithString("tag", mcplib.Description("Filter by tag")),
		), s.proxyCallFormat("register_caps", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("activate_cap",
			mcplib.WithDescription("Activate a saved cap for the current thread (thread_id is auto-injected). Use to enable a capability for this session — returns registerTool() JS to paste into the REPL. Capability re-injection on subsequent turns is automatic. NOTE: call this as a native MCP tool — do NOT invoke it from the REPL (the REPL VM binds only a subset of MCP tools as JS globals)."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Capability name")),
			mcplib.WithString("project", mcplib.Description("Project scope (required for project-scoped caps)")),
		), s.proxyCallWithThreadID("activate_cap", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("deactivate_cap",
			mcplib.WithDescription("Deactivate a cap for the current thread (thread_id is auto-injected). Use to stop capability re-injection on subsequent turns. The proxy stops re-injecting its registerTool snippet."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Capability name")),
		), s.proxyCallWithThreadID("deactivate_cap", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("execute_cap",
			mcplib.WithDescription("Execute a saved capability handler sandboxed. Returns JSON result. Use to invoke cap tools from any context."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Capability name")),
			mcplib.WithString("fn", mcplib.Description("Function name (default: first tool-kind script)")),
			mcplib.WithString("args", mcplib.Description("JSON string of arguments (default: '{}')")),
		), s.proxyCallWithThreadID("execute_cap", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("cap_store",
			mcplib.WithDescription("Capability database — namespaced tables for structured data. Use for persistent storage within caps (create_table, upsert, query, delete, list_tables, claim_and_read). Pass columns as JSON array [{name,type}] for create_table. Pass data as JSON object for upsert (include id to update). Pass args as JSON array of bind values for WHERE clauses."),
			mcplib.WithString("capability", mcplib.Required(), mcplib.Description("Capability name")),
			mcplib.WithString("action", mcplib.Required(), mcplib.Description("create_table|upsert|query|delete|list_tables|claim_and_read")),
			mcplib.WithString("table", mcplib.Description("Table name")),
			mcplib.WithString("columns", mcplib.Description("JSON array of {name,type} objects (create_table)")),
			mcplib.WithString("data", mcplib.Description("JSON object with column values (upsert)")),
			mcplib.WithString("where", mcplib.Description("WHERE clause with ? placeholders")),
			mcplib.WithString("args", mcplib.Description("JSON array of bind values for WHERE")),
			mcplib.WithNumber("limit", mcplib.Description("Max rows (query, default 100)")),
		), s.proxyCallFormat("cap_store", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("query_facts",
			mcplib.WithDescription("Search learning metadata by entity, action, or keyword. Use for targeted lookups like 'all learnings about proxy.go' or 'every git push action'. Hits the SQLite metadata table directly — faster than hybrid_search for very recent learnings that may not be vector-indexed yet."),
			mcplib.WithString("entity", mcplib.Description("Entity filter (LIKE match)")),
			mcplib.WithString("action", mcplib.Description("Action filter (LIKE match)")),
			mcplib.WithString("keyword", mcplib.Description("Keyword filter (LIKE match)")),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("category", mcplib.Description("Category filter")),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCallFormat("query_facts", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("related_to_file",
			mcplib.WithDescription("Find all sessions that edited or referenced a specific file. Use when investigating a file's history across sessions or tracking down when a change was introduced."),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		), s.proxyCallFormat("related_to_file", formatRelated))

	s.srv.AddTool(
		mcplib.NewTool("relate_learnings",
			mcplib.WithDescription("Create a typed edge between two learnings so the graph can surface one when the other is a search hit. "+
				"Write one when the relation is a fact you can name: the extraction already adds relates_to by itself for learnings that share entities, "+
				"so a manual edge is worth writing when the relation is stronger or of another kind. "+
				"Types, strongest promotion first: depends_on, supports, contradicts, relates_to — depends_on and supports lift a neighbour's score above a generic relation. "+
				"The edge is directed for search: relate_learnings(A, B) lets B surface when A is a hit, not the other way round, so call it both ways when the relation is mutual. "+
				"Repeating a call on the same pair stacks weight and the promoted score caps at weight 3.0, so one call is normally enough. "+
				"A second call on the same pair with another type overwrites the first: a pair carries one type. Never relate a learning to itself."),
			mcplib.WithNumber("learning_id_a", mcplib.Required(), mcplib.Description("Source learning ID — the edge points away from it")),
			mcplib.WithNumber("learning_id_b", mcplib.Required(), mcplib.Description("Target learning ID — this one surfaces when the source is a search hit")),
			mcplib.WithString("relation_type", mcplib.Required(), mcplib.Description("supports|contradicts|depends_on|relates_to — depends_on and supports promote strongest")),
		), s.proxyCallFormat("relate_learnings", nil))

	s.srv.AddTool(
		mcplib.NewTool("get_coverage",
			mcplib.WithDescription("Show which files were edited in a project across all sessions. Use for project overview and to discover hotspots. For per-file session history use related_to_file."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
		), s.proxyCallFormat("get_coverage", formatRelated))

	s.srv.AddTool(
		mcplib.NewTool("get_project_profile",
			mcplib.WithDescription("Get an auto-generated project portrait. Use for quick project overview — surfaces key files, technologies, session counts, and active areas."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
		), s.proxyCallLarge("get_project_profile"))

	s.srv.AddTool(
		mcplib.NewTool("get_self_feedback",
			mcplib.WithDescription("Retrieve recent corrections, confirmations, and feedback about your work. Use after returning from a break to catch up on what went wrong or right in recent sessions. Filters by number of days."),
			mcplib.WithNumber("days", mcplib.Description("Days")),
		), s.proxyCallFormat("get_self_feedback", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("set_persona",
			mcplib.WithDescription("Set a persona trait for the user profile. Use to record user preferences, expertise levels, or communication style. Dimension is auto-detected if empty."+contentLangDirective),
			mcplib.WithString("trait_key", mcplib.Required(), mcplib.Description("Trait key")),
			mcplib.WithString("value", mcplib.Required(), mcplib.Description("Value")),
			mcplib.WithString("dimension", mcplib.Description("Auto-detected if empty")),
		), s.proxyCallFormat("set_persona", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_persona",
			mcplib.WithDescription("Get the current persona profile with all traits and confidence values. Use to recall user preferences, expertise, and communication style."),
		), s.proxyCallFormat("get_persona", formatPersona))

	s.srv.AddTool(
		mcplib.NewTool("resolve",
			mcplib.WithDescription("Mark an unfinished task (learning with task_type) as resolved. Use after completing work tracked by a task-type learning. For text-based resolution without knowing the learning ID, use resolve_by_text."),
			mcplib.WithNumber("learning_id", mcplib.Required(), mcplib.Description("Learning ID")),
			mcplib.WithString("reason", mcplib.Description("Reason")),
		), s.proxyCallFormat("resolve", formatResolve))

	s.srv.AddTool(
		mcplib.NewTool("resolve_by_text",
			mcplib.WithDescription("Find and resolve an unfinished task by searching its text content. Resolves only when exactly one open item contains every word of the search text and the text has at least 3 words; otherwise nothing is resolved and the candidates are returned so you can resolve one by id. Use reopen to undo a wrong resolve. Filters by project."),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("Search text")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallFormat("resolve_by_text", formatResolve))

	s.srv.AddTool(
		mcplib.NewTool("reopen",
			mcplib.WithDescription("Make a resolved learning active again. Use when resolve or resolve_by_text closed the wrong item, or when a task was marked done too early. Also reopens items closed by the automated stale batch cleanup; a learning that points at a real successor keeps its link."),
			mcplib.WithNumber("learning_id", mcplib.Required(), mcplib.Description("Learning ID")),
		), s.proxyCallFormat("reopen", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("quarantine_session",
			mcplib.WithDescription("Exclude all learnings from a session from search and injection. Use when a session produced bad data (loops, hallucinations, wrong approaches) that should not influence future work. Pass project to scope the write: a session id is not unique across projects."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
			mcplib.WithString("project", mcplib.Description("Project the session belongs to. Scopes the quarantine; without it every row under the session id is quarantined.")),
		), s.proxyCallFormat("quarantine_session", nil))

	s.srv.AddTool(
		mcplib.NewTool("unquarantine_session",
			mcplib.WithDescription("Lift a session quarantine: its learnings become searchable and injectable again. Use when a session was quarantined by mistake or after the bad learnings were cleaned up."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		), s.proxyCallFormat("unquarantine_session", nil))

	s.srv.AddTool(
		mcplib.NewTool("skip_indexing",
			mcplib.WithDescription("Prevent a session from being indexed entirely. Use proactively when a session is unlikely to produce valuable learnings — saves processing and keeps the index clean."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		), s.proxyCallFormat("skip_indexing", nil))

	// ━━━ Plan management tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("set_plan",
			mcplib.WithDescription("Set the active plan for the current thread — survives context collapse and proxy restarts. Use for any task spanning more than 5 tool cycles, exploring multiple hypotheses, or touching multiple files. Primary anchor against context loss. Pair with update_plan."),
			mcplib.WithString("plan", mcplib.Description("Full plan text. Optional when items is given: the text is then derived from the items.")),
			mcplib.WithString("scope", mcplib.Description("session|permanent")),
			mcplib.WithArray("items", mcplib.Description("Structured outline of the plan. When set, the items are the source of truth: the ledger summarizes them and the opencode sidebar draws them. Drop depth and numbering from the text, they come from this array."), planItemsSchema()),
		), s.proxyCallWithThreadID("set_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("update_plan",
			mcplib.WithDescription("Update the active plan — mark items completed, add new items, or remove items. Use after each milestone or pivot to keep the plan current. The plan is your primary anchor against context loss. When the plan has items, completed/add/remove act on the items and the full text is re-derived."),
			mcplib.WithString("plan", mcplib.Description("Full replacement")),
			mcplib.WithArray("completed", mcplib.WithStringItems(), mcplib.Description("Items to mark done (substring match)")),
			mcplib.WithArray("add", mcplib.WithStringItems(), mcplib.Description("Items to add")),
			mcplib.WithArray("remove", mcplib.WithStringItems(), mcplib.Description("Items to remove (substring match)")),
			mcplib.WithArray("items", mcplib.Description("Structured outline as a full replacement of the current items"), planItemsSchema()),
		), s.proxyCallWithThreadID("update_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("get_plan",
			mcplib.WithDescription("Get the current active plan. Use for self-orientation after context collapse or when resuming work — the plan is your context-loss-proof anchor."),
		), s.proxyCallWithThreadID("get_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("complete_plan",
			mcplib.WithDescription("Mark the active plan as completed. Use when all plan items are done — stops plan checkpoints and signals completion to the orchestration layer."),
		), s.proxyCallWithThreadID("complete_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("verify_claims",
			mcplib.WithDescription("Judge a DONE report against the world: the daemon re-derives every claim (commit, branch, test run, diff scope, plan items, evidence ids) instead of believing it. Answer is accepted or blocked with the blocking findings. Pass the report text, or a scratchpad section that holds it."),
			mcplib.WithString("repo", mcplib.Required(), mcplib.Description("Repository the claims are checked against")),
			mcplib.WithString("report", mcplib.Description("Report text. Omit when passing project and section.")),
			mcplib.WithString("section", mcplib.Description("Scratchpad section holding the report")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("base", mcplib.Description("Base revision a diff-scope claim is compared from; rejected with the yesloop-done profile, which derives it")),
			mcplib.WithString("profile", mcplib.Description("Claim classes the report must contain, e.g. yesloop-done")),
			mcplib.WithString("policy", mcplib.Description("Class policy: yesloop-done tolerates an unprovable deploy; empty stays strict")),
			mcplib.WithBoolean("strict", mcplib.Description("Reserve success for a fully proven report")),
			mcplib.WithString("budget", mcplib.Description("Run budget as a Go duration, e.g. 5m")),
			mcplib.WithString("report_session", mcplib.Description("Session the report belongs to; a cited subagent must descend from it (ignored when a section is given: its agent owns it)")),
			mcplib.WithString("plan_thread", mcplib.Description("Thread whose plan a [PLAN:] claim is checked against (ignored when a section is given: its agent owns it)")),
		), s.proxyCall("verify_claims"))

	s.srv.AddTool(
		mcplib.NewTool("hybrid_search",
			mcplib.WithDescription("Combined BM25 keyword + vector semantic search for learning retrieval. Use as your primary memory access tool — provides the best balance of precision and recall. For raw conversation content use deep_search, for exact phrase matching use search."),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("since", mcplib.Description(sinceDesc)),
			mcplib.WithString("before", mcplib.Description(beforeDesc)),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCallFormat("hybrid_search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("get_compacted_stubs",
			mcplib.WithDescription("Retrieve compacted (archived) conversation stubs for a session. Use to zoom into previously collapsed conversation segments by index range."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
			mcplib.WithNumber("from_idx", mcplib.Description("Start index")),
			mcplib.WithNumber("to_idx", mcplib.Description("End index")),
		), s.proxyCallLarge("get_compacted_stubs"))

	s.srv.AddTool(
		mcplib.NewTool("expand_context",
			mcplib.WithDescription("Expand archived conversation segments back into context. Use when a stub or collapsed block needs more detail. Filter by search query or message range."),
			mcplib.WithString("query", mcplib.Description("Search query")),
			mcplib.WithString("message_range", mcplib.Description("Message range")),
		), s.proxyCallWithThreadID("expand_context", nil))

	s.srv.AddTool(
		mcplib.NewTool("set_config",
			mcplib.WithDescription("Set a configuration value. Without session_id the change is written to config.yaml and, for the token-threshold keys, applied to the running proxy immediately. With session_id it applies only to that session, and only for token_threshold: the proxy re-reads no other key per session, so such a call is refused."),
			mcplib.WithString("key", mcplib.Required(), mcplib.Description("Config key")),
			mcplib.WithString("value", mcplib.Required(), mcplib.Description("Value")),
			mcplib.WithString("session_id", mcplib.Description("Session ID")),
		), s.proxyCallFormat("set_config", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_config",
			mcplib.WithDescription("Read a configuration value. Use to check current settings like token thresholds. For token_threshold a session-specific override is checked first, then the global one, then config.yaml. Other keys are read from config.yaml; passing session_id for them is refused."),
			mcplib.WithString("key", mcplib.Required(), mcplib.Description("Config key")),
			mcplib.WithString("session_id", mcplib.Description("Session ID")),
		), s.proxyCall("get_config"))

	s.srv.AddTool(
		mcplib.NewTool("docs_search",
			mcplib.WithDescription("Search indexed documentation by keyword or semantic meaning. Use before guessing API behavior, function signatures, or idiomatic patterns. Supports filtering by source, section, doc type, and exact BM25-only mode. For listing available sources use list_docs."),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query (maximum 2000 characters)")),
			mcplib.WithString("source", mcplib.Description("Source name filter")),
			mcplib.WithString("section", mcplib.Description("Section heading filter")),
			mcplib.WithString("since", mcplib.Description(sinceDesc)),
			mcplib.WithString("before", mcplib.Description(beforeDesc)),
			mcplib.WithBoolean("exact", mcplib.Description("BM25 only, no vector search")),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
			mcplib.WithString("doc_type", mcplib.Description("reference|style")),
			mcplib.WithString("project", mcplib.Description("Scope results to a project (plus global sources); boosts the project's own sources")),
		), s.proxyCallFormat("docs_search", formatDocsSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("list_docs",
			mcplib.WithDescription("List all indexed documentation sources. Use to discover available reference docs before searching. Filter by project."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallFormat("list_docs", formatDocSources))

	s.srv.AddTool(
		mcplib.NewTool("ingest_docs",
			mcplib.WithDescription("Import documentation (.md/.txt/.rst/.pdf) into the knowledge base. Use to index reference docs, style guides, or API manuals for docs_search retrieval. Supports rule extraction and auto-injection by file extension."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Source name")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Absolute path to a file or directory to index (.md/.txt/.rst/.pdf). Relative paths are rejected: the daemon would resolve them against its own working directory, not the caller's.")),
			mcplib.WithString("version", mcplib.Description("Version string")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general")),
			mcplib.WithBoolean("rules", mcplib.Description("Condense into rules block for re-injection")),
			mcplib.WithString("trigger_extensions", mcplib.Description("Auto-inject on file extensions (e.g. '.go,.mod')")),
			mcplib.WithString("doc_type", mcplib.Description("reference|style")),
		), s.proxyCallFormat("ingest_docs", formatIngestResult))

	s.srv.AddTool(
		mcplib.NewTool("remove_docs",
			mcplib.WithDescription("Remove a documentation source and its indexed data. Use when a doc source is outdated or no longer needed."),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Source name")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallFormat("remove_docs", formatRemoveDocsResult))

	// ━━━ Scratchpad tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("scratchpad_write",
			mcplib.WithDescription("Write a section to the shared persistent scratchpad (upsert). Use for cross-session state, agent coordination, or storing intermediate results. Project and section are required."+contentLangDirective),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("section", mcplib.Required(), mcplib.Description("Section name")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Section content")),
		), s.proxyCallFormat("scratchpad_write", formatScratchpadWrite))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_append",
			mcplib.WithDescription("Append content to an existing scratchpad section (inserts a `---` separator between old and new content). Use for adding status updates, progress notes, or replies to an existing section without read-then-write. Project, section, and content are required."+contentLangDirective),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("section", mcplib.Required(), mcplib.Description("Section name to append to")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Content to append")),
		), s.proxyCallFormat("scratchpad_append", formatScratchpadAppend))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_read",
			mcplib.WithDescription("Read scratchpad sections. Use to retrieve shared state, agent briefings, or stored results. Omit section to read all sections in a project."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("section", mcplib.Description("Section (omit for all)")),
		), s.proxyCallFormat("scratchpad_read", formatScratchpadRead))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_list",
			mcplib.WithDescription("List scratchpad projects and their sections. Use to discover available scratchpad state across projects."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCallFormat("scratchpad_list", formatScratchpadList))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_delete",
			mcplib.WithDescription("Delete a scratchpad section or an entire project's scratchpad. Use for cleanup after work is done. Omit section to delete the entire project."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("section", mcplib.Description("Section (omit to delete project)")),
		), s.proxyCallFormat("scratchpad_delete", formatScratchpadDelete))

	// ━━━ Agent orchestration tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("spawn_agent",
			mcplib.WithDescription("Spawn an autonomous agent for a project section. Use to delegate work to an isolated agent with its own worktree, model, and token budget. Agents run as visible TUI processes. Specify backend (claude|codex|opencode) and optional model override."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("section", mcplib.Required(), mcplib.Description("Section name")),
			mcplib.WithString("caller_session", mcplib.Description("Callback session")),
			mcplib.WithNumber("token_budget", mcplib.Description("Max output tokens per agent (0=no limit). The unit is output tokens: input tokens are cumulative and include cache reads")),
			mcplib.WithNumber("max_turns", mcplib.Description("Max turns (0=unlimited)")),
			mcplib.WithString("model", mcplib.Description("Model override")),
			mcplib.WithString("theme", mcplib.Description("TUI theme for this agent (empty = the theme of the agent type, e.g. yesmem-loop for a yesloop section)")),
			mcplib.WithString("work_dir", mcplib.Description("Working directory")),
			mcplib.WithString("backend", mcplib.Description("claude|codex|opencode")),
			mcplib.WithString("permission_kick", mcplib.Description("Permission-kick setting for the agent: on|off (empty = inherit config default)")),
			mcplib.WithString("restart_strategy", mcplib.Description("What the supervisor does when the spawn fails: temporary (default, no restart)|transient (restart up to max_restarts)")),
			mcplib.WithNumber("max_restarts", mcplib.Description("Restart attempts for a transient agent (default 3)")),
		), s.proxyCall("spawn_agent"))

	s.srv.AddTool(
		mcplib.NewTool("relay_agent",
			mcplib.WithDescription("Inject content into a running agent's terminal. Use to send instructions, nudges, or context to an agent without stopping it. Target by agent ID or section name."),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID or section")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message to inject")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
			mcplib.WithString("caller_session", mcplib.Description("Caller ID")),
		), s.proxyCall("relay_agent"))

	s.srv.AddTool(
		mcplib.NewTool("stop_agent",
			mcplib.WithDescription("Stop a running agent. Use to terminate an agent that has completed its work or is misbehaving. Target by agent ID."),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCall("stop_agent"))

	s.srv.AddTool(
		mcplib.NewTool("stop_all_agents",
			mcplib.WithDescription("Stop all running agents in a project. Use for cleanup or when resetting a project's agent state."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
		), s.proxyCall("stop_all_agents"))

	s.srv.AddTool(
		mcplib.NewTool("resume_agent",
			mcplib.WithDescription("Resume a previously stopped agent. Target by agent ID, section name, or session id (claude session, opencode ses_*, codex session); an explicit session_id is persisted when the backend session column is empty. A bare opencode session id with no agent row bootstraps a fresh agent from opencode's own session DB. Use to continue work from where a stopped agent left off."),
			mcplib.WithString("to", mcplib.Description("Agent ID, section name, or session id")),
			mcplib.WithString("session_id", mcplib.Description("Explicit session id to resume (claude session, opencode ses_*, codex session)")),
			mcplib.WithString("project", mcplib.Description("Project name (for section lookup)")),
		), s.proxyCall("resume_agent"))

	s.srv.AddTool(
		mcplib.NewTool("list_agents",
			mcplib.WithDescription("List all agents in a project with their status, PID, and section. Use to monitor running agents and discover their IDs for relay_agent or stop_agent."),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCall("list_agents"))

	s.srv.AddTool(
		mcplib.NewTool("get_agent",
			mcplib.WithDescription("Get detailed information about a specific agent. Use to check an agent's status, backend session (opencode_session_id / codex_session_id) or worktree before sending commands via relay_agent or stop_agent. The row's own session id is not part of the reply: it is the capability that authorizes bind and close. The reply carries the live stream fields stream_active, stream_bytes, stream_started, stream_is_subagent and subagent_streams: a non-zero subagent_streams (or an active child session) means the agent is blocked inside a task() subagent, so a relay only reaches it after that subagent returns."),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID or section")),
			mcplib.WithString("project", mcplib.Description(projectFilterDesc)),
		), s.proxyCall("get_agent"))

	s.srv.AddTool(
		mcplib.NewTool("update_agent_status",
			mcplib.WithDescription("Update an agent's semantic work phase description. Use to signal progress through the 6-phase pipeline or to mark the current milestone. Free-form string, displayed in agent listings."),
			mcplib.WithString("phase", mcplib.Required(), mcplib.Description("Work phase description")),
			mcplib.WithString("id", mcplib.Description("Agent ID (auto-resolved)")),
		), s.proxyCall("update_agent_status"))

	// Code Intelligence tools
	s.srv.AddTool(
		mcplib.NewTool("search_code_index",
			mcplib.WithDescription("Search the code graph for symbols (functions, types, methods, packages) by name pattern. Use for fast symbol lookups without filesystem access. For full-text code search use search_code."),
			mcplib.WithString("pattern", mcplib.Required(), mcplib.Description("Substring match")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("kind", mcplib.Description("function|type|method|package")),
			mcplib.WithString("file_pattern", mcplib.Description("File path filter")),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCall("search_code_index"))

	s.srv.AddTool(
		mcplib.NewTool("search_code",
			mcplib.WithDescription("Full-text search across source files, enriched with graph context. Use to find code by content — returns containing function and callers alongside matches. For symbol-by-name search use search_code_index."),
			mcplib.WithString("pattern", mcplib.Required(), mcplib.Description("Text pattern")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("file_pattern", mcplib.Description("Glob or substring filter")),
			mcplib.WithNumber("limit", mcplib.Description(limitDesc)),
		), s.proxyCall("search_code"))

	s.srv.AddTool(
		mcplib.NewTool("get_code_context",
			mcplib.WithDescription("Get a symbol's signature plus its graph neighbors: who calls it, what it calls, what types it references. Use before refactoring or changing a function to understand the blast radius. For locating symbols by name use search_code_index, for full source use get_code_snippet."),
			mcplib.WithString("qualified_name", mcplib.Required(), mcplib.Description("From search_code_index")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithBoolean("include_neighbors", mcplib.Description("Include edges")),
		), s.proxyCall("get_code_context"))

	s.srv.AddTool(
		mcplib.NewTool("get_code_snippet",
			mcplib.WithDescription("Retrieve full source code for a symbol or arbitrary file range. Two modes: (1) qualified_name from search_code_index, (2) file + start_line + end_line for arbitrary ranges. Use as your primary code-reading tool instead of shell cat/read."),
			mcplib.WithString("qualified_name", mcplib.Description("From search_code_index")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("file", mcplib.Description("Relative file path for range mode")),
			mcplib.WithNumber("start_line", mcplib.Description("Start line (1-based) for range mode")),
			mcplib.WithNumber("end_line", mcplib.Description("End line (1-based, inclusive) for range mode")),
		), s.proxyCall("get_code_snippet"))

	s.srv.AddTool(
		mcplib.NewTool("get_file_symbols",
			mcplib.WithDescription("List all top-level symbols in a file with line numbers. Use for quick file overview before diving into specific symbols. Returns func, method, var, const, type."),
			mcplib.WithString("file", mcplib.Required(), mcplib.Description("Relative file path")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
		), s.proxyCall("get_file_symbols"))

	s.srv.AddTool(
		mcplib.NewTool("get_dependency_map",
			mcplib.WithDescription("Generate a package import dependency graph with cycle detection. Use to understand module structure and identify circular dependencies before refactoring."),
			mcplib.WithString("package", mcplib.Required(), mcplib.Description("Package name")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithNumber("depth", mcplib.Description("Depth (default 2)")),
		), s.proxyCall("get_dependency_map"))

	s.srv.AddTool(
		mcplib.NewTool("graph_traverse",
			mcplib.WithDescription("Trace call paths and dependencies from a code graph node. Use to understand call chains, imports, or type definitions — follow inbound/outbound edges by type (calls, imports, defines)."),
			mcplib.WithString("from", mcplib.Required(), mcplib.Description("Starting node")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("direction", mcplib.Description("inbound|outbound|both")),
			mcplib.WithString("edge_type", mcplib.Description("imports|defines|calls")),
			mcplib.WithNumber("depth", mcplib.Description("Max depth")),
		), s.proxyCall("graph_traverse"))

	s.srv.AddTool(
		mcplib.NewTool("get_file_index",
			mcplib.WithDescription("List source files in a directory with learning and gotcha annotations. Use to browse a project's file structure and discover files with known issues or important context."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description(projectRequiredDesc)),
			mcplib.WithString("dir", mcplib.Description("Subdirectory to index (omit for project root)")),
		), s.proxyCall("get_file_index"))

	s.srv.AddTool(
		mcplib.NewTool("dismiss_repl_pattern",
			mcplib.WithDescription("Dismiss a recorded REPL command pattern from future cap-suggestion analysis. Use when a suggested pattern is a false positive. Resets the pattern's count to 0. At 3 dismissals the pattern is flagged permanently ignored."),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name (as given in the suggestion)")),
			mcplib.WithString("shape_hash", mcplib.Required(), mcplib.Description("16-char shape hash from the suggestion")),
		), s.proxyCall("dismiss_repl_pattern"))

	s.srv.AddTool(
		mcplib.NewTool("dismiss_code_nav",
			mcplib.WithDescription("Lift the code-navigation block for a session. One call is enough: the block message names this call when the code tools do not answer the question, and the next navigation runs. The counter is kept for the session history, not as a threshold."),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID the block message names")),
		), s.proxyCall("dismiss_code_nav"))

	s.srv.AddTool(
		mcplib.NewTool("schedule",
			mcplib.WithDescription("Create, list, delete, or run scheduled jobs. Use for recurring tasks like Telegram polling, periodic checks, or one-shot delayed work. Jobs persist across daemon restarts. Supports cron, interval, agent/headless/bash modes, and sandbox profiles."),
			mcplib.WithString("action", mcplib.Required(), mcplib.Description("create|list|delete|run")),
			mcplib.WithString("name", mcplib.Description("Job name (create)")),
			mcplib.WithString("cron", mcplib.Description("5-field cron expression: min hour dom month dow (create)")),
			mcplib.WithString("prompt", mcplib.Description("Prompt to execute on each run (create, run)")),
			mcplib.WithBoolean("enabled", mcplib.Description("Whether the job is enabled (default true)")),
			mcplib.WithBoolean("recurring", mcplib.Description("Whether the job repeats (default true). Set false for one-shot jobs that auto-delete after firing.")),
			mcplib.WithString("id", mcplib.Description("Job ID (delete, run)")),
			mcplib.WithString("mode", mcplib.Description("Execution mode: 'agent' (default), 'headless', or 'bash'")),
			mcplib.WithString("cap_name", mcplib.Description("Cap name for bash mode (resolves handler_bash from saved cap)")),
			mcplib.WithString("script_name", mcplib.Description("Script name within the cap to invoke (bash mode). When empty, the first handler/bash script wins; pass an explicit name to target a specific script (e.g. 'telegram_poll').")),
			mcplib.WithString("project", mcplib.Description("Project the job belongs to, as an absolute path (create). The daemon hands it to the run as YESMEM_PROJECT so a cap can name a project to the daemon; a bare basename is refused as ambiguous. Defaults to the caller's working directory.")),
			mcplib.WithBoolean("auto_correct", mcplib.Description("Auto-correct failed bash jobs via Sonnet (default true)")),
			mcplib.WithString("allowed_ports", mcplib.Description("Comma-separated ports for sandbox network access (default '80,443')")),
			mcplib.WithString("allowed_hosts", mcplib.Description("Comma-separated hosts for the standard sandbox profile's egress allowlist. Takes effect under ai-jail 2.x; under 0.9.x the standard profile has an open network and this list is not applied.")),
			mcplib.WithString("sandbox", mcplib.Description("Sandbox profile: none (no sandbox), standard (network-restricted, default), strict (filesystem + network restricted). strict cannot mount a job log directory, so a job's own log files are lost there; its command output is still stored.")),
			mcplib.WithNumber("interval_seconds", mcplib.Description("Fire every N seconds instead of cron. Overrides cron when set. Use for sub-minute scheduling (e.g. 15 for Telegram polling).")),
			mcplib.WithString("model", mcplib.Description("Model override for headless/agent mode (e.g. claude-opus-4-7, claude-sonnet-4-6).")),
			mcplib.WithString("backend", mcplib.Description("Agent backend for 'agent' mode: claude, codex, or opencode (as spawn_agent accepts). Empty uses the daemon's default. Ignored by 'headless' (always claude) and 'bash'.")),
		), s.proxyCall("schedule"))
}

// formatDocsSearchResult converts docs_search JSON into readable text with heading paths and content snippets.
func formatDocsSearchResult(raw json.RawMessage) string {
	var data struct {
		Message string `json:"message"`
		Results []struct {
			ID          int64   `json:"id"`
			Source      string  `json:"source"`
			Version     string  `json:"version"`
			HeadingPath string  `json:"heading_path"`
			Content     string  `json:"content"`
			Score       float64 `json:"score"`
			SourceFile  string  `json:"source_file"`
			Tokens      int     `json:"tokens_approx"`
		} `json:"results"`
		Total  int    `json:"total"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if len(data.Results) == 0 {
		return data.Message + "\n(no results)"
	}

	var sb strings.Builder
	sb.WriteString(data.Message)
	sb.WriteByte('\n')
	for _, r := range data.Results {
		heading := r.HeadingPath
		if heading == "" {
			heading = r.SourceFile
		}
		src := r.Source
		if r.Version != "" {
			src += "@" + r.Version
		}
		content := r.Content
		// Truncate long content for display
		runes := []rune(content)
		if len(runes) > 500 {
			content = string(runes[:500]) + "..."
		}
		sb.WriteString(fmt.Sprintf("\n[%s | %s] %s\n%s\n", src, heading, fmt.Sprintf("(%.2f)", r.Score), content))
	}
	return sb.String()
}

// formatDocSources converts list_docs JSON into compact lines.
func formatDocSources(raw json.RawMessage) string {
	var data struct {
		Message string `json:"message"`
		Sources []struct {
			Name         string `json:"name"`
			Version      string `json:"version"`
			ChunkCount   int    `json:"chunks"`
			Project      string `json:"project"`
			LastSync     string `json:"last_sync"`
			Path         string `json:"path"`
			PathMissing  bool   `json:"path_missing"`
			PathRelative bool   `json:"path_relative"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if len(data.Sources) == 0 {
		return data.Message
	}

	var sb strings.Builder
	sb.WriteString(data.Message)
	sb.WriteByte('\n')
	for _, s := range data.Sources {
		proj := s.Project
		if proj == "" {
			proj = "global"
		}
		ver := s.Version
		if ver == "" {
			ver = "-"
		}
		sb.WriteString(fmt.Sprintf("  %s@%s [%s] %d chunks (synced: %s)", s.Name, ver, proj, s.ChunkCount, s.LastSync))
		switch {
		case s.PathMissing:
			sb.WriteString(fmt.Sprintf("  path missing: %s", s.Path))
		case s.PathRelative:
			sb.WriteString(fmt.Sprintf("  relative path, never synced: %s", s.Path))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// formatIngestResult converts ingest_docs JSON into a compact summary line.
func formatIngestResult(raw json.RawMessage) string {
	var data struct {
		Message             string `json:"message"`
		FilesProcessed      int    `json:"files_processed"`
		FilesSkipped        int    `json:"files_skipped"`
		ChunksCreated       int    `json:"chunks_created"`
		LearningsSuperseded int    `json:"learnings_superseded"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		var sb strings.Builder
		sb.WriteString(data.Message)
		if data.LearningsSuperseded > 0 {
			sb.WriteString(fmt.Sprintf(" (%d learnings superseded)", data.LearningsSuperseded))
		}
		return sb.String()
	}
	return string(raw)
}

// formatRemoveDocsResult converts remove_docs JSON into a confirmation message.
func formatRemoveDocsResult(raw json.RawMessage) string {
	var data struct {
		Message       string `json:"message"`
		Name          string `json:"name"`
		ChunksDeleted int    `json:"chunks_deleted"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		return data.Message
	}
	return string(raw)
}

// Helper for JSON serialization
var _ json.Marshaler = (*json.RawMessage)(nil)
