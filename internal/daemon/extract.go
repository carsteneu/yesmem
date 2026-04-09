package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/parser"
	"github.com/carsteneu/yesmem/internal/storage"
)

// runInitialExtraction processes sessions in 5 phases to avoid API quota competition.
// Phase 1 (Indexing) already happened before this function is called.
func runInitialExtraction(ext extraction.SessionExtractor, evoExt *extraction.Extractor, store *storage.Store, cfg *config.Config, client extraction.LLMClient, qualityClient extraction.LLMClient, handler *Handler) {
	// Wait for VectorStore to finish loading (async init). Needed for Phase 3.5 + 4.5.
	for i := 0; i < 12; i++ {
		if handler.VectorStore() != nil {
			break
		}
		if i == 0 {
			log.Println("  Waiting for VectorStore to load...")
		}
		time.Sleep(5 * time.Second)
	}

	sessions, err := store.ListAllSessions("", 0)
	if err != nil {
		log.Printf("warn: list sessions for extraction: %v", err)
		return
	}

	// Cleanup: supersede learnings from trivially short sessions
	if cleanedIDs, err := store.SupersedeShortSessionLearnings(6); err == nil && len(cleanedIDs) > 0 {
		log.Printf("  Cleaned %d learnings from short sessions (<6 messages)", len(cleanedIDs))
	}

	// Mark short sessions as extracted so they don't show as pending
	if marked, err := store.MarkShortSessionsExtracted(5); err == nil && marked > 0 {
		log.Printf("  Marked %d short sessions (≤5 messages) as extracted", marked)
	}

	// Check which sessions already have been extracted (persistent marker)
	var toExtract []models.Session
	for _, s := range sessions {
		if s.ExtractedAt.IsZero() && s.MessageCount > 5 {
			toExtract = append(toExtract, s)
		}
	}

	// Apply max_age_days filter
	toExtract = FilterByMaxAge(toExtract, cfg.Extraction.MaxAgeDays)

	// Sort newest first — onboarding extracts recent sessions first so the user
	// gets a useful briefing within minutes. Older sessions backfill on subsequent runs.
	sort.Slice(toExtract, func(i, j int) bool {
		return toExtract[i].StartedAt.After(toExtract[j].StartedAt)
	})

	// Apply max_per_run cap (takes newest first thanks to sort above)
	if cfg.Extraction.MaxPerRun > 0 && len(toExtract) > cfg.Extraction.MaxPerRun {
		remaining := len(toExtract) - cfg.Extraction.MaxPerRun
		log.Printf("  Capping extraction: %d sessions pending, processing %d newest first (%d deferred)", len(toExtract), cfg.Extraction.MaxPerRun, remaining)
		toExtract = toExtract[:cfg.Extraction.MaxPerRun]
	}

	// Log cost estimate before starting
	est := EstimateExtractionCost(store, toExtract, cfg.Extraction.Model)
	LogExtractionEstimate(est, client)

	pendingNarratives, _ := countPendingNarratives(store)
	pendingProfiles, _ := countPendingProfiles(store)
	existingTraits, _ := store.GetActivePersonaTraits("default", 0.0)
	personaNeedsWork := len(existingTraits) == 0 || len(toExtract) > 0

	if len(toExtract) == 0 && pendingNarratives == 0 && pendingProfiles == 0 && !personaNeedsWork {
		log.Println("━━━ Initial background scan skipped: no extraction/profile/narrative/persona work pending ━━━")
		return
	}

	// ━━━ Phase 2: Extraction (LLM — learnings only, NO evolution) ━━━
	if len(toExtract) > 0 {
		if !extraction.HasBudget(client) {
			log.Println("━━━ Phase 2: Extraction skipped (budget exhausted) ━━━")
		} else {
			log.Printf("━━━ Phase 2: Extraction (%d sessions) ━━━", len(toExtract))
			RunExtractionPhase(ext, store, toExtract, cfg, client)
		}
	} else {
		log.Println("━━━ Phase 2: No sessions need extraction ━━━")
	}

	// ━━━ Phase 2.5: Embed learnings (synchronous, before Evolution) ━━━
	// SSE embeddings are fast (pure lookup+pool, no neural inference), so we run
	// synchronously to ensure vectors are available for embedding-based dedup in Phase 3.
	// Without this, the first Evolution run after initial extraction has no vectors.
	if handler.VectorStore() != nil && len(toExtract) > 0 {
		pending, _ := store.GetPendingLearningsForEmbedding(0)
		if len(pending) > 0 {
			log.Printf("━━━ Phase 2.5: Embed %d learnings (sync, SSE) ━━━", len(pending))
			bin, _ := os.Executable()
			cmd := exec.Command(bin, "embed-learnings")
			homeDir, _ := os.UserHomeDir()
			logPath := filepath.Join(homeDir, ".claude", "yesmem", "logs", "embed.log")
			os.MkdirAll(filepath.Dir(logPath), 0755)
			if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
				cmd.Stdout = logFile
				cmd.Stderr = logFile
			}
			if err := cmd.Run(); err != nil {
				log.Printf("  Embed sync: failed: %v", err)
			} else {
				log.Printf("  Embed sync: done (%d items)", len(pending))
			}
		}
	}

	// ━━━ Phase 3: Evolution (1 bulk LLM call per category) ━━━
	if cfg.Evolution.AutoResolve && len(toExtract) > 0 {
		if !extraction.HasBudget(client) {
			log.Println("━━━ Phase 3: Evolution skipped (budget exhausted) ━━━")
		} else {
			evolutionScope, err := buildEvolutionScope(store, toExtract)
			if err != nil {
				log.Printf("━━━ Phase 3: Evolution skipped (scope build failed: %v) ━━━", err)
			} else if len(evolutionScope) == 0 {
				log.Println("━━━ Phase 3: Evolution skipped (no new learning categories) ━━━")
			} else {
				log.Printf("━━━ Phase 3: Knowledge Evolution (scoped to %d project/category groups) ━━━", countEvolutionScope(evolutionScope))
				checked, superseded := evoExt.RunEvolutionForScope(store, evolutionScope, nil)
				log.Printf("  Evolution complete: %d checked, %d superseded", checked, superseded)
			}
		}
	} else if cfg.Evolution.AutoResolve {
		log.Println("━━━ Phase 3: Evolution skipped (no new extraction delta) ━━━")
	}

	// ━━━ Phase 3.5: Auto-embed remaining learnings via external process ━━━
	// Only needed when Phase 2.5 didn't run (no new extraction) but pending embeddings
	// exist from prior runs. Runs async (fire-and-forget) since Evolution already passed.
	if handler.VectorStore() != nil && len(toExtract) == 0 {
		pending, _ := store.GetPendingLearningsForEmbedding(0)
		if len(pending) > 0 {
			bin, _ := os.Executable()
			cmd := exec.Command("nice", "-n", "19", bin, "embed-learnings")
			homeDir, _ := os.UserHomeDir()
			logPath := filepath.Join(homeDir, ".claude", "yesmem", "logs", "embed.log")
			os.MkdirAll(filepath.Dir(logPath), 0755)
			if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
				cmd.Stdout = logFile
				cmd.Stderr = logFile
			}
			if err := cmd.Start(); err != nil {
				log.Printf("  Auto-embed: failed to spawn embed-learnings: %v", err)
			} else {
				log.Printf("  Auto-embed: spawned external process (PID %d) for %d pending items", cmd.Process.Pid, len(pending))
				handler.SetEmbedProcess(cmd.Process)
				go cmd.Wait() // reap zombie
			}
		} else {
			log.Printf("  Auto-embed: no pending items")
		}
	}

	// ━━━ Phase 4: Narratives (LLM — session handovers, uses quality model) ━━━
	if pendingNarratives > 0 {
		if !extraction.HasBudget(qualityClient) {
			log.Printf("━━━ Phase 4: Session Narratives skipped (quality budget exhausted, %d pending) ━━━", pendingNarratives)
		} else {
			log.Printf("━━━ Phase 4: Session Narratives (%d pending) ━━━", pendingNarratives)
			GenerateMissingNarratives(store, sessions, cfg, qualityClient)
		}
	} else {
		log.Printf("━━━ Phase 4: Session Narratives skipped (up to date) ━━━")
	}

	// ━━━ Phase 4.5: Learning Clustering (Metamemory) ━━━
	log.Printf("━━━ Phase 4.5: Learning Clustering ━━━")
	ClusterLearnings(store, qualityClient, handler.VectorStore())

	// ━━━ Phase 4.6: Recurrence Detection ━━━
	alertCount := DetectRecurrence(store, client)
	if alertCount > 0 {
		log.Printf("⚠ Phase 4.6: %d recurrence alerts generated", alertCount)
	}

	// ━━━ Phase 5: Profiles ━━━
	if pendingProfiles > 0 {
		if !extraction.HasBudget(qualityClient) {
			log.Printf("━━━ Phase 5: Profiles skipped (quality budget exhausted, %d pending) ━━━", pendingProfiles)
		} else {
			log.Printf("━━━ Phase 5: Profiles (%d pending) ━━━", pendingProfiles)
			GenerateProfiles(store, cfg, qualityClient)
		}
	} else {
		log.Printf("━━━ Phase 5: Profiles skipped (up to date) ━━━")
	}

	// ━━━ Phase 6: Persona Signals ━━━
	log.Printf("━━━ Phase 6: Persona Signals ━━━")

	// Bootstrap: if no persona traits exist yet, seed from existing learnings
	// and run signal extraction over ALL sessions (not just last 10)
	if len(existingTraits) == 0 {
		log.Println("  Initial persona bootstrap...")
		bootstrapPersonaFromLearnings(store)
		if extraction.HasBudget(client) {
			extractPersonaSignalsWithLimit(store, sessions, cfg, client, 0)
		} else {
			log.Println("  Persona extraction skipped (budget exhausted)")
		}
	} else if len(toExtract) > 0 {
		if extraction.HasBudget(client) {
			extractPersonaSignals(store, sessions, cfg, client)
		} else {
			log.Println("  Persona extraction skipped (budget exhausted)")
		}
	} else {
		log.Println("  Persona skipped (no new extraction delta)")
		return
	}

	// Persona directive synthesis uses quality model (Opus) for better relationship anchors
	// But first: deduplicate traits that are semantically identical
	if handler.EmbedProvider() != nil {
		dedupPersonaTraits(store, handler.EmbedProvider())
	}

	DecayContextTraits(store)

	synthesizePersonaDirective(store, qualityClient)
	synthesizeUserProfile(store, qualityClient)

	log.Printf("━━━ All phases complete ━━━")
}

