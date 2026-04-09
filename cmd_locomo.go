
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/benchmark"
	"github.com/carsteneu/yesmem/internal/benchmark/locomo"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runLocomoBench() {
	if len(os.Args) < 3 {
		printLocomoUsage()
		os.Exit(1)
	}

	switch os.Args[2] {
	case "run":
		runLocomoFull()
	case "ingest":
		runLocomoIngest()
	case "extract", "query", "judge", "report":
		fmt.Fprintf(os.Stderr, "Subcommand %q requires full 'run' command — individual steps are not yet wired.\n", os.Args[2])
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "Unknown locomo-bench subcommand: %s\n", os.Args[2])
		printLocomoUsage()
		os.Exit(1)
	}
}

func runLocomoFull() {
	fs := flag.NewFlagSet("locomo-bench run", flag.ExitOnError)
	dataFile := fs.String("data", "", "path to LoCoMo JSON dataset file (required)")
	dbPath := fs.String("db", "", "benchmark database path (default: ~/.claude/yesmem/bench/locomo.db)")
	runs := fs.Int("runs", 1, "number of benchmark runs for statistical stability")
	evalLLM := fs.String("eval-llm", "haiku", "LLM for answering queries (default: haiku)")
	judgeLLMFlag := fs.String("judge-llm", "", "LLM for judging answers (default: same as --eval-llm)")
	skipExtract := fs.Bool("skip-extract", false, "skip extraction step, use raw messages for context")
	extractLLM := fs.String("extract-llm", "", "LLM for extraction (default: from config.yaml)")
	useMessages := fs.Bool("messages", false, "include message FTS results in context")
	fullContext := fs.Bool("full-context", false, "variant C: feed full conversation to LLM (no retrieval)")
	concurrency := fs.Int("concurrency", 10, "number of concurrent API workers (reduce for full-context)")
	topK := fs.Int("top-k", 10, "number of search results per query (default: 10)")
	dryRun := fs.Bool("dry-run", false, "estimate costs without making API calls")
	samplePct := fs.Int("sample-pct", 100, "percentage of QA questions to use (10 = 10%)")
	jsonOutput := fs.Bool("json", false, "output results as JSON")
	hybrid := fs.Bool("hybrid", false, "use local hybrid search (BM25+vector+RRF) against bench DB")
	tiered := fs.Bool("tiered", false, "multi-tool tiered search (hybrid+messages+keyword, requires --hybrid)")
	agentic := fs.Bool("agentic", false, "LLM-driven retrieval: evaluates results, escalates if insufficient (requires --hybrid)")
	agenticEval := fs.Bool("agentic-eval", false, "agentic benchmark: LLM uses search tools iteratively to answer (requires --hybrid, OpenAI LLM)")
	gold := fs.Bool("gold", false, "use gold-standard observations as learnings (skip extraction, measure search ceiling)")
	genAQ := fs.Int("gen-aq", 0, "generate anticipated_queries up to N per learning (post-extraction enrichment)")
	genAQLLM := fs.String("gen-aq-llm", "", "LLM for AQ generation (default: same as --eval-llm)")
	dumpResults := fs.String("dump-results", "", "path to dump per-question JSON results for analysis")
	_ = fs.String("daemon-socket", "", "deprecated: daemon no longer needed for hybrid search")
	fs.Parse(os.Args[3:])

	if *dataFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --data is required")
		printLocomoUsage()
		os.Exit(1)
	}

	if *dbPath == "" {
		*dbPath = defaultBenchDB()
	}

	// Parse dataset
	f, err := os.Open(*dataFile)
	if err != nil {
		log.Fatalf("open data file: %v", err)
	}
	defer f.Close()

	samples, err := locomo.ParseDataset(f)
	if err != nil {
		log.Fatalf("parse dataset: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Parsed %d samples from %s\n", len(samples), *dataFile)

	// Open store
	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("open bench db: %v", err)
	}
	defer store.Close()

	// Create LLM clients
	answerLLM := createBenchLLM(*evalLLM)
	judgeModel := *judgeLLMFlag
	if judgeModel == "" {
		judgeModel = *evalLLM
	}
	judgeLLM := createBenchLLM(judgeModel)

	fmt.Fprintf(os.Stderr, "Answer LLM: %s\n", answerLLM.Model())
	fmt.Fprintf(os.Stderr, "Judge LLM:  %s\n", judgeLLM.Model())

	// Create runner
	runner := locomo.NewRunner(store, answerLLM, judgeLLM)

	// Wire extractor if not skipping
	if !*skipExtract {
		ext := createExtractor(store, *extractLLM)
		runner.SetExtractor(ext)
	}

	// Configure query mode
	cfg := locomo.DefaultQueryConfig()
	cfg.UseMessages = *useMessages
	cfg.FullContext = *fullContext
	cfg.Concurrency = *concurrency
	cfg.TopK = *topK

	if *tiered {
		*hybrid = true // tiered implies hybrid
	}

	if *hybrid {
		// Try local search first (no daemon needed) — searches directly against bench DB
		provider, provErr := embedding.NewProviderFromConfig(embedding.DefaultEmbeddingConfig())
		if provErr == nil && provider.Enabled() {
			vs, vsErr := embedding.NewVectorStore(store.DB(), provider.Dimensions())
			if vsErr == nil {
				cfg.LocalSearcher = locomo.NewLocalSearcher(store, provider, vs)
				cfg.Hybrid = true
				cfg.Tiered = *tiered
				cfg.Agentic = *agentic
				cfg.AgenticEval = *agenticEval
				// Wire embedding indexer for post-extraction embedding
				runner.SetIndexer(embedding.NewIndexer(provider, vs))
				fmt.Fprintf(os.Stderr, "Local hybrid search: enabled (BM25+vector against bench DB)\n")
				if *tiered {
					fmt.Fprintf(os.Stderr, "Local tiered search: enabled (hybrid+messages+keyword)\n")
				}
				if *agentic {
					fmt.Fprintf(os.Stderr, "Agentic retrieval: enabled (LLM evaluates + escalates)\n")
				}
			}
		}
		if cfg.LocalSearcher == nil {
			// BM25-only local fallback (no vector provider available)
			cfg.LocalSearcher = locomo.NewLocalSearcher(store, nil, nil)
			cfg.Hybrid = true
			cfg.Tiered = *tiered
			fmt.Fprintf(os.Stderr, "Local hybrid search: enabled (BM25-only, no vector provider)\n")
			if *tiered {
				fmt.Fprintf(os.Stderr, "Local tiered search: enabled (BM25+messages+keyword)\n")
			}
		}
	}

	// Gold mode: ingest observations as learnings, skip extraction
	if *gold {
		*skipExtract = true
		runner.GoldMode = true
		fmt.Fprintf(os.Stderr, "Gold mode: using ground-truth observations as learnings\n")
	}

	// Generate anticipated_queries post-extraction
	if *genAQ > 0 {
		aqLLM := answerLLM
		if *genAQLLM != "" {
			aqLLM = createBenchLLM(*genAQLLM)
		}
		runner.GenAQTarget = *genAQ
		runner.GenAQLLM = aqLLM
		fmt.Fprintf(os.Stderr, "AQ generation: target %d per learning (LLM: %s)\n", *genAQ, aqLLM.Model())
	}

	if *dumpResults != "" {
		runner.DumpResults = *dumpResults
	}

	if *skipExtract && !*fullContext {
		cfg.UseMessages = true
	}

	fmt.Fprintf(os.Stderr, "Config: runs=%d, skipExtract=%v, messages=%v, fullContext=%v, hybrid=%v, tiered=%v\n",
		*runs, *skipExtract, cfg.UseMessages, cfg.FullContext, cfg.Hybrid, cfg.Tiered)

	// Dry-run: estimate costs and exit
	if *dryRun {
		locomo.PrintCostEstimate(os.Stderr, samples, cfg, *skipExtract, *runs, *samplePct)
		return
	}

	// Apply sample percentage (subsample QA questions)
	if *samplePct > 0 && *samplePct < 100 {
		samples = locomo.SubsampleQA(samples, *samplePct)
		fmt.Fprintf(os.Stderr, "Subsampled to %d%% → %d questions\n", *samplePct, locomo.CountTotalQuestions(samples))
	}

	if *runs > 1 {
		stats, err := runner.RunMultiple(samples, cfg, *skipExtract, *runs)
		if err != nil {
			log.Fatalf("benchmark: %v", err)
		}
		if *jsonOutput {
			locomo.PrintJSON(os.Stdout, locomo.Report{}, &stats)
		} else {
			locomo.PrintMultiRunReport(os.Stdout, stats)
		}
	} else {
		report, err := runner.RunFull(samples, cfg, *skipExtract)
		if err != nil {
			log.Fatalf("benchmark: %v", err)
		}
		if *jsonOutput {
			locomo.PrintJSON(os.Stdout, report, nil)
		} else {
			locomo.PrintReport(os.Stdout, report, 1, 1)
		}
	}
}

