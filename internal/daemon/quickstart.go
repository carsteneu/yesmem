package daemon

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// RunQuickstart runs the full extraction pipeline for the N most recent sessions.
// Output goes to stderr in a structured format parseable by setup progress.
// Phases: Extraction → Evolution → Narratives → Clustering → Profiles → Persona → ClaudeMD
func RunQuickstart(store *storage.Store, cfg *config.Config, client extraction.LLMClient, qualityClient extraction.LLMClient, lastN int) error {
	sessions, err := store.ListSessions("", lastN)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	// Filter to sessions with enough messages
	var viable []models.Session
	for _, s := range sessions {
		if s.MessageCount > 5 {
			viable = append(viable, s)
		}
	}

	if len(viable) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions with enough messages for quickstart.")
		return nil
	}

	startTime := time.Now()
	fmt.Fprintf(os.Stderr, "Quickstart: %d sessions\n", len(viable))

	// Check which sessions already have learnings
	extracted := map[string]bool{}
	learnings, _ := store.GetActiveLearnings("", "", "", "")
	for _, l := range learnings {
		if l.SessionID != "" {
			extracted[l.SessionID] = true
		}
	}

	var toExtract []models.Session
	for _, s := range viable {
		if !extracted[s.ID] {
			toExtract = append(toExtract, s)
		}
	}

	// Create extractor
	ext := extraction.NewExtractor(client, store)

	// ━━━ Phase 2: Extraction ━━━
	if len(toExtract) > 0 {
		log.Printf("━━━ Phase 2: Extraction (%d sessions) ━━━", len(toExtract))
		RunExtractionPhase(ext, store, toExtract, cfg, client)
	} else {
		log.Println("━━━ Phase 2: All sessions already extracted ━━━")
	}

	// ━━━ Phase 3: Evolution ━━━
	if cfg.Evolution.AutoResolve {
		log.Println("━━━ Phase 3: Knowledge Evolution ━━━")
		checked, superseded := ext.RunEvolution(store, nil)
		log.Printf("  Evolution: %d checked, %d superseded", checked, superseded)
	}

	// ━━━ Phase 4: Narratives ━━━
	log.Println("━━━ Phase 4: Session Narratives ━━━")
	GenerateMissingNarratives(store, viable, cfg, qualityClient)

	// ━━━ Phase 4.5: Clustering ━━━
	log.Println("━━━ Phase 4.5: Learning Clustering ━━━")
	ClusterLearnings(store, qualityClient, nil) // no vector store in quickstart

	// ━━━ Phase 5: Profiles ━━━
	log.Println("━━━ Phase 5: Profiles ━━━")
	GenerateProfiles(store, cfg, qualityClient)

	// ━━━ Phase 6: Persona ━━━
	log.Println("━━━ Phase 6: Persona ━━━")
	existingTraits, _ := store.GetActivePersonaTraits("default", 0.0)
	allSessions, _ := store.ListSessions("", 0)
	if len(existingTraits) == 0 {
		BootstrapPersonaFromLearnings(store, false)
		ExtractPersonaSignalsWithLimit(store, allSessions, cfg, client, lastN)
	} else {
		ExtractPersonaSignalsWithLimit(store, allSessions, cfg, client, lastN)
	}
	SynthesizePersonaDirective(store, qualityClient)

	// ━━━ Phase 7: Rules Refresh ━━━
	log.Println("━━━ Phase 7: Rules Refresh ━━━")
	RunRulesRefresh(store, client)

	elapsed := time.Since(startTime)
	log.Printf("━━━ Quickstart complete (%s) ━━━", elapsed.Round(time.Second))

	return nil
}
