package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tokenizer "github.com/qhenkart/anthropic-tokenizer-go"
)

// Config holds proxy configuration.
type Config struct {
	ListenAddr     string // e.g. ":9099"
	TargetURL      string // e.g. "https://api.anthropic.com"
	TokenThreshold        int            // trigger stubbing above this estimated token count
	TokenMinimumThreshold int            // stub down to this floor
	TokenThresholds       map[string]int // model-specific thresholds: {"opus": 180000, "haiku": 130000}
	KeepRecent     int    // number of messages to always keep unmodified
	DataDir        string // yesmem data directory for DB access
	OpenAITargetURL string // upstream for OpenAI-format clients; if empty, uses TargetURL

	// Signal reflection
	SignalsEnabled     bool   // enable async signal reflection calls
	SignalsMode        string // "reflection" = separate API call
	SignalsEveryNTurns int    // every N end_turn responses
	SignalsModel       string // full model ID, e.g. "claude-sonnet-4-6"
	APIKey             string // needed for reflection API calls

	// Sawtooth cache optimization
	SawtoothEnabled bool   // use sawtooth instead of progressive decay (default: true)
	CacheTTL        string // "ephemeral" (5m) or "1h" (extended)

	// Usage deflation: scale down input_tokens reported to CC to suppress "Context low" warning.
	// 0 = disabled, 0.7 = report 70% of actual tokens. Real values kept for internal tracking.
	UsageDeflationFactor float64

	// System prompt rewriting
	PromptUngate  bool   // strip "may or may not be relevant" disclaimer from CLAUDE.md injection
	PromptRewrite bool   // strip "Output efficiency" + "short and concise", inject Ant-quality directives
	PromptEnhance bool   // CLAUDE.md authority reinforcement, comment discipline, persona-based tone
	EffortFloor   string // minimum effort level: "low", "medium", "high", "max" (empty = off)

	// Cache keepalive
	CacheKeepaliveEnabled bool
	CacheKeepaliveMode    string // "auto", "5m", "1h"
	CacheKeepalivePings5m int
	CacheKeepalivePings1h int

	// Forked agents
	ForkedAgentsEnabled            bool
	ForkedAgentsModel              string // full model ID
	ForkedAgentsTokenGrowthTrigger int
	ForkedAgentsMaxFailures        int
	ForkedAgentsMaxForksPerSession int
	ForkedAgentsDebug              bool

	// Cache state management
	ResetCache bool // clear persisted frozen stubs and decay state on startup
}

const maxAnnotations = 5000 // evict oldest when exceeded

// ANSI color codes for log output
const (
	colorReset      = "\033[0m"
	colorDim        = "\033[2m"  // gray — passthrough / no stubbing
	colorGreen      = "\033[32m" // stubbed / compressed
	colorLightGreen = "\033[92m" // FINAL without compression
	colorBlue       = "\033[34m" // injection
	colorOrange     = "\033[33m" // skip / retry
	colorRed        = "\033[31m" // errors
)

// Server is the infinite-thread proxy.
type Server struct {
	cfg        Config
	httpClient *http.Client
	logger     *log.Logger
	requestIdx atomic.Int64

	// Post-hoc annotations: tool_use_id → first ~120 chars of Claude's response
	mu          sync.RWMutex
	annotations map[string]string

	// Progressive decay tracker
	decay *DecayTracker

	// Living narrative block
	narrative *Narrative

	// Pivot moments cache (from daemon)
	pivotMu     sync.RWMutex
	pivotTexts  []string
	pivotCached time.Time

	// Aggregate stats (Task #2)
	stats *ProxyStats

	// Local tokenizer for accurate token counting
	tokenizer *tokenizer.Tokenizer

	// Cached overhead tokens (system + tools), set on first request per thread
	overheadMu       sync.RWMutex
	overheadTokens   int // unused, kept for interface compat

	// Hysteresis state for stubbing (Task #6)
	stubActive atomic.Bool

	// Daemon connection state — first successful connect flips to true
	daemonReady atomic.Bool

	// Retry detection (Task #7)
	retryMu         sync.RWMutex
	lastFingerprint string

	// Session start time for temporal annotation in archive blocks
	sessionStartMu   sync.RWMutex
	sessionStartTime time.Time

	// Runtime config override (set via MCP set_config tool)
	configOverrideMu         sync.RWMutex
	tokenThresholdOverrides  map[string]int // model-key → threshold, "" = global fallback

	// Briefing cache — loaded once per thread, injected as user/assistant message pair
	briefingMu   sync.RWMutex
	briefingText string

	// Cognitive signal bus — routes _signal_* tool calls to handlers
	signalBus *SignalBus

	// Inline reflection: track which learning IDs were injected per thread
	lastInjectedIDsMu   sync.Mutex
	lastInjectedIDs     map[string]map[int64]string // threadID → id → source ("briefing"|"associative"|"fresh")
	sessionInjectCounts map[string]map[int64]int    // threadID → learningID → injection count this session
	lastTurnInjected    map[string]map[int64]bool   // threadID → learningIDs injected in previous turn

	// Self-priming anchors: threadID → last self-prime text
	selfPrimeMu sync.RWMutex
	selfPrimes  map[string]string

	// Timestamp tracking: threadID → time of last response completion
	responseTsMu sync.RWMutex
	responseTimes map[string]time.Time

	// Think reminder: per-thread request counter (replaces hook-think file-based counter)
	thinkMu       sync.Mutex
	thinkCounters map[string]int // threadID → request count

	channelMu         sync.Mutex
	channelInjectCount map[string]int // sessionID → injection turn count

	// Prompt cache gating — enables cache_control breakpoints when requests are frequent
	cacheGate *CacheGate

	// Sawtooth cache optimization
	frozenStubs       *FrozenStubs
	sawtoothTrigger   *SawtoothTrigger
	cacheStatusWriter *CacheStatusWriter
	cacheKeepalive    *CacheKeepalive
	cacheTTLDetector  *CacheTTLDetector

	// Cumulative token savings from collapsing (per threadID)
	rawSavingsMu sync.Mutex
	rawSavings   map[string]int

	// Skill hint tracking: per-thread active skills
	skillTracker *skillHintTracker

	// Forked agent state: per-thread token growth + failure tracking
	forkState   *ForkState
	forkConfigs []ForkConfig

	// Rules re-injection: condensed CLAUDE.md rules injected every ~40k tokens
	rulesMu                sync.RWMutex
	rulesBlock             string         // cached condensed rules (fetched once from daemon)
	rulesTokenCount        map[string]int // threadID → tokens since last rules injection
	rulesCollapseInjected  map[string]bool // threadID → true if collapse already injected rules (reset by normal inject)
	msgCounters      *msgCounters   // global per-thread msg counter, persists across collapses

	// Loop detection: per-thread warning state
	loopMu     sync.Mutex
	loopStates map[string]*LoopState // threadID → state
}

