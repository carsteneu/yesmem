package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/bloom"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

func timeNow() time.Time { return time.Now() }

// onMutation triggers post-mutation side effects (MEMORY.md regeneration).
// No-op when OnMutation is nil (e.g. in tests).
func (h *Handler) onMutation() {
	if h.OnMutation != nil {
		h.OnMutation()
	}
}

// Handler processes socket requests using the daemon's resources.
type Handler struct {
	store            *storage.Store
	bloom            *bloom.Manager
	dataDir          string        // ~/.claude/yesmem/ — set by daemon after construction
	agentTerminal    string        // preferred terminal for agent windows — set by daemon from config
	agentMaxRuntime  time.Duration // max runtime per agent — set by daemon from config
	agentMaxTurns    int           // max relay turns per agent — set by daemon from config
	agentMaxDepth    int           // max spawn depth — set by daemon from config
	agentTokenBudget int           // max tokens per agent — set by daemon from config

	// Optional: vector search (set via SetEmbedding)
	indexer             *embedding.Indexer
	vectorStore         *embedding.VectorStore
	embedProvider       embedding.Provider
	searchEmbedProvider embedding.Provider
	embedQueue          chan embedJob
	ivfPath             string

	// IndexProgress returns current indexing status (set by daemon)
	IndexProgress func() (total, done, skipped int, running bool)

	// Idle detection
	idleMu          sync.Mutex
	idleCounters    map[string]*idleState
	lastMCPCallTime time.Time // global — updated on every non-idle_tick Handle()

	// Recent remember cache — proxy pops this to inject into current session
	recentRememberMu sync.Mutex
	recentRemembered []recentLearning // id+text of recently remembered learnings

	// Optional: called after mutations. Nil in tests.
	OnMutation func()

	// Optional: LLM client for destillation (set by daemon after client init)
	SummarizeClient extraction.LLMClient

	// Optional: LLM client for quality tasks (rules condensation, narratives)
	QualityClient extraction.LLMClient

	// Optional: LLM client for commit-triggered staleness evaluation
	CommitEvalClient extraction.LLMClient

	// Embed subprocess lifecycle (Option B: killed on daemon shutdown, restarts fresh)
	embedProcessMu sync.Mutex
	embedProcess   *os.Process

	// Active session tracking — set by proxy via set_active_session RPC
	activeSessionMu sync.Mutex
	activeSessionID string

	// Briefing config — set by daemon after config load
	BriefingUserProfile bool

	// PID tracking for stdin injection (session_id → OS PID of claude process)
	pidMapMu sync.Mutex
	pidMap   map[string]int

	// Window tracking for xdotool push (session_id → X11 window ID string)
	windowMapMu sync.Mutex
	windowMap   map[string]string
	terminalMap map[string]string // session_id → terminal type (ghostty, gnome-terminal, etc.)

	// Project name resolution cache (directory path → resolved project_short)
	projectCacheMu sync.RWMutex
	projectCache   map[string]string

	// Code graph per project — lazy initialized on first MCP tool access
	codeGraphMu sync.RWMutex
	codeGraphs  map[string]*codeGraphEntry
}

// recentLearning holds a recently remembered learning with its ID for injection.
type recentLearning struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// injectExcludeSession copies _session_id into exclude_session for search handlers.
// This prevents the self-referential echo loop where a session finds its own
// messages in search results. expand_context deliberately does NOT use this —
// finding own-session messages is its purpose.
func injectExcludeSession(params map[string]any) {
	if sid, ok := params["_session_id"].(string); ok && sid != "" {
		params["exclude_session"] = sid
	}
}

// resolveProjectParam resolves the "project" field in params from a directory path
// to the correct project_short via DB lookup. Results are cached per session.
func (h *Handler) resolveProjectParam(params map[string]any) map[string]any {
	project, ok := params["project"].(string)
	if !ok || project == "" {
		return params
	}

	// Fast path: already a short name (no slashes)
	// Still resolve — could be a stale name after project rename
	if len(project) < 20 && project[0] != '/' {
		// Check cache first
		h.projectCacheMu.RLock()
		if resolved, found := h.projectCache[project]; found {
			h.projectCacheMu.RUnlock()
			params["project"] = resolved
			return params
		}
		h.projectCacheMu.RUnlock()
	}

	// Check cache for full paths
	h.projectCacheMu.RLock()
	if resolved, found := h.projectCache[project]; found {
		h.projectCacheMu.RUnlock()
		params["project"] = resolved
		return params
	}
	h.projectCacheMu.RUnlock()

	// DB lookup
	resolved := h.store.ResolveProjectShort(project)

	// Cache result
	h.projectCacheMu.Lock()
	if h.projectCache == nil {
		h.projectCache = make(map[string]string)
	}
	h.projectCache[project] = resolved
	h.projectCacheMu.Unlock()

	params["project"] = resolved
	return params
}

