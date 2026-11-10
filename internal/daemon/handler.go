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
	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/indexer"
	"github.com/carsteneu/yesmem/internal/sanitize"
	"github.com/carsteneu/yesmem/internal/storage"
	_ "modernc.org/sqlite" // opencode session tracking
)

func timeNow() time.Time { return time.Now() }

// SetAutoCorrectClient wires the client the failed-bash-job pass asks. The
// daemon constructs it in its async startup goroutine, which runs while the
// heartbeat already ticks, so the write is guarded — the pass has to be able to
// tell "not wired yet" from "wired" without a data race.
func (h *Handler) SetAutoCorrectClient(c extraction.LLMClient) {
	h.autoCorrectClientMu.Lock()
	h.autoCorrectClient = c
	h.autoCorrectClientMu.Unlock()
}

// autoCorrectLLM returns the auto-correct client, or nil while it is not wired.
func (h *Handler) autoCorrectLLM() extraction.LLMClient {
	h.autoCorrectClientMu.Lock()
	defer h.autoCorrectClientMu.Unlock()
	return h.autoCorrectClient
}

// llmWiring is the LLM client set and the provider routing the daemon
// publishes once at startup.
//
// The publishing goroutine (daemon.go) runs while RPC handlers and the periodic
// jobs — rules refresh, staleness tick, llm_complete — already read. Every
// access goes through llmMu: llm_complete must see the clients and the routing
// as one consistent set, and a client is an interface value (two words), so an
// unguarded reader can catch a write in flight and see a mismatched pair.
type llmWiring struct {
	summarize        extraction.LLMClient
	quality          extraction.LLMClient
	commitEval       extraction.LLMClient
	provider         string // e.g. "opencode", "api", "openai"
	completeProvider string // override for llm_complete (empty = use provider)
	baseURL          string
	overrideClient   func(provider, model string) (extraction.LLMClient, error)
}

// SetSummarizeClient wires the lightweight client for rules condensation and
// doc destillation.
func (h *Handler) SetSummarizeClient(c extraction.LLMClient) {
	h.llmMu.Lock()
	h.llm.summarize = c
	h.llmMu.Unlock()
}

// SetQualityClient wires the client for quality tasks (rules condensation,
// narratives, llm_complete default).
func (h *Handler) SetQualityClient(c extraction.LLMClient) {
	h.llmMu.Lock()
	h.llm.quality = c
	h.llmMu.Unlock()
}

// SetCommitEvalClient wires the client for commit-triggered staleness work.
func (h *Handler) SetCommitEvalClient(c extraction.LLMClient) {
	h.llmMu.Lock()
	h.llm.commitEval = c
	h.llmMu.Unlock()
}

// SetLLMRouting publishes the provider settings of the llm_complete RPC and the
// factory for a call that overrides model or provider. The factory is built by
// the daemon from newDaemonLLMClient with the same tracker, api gate and
// per-call cap as its own paths, so an override cannot bypass the budget.
func (h *Handler) SetLLMRouting(provider, completeProvider, baseURL string, override func(provider, model string) (extraction.LLMClient, error)) {
	h.llmMu.Lock()
	h.llm.provider = provider
	h.llm.completeProvider = completeProvider
	h.llm.baseURL = baseURL
	h.llm.overrideClient = override
	h.llmMu.Unlock()
}

// llmSnapshot returns the published wiring as one consistent set.
func (h *Handler) llmSnapshot() llmWiring {
	h.llmMu.RLock()
	defer h.llmMu.RUnlock()
	return h.llm
}

// summarizeLLM returns the summarize client, or nil while it is not wired.
func (h *Handler) summarizeLLM() extraction.LLMClient {
	h.llmMu.RLock()
	defer h.llmMu.RUnlock()
	return h.llm.summarize
}

// qualityLLMClient returns the quality client, or nil while it is not wired.
func (h *Handler) qualityLLMClient() extraction.LLMClient {
	h.llmMu.RLock()
	defer h.llmMu.RUnlock()
	return h.llm.quality
}

// commitEvalLLM returns the staleness client, or nil while it is not wired.
func (h *Handler) commitEvalLLM() extraction.LLMClient {
	h.llmMu.RLock()
	defer h.llmMu.RUnlock()
	return h.llm.commitEval
}

// onMutation triggers post-mutation side effects (MEMORY.md regeneration).
// No-op when OnMutation is nil (e.g. in tests).
func (h *Handler) onMutation() {
	if h.OnMutation != nil {
		h.OnMutation()
	}
}

