package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runBootstrapPersona() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	force := false
	reset := false
	sessionLimit := 30 // Default: 30 sessions for signal extraction
	args := os.Args[2:]
	for i, arg := range args {
		switch arg {
		case "--force", "-f":
			force = true
		case "--reset":
			reset = true
			force = true // reset implies force
		case "--limit", "-l":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &sessionLimit)
			}
		case "--all":
			sessionLimit = 0
		}
	}

	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Phase 0: Reset / Cleanup
	if reset {
		log.Println("━━━ RESET: Clearing all persona traits and directives ━━━")
		daemon.ResetPersona(store)
	}
	daemon.CleanupGenericTraits(store)

	// Phase 1a: Bootstrap from existing learnings (no LLM needed)
	log.Println("━━━ Phase 1a: Bootstrap from Learnings ━━━")
	count := daemon.BootstrapPersonaFromLearnings(store, force)
	log.Printf("  %d traits created from learnings", count)

	// Phase 1b: Extract expertise from tech keywords in learnings
	log.Println("━━━ Phase 1b: Expertise Scan from Learnings ━━━")
	expertiseCount := daemon.ExtractExpertiseFromLearnings(store)
	log.Printf("  %d expertise traits created", expertiseCount)

	// Phase 2: Signal extraction (needs LLM)
	if apiKey == "" {
		log.Println("━━━ Phase 2: Skipped (no API key) ━━━")
		log.Println("  Set ANTHROPIC_API_KEY or OPENAI_API_KEY to run LLM-based signal extraction")
	} else {
		client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
		if err != nil {
			log.Fatalf("LLM client: %v", err)
		}

		sessions, _ := store.ListSessions("", 0)
		limitDesc := fmt.Sprintf("%d", sessionLimit)
		if sessionLimit == 0 {
			limitDesc = "ALL"
		}
		log.Printf("━━━ Phase 2: Signal Extraction (%d sessions, limit: %s) ━━━", len(sessions), limitDesc)
		daemon.ExtractPersonaSignalsWithLimit(store, sessions, cfg, client, sessionLimit)

		// Phase 2.5: Dedup traits via embedding cosine similarity
		log.Println("━━━ Phase 2.5: Trait Dedup (embedding cosine similarity) ━━━")
		embedProvider, embedErr := embedding.NewProviderFromConfig(cfg.Embedding)
		if embedErr != nil {
			log.Printf("  Dedup skipped (embedding provider: %v)", embedErr)
		} else {
			defer embedProvider.Close()
			daemon.DedupPersonaTraits(store, embedProvider)
		}

		// Phase 3: Synthesize directive (uses quality model — Opus)
		log.Println("━━━ Phase 3: Synthesize Persona Directive ━━━")
		qualityClient, qErr := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.NarrativeModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
		if qErr != nil {
			log.Printf("  Quality model unavailable, falling back to extraction model: %v", qErr)
			qualityClient = client
		} else {
			log.Printf("  Using quality model: %s", cfg.NarrativeModelID())
		}
		daemon.SynthesizePersonaDirective(store, qualityClient)
	}

	// Show result
	traits, _ := store.GetActivePersonaTraits("default", 0.0)
	directive, _ := store.GetPersonaDirective("default")
	log.Printf("━━━ Done: %d active traits ━━━", len(traits))
	for _, t := range traits {
		log.Printf("  %s.%s = %q (confidence: %.1f, source: %s)", t.Dimension, t.TraitKey, t.TraitValue, t.Confidence, t.Source)
	}
	if directive != nil {
		log.Printf("\nPersona Directive:\n%s", directive.Directive)
	}
}

func runSynthesizePersona() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key available. Set ANTHROPIC_API_KEY or OPENAI_API_KEY.")
	}

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Use quality model (Opus) for persona synthesis
	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.NarrativeModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}
	log.Printf("Synthesizing persona directive with %s...", cfg.NarrativeModelID())

	// Invalidate hash to force regeneration
	existing, _ := store.GetPersonaDirective("default")
	if existing != nil {
		store.InvalidatePersonaDirectiveHash("default")
	}

	daemon.SynthesizePersonaDirective(store, client)

	// Also synthesize user profile (force by invalidating hash)
	existingProfile, _ := store.GetPersonaDirective("user_profile")
	if existingProfile != nil {
		store.InvalidatePersonaDirectiveHash("user_profile")
	}
	daemon.SynthesizeUserProfile(store, client)

	directive, _ := store.GetPersonaDirective("default")
	if directive != nil {
		fmt.Println(directive.Directive)
	}
}

func runRegenerateNarratives() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key — set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure in config.yaml")
	}

	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.NarrativeModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}
	log.Printf("Using model: %s for narrative generation", cfg.NarrativeModelID())

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	sessions, err := store.ListSessions("", 0)
	if err != nil {
		log.Fatalf("list sessions: %v", err)
	}

	// Supersede narratives from short sessions
	cleanedIDs, _ := store.SupersedeShortNarratives(10)
	if len(cleanedIDs) > 0 {
		fmt.Fprintf(os.Stderr, "Cleaned up %d narratives from short sessions (<10 messages)\n", len(cleanedIDs))
	}

	// Filter sessions worth narrating
	var toProcess []models.Session
	for _, s := range sessions {
		if s.MessageCount >= 3 {
			toProcess = append(toProcess, s)
		}
	}

	workers := 10
	if client.Name() == "cli" {
		workers = 1
	}
	total := len(toProcess)
	fmt.Fprintf(os.Stderr, "Regenerating narratives: %d sessions, %d workers, model: %s\n", total, workers, cfg.NarrativeModelID())

	var (
		mu          sync.Mutex
		regenerated int
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
				if daemon.RegenerateNarrativeForSession(store, s, client) {
					mu.Lock()
					regenerated++
					g := regenerated
					mu.Unlock()
					if g%10 == 0 {
						fmt.Fprintf(os.Stderr, "\r  %d/%d regenerated", g, total)
					}
				}
			}
		}()
	}

	wg.Wait()
	fmt.Fprintf(os.Stderr, "\n✓ Done: %d/%d narratives regenerated\n", regenerated, total)
}