type idleState struct {
	count    int
	lastTick time.Time
}

type idleTickResult struct {
	Count    int    `json:"count"`
	Reminder string `json:"reminder,omitempty"`
}

// NewHandler creates a request handler with access to all daemon resources.
func NewHandler(store *storage.Store, bloomMgr *bloom.Manager) *Handler {
	h := &Handler{store: store, bloom: bloomMgr, pidMap: make(map[string]int), windowMap: make(map[string]string), terminalMap: make(map[string]string)}
	h.initIdleState()
	return h
}

func (h *Handler) initIdleState() {
	h.idleCounters = make(map[string]*idleState)
}

func (h *Handler) handleIdleTick(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		sessionID = "unknown"
	}

	// Track active session for remember() calls
	if sessionID != "unknown" {
		h.activeSessionMu.Lock()
		h.activeSessionID = sessionID
		h.activeSessionMu.Unlock()
	}

	h.idleMu.Lock()
	defer h.idleMu.Unlock()

	state, ok := h.idleCounters[sessionID]
	if !ok {
		state = &idleState{}
		h.idleCounters[sessionID] = state
	}

	// Reset counter if a real MCP call happened since the last tick
	if h.lastMCPCallTime.After(state.lastTick) && !state.lastTick.IsZero() {
		state.count = 0
	}

	state.count++
	state.lastTick = time.Now()

	var reminder string
	switch {
	case state.count >= 50:
		reminder = fmt.Sprintf("⛔ Seit %d Requests kein yesmem-Zugriff. search() JETZT.", state.count)
	case state.count >= 30:
		reminder = fmt.Sprintf("⚠ Seit %d Requests kein yesmem-Zugriff. search() nutzen!", state.count)
	}

	return jsonResponse(idleTickResult{Count: state.count, Reminder: reminder})
}

