package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/bloom"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/indexer"
	"github.com/carsteneu/yesmem/internal/sanitize"
	"github.com/carsteneu/yesmem/internal/storage"
	_ "modernc.org/sqlite" // opencode session tracking
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
	store                 *storage.Store
	bloom                 *bloom.Manager
	dataDir               string        // ~/.claude/yesmem/ — set by daemon after construction
	agentTerminal         string        // preferred terminal for agent windows — set by daemon from config
	agentMaxRuntime       time.Duration // max runtime per agent — set by daemon from config
	scheduler             *Scheduler
	agentMaxTurns         int                // max relay turns per agent — set by daemon from config
	agentMaxDepth         int                // max spawn depth — set by daemon from config
	agentTokenBudget      int                // max tokens per agent — set by daemon from config
	agentDefaultBackend   string             // default agent backend — set by daemon from config
	disableAgentProcesses bool               // test guard; production default remains false
	defaultSandboxProfile SandboxProfile     // default sandbox for scheduled jobs — set by daemon from config
	httpRPCAddr           string             // HTTP API listen address for bun MCP polyfill (e.g. 127.0.0.1:9377)
	httpAuthToken         string             // bearer token for HTTP API authentication
	redactor              sanitize.Sanitizer // optional; nil = passthrough

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

	headlessSessionsMu sync.Mutex
	headlessSessions   map[string]string // jobID -> sessionID (in-memory, lost on restart)

	// Auto-correct rate limiting (T4): per-cap cooldown + cross-tick semaphore.
	// Bash-job auto-correct is rate-limited so a cap with a persistent bug
	// can't burn through the LLM budget. autoCorrectRunning gives mutual
	// exclusion across concurrent ticks; autoCorrectCooldown[cap] holds the
	// timestamp until which further attempts on that cap will be skipped.
	autoCorrectMu       sync.Mutex
	autoCorrectRunning  bool
	autoCorrectCooldown map[string]time.Time

	// Optional: called after mutations. Nil in tests.
	OnMutation func()

	// Optional: LLM client for destillation (set by daemon after client init)
	SummarizeClient extraction.LLMClient

	// Optional: LLM client for quality tasks (rules condensation, narratives)
	QualityClient extraction.LLMClient

	// LLM provider config for on-the-fly client creation (llm_complete RPC)
	LLMProvider         string // e.g. "opencode", "api", "openai"
	LLMCompleteProvider string // override for handleLLMComplete (empty = use LLMProvider)
	LLMAPIKey           string
	LLMBaseURL          string

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

	// OpenCode session scanner for extraction pipeline
	opencodeScanner *indexer.OpencodeScanner

	// Window tracking for xdotool push (session_id → X11 window ID string)
	windowMapMu sync.Mutex
	windowMap   map[string]string
	terminalMap map[string]string // session_id → terminal type (ghostty, gnome-terminal, etc.)

	// Project name resolution cache: input key → resolved full path or
	// ambiguous-marker error. Keys are composite: bare "project" for global
	// (cwd-independent) results and "project\x00cwd" for cwd-specific results.
	projectCacheMu  sync.RWMutex
	projectCache    map[string]string
	projectCacheGen uint64 // incremented on invalidation; guards against stale writes

	// Code graph per project — lazy initialized on first MCP tool access
	codeGraphMu sync.RWMutex
	codeGraphs  map[string]*codeGraphEntry

	// Auto-discovered modelID → providerID map, lazy initialized on first agent spawn.
	// Used by resolveSpawnModel to resolve bare model names ("glm-5.2") to
	// "providerID/modelID" ("zai/glm-5.2"). Coding variants excluded upstream.
	modelProviderMapOnce sync.Once
	modelProviderMap     map[string]string
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
// or short name to the canonical full project path via strict DB lookup. Results
// are cached per session. On unresolved ambiguity (short name matching multiple
// distinct project paths), the error is stored under "_project_error" and surfaced
// as an MCP error response before the handler runs.
func (h *Handler) resolveProjectParam(params map[string]any) map[string]any {
	project, ok := params["project"].(string)
	if !ok || project == "" {
		return params
	}

	// Extract cwd early — it decides whether cached ambiguity errors may be
	// served: ambiguity is cwd-dependent, the tiebreaker may resolve it for
	// a caller standing inside one of the candidate directories.
	cwd, _ := params["_cwd"].(string)

	// Resolve authoritative per-call session context before shared cwd/global
	// cache entries. A resumed process can carry the same stale cwd while serving
	// different same-basename sessions, so session-derived results need their own
	// cache key and must never be shadowed by another caller's cwd entry.
	if !strings.HasPrefix(project, "/") {
		sessionID, _ := params["_session_id"].(string)
		if sessionID == "" {
			if pid, ok := params["_caller_pid"].(float64); ok && pid > 0 {
				sessionID = h.resolveSessionIDFromPID(int(pid))
			}
		}
		if sessionID != "" {
			cacheKey := project + "\x00session\x00" + sessionID
			h.projectCacheMu.RLock()
			generation := h.projectCacheGen
			cached, found := h.projectCache[cacheKey]
			h.projectCacheMu.RUnlock()
			if found {
				params["project"] = cached
				return params
			}

			if session, err := h.store.GetSession(sessionID); err == nil && session != nil && filepath.Base(session.Project) == project {
				h.projectCacheMu.Lock()
				if h.projectCache == nil {
					h.projectCache = make(map[string]string)
				}
				if h.projectCacheGen == generation {
					h.projectCache[cacheKey] = session.Project
				}
				h.projectCacheMu.Unlock()
				params["project"] = session.Project
				return params
			}
		}
	}

	// Check cache. Keys are composite: bare project (global, for results
	// that do not depend on cwd — unique basenames, passthrough, cwd-less
	// ambiguous errors) and project+"\x00"+cwd (cwd-specific, for results
	// produced by cwd-dependent resolution among same-basename candidates).
	//
	// Lookup order: cwd-specific first (scoped hit always returns), then
	// global fallback (returns only for cwd-less callers; cwd-bearing
	// callers with global ambiguous markers fall through to DB).
	h.projectCacheMu.RLock()
	// Snapshot generation under lock — prevents data race with
	// InvalidateProjectCache's projectCacheGen++ write.
	gen := h.projectCacheGen
	// Cwd-specific key: hit here means this exact cwd was already resolved
	// (success or scoped error). Return unconditionally.
	if cwd != "" {
		if cached, ok := h.projectCache[project+"\x00"+cwd]; ok {
			h.projectCacheMu.RUnlock()
			if isAmbiguousMarker(cached) {
				params["_project_error"] = stripMarker(cached)
			} else {
				params["project"] = cached
			}
			return params
		}
	}
	// Global key: deterministic results shared across all callers.
	if cached, ok := h.projectCache[project]; ok {
		h.projectCacheMu.RUnlock()
		if !isAmbiguousMarker(cached) {
			params["project"] = cached
			return params
		}
		if cwd == "" {
			params["_project_error"] = stripMarker(cached)
			return params
		}
		// Global ambiguous marker but cwd present: fall through to
		// DB lookup — the cwd tiebreaker may resolve it.
	} else {
		h.projectCacheMu.RUnlock()
	}

	// DB lookup. Ambiguous short names return *AmbiguousProjectError with
	// all candidate paths. The error is cached via ambiguousMarker so cwd-less
	// callers get a fast error response; cwd-bearing callers bypass the cache
	// and retry the DB lookup to run the cwd tiebreaker.
	//
	// gen was captured under projectCacheMu.RLock above. If invalidation
	// fires during the DB lookup, gen will have advanced and the write
	// below is skipped — preventing stale-data reinsertion.
	resolved, err := h.store.ResolveProjectShortStrict(project, cwd)
	cacheVal := resolved
	if err != nil {
		cacheVal = ambiguousMarker(err.Error())
	}

	// Cache results with composite keys to prevent cross-caller pollution:
	//   - cwd!="":  key = project+"\x00"+cwd (cwd-specific, per-directory)
	//   - cwd=="":  key = project (global, deterministic)
	// Rules for what gets cached:
	//   - Full path       → always cache (deterministic).
	//   - Short, success  → cache (unique basename or passthrough).
	//   - Short, ambiguous, cwd=="" → cache error as global marker (permanent
	//     fact for cwd-less callers; cwd-bearing callers fall through to DB).
	//   - Short, ambiguous, cwd!="" → cache error under cwd-specific key
	//     (scoped: repeated unmatched identical cwd queries/logs once).
	//   - Other errors (transient DB) → never cache.
	isAmbiguous := false
	if err != nil {
		_, isAmbiguous = err.(*storage.AmbiguousProjectError)
	}

	shouldCache := false
	if strings.HasPrefix(project, "/") {
		shouldCache = true
	} else if err == nil {
		shouldCache = true
	} else if isAmbiguous {
		shouldCache = true // global or scoped, depending on key
	}

	if shouldCache {
		cacheKey := project
		if cwd != "" {
			cacheKey = project + "\x00" + cwd
		}
		h.projectCacheMu.Lock()
		if h.projectCache == nil {
			h.projectCache = make(map[string]string)
		}
		// Generation guard: only write if no invalidation happened
		// during the DB lookup. Prevents stale-data reinsertion.
		if h.projectCacheGen == gen {
			h.projectCache[cacheKey] = cacheVal
		}
		h.projectCacheMu.Unlock()
	}

	if err != nil {
		params["_project_error"] = err.Error()
	} else {
		params["project"] = resolved
	}
	return params
}

