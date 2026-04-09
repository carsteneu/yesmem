package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runReextract() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	// Parse flags
	fs := flag.NewFlagSet("reextract", flag.ExitOnError)
	project := fs.String("project", "", "filter by project")
	fs.StringVar(project, "p", "", "filter by project (short)")
	last := fs.Int("last", 0, "limit to last N sessions")
	fs.IntVar(last, "l", 0, "limit to last N sessions (short)")
	dryRun := fs.Bool("dry-run", false, "list sessions without re-extracting")
	fs.BoolVar(dryRun, "n", false, "dry-run (short)")
	fs.Parse(os.Args[2:])

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// List sessions matching filters
	sessions, err := store.ListAllSessions(*project, *last)
	if err != nil {
		log.Fatalf("list sessions: %v", err)
	}

	// Filter to sessions with enough messages
	var toProcess []models.Session
	for _, s := range sessions {
		if s.MessageCount > 5 {
			toProcess = append(toProcess, s)
		}
	}

	if len(toProcess) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions to re-extract.")
		return
	}

	fmt.Fprintf(os.Stderr, "Sessions to re-extract: %d\n\n", len(toProcess))
	for _, s := range toProcess {
		fmt.Fprintf(os.Stderr, "  %s  %-25s  %3d msgs  %s\n",
			truncID(s.ID), s.ProjectShort, s.MessageCount, s.StartedAt.Format("2006-01-02"))
	}

	if *dryRun {
		fmt.Fprintf(os.Stderr, "\n--dry-run: no changes made.\n")
		return
	}

	// Need LLM client for extraction
	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
	}

	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}

	// Respect extraction mode from config (two-pass vs single-pass)
	var ext extraction.SessionExtractor
	if cfg.Extraction.Mode == "two-pass" {
		extractClient, qErr := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.QualityModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
		if qErr == nil && extractClient != nil {
			ext = extraction.NewTwoPassExtractor(client, extractClient, store)
			fmt.Fprintf(os.Stderr, "Mode: two-pass (summarize=%s, extract=%s)\n", client.Model(), extractClient.Model())
		} else {
			ext = extraction.NewExtractor(client, store)
			fmt.Fprintf(os.Stderr, "Mode: single-pass fallback (quality client unavailable)\n")
		}
	} else {
		ext = extraction.NewExtractor(client, store)
		fmt.Fprintf(os.Stderr, "Mode: single-pass (%s)\n", client.Model())
	}

	workers := 3
	if client.Name() == "cli" {
		workers = 1
	}
	total := len(toProcess)
	startTime := time.Now()
	fmt.Fprintf(os.Stderr, "\nRe-extracting with %d workers, model: %s\n\n", workers, client.Model())

	var (
		mu      sync.Mutex
		done    int
		errors  int
		deleted int64
	)

	work := make(chan models.Session, total)
	for _, s := range toProcess {
		work <- s
	}
	close(work)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range work {
				// Delete existing auto-extracted learnings
				supersededIDs, err := store.SupersedeSessionLearnings(s.ID)
				if err != nil {
					log.Printf("  warn: delete learnings for %s: %v", truncID(s.ID), err)
				}
				// Reset extracted_at so daemon won't skip this session
				store.ResetSessionExtracted(s.ID)
				mu.Lock()
				deleted += int64(len(supersededIDs))
				mu.Unlock()

				// Re-run extraction
				msgs, err := store.GetMessagesBySession(s.ID)
				if err != nil || len(msgs) == 0 {
					mu.Lock()
					errors++
					done++
					mu.Unlock()
					continue
				}

				if err := ext.ExtractAndStore(s.ID, s.ProjectShort, msgs, false); err != nil {
					mu.Lock()
					errors++
					done++
					d := done
					mu.Unlock()
					log.Printf("  [%d/%d] WARN %s: %v", d, total, truncID(s.ID), err)

					if strings.Contains(err.Error(), "rate_limit") {
						time.Sleep(30 * time.Second)
					}
				} else {
					store.MarkSessionExtracted(s.ID)
					mu.Lock()
					done++
					d := done
					elapsed := time.Since(startTime)
					eta := time.Duration(0)
					if d > 0 {
						eta = time.Duration(float64(elapsed) / float64(d) * float64(total-d))
					}
					mu.Unlock()
					fmt.Fprintf(os.Stderr, "  [%d/%d] OK %s (%s, %d msgs) — elapsed %s, ETA %s\n",
						d, total, truncID(s.ID), s.ProjectShort, s.MessageCount,
						elapsed.Round(time.Second), eta.Round(time.Second))
				}
			}
		}()
	}

	wg.Wait()
	fmt.Fprintf(os.Stderr, "\nExtraction done: %d sessions, %d errors, %d old learnings superseded\n", done-errors, errors, deleted)

	// Migrate hit_counts from superseded learnings to new ones
	migrated, err := store.MigrateHitCounts()
	if err != nil {
		log.Printf("warn: migrate hit counts: %v", err)
	} else if migrated > 0 {
		fmt.Fprintf(os.Stderr, "Migrated hit_counts to %d new learnings\n", migrated)
	}
}

