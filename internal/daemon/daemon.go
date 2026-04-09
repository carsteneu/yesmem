package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"net/http"

	"github.com/carsteneu/yesmem/internal/archive"
	"github.com/carsteneu/yesmem/internal/bloom"
	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/graph"
	"github.com/carsteneu/yesmem/internal/httpapi"
	"github.com/carsteneu/yesmem/internal/indexer"
	"github.com/carsteneu/yesmem/internal/ingest"
	"github.com/carsteneu/yesmem/internal/ivf"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/update"
)

type briefingRefreshTarget struct {
	Project     storage.ProjectSummary
	Raw         string
	Fingerprint string
}

func embeddingCacheModelKey(cfg *config.Config) string {
	if cfg == nil {
		return "unknown"
	}
	if cfg.Embedding.Provider != "" {
		return cfg.Embedding.Provider
	}
	return "unknown"
}

// ReadClaudeCodeAPIKey reads the API key from Claude Code's config.json.
func ReadClaudeCodeAPIKey() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "config.json"))
	if err != nil {
		return ""
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	if key, ok := cfg["primaryApiKey"].(string); ok {
		return key
	}
	return ""
}

// Config holds daemon configuration.
type Config struct {
	DataDir          string // ~/.claude/yesmem/
	ProjectsDir      string // ~/.claude/projects/
	CodexSessionsDir string // ~/.codex/sessions/
	SessionSources   []string
	Replace          bool   // Kill existing daemon before starting
	HTTPEnabled      bool   // --http flag or config: start HTTP API server
	HTTPListen       string // from config, default "127.0.0.1:9377"
}