const ambiguousPrefix = "__ambiguous__:"

func ambiguousMarker(msg string) string { return ambiguousPrefix + msg }
func isAmbiguousMarker(s string) bool   { return strings.HasPrefix(s, ambiguousPrefix) }
func stripMarker(s string) string       { return strings.TrimPrefix(s, ambiguousPrefix) }

// InvalidateProjectCache clears the project resolution cache.
// Call after new sessions are indexed so stale cached resolutions
// (e.g. pre-ambiguity passthrough or stale candidate lists) are
// re-queried from the DB on next lookup. Increments a generation
// counter so concurrent in-flight DB lookups do not reinsert stale
// data after the flush.
func (h *Handler) InvalidateProjectCache() {
	h.projectCacheMu.Lock()
	h.projectCache = nil
	h.projectCacheGen++
	h.projectCacheMu.Unlock()
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
	h := &Handler{store: store, bloom: bloomMgr, pidMap: make(map[string]int), windowMap: make(map[string]string), terminalMap: make(map[string]string), headlessSessions: make(map[string]string), autoCorrectCooldown: make(map[string]time.Time)}
	h.initIdleState()
	return h
}

// SetOpencodeScanner sets the opencode DB scanner for periodic indexing.
func (h *Handler) SetOpencodeScanner(scanner *indexer.OpencodeScanner) {
	h.opencodeScanner = scanner
}

