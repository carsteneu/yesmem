package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/hooks"
	yesmcp "github.com/carsteneu/yesmem/internal/mcp"
	"github.com/carsteneu/yesmem/internal/setup"
	"github.com/carsteneu/yesmem/internal/storage"
)

// version is set at build time via: go build -ldflags "-X main.version=..."
var version = "dev"

func init() {
	buildinfo.Version = version

	// Auto-detect system language from $LANG (e.g. "de_DE.UTF-8" → "de")
	// Always includes "en" as fallback. Used for stopword filtering.
	if lang := os.Getenv("LANG"); len(lang) >= 2 {
		sysLang := strings.ToLower(lang[:2])
		if sysLang == "en" {
			briefing.SetLanguages([]string{"en"})
		} else {
			briefing.SetLanguages([]string{sysLang, "en"})
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("yesmem %s\n", version)
	case "mcp":
		runMCP()
	case "micro-reminder":
		runMicroReminder()
	case "idle-tick":
		hooks.RunIdleTick(yesmemDataDir())
	case "daemon":
		runDaemon()
	case "briefing":
		runBriefing()
	case "briefing-hook":
		runBriefingHook()
	case "install", "setup":
		if err := setup.Run(); err != nil {
			log.Fatalf("install: %v", err)
		}
	case "status":
		if err := setup.Status(yesmemDataDir()); err != nil {
			log.Fatalf("status: %v", err)
		}
	case "regenerate-narratives":
		runRegenerateNarratives()
	case "bootstrap-persona":
		runBootstrapPersona()
	case "synthesize-persona":
		runSynthesizePersona()
	case "gap-review":
		runGapReview()
	case "hook-check":
		hooks.RunCheck(yesmemDataDir())
	case "hook-learn":
		hooks.RunLearn(yesmemDataDir())
	case "hook-assist":
		hooks.RunAssist(yesmemDataDir())
	case "hook-failure":
		hooks.RunFailure(yesmemDataDir())
	case "hook-resolve":
		hooks.RunResolveCheck(yesmemDataDir())
	case "hook-think":
		hooks.RunThink(yesmemDataDir())
	case "session-end":
		hooks.RunSessionEnd(yesmemDataDir())
	case "reextract":
		runReextract()
	case "quickstart":
		runQuickstart()
	case "resolve-stale":
		runResolveStale()
	case "embed-learnings":
		runEmbedLearnings()
	case "resolve-check":
		runResolveCheck()
	case "backfill-flavor":
		runBackfillFlavor()
	case "proxy":
		runProxy()
	case "stop":
		runStop()
	case "restart":
		runRestart()
	case "claudemd":
		runClaudeMd()
	case "uninstall":
		if err := setup.Uninstall(); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
	case "add-docs":
		runAddDocs(os.Args[2:])
	case "sync-docs":
		runSyncDocs(os.Args[2:])
	case "list-docs":
		runListDocs(os.Args[2:])
	case "remove-docs":
		runRemoveDocs(os.Args[2:])
	case "export":
		runExport()
	case "import":
		runImport()
	case "cost":
		runCost()
	case "stats":
		runStats()
	case "benchmark":
		runBenchmark()
	case "trait-cleanup":
		runTraitCleanup()
	case "statusline":
		runStatusline()
	case "backup":
		runBackup()
	case "migrate-project":
		runMigrateProject()
	case "migrate-messages":
		runMigrateMessages()
	case "consolidate":
		runConsolidate()
	case "locomo-bench":
		runLocomoBench()
	case "check-update":
		runCheckUpdate()
	case "update":
		runUpdate()
	// case "telegram-bridge":
	// 	runTelegramBridge() // disabled — WIP, re-enable when ready
case "migrate":
		runMigrateCmd()
	case "scratchpad":
		runScratchpad()
	case "spawn-agents":
		runSpawnAgents(os.Args[2:])
	case "agent-tty":
		runAgentTTY()
	case "relay":
		runRelay()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func runMCP() {
	dataDir := yesmemDataDir()

	srv, err := yesmcp.New(dataDir)
	if err != nil {
		log.Fatalf("mcp server: %v", err)
	}
	if err := srv.ServeStdio(); err != nil {
		log.Fatalf("mcp server: %v", err)
	}
}

func runDaemon() {
	home, _ := os.UserHomeDir()
	dataDir := yesmemDataDir()
	projectsDir := filepath.Join(home, ".claude", "projects")
	codexSessionsDir := filepath.Join(home, ".codex", "sessions")

	replace := false
	enableHTTP := false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--replace", "-r":
			replace = true
		case "--http":
			enableHTTP = true
		}
	}

	// Load app config for HTTP settings
	appCfg := config.Default()
	cfgPath := filepath.Join(dataDir, "config.yaml")
	if loaded, err := config.Load(cfgPath); err == nil {
		appCfg = loaded
	}

	if err := daemon.Run(daemon.Config{
		DataDir:          dataDir,
		ProjectsDir:      projectsDir,
		CodexSessionsDir: codexSessionsDir,
		SessionSources:   []string{projectsDir, codexSessionsDir},
		Replace:          replace,
		HTTPEnabled:      enableHTTP || appCfg.HTTP.Enabled,
		HTTPListen:       appCfg.HTTP.Listen,
	}); err != nil {
		log.Fatalf("daemon: %v", err)
	}
}

func yesmemDataDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "yesmem")
	os.MkdirAll(dir, 0755)
	return dir
}