// Run starts the daemon: socket-first for instant MCP availability, then index async.
func Run(cfg Config) error {
	// Check for existing daemon
	if err := ensureSingleInstance(cfg.DataDir, cfg.Replace); err != nil {
		return err
	}

	// Extend PATH so agent processes can find user-installed binaries (e.g. claude in ~/.local/bin)
	if home, err := os.UserHomeDir(); err == nil {
		os.Setenv("PATH", filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"))
	}

	// Set up log file
	logDir := filepath.Join(cfg.DataDir, "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
		defer logFile.Close()
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("  YesMem Daemon", buildinfo.Version)
	log.Printf("  Log: %s", logPath)
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Open storage (has data from previous runs — MCP can serve immediately)
	store, err := storage.Open(filepath.Join(cfg.DataDir, "yesmem.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	if err := store.MigrateAgentsSchema(); err != nil {
		log.Printf("[warn] agents schema migration: %v", err)
	}

	// Backfill valid_until on old superseded learnings (one-time, idempotent)
	if res, err := store.DB().Exec(`UPDATE learnings SET valid_until = datetime('now')
		WHERE superseded_by IS NOT NULL AND valid_until IS NULL`); err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("Backfilled valid_until on %d superseded learnings", n)
		}
	}

	// In-memory components
	bloomMgr := bloom.New()
	assocGraph := graph.New()

	// Archiver + Indexer
	arch := archive.New(filepath.Join(cfg.DataDir, "archive"))
	idx := indexer.New(store, bloomMgr, arch)

	// Index progress tracking (atomics — safe for concurrent reads)
	var indexTotal, indexDone, indexSkipped int64
	var indexRunning int32    // 1 = running, 0 = done
	var extractionActive int32 // >0 = extraction goroutines running
	var lastConsolidation time.Time
	batchExtractNotify := make(chan struct{}, 1) // non-blocking signal for batch trigger

	// ━━━ Socket server FIRST — MCP available immediately ━━━
	handler := NewHandler(store, bloomMgr)
	handler.dataDir = cfg.DataDir
	if ac, _ := config.Load(filepath.Join(cfg.DataDir, "config.yaml")); ac != nil {
		handler.agentTerminal = ac.Agents.Terminal
		if ac.Agents.MaxRuntime != "" {
			if d, err := time.ParseDuration(ac.Agents.MaxRuntime); err == nil {
				handler.agentMaxRuntime = d
			}
		}
		if ac.Agents.MaxTurns > 0 {
			handler.agentMaxTurns = ac.Agents.MaxTurns
		}
		if ac.Agents.MaxDepth > 0 {
			handler.agentMaxDepth = ac.Agents.MaxDepth
		}
		if ac.Agents.TokenBudget > 0 {
			handler.agentTokenBudget = ac.Agents.TokenBudget
		}
	}
	LoadPlansFromDB(store)

	// Recover agents from before daemon restart — keep living, delete dead
	reconnected, deleted, err := store.AgentRecoverOrphaned(func(pid int) bool {
		return syscall.Kill(pid, 0) == nil
	})
	if err == nil && (reconnected > 0 || deleted > 0) {
		log.Printf("Agent recovery: %d reconnected, %d deleted (daemon restart)", reconnected, deleted)
	}

	handler.IndexProgress = func() (total, done, skipped int, running bool) {
		return int(atomic.LoadInt64(&indexTotal)),
			int(atomic.LoadInt64(&indexDone)),
			int(atomic.LoadInt64(&indexSkipped)),
			atomic.LoadInt32(&indexRunning) == 1
	}
	socketSrv, err := NewSocketServer(cfg.DataDir, handler.Handle)
	if err != nil {
		return err
	}
	go socketSrv.Serve()
	defer socketSrv.Close()
	log.Println("Socket ready for MCP connections.")

	// ━━━ HTTP API (optional, for OpenClaw) ━━━
	if cfg.HTTPEnabled {
		tokenPath := filepath.Join(cfg.DataDir, "auth_token")
		authToken, err := httpapi.EnsureAuthToken(tokenPath)
		if err != nil {
			log.Printf("warn: HTTP auth token: %v", err)
		} else {
			listen := cfg.HTTPListen
			if listen == "" {
				listen = "127.0.0.1:9377"
			}
			// Adapter: bridges daemon.Handler to httpapi.RPCHandler interface
			adapter := &handlerAdapter{h: handler}
			httpSrv := httpapi.New(adapter, listen, authToken, log.Default())
			go func() {
				if err := httpSrv.Serve(); err != nil && err != http.ErrServerClosed {
					log.Printf("HTTP server error: %v", err)
				}
			}()
			defer httpSrv.Shutdown(context.Background())
			log.Printf("HTTP API ready on %s (token: %s...)", listen, authToken[:8])
		}
	}

	// Proxy runs as separate systemd unit (yesmem-proxy.service), not embedded here.

	// Context for graceful shutdown of background workers
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	_ = daemonCancel // used at shutdown

	// FTS5 background sync — replaces triggers to avoid write contention on BM25 reads
	store.StartFTSSync(daemonCtx, 10*time.Second)

	// Agent heartbeat — polls for messages targeting running agents, relays via inject socket
	go handler.startAgentHeartbeat(daemonCtx)

	// Extractor holder — set asynchronously after config is loaded
	var (
		extMu        sync.Mutex
		extractor    extraction.SessionExtractor
		evoExtractor *extraction.Extractor
		llmClient    extraction.LLMClient
		qualityLLM   extraction.LLMClient
		appCfg       *config.Config
		apiGateRef   *extraction.APIGate
	)

	sessionRoots := cfg.SessionSources
	if len(sessionRoots) == 0 {
		sessionRoots = []string{cfg.ProjectsDir, cfg.CodexSessionsDir}
	}

	startWatcher := func(root string) (*Watcher, error) {
		if root == "" {
			return nil, nil
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			if err == nil {
				err = fmt.Errorf("not a directory")
			}
			log.Printf("warn: watcher disabled for %s: %v", root, err)
			return nil, nil
		}
		w := NewWatcher(root,
			func(path string) {
				log.Printf("Change detected: %s", filepath.Base(path))
				if err := idx.IndexSession(path); err != nil {
					log.Printf("warn: index %s: %v", path, err)
				}
			},
			func(path string) {
				log.Printf("Session settled (5min quiet): %s — queued for batch extraction", filepath.Base(path))
				select {
				case batchExtractNotify <- struct{}{}:
				default:
				}
			},
		)
		if err := w.Start(); err != nil {
			return nil, err
		}
		return w, nil
	}

	var watchers []*Watcher
	for _, root := range sessionRoots {
		w, err := startWatcher(root)
		if err != nil {
			return err
		}
		if w != nil {
			watchers = append(watchers, w)
		}
	}
	for _, w := range watchers {
		defer w.Stop()
	}

	// ━━━ Async: Indexing + Config + Extraction (does not block MCP) ━━━
	go func() {
		// Phase 1: Index all sessions
		start := time.Now()
		atomic.StoreInt32(&indexRunning, 1)
		totalIndexed := 0
		totalSkipped := 0
		totalFiles := 0

		var idxErr error
		for _, root := range sessionRoots {
			if root == "" {
				continue
			}
			if info, err := os.Stat(root); err != nil || !info.IsDir() {
				continue
			}
			log.Printf("Indexing all sessions from %s...", root)
			indexed, skipped, runErr := idx.IndexAllWithProgress(root, func(total, done, skip int) {
				atomic.StoreInt64(&indexTotal, int64(totalFiles+total))
				atomic.StoreInt64(&indexDone, int64(totalIndexed+done))
				atomic.StoreInt64(&indexSkipped, int64(totalSkipped+skip))
			})
			totalIndexed += indexed
			totalSkipped += skipped
			totalFiles += indexed + skipped
			atomic.StoreInt64(&indexTotal, int64(totalFiles))
			atomic.StoreInt64(&indexDone, int64(totalIndexed+totalSkipped))
			atomic.StoreInt64(&indexSkipped, int64(totalSkipped))
			if runErr != nil && idxErr == nil {
				idxErr = runErr
			}
		}
		atomic.StoreInt32(&indexRunning, 0)
		if idxErr != nil {
			log.Printf("warn: initial index: %v", idxErr)
		}
		log.Printf("Indexed %d sessions (%d skipped) in %v", totalIndexed, totalSkipped, time.Since(start).Round(time.Millisecond))

		// Load graph from DB
		assocs, assocErr := store.LoadAllAssociations()
		if assocErr == nil {
			assocGraph.LoadFromAssociations(assocs)
			log.Printf("Graph loaded: %d nodes, %d edges", assocGraph.NodeCount(), assocGraph.EdgeCount())
		}
		log.Printf("Bloom filters: %d sessions tracked", bloomMgr.SessionCount())

		// Load config + resolve API key
		ac, _ := config.Load(filepath.Join(cfg.DataDir, "config.yaml"))
		apiKey := ac.ResolvedAPIKey()
		if apiKey == "" {
			apiKey = ReadClaudeCodeAPIKey()
		}
		ac.API.APIKey = apiKey
		baseURL := ac.ResolvedOpenAIBaseURL()

		// Set up LLM clients
		// Extraction client (Sonnet default) — for learnings extraction
		client, clientErr := extraction.NewLLMClient(ac.LLM.Provider, apiKey, ac.ModelID(), ac.LLM.ClaudeBinary, baseURL)
		if clientErr != nil {
			log.Printf("LLM client error: %v", clientErr)
		}
		// Wrap with daily budget enforcement
		spendAdapter := &storage.SpendAdapter{Store: store}

		// Always create a lightweight summarize client (Haiku) for on-demand tasks
		// (rules condensation, doc destillation) — independent of extraction cycle
		if apiKey != "" {
			summarizeModel := ac.SummarizeModelID()
			sc, scErr := extraction.NewLLMClient(ac.LLM.Provider, apiKey, summarizeModel, ac.LLM.ClaudeBinary, baseURL)
			if scErr == nil && sc != nil {
				handler.SummarizeClient = sc
				log.Printf("Summarize client ready: %s (for rules/destillation)", summarizeModel)
			}
		}

		// Shared extraction budget tracker (used by summarize + extract clients)
		var extractTracker *extraction.BudgetTracker
		if ac.LLM.DailyBudgetExtractUSD > 0 {
			extractTracker = extraction.NewPersistentBudgetTracker(ac.LLM.DailyBudgetExtractUSD, "extract", spendAdapter)
			log.Printf("Daily extraction budget: $%.2f", ac.LLM.DailyBudgetExtractUSD)
		}
		// Throttle function: blocks LLM calls when API utilization exceeds 50%
		throttleFn := func() bool {
			rlJSON, err := store.GetProxyState("rate_limits")
			if err != nil || rlJSON == "" {
				return false
			}
			var rl struct {
				Unified5hUtilization float64 `json:"unified_5h_utilization"`
				TokensLimit          int     `json:"tokens_limit"`
				TokensRemaining      int     `json:"tokens_remaining"`
				IsSubscription       bool    `json:"is_subscription"`
			}
			if json.Unmarshal([]byte(rlJSON), &rl) != nil {
				return false
			}
			var utilization float64
			if rl.IsSubscription {
				utilization = rl.Unified5hUtilization
			} else if rl.TokensLimit > 0 {
				utilization = 1.0 - float64(rl.TokensRemaining)/float64(rl.TokensLimit)
			}
			return utilization > 0.5
		}
		if client != nil && extractTracker != nil {
			bc := extraction.NewBudgetClient(client, extractTracker)
			bc.ThrottleFn = throttleFn
			client = bc
		}
		// Quality client (Opus default) — for narratives, profiles, persona synthesis
		var qualityClient extraction.LLMClient
		var qualityTracker *extraction.BudgetTracker
		if ac.NarrativeModelID() != ac.ModelID() {
			qc, qcErr := extraction.NewLLMClient(ac.LLM.Provider, apiKey, ac.NarrativeModelID(), ac.LLM.ClaudeBinary, baseURL)
			if qcErr != nil {
				log.Printf("Quality LLM client error (falling back to extraction model): %v", qcErr)
				qualityClient = client
			} else {
				if ac.LLM.DailyBudgetQualityUSD > 0 {
					qualityTracker = extraction.NewPersistentBudgetTracker(ac.LLM.DailyBudgetQualityUSD, "quality", spendAdapter)
					qbc := extraction.NewBudgetClient(qc, qualityTracker)
					qbc.ThrottleFn = throttleFn
					qc = qbc
					log.Printf("Daily quality budget: $%.2f", ac.LLM.DailyBudgetQualityUSD)
				}
				qualityClient = qc
			}
		} else {
			qualityClient = client
		}

		// ━━━ Global API Health Gate ━━━
		apiGate := extraction.NewAPIGate(extractTracker, qualityTracker, 5, 0.50)
		apiGateRef = apiGate
		if client != nil {
			client = extraction.NewGatedClient(client, apiGate, "extract")
		}
		if qualityClient != nil && qualityClient != client {
			qualityClient = extraction.NewGatedClient(qualityClient, apiGate, "quality")
		}
		// Feed actual token usage from API responses into budget tracking
		extraction.OnUsage = func(model string, inputTokens, outputTokens int) {
			inputPerM, outputPerM := ac.PricingForModel(model)
			if extractTracker != nil {
				extractTracker.TrackTokens(inputTokens, outputTokens, inputPerM, outputPerM)
			}
		}
		log.Printf("[api-gate] Global health gate active (threshold=5, reserve=$0.50)")

		// Set per-call budget limit on CLI clients (safety net via --max-budget-usd)
		if ac.LLM.MaxBudgetPerCallUSD > 0 {
			extraction.SetMaxBudgetPerCall(client, ac.LLM.MaxBudgetPerCallUSD)
			extraction.SetMaxBudgetPerCall(qualityClient, ac.LLM.MaxBudgetPerCallUSD)
		}

		// ━━━ Embedding: Initialize provider synchronously (needed for clustering) ━━━
		log.Println("Loading embedding provider...")
		var queryEmbedProv embedding.Provider
		var embedErr error
		queryEmbedProv, embedErr = embedding.NewProviderFromConfig(ac.Embedding)
		if embedErr != nil {
			log.Printf("Embedding provider error: %v", embedErr)
			queryEmbedProv = nil
		} else if !queryEmbedProv.Enabled() {
			log.Println("Embedding disabled (provider: none)")
			queryEmbedProv = nil
		} else {
			queryEmbedProv = embedding.NewCachedProvider(queryEmbedProv, store.DB(), embeddingCacheModelKey(ac))
			handler.SetSearchEmbeddingProvider(queryEmbedProv)
		}
		embedding.ReleaseWeightData()

		go func() {
			if queryEmbedProv == nil {
				return
			}
			defer queryEmbedProv.Close()
			<-daemonCtx.Done()
		}()

		// Vector store + dedicated indexing provider + worker async
		go func() {
			if queryEmbedProv == nil {
				return
			}
			embedProv, err := embedding.NewProviderFromConfig(ac.Embedding)
			if err != nil {
				log.Printf("Embedding provider error: %v", err)
				return
			}
			if !embedProv.Enabled() {
				embedProv.Close()
				return
			}
			defer embedProv.Close() // release provider resources on shutdown
			vecStore, vecErr := embedding.NewVectorStore(store.DB(), embedProv.Dimensions())
			if vecErr != nil {
				log.Printf("Vector store error: %v", vecErr)
				return
			}

			// IVF index: auto-activate above threshold, or force via config
			vecCount := vecStore.Count()
			ivfThreshold := ac.Embedding.Search.IVFThreshold
			if ivfThreshold <= 0 {
				ivfThreshold = 50000
			}
			method := ac.Embedding.Search.Method
			if method == "ivf" || (method == "" && vecCount >= ivfThreshold) {
				ivfPath := filepath.Join(cfg.DataDir, "yesmem.ivf")
				nProbe := ac.Embedding.Search.IVF.NProbe
				if nProbe <= 0 {
					nProbe = 5
				}
				k := ac.Embedding.Search.IVF.K
				idx, loadErr := ivf.Load(ivfPath, store.DB())
				if loadErr != nil {
					log.Printf("IVF index not found, building from %d vectors...", vecCount)
					idx, loadErr = ivf.Build(store.DB(), embedProv.Dimensions(), k, nProbe)
					if loadErr != nil {
						log.Printf("IVF build error: %v", loadErr)
					} else {
						idx.Save(ivfPath)
					}
				} else if activeCount := vecStore.ActiveCount(); idx.IsStale(activeCount) {
					log.Printf("IVF index stale: %d vectors in index vs %d active in DB (>2%% gap), rebuilding...", idx.TotalVectors(), activeCount)
					idx, loadErr = ivf.Build(store.DB(), embedProv.Dimensions(), k, nProbe)
					if loadErr != nil {
						log.Printf("IVF rebuild error: %v", loadErr)
					} else {
						idx.Save(ivfPath)
					}
				}
				if idx != nil {
					vecStore.SetIVFIndex(idx)
					vecStore.SetIVFSavePath(ivfPath, 100)
					log.Printf("IVF index active: %d centroids, %d vectors, nprobe=%d", idx.K, idx.TotalVectors(), idx.NProbe)
				}
			} else if method != "brute_force" {
				log.Printf("Brute-force search: %d vectors (IVF threshold: %d)", vecCount, ivfThreshold)
			}

			embedIndexer := embedding.NewIndexer(embedProv, vecStore)
			handler.SetEmbedding(embedIndexer, vecStore, embedProv)
			handler.ivfPath = filepath.Join(cfg.DataDir, "yesmem.ivf")
			handler.StartEmbedWorker(daemonCtx)
			log.Printf("Embedding enabled: %s (%dd vectors, %d docs in vec_learnings)", ac.Embedding.Provider, embedProv.Dimensions(), vecStore.Count())

			// Backfill doc chunk embeddings (non-blocking)
			go handler.EmbedDocChunks()

			// Block until daemon shuts down — defer embedProv.Close() runs here
			<-daemonCtx.Done()
		}()

		// ━━━ Extraction + Evolution (runs independently) ━━━
		if client != nil && ac.Extraction.AutoExtract {
			log.Printf("LLM backend: %s (model: %s)", client.Name(), ac.Extraction.Model)

			// Always create single-pass extractor for evolution
			evoExt := extraction.NewExtractor(client, store)

			// Create session extractor based on mode
			var ext extraction.SessionExtractor
			if ac.Extraction.Mode == "two-pass" {
				// Pass 1: Summarize client — uses SummarizeModelID (default: haiku)
				summarizeModel := ac.SummarizeModelID()
				var summarizeClient extraction.LLMClient
				if summarizeModel == ac.ModelID() {
					// Same model — reuse client (already budget-wrapped)
					summarizeClient = client
				} else {
					sc, scErr := extraction.NewLLMClient(ac.LLM.Provider, apiKey, summarizeModel, ac.LLM.ClaudeBinary, baseURL)
					if scErr != nil || sc == nil {
						log.Printf("Summarize client unavailable, using extraction model: %v", scErr)
						summarizeClient = client
					} else {
						if extractTracker != nil {
							sbc := extraction.NewBudgetClient(sc, extractTracker)
							sbc.ThrottleFn = throttleFn
							sc = sbc
						}
						summarizeClient = sc
					}
				}

				// Pass 2: Extract client — uses QualityModelID
				extractModel := ac.QualityModelID()
				extractClient, extractErr := extraction.NewLLMClient(ac.LLM.Provider, apiKey, extractModel, ac.LLM.ClaudeBinary, baseURL)
				if extractErr == nil && extractClient != nil {
					if extractTracker != nil {
						ebc := extraction.NewBudgetClient(extractClient, extractTracker)
						ebc.ThrottleFn = throttleFn
						extractClient = ebc
					}
					ext = extraction.NewTwoPassExtractor(summarizeClient, extractClient, store)
					log.Printf("Extraction mode: two-pass (summarize=%s, extract=%s)", summarizeClient.Model(), extractClient.Model())
					// Make summarize client available for doc destillation
					handler.SummarizeClient = summarizeClient
					handler.CommitEvalClient = extractClient
					handler.QualityClient = qualityClient
				} else {
					ext = evoExt
					log.Printf("Extraction mode: single-pass fallback (quality client unavailable)")
				}
			} else {
				ext = evoExt
				log.Printf("Extraction mode: single-pass (%s)", client.Model())
			}

			extMu.Lock()
			extractor = ext
			evoExtractor = evoExt
			llmClient = client
			qualityLLM = qualityClient
			appCfg = ac
			extMu.Unlock()

			handler.BriefingUserProfile = ac.Briefing.UserProfile

			log.Printf("Quality model: %s", qualityClient.Model())
			runInitialExtraction(ext, evoExt, store, ac, client, qualityClient, handler)

			// Schlaf-Konsolidierung bei Startup (rule-based, kein LLM)
			go func() {
				log.Printf("💤 Startup consolidation...")
				result := extraction.RunConsolidation(store, nil, nil, extraction.ConsolidateConfig{
					MaxRounds:     2,
					RuleBasedOnly: true,
				})
				log.Printf("💤 Startup consolidation done: %d checked, %d superseded in %d rounds",
					result.TotalChecked, result.TotalSuperseded, result.Rounds)

				// Phase A: LLM-Destillation auf Learning-Clustern
				extMu.Lock()
				cl := llmClient
				extMu.Unlock()
				if cl != nil {
					log.Printf("💤 Cluster distillation starting...")
					dr := extraction.RunClusterDistillation(store, cl, 3)
					log.Printf("💤 Cluster distillation done: %d clusters, %d distilled, %d superseded, %d skipped, %d errors",
						dr.ClustersProcessed, dr.Distilled, dr.Superseded, dr.Skipped, dr.Errors)
				}

				lastConsolidation = time.Now()
			}()
		} else {
			extMu.Lock()
			llmClient = client
			qualityLLM = qualityClient
			appCfg = ac
			extMu.Unlock()
			if client == nil {
				log.Println("LLM extraction disabled: no API key and no Claude CLI in PATH")
			} else {
				log.Println("LLM extraction disabled: auto_extract=false")
			}
		}
	}()

	// ━━━ ClaudeMd refresh timer ━━━
	go func() {
		// Wait for appCfg to be populated
		var ac *config.Config
		for {
			extMu.Lock()
			ac = appCfg
			extMu.Unlock()
			if ac != nil {
				break
			}
			time.Sleep(5 * time.Second)
		}
		if !ac.ClaudeMd.Enabled {
			return
		}
		interval, err := time.ParseDuration(ac.ClaudeMd.RefreshInterval)
		if err != nil || interval <= 0 {
			interval = 2 * time.Hour
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				// Rules refresh handled by startRulesWatch, periodic ops.md removed
			}
		}
	}()

	// ━━━ Rules auto-refresh on CLAUDE.md changes ━━━
	go func() {
		// Wait for summarize client (set at daemon startup)
		time.Sleep(3 * time.Second)
		rulesClient := handler.QualityClient
		if rulesClient == nil {
			rulesClient = handler.SummarizeClient
		}
		RunRulesRefresh(store, rulesClient)
		startRulesWatch(daemonCtx, store, rulesClient)
	}()

	// ━━━ Briefing refinement background job ━━━
	go func() {
		// Wait for appCfg + API key
		var ac *config.Config
		var apiKey string
		for {
			extMu.Lock()
			ac = appCfg
			extMu.Unlock()
			if ac != nil {
				apiKey = ac.ResolvedAPIKey()
				if apiKey == "" {
					apiKey = ReadClaudeCodeAPIKey()
				}
				if apiKey != "" {
					break
				}
			}
			time.Sleep(5 * time.Second)
		}

		model := ac.QualityModelID()
		log.Printf("Briefing refinement: model=%s, interval=24h", model)

		// Build a gated client for briefing refinement
		briefingClient, _ := extraction.NewLLMClient(ac.LLM.Provider, apiKey, model, ac.LLM.ClaudeBinary, ac.ResolvedOpenAIBaseURL())
		if briefingClient != nil {
			extMu.Lock()
			gate := apiGateRef
			extMu.Unlock()
			if gate != nil {
				briefingClient = extraction.NewGatedClient(briefingClient, gate, "quality")
			}
		}

		// Initial generation on daemon start only for projects with missing or changed cache entries.
		if targets, err := listProjectsNeedingBriefingRefresh(store, ac); err == nil && len(targets) > 0 {
			log.Printf("Briefing refinement: %d projects missing/changed — generating", len(targets))
			regenerateBriefingsForTargets(store, ac, briefingClient, targets)
		} else {
			log.Printf("Briefing refinement: startup skipped (cache present)")
		}

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				extMu.Lock()
				ac = appCfg
				extMu.Unlock()
				apiKey = ac.ResolvedAPIKey()
				if apiKey == "" {
					apiKey = ReadClaudeCodeAPIKey()
				}
				if apiKey != "" {
					model = ac.QualityModelID()
					targets, err := listProjectsNeedingBriefingRefresh(store, ac)
					if err != nil {
						log.Printf("[briefing] refine: target scan failed: %v", err)
						continue
					}
					if len(targets) == 0 {
						log.Printf("[briefing] refine: periodic scan skipped (no briefing changes)")
						continue
					}
					regenerateBriefingsForTargets(store, ac, briefingClient, targets)
				}
			}
		}
	}()

	// Batch extraction cycle — replaces immediate settled-trigger extraction.
	// Runs every 2h OR when ≥5 sessions are pending (whichever comes first).
	// Back-to-back processing enables Anthropic Prompt Cache reuse (5min TTL).
	go func() {
		const batchPendingThreshold = 5
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
			case <-batchExtractNotify:
			}
			// Check pending count — only run if enough sessions or timer fired
			pending, _ := store.CountPendingExtractions(5)
			isTick := true // simplification: always check, ticker resets on select
			if !isTick && pending < batchPendingThreshold {
				continue
			}
			if pending == 0 {
				continue
			}
			// Throttle extraction when API utilization is high
			if rlJSON, err := store.GetProxyState("rate_limits"); err == nil && rlJSON != "" {
				var rl struct {
					Unified5hUtilization float64 `json:"unified_5h_utilization"`
					TokensLimit          int     `json:"tokens_limit"`
					TokensRemaining      int     `json:"tokens_remaining"`
					IsSubscription       bool    `json:"is_subscription"`
				}
				if json.Unmarshal([]byte(rlJSON), &rl) == nil {
					var utilization float64
					if rl.IsSubscription {
						utilization = rl.Unified5hUtilization
					} else if rl.TokensLimit > 0 {
						utilization = 1.0 - float64(rl.TokensRemaining)/float64(rl.TokensLimit)
					}
					if utilization > 0.5 {
						log.Printf("━━━ Extraction deferred: utilization %.0f%% (threshold 50%%) ━━━", utilization*100)
						continue
					}
				}
			}
			extMu.Lock()
			ext := extractor
			evo := evoExtractor
			cl := llmClient
			ql := qualityLLM
			ac := appCfg
			extMu.Unlock()
			if ext == nil || cl == nil || ac == nil {
				continue // not initialized yet
			}
			log.Printf("━━━ Batch extraction cycle: %d sessions pending ━━━", pending)
			atomic.AddInt32(&extractionActive, 1)
			runBatchExtraction(ext, evo, store, ac, cl, ql, handler)
			atomic.AddInt32(&extractionActive, -1)
			// Rule-based consolidation after batch (1h cooldown)
			if time.Since(lastConsolidation) > time.Hour {
				result := extraction.RunConsolidation(store, nil, nil, extraction.ConsolidateConfig{MaxRounds: 2, RuleBasedOnly: true})
				if result.TotalSuperseded > 0 {
					log.Printf("  Batch consolidation: %d checked, %d superseded", result.TotalChecked, result.TotalSuperseded)
				}
				lastConsolidation = time.Now()
			}
		}
	}()

	// Doc sync — checks all registered doc sources for updates every 2h
	go func() {
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				extMu.Lock()
				ac := appCfg
				extMu.Unlock()
				if ac == nil {
					continue
				}
				if hasDocSyncWork(store) {
					runDocSync(store, ac)
				} else {
					log.Printf("[doc-sync] periodic scan skipped (no source changes detected)")
				}
			}
		}
	}()

	// Query clustering — cluster query_log entries every 30 minutes.
	// Builds query clusters for context-aware retrieval scoring.
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				RunQueryClustering(store)
			}
		}
	}()

	// Recurrence detection — every 2h, but only if ≥50 new learnings since last run.
	// Finds clusters with recurring patterns and generates alerts.
	go func() {
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()
		lastRun := time.Now()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				newCount := store.CountLearningSince(lastRun)
				if newCount < 50 {
					log.Printf("[recurrence] skipped: only %d new learnings (need 50)", newCount)
					continue
				}
				extMu.Lock()
				cl := llmClient
				extMu.Unlock()
				if cl == nil {
					continue
				}
				alerts := DetectRecurrence(store, cl)
				log.Printf("[recurrence] done: %d alerts from %d new learnings", alerts, newCount)
				lastRun = time.Now()
			}
		}
	}()

	// Cluster distillation — every 2h, condenses similar learnings within clusters.
	go func() {
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				extMu.Lock()
				cl := llmClient
				extMu.Unlock()
				if cl == nil {
					continue
				}
				dr := extraction.RunClusterDistillation(store, cl, 3)
				log.Printf("[distillation] done: %d clusters, %d distilled, %d superseded, %d skipped",
					dr.ClustersProcessed, dr.Distilled, dr.Superseded, dr.Skipped)
			}
		}
	}()

	// Gap review — daily LLM review of unreviewed knowledge gaps.
	// Deletes noise (context-loss artifacts), keeps real knowledge gaps.
	// Prefers 06:00 when budget is fresh, but catches up on startup if >24h since last review.
	go func() {
		// Catch-up: if last review was >24h ago, run now (after 5min delay)
		lastReview := store.GetLastGapReviewTime()
		if time.Since(lastReview) > 24*time.Hour {
			select {
			case <-daemonCtx.Done():
				return
			case <-time.After(5 * time.Minute):
			}
			runGapReviewDaemon(store, &extMu, &appCfg)
		}

		// Then schedule daily at 06:00
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, now.Location())
			if now.After(next) {
				next = next.Add(24 * time.Hour)
			}
			select {
			case <-daemonCtx.Done():
				return
			case <-time.After(time.Until(next)):
			}
			runGapReviewDaemon(store, &extMu, &appCfg)
		}
	}()

	// Auto-update check
	go func() {
		extMu.Lock()
		ac := appCfg
		extMu.Unlock()
		if ac == nil || !ac.Update.AutoUpdate {
			return
		}
		interval := update.ParseCheckInterval(ac.Update.CheckInterval)
		log.Printf("[update] auto-update enabled, checking every %v", interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				extMu.Lock()
				ac = appCfg
				extMu.Unlock()
				if ac == nil || !ac.Update.AutoUpdate {
					continue
				}
				// Skip update while extraction is running — avoid replacing binary mid-extraction
				if atomic.LoadInt32(&extractionActive) > 0 || atomic.LoadInt32(&indexRunning) == 1 {
					log.Println("[update] skipping — extraction or indexing in progress")
					continue
				}
				newVersion, err := update.RunUpdate(buildinfo.Version, log.Default())
				if err != nil {
					log.Printf("[update] check failed: %v", err)
					continue
				}
				if newVersion != "" {
					log.Printf("[update] updated to %s, restarting...", newVersion)
					update.RunRestart(log.Default())
					return
				}
			}
		}
	}()

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	daemonCancel()
	handler.StopEmbedProcess()
	handler.SaveIVF()
	log.Println("Shutting down...")
	return nil
}