// Run starts the proxy server and blocks until interrupted.
func Run(cfg Config) error {
	s := &Server{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute,
			Transport: &http.Transport{
				TLSClientConfig:       &tls.Config{},
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				DisableCompression:    true,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
		logger:          createLogger(cfg.DataDir),
		annotations:     make(map[string]string),
		decay:           NewDecayTracker(),
		narrative:       NewNarrative(),
		stats:           &ProxyStats{startTime: time.Now()},
		selfPrimes:        make(map[string]string),
		lastInjectedIDs:     make(map[string]map[int64]string),
		sessionInjectCounts: make(map[string]map[int64]int),
		lastTurnInjected:    make(map[string]map[int64]bool),
		responseTimes:     make(map[string]time.Time),
		thinkCounters:      make(map[string]int),
		channelInjectCount: make(map[string]int),
		cacheGate:       NewCacheGate(cacheGapForTTL(cfg.CacheTTL)),
		frozenStubs:       NewFrozenStubsWithTTL(sawtoothTTLForCacheTTL(cfg.CacheTTL)),
		sawtoothTrigger:   NewSawtoothTrigger(cacheGapForTTL(cfg.CacheTTL), cfg.TokenThreshold, cfg.TokenMinimumThreshold),
		cacheStatusWriter: NewCacheStatusWriter(cfg.DataDir, cfg.CacheTTL, cfg.TokenThreshold, cfg.TokenMinimumThreshold),
		cacheTTLDetector:  NewCacheTTLDetectorWithPersist(cfg.DataDir),
		skillTracker:      newSkillHintTracker(),
		rulesTokenCount:       make(map[string]int),
		rulesCollapseInjected: make(map[string]bool),
		msgCounters:       newMsgCounters(),
		loopStates:        make(map[string]*LoopState),
		forkState:   NewForkState(cfg.ForkedAgentsTokenGrowthTrigger, cfg.ForkedAgentsMaxFailures, cfg.ForkedAgentsMaxForksPerSession),
		forkConfigs: []ForkConfig{},
	}

	// Log persisted detection state
	if sup := s.cacheTTLDetector.Is1hSupported(); sup != nil {
		s.logger.Printf("[proxy] loaded persisted TTL detection: 1h_supported=%v", *sup)
		if *sup && s.frozenStubs != nil {
			s.frozenStubs.UpdateTTL(sawtoothTTLForCacheTTL("1h"))
		}
	}

	if cfg.CacheKeepaliveEnabled {
		s.cacheKeepalive = NewCacheKeepalive(CacheKeepaliveConfig{
			Target:   cfg.TargetURL,
			Mode:     cfg.CacheKeepaliveMode,
			Pings5m:  cfg.CacheKeepalivePings5m,
			Pings1h:  cfg.CacheKeepalivePings1h,
			Detector: s.cacheTTLDetector,
			Logger:   s.logger,
			OnPing: func(threadID string, cacheRead, cacheWrite int) {
				s.cacheTTLDetector.RecordResponse(cacheRead, cacheWrite, 0)
				if s.frozenStubs != nil {
					s.frozenStubs.Touch(threadID)
				}
				if s.sawtoothTrigger != nil {
					s.sawtoothTrigger.TouchRequestTime(threadID)
				}
			},
		})
	}

	// Wire detector + keepalive to status writer for terminal display
	s.cacheStatusWriter.SetDetector(s.cacheTTLDetector)
	if s.cacheKeepalive != nil {
		s.cacheStatusWriter.SetKeepalive(s.cacheKeepalive)
	}

	// Register forked agent configs
	if cfg.ForkedAgentsEnabled {
		s.forkConfigs = append(s.forkConfigs, NewExtractAndEvaluateConfig(cfg.ForkedAgentsModel))
		modelDesc := cfg.ForkedAgentsModel
		if modelDesc == "" {
			modelDesc = "(same as main thread)"
		}
		s.logger.Printf("[proxy] forked agents enabled: %d configs, model=%s, trigger=%dk tokens",
			len(s.forkConfigs), modelDesc, cfg.ForkedAgentsTokenGrowthTrigger/1000)
	}

	// Wire sawtooth token persistence via daemon RPC
	s.sawtoothTrigger.SetPersistFunc(func(key, value string) {
		if _, err := s.queryDaemon("set_proxy_state", map[string]any{"key": key, "value": value}); err != nil {
			s.logger.Printf("[sawtooth] persist failed for %s: %v", key, err)
		}
	})
	s.sawtoothTrigger.SetLoadFunc(func(key string) (string, bool) {
		result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": key})
		if err != nil || result == nil {
			return "", false
		}
		var resp struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(result, &resp) != nil || resp.Value == "" {
			return "", false
		}
		return resp.Value, true
	})

	// Wire frozen stubs persistence via same daemon RPC
	s.frozenStubs.SetPersistFunc(func(key, value string) {
		if _, err := s.queryDaemon("set_proxy_state", map[string]any{"key": key, "value": value}); err != nil {
			s.logger.Printf("[frozen] persist failed for %s: %v", key, err)
		}
	})
	s.frozenStubs.SetLoadFunc(func(key string) (string, bool) {
		result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": key})
		if err != nil || result == nil {
			return "", false
		}
		var resp struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(result, &resp) != nil || resp.Value == "" {
			return "", false
		}
		return resp.Value, true
	})

	// Wire decay tracker persistence via same daemon RPC
	s.decay.SetPersistFunc(func(key, value string) {
		if _, err := s.queryDaemon("set_proxy_state", map[string]any{"key": key, "value": value}); err != nil {
			s.logger.Printf("[decay] persist failed for %s: %v", key, err)
		}
	})
	s.decay.SetLoadFunc(func(key string) (string, bool) {
		result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": key})
		if err != nil || result == nil {
			return "", false
		}
		var resp struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(result, &resp) != nil || resp.Value == "" {
			return "", false
		}
		return resp.Value, true
	})

	// Clear persisted cache state if --reset-cache was passed
	if cfg.ResetCache {
		for _, prefix := range []string{"frozen:", "decay:"} {
			result, err := s.queryDaemon("delete_proxy_state_prefix", map[string]any{"prefix": prefix})
			if err != nil {
				s.logger.Printf("[reset-cache] failed to delete %s*: %v", prefix, err)
			} else {
				var resp struct{ Deleted int }
				json.Unmarshal(result, &resp)
				s.logger.Printf("[reset-cache] deleted %d %s* entries", resp.Deleted, prefix)
			}
		}
	}

	// Init signal bus with daemon RPC as callback
	s.signalBus = NewSignalBus(s.logger)
	registerSignalHandlers(s.signalBus, s.logger, s.queryDaemon, func(threadID, anchor string) {
		s.selfPrimeMu.Lock()
		s.selfPrimes[threadID] = anchor
		s.selfPrimeMu.Unlock()
	})

	// Init local tokenizer (BPE-based, ~60ms)
	tok, err := tokenizer.New()
	if err != nil {
		s.logger.Printf("%s[proxy] tokenizer init failed, falling back to heuristic: %v%s", colorOrange, err, colorReset)
	} else {
		s.tokenizer = tok
		s.logger.Printf("[proxy] tokenizer initialized")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleRequest)

	srv := &http.Server{
		Addr:    sanitizeListenAddr(cfg.ListenAddr),
		Handler: mux,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-done
		s.logger.Println("shutting down (draining in-flight requests)...")
		if s.cacheKeepalive != nil {
			s.cacheKeepalive.Stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			s.logger.Printf("graceful shutdown failed: %v, forcing close", err)
			srv.Close()
		}
	}()

	s.logger.Printf("listening on %s → %s (threshold=%d, keepRecent=%d, model_thresholds=%v)",
		sanitizeListenAddr(cfg.ListenAddr), cfg.TargetURL, cfg.TokenThreshold, cfg.KeepRecent, cfg.TokenThresholds)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// effectiveTokenThreshold returns the runtime token threshold for a given model.
// Priority: MCP override (model-specific) > MCP override (global) > config model-specific > config global.
func (s *Server) effectiveTokenThreshold(model string) int {
	s.configOverrideMu.RLock()
	overrides := s.tokenThresholdOverrides
	s.configOverrideMu.RUnlock()
	if len(overrides) > 0 && model != "" {
		lower := strings.ToLower(model)
		for key, threshold := range overrides {
			if key != "" && strings.Contains(lower, strings.ToLower(key)) && threshold > 0 {
				return threshold
			}
		}
	}
	// Global override fallback (key "")
	if len(overrides) > 0 {
		if globalOverride, ok := overrides[""]; ok && globalOverride > 0 {
			return globalOverride
		}
	}
	if model != "" && len(s.cfg.TokenThresholds) > 0 {
		lower := strings.ToLower(model)
		for key, threshold := range s.cfg.TokenThresholds {
			if strings.Contains(lower, strings.ToLower(key)) {
				return threshold
			}
		}
	}
	return s.cfg.TokenThreshold
}

// SetTokenThresholdOverride sets a runtime override for the token threshold per model key.
// Pass threshold=0 to reset the override for that key. Key "" is the global fallback.
func (s *Server) SetTokenThresholdOverride(modelKey string, threshold int) {
	s.configOverrideMu.Lock()
	if s.tokenThresholdOverrides == nil {
		s.tokenThresholdOverrides = make(map[string]int)
	}
	if threshold <= 0 {
		delete(s.tokenThresholdOverrides, modelKey)
	} else {
		s.tokenThresholdOverrides[modelKey] = threshold
	}
	s.configOverrideMu.Unlock()
}

// refreshConfigOverrides loads runtime config overrides from daemon proxy_state.
// Checks session-specific override first (by threadID), then falls back to global.
func (s *Server) refreshConfigOverrides(threadID string) {
	// Try session-specific first
	if threadID != "" {
		result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": "config_override:token_threshold:" + threadID})
		if err == nil && result != nil {
			var resp struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(result, &resp) == nil && resp.Value != "" {
				if v, err := strconv.Atoi(resp.Value); err == nil && v > 0 {
					s.SetTokenThresholdOverride("", v)
					return
				}
			}
		}
	}
	// Fall back to global
	result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": "config_override:token_threshold"})
	if err != nil || result == nil {
		return
	}
	var resp struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(result, &resp) == nil && resp.Value != "" {
		if v, err := strconv.Atoi(resp.Value); err == nil && v > 0 {
			s.SetTokenThresholdOverride("", v)
		}
	}
}

// queryDaemon opens a fresh unix socket connection per call (thread-safe, no import cycle).
// ensureFrozenPersisted checks if frozen stubs for a thread are in the DB.
// If missing (e.g. daemon was unreachable after deploy), re-persists them.
// Runs asynchronously to avoid blocking the request path.
func (s *Server) ensureFrozenPersisted(threadID string) {
	go func() {
		key := "frozen:" + threadID
		result, err := s.queryDaemon("get_proxy_state", map[string]any{"key": key})
		if err != nil {
			return // daemon still unreachable, will retry next request
		}
		var resp struct{ Value string }
		if json.Unmarshal(result, &resp) == nil && resp.Value != "" {
			return // already persisted
		}
		// Not in DB — trigger re-persist from in-memory state
		s.frozenStubs.Persist(threadID)
		s.decay.Persist(threadID)
		s.logger.Printf("[frozen] re-persisted stubs for %s (was missing from DB)", threadID)
	}()
}

func (s *Server) queryDaemon(method string, params map[string]any) (json.RawMessage, error) {
	if s.cfg.DataDir == "" {
		return nil, fmt.Errorf("no data dir configured")
	}
	sockPath := filepath.Join(s.cfg.DataDir, "daemon.sock")

	// On cold start (daemon not yet confirmed ready), retry longer to survive daemon restart
	maxAttempts := 3
	if !s.daemonReady.Load() {
		maxAttempts = 30 // 30 x 500ms = 15s max wait for daemon startup
	}

	var conn net.Conn
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		conn, err = net.DialTimeout("unix", sockPath, 3*time.Second)
		if err == nil {
			s.daemonReady.Store(true)
			break
		}
		if attempt < maxAttempts-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// JSON-RPC style request/response (matches daemon.SocketServer protocol)
	type request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params,omitempty"`
	}
	type response struct {
		Result json.RawMessage `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
	}

	if err := json.NewEncoder(conn).Encode(request{Method: method, Params: params}); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("recv: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("daemon: %s", resp.Error)
	}
	return resp.Result, nil
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if isResponsesPath(r) {
		s.handleResponses(w, r)
		return
	}
	if isOpenAIPath(r) {
		s.handleOpenAICompletions(w, r)
		return
	}
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/messages") &&
		!strings.Contains(r.URL.Path, "/batches") &&
		!strings.Contains(r.URL.Path, "/count_tokens") {
		s.handleMessages(w, r)
		return
	}
	s.passthrough(w, r)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	reqIdx := int(s.requestIdx.Add(1))

	// Track session start time (first request sets it)
	s.sessionStartMu.Lock()
	if s.sessionStartTime.IsZero() {
		s.sessionStartTime = time.Now()
	}
	s.sessionStartMu.Unlock()

	// Sawtooth: periodic TTL eviction of frozen stubs
	if s.cfg.SawtoothEnabled && reqIdx%50 == 0 {
		if n := s.frozenStubs.Evict(); n > 0 {
			s.logger.Printf("[req %d] SAWTOOTH: evicted %d stale frozen stubs", reqIdx, n)
		}
	}

	// Task #9: Bypass — skip all proxy logic
	if isBypassed(r.Header) {
		s.logger.Printf("%s[req %d] bypass active, passthrough%s", colorDim, reqIdx, colorReset)
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadGateway)
			return
		}
		s.forwardRaw(w, r, body)
		return
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		s.logger.Printf("[req %d] read body error: %v", reqIdx, err)
		http.Error(w, "failed to read request body", http.StatusBadGateway)
		return
	}

	// Parse JSON
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		s.logger.Printf("%s[req %d] JSON parse error, passing through: %v%s", colorDim, reqIdx, err, colorReset)
		s.forwardRaw(w, r, body)
		return
	}

	messages, ok := req["messages"].([]any)
	if !ok {
		s.forwardRaw(w, r, body)
		return
	}

	req["messages"] = messages

	// Use session_id directly as thread ID (unique per CC session).
	// Prefer X-Claude-Code-Session-Id header (CC v2.1.86+), fallback to body metadata.
	threadID := extractSessionID(req, r.Header.Get("X-Claude-Code-Session-Id"))
	if threadID == "" {
		threadID = DeriveThreadID(req)
	}
	proj := extractProjectName(req)
	model, _ := req["model"].(string)

	// Refresh runtime config overrides from daemon (per-session or global)
	s.refreshConfigOverrides(threadID)

	// Track whether req has been modified and needs re-serialization
	needsReserialization := false

	// Message count at request entry (used for actual-based token estimation)
	msgCount := len(messages)

	// Log message count + cache breakpoint positions for diagnostics
	s.logger.Printf("req %d: %d messages total", reqIdx, msgCount)
	logCacheBreakpointLocations(req, s.logger)

	// Cache bug mitigations (CC Bun fork issues):
	// Bug 1: Normalize cch= sentinel patterns in message content to prevent
	// Bun's string replacement from corrupting the wrong occurrence.
	if SanitizeBillingSentinel(messages) {
		s.logger.Printf("[req %d] CACHE: sanitized billing sentinel in message content", reqIdx)
		needsReserialization = true
	}
	// Bug 2: Normalize system[0] billing hash to a fixed value so the cache
	// prefix stays stable across fresh/resume session boundaries.
	if NormalizeBillingHeader(req) {
		s.logger.Printf("[req %d] CACHE: normalized billing header cch= hash", reqIdx)
		needsReserialization = true
	}

	// prompt_ungate: strip the CLAUDE.md subordination disclaimer so user instructions carry full authority.
	if s.cfg.PromptUngate {
		if StripCLAUDEMDDisclaimer(req) {
			s.logger.Printf("[req %d] SYSTEM: stripped CLAUDE.md disclaimer", reqIdx)
			needsReserialization = true
		}
	}

	// prompt_rewrite: strip output-throttling directives, rewrite quality caps, inject Ant-quality directives
	if s.cfg.PromptRewrite {
		if StripOutputEfficiency(req) {
			s.logger.Printf("[req %d] REWRITE: stripped Output efficiency section", reqIdx)
			needsReserialization = true
		}
		if StripToneBrevity(req) {
			s.logger.Printf("[req %d] REWRITE: stripped 'short and concise' from Tone", reqIdx)
			needsReserialization = true
		}
		if RewriteGoldPlating(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten gold-plating directive", reqIdx)
			needsReserialization = true
		}
		if RewriteErrorHandling(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten error handling directive", reqIdx)
			needsReserialization = true
		}
		if RewriteThreeLinesRule(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten three-lines rule", reqIdx)
			needsReserialization = true
		}
		if RewriteSubagentCompleteness(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten subagent completeness", reqIdx)
			needsReserialization = true
		}
		if RewriteExploreAgentSpeed(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten explore agent speed bias", reqIdx)
			needsReserialization = true
		}
		if RewriteSubagentCodeSuppression(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten subagent code suppression", reqIdx)
			needsReserialization = true
		}
		if RewriteScopeMatching(req) {
			s.logger.Printf("[req %d] REWRITE: rewritten scope matching", reqIdx)
			needsReserialization = true
		}
		InjectAntDirectives(req)
		s.logger.Printf("[req %d] REWRITE: injected Ant-quality directives", reqIdx)
		needsReserialization = true
	}

	// prompt_enhance: CLAUDE.md authority + comment discipline + persona tone
	if s.cfg.PromptEnhance {
		InjectCLAUDEMDAuthority(req)
		if raw, err := s.queryDaemon("get_persona", nil); err == nil {
			var persona map[string]any
			if json.Unmarshal(raw, &persona) == nil {
				if traits, ok := persona["traits"].(map[string]any); ok {
					if verbosity, ok := traits["verbosity"].(string); ok {
						InjectPersonaTone(req, verbosity)
					}
				}
			}
		}
		s.logger.Printf("[req %d] ENHANCE: injected authority + persona tone", reqIdx)
		needsReserialization = true
	}

	// effort_floor: ensure output_config.effort is at least the configured minimum
	if s.cfg.EffortFloor != "" {
		if EnforceEffortFloor(req, s.cfg.EffortFloor) {
			s.logger.Printf("[req %d] EFFORT: enforced floor=%s", reqIdx, s.cfg.EffortFloor)
			needsReserialization = true
		}
	}

	// Inject identity hint on first message of a thread so Claude knows the briefing is from itself.
	if len(messages) <= 1 {
		AppendSystemBlock(req, "yesmem-identity", "The first user message contains your briefing — written by yourself, from your memory. This is not an external document. This is you, remembering.")
		needsReserialization = true
	}

	// Token estimation: actual-based (lastActual + delta) or full count fallback
	overhead := s.measureOverhead(req)
	totalTokens := s.estimateTotalTokens(threadID, messages, overhead)

	// Task #7: Retry detection — skip side-effects on retries
	fp := requestFingerprint(messages)
	isRetryReq := s.isRetry(fp)
	if isRetryReq {
		s.logger.Printf("%s[req %d %s tid=%s] retry detected, skipping side-effects%s", colorOrange, reqIdx, proj, threadID, colorReset)
	}

	// Increment project turn counter (async, non-blocking, skip on retries)
	if !isRetryReq {
		turnProject := proj
		if turnProject == "" {
			turnProject = "__global__"
		}
		go s.queryDaemon("increment_turn", map[string]any{"project": turnProject})
	}

	// Extract user query once (used by re-expansion and associative retrieval)
	userQuery := lastUserText(messages)

	// Extract tool_use_ids for post-hoc annotation (passed as parameter, no shared state)
	toolUseIDs := extractToolUseIDs(messages)

	// === Inline Reflection: scan previous assistant response for signals ===
	s.lastInjectedIDsMu.Lock()
	prevInjected := s.lastInjectedIDs[threadID]
	s.lastInjectedIDsMu.Unlock()

	if len(prevInjected) > 0 && !isRetryReq {
		signals := scanAssistantSignals(messages)

		// Signal 1: Learning Used — use_count (all sources)
		var confirmedUsed []int64
		for _, id := range signals.UsedIDs {
			if _, ok := prevInjected[id]; ok {
				confirmedUsed = append(confirmedUsed, id)
			}
		}
		if len(confirmedUsed) > 0 {
			idsAny := make([]any, len(confirmedUsed))
			for i, id := range confirmedUsed {
				idsAny[i] = id
			}
			go s.queryDaemon("increment_use", map[string]any{"ids": idsAny})
		}

		// Signal 2: Noise — only for associative and fresh injections.
		// Briefing learnings are injected every turn, so "not used this turn"
		// is expected and not noise. Only contextual injections that were
		// ignored are real noise signals.
		usedSet := make(map[int64]bool, len(confirmedUsed))
		for _, id := range confirmedUsed {
			usedSet[id] = true
		}
		var noiseIDs []int64
		for id, source := range prevInjected {
			if usedSet[id] {
				continue
			}
			if source == "associative" || source == "fresh" {
				noiseIDs = append(noiseIDs, id)
			}
			// briefing: skip — not being used in one turn is expected
		}
		if len(noiseIDs) > 0 {
			idsAny := make([]any, len(noiseIDs))
			for i, id := range noiseIDs {
				idsAny[i] = id
			}
			go s.queryDaemon("increment_noise", map[string]any{"ids": idsAny})
		}

		// Signal 3: Error Feedback — fail_count on used learnings when tool errors occurred.
		// If Claude referenced a learning and the resulting action still failed,
		// the learning didn't prevent the error. Only bump on USED ids (not all injected).
		if signals.HasToolErrors && len(confirmedUsed) > 0 {
			failAny := make([]any, len(confirmedUsed))
			for i, id := range confirmedUsed {
				failAny[i] = id
			}
			go s.queryDaemon("increment_fail", map[string]any{"ids": failAny})
			if s.logger != nil {
				s.logger.Printf("[inline-reflection] error feedback: %d used learnings → fail_count++", len(confirmedUsed))
			}
		}

		// Signal 4: Knowledge Gaps
		for _, topic := range signals.GapTopics {
			go s.queryDaemon("track_gap", map[string]any{"topic": topic, "project": proj})
		}

		// Signal 3: Contradictions
		for _, desc := range signals.Contradictions {
			go s.queryDaemon("flag_contradiction", map[string]any{"description": desc, "project": proj, "thread_id": threadID})
		}

		if len(confirmedUsed) > 0 || len(signals.GapTopics) > 0 || len(signals.Contradictions) > 0 {
			s.logger.Printf("[req %d %s tid=%s] inline-reflection: injected=%d used=%d noise=%d gaps=%d contradictions=%d",
				reqIdx, proj, threadID, len(prevInjected), len(confirmedUsed), len(noiseIDs), len(signals.GapTopics), len(signals.Contradictions))
		}
	}

	// Commit detection: check for git commit in tool_result, trigger async stale-learning evaluation
	if ci := detectGitCommit(messages); ci != nil {
		workdir := extractWorkingDirectory(req)
		s.logger.Printf("[req %d %s tid=%s] git commit detected: %s %s", reqIdx, proj, threadID, ci.Hash, ci.Message)
		go s.queryDaemon("invalidate_on_commit", map[string]any{
			"hash": ci.Hash, "project": proj, "workdir": workdir,
		})
	}

	// Loop detection: check for repeating tool-call patterns and inject warning
	if threadID != "" && !isRetryReq {
		s.loopMu.Lock()
		loopState := s.loopStates[threadID]
		if loopState == nil {
			loopState = &LoopState{}
			s.loopStates[threadID] = loopState
		}
		s.loopMu.Unlock()

		if warning, level := CheckLoopAndFormat(messages, loopState); warning != "" {
			AppendSystemBlock(req, "yesmem-loop-warning", warning)
			needsReserialization = true
			s.logger.Printf("%s[req %d %s tid=%s] LOOP: level %d warning injected%s", colorOrange, reqIdx, proj, threadID, level, colorReset)
		}
	}

	// Task #4: Subagent detection — skip all proxy logic for short conversations
	if isSubagent(messages, req) {
		// Inject docs hint for subagents if reference docs are indexed
		if hint := s.getDocsHint(proj); hint != "" {
			modified := injectDocsHintForSubagent(messages, hint)
			req["messages"] = modified
			if newBody, err := json.Marshal(req); err == nil {
				body = newBody
			}
			s.logger.Printf("%s[req %d %s tid=%s] %d messages (subagent, docs-hint injected)%s", colorOrange, reqIdx, proj, threadID, len(messages), colorReset)
		} else {
			s.logger.Printf("%s[req %d %s tid=%s] %d messages (subagent, passthrough)%s", colorOrange, reqIdx, proj, threadID, len(messages), colorReset)
		}
		s.forwardWithAnnotation(w, r, body, reqIdx, toolUseIDs, proj, threadID, msgCount)
		return
	}

	// === Sawtooth Cache Optimization ===
	// When enabled: use frozen stubs between stub-cycles for cache hits.
	// When disabled: fall back to progressive decay on every request above threshold.

	if s.cfg.SawtoothEnabled {
		// Restore decay state from DB on cold start (before frozen stubs use it)
		s.decay.LoadFromDB(threadID)

		// Check for existing frozen stubs first
		frozen := s.frozenStubs.Get(threadID, messages)
		if frozen != nil {
			// Estimate combined token count: frozen prefix + fresh tail + overhead
			freshMessages := messages[frozen.Cutoff:]
			freshTokens := s.countMessageTokens(freshMessages)
			combinedTokens := frozen.Tokens + freshTokens + overhead

			if combinedTokens > s.effectiveTokenThreshold(model) ||
				s.sawtoothTrigger.GetLastTokens(threadID) > s.effectiveTokenThreshold(model) {
				// Fresh tail grew too large — invalidate and re-stub
				s.logger.Printf("[req %d %s tid=%s] SAWTOOTH: frozen prefix expired (%dk frozen + %dk fresh + %dk overhead = %dk > %dk threshold)",
					reqIdx, proj, threadID, frozen.Tokens/1000, freshTokens/1000, overhead/1000, combinedTokens/1000, s.effectiveTokenThreshold(model)/1000)
				s.frozenStubs.Invalidate(threadID)
				frozen = nil // fall through to trigger check below
			} else {
				// Use frozen prefix + fresh tail
				combined := make([]any, 0, len(frozen.Messages)+len(freshMessages))
				combined = append(combined, frozen.Messages...)
				combined = append(combined, freshMessages...)
				req["messages"] = combined
				// Eager-stub large tool_results in the fresh tail (uncached → zero cache cost)
				beforeEager := s.countMessageTokens(combined)
				combined = EagerStubToolResults(combined, len(frozen.Messages), s.countTokens)
				afterEager := s.countMessageTokens(combined)
				if beforeEager != afterEager {
					s.logger.Printf("[req %d %s tid=%s] EAGER-STUB: %dk → %dk tokens (saved %dk)",
						reqIdx, proj, threadID, beforeEager/1000, afterEager/1000, (beforeEager-afterEager)/1000)
				}
				req["messages"] = combined
				needsReserialization = true
				s.logger.Printf("%s[req %d %s tid=%s] SAWTOOTH: frozen prefix (%d stubs, %dk msg) + %d fresh (%dk msg) + %dk overhead = %dk total%s",
					colorGreen, reqIdx, proj, threadID, len(frozen.Messages), frozen.Tokens/1000, len(freshMessages), freshTokens/1000, overhead/1000, combinedTokens/1000, colorReset)
				rawMsgTokens := s.countMessageTokens(messages)
				s.setRawEstimate(threadID, rawMsgTokens+overhead)
				s.logger.Printf("[req %d %s tid=%s] RAW ESTIMATE: msgTokens=%dk + overhead=%dk = %dk (messages=%d)", reqIdx, proj, threadID, rawMsgTokens/1000, overhead/1000, (rawMsgTokens+overhead)/1000, len(messages))
				if InjectFrozenStubCacheBreakpoint(req, len(frozen.Messages)) {
					s.logger.Printf("[req %d %s tid=%s] SAWTOOTH: frozen stub cache breakpoint at messages[%d]",
						reqIdx, proj, threadID, len(frozen.Messages)-1)
				}

				// Ensure frozen stubs are persisted (may have failed on first request after deploy)
				s.ensureFrozenPersisted(threadID)
			}
		}
		if frozen == nil {
			// No frozen stubs — check trigger
			triggerReason := s.sawtoothTrigger.ShouldTrigger(threadID, totalTokens)
			if triggerReason != TriggerNone {
				s.logger.Printf("[req %d %s tid=%s] SAWTOOTH stub-cycle triggered: %s (tokens=%dk)",
					reqIdx, proj, threadID, triggerReason, totalTokens/1000)

				// Refresh briefing during stub-cycle — disabled (briefing now via SessionStart hook)
				// s.refreshBriefing(proj)
				// if s.briefingText != "" {
				// 	UpsertSystemBlockCached(req, "yesmem-briefing", s.briefingText)
				// }

				// Run full stub pipeline
				s.setRawEstimate(threadID, totalTokens)
				_ = s.runStubCycle(messages, req, reqIdx, proj, threadID, overhead, totalTokens, userQuery, isRetryReq)

				// Freeze the result
				finalMessages, _ := req["messages"].([]any)
				if finalMessages != nil && len(finalMessages) > 0 {
					// Boundary = last message of original that maps to last stub
					cutoff := len(messages)
					var boundaryMsg any
					if cutoff > 0 {
						boundaryMsg = messages[cutoff-1]
					}
					frozenTokens := s.countMessageTokens(finalMessages)
					s.frozenStubs.Store(threadID, finalMessages, cutoff, boundaryMsg, frozenTokens, totalTokens)
					s.decay.Persist(threadID)
					s.logger.Printf("[req %d %s tid=%s] SAWTOOTH: frozen %d messages at cutoff=%d (~%dk tokens)",
						reqIdx, proj, threadID, len(finalMessages), cutoff, frozenTokens/1000)
					if InjectFrozenStubCacheBreakpoint(req, len(finalMessages)) {
						s.logger.Printf("[req %d %s tid=%s] SAWTOOTH: frozen stub cache breakpoint at messages[%d]",
							reqIdx, proj, threadID, len(finalMessages)-1)
					}
				}
				needsReserialization = true
			} else {
				// No trigger, no frozen stubs — pass through (early conversation)
				// Eager-stub large tool_results even before first stub cycle
				beforeEager := s.countMessageTokens(messages)
				messages = EagerStubToolResults(messages, 0, s.countTokens)
				afterEager := s.countMessageTokens(messages)
				if beforeEager != afterEager {
					s.logger.Printf("[req %d %s tid=%s] EAGER-STUB: %dk → %dk tokens (saved %dk)",
						reqIdx, proj, threadID, beforeEager/1000, afterEager/1000, (beforeEager-afterEager)/1000)
				}
				req["messages"] = messages
				s.logger.Printf("%s[req %d %s tid=%s] %d messages, ~%dk tokens (sawtooth: no trigger)%s",
					colorDim, reqIdx, proj, threadID, len(messages), totalTokens/1000, colorReset)
			}
		}
	} else {
		// Legacy path: progressive decay
		if s.shouldStub(totalTokens, model) {
			s.setRawEstimate(threadID, totalTokens)
			_ = s.runStubCycle(messages, req, reqIdx, proj, threadID, overhead, totalTokens, userQuery, isRetryReq)
			needsReserialization = true
		}
	}

	// Associative context injection (for ALL requests, not just stubbed ones)
	// Re-enabled with higher thresholds (55/75, max 1) after disabling Fresh Remember.
	assocContext := s.findAssociativeContextFor(userQuery, proj, threadID, messages)
	if assocContext != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, assocContext, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] associative context injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}

	// Doc-chunk context injection: separate search path for indexed docs (not skills)
	docContext := s.findDocContextFor(userQuery, proj, messages)
	if docContext != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, docContext, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] doc context injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}

	// Fresh Remember Injection: DISABLED
	// Causes echo-loop: Claude saves via remember(), sees it again next turn as
	// [yesmem fresh memory], reacts to own output instead of user's request.
	// Recovery after /clear is handled by recovery.go (briefing), not here.
	// Subagents get their own briefing. No remaining use-case.
	// remItems := s.popRecentRemember()
	var remItems []recentLearningItem

	// Rules re-injection: inject condensed CLAUDE.md rules every ~40k output tokens
	if rulesBlock := s.rulesInject(threadID, totalTokens, proj); rulesBlock != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, s.formatRulesReminder(rulesBlock, proj), s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] rules reminder injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}

	// Plan auto-activation: nudge Claude to call set_plan() when reading a plan file
	if planFile := detectPlanFileRead(messages); planFile != "" {
		if activePlan, _ := s.getActivePlan(threadID); shouldNudgePlan(planFile, activePlan) {
			AppendSystemBlock(req, "plan-nudge",
				fmt.Sprintf("Du hast einen Implementierungsplan gelesen (%s). Aktiviere ihn jetzt via set_plan() damit Plan-Checkpoints funktionieren und dein Fortschritt getrackt wird.", filepath.Base(planFile)))
			needsReserialization = true
			s.logger.Printf("%s[req %d %s tid=%s] plan nudge injected for %s%s", colorBlue, reqIdx, proj, threadID, filepath.Base(planFile), colorReset)
		}
	}

	// Plan checkpoint: inject plan reminder every ~20k tokens if active plan exists
	s.detectPlanToolCall(messages, threadID, totalTokens)
	if checkpoint := s.planCheckpointInject(threadID, totalTokens); checkpoint != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, checkpoint, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] plan checkpoint injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}

	// Docs hint injection: inject docs-available reminder every ~10k tokens (independent of plan)
	if docsHint := s.docsHintInject(threadID, totalTokens, proj); docsHint != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, docsHint, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] docs hint injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}
	if len(remItems) > 0 {
		var lines []string
		for _, item := range remItems {
			if item.ID > 0 {
				lines = append(lines, fmt.Sprintf("- [ID:%d] %s", item.ID, truncateStr(item.Text, 200)))
			} else {
				lines = append(lines, "- "+truncateStr(item.Text, 200))
			}
		}
		remContext := fmt.Sprintf("[yesmem fresh memory]\nGerade gelernt:\n%s\n%s\n[/yesmem fresh memory]", strings.Join(lines, "\n"), InlineReflectionHint)
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, remContext, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] fresh remember injected (%d items)%s", colorBlue, reqIdx, proj, threadID, len(remItems), colorReset)
	}

	// Skill eval injection: mandatory evaluation instruction for real user input only
	if threadID != "" && proj != "" && isUserInputTurn(messages) {
		// First: detect any skill activations from message history (updates tracker)
		s.syncSkillActivations(messages, proj, threadID)

		// Inject mandatory skill evaluation block into last user message
		skillEval := buildSkillEvalBlock()
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		req["messages"] = injectAssociativeContext(currentMessages, skillEval, s.cfg.SawtoothEnabled)
		needsReserialization = true
		s.logger.Printf("%s[req %d %s tid=%s] skill-eval injected%s", colorBlue, reqIdx, proj, threadID, colorReset)
	}

	// === Collect injected learning IDs for next turn's inline reflection ===
	if threadID != "" {
		currentIDs := make(map[int64]string) // id → source
		// Source 1: Briefing learnings (cached, same every turn)
		s.briefingMu.RLock()
		briefingSnapshot := s.briefingText
		s.briefingMu.RUnlock()
		if briefingSnapshot != "" {
			for _, m := range idPattern.FindAllStringSubmatch(briefingSnapshot, -1) {
				if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					currentIDs[id] = "briefing"
				}
			}
		}
		// Source 2: Associative context (contextual, per-turn)
		if assocContext != "" {
			for _, m := range idPattern.FindAllStringSubmatch(assocContext, -1) {
				if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					currentIDs[id] = "associative" // overwrites briefing if both — associative is the stronger signal
				}
			}
		}
		// Source 3: Fresh remember items (just learned)
		for _, item := range remItems {
			if item.ID > 0 {
				currentIDs[item.ID] = "fresh"
			}
		}
		s.lastInjectedIDsMu.Lock()
		if len(currentIDs) > 0 {
			// Accumulate IDs across turns — don't overwrite.
			// Claude may reference an ID from turn 30 in turn 50.
			existing := s.lastInjectedIDs[threadID]
			if existing == nil {
				existing = make(map[int64]string)
			}
			for id, source := range currentIDs {
				existing[id] = source
			}
			s.lastInjectedIDs[threadID] = existing
		}
		s.lastInjectedIDsMu.Unlock()
	}

	// Think reminder injection — only for real user turns, not tool continuations
	if threadID != "" {
		currentMessages, _ := req["messages"].([]any)
		if currentMessages == nil {
			currentMessages = messages
		}
		if lastUserHasText(currentMessages) {
			thinkReminder := s.buildThinkReminder(threadID, threadID, false)
			if thinkReminder != "" {
				req["messages"] = injectAssociativeContext(currentMessages, thinkReminder, s.cfg.SawtoothEnabled)
				needsReserialization = true
			}
		}

		// Dialog injection: append at end of messages with pseudo-padding for alternation.
		// Always works regardless of tool_result chains or conversation state.
		dr := s.checkDialogMessages(threadID, proj)
		if dr.Extra != "" {
			// Prepend session ID for agent-to-agent communication
			dr.Extra = fmt.Sprintf("DEINE_SESSION_ID: %s\n", threadID) + dr.Extra
			injected := false
			if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
				// Pad with empty assistant if last message is user (maintains alternation)
				lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
				if lastMsg != nil && lastMsg["role"] == "user" {
					msgs = append(msgs, map[string]any{
						"role":    "assistant",
						"content": "​", // zero-width space — invisible padding
					})
				}
				// Append dialog as new user message (becomes the last message Claude sees)
				msgs = append(msgs, map[string]any{
					"role":    "user",
					"content": "<channel source=\"yesmem-dialog\">\n" + dr.Extra + "\n</channel>",
				})
				req["messages"] = msgs
				needsReserialization = true
				injected = true
			}
			// Mark as read after 3 injection turns — long enough for Claude to process,
			// resilient against fast ping-pong sessions (time-based was broken for <30s turns).
			if injected && dr.HasUnread && dr.SessionID != "" {
				if s.shouldMarkChannelRead(dr) {
					s.markDialogRead(dr)
				}
			}
			s.logger.Printf("[dialog] injection injected=%v extra_len=%d", injected, len(dr.Extra))
		}
	}

	// Signal tools are NOT injected into the main request.
	// They are collected via a separate async reflection call after the response.
	// See fireReflectionCall() in reflection.go.
	// NEVER put _signal_* tools back here — the model ignores them among 44+ real tools.

	// Metadata injection: timestamp, message index, time-delta for messages after cache breakpoint.
	// Messages before the breakpoint are cached — modifying them would break the cache.
	if threadID != "" {
		if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
			now := time.Now()
			nowStr := shortWeekday(now.Weekday()) + " " + now.Format("2006-01-02 15:04:05")

			// Find last cache_control breakpoint in messages
			cacheIdx := -1
			for i, m := range msgs {
				msg, _ := m.(map[string]any)
				if msg == nil {
					continue
				}
				if _, has := msg["cache_control"]; has {
					cacheIdx = i
				}
				if content, ok := msg["content"].([]any); ok {
					for _, block := range content {
						if b, ok := block.(map[string]any); ok {
							if _, has := b["cache_control"]; has {
								cacheIdx = i
							}
						}
					}
				}
			}

			startIdx := cacheIdx + 1
			if startIdx < 0 {
				startIdx = 0
			}

			// Retrieve stored response time for assistant annotation
			s.responseTsMu.RLock()
			respTime, hasRespTime := s.responseTimes[threadID]
			s.responseTsMu.RUnlock()

			// 1. Last user message: annotate with [2026-03-13 00:15:42] [msg:N]
			lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
			if lastMsg != nil && lastMsg["role"] == "user" {
				meta := fmt.Sprintf("[%s] [msg:%d]", nowStr, s.msgCounters.nextFor(threadID, len(msgs)-1))
				if hasRespTime {
					delta := now.Sub(respTime)
					meta += " [+" + formatDelta(delta) + "]"
				}
				prependMeta(lastMsg, meta)
				needsReserialization = true
			}

			// 2. Last assistant message after cache breakpoint: use stored response time
			if hasRespTime {
				for i := len(msgs) - 1; i >= startIdx; i-- {
					msg, _ := msgs[i].(map[string]any)
					if msg != nil && msg["role"] == "assistant" {
						meta := fmt.Sprintf("[%s] [msg:%d]", respTime.Format("2006-01-02 15:04:05"), i)
						prependMeta(msg, meta)
						needsReserialization = true
						break
					}
				}
			}
		}
	}

	// Self-Priming: DISABLED (experimental, see signal_handlers.go comment block).
	// The reflection-based selfprime (Haiku) was too inaccurate to be useful —
	// the main model adapts to user context organically from message history.
	// Kept for reference; re-enable by uncommenting below + setting ShouldActivate=true.
	//
	// s.selfPrimeMu.RLock()
	// anchor := s.selfPrimes[threadID]
	// s.selfPrimeMu.RUnlock()
	// if anchor != "" {
	// 	if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
	// 		lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	// 		if lastMsg != nil && lastMsg["role"] == "user" {
	// 			primeBlock := map[string]any{
	// 				"type": "text",
	// 				"text": "[self-prime]\n[Self-Prime von deiner letzten Antwort]: " + anchor,
	// 			}
	// 			if content, ok := lastMsg["content"].([]any); ok {
	// 				lastMsg["content"] = append(content, primeBlock)
	// 			} else if text, ok := lastMsg["content"].(string); ok {
	// 				lastMsg["content"] = []any{
	// 					map[string]any{"type": "text", "text": text},
	// 					primeBlock,
	// 				}
	// 			}
	// 			needsReserialization = true
	// 		}
	// 	}
	// }

	// Prompt Caching: inject cache_control breakpoints on system + tools
	// Sawtooth: disabled (all 4 slots are used by CC system (2) + briefing (1) + CC messages (1))
	// Legacy: count existing breakpoints and fill up remaining slots.
	if !s.cfg.SawtoothEnabled && s.cacheGate.ShouldCache() {
		if n := InjectCacheBreakpoints(req, s.logger); n > 0 {
			needsReserialization = true
			s.logger.Printf("[req %d %s] prompt cache: %d breakpoints injected", reqIdx, proj, n)
		} else {
			s.logger.Printf("[req %d %s] prompt cache: no budget (4/4 already set)", reqIdx, proj)
		}
	}

	// Final: upgrade cache TTL + enforce breakpoint limit (must run AFTER all injections + InjectCacheBreakpoints)
	// TTL upgrade only for API key auth — subscription (OAuth) users should not get extended TTL.
	if s.cfg.CacheTTL != "" && s.cfg.CacheTTL != "ephemeral" && IsAPIKeyAuth(r.Header) {
		if n := UpgradeCacheTTL(req, s.cfg.CacheTTL); n > 0 {
			needsReserialization = true
			s.logger.Printf("[req %d %s] cache TTL upgraded: %d blocks → %s", reqIdx, proj, n, s.cfg.CacheTTL)
		}
	}
	if n := EnforceCacheBreakpointLimit(req, maxCacheBreakpoints); n > 0 {
		needsReserialization = true
		s.logger.Printf("[req %d %s] prompt cache: trimmed %d surplus breakpoints", reqIdx, proj, n)
	}

	// Pre-flight: validate tool_use/tool_result pairing before forwarding.
	// Repairs orphans created by Stubify/Collapse/Injection pipeline.
	if msgs, ok := req["messages"].([]any); ok {
		repaired, orphans := validateToolPairs(msgs, s.logger)
		if orphans > 0 {
			req["messages"] = repaired
			s.logger.Printf("%s[req %d %s tid=%s] validate: repaired %d orphan tool_result(s)%s", colorOrange, reqIdx, proj, threadID, orphans, colorReset)
			needsReserialization = true
		}
	}

	// Early-out: skip re-serialization when nothing changed
	if !needsReserialization {
		s.logger.Printf("%s[req %d %s tid=%s] %d messages, ~%dk tokens (no stubbing, threshold=%dk)%s", colorDim, reqIdx, proj, threadID, len(messages), totalTokens/1000, s.effectiveTokenThreshold(model)/1000, colorReset)
		s.forwardWithAnnotation(w, r, body, reqIdx, toolUseIDs, proj, threadID, msgCount, totalTokens)
		return
	}

	// Re-serialize
	body, err = json.Marshal(req)
	if err != nil {
		s.logger.Printf("%s[req %d %s tid=%s] re-marshal error: %v%s", colorRed, reqIdx, proj, threadID, err, colorReset)
		http.Error(w, "internal proxy error", http.StatusInternalServerError)
		return
	}

	// Debug: dump final request body to file (enable with YESMEM_PROXY_DEBUG=1)
	if needsReserialization && os.Getenv("YESMEM_PROXY_DEBUG") == "1" {
		dumpDir := filepath.Join(s.cfg.DataDir, "debug")
		os.MkdirAll(dumpDir, 0755)
		ts := time.Now().Format("20060102-150405")
		dumpPath := fmt.Sprintf("%s/req_%s_%d.json", dumpDir, ts, reqIdx)
		os.WriteFile(dumpPath, body, 0644)
		s.logger.Printf("[req %d] dumped %dk to %s", reqIdx, len(body)/1024, dumpPath)
	}

	// Task #7: Mark this request fingerprint for retry detection
	if !isRetryReq {
		s.markRequest(fp)
	}

	// Forward to Anthropic with SSE annotation extraction
	// Use original msgCount (pre-injection) so actual-based estimation stays consistent across turns
	// Use actual-based totalTokens as estimate when available (more accurate than full recount)
	finalEstimate := totalTokens
	if s.sawtoothTrigger == nil || s.sawtoothTrigger.GetLastTokens(threadID) == 0 {
		// First request: no actual yet, do a full recount of post-injection messages
		finalMsgs, _ := req["messages"].([]any)
		finalEstimate = s.measureOverhead(req) + s.countMessageTokens(finalMsgs)
	}
	s.logger.Printf("[req %d %s tid=%s] FINAL: ~%dk estimated (after all injections)", reqIdx, proj, threadID, finalEstimate/1000)
	if raw := s.getRawEstimate(threadID); raw > 0 {
		s.cacheStatusWriter.UpdateRawForThread(threadID, raw)
	}
	s.forwardWithAnnotation(w, r, body, reqIdx, toolUseIDs, proj, threadID, msgCount, finalEstimate)
}

// bytesPerToken is the approximation factor for token estimation.
// Anthropic recommends ~3.5 chars/token; we use 3.6 for slight overestimation
// (triggers stubbing a bit earlier = safer for context budget).
const bytesPerToken = 3.6

// TokenEstimateFunc is the central token estimation function.
// All stubbing/budget decisions use this. Receives extracted text content
// (not JSON wire format) so the tokenizer counts actual content tokens.
type TokenEstimateFunc func(text string) int
