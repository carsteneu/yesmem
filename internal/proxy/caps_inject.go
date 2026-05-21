package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/capfile"
)

// ScriptInjection carries one script entry inside a cap — Cap-Spec v1.1
// allows multiple scripts per cap (bundle caps), each with its own kind,
// runtime and schema. Only kind="tool" scripts become registerTool() calls.
type ScriptInjection struct {
	Name    string
	Kind    string
	Runtime string
	Body    string
	Schema  string
}

// CapInjection carries the fields the proxy needs to render the
// registerTool(...) snippets for one active cap. Cap-level metadata
// (Name/Description/Tags) plus a slice of Scripts — each tool-script
// produces one registerTool call in renderCapabilitiesBlock.
type CapInjection struct {
	Name        string
	Description string
	Tags        []string
	Project     string
	Scripts     []ScriptInjection
}

// decodeCapsResponse unmarshals the JSON list returned by the daemon's
// get_active_caps handler into []CapInjection. The daemon
// returns capResult structs with a nested CapabilityMeta — we only
// need the subset of Meta fields required to render registerTool() snippets.
func decodeCapsResponse(raw json.RawMessage) ([]CapInjection, error) {
	var results []struct {
		ID      int64  `json:"id"`
		Project string `json:"project,omitempty"`
		Source  string `json:"source"`
		Meta    struct {
			Name        string   `json:"cap_name"`
			Description string   `json:"cap_description"`
			Tags        []string `json:"cap_tags"`
			Scripts     []struct {
				Name    string `json:"name"`
				Kind    string `json:"kind"`
				Runtime string `json:"runtime"`
				Body    string `json:"body"`
				Schema  string `json:"schema"`
			} `json:"cap_scripts"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("decode caps response: %w", err)
	}
	caps := make([]CapInjection, 0, len(results))
	for _, r := range results {
		scripts := make([]ScriptInjection, 0, len(r.Meta.Scripts))
		for _, sc := range r.Meta.Scripts {
			scripts = append(scripts, ScriptInjection{
				Name:    sc.Name,
				Kind:    sc.Kind,
				Runtime: sc.Runtime,
				Body:    sc.Body,
				Schema:  sc.Schema,
			})
		}
		caps = append(caps, CapInjection{
			Name:        r.Meta.Name,
			Description: r.Meta.Description,
			Tags:        r.Meta.Tags,
			Project:     r.Project,
			Scripts:     scripts,
		})
	}
	return caps, nil
}

// renderCapabilitiesBlock produces the <system-reminder>-wrapped
// renderCapabilitiesCatalog builds a short catalog listing available caps
// without embedding full handler code. Each cap is listed with name,
// description, and schema summary. A small activate_cap bootstrapper is
// included so Claude can load the full handler on demand via MCP.
func renderCapabilitiesCatalog(caps []CapInjection) string {
	if len(caps) == 0 {
		return ""
	}
	sorted := make([]CapInjection, len(caps))
	copy(sorted, caps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString("<system-reminder>\n<caps-available>\n")
	b.WriteString("# Available Capabilities\n\n")
	b.WriteString("Activate before use: `await mcp__yesmem__activate_cap({name: \"<name>\"})` in REPL — fetches full handler from DB and returns `{code, description}`. Then `eval(result.code)` to register the tool.\n\n")
	b.WriteString("When a capability handler errors: diagnose the root cause, fix the handler, save the corrected version via `save_cap` (auto-supersedes), then retry.\n\n")

	b.WriteString("| Capability | Description |\n|---|---|\n")
	for _, cap := range sorted {
		desc := cap.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		b.WriteString(fmt.Sprintf("| %s | %s |\n", cap.Name, desc))
	}

	b.WriteString("</caps-available>\n</system-reminder>")
	return b.String()
}

// <caps-active> body containing one registerTool() snippet per cap.
// Returns empty string if caps is empty.
func renderCapabilitiesBlock(caps []CapInjection) string {
	if len(caps) == 0 {
		return ""
	}
	sorted := make([]CapInjection, len(caps))
	copy(sorted, caps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	var sb strings.Builder
	sb.WriteString("<system-reminder>\n")
	sb.WriteString("<caps-active>\n")
	sb.WriteString("# MANDATORY: Active Capabilities\n\n")
	sb.WriteString("Match user requests to the `description` of each cap below and **call the matching tool** (e.g. `await reddit_fetch({...})`) — do **NOT** use raw `Bash(curl)`, `WebFetch`, or manual pipelines when a matching cap exists. Register ALL tools below in ONE `REPL()` call at session start (idempotent, safe to repeat). Fall back to generic tools only if a cap's handler fails at runtime — and report the cap error first.\n\n")
	needsAdapter := false
	for _, c := range sorted {
		for _, sc := range c.Scripts {
			if sc.Runtime == "repl" && capfile.UsesGenericAdapters(sc.Body) {
				needsAdapter = true
				break
			}
		}
		if needsAdapter {
			break
		}
	}
	if needsAdapter {
		sharedJS := capfile.GenerateAdapterJS(capfile.DefaultAdapters(), true)
		if sharedJS != "" {
			sb.WriteString("## Adapter Aliases\n\n```js\n")
			sb.WriteString(sharedJS)
			sb.WriteString("```\n\n")
		}
	}
	sb.WriteString("## Registered Tools\n\n")
	for _, c := range sorted {
		for _, line := range renderRegisterTools(c) {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("</caps-active>\n")
	sb.WriteString("</system-reminder>")
	return sb.String()
}

// renderRegisterTools emits one registerTool(...) line per kind="tool"
// script in the cap. Bundle caps (Cap-Spec v1.1) may contain multiple
// tool-scripts plus internal handler-scripts; only tool-scripts become
// registerTool calls. Each tool-script's body is wrapped depending on
// runtime: repl bodies inline as the function expression, bash bodies
// wrap into `async () => sh(...)`. Repl bodies that use the store()
// adapter get a capability-bound store closure via WrapToolWithStore.
func renderRegisterTools(c CapInjection) []string {
	lines := make([]string, 0, len(c.Scripts))
	for _, sc := range c.Scripts {
		if sc.Kind != "tool" {
			continue
		}
		var fn string
		switch sc.Runtime {
		case "repl":
			fn = sc.Body
			if capfile.UsesStoreAdapter(fn) {
				fn = capfile.ProviderToGeneric(fn, capfile.DefaultAdapters())
				fn = capfile.WrapToolWithStore(fn, c.Name)
			}
		case "bash":
			fn = fmt.Sprintf("async () => sh(%q)", sc.Body)
		default:
			continue
		}
		if fn == "" {
			fn = "async () => null"
		}
		schema := sc.Schema
		if schema == "" {
			schema = "{}"
		}
		lines = append(lines, fmt.Sprintf("registerTool(%q, %q, %s, %s);", sc.Name, c.Description, schema, fn))
	}
	return lines
}

// injectCapabilitiesTurnImpl is the testable core of the proxy stage. It
// queries the daemon for active caps on the given thread and, if any
// are active, inserts a user/assistant pair into the message stream. The user
// message carries the <system-reminder>-wrapped <caps-active> block;
// the assistant message is a short ack that preserves the alternating-role
// invariant the Anthropic API requires.
//
// Position choice — scans forward from msgs[0] for consecutive
// framework header pairs (user message wrapped in <system-reminder> +
// assistant "Understood..." ack) and lands the caps-pair immediately
// after the last such pair. Claude Code's per-session header is always
// the briefing + code-map in that shape, so this puts caps at a
// deterministic index (e.g. 4 after a two-pair header, 2 after a
// one-pair header, 0 if no header). Stable position across turns is
// required for Anthropic prefix-cache hit rate: caps-pair sits on the
// byte-stable header prefix, and only content below the injection
// invalidates when the active-cap set changes.
//
// Adjacency — inserting before assistant(tool_use) is safe because the
// matched assistant role in header detection requires an "Understood..."
// text ack, which never carries tool_use. If a tool_use chain follows
// the header, the caps-pair lands before it and the tool_use/tool_result
// adjacency is preserved by inserting both caps messages together.
//
// No cross-message idempotency — each proxy request comes fresh from
// Claude Code, which never resends proxy-injected system-reminders from
// prior turns, so stacking cannot occur. Removed 2026-04-17 after root
// cause analysis: the old marker-scan over-matched archive-stub summaries
// that happened to mention "<caps-active>" in plain text (e.g.,
// gotcha notes referencing this injector), silently suppressing every
// subsequent injection once such a stub entered session history.
func injectCapabilitiesTurnImpl(
	req map[string]any,
	threadID string,
	parentThreadID string,
	queryFn func(method string, params map[string]any) (json.RawMessage, error),
	capsCache *CapsCache,
	logger *log.Logger,
) bool {
	qp := map[string]any{"thread_id": threadID}
	if parentThreadID != "" {
		qp["parent_thread_id"] = parentThreadID
	}
	raw, err := queryFn("get_active_caps", qp)
	var caps []CapInjection
	var decodeErr error
	if err == nil && len(raw) > 0 {
		caps, decodeErr = decodeCapsResponse(raw)
	}
	if len(caps) == 0 {
		if capsCache != nil {
			if cached, ok := capsCache.Get(threadID); ok {
				if fallback, ferr := decodeCapsResponse(cached); ferr == nil && len(fallback) > 0 {
					if logger != nil {
						logger.Printf("[caps-inject] fallback to cache for tid=%s: daemon err=%v decode err=%v cached=%d caps", threadID, err, decodeErr, len(fallback))
					}
					caps = fallback
				}
			}
		}
		if len(caps) == 0 {
			if logger != nil && (err != nil || decodeErr != nil) {
				logger.Printf("[caps-inject] skip for tid=%s: daemon err=%v decode err=%v cache empty", threadID, err, decodeErr)
			}
			return false
		}
	} else if capsCache != nil {
		capsCache.Set(threadID, raw)
	}

	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return false
	}

	lastIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m != nil && m["role"] == "user" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return false
	}

	insertAt := 0
	for insertAt+1 < lastIdx {
		u, uok := msgs[insertAt].(map[string]any)
		a, aok := msgs[insertAt+1].(map[string]any)
		if !uok || !aok {
			break
		}
		if u["role"] != "user" || a["role"] != "assistant" {
			break
		}
		uc, _ := u["content"].(string)
		if !strings.Contains(uc, "<system-reminder>") {
			break
		}
		ac, _ := a["content"].(string)
		if !strings.HasPrefix(ac, "Understood") {
			break
		}
		insertAt += 2
	}

	block := renderCapabilitiesCatalog(caps)
	capsUser := map[string]any{"role": "user", "content": block}
	capsAssistant := map[string]any{"role": "assistant", "content": "Understood. I've read the active caps."}

	newMsgs := make([]any, 0, len(msgs)+2)
	newMsgs = append(newMsgs, msgs[:insertAt]...)
	newMsgs = append(newMsgs, capsUser, capsAssistant)
	newMsgs = append(newMsgs, msgs[insertAt:]...)
	req["messages"] = newMsgs

	injectReplPatternSuggestionIfReady(req, caps, queryFn)

	return true
}

func injectReplPatternSuggestionIfReady(
	req map[string]any,
	caps []CapInjection,
	queryFn func(method string, params map[string]any) (json.RawMessage, error),
) {
	if queryFn == nil || len(caps) == 0 {
		return
	}
	project := ""
	for _, c := range caps {
		if c.Project != "" {
			project = c.Project
			break
		}
	}
	// All-globals threads (every cap is user-scoped, Project=="") have no
	// project anchor in the caps response — get_repl_pattern_suggestion needs
	// one to scope recorded patterns. Skip silently; resolving via thread→project
	// would require a daemon-side RPC which is out of scope for this fix.
	if project == "" {
		return
	}
	activeCapNames := make([]any, 0, len(caps))
	for _, c := range caps {
		if c.Name == "" {
			continue
		}
		// Spec § scope: user-scoped caps (Project=="") are usable in any project
		// context, so they belong in active_caps alongside the chosen project's caps.
		if c.Project == project || c.Project == "" {
			activeCapNames = append(activeCapNames, c.Name)
		}
	}
	if len(activeCapNames) == 0 {
		return
	}
	raw, err := queryFn("get_repl_pattern_suggestion", map[string]any{
		"project":     project,
		"active_caps": activeCapNames,
	})
	if err != nil || raw == nil || string(raw) == "null" {
		return
	}
	var resp struct {
		Pattern *ReplPatternSuggestion `json:"pattern"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Pattern == nil {
		return
	}
	if resp.Pattern.MatchedCap == "" {
		return
	}
	body := formatReplPatternSuggestion(*resp.Pattern, project)
	injectReminderIntoLastUserMessage(req, body)
}

// injectCapabilitiesTurn is the production entry point wired into the proxy
// pipeline. A thin wrapper over injectCapabilitiesTurnImpl that binds the
// queryFn to s.queryDaemon for live daemon RPC.
func (s *Server) injectCapabilitiesTurn(req map[string]any, threadID string) bool {
	tidPreview := threadID
	if len(tidPreview) > 12 {
		tidPreview = tidPreview[:12]
	}
	if threadID == "" {
		log.Printf("[caps-inject] skip: empty threadID")
		return false
	}
	injected := injectCapabilitiesTurnImpl(req, threadID, s.getParentThread(threadID), s.queryDaemon, s.capsCache, s.logger)
	log.Printf("[caps-inject] threadID=%s injected=%v", tidPreview, injected)
	return injected
}

func (s *Server) getParentThread(threadID string) string {
	return ""
}

// renderOpencodeCapabilitiesCatalog builds a catalog listing available caps
// for opencode sessions. Uses mcp__yesmem__execute_cap instead of REPL activation.
func renderOpencodeCapabilitiesCatalog(caps []CapInjection) string {
	if len(caps) == 0 {
		return ""
	}
	sorted := make([]CapInjection, len(caps))
	copy(sorted, caps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString("<system-reminder>\n<caps-available>\n")
	b.WriteString("# Available Capabilities\n\n")
	b.WriteString("To execute: `mcp__yesmem__execute_cap({name: \"<name>\", fn: \"<function>\", args: '{\"key\":\"value\"}'})` — the daemon runs the handler sandboxed and returns the result.\n\n")
	b.WriteString("| Capability | Description |\n|---|---|\n")
	for _, cap := range sorted {
		desc := cap.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		b.WriteString(fmt.Sprintf("| %s | %s |\n", cap.Name, desc))
	}
	b.WriteString("</caps-available>\n</system-reminder>")
	return b.String()
}

// injectOpencodeCapabilitiesCatalog fetches active caps and injects the opencode catalog
// into the system prompt. Called from the OpenAI parity pipeline.
func (s *Server) injectOpencodeCapabilitiesCatalog(req map[string]any, threadID, project string) {
	if threadID == "" {
		return
	}
	raw, err := s.queryDaemon("get_active_caps", map[string]any{"thread_id": threadID})
	if err != nil {
		return
	}
	caps, err := decodeCapsResponse(raw)
	if err != nil || len(caps) == 0 {
		return
	}
	catalog := renderOpencodeCapabilitiesCatalog(caps)
	if catalog == "" {
		return
	}
	AppendSystemBlock(req, "capabilities-available", catalog)
}