// Handle dispatches a request to the appropriate method.
func (h *Handler) Handle(req Request) Response {
	// Track MCP usage for idle detection (skip for idle_tick itself)
	if req.Method != "idle_tick" {
		h.idleMu.Lock()
		h.lastMCPCallTime = time.Now()
		h.idleMu.Unlock()
	}

	switch req.Method {
	case "search":
		params := h.resolveProjectParam(req.Params)
		injectExcludeSession(params)
		return h.handleSearch(params)
	case "deep_search":
		params := h.resolveProjectParam(req.Params)
		injectExcludeSession(params)
		return h.handleDeepSearch(params)
	case "remember":
		return h.handleRemember(h.resolveProjectParam(req.Params))
	case "get_session":
		return h.handleGetSession(req.Params)
	case "list_projects":
		return h.handleListProjects()
	case "project_summary":
		return h.handleProjectSummary(h.resolveProjectParam(req.Params))
	case "get_learnings":
		return h.handleGetLearnings(h.resolveProjectParam(req.Params))
	case "query_facts":
		return h.handleQueryFacts(h.resolveProjectParam(req.Params))
	case "related_to_file":
		return h.handleRelatedToFile(req.Params)
	case "get_coverage":
		return h.handleGetCoverage(h.resolveProjectParam(req.Params))
	case "get_project_profile":
		return h.handleGetProjectProfile(h.resolveProjectParam(req.Params))
	case "get_self_feedback":
		return h.handleGetSelfFeedback(req.Params)
	case "set_persona":
		return h.handleSetPersona(req.Params)
	case "get_persona":
		return h.handleGetPersona()
	case "resolve":
		return h.handleResolve(req.Params)
	case "resolve_by_text":
		return h.handleResolveByText(h.resolveProjectParam(req.Params))
	case "quarantine_session":
		return h.handleQuarantineSession(req.Params)
	case "skip_indexing":
		return h.handleSkipIndexing(req.Params)
	case "resolve_project":
		return h.handleResolveProject(req.Params)
	case "get_rules_block":
		return h.handleGetRulesBlock(h.resolveProjectParam(req.Params))
	case "set_plan":
		return h.handleSetPlan(h.resolveProjectParam(req.Params))
	case "update_plan":
		return h.handleUpdatePlan(h.resolveProjectParam(req.Params))
	case "get_plan":
		return h.handleGetPlan(h.resolveProjectParam(req.Params))
	case "get_docs_hint":
		return h.handleGetDocsHint(h.resolveProjectParam(req.Params))
	case "complete_plan":
		return h.handleCompletePlan(h.resolveProjectParam(req.Params))
	case "hybrid_search":
		params := h.resolveProjectParam(req.Params)
		injectExcludeSession(params)
		return h.handleHybridSearch(params)
	case "vector_search":
		return h.handleVectorSearch(h.resolveProjectParam(req.Params))
	case "get_compacted_stubs":
		return h.handleGetCompactedStubs(req.Params)
	case "expand_context":
		return h.handleExpandContext(req.Params)
	case "store_compacted_block":
		return h.handleStoreCompactedBlock(req.Params)
	case "get_proxy_state":
		return h.handleGetProxyState(req.Params)
	case "set_proxy_state":
		return h.handleSetProxyState(req.Params)
	case "delete_proxy_state_prefix":
		return h.handleDeleteProxyStatePrefix(req.Params)
	case "set_config":
		return h.handleSetConfig(req.Params)
	case "get_config":
		return h.handleGetConfig(req.Params)
	case "index_status":
		return h.handleIndexStatus()
	case "idle_tick":
		return h.handleIdleTick(req.Params)
	case "track_gap":
		return h.handleTrackGap(h.resolveProjectParam(req.Params))
	case "track_session_end":
		return h.handleTrackSessionEnd(req.Params)
	case "resolve_gap":
		return h.handleResolveGap(h.resolveProjectParam(req.Params))
	case "get_active_gaps":
		return h.handleGetActiveGaps(h.resolveProjectParam(req.Params))
	case "get_learnings_since":
		return h.handleGetLearningsSince(h.resolveProjectParam(req.Params))
	case "get_session_flavors_since":
		return h.handleGetSessionFlavorsSince(h.resolveProjectParam(req.Params))
	case "get_session_flavors_for_session":
		return h.handleGetSessionFlavorsForSession(req.Params)
	case "get_pulse_learnings_since":
		return h.handleGetPulseLearningsSince(h.resolveProjectParam(req.Params))
	case "get_session_start":
		return h.handleGetSessionStart(req.Params)
	case "generate_briefing":
		return h.handleGenerateBriefing(h.resolveProjectParam(req.Params))
	case "docs_search":
		return h.handleDocsSearch(h.resolveProjectParam(req.Params))
	case "get_skill_content":
		return h.handleGetSkillContent(req.Params)
	case "list_doc_sources":
		return h.handleListDocSources(h.resolveProjectParam(req.Params))
	case "ingest_docs":
		return h.handleIngestDocs(h.resolveProjectParam(req.Params))
	case "remove_docs":
		return h.handleRemoveDocs(h.resolveProjectParam(req.Params))
	case "contextual_docs":
		return h.handleContextualDocs(h.resolveProjectParam(req.Params))
	case "list_trigger_extensions":
		return h.handleListTriggerExtensions(h.resolveProjectParam(req.Params))
	case "ping":
		return jsonResponse("pong")
	case "increment_hits":
		return h.handleIncrementInject(req.Params)
	case "increment_noise":
		return h.handleIncrementNoise(req.Params)
	case "increment_match":
		return h.handleIncrementMatch(req.Params)
	case "increment_inject":
		return h.handleIncrementInject(req.Params)
	case "increment_use":
		return h.handleIncrementUse(req.Params)
	case "increment_fail":
		return h.handleIncrementFail(req.Params)
	case "increment_save":
		return h.handleIncrementSave(req.Params)
	case "increment_turn":
		return h.handleIncrementTurn(req.Params)
	case "flag_contradiction":
		return h.handleFlagContradiction(req.Params)
	case "relate_learnings":
		return h.handleRelate(req.Params)
	case "get_contradicting_pairs":
		return h.handleGetContradictingPairs(req.Params)
	case "invalidate_on_commit":
		return h.handleInvalidateOnCommit(req.Params)
	case "pop_recent_remember":
		return h.handlePopRecentRemember()
	case "pin":
		return h.handlePin(req.Params)
	case "unpin":
		return h.handleUnpin(req.Params)
	case "get_pins":
		return h.handleGetPins(req.Params)
	case "update_fixation_ratio":
		sid, _ := req.Params["session_id"].(string)
		ratio, _ := req.Params["fixation_ratio"].(float64)
		if sid == "" {
			return errorResponse("session_id required")
		}
		if err := h.store.UpdateSessionFixationRatio(sid, ratio); err != nil {
			return errorResponse(err.Error())
		}
		return jsonResponse(map[string]any{"status": "ok"})
	case "track_proxy_usage":
		day, _ := req.Params["day"].(string)
		input := intOr(req.Params, "input_tokens", 0)
		output := intOr(req.Params, "output_tokens", 0)
		cacheRead := intOr(req.Params, "cache_read_tokens", 0)
		cacheWrite := intOr(req.Params, "cache_creation_tokens", 0)
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		if err := h.store.TrackProxyUsage(day, input, output, cacheRead, cacheWrite); err != nil {
			return errorResponse(err.Error())
		}
		return jsonResponse(map[string]any{"status": "ok"})
	case "track_fork_usage":
		day, _ := req.Params["day"].(string)
		input := intOr(req.Params, "input_tokens", 0)
		output := intOr(req.Params, "output_tokens", 0)
		cacheRead := intOr(req.Params, "cache_read_tokens", 0)
		cacheWrite := intOr(req.Params, "cache_creation_tokens", 0)
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		if err := h.store.TrackForkUsage(day, input, output, cacheRead, cacheWrite); err != nil {
			return errorResponse(err.Error())
		}
		return jsonResponse(map[string]any{"status": "ok"})
	case "send_to":
		return h.handleSendTo(req.Params)
	case "whoami":
		return h.handleWhoami(req.Params)
	case "check_channel":
		return h.handleCheckChannel(req.Params)
	case "mark_channel_read":
		return h.handleMarkChannelRead(req.Params)
	case "broadcast":
		return h.handleBroadcast(req.Params)
	case "check_broadcasts":
		return h.handleCheckBroadcasts(req.Params)
	case "scratchpad_write":
		return h.handleScratchpadWrite(req.Params)
	case "scratchpad_read":
		return h.handleScratchpadRead(req.Params)
	case "scratchpad_list":
		return h.handleScratchpadList(req.Params)
	case "scratchpad_delete":
		return h.handleScratchpadDelete(req.Params)
	case "spawn_agent":
		return h.handleSpawnAgent(h.resolveProjectParam(req.Params))
	case "register_agent":
		return h.handleRegisterAgent(req.Params)
	case "update_agent":
		return h.handleUpdateAgent(req.Params)
	case "relay_agent":
		return h.handleRelayAgent(h.resolveProjectParam(req.Params))
	case "stop_agent":
		return h.handleStopAgent(h.resolveProjectParam(req.Params))
	case "stop_all_agents":
		return h.handleStopAllAgents(h.resolveProjectParam(req.Params))
	case "resume_agent":
		return h.handleResumeAgent(h.resolveProjectParam(req.Params))
	case "_track_usage":
		return h.handleTrackUsage(req.Params)
	case "_persist_rate_limits":
		return h.handlePersistRateLimits(req.Params)
	case "list_agents":
		return h.handleListAgents(h.resolveProjectParam(req.Params))
	case "get_agent":
		return h.handleGetAgent(h.resolveProjectParam(req.Params))
	case "update_agent_status":
		return h.handleUpdateAgentStatus(req.Params)
	case "register_pid":
		return h.handleRegisterPID(req.Params)
	case "register_window":
		return h.handleRegisterWindow(req.Params)
	case "fork_extract_learnings":
		return h.handleForkExtractLearnings(req.Params)
	case "fork_set_session_flavor":
		return h.handleForkSetSessionFlavor(req.Params)
	case "fork_evaluate_learning":
		return h.handleForkEvaluateLearning(req.Params)
	case "fork_update_impact":
		return h.handleForkUpdateImpact(req.Params)
	case "fork_resolve_contradiction":
		return h.handleForkResolveContradiction(req.Params)
	case "get_fork_learnings":
		return h.handleGetForkLearnings(req.Params)
	case "reload_vectors":
		if h.vectorStore == nil {
			return errorResponse("vector store not initialized")
		}
		if err := h.vectorStore.Reload(); err != nil {
			return errorResponse(fmt.Sprintf("reload_vectors failed: %v", err))
		}
		return jsonResponse(map[string]any{"status": "ok", "count": h.vectorStore.Count()})

	// Code Intelligence tools
	case "search_code_index":
		return h.handleSearchCodeIndex(req.Params)
	case "search_code":
		return h.handleSearchCode(req.Params)
	case "get_code_context":
		return h.handleGetCodeContext(req.Params)
	case "get_dependency_map":
		return h.handleGetDependencyMap(req.Params)
	case "graph_traverse":
		return h.handleGraphTraverse(req.Params)
	case "get_file_index":
		return h.handleGetFileIndex(req.Params)
	case "get_code_snippet":
		return h.handleGetCodeSnippet(req.Params)
	case "get_file_symbols":
		return h.handleGetFileSymbols(req.Params)

	default:
		return errorResponse(fmt.Sprintf("unknown method: %s", req.Method))
	}
}

// helpers

func jsonResponse(v any) Response {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return errorResponse("internal error")
	}
	return Response{Result: data}
}

func errorResponse(msg string) Response {
	return Response{Error: msg}
}

func stringOr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intOr(m map[string]any, key string, def int) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return def
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}