func runQuickstart() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	// Parse flags
	lastN := 5
	args := os.Args[2:]
	for i, arg := range args {
		switch arg {
		case "--last", "-l":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &lastN)
			}
		}
	}

	// Resolve API key
	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
	}

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Extraction client (Sonnet)
	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}

	// Quality client (Opus) — for narratives, persona synthesis
	var qualityClient extraction.LLMClient
	if cfg.NarrativeModelID() != cfg.ModelID() {
		qc, qErr := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.NarrativeModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
		if qErr != nil {
			log.Printf("Quality model unavailable, using extraction model: %v", qErr)
			qualityClient = client
		} else {
			qualityClient = qc
		}
	} else {
		qualityClient = client
	}

	fmt.Fprintf(os.Stderr, "Quickstart: analyzing last %d sessions (extract=%s, quality=%s)\n\n",
		lastN, client.Model(), qualityClient.Model())

	if err := daemon.RunQuickstart(store, cfg, client, qualityClient, lastN); err != nil {
		log.Fatalf("quickstart: %v", err)
	}
}

func runResolveStale() {
	dataDir := yesmemDataDir()

	dryRun := false
	for _, arg := range os.Args[2:] {
		if arg == "--dry-run" || arg == "-n" {
			dryRun = true
		}
	}

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	unfinished, err := store.GetActiveLearnings("unfinished", "", "", "")
	if err != nil {
		log.Fatalf("get unfinished: %v", err)
	}

	if len(unfinished) == 0 {
		fmt.Println("No active unfinished items found.")
		return
	}

	fmt.Fprintf(os.Stderr, "Found %d active unfinished items:\n\n", len(unfinished))
	for _, u := range unfinished {
		age := time.Since(u.CreatedAt).Hours() / 24
		fmt.Fprintf(os.Stderr, "  #%d [%s] (%.0fd old) %s\n", u.ID, u.Project, age, u.Content)
	}

	if dryRun {
		fmt.Fprintf(os.Stderr, "\n--dry-run: no changes made. Use resolve(id) or resolve_by_text() to resolve items.\n")
		return
	}

	// Without --dry-run: resolve items older than 30 days as stale
	var staleIDs []int64
	cutoff := time.Now().AddDate(0, 0, -30)
	for _, u := range unfinished {
		if u.CreatedAt.Before(cutoff) {
			staleIDs = append(staleIDs, u.ID)
		}
	}

	if len(staleIDs) == 0 {
		fmt.Fprintf(os.Stderr, "\nNo items older than 30 days. Nothing to resolve.\n")
		return
	}

	if err := store.ResolveBatch(staleIDs, "batch-cleanup: stale >30 days"); err != nil {
		log.Fatalf("resolve batch: %v", err)
	}

	fmt.Fprintf(os.Stderr, "\nResolved %d stale items (>30 days old).\n", len(staleIDs))

	// Regenerate MEMORY.md
	daemon.GenerateAllMemoryMDs(store)
	fmt.Fprintf(os.Stderr, "MEMORY.md updated.\n")
}