func (h *Handler) initIdleState() {
	h.idleCounters = make(map[string]*idleState)
}

func (h *Handler) handleIdleTick(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		sessionID = "unknown"
	}

	// Periodic opencode DB scanning (every ~5min via MaybeScan rate-limit)
	if h.opencodeScanner != nil {
		if h.opencodeScanner.MaybeScan() > 0 {
			h.InvalidateProjectCache()
		}
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

	// Persist CWD for opencode sessions (not sent in HTTP request body).
	// opencode sends promptCacheKey in API requests — proxy looks up cwd:<session_id>.
	if cwd, _ := req.Params["_cwd"].(string); cwd != "" {
		sid, _ := req.Params["_session_id"].(string)
		sa, _ := req.Params["_source_agent"].(string)
		if sid != "" {
			_ = h.store.SetProxyState("cwd:"+sid, cwd)
		}
		if sid != "" && sa != "" {
			_ = h.store.SetProxyState("source_agent:"+sid, sa)
		}
	}

	// Resolve project parameter once for all methods. Ambiguous short names
	// produce a hard error surfaced before dispatch.
	req.Params = h.resolveProjectParam(req.Params)
	if errStr, ok := req.Params["_project_error"].(string); ok {
		return errorResponse(errStr)
	}

	switch req.Method {
	case "search":
		params := req.Params
		injectExcludeSession(params)
		return h.handleSearch(params)
	case "deep_search":
		params := req.Params
		injectExcludeSession(params)
		return h.handleDeepSearch(params)
	case "remember":
		return h.handleRemember(req.Params)
	case "get_session":
		return h.handleGetSession(req.Params)
	case "list_projects":
		return h.handleListProjects()
	case "project_summary":
		return h.handleProjectSummary(req.Params)
	case "get_learnings":
		return h.handleGetLearnings(req.Params)
	case "get_caps":
		return h.handleGetCaps(req.Params)
	case "save_cap":
		return h.handleSaveCap(req.Params)
	case "register_caps":
		return h.handleRegisterCaps(req.Params)
	case "activate_cap":
		return h.handleActivateCap(req.Params)
	case "deactivate_cap":
		// No project scope: activations are keyed on (thread_id, name).
		return h.handleDeactivateCap(req.Params)
	case "execute_cap":
		return h.handleExecuteCap(req.Params)
	case "get_active_caps":
		// Internal: called by the proxy via RPC, not exposed as an MCP tool.
		return h.handleGetActiveCaps(req.Params)
	case "cap_store":
		return h.handleCapStore(req.Params)
	case "cap_proposal_decide":
		return h.handleCapProposalDecide(req)
	case "list_cap_proposals":
		return h.handleListCapProposals(req)
	case "query_facts":
		return h.handleQueryFacts(req.Params)
	case "related_to_file":
		return h.handleRelatedToFile(req.Params)
	case "get_coverage":
		return h.handleGetCoverage(req.Params)
	case "get_project_profile":
		return h.handleGetProjectProfile(req.Params)
	case "get_self_feedback":
		return h.handleGetSelfFeedback(req.Params)
	case "set_persona":
		return h.handleSetPersona(req.Params)
	case "get_persona":
		return h.handleGetPersona()
	case "resolve":
		return h.handleResolve(req.Params)
	case "resolve_by_text":
		return h.handleResolveByText(req.Params)
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
	case "record_repl_pattern":
		// Internal: called by the proxy via RPC, not exposed as an MCP tool.
		return h.handleRecordReplPattern(req.Params)
	case "record_turn_sequence":
		// Internal: called by the proxy via RPC, not exposed as an MCP tool.
		return h.handleRecordTurnSequence(req.Params)
	case "get_repl_pattern_suggestion":
		// Internal: called by the proxy via RPC, not exposed as an MCP tool.
		return h.handleGetReplPatternSuggestion(req.Params)
	case "dismiss_repl_pattern":
		return h.handleDismissReplPattern(h.resolveProjectParam(req.Params))
	case "dismiss_code_nav":
		sessionID, _ := req.Params["session_id"].(string)
		if sessionID == "" {
			return errorResponse("session_id required")
		}
		if err := h.store.DismissCodeNav(sessionID); err != nil {
			return errorResponse(err.Error())
		}
		return jsonResponse(map[string]any{"status": "ok", "session_id": sessionID})
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
	case "list_docs":
		return h.handleListDocs(h.resolveProjectParam(req.Params))
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
	case "scratchpad_append":
		return h.handleScratchpadAppend(req.Params)
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
	case "track_stream_state":
		return h.handleTrackStreamState(req.Params)
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
	case "list_registrations":
		return h.handleListRegistrations(req.Params)
	case "open_agent_terminal":
		return h.handleOpenAgentTerminal(req.Params)
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

	case "schedule":
		return h.handleSchedule(req.Params)

	case "llm_complete":
		return h.handleLLMComplete(req.Params)

	default:
		return errorResponse(fmt.Sprintf("unknown method: %s", req.Method))
	}
}