func countPendingNarratives(store *storage.Store) (int, error) {
	var count int
	err := store.DB().QueryRow(`SELECT COUNT(*)
		FROM sessions s
		WHERE s.message_count >= ?
		AND s.narrative_at IS NULL`, minNarrativeMessages).Scan(&count)
	return count, err
}

func countPendingProfiles(store *storage.Store) (int, error) {
	var count int
	err := store.DB().QueryRow(`SELECT COUNT(*)
		FROM (
			SELECT project_short, COUNT(*) AS session_count
			FROM sessions
			GROUP BY project_short
			HAVING COUNT(*) >= 3
		) p
		LEFT JOIN project_profiles pp ON pp.project = p.project_short
		WHERE pp.project IS NULL
		OR (pp.model_used != 'manual' AND pp.session_count < MIN(p.session_count, 20))`).Scan(&count)
	return count, err
}

func countPendingRefinedBriefings(store *storage.Store) (int, error) {
	var count int
	err := store.DB().QueryRow(`SELECT COUNT(*)
		FROM (
			SELECT DISTINCT project_short
			FROM sessions
			WHERE project_short != ''
		) p
		LEFT JOIN refined_briefings rb ON rb.project = p.project_short
		WHERE rb.project IS NULL`).Scan(&count)
	return count, err
}