func runLocomoIngest() {
	fs := flag.NewFlagSet("locomo-bench ingest", flag.ExitOnError)
	dataFile := fs.String("data", "", "path to LoCoMo JSON dataset file (required)")
	dbPath := fs.String("db", "", "benchmark database path (default: ~/.claude/yesmem/bench/locomo.db)")
	fs.Parse(os.Args[3:])

	if *dataFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --data is required")
		printLocomoUsage()
		os.Exit(1)
	}

	if *dbPath == "" {
		*dbPath = defaultBenchDB()
	}

	// Parse dataset
	f, err := os.Open(*dataFile)
	if err != nil {
		log.Fatalf("open data file: %v", err)
	}
	defer f.Close()

	samples, err := locomo.ParseDataset(f)
	if err != nil {
		log.Fatalf("parse dataset: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Parsed %d samples from %s\n", len(samples), *dataFile)

	// Open store
	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("open bench db: %v", err)
	}
	defer store.Close()

	// Ingest
	stats, err := locomo.IngestAll(store, samples)
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}

	totalSessions := 0
	totalMessages := 0
	for _, s := range stats {
		totalSessions += s.Sessions
		totalMessages += s.Messages
		fmt.Fprintf(os.Stderr, "  %s: %d sessions, %d messages\n", s.Project, s.Sessions, s.Messages)
	}
	fmt.Fprintf(os.Stderr, "\nIngested %d samples: %d sessions, %d messages total\n",
		len(stats), totalSessions, totalMessages)
}