func printUsage() {
	fmt.Println("YesMem — Long-term memory for Claude Code")
	fmt.Println()
	fmt.Println("Usage: yesmem <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  daemon          Run background indexer with file watching")
	fmt.Println("  mcp             Start MCP server (stdio)")
	fmt.Println("  briefing        Generate session briefing for hooks")
	fmt.Println("  micro-reminder  Output micro-reminder for UserPromptSubmit hook")
	fmt.Println("  idle-tick       Dynamic yesmem-usage reminder via daemon (replaces micro-reminder)")
	fmt.Println("  install         Interactive install wizard")
	fmt.Println("  regenerate-narratives  Re-generate all narratives with immersive prompt")
	fmt.Println("  bootstrap-persona     Run persona extraction [--force] [--limit N] [--all]")
	fmt.Println("  synthesize-persona    Force re-synthesis of persona directive (uses Opus)")
	fmt.Println("  reextract       Re-run extraction on existing sessions [--project P] [--last N] [--dry-run]")
	fmt.Println("  resolve-stale   List/resolve stale unfinished items [--dry-run]")
	fmt.Println("  resolve-check   Check commit message against open tasks (for git hooks)")
	fmt.Println("  backfill-flavor Backfill session_flavor for sessions without one [--last N] [--dry-run]")
	fmt.Println("  embed-learnings Bulk-embed learnings into vector store [--force] [--all] [--batch-size N]")
	fmt.Println("  proxy           Start infinite-thread proxy [--port N] [--threshold N] [--target URL]")
	fmt.Println("  stop [target]   Stop daemon/proxy/all (default: all)")
	fmt.Println("  restart         Stop all, clean up, start daemon fresh")
	fmt.Println("  hook-check      PreToolUse hook: warn about known gotchas")
	fmt.Println("  hook-failure    PostToolUseFailure hook: learn gotcha + deep search (combined)")
	fmt.Println("  hook-learn      PostToolUseFailure hook: auto-learn gotchas (legacy, use hook-failure)")
	fmt.Println("  hook-assist     Deep search on Bash failures (legacy, use hook-failure)")
	fmt.Println("  claudemd        Regenerate CLAUDE.md ops files [--all] [--dry-run]")
	fmt.Println("  add-docs        Ingest documentation [--name N --path P|--file F] [--project P] [--dry-run]")
	fmt.Println("  sync-docs       Re-sync registered doc sources [--name N|--project P|--all]")
	fmt.Println("  list-docs       List registered documentation sources [--project P]")
	fmt.Println("  remove-docs     Remove a doc source and its chunks [--name N] [--project P]")
	fmt.Println("  export [path]   Export learnings + persona to JSON (default: yesmem-export.json)")
	fmt.Println("  import <file>   Import learnings from JSON export file")
	fmt.Println("  backup [path]   Back up database to timestamped file (default: backups/)")
	fmt.Println("  migrate-project <from> <to>  Rename project across all tables [--no-backup]")
	fmt.Println("  migrate-messages            Move messages to separate messages.db + FTS5")
	fmt.Println("  cost            Show API cost summary")
	fmt.Println("  stats           Learning statistics per category/project [--project P] [--json]")
	fmt.Println("  benchmark       Embedding/search performance measurement [--project P] [--json]")
	fmt.Println("  trait-cleanup   Deduplicate persona traits via cosine similarity [--dry-run] [--threshold F]")
	fmt.Println("  consolidate     Iterative knowledge consolidation [--rule-based] [--rounds=N]")
	fmt.Println("  check-update    Check if a new version is available")
	fmt.Println("  update          Download and install the latest version")
	fmt.Println("  migrate         Run post-update DB/config migration")
	fmt.Println("  quickstart      Fast extraction of last N sessions [--last N]")
	fmt.Println("  uninstall       Remove all YesMem registrations")
	fmt.Println("  status          Show index status")
	fmt.Println("  version         Show version")
	fmt.Println()
	fmt.Println("Hooks (called by Claude Code, not directly):")
	fmt.Println("  briefing-hook   SessionStart hook (briefing generation)")
	fmt.Println("  hook-resolve    PostToolUse hook: auto-resolve tasks on commit")
	fmt.Println("  hook-think      Inner voice reminder for UserPromptSubmit/PreToolUse")
	fmt.Println("  statusline      Claude Code status bar (reads JSON from stdin)")
	fmt.Println("  session-end     Stop hook: session cleanup")
}

func runConsolidate() {
	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	maxRounds := 3
	ruleOnly := false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--rule-based":
			ruleOnly = true
		}
		if strings.HasPrefix(arg, "--rounds=") {
			fmt.Sscanf(arg, "--rounds=%d", &maxRounds)
		}
	}

	var ext *extraction.Extractor
	if !ruleOnly {
		cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
		if cfg == nil {
			cfg = config.Default()
		}
		apiKey := cfg.ResolvedAPIKey()
		if apiKey == "" {
			apiKey = daemon.ReadClaudeCodeAPIKey()
		}
		client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
		if err != nil {
			log.Printf("warn: no LLM client — falling back to rule-based only: %v", err)
			ruleOnly = true
		} else {
			ext = extraction.NewExtractor(client, store)
		}
	}

	result := extraction.RunConsolidation(store, ext, nil, extraction.ConsolidateConfig{
		MaxRounds:     maxRounds,
		RuleBasedOnly: ruleOnly,
	})

	fmt.Printf("Consolidation: %d rounds, %d checked, %d superseded\n",
		result.Rounds, result.TotalChecked, result.TotalSuperseded)
}
