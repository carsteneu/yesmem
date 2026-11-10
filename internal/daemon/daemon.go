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
	"github.com/carsteneu/yesmem/internal/logrotate"
	"github.com/carsteneu/yesmem/internal/repo"
	"github.com/carsteneu/yesmem/internal/sanitize"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/update"
)

type briefingRefreshTarget struct {
	Project     storage.ProjectSummary
	Raw         string
	Fingerprint string
}

// excludeProjectsFor returns the config's project exclusion list and is the only
// place the daemon start reads that field. config.Load falls back to defaults on
// every error path and never returns nil today, so the nil branch is defensive:
// it keeps the read in one guarded place instead of guarding it at each of the
// two call sites, only one of which had a check.
func excludeProjectsFor(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.ExcludeProjects
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

// wrapWithSanitizationIfEnabled wraps c with a SanitizingClient when sanitization
// is enabled. Returns c unchanged when c, cfg, or r is nil, or when
// cfg.SecretsSanitization.Enabled is false.
func wrapWithSanitizationIfEnabled(c extraction.LLMClient, cfg *config.Config, r sanitize.Sanitizer) extraction.LLMClient {
	if c == nil || cfg == nil || !cfg.SecretsSanitization.Enabled || r == nil {
		return c
	}
	return extraction.NewSanitizingClient(c, r)
}

// ReadClaudeCodeAPIKey reads the API key from Claude Code's config.json.
//
// The file holds a live provider key, and this read is the daemon's one contact
// with it, so it also takes the key out of reach of other local users: Claude Code
// has been observed leaving the file at mode 0777 (YM-303).
func ReadClaudeCodeAPIKey() string {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "config.json")
	hardenSecretFileMode(path)
	data, err := os.ReadFile(path)
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

// hardenSecretFileMode narrows a file that carries a key to 0600 when it is group
// or world readable. Best effort: a file owned by another user, or one on a
// read-only mount, keeps its mode — that must not stop the read that follows.
//
// Only the file itself is narrowed, never a path that merely leads to one: a run
// can write ~/.claude, so a symlink or a hard link placed at this path would aim
// the chmod at an inode of its choosing (a directory loses its x bit and everything
// below becomes unreachable). The mode is changed on the file descriptor, not on
// the path, so a swap between the check and the chmod cannot retarget it.
func hardenSecretFileMode(path string) {
	// A FIFO or a directory is not this file; opening a FIFO would block.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			log.Printf("[sandbox] not narrowing %s: it is not a regular file", path)
		}
		return
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0o077 == 0 {
		return
	}
	if sys, ok := stat.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		log.Printf("[sandbox] not narrowing %s: it has %d hard links", path, sys.Nlink)
		return
	}
	if err := file.Chmod(0o600); err != nil {
		log.Printf("[sandbox] WARNING: %s is readable by other users and could not be narrowed to 0600: %v", path, err)
		return
	}
	log.Printf("[sandbox] narrowed %s to 0600 (it carries a provider key)", path)
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
	PprofEnabled     bool   // config diagnostics.pprof or YESMEM_PPROF: start the localhost pprof endpoint
	PprofListen      string // from config, default "127.0.0.1:7071"
	CapsDir          string // CAP.md file directory — defaults to DataDir/../caps

	// TerminalsSave, if set, runs 1 minute after daemon start and then every
	// 15 minutes to keep terminals.json resume-ready. Wired in main (main.go)
	// because internal/terminals imports this package back.
	TerminalsSave func()

	// LiveWindows lists every mapped window (id, desktop, owner pid) with one
	// window-manager call; err means "can't tell". Used by whoami, agent
	// placement and to prune windows.json. Wired in main for the same reason
	// as TerminalsSave.
	LiveWindows func() ([]LiveWindow, error)
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

	// Set up log file with rotation (50MB default, keeps 5 timestamped backups)
	logPath := filepath.Join(cfg.DataDir, "logs", "daemon.log")
	logWriter, err := logrotate.New(logPath)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, logWriter))
		defer logWriter.Close()
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Println("  YesMem Daemon", buildinfo.Version)
	log.Printf("  Log: %s", logPath)
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Databases that never pass through openSQLite (guard_state.db, caps.db) and
	// the legacy stubs of older installations keep the umask mode until a sweep
	// restricts them (audit 16.10.2026, YM-338). After the log is attached, so the
	// result is visible in daemon.log. Best effort: a file on a foreign mount must
	// not keep the daemon from starting.
	if hardened, err := storage.HardenDBFilesInDir(cfg.DataDir); err != nil {
		log.Printf("[warn] database mode sweep: %d file(s) restricted, %v", hardened, err)
	} else if hardened > 0 {
		log.Printf("Restricted %d database files to owner-only", hardened)
	}

	// Open storage (has data from previous runs — MCP can serve immediately)
	store, err := storage.Open(filepath.Join(cfg.DataDir, "yesmem.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	consolidator := newConsolidationRunner(store)
	if err := consolidator.Baseline(); err != nil {
		log.Printf("[warn] consolidation baseline: %v", err)
	}

	if err := store.MigrateAgentsSchema(); err != nil {
		log.Printf("[warn] agents schema migration: %v", err)
	}

	if err := store.OpenCapsDB(cfg.DataDir); err != nil {
		log.Printf("[warn] cap_store open: %v", err)
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

	// Load config early — needed for indexer exclude lists and agent settings
	ac, _ := config.Load(filepath.Join(cfg.DataDir, "config.yaml"))

	// Archiver + Indexer
	archDir := ac.ArchiveDir(cfg.DataDir)
	// A configured paths.archive is used unchecked otherwise, and a failure is
	// only logged per session, where the watermark then keeps it from being
	// retried. Say it once at startup instead.
	if err := archive.EnsureBaseDir(archDir); err != nil {
		log.Printf("[archive] %v: archiving will fail until it is writable", err)
	}
	arch := archive.New(archDir)
	excludeProjects := excludeProjectsFor(ac)
	idx := indexer.New(store, bloomMgr, arch, excludeProjects)

	// Index progress tracking (atomics — safe for concurrent reads)
	var indexTotal, indexDone, indexSkipped int64
	var indexRunning int32                       // 1 = running, 0 = done
	var extractionActive int32                   // >0 = extraction goroutines running
	batchExtractNotify := make(chan struct{}, 1) // non-blocking signal for batch trigger

	// ━━━ Socket server FIRST — MCP available immediately ━━━
	handler := newProductionHandler(store, bloomMgr, ac.ClaudeProjectsDir())
	handler.dataDir = cfg.DataDir
	handler.RebuildFromPIDFiles()
	handler.liveWindows = cfg.LiveWindows
	handler.RebuildWindowMap()
	// Liveness check calls the window manager: never on the startup path.
	go handler.PruneDeadWindows()
	if ac != nil {
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
		if ac.Agents.DefaultBackend != "" {
			handler.agentDefaultBackend = ac.Agents.DefaultBackend
		}
		handler.agentPermissionKick = ac.Agents.PermissionKick
		handler.agentThemes = mergeAgentThemes(ac.Agents.Themes)
		handler.SetDoneVerifyConfig(&ac.DoneVerify)
		if d, err := time.ParseDuration(ac.Agents.PermissionKickDelay); err == nil && d > 0 {
			handler.agentPermissionKickDelay = d
		}
		if d, err := time.ParseDuration(ac.Agents.PermissionKickInterval); err == nil && d > 0 {
			handler.agentPermissionKickInterval = d
		}
		if ac.DefaultSandboxProfile != "" {
			if p, err := ParseSandboxProfile(ac.DefaultSandboxProfile); err == nil {
				handler.defaultSandboxProfile = p
				handler.sandboxDefaultSet = true
				if p == ProfileNone {
					log.Printf("[scheduler] default_sandbox_profile=none: scheduled jobs run unsandboxed")
				}
			} else {
				log.Printf("[scheduler] invalid default_sandbox_profile %q, jobs get standard: %v", ac.DefaultSandboxProfile, err)
			}
		}
		// Sandboxed agent runs reach the LLM through the proxy on the host
		// loopback; ai-jail 2.x needs that port relayed into the sandbox.
		handler.proxyForwardPort = proxyForwardPort(ac.Proxy.Listen)
		if ac.SecretsSanitization.Enabled {
			handler.redactor = sanitize.NewSecretRedactor(ac.SecretsSanitization.AllowedExceptions)
		}
	}
	LoadPlansFromDB(store)

	// Opencode DB scanner for extraction pipeline
	ocDBPath := ""
	if ac != nil {
		ocDBPath = ac.Paths.OpencodeDB
	}
	if ocDBPath == "" {
		ocDBPath = filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode", "opencode.db")
	}
	handler.SetOpencodeDBPath(ocDBPath)
	if _, err := os.Stat(ocDBPath); err == nil {
		ocScanner := indexer.NewOpencodeScanner(ocDBPath, store, log.Default(), batchExtractNotify, excludeProjects, arch)
		handler.SetOpencodeScanner(ocScanner)
		log.Printf("opencode scanner: watching %s", ocDBPath)

		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if ocScanner.MaybeScan() > 0 {
					handler.InvalidateProjectCache()
				}
			}
		}()
	}

	// Recover agents from before daemon restart — keep living, keep resumable
	// opencode sessions, delete only what has neither.
	reconnected, reattachable, deleted, err := store.AgentRecoverOrphaned(
		func(pid int) bool { return syscall.Kill(pid, 0) == nil },
		opencodeSessionResumableFn,
	)
	if err == nil && (reconnected > 0 || reattachable > 0 || deleted > 0) {
		log.Printf("Agent recovery: %d reconnected, %d kept (resumable or unverified), %d deleted (daemon restart)", reconnected, reattachable, deleted)
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

	// Proof of life for systemd (Type=notify). The watchdog pings only while a
	// probe confirms the socket answers: a daemon that lives on with a dead socket
	// is the state Restart=always cannot see. Without a notify socket nobody is
	// listening, so the daemon runs without the extra probe. The address is read
	// once and then removed from the environment, so it does not reach the agents
	// and sandbox jobs this daemon spawns.
	if addr, supervised := notifyAddr(); supervised {
		// The interval comes from the unit's WatchdogSec before the variables are
		// stripped, so a hand-tuned deadline is honoured instead of outrun.
		interval := watchdogInterval()
		_ = sendNotifyTo(addr, "READY=1")
		watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
		defer watchdogCancel()
		startDaemonWatchdog(watchdogCtx, SocketPath(cfg.DataDir), interval, func(state string) error {
			return sendNotifyTo(addr, state)
		})
		stripNotifyVars()
	}

	// Restricted socket for sandboxed runs (YM-267). A job gets this one mounted
	// instead of the full socket, so it can reach its cap store and the LLM
	// bridge but cannot start anything the sandbox would not have started
	// itself. It lives in the runtime dir beside the data dir, which the sandbox
	// masks as a whole (YM-276). A failure here costs jobs their socket, not the
	// daemon its own.
	// The job socket lived inside the data dir before YM-276. A leftover there
	// would still look like a socket to external checks while nothing serves it.
	legacyJobSock := filepath.Join(cfg.DataDir, "job.sock")
	if _, statErr := os.Lstat(legacyJobSock); statErr == nil {
		os.Remove(legacyJobSock)
	}

	if _, dirErr := EnsureRuntimeDir(cfg.DataDir); dirErr != nil {
		log.Printf("warn: job runtime dir: %v (sandboxed caps cannot reach the daemon)", dirErr)
	} else if jobSrv, jobErr := NewSocketServerAt(JobSocketPath(cfg.DataDir), handler.handleJobSocket); jobErr != nil {
		log.Printf("warn: restricted job socket: %v (sandboxed caps cannot reach the daemon)", jobErr)
	} else {
		go jobSrv.Serve()
		defer jobSrv.Close()
	}

	// Sync CAP.md files from disk into DB (user-scoped caps)
	userCapsDir := config.CapsDirPath(cfg.CapsDir, cfg.DataDir)
	// The sandbox masks this very directory (newSandbox): a run that can write a
	// CAP.md here reaches past the sandbox, because the watcher below re-imports it
	// and later runs the edited script. The handler needs the configured value,
	// not just the default derived from the data dir.
	handler.capsDir = userCapsDir
	go func() {
		// YM-230: report what the stored caps inherit before the import below adds
		// rows that never ran unsandboxed.
		logSandboxProfilesOnLoad(handler)
		SyncCapsFromDisk(handler, userCapsDir, "")
		ExportAllCaps(handler, userCapsDir)
	}()

	// Periodic CAP.md watcher — re-imports changed files every 30s
	go func() {
		watcher := NewCapsDirWatcher()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			changed := watcher.ScanChanged(userCapsDir)
			for _, cf := range changed {
				params := CapFileToParams(cf)
				resp := handler.handleSaveCapFrom(params, cf.SourcePath)
				if resp.Error != "" {
					log.Printf("[cap-watch] save %s: %s", cf.Name, resp.Error)
				} else if unchangedResponse(resp) {
					// The mtime moved, the content did not: reporting an import here
					// claims a change that never happened.
					log.Printf("[cap-watch] %s unchanged (disk v%d: %s)", cf.Name, cf.Version, unchangedReason(resp))
				} else {
					log.Printf("[cap-watch] imported %s from disk", cf.Name)
				}
				watcher.RefreshMtime(cf.SourcePath)
			}
		}
	}()

	// Periodic terminal snapshot — keeps terminals.json resume-ready even
	// when windows never fire hooks (idle opencode/codex sessions).
	if cfg.TerminalsSave != nil {
		go func() {
			time.AfterFunc(time.Minute, cfg.TerminalsSave)
			ticker := time.NewTicker(15 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				cfg.TerminalsSave()
			}
		}()
	}

	// ━━━ Scheduler ━━━
	var sched *Scheduler
	sched = newScheduler(handler.store, func(job ScheduledJob) {
		log.Printf("[scheduler] firing job %s: %s", job.Name, job.Prompt)
		defer sched.JobDone(job.ID)
		handler.executeScheduledPrompt(job)
		if !job.Recurring {
			// The row is already gone (Tick removed it), so there is nothing to stamp.
			// The log directory stays: it is the only file-level record of the run.
			return
		}
		_ = handler.store.UpdateJobLastRun(job.ID, time.Now())
	})
	handler.scheduler = sched

	// A one-shot row that fired before the persistence hook existed is still in the
	// table; loading it would fire it a second time right after the upgrade.
	if removed, err := handler.store.DeleteFiredOneShotJobs(); err != nil {
		log.Printf("[scheduler] cleanup fired one-shot jobs: %v", err)
	} else if removed > 0 {
		log.Printf("[scheduler] removed %d already fired one-shot jobs", removed)
	}

	// Load persisted jobs from DB
	if dbJobs, err := handler.store.ListScheduledJobs(); err != nil {
		// Without a log line a broken query leaves the daemon running with no jobs
		// at all, and nothing says so.
		log.Printf("[scheduler] load persisted jobs: %v", err)
	} else {
		for _, dj := range dbJobs {
			sched.AddJob(ScheduledJob{
				ID: dj.ID, Name: dj.Name, Cron: dj.Cron,
				Prompt: dj.Prompt, Enabled: dj.Enabled, Recurring: dj.Recurring, Mode: dj.Mode,
				CapName: dj.CapName, ScriptName: dj.ScriptName, Project: dj.Project, AutoCorrect: dj.AutoCorrect, AllowedPorts: dj.AllowedPorts,
				AllowedHosts: validatedHosts(dj.AllowedHosts),
				Sandbox:      jobSandboxProfile(dj.Sandbox), IntervalSeconds: dj.IntervalSeconds, Backend: dj.Backend, Model: dj.Model, LastRun: dj.LastRun,
			})
		}
		if len(dbJobs) > 0 {
			log.Printf("[scheduler] loaded %d jobs from DB", len(dbJobs))
		}
	}

	// Scheduler tick goroutine
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case t := <-ticker.C:
				sched.Tick(t)
			case <-schedCtx.Done():
				return
			}
		}
	}()

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
			handler.httpRPCAddr = listen
			handler.httpAuthToken = authToken
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

	// ━━━ pprof diagnostics (optional, loopback only) ━━━
	if srv, err := startPprofIfEnabled(cfg.PprofEnabled, cfg.PprofListen); err != nil {
		log.Printf("warn: pprof diagnostics not started: %v", err)
	} else if srv != nil {
		defer shutdownServer(srv)
	}

	// Proxy runs as separate systemd unit (yesmem-proxy.service), not embedded here.

	// Context for graceful shutdown of background workers
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	_ = daemonCancel // used at shutdown

	// Keep scheduler execution history bounded. Pending errors are retained
	// indefinitely so diagnosis and auto-correction cannot lose work.
	go func() {
		const (
			bashRunCleanupInterval  = 24 * time.Hour
			nonErrorRetention       = 24 * time.Hour
			processedErrorRetention = 30 * 24 * time.Hour
		)
		cleanup := func() {
			now := time.Now()
			deleted, err := store.PurgeBashJobRuns(
				now.Add(-nonErrorRetention),
				now.Add(-processedErrorRetention),
			)
			if err != nil {
				log.Printf("[scheduler] bash run cleanup failed: %v", err)
			} else if deleted > 0 {
				log.Printf("[scheduler] purged %d expired bash job runs (retention: %s non-error, %s processed-error)",
					deleted, nonErrorRetention, processedErrorRetention)
			}
			// Closed agent rows are the other table that only grew: every probe,
			// crash and finished loop left one behind (YM-312).
			pruned, pruneErr := store.PruneStoppedAgents(now.Add(-storage.AgentPruneRetention))
			if pruneErr != nil {
				log.Printf("[agents] prune failed: %v", pruneErr)
			} else if pruned > 0 {
				log.Printf("[agents] pruned %d closed agent row(s) older than %s", pruned, storage.AgentPruneRetention)
			}
		}

		cleanup()
		ticker := time.NewTicker(bashRunCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()

	// FTS5 background sync — replaces triggers to avoid write contention on BM25 reads
	store.StartFTSSync(daemonCtx, 10*time.Second)

	// WAL guard — heals SQLite log files that an external writer truncated or
	// unlinked while this process held them open (YM-352)
	store.StartWALGuard(daemonCtx, time.Minute)

	// Agent heartbeat — polls for messages targeting running agents, relays via inject socket
	go handler.startAgentHeartbeat(daemonCtx)

	// Wiki render ticker — rebuilds wiki for all active projects every 5min
	startWikiTicker(daemonCtx, store)

	// Scan bookkeeping prune — drops rows for projects that no longer exist
	startScanPruneTicker(daemonCtx, store)

	// Session pin sweep — bounds pins of sessions whose end no hook reports
	go startSessionPinSweep(daemonCtx, store, storage.SessionPinMaxAge)

	// CBM daemon keeper — attach CLI calls to one permanent CBM daemon
	startCBMDaemonKeeper(daemonCtx, store)

	// CBM GC task — daily cleanup of orphaned worktree index DBs
	startCBMGCTask(daemonCtx)

	// Extractor holder — set asynchronously after config is loaded
	var (
		extMu          sync.Mutex
		extractor      extraction.SessionExtractor
		evoExtractor   *extraction.Extractor
		llmClient      extraction.LLMClient
		qualityLLM     extraction.LLMClient
		appCfg         *config.Config
		apiGateRef     *extraction.APIGate
		extractTracker *extraction.BudgetTracker
	)

	sessionRoots := cfg.SessionSources
	if len(sessionRoots) == 0 {
		sessionRoots = []string{cfg.ProjectsDir}
		if info, err := os.Stat(cfg.CodexSessionsDir); err == nil && info.IsDir() {
			sessionRoots = append(sessionRoots, cfg.CodexSessionsDir)
		}
	}

	// One pool for every session root: indexing happens off the watcher's
	// dispatch loop with a bounded worker count, so a large session no longer
	// blocks the small ones behind it and two large ones never expand at once.
	indexPool := indexer.NewIndexPool(idx.IndexSession, indexer.DefaultIndexWorkers,
		indexer.LargeSessionBytes, func(path string) {
			handler.InvalidateProjectCache()
		})

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
				indexPool.Submit(path)
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

		// New sessions may introduce project paths → invalidate resolution cache.
		if totalIndexed > 0 {
			handler.InvalidateProjectCache()
		}

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

		// Set up LLM clients.
		//
		// The trackers, the api gate and the per-call cap are shared by every
		// daemon LLM path, so they are built before the clients:
		// newDaemonLLMClient applies the whole decoration in one step and no
		// handle can end up outside the budget/gate/redaction chain.
		spendAdapter := &storage.SpendAdapter{Store: store}

		// Shared extraction budget tracker (used by summarize + extract clients,
		// and read by the gap review so it meters against the same counter)
		if ac.LLM.DailyBudgetExtractUSD > 0 {
			tracker := extraction.NewPersistentBudgetTracker(ac.LLM.DailyBudgetExtractUSD, "extract", spendAdapter)
			extMu.Lock()
			extractTracker = tracker
			extMu.Unlock()
			log.Printf("Daily extraction budget: $%.2f", ac.LLM.DailyBudgetExtractUSD)
		}
		// Throttle function: blocks LLM calls when API utilization exceeds 50%
		throttleFn := func() bool {
			return proxyRateLimitThrottled(store, 0.5)
		}

		// The narrative/quality path is a separate handle only when it uses a
		// different model; its budget bucket exists under the same condition.
		separateQualityModel := ac.NarrativeModelID() != ac.ModelID()
		var qualityTracker *extraction.BudgetTracker
		if separateQualityModel && ac.LLM.DailyBudgetQualityUSD > 0 {
			qualityTracker = extraction.NewPersistentBudgetTracker(ac.LLM.DailyBudgetQualityUSD, "quality", spendAdapter)
			log.Printf("Daily quality budget: $%.2f", ac.LLM.DailyBudgetQualityUSD)
		}

		// ━━━ Global API Health Gate ━━━
		apiGate := extraction.NewAPIGate(extractTracker, qualityTracker, 5, 0.50)
		apiGateRef = apiGate
		// Feed actual token usage from API responses into budget tracking
		extraction.OnUsage = func(model string, inputTokens, outputTokens int) {
			inputPerM, outputPerM := ac.PricingForModel(model)
			if extractTracker != nil {
				extractTracker.TrackTokens(inputTokens, outputTokens, inputPerM, outputPerM)
			}
		}
		log.Printf("[api-gate] Global health gate active (threshold=5, reserve=$0.50)")

		// Set per-call budget limit on CLI clients (safety net via --max-budget-usd)
		env := daemonLLMEnv(apiKey, ac.LLM.ClaudeBinary, baseURL, extractTracker, apiGate, throttleFn, ac.LLM.MaxBudgetPerCallUSD)

		// Extraction client (Sonnet default) — extraction, evolution, cluster
		// distillation, recurrence detection, persona signals
		client, clientErr := newDaemonLLMClient(ac, meteredOpts(env, ac.ModelID(), "extract"))
		if clientErr != nil {
			log.Printf("LLM client error: %v", clientErr)
		}

		// Quality client (Opus default) — narratives, profiles, persona synthesis.
		// Without a separate narrative model the handle shares the extraction
		// tracker: there is no quality budget to meter against, and the gate's
		// quality bucket stays budget-neutral, exactly as before.
		qualityOpts := meteredOpts(env, ac.ModelID(), "quality")
		if separateQualityModel {
			qualityOpts.model = ac.NarrativeModelID()
			qualityOpts.tracker = qualityTracker
		}
		qualityClient, qcErr := newDaemonLLMClient(ac, qualityOpts)
		if qcErr != nil {
			log.Printf("Quality LLM client error (falling back to extraction model): %v", qcErr)
			qualityClient = client
		}

		// New briefing keys reach an existing installation only through setup, and
		// LoadStrings then fills them with English defaults. The backfill translates
		// the missing keys into the language the file already uses. Off the start
		// path: a slow or failing model must not delay the daemon (YM-338). The
		// directory follows the configured DB path, the same one the briefing
		// renderer reads (regenerateBriefingsForTargets).
		stringsDir := cfg.DataDir
		if ac.Paths.DB != "" {
			stringsDir = filepath.Dir(ac.Paths.DB)
		}
		go func() {
			if err := backfillStringsOnStartup(stringsDir, client); err != nil {
				log.Printf("[i18n] strings.yaml backfill: %v", err)
			}
		}()

		// Auto-correct client — the bash-job repair pass asks the model to repair
		// a failing cap script. Same decoration as the other metered paths: the
		// prompt carries the failure excerpt and the cap metadata, never the
		// script body, so it goes through the redactor like every other prompt
		// (see autoCorrectPrompt).
		autoCorrectOpts := meteredOpts(env, ac.ModelID(), "extract")
		if acClient, acErr := newDaemonLLMClient(ac, autoCorrectOpts); acErr != nil {
			log.Printf("Auto-correct LLM client error: %v", acErr)
		} else if acClient == nil {
			log.Printf("Auto-correct LLM client unavailable (no provider/API key): failed bash jobs stay queued without a diagnosis")
		} else {
			handler.SetAutoCorrectClient(acClient)
		}

		// Always create a lightweight summarize client (Haiku) for on-demand tasks
		// (rules condensation, doc destillation) — independent of extraction cycle
		if apiKey != "" {
			summarizeModel := ac.SummarizeModelID()
			sc, scErr := newDaemonLLMClient(ac, llmClientOpts{
				apiKey:       apiKey,
				model:        summarizeModel,
				claudeBinary: ac.LLM.ClaudeBinary,
				baseURL:      baseURL,
			})
			if scErr == nil && sc != nil {
				handler.SetSummarizeClient(sc)
				log.Printf("Summarize client ready: %s (for rules/destillation)", summarizeModel)
			}
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
			handler.SetIVFPath(filepath.Join(cfg.DataDir, "yesmem.ivf"))
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

			// Always create single-pass extractor for evolution. The extraction
			// client already carries the redactor (newDaemonLLMClient), so it is
			// used as-is — a second layer would only duplicate the redaction.
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
					sc, scErr := newDaemonLLMClient(ac, llmClientOpts{
						apiKey:       apiKey,
						model:        summarizeModel,
						claudeBinary: ac.LLM.ClaudeBinary,
						baseURL:      baseURL,
						tracker:      extractTracker,
						throttle:     throttleFn,
					})
					if scErr != nil || sc == nil {
						log.Printf("Summarize client unavailable, using extraction model: %v", scErr)
						summarizeClient = client
					} else {
						summarizeClient = sc
					}
				}

				// Pass 2: Extract client — uses QualityModelID
				extractModel := ac.QualityModelID()
				extractClient, extractErr := newDaemonLLMClient(ac, llmClientOpts{
					apiKey:       apiKey,
					model:        extractModel,
					claudeBinary: ac.LLM.ClaudeBinary,
					baseURL:      baseURL,
					tracker:      extractTracker,
					throttle:     throttleFn,
				})
				if extractErr == nil && extractClient != nil {
					ext = extraction.NewTwoPassExtractor(summarizeClient, extractClient, store)
					log.Printf("Extraction mode: two-pass (summarize=%s, extract=%s)", summarizeClient.Model(), extractClient.Model())
					// Make summarize client available for doc destillation
					handler.SetSummarizeClient(summarizeClient)
					handler.SetCommitEvalClient(extractClient)
				} else {
					ext = evoExt
					log.Printf("Extraction mode: single-pass fallback (quality client unavailable)")
				}
			} else {
				ext = evoExt
				log.Printf("Extraction mode: single-pass (%s)", client.Model())
			}
			ext.SetChunkSize(ac.Extraction.ChunkSize)

			// Always wire handler LLM fields for llm_complete RPC,
			// regardless of extraction mode.
			handler.SetQualityClient(qualityClient)
			wiringBaseURL := baseURL
			if ac.LLM.OpenAIBaseURL != "" {
				wiringBaseURL = ac.LLM.OpenAIBaseURL
			}
			// An llm_complete call that overrides model or provider gets a client
			// with the same metering, api gate, per-call cap and redactor as the
			// daemon's own paths — not a bare client outside the budget.
			handler.SetLLMRouting(ac.LLM.Provider, ac.LLM.CompleteProvider, wiringBaseURL, func(provider, model string) (extraction.LLMClient, error) {
				o := meteredOpts(env, model, "extract")
				o.provider = provider
				o.baseURL = wiringBaseURL
				return newDaemonLLMClient(ac, o)
			})

			extMu.Lock()
			extractor = ext
			evoExtractor = evoExt
			llmClient = client
			qualityLLM = qualityClient
			appCfg = ac
			extMu.Unlock()

			log.Printf("Quality model: %s", qualityClient.Model())
			runInitialExtraction(ext, evoExt, store, ac, client, qualityClient, handler)

			// Consolidate only scopes changed since the durable startup baseline.
			go func() {
				result, ran, err := consolidator.RunIfDirty()
				if err != nil {
					log.Printf("[warn] startup consolidation: %v", err)
				} else if ran {
					log.Printf("💤 Incremental startup consolidation done: %d checked, %d superseded, %d bigram and %d embedding comparisons (%d dimensions)",
						result.TotalChecked, result.TotalSuperseded, result.BigramComparisons, result.EmbeddingComparisons, result.EmbeddingDimensions)
				}

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
		rulesClient := handler.qualityLLMClient()
		if rulesClient == nil {
			rulesClient = handler.summarizeLLM()
		}
		RunRulesRefresh(store, rulesClient)
		startRulesWatch(daemonCtx, store, rulesClient)
	}()

	// ━━━ Staleness tick — periodic git commit scan, LLM-based learning freshness evaluation ━━━
	go func() {
		// Wait for CommitEvalClient (set asynchronously during extraction init).
		// Give up after 2 minutes if the client never materializes (e.g. LLM disabled).
		const stalenessStartupTimeout = 2 * time.Minute
		deadline := time.After(stalenessStartupTimeout)
		for handler.commitEvalLLM() == nil {
			select {
			case <-daemonCtx.Done():
				return
			case <-deadline:
				log.Printf("[staleness-tick] CommitEvalClient not available after %v, disabling staleness scan", stalenessStartupTimeout)
				return
			case <-time.After(10 * time.Second):
			}
		}
		startStalenessTick(daemonCtx, handler, cfg.ProjectsDir)
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
		extMu.Lock()
		gate := apiGateRef
		extMu.Unlock()
		briefingClient, _ := newDaemonLLMClient(ac, llmClientOpts{
			apiKey:       apiKey,
			model:        model,
			claudeBinary: ac.LLM.ClaudeBinary,
			gate:         gate,
			gateBucket:   "quality",
		})

		// Initial generation on daemon start only for projects with missing or changed cache entries.
		if targets, err := listProjectsNeedingBriefingRefresh(store, ac); err != nil {
			// Its own branch: without it a failed scan reaches the else below and
			// reports "cache present", which is the opposite of what happened now
			// that the fingerprint and hash reads surface their errors.
			log.Printf("[briefing] refine: startup scan failed: %v", err)
		} else if len(targets) > 0 {
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
	// Runs every 2h or when the watcher reports a settled session.
	// Back-to-back processing enables Anthropic Prompt Cache reuse (5min TTL).
	go func() {
		ticker := time.NewTicker(2 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
			case <-batchExtractNotify:
			}
			// Check pending count — the ticker fires unconditionally, and a notify
			// arrives per settled session, so there is no threshold to apply here.
			pending, err := store.CountPendingExtractions(storage.ShortSessionMaxMessages)
			if err != nil {
				log.Printf("warn: pending extraction count: %v", err)
			}
			if pending == 0 {
				// A session that a fork marked as extracted can still carry an
				// uncovered tail; it has no extracted_at gap to make it pending.
				tails, err := store.CountUncoveredTails(forkCoverageSlack)
				if err != nil {
					log.Printf("warn: uncovered-tail count: %v", err)
				}
				if tails == 0 && err == nil {
					continue
				}
			}
			// Throttle extraction when API utilization is high
			if rl := readRateLimits(store); rl != nil && rl.ShouldThrottle(0.5) {
				log.Printf("━━━ Extraction deferred: utilization %.0f%% (threshold 50%%) ━━━", rl.Utilization()*100)
				continue
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
			// The durable high-watermark makes unchanged calls free and the runner
			// coalesces a concurrent startup pass into the same serialized run.
			result, ran, err := consolidator.RunIfDirty()
			if err != nil {
				log.Printf("[warn] batch consolidation: %v", err)
			} else if ran {
				log.Printf("  Incremental batch consolidation: %d checked, %d superseded, %d bigram and %d embedding comparisons (%d dimensions)",
					result.TotalChecked, result.TotalSuperseded, result.BigramComparisons, result.EmbeddingComparisons, result.EmbeddingDimensions)
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

	// Orphaned proxy rows — a thread that neither invalidates its frozen prefix
	// nor comes back for it leaves the row (several megabytes) in proxy_state
	// forever: the proxy eviction is deliberately in-memory only and never
	// rewrites a row from the request path.
	//
	// The age is far above the prefix RAM TTL (65 minutes for a 1h cache TTL)
	// because a long-lived thread keeps a byte-identical prefix and only rewrites
	// the row when its bytes change, so an old updated_at does not prove the
	// thread is gone. Such a thread pays for the deletion in two ways: a restart
	// inside the gap rebuilds the prefix from scratch, and so does a live process
	// after Evict drops loadedFromDB. Both are one cache miss, not a state loss;
	// ensureFrozenPersisted re-persists the row on the next restore turn. Rows
	// older than a day cannot carry a warm upstream cache anyway: keepalive pings
	// stop after about 22 minutes.
	go func() {
		const frozenRowMaxAge = 24 * time.Hour
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-daemonCtx.Done():
				return
			case <-ticker.C:
				for _, prefix := range []string{"frozen:", frozenMarkersKeyPrefix} {
					n, err := store.DeleteProxyStatePrefixOlderThan(prefix, frozenRowMaxAge)
					if err != nil {
						log.Printf("[janitor] reclaim %s*: %v", prefix, err)
					} else if n > 0 {
						log.Printf("[janitor] reclaimed %d orphaned %s* rows", n, prefix)
					}
				}
			}
		}
	}()

	// Recurrence detection — every 2h. The new-learning gate lives inside
	// DetectRecurrence (persisted cursor), so both callers share one rule.
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
				alerts := DetectRecurrence(store, cl)
				if alerts > 0 {
					log.Printf("[recurrence] done: %d alerts", alerts)
				}
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
	// gapReviewTracker reads the shared tracker under the same lock the config
	// holder uses: it is written by the async setup goroutine, so an unguarded
	// read here would race with that write.
	gapReviewTracker := func() *extraction.BudgetTracker {
		extMu.Lock()
		defer extMu.Unlock()
		return extractTracker
	}

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
			runGapReviewDaemon(store, &extMu, &appCfg, gapReviewTracker())
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
			runGapReviewDaemon(store, &extMu, &appCfg, gapReviewTracker())
		}
	}()

	// Auto-update check
	go func() {
		interval, enabled := updateTickerDecision(daemonCtx, &extMu, &appCfg)
		if !enabled {
			return
		}
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
					// Not only a failed release check lands here: a refused lock and
					// a rolled-back migration do too.
					log.Printf("[update] failed: %v", err)
					continue
				}
				if newVersion != "" {
					log.Printf("[update] updated to %q, restarting...", newVersion)
					// The restart takes the update lock now, so it can be refused
					// while a deploy or a second updater holds it: the new binary
					// is installed but not active. This goroutine ends here, so the
					// process makes no further automatic attempt; %q because the
					// version comes from the release JSON.
					if err := update.RunRestart(log.Default()); err != nil {
						log.Printf("[update] restart failed, %q is installed but not active: %v", newVersion, err)
					}
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

// backfillStringsOnStartup adds the briefing keys a new version introduced to an
// existing strings.yaml, translated into the language the file already uses.
//
// Setup owns a missing file: without one there is no language to match, and
// writing English defaults would cement the exact half-translated state this
// backfill removes. The model is required; without a client the daemon starts
// unchanged.
func backfillStringsOnStartup(dataDir string, client extraction.LLMClient) error {
	if client == nil {
		return nil
	}
	path := filepath.Join(dataDir, "strings.yaml")
	missing, err := briefing.MissingStringKeys(path)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	prompt, err := briefing.BuildMissingStringsPrompt(path, missing)
	if err != nil {
		return err
	}
	response, err := client.Complete("You are a translator. Return ONLY valid YAML, no explanation.", prompt)
	if err != nil {
		return err
	}
	merged, err := briefing.MergeStringKeys(path, response, missing)
	if err != nil {
		return err
	}
	if merged > 0 {
		log.Printf("[i18n] strings.yaml: %d missing keys translated into the file's language", merged)
	}
	return nil
}

// regenerateAllBriefings generates refined briefings for all known projects.
func regenerateAllBriefings(store *storage.Store, cfg *config.Config, llmClient extraction.LLMClient) {
	projects, err := store.ListProjectsWithRepositoryRoots()
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
			raw = briefing.NewFromConfig(store, cfg).GenerateForKey(p.ProjectShort)
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

// briefingInactivityThreshold: projects whose most recent session is older
// than this are skipped by listProjectsNeedingBriefingRefresh. Saves LLM
// refinement calls on long-dormant projects.
const briefingInactivityThreshold = 30 * 24 * time.Hour

// briefingLastActiveAt is the activity timestamp the refresh loop ranks by. A
// synthetic repository-root row owns no session, so the exact-match lookup in the
// sessions table returns zero for it; its worktree copies are what moved, and the
// row's aggregated last_active is the only source. Zero stays "unknown" and fails
// open, as before.
func briefingLastActiveAt(store *storage.Store, project storage.ProjectSummary) time.Time {
	if last := projectLastActiveAt(context.Background(), store, project.ProjectShort); !last.IsZero() {
		return last
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, project.LastActive); err == nil {
			return t
		}
	}
	return time.Time{}
}

func listProjectsNeedingBriefingRefresh(store *storage.Store, cfg *config.Config) ([]briefingRefreshTarget, error) {
	projects, err := store.ListProjectsWithRepositoryRoots()
	if err != nil {
		return nil, err
	}

	targets := make([]briefingRefreshTarget, 0, len(projects))

	for _, project := range projects {
		if project.ProjectShort == "" {
			continue
		}
		// Dead-path filter: /tmp/* opencode session dirs and removed worktrees.
		// Same predicate as the wiki-tick filter so the two loops agree.
		if !isLiveProjectPath(project.ProjectShort) {
			continue
		}
		// Inactivity filter: skip projects dormant longer than the threshold.
		// Uses the sessions table's MAX(started_at); falls back to "include"
		// when the timestamp can't be parsed so we fail open.
		if last := briefingLastActiveAt(store, project); !last.IsZero() &&
			time.Since(last) > briefingInactivityThreshold {
			continue
		}
		// Lightweight change detection: compare DB fingerprint instead of
		// generating the full raw briefing (which contains volatile timestamps).
		fingerprint, err := store.ProjectChangeFingerprint(project.ProjectShort)
		if err != nil {
			// One unreadable project must not cost the whole scan: the other
			// projects are healthy and the next attempt is 24h away.
			log.Printf("[briefing] refine: fingerprint for %q failed: %v", project.ProjectShort, err)
			continue
		}
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
	// Read the chunk watermark before listing the sources: the sweep for a
	// source without a path leaves every row above it alone, because a
	// concurrent pass may have written them after this snapshot (same rule as
	// ingest.Run). 0 disables that sweep.
	sweepWatermark, watermarkErr := store.MaxDocChunkID()
	if watermarkErr != nil {
		log.Printf("[doc-sync] chunk watermark: %q; sources without a path will not be swept", watermarkErr)
		sweepWatermark = 0
	}
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
		// Rules and skill sources hold their body in full_content, not chunks —
		// re-ingesting them would index the source file as documentation and clear
		// the flag that makes the content reachable. Rules are refreshed by
		// RunRulesRefresh, not by doc-sync.
		if src.IsRules || src.IsSkill {
			continue
		}
		if src.Path == "" {
			// The list is a snapshot, and the loop that follows takes real time
			// (git pulls, full walks). Another pass may have re-registered this
			// source with a path and re-indexed it meanwhile; a content match
			// then writes no row the watermark could spare, so judge the current
			// row rather than the snapshot.
			current, readErr := store.GetDocSource(src.Name, src.Project)
			if readErr != nil {
				log.Printf("[doc-sync] %q: re-read source before the sweep: %q", src.Name, readErr)
				continue
			}
			if current == nil || current.Path != "" {
				continue
			}
			if sweepWatermark == 0 {
				log.Printf("[doc-sync] %q: skipped the sweep of a source without a path (no chunk watermark)", src.Name)
				continue
			}
			// Without a path there is no tree to compare against, so ingest.Run
			// never sees this source and its documents stay indexed although
			// nothing can ever refresh them. That is the same loss the sweep in
			// Run handles for a deleted document, only the whole source is gone.
			swept, sweepErr := ingest.SweepUnreachableSource(store, src.ID, sweepWatermark)
			if sweepErr != nil {
				log.Printf("[doc-sync] %q: sweep chunks of a source without a path: %q", src.Name, sweepErr)
			}
			if swept.FilesRemoved > 0 {
				log.Printf("[doc-sync] %q: removed %d document(s) and retired %d learning(s) of a source without a path",
					src.Name, swept.FilesRemoved, swept.LearningsSuperseded)
				if countErr := store.UpdateDocSourceChunkCount(src.ID); countErr != nil {
					log.Printf("[doc-sync] %q: refresh chunk count: %q", src.Name, countErr)
				}
			}
			continue
		}
		// A relative path was registered against a working directory nobody
		// recorded; re-reading it from the daemon's cwd indexes whatever sits
		// at that name now, under the source's name.
		if !filepath.IsAbs(src.Path) {
			log.Printf("[doc-sync] %q: skipped relative path %q (re-register it with an absolute path)", src.Name, src.Path)
			continue
		}

		// The name and the path come from the caller, so both are quoted: a
		// newline in either would split one log line into two, the second
		// reading like a line this daemon never wrote.
		if _, err := os.Stat(src.Path); err != nil {
			if os.IsNotExist(err) {
				log.Printf("[doc-sync] %q: skipped missing path %q", src.Name, src.Path)
			} else {
				log.Printf("[doc-sync] %q: skipped unreadable path %q: %q", src.Name, src.Path, err)
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
			// The error text repeats the path, so quoting it here is what keeps
			// the newline out: the message carries the raw path from the OS
			// error even though Run formats its own copy with %q.
			log.Printf("[doc-sync] %q: error: %q", src.Name, runErr)
			continue
		}

		if result.FilesProcessed > 0 || result.UnreadableEntries > 0 || gitUpdated {
			log.Printf("[doc-sync] %q: %d files updated, %d chunks created, %d unreadable entries skipped",
				src.Name, result.FilesProcessed, result.ChunksCreated, result.UnreadableEntries)
		}
	}
}

// tryGitPull updates a doc source from its upstream, but only when the source
// path is itself a repository root (a docs clone). It never walks up to an
// enclosing repository — see repo.CloneRoot.
func tryGitPull(path string) bool {
	gitRoot := repo.CloneRoot(path)
	if gitRoot == "" {
		return false
	}

	cmd := exec.Command("git", "-C", gitRoot, "pull", "--quiet")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// gitRoot derives from the registered source path and git's own output
		// repeats it, so every part of this line can carry a caller-chosen
		// newline.
		log.Printf("[doc-sync] git pull %q: %q: %q", gitRoot, err, output)
		return false
	}

	// "Already up to date." means no changes
	return !strings.Contains(string(output), "Already up to date")
}

// handlerAdapter bridges daemon.Handler to httpapi.RPCHandler interface,
// avoiding an import cycle between daemon and httpapi packages.
type handlerAdapter struct {
	h *Handler
}

// httpDeniedMethods are the daemon methods the HTTP RPC pass-through must not
// serve. They read and consume a session's channel inbox and identify their
// caller by the peer of its unix-socket connection (channelInboxSession); an
// HTTP request carries no peer, so on that transport the guard has nothing to
// hold a caller to and any bearer-token holder could name any inbox. The proxy
// reaches them over the socket, which is where they belong.
//
// broadcast is denied for the same reason on the write side: its sender label is
// resolved from the caller's proven identity, and the proof lives on the socket
// (peer pid, PID/workdir mapping). A transport that carries no peer would leave
// the label caller-chosen again — and a broadcast is read by every agent in the
// project (YM-271).
var httpDeniedMethods = map[string]bool{
	"check_channel":     true,
	"mark_channel_read": true,
	"check_broadcasts":  true,
	"broadcast":         true,
	// The proxy_state and config_runtime methods ask "is this caller the proxy,
	// or the session it names" and answer it from the peer the kernel reports
	// (proxyStateForeignPeer, sessionOverrideWriteAllowed). A transport without a
	// peer makes those guards abstain, so the methods belong to the socket no
	// matter what a future entry in httpRPCMethods says (YM-427).
	"set_proxy_state":           true,
	"get_proxy_state":           true,
	"delete_proxy_state_prefix": true,
	"set_config":                true,
	"get_config":                true,
}

func (a *handlerAdapter) Handle(req httpapi.RPCRequest) httpapi.RPCResponse {
	if httpDeniedMethods[req.Method] {
		return httpapi.RPCResponse{Error: "method is not available over the HTTP API"}
	}
	resp := a.h.Handle(Request{
		Method: req.Method,
		Params: req.Params,
	})
	return httpapi.RPCResponse{
		Result: resp.Result,
		Error:  resp.Error,
	}
}