// embedWiring is the vector-search wiring the daemon publishes while requests
// are already running: the indexer, the vector store and the indexing provider
// come from the async startup goroutine, the dedicated query provider from a
// second one, the IVF path is read on shutdown, and the queue is created when
// the embed worker starts.
//
// Every access goes through embedMu. hybrid_search, vector_search, the docs
// removal path, reload_vectors and SaveIVF all read this set, and a provider is
// an interface value (two words), so an unguarded reader can catch a write in
// flight. Without the guard the race detector reports a DATA RACE on ivfPath
// and on the provider fields (YM-432).
type embedWiring struct {
	indexer        *embedding.Indexer
	vectorStore    *embedding.VectorStore
	embedProvider  embedding.Provider
	searchProvider embedding.Provider
	ivfPath        string
	queue          chan embedJob
}

// Handler processes socket requests using the daemon's resources.
type Handler struct {
	store                       *storage.Store
	bloom                       *bloom.Manager
	dataDir                     string        // ~/.claude/yesmem/ — set by daemon after construction
	capsDir                     string        // CAP.md directory the daemon imports from — set by daemon from config
	agentTerminal               string        // preferred terminal for agent windows — set by daemon from config
	agentMaxRuntime             time.Duration // max runtime per agent — set by daemon from config
	scheduler                   *Scheduler
	agentMaxTurns               int                // max relay turns per agent — set by daemon from config
	agentMaxDepth               int                // max spawn depth — set by daemon from config
	agentTokenBudget            int                // max tokens per agent — set by daemon from config
	agentDefaultBackend         string             // default agent backend — set by daemon from config
	agentPermissionKick         bool               // kick agents stuck in permission dialogs via double-Enter — set by daemon from config
	agentPermissionKickDelay    time.Duration      // stream-idle before first kick — set by daemon from config
	agentPermissionKickInterval time.Duration      // re-kick interval after first kick — set by daemon from config
	agentThemes                 map[string]string  // agent type → TUI theme — set by daemon from config (agents.themes)
	disableAgentProcesses       bool               // test guard; production default remains false
	defaultSandboxProfile       SandboxProfile     // default sandbox for scheduled jobs — set by daemon from config
	sandboxDefaultSet           bool               // config carried a default_sandbox_profile (none is a valid value)
	proxyForwardPort            int                // proxy loopback port relayed into a 2.x sandbox — set by daemon from config (proxy.listen)
	httpRPCAddr                 string             // HTTP API listen address for bun MCP polyfill (e.g. 127.0.0.1:9377)
	httpAuthToken               string             // bearer token for HTTP API authentication
	redactor                    sanitize.Sanitizer // optional; nil = passthrough
	ocDBPath                    string             // opencode session DB for Phase 5 evidence checks — set by daemon from config

	// doneVerify is the DONE gate's verification configuration. Nil means the
	// documented default, which is enabled.
	doneVerify   *config.DoneVerifyConfig
	doneVerifyMu sync.Mutex
	// verifySlot admits one verification at a time: the DONE gate no longer runs
	// one, but two direct verify_claims calls would otherwise run two test suites
	// side by side.
	verifySlot chan struct{}

	// Optional: vector search, published at startup through the setters in
	// handler_hybrid.go (guarded by embedMu, see embedWiring)
	embedMu sync.RWMutex
	embed   embedWiring

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

	// Runs that are in flight, keyed the way their working directory is keyed.
	// The directory sweep must not remove a live process's cwd, and a job row
	// deleted during its run leaves no other trace of it.
	activeRunsMu sync.Mutex
	activeRuns   map[string]int

	// Auto-correct rate limiting (T4): per-cap cooldown + cross-tick semaphore.
	// Bash-job auto-correct is rate-limited so a cap with a persistent bug
	// can't burn through the LLM budget. autoCorrectRunning gives mutual
	// exclusion across concurrent ticks; autoCorrectCooldown[cap] holds the
	// timestamp until which further attempts on that cap will be skipped.
	autoCorrectMu       sync.Mutex
	autoCorrectRunning  bool
	autoCorrectCooldown map[string]time.Time

	// bashErrorsMu/bashErrorsRunning keep one failed-bash-job pass in flight at
	// a time now that the heartbeat dispatches it to its own goroutine.
	bashErrorsMu      sync.Mutex
	bashErrorsRunning bool

	// startedAt is when this handler was built, i.e. when its daemon started. It
	// bounds how long the failed-bash-job pass waits for the async wiring of the
	// auto-correct client before it reports failures undiagnosed.
	startedAt time.Time

	// Optional: called after mutations. Nil in tests.
	OnMutation func()

	// LLM wiring — clients and provider routing, published once at startup
	// through the setters above (guarded by llmMu, see llmWiring).
	llmMu sync.RWMutex
	llm   llmWiring

	// AutoCorrectClient answers the bash-job diagnosis and repair prompts. It is
	// wired through SetAutoCorrectClient with the shared budget, gate and per-call
	// cap, so the loop is not a second, undecorated LLM path. Nil while the daemon
	// is still starting (and in tests): the pass then leaves its rows queued.
	autoCorrectClientMu sync.Mutex
	autoCorrectClient   extraction.LLMClient

	// autoCorrectVerify runs a proposed fix once before it is persisted. It is
	// nil in production — autoCorrectBashCap then builds the sandbox and runs the
	// body there. Tests inject a stub so the persist paths can be exercised
	// without ai-jail; nil-without-sandbox still fails closed.
	autoCorrectVerify func(body string, timeoutSec int, profile SandboxProfile) (int, error)

	// Embed subprocess lifecycle (Option B: killed on daemon shutdown, restarts fresh)
	embedProcessMu sync.Mutex
	embedProcess   *os.Process

	// Active session tracking — set by proxy via set_active_session RPC
	activeSessionMu sync.Mutex
	opencodeBind    bindThrottle // on-demand opencode session binding (opencode_bind.go)
	activeSessionID string

	// Reporting of guessed identities (resolveActiveSessionFallback): one line
	// per guess and window, so the last-writer-wins fallback stays visible
	// without flooding the log on every request.
	guessedSessionMu   sync.Mutex
	guessedSessionLast map[string]time.Time

	// PID tracking for stdin injection (session_id → OS PID of claude process)
	pidMapMu sync.Mutex
	pidMap   map[string]int

	// OpenCode session scanner for extraction pipeline
	opencodeScanner *indexer.OpencodeScanner

	// Window tracking (session_id → X11 window ID string), fed by register_window
	// from the opencode TUI plugin; read-only, used for agent window placement
	// and whoami. Persisted in dataDir/windows.json (window_persist.go).
	windowMapMu sync.Mutex
	windowMap   map[string]string
	terminalMap map[string]string // session_id → terminal type (ghostty, gnome-terminal, etc.)
	// windowSaveMu serialises writes of windows.json.
	windowSaveMu sync.Mutex
	// windowPid / windowSeq: owner pid of the registered window (0 = unknown)
	// and registration order, both guarded by windowMapMu; lazily created.
	windowPid     map[string]int
	windowSeq     map[string]int64
	windowNextSeq int64
	// liveWindows lists the mapped windows (err: can't tell). Injected via
	// Config.LiveWindows because internal/terminals imports this package.
	liveWindows func() ([]LiveWindow, error)

	// Project name resolution cache: input key → resolved full path or
	// ambiguous-marker error. Keys are composite: bare "project" for global
	// (cwd-independent) results and "project\x00cwd" for cwd-specific results.
	projectCacheMu  sync.RWMutex
	projectCache    map[string]string
	projectCacheGen uint64 // incremented on invalidation; guards against stale writes

	// Code graph per project — lazy initialized on first MCP tool access
	codeGraphMu sync.RWMutex
	codeGraphs  map[string]*codeGraphEntry
	// codeScanner overrides the scanner used to build a project graph. It exists
	// so tests can exercise build and failure paths without the CBM binary.
	codeScanner func() codescan.Scanner
}