// regenerateAllBriefings generates refined briefings for all known projects.
func regenerateAllBriefings(store *storage.Store, cfg *config.Config, llmClient extraction.LLMClient) {
	projects, err := store.ListProjects()
	if err != nil {
		log.Printf("[briefing] refine: list projects failed: %v", err)
		return
	}
	targets := make([]briefingRefreshTarget, 0, len(projects))
	for _, project := range projects {
		targets = append(targets, briefingRefreshTarget{Project: project})
	}
	regenerateBriefingsForTargets(store, cfg, llmClient, targets)
}

func regenerateBriefingsForTargets(store *storage.Store, cfg *config.Config, llmClient extraction.LLMClient, targets []briefingRefreshTarget) {
	for _, target := range targets {
		p := target.Project
		if len(cfg.Briefing.Languages) > 0 {
			briefing.SetLanguages(cfg.Briefing.Languages)
		}
		briefing.SetStringsPath(filepath.Join(filepath.Dir(cfg.Paths.DB), "strings.yaml"))
		raw := target.Raw
		if raw == "" {
			gen := briefing.New(store, 0)
			gen.SetUserProfile(cfg.Briefing.UserProfile)
			raw = gen.Generate(p.Project)
		}
		if raw == "" {
			continue
		}
		log.Printf("[briefing] refine: regenerating for %s", p.ProjectShort)
		if err := briefing.RegenerateRefinedBriefing(store, p.ProjectShort, raw, llmClient, log.Default(), target.Fingerprint); err != nil {
			log.Printf("[briefing] refine: %s failed: %v", p.ProjectShort, err)
		}
	}
}