func defaultBenchDB() string {
	dir := filepath.Join(yesmemDataDir(), "bench")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("create bench dir: %v", err)
	}
	return filepath.Join(dir, "locomo.db")
}

// createBenchLLM creates an LLMClient for benchmark use.
// If name contains "gpt" -> OpenAI, otherwise -> provider from config.
func createBenchLLM(name string) benchmark.LLMClient {
	if name == "" {
		// Default: use configured provider + model
		dataDir := yesmemDataDir()
		cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
		if cfg == nil {
			cfg = config.Default()
		}
		apiKey := cfg.ResolvedAPIKey()
		if apiKey == "" {
			apiKey = daemon.ReadClaudeCodeAPIKey()
		}
		if apiKey == "" {
			log.Fatal("No API key — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
		}
		if config.IsOpenAIProvider(cfg.LLM.Provider) {
			return benchmark.NewOpenAIClient(apiKey, cfg.ModelID(), cfg.ResolvedOpenAIBaseURL())
		}
		return benchmark.NewAnthropicAdapter(apiKey, cfg.ModelID())
	}

	if strings.Contains(strings.ToLower(name), "gpt") {
		apiKey := requireEnv("OPENAI_API_KEY")
		return benchmark.NewOpenAIClient(apiKey, name, "")
	}

	// Anthropic model by name
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
	if cfg == nil {
		cfg = config.Default()
	}
	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key — set ANTHROPIC_API_KEY or configure in config.yaml")
	}
	return benchmark.NewAnthropicAdapter(apiKey, name)
}