func runResolveCheck() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: yesmem resolve-check <commit-message>")
		os.Exit(1)
	}
	commitMsg := os.Args[2]

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Extract significant keywords from commit message (skip common prefixes)
	// e.g. "NEW: unfinished TTL filter" → search for "unfinished TTL filter"
	query := stripCommitPrefix(commitMsg)
	if len(query) < 5 {
		return // Too short to match meaningfully
	}

	matches, err := store.SearchUnfinished(query, "")
	if err != nil || len(matches) == 0 {
		return // No candidates, exit silently
	}

	// For now: resolve the best match directly if keyword match is strong
	// (single word matches multiple words in content = good signal)
	// Future: add Haiku LLM confirmation for ambiguous cases
	best := matches[0]

	reason := fmt.Sprintf("git commit: %s", commitMsg)
	if len(reason) > 200 {
		reason = reason[:200]
	}

	if err := store.ResolveLearning(best.ID, reason); err != nil {
		log.Printf("resolve-check: could not resolve #%d: %v", best.ID, err)
		return
	}

	fmt.Fprintf(os.Stderr, "resolve-check: resolved #%d (%s) via commit: %s\n", best.ID, best.Content, commitMsg)

	// Update MEMORY.md
	daemon.GenerateAllMemoryMDs(store)
}

// stripCommitPrefix removes conventional commit prefixes like "NEW:", "FIX:", "UPD:" etc.
func stripCommitPrefix(msg string) string {
	prefixes := []string{
		"NEW:", "FIX:", "UPD:", "FEAT:", "REFACTOR:", "CHORE:", "DOC:", "TEST:",
		"feat:", "fix:", "chore:", "docs:", "refactor:", "test:", "style:", "perf:",
		"feat(", "fix(", "chore(", "docs(",
	}
	for _, p := range prefixes {
		if len(msg) > len(p) && msg[:len(p)] == p {
			msg = msg[len(p):]
			// Also strip closing paren for scoped commits like "feat(api):"
			if idx := 0; idx < len(msg) && msg[0] == ')' {
				msg = msg[1:]
			}
			if len(msg) > 0 && msg[0] == ':' {
				msg = msg[1:]
			}
			break
		}
	}
	return strings.TrimSpace(msg)
}