func listProjectsNeedingBriefingRefresh(store *storage.Store, cfg *config.Config) ([]briefingRefreshTarget, error) {
	projects, err := store.ListProjects()
	if err != nil {
		return nil, err
	}

	targets := make([]briefingRefreshTarget, 0, len(projects))

	for _, project := range projects {
		if project.ProjectShort == "" {
			continue
		}
		// Lightweight change detection: compare DB fingerprint instead of
		// generating the full raw briefing (which contains volatile timestamps).
		fingerprint := store.ProjectChangeFingerprint(project.ProjectShort)
		cachedHash, err := store.GetRefinedBriefingHash(project.ProjectShort)
		if err != nil {
			return nil, err
		}
		if cachedHash == fingerprint {
			continue
		}
		targets = append(targets, briefingRefreshTarget{
			Project:     project,
			Fingerprint: fingerprint,
		})
	}

	return targets, nil
}

// runDocSync checks all registered doc sources for updates.
// If a source is inside a git repo, pulls first. Then re-imports changed files.
func runDocSync(store *storage.Store, cfg *config.Config) {
	sources, err := store.ListDocSources("")
	if err != nil {
		log.Printf("[doc-sync] list sources: %v", err)
		return
	}

	if len(sources) == 0 {
		return
	}

	log.Printf("[doc-sync] Checking %d doc sources for updates...", len(sources))

	for _, src := range sources {
		if src.Path == "" {
			continue
		}

		if _, err := os.Stat(src.Path); err != nil {
			if os.IsNotExist(err) {
				log.Printf("[doc-sync] %s: skipped missing path %s", src.Name, src.Path)
			} else {
				log.Printf("[doc-sync] %s: skipped unreadable path %s: %v", src.Name, src.Path, err)
			}
			continue
		}

		// Git-aware: if source is in a git repo, fetch+pull
		gitUpdated := tryGitPull(src.Path)

		icfg := ingest.Config{
			Name:    src.Name,
			Version: src.Version,
			Project: src.Project,
			Destill: false,
		}

		result, runErr := ingest.Run(icfg, []string{src.Path}, store)
		if runErr != nil {
			log.Printf("[doc-sync] %s: error: %v", src.Name, runErr)
			continue
		}

		if result.FilesProcessed > 0 || gitUpdated {
			log.Printf("[doc-sync] %s: %d files updated, %d chunks created",
				src.Name, result.FilesProcessed, result.ChunksCreated)
		}
	}
}