// createExtractor builds a SessionExtractor from config + API key.
// If extractModel is non-empty, it overrides the config model (both passes use same model).
func createExtractor(store *storage.Store, extractModel string) extraction.SessionExtractor {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
	if cfg == nil {
		cfg = config.Default()
	}

	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key for extraction — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
	}

	modelID := cfg.ModelID()
	if extractModel != "" {
		modelID = benchmark.ResolveModel(extractModel)
		log.Printf("Extraction model override: %s", modelID)
	}

	baseURL := cfg.ResolvedOpenAIBaseURL()
	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, modelID, cfg.LLM.ClaudeBinary, baseURL)
	if err != nil {
		log.Fatalf("extraction LLM client: %v", err)
	}

	if extractModel == "" && cfg.Extraction.Mode == "two-pass" {
		extractClient, qErr := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.QualityModelID(), cfg.LLM.ClaudeBinary, baseURL)
		if qErr == nil && extractClient != nil {
			return extraction.NewTwoPassExtractor(client, extractClient, store)
		}
	}
	return extraction.NewExtractor(client, store)
}

// requireEnv returns the value of an environment variable or exits with an error.
func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}

func printLocomoUsage() {
	fmt.Fprintln(os.Stderr, "Usage: yesmem locomo-bench <subcommand> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  run      Run full benchmark pipeline (ingest + extract + query + judge + report)")
	fmt.Fprintln(os.Stderr, "  ingest   Ingest LoCoMo dataset into benchmark database")
	fmt.Fprintln(os.Stderr, "  extract  Run extraction on ingested data (requires full 'run')")
	fmt.Fprintln(os.Stderr, "  query    Query memory system with QA pairs (requires full 'run')")
	fmt.Fprintln(os.Stderr, "  judge    Judge generated answers (requires full 'run')")
	fmt.Fprintln(os.Stderr, "  report   Generate report from results (requires full 'run')")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags for 'run':")
	fmt.Fprintln(os.Stderr, "  --data <path>       Path to LoCoMo JSON dataset file (required)")
	fmt.Fprintln(os.Stderr, "  --db <path>         Benchmark database path (default: ~/.claude/yesmem/bench/locomo.db)")
	fmt.Fprintln(os.Stderr, "  --runs <N>          Number of runs for statistical stability (default: 1)")
	fmt.Fprintln(os.Stderr, "  --eval-llm <model>  LLM for answering queries (default: haiku)")
	fmt.Fprintln(os.Stderr, "  --judge-llm <model> LLM for judging answers (default: same as --eval-llm)")
	fmt.Fprintln(os.Stderr, "  --skip-extract      Skip extraction, use raw messages for context")
	fmt.Fprintln(os.Stderr, "  --messages          Include message FTS results in context")
	fmt.Fprintln(os.Stderr, "  --full-context      Variant C: feed full conversation (no retrieval)")
	fmt.Fprintln(os.Stderr, "  --hybrid            Use local hybrid search (BM25+vector+RRF) against bench DB")
	fmt.Fprintln(os.Stderr, "  --tiered            Multi-tool tiered search (hybrid+messages+keyword, implies --hybrid)")
	fmt.Fprintln(os.Stderr, "  --daemon-socket     Deprecated: daemon no longer needed for hybrid search")
	fmt.Fprintln(os.Stderr, "  --json              Output results as JSON")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Flags for 'ingest':")
	fmt.Fprintln(os.Stderr, "  --data <path>       Path to LoCoMo JSON dataset file (required)")
	fmt.Fprintln(os.Stderr, "  --db <path>         Benchmark database path")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Short model names: haiku, sonnet, opus (resolved to full Anthropic model IDs).")
	fmt.Fprintln(os.Stderr, "Use 'gpt-*' model names to use OpenAI (requires OPENAI_API_KEY).")
	fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY required for Anthropic models (default).")
}