func (h *Handler) handleLLMComplete(params map[string]any) Response {
	model, _ := params["model"].(string)
	system, _ := params["system"].(string)
	prompt, _ := params["prompt"].(string)
	sessionID := stringOr(params, "session", "")
	tools, _ := params["tools"].(bool)

	if system == "" && prompt == "" {
		return errorResponse("system or prompt required")
	}

	client := h.QualityClient
	if client == nil {
		client = h.SummarizeClient
	}
	provider := h.LLMProvider
	if h.LLMCompleteProvider != "" {
		provider = h.LLMCompleteProvider
	}
	if model != "" && provider != "" {
		if client == nil || client.Model() != model || client.Name() != provider {
			mc, err := extraction.NewLLMClient(provider, h.LLMAPIKey, model, "", h.LLMBaseURL)
			if err == nil && mc != nil {
				client = mc
			}
		}
	}
	if client == nil {
		return errorResponse("llm client not initialized — missing config or API key")
	}

	// Track opencode session creation: before the call, record the latest
	// session timestamp. After the call, check for a new one.
	var beforeTS int64
	if sessionID == "" && provider == "opencode" {
		beforeTS = opencodeLatestSessionTS()
	}

	// For opencode provider without an explicit system prompt, inject a
	// minimal instruction to prevent the LLM from making tool calls.
	// The proxy injects CUSTOM-SYSTEM which advertises yesmem MCP tools;
	// without this guard the LLM uses tools → zero text events.
	injectedSystem := system
	if provider == "opencode" && injectedSystem == "" {
		injectedSystem = "You are a plain text completion API with no tools. You cannot call any functions. Respond with text directly — never use a tool."
	}

	var opts []extraction.CallOption
	// Generate a proxy session ID for openai_compatible calls
	// so the proxy injects SYSTEM.md and MCP context.
	if provider == "openai_compatible" || provider == "openai" {
		if sessionID == "" {
			sessionID = randomSessionID()
		}
	}
	if sessionID != "" {
		opts = append(opts, extraction.WithSession(sessionID))
	}
	// Default to enabling MCP tools for opencode subprocess calls. The Telegram
	// cap and other llm-builtin callers send no tools flag but expect tool
	// access for sensible replies. Explicit tools=false still wins.
	if provider == "opencode" && !tools {
		tools = true
	}
	if tools {
		opts = append(opts, extraction.WithTools())
	}
	result, err := client.Complete(injectedSystem, prompt, opts...)
	if err != nil {
		return errorResponse(fmt.Sprintf("llm call failed: %v", err))
	}

	resp := map[string]any{"result": result}

	// Return the opencode session ID on every call so callers
	// can always update their stored session for resume.
	if provider == "opencode" {
		if sessionID != "" {
			resp["session_id"] = sessionID
		} else {
			newID := opencodeSessionAfter(beforeTS)
			if newID != "" {
				resp["session_id"] = newID
			}
		}
	}

	return jsonResponse(resp)
}