func runBackfillFlavor() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	fs := flag.NewFlagSet("backfill-flavor", flag.ExitOnError)
	last := fs.Int("last", 0, "limit to last N sessions")
	fs.IntVar(last, "l", 0, "limit (short)")
	dryRun := fs.Bool("dry-run", false, "list sessions without backfilling")
	fs.BoolVar(dryRun, "n", false, "dry-run (short)")
	force := fs.Bool("force", false, "re-generate all flavors (not just missing)")
	fs.Parse(os.Args[2:])

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	var sessionIDs []string
	if *force {
		sessionIDs, err = store.AllSessionIDs(*last)
	} else {
		sessionIDs, err = store.SessionsWithoutFlavor(*last)
	}
	if err != nil {
		log.Fatalf("query: %v", err)
	}

	if len(sessionIDs) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions to process.")
		return
	}

	mode := "missing"
	if *force {
		mode = "ALL (force)"
	}
	fmt.Fprintf(os.Stderr, "Sessions to process (%s): %d\n", mode, len(sessionIDs))
	if *dryRun {
		for _, id := range sessionIDs {
			fmt.Fprintf(os.Stderr, "  %s\n", truncID(id))
		}
		fmt.Fprintln(os.Stderr, "\n--dry-run: no changes made.")
		return
	}

	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
	}

	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}

	const flavorPrompt = `Du bist ein Session-Analyst. Du bekommst eine Transkription einer Programmier-Session.

1. Fasse den CHARAKTER der Session zusammen (max 400 Zeichen).
   Beschreibe die DYNAMIK, STIMMUNG und WENDEPUNKTE, nicht nur den Inhalt.
   Was war der Bogen? Was war der praegende Moment?

2. Bewerte die EMOTIONALE INTENSITAET der Session (0.0 - 1.0).
   0.0 = ruhige Routine, Config-Arbeit
   0.3 = normale Entwicklung, alles laeuft
   0.5 = interessante Herausforderung, mehrere Ansaetze
   0.7 = Frustration oder Durchbruch, deutliche Stimmungswechsel
   0.9 = intensive Session mit starken Wendepunkten
   1.0 = Ausnahme-Session (Architektur-Entscheidung, grundlegender Pivot)

WICHTIG: Schreibe KEINEN Code. Fuehre KEINE Befehle aus. Vervollstaendige NICHTS aus der Transkription.

Beispiele guter Flavors:
- "3h Sandbox-Kampf → User schaltet sie aus → dann laeuft alles. Wendepunkt: direkter DB-Weg statt Workarounds."
- "Sauberer Sprint, alles klappt auf Anhieb. TDD-Zyklus durchgezogen, 12 Tests gruen."
- "Frust: Race Condition, 5 Ansaetze, pragmatischer Workaround. Am Ende hat atomare DB-Op alles geloest."
- "Tiefes Design-Gespraech ueber Erinnerungsqualitaet. User fragt 'was brauchst DU?' — das oeffnet alles."`

	flavorSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"flavor":              map[string]any{"type": "string"},
			"emotional_intensity": map[string]any{"type": "number"},
		},
		"required":             []string{"flavor", "emotional_intensity"},
		"additionalProperties": false,
	}

	done := 0
	for _, sid := range sessionIDs {
		msgs, err := store.GetMessagesBySession(sid)
		if err != nil || len(msgs) < 3 {
			continue
		}

		// Build compact summary: budget-based (5k tokens ≈ 20k chars)
		const charBudget = 20000
		var content strings.Builder
		for _, m := range msgs {
			role := m.Role
			text := m.Content
			remaining := charBudget - content.Len()
			if remaining <= 0 {
				break
			}
			if len(text) > remaining {
				text = text[:remaining] + "..."
			}
			fmt.Fprintf(&content, "[%s] %s\n", role, text)
		}

		response, err := client.CompleteJSON(flavorPrompt, content.String(), flavorSchema)
		if err != nil {
			log.Printf("warn: flavor for %s: %v", truncID(sid), err)
			if strings.Contains(err.Error(), "rate_limit") {
				time.Sleep(60 * time.Second)
			}
			continue
		}

		// Parse JSON response
		var result struct {
			Flavor             string  `json:"flavor"`
			EmotionalIntensity float64 `json:"emotional_intensity"`
		}
		if jsonErr := json.Unmarshal([]byte(response), &result); jsonErr != nil {
			log.Printf("warn: parse flavor for %s: %v", truncID(sid), jsonErr)
			continue
		}

		flavor := strings.TrimSpace(result.Flavor)
		flavor = strings.Trim(flavor, "\"'")
		runes := []rune(flavor)
		if len(runes) > 400 {
			flavor = string(runes[:400])
		}

		intensity := result.EmotionalIntensity
		if intensity < 0 {
			intensity = 0
		}
		if intensity > 1 {
			intensity = 1
		}

		n, err := store.UpdateSessionFlavorAndIntensity(sid, flavor, intensity)
		if err != nil {
			log.Printf("warn: update %s: %v", truncID(sid), err)
			continue
		}

		done++
		fmt.Fprintf(os.Stderr, "  [%d/%d] %s → %q (intensity: %.1f, %d learnings)\n", done, len(sessionIDs), truncID(sid), flavor, intensity, n)

		// Rate limit pause
		time.Sleep(1 * time.Second)
	}

	fmt.Fprintf(os.Stderr, "\nDone: %d/%d sessions backfilled.\n", done, len(sessionIDs))
}

