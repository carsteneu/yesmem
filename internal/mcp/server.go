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

// ServeStdio starts the MCP server on stdin/stdout.
func (s *Server) ServeStdio() error {
	defer func() {
		if s.proxy != nil {
			s.proxy.Close()
		}
	}()
	return mcpserver.ServeStdio(s.srv)
}

func (s *Server) proxyCall(method string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		proxy, err := s.getProxy()
		if err != nil {
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		params := make(map[string]any)
		for k, v := range req.Params.Arguments.(map[string]any) {
			params[k] = v
		}

		// Inject caller PID so daemon can resolve session_id from pidMap
		params["_caller_pid"] = float64(os.Getppid())
		// Inject CLAUDE_SESSION_ID directly — PID lookup is unreliable with multiple sessions
		if sid := os.Getenv("CLAUDE_SESSION_ID"); sid != "" {
			params["_session_id"] = sid
		}
		if model := currentClientModel(); model != "" {
			params["_client_model"] = model
		}
		// Inject working directory so daemon can set agent workDir
		if cwd, err := os.Getwd(); err == nil {
			params["_cwd"] = cwd
		}

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

func currentClientModel() string {
	for _, key := range []string{
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
		ID           int64  `json:"id"`
		Category     string `json:"category"`
		Project      string `json:"project"`
		Content      string `json:"content"`
		ModelUsed    string `json:"model_used"`
		SupersedesID int64 `json:"supersedes_id"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
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
		sb.WriteString(fmt.Sprintf("  Supersedes: #%d\n", data.SupersedesID))
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
func formatResolve(raw json.RawMessage) string {
	var data struct {
		ID       int64  `json:"id"`
		Category string `json:"category"`
		Project  string `json:"project"`
		Content  string `json:"content"`
		Reason   string `json:"reason"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.ID == 0 {
		if data.Message != "" {
			return data.Message
		}
		return string(raw)
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

// formatLearnings converts a JSON array of learnings into compact one-liners with relative time.
func formatLearnings(raw json.RawMessage) string {
	var items []struct {
		ID          int64    `json:"id"`
		Content     string   `json:"content"`
		Category    string   `json:"category"`
		Project     string   `json:"project"`
		Confidence  float64  `json:"confidence"`
		HitCount    int      `json:"hit_count"`
		UseCount    int      `json:"use_count"`
		InjectCount int      `json:"inject_count"`
		Source      string   `json:"source"`
		CreatedAt   string   `json:"created_at"`
		Entities    []string `json:"entities,omitempty"`
		TriggerRule string   `json:"trigger_rule,omitempty"`
		TaskType    string   `json:"task_type,omitempty"`
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

		params := make(map[string]any)
		for k, v := range req.Params.Arguments.(map[string]any) {
			params[k] = v
		}
		params["_caller_pid"] = float64(os.Getppid())
		if sid := os.Getenv("CLAUDE_SESSION_ID"); sid != "" {
			params["_session_id"] = sid
			params["thread_id"] = sid
		}

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
func formatScratchpadDelete(raw json.RawMessage) string {
	var data struct {
		Deleted int    `json:"deleted"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if data.Message != "" {
		return data.Message
	}
	return fmt.Sprintf("✓ Deleted %d entries", data.Deleted)
}

func (s *Server) proxyCallFormat(method string, formatter func(json.RawMessage) string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		proxy, err := s.getProxy()
		if err != nil {
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		params := make(map[string]any)
		for k, v := range req.Params.Arguments.(map[string]any) {
			params[k] = v
		}

		// Inject caller PID so daemon can resolve session_id from pidMap
		params["_caller_pid"] = float64(os.Getppid())
		// Inject CLAUDE_SESSION_ID directly — PID lookup is unreliable with multiple sessions
		if sid := os.Getenv("CLAUDE_SESSION_ID"); sid != "" {
			params["_session_id"] = sid
		}
		// Inject working directory so daemon can set agent workDir
		if cwd, err := os.Getwd(); err == nil {
			params["_cwd"] = cwd
		}

		result, err := proxy.Call(method, params)
		if err != nil {
			logMCPError("call_failed_after_retry", method, err)
			s.mu.Lock()
			s.proxy = nil
			s.mu.Unlock()
			return mcplib.NewToolResultText(fmt.Sprintf("Error: %v", err)), nil
		}

		return mcplib.NewToolResultText(formatter(result)), nil
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

func formatPlanResult(raw json.RawMessage) string {
	var r struct {
		Message string `json:"message"`
		Plan    string `json:"plan"`
		Status  string `json:"status"`
		Exists  bool   `json:"exists"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	if r.Message != "" && r.Plan != "" {
		return fmt.Sprintf("%s\n\n[Active Plan]\n%s\n[/Active Plan]", r.Message, r.Plan)
	}
	if r.Plan != "" {
		status := r.Status
		if status == "" {
			status = "active"
		}
		return fmt.Sprintf("[Active Plan] (status: %s)\n%s\n[/Active Plan]", status, r.Plan)
	}
	if !r.Exists {
		return "No active plan. Use set_plan() to create one."
	}
	if r.Message != "" {
		return r.Message
	}
	return string(raw)
}

func formatSendTo(raw json.RawMessage) string {
	var r struct {
		MessageID int64  `json:"message_id"`
		Target    string `json:"target"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	return fmt.Sprintf("✓ Message #%d sent to %s. Start polling if needed: CronCreate(cron='*/1 * * * *', prompt='.', recurring=true)", r.MessageID, r.Target)
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
			mcplib.WithDescription("Full-text search across sessions"),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("since", mcplib.Description("ISO date lower bound")),
			mcplib.WithString("before", mcplib.Description("ISO date upper bound")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
		), s.proxyCallFormat("search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("remember",
			mcplib.WithDescription("Save a learning for future sessions"),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("Content to remember")),
			mcplib.WithString("category", mcplib.Description("gotcha|decision|pattern|preference|explicit_teaching|strategic")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("model", mcplib.Description("Caller model")),
			mcplib.WithString("source", mcplib.Description("user_stated|agreed_upon|claude_suggested")),
			mcplib.WithNumber("supersedes", mcplib.Description("ID of learning this replaces")),
			mcplib.WithArray("entities", mcplib.WithStringItems(), mcplib.Description("Files, systems, people affected")),
			mcplib.WithArray("actions", mcplib.WithStringItems(), mcplib.Description("Commands or operations involved")),
			mcplib.WithString("trigger", mcplib.Description("When to surface this knowledge")),
			mcplib.WithArray("anticipated_queries", mcplib.WithStringItems(), mcplib.Description("Search phrases to find this later")),
			mcplib.WithString("context", mcplib.Description("Why/when is this relevant?")),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general")),
			mcplib.WithString("task_type", mcplib.Description("For unfinished: task|idea|blocked|stale")),
		), s.proxyCallFormat("remember", formatRemember))

	s.srv.AddTool(
		mcplib.NewTool("pin",
			mcplib.WithDescription("Pin an instruction visible in every turn"),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Instruction to pin")),
			mcplib.WithString("scope", mcplib.Description("session|permanent")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCallFormat("pin", formatPin))

	s.srv.AddTool(
		mcplib.NewTool("unpin",
			mcplib.WithDescription("Remove a pin by ID"),
			mcplib.WithNumber("id", mcplib.Required(), mcplib.Description("Pin ID")),
			mcplib.WithString("scope", mcplib.Description("session|permanent")),
		), s.proxyCallFormat("unpin", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_pins",
			mcplib.WithDescription("List active pins"),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCall("get_pins"))

	// Agent-to-Agent Channel Messaging (fire-and-forget, no dialog state)
	s.srv.AddTool(
		mcplib.NewTool("send_to",
			mcplib.WithDescription("Send message to another session"),
			mcplib.WithString("target", mcplib.Required(), mcplib.Description("Target session ID")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message content")),
			mcplib.WithString("msg_type", mcplib.Description("command|response|ack|status")),
		), s.proxyCallFormat("send_to", formatSendTo))

	s.srv.AddTool(
		mcplib.NewTool("whoami",
			mcplib.WithDescription("Get own session ID and agent metadata"),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCall("whoami"))

	s.srv.AddTool(
		mcplib.NewTool("broadcast",
			mcplib.WithDescription("Send message to all sessions in a project"),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message")),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
		), s.proxyCallFormat("broadcast", formatBroadcast))

	s.srv.AddTool(
		mcplib.NewTool("deep_search",
			mcplib.WithDescription("Deep search with full content and ±3 message context"),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithBoolean("include_thinking", mcplib.Description("Include thinking blocks")),
			mcplib.WithBoolean("include_commands", mcplib.Description("Include command outputs")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("since", mcplib.Description("ISO date lower bound")),
			mcplib.WithString("before", mcplib.Description("ISO date upper bound")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
		), s.proxyCallFormatLarge("deep_search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("get_session",
			mcplib.WithDescription("Load a session by mode"),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
			mcplib.WithString("mode", mcplib.Description("summary|recent|paginated|full")),
			mcplib.WithNumber("offset", mcplib.Description("Offset")),
			mcplib.WithNumber("limit", mcplib.Description("Max messages")),
		), s.proxyCallLarge("get_session"))

	s.srv.AddTool(
		mcplib.NewTool("list_projects",
			mcplib.WithDescription("List all projects with session counts"),
		), s.proxyCallFormat("list_projects", formatProjects))

	s.srv.AddTool(
		mcplib.NewTool("project_summary",
			mcplib.WithDescription("Chronological project summary"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
			mcplib.WithNumber("limit", mcplib.Description("Max sessions")),
		), s.proxyCallFormat("project_summary", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_learnings",
			mcplib.WithDescription("Retrieve learnings by category or ID"),
			mcplib.WithNumber("id", mcplib.Description("Get by ID (ignores other filters)")),
			mcplib.WithString("category", mcplib.Description("gotcha|decision|pattern|preference|explicit_teaching|strategic|narrative")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("since", mcplib.Description("ISO date lower bound")),
			mcplib.WithString("before", mcplib.Description("ISO date upper bound")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
			mcplib.WithString("task_type", mcplib.Description("task|idea|blocked|stale")),
		), s.proxyCallFormat("get_learnings", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("query_facts",
			mcplib.WithDescription("Search learning metadata by entity, action, or keyword"),
			mcplib.WithString("entity", mcplib.Description("Entity filter (LIKE match)")),
			mcplib.WithString("action", mcplib.Description("Action filter (LIKE match)")),
			mcplib.WithString("keyword", mcplib.Description("Keyword filter (LIKE match)")),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("category", mcplib.Description("Category filter")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
		), s.proxyCallFormat("query_facts", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("related_to_file",
			mcplib.WithDescription("Which sessions touched this file?"),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("File path")),
		), s.proxyCallFormat("related_to_file", formatRelated))

	s.srv.AddTool(
		mcplib.NewTool("relate_learnings",
			mcplib.WithDescription("Set semantic edge between two learnings"),
			mcplib.WithNumber("learning_id_a", mcplib.Required(), mcplib.Description("Learning ID")),
			mcplib.WithNumber("learning_id_b", mcplib.Required(), mcplib.Description("Learning ID")),
			mcplib.WithString("relation_type", mcplib.Required(), mcplib.Description("supports|contradicts|depends_on|relates_to")),
		), s.proxyCallFormat("relate_learnings", nil))

	s.srv.AddTool(
		mcplib.NewTool("get_coverage",
			mcplib.WithDescription("Which files were edited in a project?"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
		), s.proxyCallFormat("get_coverage", formatRelated))

	s.srv.AddTool(
		mcplib.NewTool("get_project_profile",
			mcplib.WithDescription("Auto-generated project portrait"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
		), s.proxyCallLarge("get_project_profile"))

	s.srv.AddTool(
		mcplib.NewTool("get_self_feedback",
			mcplib.WithDescription("Recent corrections and feedback"),
			mcplib.WithNumber("days", mcplib.Description("Days")),
		), s.proxyCallFormat("get_self_feedback", formatLearnings))

	s.srv.AddTool(
		mcplib.NewTool("set_persona",
			mcplib.WithDescription("Set persona trait"),
			mcplib.WithString("trait_key", mcplib.Required(), mcplib.Description("Trait key")),
			mcplib.WithString("value", mcplib.Required(), mcplib.Description("Value")),
			mcplib.WithString("dimension", mcplib.Description("Auto-detected if empty")),
		), s.proxyCallFormat("set_persona", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_persona",
			mcplib.WithDescription("Current persona profile"),
		), s.proxyCallFormat("get_persona", formatPersona))

	s.srv.AddTool(
		mcplib.NewTool("resolve",
			mcplib.WithDescription("Resolve an unfinished task"),
			mcplib.WithNumber("learning_id", mcplib.Required(), mcplib.Description("Learning ID")),
			mcplib.WithString("reason", mcplib.Description("Reason")),
		), s.proxyCallFormat("resolve", formatResolve))

	s.srv.AddTool(
		mcplib.NewTool("resolve_by_text",
			mcplib.WithDescription("Find and resolve unfinished task by text"),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("Search text")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCallFormat("resolve_by_text", formatResolve))

	s.srv.AddTool(
		mcplib.NewTool("quarantine_session",
			mcplib.WithDescription("Quarantine session — exclude learnings from search"),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		), s.proxyCallFormat("quarantine_session", nil))

	s.srv.AddTool(
		mcplib.NewTool("skip_indexing",
			mcplib.WithDescription("Skip indexing for this session"),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		), s.proxyCallFormat("skip_indexing", nil))

	// ━━━ Plan management tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("set_plan",
			mcplib.WithDescription("Set the active plan"),
			mcplib.WithString("plan", mcplib.Required(), mcplib.Description("Plan content (free text or structured list)")),
			mcplib.WithString("scope", mcplib.Description("session|persistent")),
		), s.proxyCallWithThreadID("set_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("update_plan",
			mcplib.WithDescription("Update active plan"),
			mcplib.WithString("plan", mcplib.Description("Full replacement")),
			mcplib.WithArray("completed", mcplib.Description("Items to mark done (substring match)")),
			mcplib.WithArray("add", mcplib.Description("Items to add")),
			mcplib.WithArray("remove", mcplib.Description("Items to remove (substring match)")),
		), s.proxyCallWithThreadID("update_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("get_plan",
			mcplib.WithDescription("Get active plan"),
		), s.proxyCallWithThreadID("get_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("complete_plan",
			mcplib.WithDescription("Mark plan as completed"),
		), s.proxyCallWithThreadID("complete_plan", formatPlanResult))

	s.srv.AddTool(
		mcplib.NewTool("hybrid_search",
			mcplib.WithDescription("Hybrid BM25 + vector search"),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("since", mcplib.Description("ISO date lower bound")),
			mcplib.WithString("before", mcplib.Description("ISO date upper bound")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
		), s.proxyCallFormat("hybrid_search", formatSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("get_compacted_stubs",
			mcplib.WithDescription("Get compacted stubs for a session"),
			mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
			mcplib.WithNumber("from_idx", mcplib.Description("Start index")),
			mcplib.WithNumber("to_idx", mcplib.Description("End index")),
		), s.proxyCallLarge("get_compacted_stubs"))

	s.srv.AddTool(
		mcplib.NewTool("expand_context",
			mcplib.WithDescription("Expand archived conversation parts"),
			mcplib.WithString("query", mcplib.Description("Search query")),
			mcplib.WithString("message_range", mcplib.Description("Message range")),
		), s.proxyCallWithThreadID("expand_context", nil))

	s.srv.AddTool(
		mcplib.NewTool("set_config",
			mcplib.WithDescription("Set runtime config"),
			mcplib.WithString("key", mcplib.Required(), mcplib.Description("Config key")),
			mcplib.WithString("value", mcplib.Required(), mcplib.Description("Value")),
			mcplib.WithString("session_id", mcplib.Description("Session ID")),
		), s.proxyCallFormat("set_config", formatSimpleMessage))

	s.srv.AddTool(
		mcplib.NewTool("get_config",
			mcplib.WithDescription("Read runtime config"),
			mcplib.WithString("key", mcplib.Required(), mcplib.Description("Config key")),
			mcplib.WithString("session_id", mcplib.Description("Session ID")),
		), s.proxyCall("get_config"))

	s.srv.AddTool(
		mcplib.NewTool("docs_search",
			mcplib.WithDescription("Search indexed documentation"),
			mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query")),
			mcplib.WithString("source", mcplib.Description("Source name filter")),
			mcplib.WithString("section", mcplib.Description("Section heading filter")),
			mcplib.WithString("since", mcplib.Description("ISO date lower bound")),
			mcplib.WithString("before", mcplib.Description("ISO date upper bound")),
			mcplib.WithBoolean("exact", mcplib.Description("BM25 only, no vector search")),
			mcplib.WithNumber("limit", mcplib.Description("Max results")),
			mcplib.WithString("doc_type", mcplib.Description("reference|style")),
		), s.proxyCallFormat("docs_search", formatDocsSearchResult))

	s.srv.AddTool(
		mcplib.NewTool("list_docs",
			mcplib.WithDescription("List indexed documentation sources"),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCallFormat("list_doc_sources", formatDocSources))

	s.srv.AddTool(
		mcplib.NewTool("ingest_docs",
			mcplib.WithDescription("Import documentation (.md/.txt/.rst/.pdf) into knowledge base"),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Source name")),
			mcplib.WithString("path", mcplib.Required(), mcplib.Description("Path to index")),
			mcplib.WithString("version", mcplib.Description("Version string")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("domain", mcplib.Description("code|marketing|legal|finance|general")),
			mcplib.WithBoolean("rules", mcplib.Description("Condense into rules block for re-injection")),
			mcplib.WithString("trigger_extensions", mcplib.Description("Auto-inject on file extensions (e.g. '.go,.mod')")),
			mcplib.WithString("doc_type", mcplib.Description("reference|style")),
		), s.proxyCallFormat("ingest_docs", formatIngestResult))

	s.srv.AddTool(
		mcplib.NewTool("remove_docs",
			mcplib.WithDescription("Remove a documentation source and its data"),
			mcplib.WithString("name", mcplib.Required(), mcplib.Description("Source name")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCallFormat("remove_docs", formatRemoveDocsResult))

	// ━━━ Scratchpad tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("scratchpad_write",
			mcplib.WithDescription("Write a section to the shared scratchpad (upsert)"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
			mcplib.WithString("section", mcplib.Required(), mcplib.Description("Section name")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Section content")),
		), s.proxyCallFormat("scratchpad_write", formatScratchpadWrite))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_read",
			mcplib.WithDescription("Read scratchpad sections"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
			mcplib.WithString("section", mcplib.Description("Section (omit for all)")),
		), s.proxyCallFormat("scratchpad_read", formatScratchpadRead))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_list",
			mcplib.WithDescription("List scratchpad projects and sections"),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCallFormat("scratchpad_list", formatScratchpadList))

	s.srv.AddTool(
		mcplib.NewTool("scratchpad_delete",
			mcplib.WithDescription("Delete a scratchpad section or project"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
			mcplib.WithString("section", mcplib.Description("Section (omit to delete project)")),
		), s.proxyCallFormat("scratchpad_delete", formatScratchpadDelete))

	// ━━━ Agent orchestration tools ━━━
	s.srv.AddTool(
		mcplib.NewTool("spawn_agent",
			mcplib.WithDescription("Spawn agent for project section"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
			mcplib.WithString("section", mcplib.Required(), mcplib.Description("Section name")),
			mcplib.WithString("caller_session", mcplib.Description("Callback session")),
			mcplib.WithNumber("token_budget", mcplib.Description("Token budget (0=default)")),
			mcplib.WithNumber("max_turns", mcplib.Description("Max turns (0=unlimited)")),
			mcplib.WithString("model", mcplib.Description("Model override")),
			mcplib.WithString("work_dir", mcplib.Description("Working directory")),
			mcplib.WithString("backend", mcplib.Description("claude|codex")),
		), s.proxyCall("spawn_agent"))

	s.srv.AddTool(
		mcplib.NewTool("relay_agent",
			mcplib.WithDescription("Inject content into a running agent's terminal"),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID or section")),
			mcplib.WithString("content", mcplib.Required(), mcplib.Description("Message to inject")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
			mcplib.WithString("caller_session", mcplib.Description("Caller ID")),
		), s.proxyCall("relay_agent"))

	s.srv.AddTool(
		mcplib.NewTool("stop_agent",
			mcplib.WithDescription("Stop an agent"),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCall("stop_agent"))

	s.srv.AddTool(
		mcplib.NewTool("stop_all_agents",
			mcplib.WithDescription("Stop all agents in project"),
			mcplib.WithString("project", mcplib.Required(), mcplib.Description("Project name")),
		), s.proxyCall("stop_all_agents"))

	s.srv.AddTool(
		mcplib.NewTool("resume_agent",
			mcplib.WithDescription("Resume a stopped agent"),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID or section name")),
			mcplib.WithString("project", mcplib.Description("Project name (for section lookup)")),
		), s.proxyCall("resume_agent"))

	s.srv.AddTool(
		mcplib.NewTool("list_agents",
			mcplib.WithDescription("List agents with status and PID"),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCall("list_agents"))

	s.srv.AddTool(
		mcplib.NewTool("get_agent",
			mcplib.WithDescription("Get agent details"),
			mcplib.WithString("to", mcplib.Required(), mcplib.Description("Agent ID or section")),
			mcplib.WithString("project", mcplib.Description("Project filter")),
		), s.proxyCall("get_agent"))

	s.srv.AddTool(
		mcplib.NewTool("update_agent_status",
			mcplib.WithDescription("Update agent's semantic work phase"),
			mcplib.WithString("phase", mcplib.Required(), mcplib.Description("Work phase description")),
			mcplib.WithString("id", mcplib.Description("Agent ID (auto-resolved)")),
		), s.proxyCall("update_agent_status"))
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

// formatDocSources converts list_doc_sources JSON into compact lines.
func formatDocSources(raw json.RawMessage) string {
	var data struct {
		Message string `json:"message"`
		Sources []struct {
			Name       string `json:"name"`
			Version    string `json:"version"`
			ChunkCount int    `json:"chunks"`
			Project    string `json:"project"`
			LastSync   string `json:"last_sync"`
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
		sb.WriteString(fmt.Sprintf("  %s@%s [%s] %d chunks (synced: %s)\n", s.Name, ver, proj, s.ChunkCount, s.LastSync))
	}
	return sb.String()
}

// formatIngestResult converts ingest_docs JSON into a compact summary line.
func formatIngestResult(raw json.RawMessage) string {
	var data struct {
		Message            string `json:"message"`
		FilesProcessed     int    `json:"files_processed"`
		FilesSkipped       int    `json:"files_skipped"`
		ChunksCreated      int    `json:"chunks_created"`
		LearningsSuperseded int   `json:"learnings_superseded"`
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