func listProjectsMissingRefinedBriefings(store *storage.Store) ([]storage.ProjectSummary, error) {
	projects, err := store.ListProjects()
	if err != nil {
		return nil, err
	}

	missing := make([]storage.ProjectSummary, 0, len(projects))
	for _, project := range projects {
		if project.ProjectShort == "" {
			continue
		}
		hash, err := store.GetRefinedBriefingHash(project.ProjectShort)
		if err != nil {
			return nil, err
		}
		if hash == "" {
			missing = append(missing, project)
		}
	}
	return missing, nil
}

func hasDocSyncWork(store *storage.Store) bool {
	sources, err := store.ListDocSources("")
	if err != nil || len(sources) == 0 {
		return false
	}
	for _, src := range sources {
		if src.Path == "" {
			continue
		}
		info, err := os.Stat(src.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return true
		}
		if info.ModTime().After(src.LastSync) {
			return true
		}
	}
	return false
}

func isLikelyGitRepo(path string) bool {
	for dir := path; dir != "" && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info != nil {
			return true
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
	}
	return false
}

func buildEvolutionScope(store *storage.Store, sessions []models.Session) (map[string]map[string]struct{}, error) {
	if len(sessions) == 0 {
		return nil, nil
	}

	sessionIDs := make([]string, 0, len(sessions))
	for _, s := range sessions {
		sessionIDs = append(sessionIDs, s.ID)
	}

	learnings, err := store.GetActiveLearningsBySessionIDs(sessionIDs)
	if err != nil {
		return nil, err
	}

	scope := make(map[string]map[string]struct{})
	for _, learning := range learnings {
		if learning.Category == "narrative" || learning.Category == "unfinished" {
			continue
		}
		if _, ok := scope[learning.Project]; !ok {
			scope[learning.Project] = make(map[string]struct{})
		}
		scope[learning.Project][learning.Category] = struct{}{}
	}
	return scope, nil
}