// tryGitPull checks if the path is inside a git repo and pulls if so.
func tryGitPull(path string) bool {
	gitRoot := findGitRoot(path)
	if gitRoot == "" {
		return false
	}

	cmd := exec.Command("git", "-C", gitRoot, "pull", "--quiet")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[doc-sync] git pull %s: %v: %s", gitRoot, err, output)
		return false
	}

	// "Already up to date." means no changes
	return !strings.Contains(string(output), "Already up to date")
}

// findGitRoot walks up from path to find the nearest .git directory.
func findGitRoot(path string) string {
	absPath, _ := filepath.Abs(path)
	info, err := os.Stat(absPath)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}
	for {
		if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
			return absPath
		}
		parent := filepath.Dir(absPath)
		if parent == absPath {
			return ""
		}
		absPath = parent
	}
}

// handlerAdapter bridges daemon.Handler to httpapi.RPCHandler interface,
// avoiding an import cycle between daemon and httpapi packages.
type handlerAdapter struct {
	h *Handler
}

func (a *handlerAdapter) Handle(req httpapi.RPCRequest) httpapi.RPCResponse {
	resp := a.h.Handle(Request{
		Method: req.Method,
		Params: req.Params,
	})
	return httpapi.RPCResponse{
		Result: resp.Result,
		Error:  resp.Error,
	}
}