// recentLearning holds a recently remembered learning with its ID for injection.
type recentLearning struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// injectExcludeSession copies the caller's own session into exclude_session for
// search handlers. This prevents the self-referential echo loop where a session
// finds its own messages in search results. expand_context deliberately does NOT
// use this — finding own-session messages is its purpose.
//
// An id the MCP layer took from the shared service environment names another
// session (see envDerivedSessionUntrusted), so it must not be excluded; the
// caller's own session is then taken from its working directory (agentByCallerCWD),
// which is what resolveSessionID does for the same layout. Dropping the exclusion
// instead would hand the caller its own learnings back.
func (h *Handler) injectExcludeSession(params map[string]any) {
	if h.envDerivedSessionUntrusted(params) {
		if agent := h.agentByCallerCWD(params); agent != nil && agent.SessionID != "" {
			params["exclude_session"] = agent.SessionID
		}
		return
	}
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

	// finish stamps the unresolved state on every return path, cached or fresh: a
	// caller that got its short name back (or nothing at all) must not have it
	// persisted, and the cache would otherwise hand out the value without this
	// marker on the second call (YM-300, cold review F1).
	finish := func() map[string]any {
		if strings.HasPrefix(project, "/") {
			return params
		}
		if _, hasErr := params["_project_error"]; hasErr {
			return params
		}
		if resolved, _ := params["project"].(string); resolved == "" || resolved == project {
			params["_project_unresolved"] = project
		}
		return params
	}

	// Resolve authoritative per-call session context before shared cwd/global
	// cache entries. A resumed process can carry the same stale cwd while serving
	// different same-basename sessions, so session-derived results need their own
	// cache key and must never be shadowed by another caller's cwd entry.
	if !strings.HasPrefix(project, "/") {
		sessionID, _ := params["_session_id"].(string)
		// A leaked environment id names another session, and a PID that belongs
		// to no agent names the last session that registered — neither may decide
		// this caller's project (see envDerivedSessionUntrusted, callerPIDIsAgent).
		if h.envDerivedSessionUntrusted(params) {
			sessionID = ""
		}
		if sessionID == "" && h.callerPIDIsAgent(params) {
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
				return finish()
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
				return finish()
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
			return finish()
		}
	}
	// Global key: deterministic results shared across all callers.
	if cached, ok := h.projectCache[project]; ok {
		h.projectCacheMu.RUnlock()
		if !isAmbiguousMarker(cached) {
			// A passthrough cached by a cwd-less caller must not answer a caller that
			// brought a cwd: the directory is exactly what turns the name into a
			// project, and the cached value would keep that branch from running
			// (YM-300, cold review F1).
			if cwd == "" || cached != project || strings.HasPrefix(cached, "/") {
				params["project"] = cached
				return finish()
			}
		} else if cwd == "" {
			params["_project_error"] = stripMarker(cached)
			return finish()
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
		if !strings.HasPrefix(project, "/") && (resolved == "" || resolved == project) {
			// Nothing resolved: the value is either the name handed back or empty.
			// Nothing may be written under a basename — a row keyed on one is read by
			// no project once the real path appears (YM-300). Handlers that persist
			// the value read this marker and refuse with the name the caller sent.
			params["_project_unresolved"] = project
		}
	}
	return finish()
}

const ambiguousPrefix = "__ambiguous__:"

// handlesUnresolvedProject names the methods that deal with a name nothing
// resolves themselves instead of taking it as a project: remember attributes the
// row to the caller's repository (attributeLearningProject), so the central
// refusal would take that fallback away. Every other method is refused, because
// an empty project would drop its filter and read across all projects.
func handlesUnresolvedProject(method string) bool {
	return method == "remember"
}

// unresolvedProjectResponse is the one wording for a short name that names no
// project: it quotes what the caller sent, because that is the only handle an
// operator has on the case (YM-300).
func unresolvedProjectResponse(name string) Response {
	return errorResponse(fmt.Sprintf("project %q is not a known project: no session carries that name. Pass the project's absolute path", name))
}

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

// newProductionHandler assembles the handler the daemon serves on its socket.
// It is separate from NewHandler because the extra wiring (MEMORY.md regen on
// mutation) must stay out of tests.
func newProductionHandler(store *storage.Store, bloomMgr *bloom.Manager, claudeProjects string) *Handler {
	h := NewHandler(store, bloomMgr)
	wireMutationHook(h, store, claudeProjects)
	return h
}

// NewHandler creates a request handler with access to all daemon resources.
func NewHandler(store *storage.Store, bloomMgr *bloom.Manager) *Handler {
	h := &Handler{store: store, bloom: bloomMgr, pidMap: make(map[string]int), windowMap: make(map[string]string), terminalMap: make(map[string]string), headlessSessions: make(map[string]string), activeRuns: make(map[string]int), autoCorrectCooldown: make(map[string]time.Time), startedAt: time.Now(), verifySlot: make(chan struct{}, 1)}
	h.initIdleState()
	return h
}

// SetOpencodeScanner sets the opencode DB scanner for periodic indexing.
func (h *Handler) SetOpencodeScanner(scanner *indexer.OpencodeScanner) {
	h.opencodeScanner = scanner
}

// SetOpencodeDBPath sets the opencode session DB path used by the Phase 5
// DONE-guard evidence check.
func (h *Handler) SetOpencodeDBPath(dbPath string) {
	h.ocDBPath = dbPath
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

	// Persist the source agent for opencode sessions (not sent in HTTP request
	// body): the briefing reads source_agent:<session> to identify a non-Claude
	// caller. The working directory is not persisted: the proxy takes it from the
	// request, and _cwd reaches the agent resolution directly, so a cwd:<session>
	// row had no reader and grew one row per session (YM-307).
	if cwd, _ := req.Params["_cwd"].(string); cwd != "" {
		sid, _ := req.Params["_session_id"].(string)
		sa, _ := req.Params["_source_agent"].(string)
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
	// A short name nothing resolves names no project. A name that resolved to
	// nothing at all (a cwd was given and does not carry it) is refused centrally:
	// no handler may read unscoped because the filter vanished (YM-300). The
	// cwd-less passthrough keeps working for the CLI's short-name flow; of the
	// handlers that store under that value, the two broadcast handlers refuse it
	// themselves (see unresolvedProjectResponse). Other writers keep their previous
	// short-name behaviour and are out of this ticket's scope.
	if unresolved, ok := req.Params["_project_unresolved"].(string); ok && unresolved != "" {
		if resolved, _ := req.Params["project"].(string); resolved == "" && !handlesUnresolvedProject(req.Method) {
			return unresolvedProjectResponse(unresolved)
		}
	}

	switch req.Method {
	case "search":
		params := req.Params
		h.injectExcludeSession(params)
		return h.handleSearch(params)
	case "deep_search":
		params := req.Params
		h.injectExcludeSession(params)
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
	case "resolve_by_commit":
		return h.handleResolveByCommit(req.Params)
	case "reopen":
		return h.handleReopen(req.Params)
	case "quarantine_session":
		return h.handleQuarantineSession(req.Params)
	case "unquarantine_session":
		return h.handleUnquarantineSession(req.Params)
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
		h.injectExcludeSession(params)
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
	case "guard_check":
		// The opencode plugin's judgement call. Everything about it lives in internal/guard, the same core the
		// Claude Code hook runs: the plugin only wires the event and renders the answer.
		return h.handleGuardCheck(req.Params)
	case "verify_claims":
		// The verification as an operation: a report in, a judgement out. The
		// DONE gate is one caller; a harness or a human may be another.
		return h.handleVerifyClaims(req.Params)
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
	case "is_code_nav_dismissed":
		// Read side of dismiss_code_nav: the opencode plugin blocks in its own
		// process and cannot see the counter, so it asks here before it blocks
		// again (YM-425).
		sessionID, _ := req.Params["session_id"].(string)
		if sessionID == "" {
			return errorResponse("session_id required")
		}
		return jsonResponse(map[string]any{
			"session_id": sessionID,
			"dismissed":  h.store.CodeNavDismissed(sessionID),
		})
	case "expand_context":
		return h.handleExpandContext(req.Params)
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
		vecStore := h.VectorStore()
		if vecStore == nil {
			return errorResponse("vector store not initialized")
		}
		if err := vecStore.Reload(); err != nil {
			return errorResponse(fmt.Sprintf("reload_vectors failed: %v", err))
		}
		return jsonResponse(map[string]any{"status": "ok", "count": vecStore.Count()})

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

	wiring := h.llmSnapshot()
	client := wiring.quality
	if client == nil {
		client = wiring.summarize
	}
	provider := wiring.provider
	if wiring.completeProvider != "" {
		provider = wiring.completeProvider
	}
	if model != "" && provider != "" {
		if client == nil || client.Model() != model || client.Name() != provider {
			if wiring.overrideClient == nil {
				log.Printf("[llm_complete] model override ignored: no metered client factory wired")
			} else if mc, err := wiring.overrideClient(provider, model); err != nil {
				log.Printf("[llm_complete] model override failed: %v", err)
			} else if mc != nil {
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
	// llm_complete is a boundary: the prompt comes from an arbitrary caller (a
	// cap, the llm() primitive), so the redactor is applied here regardless of
	// how the client behind it was wired upstream.
	client = h.sanitizeClient(client)

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