// opencodeLatestSessionTS returns the max time_created from the opencode session table.
func opencodeLatestSessionTS() int64 {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[opencode-session] home dir error: %v", err)
		return 0
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Printf("[opencode-session] open error (latest): %v", err)
		return 0
	}
	defer db.Close()
	var ts int64
	err = db.QueryRow("SELECT IFNULL(MAX(time_created),0) FROM session").Scan(&ts)
	if err != nil {
		log.Printf("[opencode-session] query/scan error (latest): %v", err)
		return 0
	}
	log.Printf("[opencode-session] latest TS: %d", ts)
	return ts
}

// opencodeSessionAfter returns the newest session ID created after the given timestamp.
func opencodeSessionAfter(afterTS int64) string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[opencode-session] home dir error: %v", err)
		return ""
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Printf("[opencode-session] open error: %v", err)
		return ""
	}
	defer db.Close()
	var id string
	err = db.QueryRow("SELECT id FROM session WHERE time_created > ? ORDER BY time_created DESC LIMIT 1", afterTS).Scan(&id)
	if err != nil {
		log.Printf("[opencode-session] query/scan error (afterTS=%d): %v", afterTS, err)
		return ""
	}
	return id
}

func randomSessionID() string {
	return fmt.Sprintf("yesmem-%d", time.Now().UnixNano())
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