func runEmbedLearnings() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	force := false
	includeAll := false
	batchSize := 512
	throttle := 0 * time.Millisecond // static embeddings are instant — no throttle needed
	args := os.Args[2:]
	for i, arg := range args {
		switch arg {
		case "--force", "-f":
			force = true
		case "--all", "-a":
			includeAll = true
		case "--batch-size", "-b":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &batchSize)
			}
		case "--no-throttle":
			throttle = 0
		case "--throttle":
			if i+1 < len(args) {
				if d, err := time.ParseDuration(args[i+1]); err == nil {
					throttle = d
				}
			}
		}
	}

	// Open DB
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Create embedding provider from config
	provider, err := embedding.NewProviderFromConfig(cfg.Embedding)
	if err != nil {
		log.Fatalf("create embedding provider: %v", err)
	}
	defer provider.Close()

	if !provider.Enabled() {
		fmt.Fprintf(os.Stderr, "Embedding provider is disabled (provider: %q). Set embedding.provider to 'static' in config.yaml.\n", cfg.Embedding.Provider)
		os.Exit(1)
	}

	// No VectorStore needed — vectors are saved to SQLite via SaveEmbeddingVectors().
	// VectorStore reads directly from learnings.embedding_vector (brute-force cosine).

	// Get learnings to embed
	var learnings []models.Learning
	if includeAll {
		learnings, err = store.GetAllLearningsForEmbedding()
	} else {
		learnings, err = store.GetPendingLearningsForEmbedding(0)
	}
	if err != nil {
		log.Fatalf("get learnings: %v", err)
	}

	label := "active"
	if includeAll {
		label = "all (incl. superseded)"
	}
	fmt.Fprintf(os.Stderr, "Found %d %s learnings. Embedding with batch size %d...\n", len(learnings), label, batchSize)

	// Convert to IndexItems — use V2 enriched embedding text when available
	items := make([]embedding.IndexItem, len(learnings))
	for i, l := range learnings {
		embText := l.EmbeddingText
		if embText == "" {
			embText = l.BuildEmbeddingText()
		}
		items[i] = embedding.IndexItem{
			ID:      fmt.Sprintf("%d", l.ID),
			Content: embText,
			Metadata: map[string]string{
				"category": l.Category,
				"project":  l.Project,
				"domain":   l.Domain,
			},
		}
	}

	stats, err := embedding.MigrateEmbeddings(
		context.Background(), provider, nil, items, batchSize, force, throttle,
		func(ids []string, vectors [][]float32) {
			results := make([]storage.EmbeddingResult, 0, len(ids))
			for i, id := range ids {
				var n int64
				if _, err := fmt.Sscanf(id, "%d", &n); err == nil {
					results = append(results, storage.EmbeddingResult{ID: n, Vector: vectors[i]})
				}
			}
			if len(results) > 0 {
				if err := store.SaveEmbeddingVectors(results); err != nil {
					log.Printf("warn: save vectors to SQLite: %v", err)
				}
			}
		},
	)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Done. Total: %d, Embedded: %d, Skipped: %d, Errors: %d\n",
		stats.Total, stats.Embedded, stats.Skipped, stats.Errors)

	// Notify running daemon to reload vector store
	if stats.Embedded > 0 {
		conn, err := net.DialTimeout("unix", filepath.Join(dataDir, "daemon.sock"), 500*time.Millisecond)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Note: daemon not reachable for reload (%v)\n", err)
			return
		}
		defer conn.Close()
		msg := `{"jsonrpc":"2.0","method":"reload_vectors","id":1}` + "\n"
		conn.Write([]byte(msg))
		buf := make([]byte, 1024)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		fmt.Fprintf(os.Stderr, "Daemon reload: %s\n", string(buf[:n]))
	}
}

func truncID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