func countEvolutionScope(scope map[string]map[string]struct{}) int {
	total := 0
	for _, categories := range scope {
		total += len(categories)
	}
	return total
}

// runExtractionPhase processes sessions with parallel workers (no evolution).
func RunExtractionPhase(ext extraction.SessionExtractor, store *storage.Store, sessions []models.Session, cfg *config.Config, client extraction.LLMClient) {
	workers := 3
	if client != nil && client.Name() == "cli" {
		workers = 1 // CLI: sequential, Pro/Max rate limits
	}
	startTime := time.Now()
	total := len(sessions)

	var (
		mu     sync.Mutex
		done   int
		errors int
	)

	work := make(chan models.Session, total)
	for _, s := range sessions {
		work <- s
	}
	close(work)

	var wg sync.WaitGroup
	var apiDown int32 // atomic: set to 1 when API is consistently failing
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consecutiveErrors := 0
			for s := range work {
				if atomic.LoadInt32(&apiDown) == 1 {
					mu.Lock()
					done++
					mu.Unlock()
					continue // drain queue without API calls
				}
				// Skip sessions marked for no extraction
				if store.IsExtractionSkipped(s.ID) {
					mu.Lock()
					done++
					mu.Unlock()
					continue
				}
				if err := extractSession(ext, store, s.ID, false); err != nil {
					mu.Lock()
					errors++
					done++
					d := done
					mu.Unlock()
					log.Printf("  [%d/%d] ⚠ %s: %v", d, total, truncID(s.ID), err)
					consecutiveErrors++

					if consecutiveErrors >= 5 {
						log.Printf("  Extraction worker: %d consecutive errors — marking API as down", consecutiveErrors)
						atomic.StoreInt32(&apiDown, 1)
						continue
					}
					if strings.Contains(err.Error(), "rate_limit") {
						time.Sleep(30 * time.Second)
					} else {
						time.Sleep(2 * time.Second)
					}
				} else {
					consecutiveErrors = 0
					store.MarkSessionExtracted(s.ID)
					mu.Lock()
					done++
					d := done
					mu.Unlock()
					log.Printf("  [%d/%d] ✓ %s (%s)", d, total, truncID(s.ID), s.ProjectShort)
					// Yield between sessions — lets BM25 readers through
					runtime.Gosched()
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("  Extraction: %d processed, %d errors, %v (%d workers)", done, errors, time.Since(startTime).Round(time.Second), workers)
}

// extractFromSession extracts learnings from a single newly-indexed session.
func extractFromSession(ext extraction.SessionExtractor, store *storage.Store, jsonlPath string, cfg *config.Config, client extraction.LLMClient, qualityClient extraction.LLMClient) {
	msgs, meta, err := parser.ParseAuto(jsonlPath)
	if err != nil || meta.SessionID == "" || len(msgs) <= 5 {
		return
	}
	meta.SourceAgent = models.NormalizeSourceAgent(meta.SourceAgent)
	meta.SessionID = models.NormalizeSessionID(meta.SourceAgent, meta.SessionID)

	if err := extractSession(ext, store, meta.SessionID, cfg.Evolution.AutoResolve); err != nil {
		log.Printf("warn: extract new session %s: %v", truncID(meta.SessionID), err)
		return
	}
	store.MarkSessionExtracted(meta.SessionID)

	// Generate session narrative (handover to next Claude) — uses quality model
	narrativeClient := qualityClient
	if narrativeClient == nil {
		narrativeClient = client
	}
	generateSessionNarrative(store, msgs, meta, narrativeClient)

}

// generateSessionNarrative creates a narrative summary for the session.
func generateSessionNarrative(store *storage.Store, msgs []models.Message, meta *parser.SessionMeta, client extraction.LLMClient) {
	if client == nil {
		return
	}

	summaries := SummarizeMessages(msgs)
	if len(summaries) < minNarrativeMessages {
		return // Too short to narrate
	}

	project := models.ProjectShortFromPath(meta.Project)
	sessionTime := ""
	if !meta.StartedAt.IsZero() {
		sessionTime = meta.StartedAt.Format("2006-01-02 15:04")
	}
	userMsg := BuildNarrativeUserMessageWithTime(summaries, project, sessionTime)

	response, err := client.Complete(NarrativePrompt(), userMsg)
	if err != nil {
		log.Printf("warn: narrative for %s: %v", truncID(meta.SessionID), err)
		return
	}

	narrative := CleanNarrativeResponse(response)
	if narrative == "" {
		log.Printf("warn: narrative for %s too short, skipping", truncID(meta.SessionID))
		return
	}

	_, err = store.InsertLearning(&models.Learning{
		Category:   "narrative",
		Content:    narrative,
		Project:    project,
		SessionID:  meta.SessionID,
		CreatedAt:  time.Now(),
		ModelUsed:  client.Model(),
		Confidence: 1.0,
	})
	if err != nil {
		log.Printf("warn: save narrative: %v", err)
		return
	}
	if project != "" {
		if n, _ := store.DeleteOldNarratives(project, 2); n > 0 {
			log.Printf("  Narrative cleanup: deleted %d old narratives for %s", n, project)
		}
	}

	store.MarkSessionNarrative(meta.SessionID)
	log.Printf("Narrative saved for session %s (%s)", truncID(meta.SessionID), project)
}

// extractSession runs extraction for a single session by ID.
// autoResolve controls whether evolution checks run inline (true for file-watcher, false for bulk).
func extractSession(ext extraction.SessionExtractor, store *storage.Store, sessionID string, autoResolve bool) error {
	msgs, err := store.GetMessagesBySession(sessionID)
	if err != nil || len(msgs) == 0 {
		return err
	}

	// Look up project for this session
	session, _ := store.GetSession(sessionID)
	project := ""
	if session != nil {
		project = session.ProjectShort
	}

	return ext.ExtractAndStore(sessionID, project, msgs, autoResolve)
}

// generateProfiles creates/updates project profiles for projects with enough sessions.
func GenerateProfiles(store *storage.Store, cfg *config.Config, client extraction.LLMClient) {
	projects, err := store.ListProjects()
	if err != nil {
		return
	}

	if client == nil {
		return
	}

	profilesDone := 0
	for _, p := range projects {
		if p.SessionCount < 3 { // Need at least 3 sessions
			continue
		}

		// Check if profile already exists and is recent
		existing, err := store.GetProjectProfile(p.ProjectShort)
		if err == nil {
			// Never overwrite manually set profiles
			if existing.ModelUsed == "manual" {
				log.Printf("  profile %s: skipped (manual)", p.ProjectShort)
				continue
			}
			// ListSessions caps at 20, so normalize the comparison
			maxSessions := p.SessionCount
			if maxSessions > 20 {
				maxSessions = 20
			}
			if existing.SessionCount >= maxSessions {
				log.Printf("  profile %s: skipped (up to date, %d sessions)", p.ProjectShort, existing.SessionCount)
				continue
			}
		}

		fmt.Fprintf(os.Stderr, "\r  ⟳ Generating profile: %-30s (%d sessions)   ", p.ProjectShort, p.SessionCount)
		if err := extraction.GenerateProjectProfile(client, store, p.ProjectShort); err != nil {
			log.Printf("\n  ⚠ profile %s: %v", p.ProjectShort, err)
		} else {
			profilesDone++
		}

		time.Sleep(1 * time.Second)
	}
	fmt.Fprintf(os.Stderr, "\r%s\r", "                                                                                  ")
	log.Printf("  Profiles generated: %d", profilesDone)
}

// narrativeBudgetBackoff tracks when to retry narrative generation after budget exhaustion.
var narrativeBudgetBackoff time.Time

// generateMissingNarratives creates narrative summaries for sessions that don't have one.
func GenerateMissingNarratives(store *storage.Store, sessions []models.Session, cfg *config.Config, client extraction.LLMClient) {
	if client == nil {
		log.Println("  Skipped: no LLM client")
		return
	}

	if time.Now().Before(narrativeBudgetBackoff) {
		log.Printf("  Narratives deferred: budget backoff until %s", narrativeBudgetBackoff.Format("15:04"))
		return
	}

	// Filter to sessions needing narratives (persistent marker, min messages)
	var toProcess []models.Session
	for _, s := range sessions {
		if s.NarrativeAt.IsZero() && s.MessageCount >= minNarrativeMessages {
			toProcess = append(toProcess, s)
		}
	}

	if len(toProcess) == 0 {
		log.Println("  All sessions have narratives")
		return
	}

	workers := 3
	if client.Name() == "cli" {
		workers = 1 // CLI: sequential, Pro/Max rate limits
	}
	total := len(toProcess)
	log.Printf("  %d sessions need narratives (%d workers)", total, workers)

	var (
		mu        sync.Mutex
		generated int
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
				msgs, err := store.GetMessagesBySession(s.ID)
				if err != nil || len(msgs) < minNarrativeMessages {
					continue
				}

				summaries := SummarizeMessages(msgs)
				if len(summaries) < minNarrativeMessages {
					continue
				}

				project := s.ProjectShort
				userMsg := BuildNarrativeUserMessageWithTime(summaries, project, sessionTimeRange(s))

				response, err := client.Complete(NarrativePrompt(), userMsg)
				if err != nil {
					if strings.Contains(err.Error(), "budget exceeded") {
						mu.Lock()
						narrativeBudgetBackoff = time.Now().Add(1 * time.Hour)
						mu.Unlock()
						log.Printf("  Narratives: budget exceeded, backing off until %s", narrativeBudgetBackoff.Format("15:04"))
						return
					}
					if strings.Contains(err.Error(), "rate_limit") {
						time.Sleep(30 * time.Second)
					}
					continue
				}

				narrative := CleanNarrativeResponse(response)
				if narrative == "" {
					continue
				}

				store.InsertLearning(&models.Learning{
					Category:   "narrative",
					Content:    narrative,
					Project:    project,
					SessionID:  s.ID,
					CreatedAt:  s.StartedAt,
					ModelUsed:  client.Model(),
					Confidence: 1.0,
					Source:     "llm_extracted",
				})
				if project != "" {
					if n, _ := store.DeleteOldNarratives(project, 2); n > 0 {
						log.Printf("  Narrative cleanup: deleted %d old narratives for %s", n, project)
					}
				}
				store.MarkSessionNarrative(s.ID)

				mu.Lock()
				generated++
				g := generated
				mu.Unlock()
				if g%10 == 0 {
					log.Printf("  Narratives: %d/%d generated", g, total)
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("  Narratives complete: %d generated", generated)
}

// extractionNeeded checks if the session has already been extracted.
func extractionNeeded(store *storage.Store, sessionID string) bool {
	sess, err := store.GetSession(sessionID)
	if err != nil {
		return true // can't check → assume needed
	}
	return sess.ExtractedAt.IsZero()
}

// runBatchExtraction runs Phase 2 (extraction) + 2.5 (embed) + 3 (evolution) for all pending sessions.
// Called by the batch extraction cycle goroutine.
func runBatchExtraction(ext extraction.SessionExtractor, evoExt *extraction.Extractor, store *storage.Store, cfg *config.Config, client extraction.LLMClient, qualityClient extraction.LLMClient, handler *Handler) {
	sessions, err := store.ListAllSessions("", 0)
	if err != nil {
		log.Printf("warn: batch extraction list sessions: %v", err)
		return
	}

	// Cleanup: mark short sessions
	if marked, err := store.MarkShortSessionsExtracted(5); err == nil && marked > 0 {
		log.Printf("  Marked %d short sessions (≤5 messages) as extracted", marked)
	}

	// Build pending list — skip sessions younger than min_session_age_hours (default 24h)
	// because the forked agent proxy handles real-time extraction for active sessions.
	minAgeH := cfg.Extraction.MinSessionAgeH
	if minAgeH <= 0 {
		minAgeH = 24
	}
	cutoff := time.Now().Add(-time.Duration(minAgeH) * time.Hour)
	var toExtract []models.Session
	for _, s := range sessions {
		if s.ExtractedAt.IsZero() && s.MessageCount > 5 && s.StartedAt.Before(cutoff) {
			toExtract = append(toExtract, s)
		}
	}
	toExtract = FilterByMaxAge(toExtract, cfg.Extraction.MaxAgeDays)

	// Sort newest first
	sort.Slice(toExtract, func(i, j int) bool {
		return toExtract[i].StartedAt.After(toExtract[j].StartedAt)
	})

	if cfg.Extraction.MaxPerRun > 0 && len(toExtract) > cfg.Extraction.MaxPerRun {
		toExtract = toExtract[:cfg.Extraction.MaxPerRun]
	}

	if len(toExtract) == 0 {
		return
	}

	// Phase 2: Extraction
	if !extraction.HasBudget(client) {
		log.Println("  Batch extraction skipped (budget exhausted)")
		return
	}
	log.Printf("  Phase 2: Extracting %d sessions", len(toExtract))
	RunExtractionPhase(ext, store, toExtract, cfg, client)

	// Phase 2.5: Embed (sync, needed for evolution)
	if handler.VectorStore() != nil {
		pending, _ := store.GetPendingLearningsForEmbedding(0)
		if len(pending) > 0 {
			log.Printf("  Phase 2.5: Embed %d learnings", len(pending))
			bin, _ := os.Executable()
			cmd := exec.Command(bin, "embed-learnings")
			homeDir, _ := os.UserHomeDir()
			logPath := filepath.Join(homeDir, ".claude", "yesmem", "logs", "embed.log")
			os.MkdirAll(filepath.Dir(logPath), 0755)
			if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
				cmd.Stdout = logFile
				cmd.Stderr = logFile
			}
			if err := cmd.Run(); err != nil {
				log.Printf("  Embed: failed: %v", err)
			}
		}
	}

	// Phase 3: Evolution (scoped to extracted sessions)
	if cfg.Evolution.AutoResolve && extraction.HasBudget(client) {
		evolutionScope, err := buildEvolutionScope(store, toExtract)
		if err == nil && len(evolutionScope) > 0 {
			log.Printf("  Phase 3: Evolution (%d project/category groups)", countEvolutionScope(evolutionScope))
			checked, superseded := evoExt.RunEvolutionForScope(store, evolutionScope, nil)
			log.Printf("  Evolution: %d checked, %d superseded", checked, superseded)
		}
	}

	log.Printf("━━━ Batch extraction complete ━━━")
}

// placeholder to satisfy import
var _ models.Session

// truncID safely truncates an ID to 8 chars for logging.
func truncID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
