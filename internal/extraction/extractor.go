package extraction

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// SessionExtractor is the common interface for single-pass and two-pass extractors.
type SessionExtractor interface {
	ExtractFromSession(sessionID string, msgs []models.Message) ([]models.Learning, error)
	ExtractAndStore(sessionID, project string, msgs []models.Message, autoResolve bool) error
}

// Extractor orchestrates LLM-based knowledge extraction from sessions (single-pass).
type Extractor struct {
	client LLMClient
	store  *storage.Store
	model  string
}

// NewExtractor creates an extractor with an LLMClient.
func NewExtractor(client LLMClient, store *storage.Store) *Extractor {
	return &Extractor{
		client: client,
		store:  store,
		model:  client.Model(),
	}
}

// learningItem represents a single structured learning from the LLM extraction response.
type learningItem struct {
	Category           string   `json:"category"`
	Content            string   `json:"content"`
	Context            string   `json:"context"`
	Entities           []string `json:"entities"`
	Actions            []string `json:"actions"`
	Keywords           []string `json:"keywords"`
	Trigger            string   `json:"trigger"`
	AnticipatedQueries []string `json:"anticipated_queries"`
	Importance         int      `json:"importance"`
	TaskType           string   `json:"task_type,omitempty"`
}

// extractionResult holds the parsed LLM response (flat list format).
type extractionResult struct {
	Domain                    string         `json:"domain"`
	Learnings                 []learningItem `json:"learnings"`
	SessionEmotionalIntensity float64        `json:"session_emotional_intensity"`
	SessionFlavor             string         `json:"session_flavor"`
	// CLI provider returns categorized fields instead of flat learnings array
	Gotchas           []learningItem `json:"gotchas"`
	ExplicitTeachings []learningItem `json:"explicit_teachings"`
	Decisions         []learningItem `json:"decisions"`
	Patterns          []learningItem `json:"patterns"`
	UserPreferences   []learningItem `json:"user_preferences"`
	Unfinished        []learningItem `json:"unfinished"`
	Relationships     []learningItem `json:"relationships"`
	PivotMoments      []learningItem `json:"pivot_moments"`
}

// allLearnings merges categorized fields into the flat learnings array,
// setting category from the field name if not already set.
func (r *extractionResult) allLearnings() []learningItem {
	all := append([]learningItem{}, r.Learnings...)
	for cat, items := range map[string][]learningItem{
		"gotcha":            r.Gotchas,
		"explicit_teaching": r.ExplicitTeachings,
		"decision":          r.Decisions,
		"pattern":           r.Patterns,
		"user_preference":   r.UserPreferences,
		"unfinished":        r.Unfinished,
		"relationship":      r.Relationships,
		"pivot_moment":      r.PivotMoments,
	} {
		for _, item := range items {
			if item.Category == "" {
				item.Category = cat
			}
			all = append(all, item)
		}
	}
	return all
}

// ExtractFromSession runs the full extraction pipeline for a session.
func (e *Extractor) ExtractFromSession(sessionID string, msgs []models.Message) ([]models.Learning, error) {
	// Pre-filter noise (system-reminders, briefing blocks, tool_results etc.)
	msgs = PreFilterMessages(msgs)

	// Chunk messages
	chunks := ChunkMessages(msgs, 25000)

	var allLearnings []models.Learning
	for i, chunk := range chunks {
		// Pause between chunks (Haiku: ~60 req/min limit)
		if i > 0 {
			time.Sleep(2 * time.Second)
		}

		content := chunk.Content
		if chunk.PrevSummary != "" {
			content = chunk.PrevSummary + "\n\n" + content
		}

		// Call LLM with structured output (guaranteed valid JSON)
		response, err := e.client.CompleteJSON(BuildExtractionSystemPrompt(), content, ExtractionSchema())
		if err != nil {
			log.Printf("warn: extraction chunk %d/%d for %s: %v", chunk.Index+1, chunk.Total, sessionID, err)
			if strings.Contains(err.Error(), "rate_limit") {
				log.Printf("  Rate limited on chunk — waiting 60s")
				time.Sleep(60 * time.Second)
			}
			continue
		}

		// Structured output guarantees valid JSON — parse directly
		if strings.TrimSpace(response) == "" {
			continue
		}

		learnings, err := parseExtractionResponse(response, sessionID, e.model)
		if err != nil {
			log.Printf("warn: parse extraction for %s chunk %d: %v", sessionID, chunk.Index+1, err)
			continue
		}

		// Set lineage — which messages this chunk covered
		for i := range learnings {
			learnings[i].SourceMsgFrom = chunk.FromMsgIdx
			learnings[i].SourceMsgTo = chunk.ToMsgIdx
		}

		allLearnings = append(allLearnings, learnings...)
	}

	return allLearnings, nil
}

// mustJSON marshals a string to a JSON string (with proper escaping).
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// ExtractAndStore extracts learnings and stores them, with optional evolution check.
func (e *Extractor) ExtractAndStore(sessionID, project string, msgs []models.Message, autoResolve bool) error {
	learnings, err := e.ExtractFromSession(sessionID, msgs)
	if err != nil {
		return err
	}

	// Filter junk, collect valid learnings for batch insert
	var valid []*models.Learning
	for i := range learnings {
		content := strings.TrimSpace(learnings[i].Content)
		if len(content) < 10 || content[0] == '{' || content[0] == '[' || strings.HasPrefix(content, "```") {
			continue
		}
		learnings[i].Project = project
		// Parse deadline trigger into ExpiresAt
		if expires := ParseDeadlineExpiry(learnings[i].TriggerRule); expires != nil {
			learnings[i].ExpiresAt = expires
		}
		valid = append(valid, &learnings[i])
	}

	// Pre-admission dedup: check each learning against existing corpus
	var toInsert []*models.Learning
	var preSkipped, preUpdated int
	for _, l := range valid {
		result := CheckPreAdmission(e.store, l)
		switch result.Action {
		case PreAdmissionSkip:
			preSkipped++
		case PreAdmissionUpdate:
			if err := e.store.UpdateLearningContent(result.ExistingID, l.Content); err == nil {
				e.store.IncrementMatchCounts([]int64{result.ExistingID})
				preUpdated++
			} else {
				toInsert = append(toInsert, l)
			}
		case PreAdmissionInsert:
			toInsert = append(toInsert, l)
		}
	}
	if preSkipped > 0 || preUpdated > 0 {
		log.Printf("Pre-admission: %d skipped, %d updated, %d new (session %s)", preSkipped, preUpdated, len(toInsert), truncID(sessionID))
	}

	// Single transaction for genuinely new learnings
	ids, err := e.store.InsertLearningBatch(toInsert)
	if err != nil {
		log.Printf("warn: batch store learnings: %v", err)
	}

	// Evolution runs after commit (only for single-session file watcher extraction)
	if autoResolve {
		for i, id := range ids {
			time.Sleep(2 * time.Second)
			e.resolveConflicts(id, toInsert[i])
		}
	}

	log.Printf("Extracted %d learnings from session %s", len(ids), truncID(sessionID))
	return nil
}

// truncID safely truncates an ID to 8 chars for logging.
func truncID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// RunEvolution runs bulk conflict resolution — one LLM call per project+category.
// Keeps batches small enough for Haiku's 4096 output token limit.
func (e *Extractor) RunEvolution(store *storage.Store, onSupersede func(int64), sinceTime ...time.Time) (int, int) {
	return e.runEvolution(store, onSupersede, nil)
}

// RunEvolutionForScope limits the evolution pass to project/category pairs touched by new extraction work.
// Cross-project evolution is still allowed, but only for categories seen in the scoped delta.
func (e *Extractor) RunEvolutionForScope(store *storage.Store, scope map[string]map[string]struct{}, onSupersede func(int64)) (int, int) {
	if len(scope) == 0 {
		return 0, 0
	}
	return e.runEvolution(store, onSupersede, scope)
}

func (e *Extractor) runEvolution(store *storage.Store, onSupersede func(int64), scope map[string]map[string]struct{}) (int, int) {
	categories, err := store.GetActiveCategories()
	if err != nil {
		log.Printf("warn: get categories for evolution: %v", err)
		return 0, 0
	}
	// Exclude narrative — handled separately by SupersedeNarrativesBySession
	var filtered []string
	for _, c := range categories {
		if c != "narrative" {
			filtered = append(filtered, c)
		}
	}
	categories = filtered

	totalChecked := 0
	totalSuperseded := 0

	// Collect all active learnings, group by project+category
	type groupKey struct{ project, category string }
	groups := map[groupKey][]models.Learning{}
	scopedCategories := map[string]struct{}{}

	for _, cat := range categories {
		if scope == nil {
			learnings, err := store.GetActiveLearnings(cat, "", "", "")
			if err != nil {
				continue
			}
			for _, l := range learnings {
				key := groupKey{project: l.Project, category: cat}
				groups[key] = append(groups[key], l)
			}
			continue
		}

		for project, categories := range scope {
			if _, ok := categories[cat]; !ok {
				continue
			}
			learnings, err := store.GetActiveLearnings(cat, project, "", "")
			if err != nil {
				continue
			}
			if len(learnings) > 0 {
				scopedCategories[cat] = struct{}{}
			}
			for _, l := range learnings {
				key := groupKey{project: l.Project, category: cat}
				groups[key] = append(groups[key], l)
			}
		}
	}

	calls := 0
	consecutiveErrors := 0
	const maxConsecutiveErrors = 3
	for key, learnings := range groups {
		if len(learnings) < 2 {
			continue
		}

		label := fmt.Sprintf("%s/%s", key.project, key.category)
		if key.project == "" {
			label = "(global)/" + key.category
		}

		// B1.1: Cross-chunk rule-based dedup BEFORE chunking.
		// BigramJaccard is cheap (no LLM), so run over all learnings in the category.
		var preCleaned []models.Learning
		preSuperseded := make(map[int64]int64)
		for _, l := range learnings {
			if IsSubstanzlos(l.Content) {
				preSuperseded[l.ID] = 0
				continue
			}
			isDupe := false
			for _, kept := range preCleaned {
				if BigramJaccard(l.Content, kept.Content) > 0.85 {
					if l.ID > kept.ID {
						preSuperseded[kept.ID] = l.ID
						for ci := range preCleaned {
							if preCleaned[ci].ID == kept.ID {
								preCleaned[ci] = l
								break
							}
						}
					} else {
						preSuperseded[l.ID] = kept.ID
					}
					isDupe = true
					break
				}
			}
			if !isDupe {
				preCleaned = append(preCleaned, l)
			}
		}
		if len(preSuperseded) > 0 {
			log.Printf("  Evolution: %s cross-chunk pre-dedup: %d superseded", label, len(preSuperseded))
			for loserID, winnerID := range preSuperseded {
				if err := store.SupersedeLearning(loserID, winnerID, "rule-based: cross-chunk near-duplicate"); err == nil {
					totalSuperseded++
					if onSupersede != nil {
						onSupersede(loserID)
					}
				}
			}
		}
		learnings = preCleaned
		if len(learnings) < 2 {
			continue
		}

		// Embedding-based pre-dedup: cosine similarity on existing vectors.
		// Runs BEFORE chunking so it sees the full group, not just 50-item slices.
		// Catches semantic duplicates that BigramJaccard misses.
		embIDs := make([]int64, len(learnings))
		for i, l := range learnings {
			embIDs[i] = l.ID
		}
		vectors := LoadVectorsForLearnings(store, embIDs)
		if len(vectors) >= 2 {
			embDupes := FindEmbeddingDuplicates(learnings, vectors, 0.92)
			if len(embDupes) > 0 {
				log.Printf("  Evolution: %s embedding pre-dedup: %d superseded", label, len(embDupes))
				for loserID, winnerID := range embDupes {
					if err := store.SupersedeLearning(loserID, winnerID, "rule-based: embedding near-duplicate"); err == nil {
						totalSuperseded++
						if onSupersede != nil {
							onSupersede(loserID)
						}
					}
				}
				var remaining []models.Learning
				for _, l := range learnings {
					if _, superseded := embDupes[l.ID]; !superseded {
						remaining = append(remaining, l)
					}
				}
				learnings = remaining
				if len(learnings) < 2 {
					continue
				}
			}
		}

		chunks := chunkLearnings(learnings, 50)
		for ci, chunk := range chunks {
			calls++
			chunkLabel := label
			if len(chunks) > 1 {
				chunkLabel = fmt.Sprintf("%s [%d/%d]", label, ci+1, len(chunks))
			}
			log.Printf("  Evolution: %s (%d learnings)", chunkLabel, len(chunk))

			// Rule-based pre-dedup: remove substanzlos + exact/near-duplicates
			var cleaned []models.Learning
			autoSuperseded := make(map[int64]int64) // loserID -> winnerID (0 = no winner for substanzlos)
			for _, l := range chunk {
				if IsSubstanzlos(l.Content) {
					autoSuperseded[l.ID] = 0
					continue
				}
				isDupe := false
				for _, kept := range cleaned {
					if BigramJaccard(l.Content, kept.Content) > 0.85 {
						if l.ID > kept.ID {
							autoSuperseded[kept.ID] = l.ID
							for ci := range cleaned {
								if cleaned[ci].ID == kept.ID {
									cleaned[ci] = l
									break
								}
							}
						} else {
							autoSuperseded[l.ID] = kept.ID
						}
						isDupe = true
						break
					}
				}
				if !isDupe {
					cleaned = append(cleaned, l)
				}
			}

			for loserID, winnerID := range autoSuperseded {
				if err := store.SupersedeLearning(loserID, winnerID, "rule-based: substanzlos or near-duplicate"); err == nil {
					totalSuperseded++
					if onSupersede != nil {
						onSupersede(loserID)
					}
					log.Printf("  #%d auto-superseded (rule-based)", loserID)
				}
			}
			totalChecked += len(chunk)

			if len(cleaned) < 2 {
				continue
			}
			chunk = cleaned

			var list strings.Builder
			for _, l := range chunk {
				list.WriteString(fmt.Sprintf("#%d: %s\n", l.ID, l.Content))
			}

			prompt := fmt.Sprintf("Project: %s, Category: %s (%d learnings)\n\n%s\nFind duplicates, near-duplicates, and contradictions. Newer learning (higher ID) wins.",
				key.project, key.category, len(chunk), list.String())

			response, err := e.client.CompleteJSON(BulkEvolutionSystemPrompt, prompt, EvolutionSchema())
			if err != nil {
				log.Printf("  warn: bulk evolution for %s: %v", chunkLabel, err)
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					log.Printf("  Evolution aborted: %d consecutive API errors", consecutiveErrors)
					goto evolutionDone
				}
				if strings.Contains(err.Error(), "rate_limit") {
					time.Sleep(60 * time.Second)
				} else {
					time.Sleep(5 * time.Second)
				}
				continue
			}
			consecutiveErrors = 0

			superseded := e.applyEvolutionResponse(response, chunkLabel, store, onSupersede)
			totalSuperseded += superseded
		}
	}

evolutionDone:
	log.Printf("  Evolution: %d calls, %d checked, %d superseded", calls, totalChecked, totalSuperseded)

	// ━━━ Cross-Project Pass: find global truths duplicated across projects ━━━
	// Reuse dynamic categories (narrative already excluded)
	for _, cat := range categories {
		if scope != nil {
			if _, ok := scopedCategories[cat]; !ok {
				continue
			}
		}
		all, err := store.GetActiveLearnings(cat, "", "", "")
		if err != nil || len(all) < 2 {
			continue
		}

		// Group by project, skip if only one project
		byProject := map[string][]models.Learning{}
		for _, l := range all {
			byProject[l.Project] = append(byProject[l.Project], l)
		}
		if len(byProject) < 2 {
			continue
		}

		// Sample: take top 10 from each project (newest = highest ID = last in slice)
		var sample []models.Learning
		for _, pLearnings := range byProject {
			start := 0
			if len(pLearnings) > 10 {
				start = len(pLearnings) - 10
			}
			sample = append(sample, pLearnings[start:]...)
		}
		if len(sample) < 2 {
			continue
		}

		calls++
		log.Printf("  Cross-project: %s (%d learnings from %d projects)", cat, len(sample), len(byProject))

		var list strings.Builder
		for _, l := range sample {
			proj := l.Project
			if proj == "" {
				proj = "(global)"
			}
			list.WriteString(fmt.Sprintf("#%d [%s]: %s\n", l.ID, proj, l.Content))
		}

		prompt := fmt.Sprintf("Kategorie: %s — Learnings aus %d Projekten:\n\n%s",
			cat, len(byProject), list.String())

		response, err := e.client.CompleteJSON(CrossProjectEvolutionPrompt, prompt, EvolutionSchema())
		if err != nil {
			log.Printf("  warn: cross-project evolution for %s: %v", cat, err)
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				log.Printf("  Cross-project evolution aborted: %d consecutive API errors", consecutiveErrors)
				break
			}
			if strings.Contains(err.Error(), "rate_limit") {
				time.Sleep(60 * time.Second)
			} else {
				time.Sleep(5 * time.Second)
			}
			continue
		}
		consecutiveErrors = 0

		superseded := e.applyEvolutionResponse(response, "cross/"+cat, store, onSupersede)
		totalChecked += len(sample)
		totalSuperseded += superseded
	}

	log.Printf("  Evolution total: %d calls, %d checked, %d superseded", calls, totalChecked, totalSuperseded)
	return totalChecked, totalSuperseded
}

// chunkLearnings splits learnings into batches of maxSize.
func chunkLearnings(learnings []models.Learning, maxSize int) [][]models.Learning {
	if maxSize <= 0 {
		maxSize = 50
	}
	var chunks [][]models.Learning
	for i := 0; i < len(learnings); i += maxSize {
		end := i + maxSize
		if end > len(learnings) {
			end = len(learnings)
		}
		chunks = append(chunks, learnings[i:end])
	}
	return chunks
}

// applyEvolutionResponse parses and applies evolution actions from a bulk evolution response.
func (e *Extractor) applyEvolutionResponse(response, label string, store *storage.Store, onSupersede func(int64)) int {
	var evoResp evolutionResponse
	if err := json.Unmarshal([]byte(response), &evoResp); err != nil {
		log.Printf("  warn: parse bulk evolution for %s: %v", label, err)
		return 0
	}

	superseded := 0
	for _, action := range evoResp.Actions {
		switch action.Type {
		case "supersede":
			if len(action.SupersedesIDs) == 0 {
				continue
			}
			if len(action.SupersedesIDs) == 1 {
				// Single ID = only a winner, no losers → no-op.
				// The LLM prompt defines: first ID = winner, rest = losers.
				continue
			}
			// First ID = winner, rest = losers
			winnerID := action.SupersedesIDs[0]
			for _, loserID := range action.SupersedesIDs[1:] {
				loser, err := store.GetLearning(loserID)
				if err != nil {
					log.Printf("  warn: get learning #%d for trust check: %v", loserID, err)
					continue
				}
				trust := storage.TrustScore(loser)
				if storage.ClassifyTrust(trust) == storage.TrustHigh {
					store.SetSupersedeStatus(loserID, "pending_confirmation")
					log.Printf("  #%d: supersede blocked (trust %.1f, high) — set pending_confirmation", loserID, trust)
					continue
				}
				if err := store.SupersedeLearning(loserID, winnerID, action.Reason); err == nil {
					superseded++
					if onSupersede != nil {
						onSupersede(loserID)
					}
					log.Printf("  #%d supersedes #%d: %s (trust %.1f)", winnerID, loserID, action.Reason, trust)
				}
			}

		case "update":
			for _, targetID := range action.SupersedesIDs {
				if err := store.UpdateLearningContent(targetID, action.NewLearning); err != nil {
					log.Printf("  warn: update learning #%d: %v", targetID, err)
				} else {
					store.IncrementMatchCounts([]int64{targetID})
					log.Printf("  #%d updated in-place: %s", targetID, action.Reason)
				}
			}

		case "confirmation":
			if len(action.SupersedesIDs) > 0 {
				store.IncrementUseCounts(action.SupersedesIDs)
				log.Printf("  Learnings %v confirmed: %s", action.SupersedesIDs, action.Reason)
			}

		case "independent":
			// no-op

		default:
			log.Printf("  warn: unknown evolution action type: %s", action.Type)
		}
	}
	return superseded
}

// stripMarkdownFence removes ```json ... ``` wrapping from LLM responses.
// The CLI provider (claude -p) returns markdown-fenced JSON even with --output-format text.
func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

func parseExtractionResponse(response, sessionID, model string) ([]models.Learning, error) {
	jsonStr := stripMarkdownFence(response)

	var result extractionResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse json: %w (response: %.200s)", err, response)
	}

	now := time.Now()

	// Clamp emotional intensity to [0, 1.0] before storing
	intensity := result.SessionEmotionalIntensity
	if intensity < 0 {
		intensity = 0
	}
	if intensity > 1.0 {
		intensity = 1.0
	}
	// Truncate flavor to 80 runes (UTF-8 safe)
	flavor := result.SessionFlavor
	runes := []rune(flavor)
	if len(runes) > 80 {
		flavor = string(runes[:80])
	}

	domain := result.Domain
	if domain == "" {
		domain = "general"
	}

	var learnings []models.Learning

	// Category mapping from schema enum to internal names
	categoryMap := map[string]string{
		"explicit_teaching": "explicit_teaching",
		"gotcha":            "gotcha",
		"decision":          "decision",
		"pattern":           "pattern",
		"user_preference":   "preference",
		"unfinished":        "unfinished",
		"relationship":      "relationship",
		"pivot_moment":      "pivot_moment",
	}

	for _, item := range result.allLearnings() {
		if len([]rune(item.Content)) < 10 {
			continue
		}
		cat, ok := categoryMap[item.Category]
		if !ok {
			cat = item.Category // fallback: use as-is
		}
		importance := item.Importance
		if importance < 1 || importance > 5 {
			importance = 3
		}
		l := models.Learning{
			SessionID:          sessionID,
			Category:           cat,
			Content:            item.Content,
			Context:            item.Context,
			Domain:             domain,
			TriggerRule:        item.Trigger,
			Entities:           item.Entities,
			Actions:            item.Actions,
			Keywords:           item.Keywords,
			AnticipatedQueries: item.AnticipatedQueries,
			Confidence:         1.0,
			CreatedAt:          now,
			ModelUsed:          model,
			Source:             "llm_extracted",
			EmotionalIntensity: intensity,
			SessionFlavor:      flavor,
			Importance:         importance,
			TaskType:           item.TaskType,
		}
		l.EmbeddingText = l.BuildEmbeddingText()
		learnings = append(learnings, l)
	}

	return learnings, nil
}

// addLearningsV2 converts structured learningItems into models.Learning objects with full V2 metadata.
func addLearningsV2(items []learningItem, category, sessionID, model, domain string, intensity float64, flavor string, ts time.Time, project string) []models.Learning {
	var learnings []models.Learning
	for _, item := range items {
		if len([]rune(item.Content)) < 10 {
			continue
		}
		importance := item.Importance
		if importance < 1 || importance > 5 {
			importance = 3
		}
		l := models.Learning{
			SessionID:          sessionID,
			Category:           category,
			Content:            item.Content,
			Context:            item.Context,
			Domain:             domain,
			TriggerRule:        item.Trigger,
			Entities:           item.Entities,
			Actions:            item.Actions,
			Keywords:           item.Keywords,
			AnticipatedQueries: item.AnticipatedQueries,
			Project:            project,
			Confidence:         1.0,
			CreatedAt:          ts,
			ModelUsed:          model,
			Source:             "llm_extracted",
			EmotionalIntensity: intensity,
			SessionFlavor:      flavor,
			Importance:         importance,
			TaskType:           item.TaskType,
		}
		l.EmbeddingText = l.BuildEmbeddingText()
		learnings = append(learnings, l)
	}
	return learnings
}

// extractJSON tries to find a JSON object in the response text.
func extractJSON(s string) string {
	// Find first { and last }
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}

	depth := 0
	end := -1
	for i := start; i < len(s); i++ {
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}
	if end < 0 {
		return s[start:]
	}
	return s[start:end]
}

// TwoPassExtractor uses two LLM passes: summarize (Haiku) then extract (Sonnet).
type TwoPassExtractor struct {
	summarizeClient LLMClient // Pass 1: chunk → summary (Haiku)
	extractClient   LLMClient // Pass 2: summaries → learnings (Sonnet)
	store           *storage.Store
}

// NewTwoPassExtractor creates a two-pass extractor with separate clients for each pass.
func NewTwoPassExtractor(summarizeClient, extractClient LLMClient, store *storage.Store) *TwoPassExtractor {
	return &TwoPassExtractor{
		summarizeClient: summarizeClient,
		extractClient:   extractClient,
		store:           store,
	}
}

// ExtractFromSession runs the two-pass extraction pipeline.
func (e *TwoPassExtractor) ExtractFromSession(sessionID string, msgs []models.Message) ([]models.Learning, error) {
	// Pre-filter: remove noise
	filtered := PreFilterMessages(msgs)

	// Chunk the filtered messages
	chunks := ChunkMessages(filtered, 25000)

	// Pass 1: Summarize each chunk with Haiku
	var summaries []string
	for i, chunk := range chunks {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}

		content := chunk.Content
		if chunk.PrevSummary != "" {
			content = chunk.PrevSummary + "\n\n" + content
		}

		summary, err := e.summarizeClient.Complete(SummarizeSystemPrompt, content, WithMaxTokens(1024))
		if err != nil {
			log.Printf("warn: summarize chunk %d/%d for %s: %v", chunk.Index+1, chunk.Total, sessionID, err)
			if strings.Contains(err.Error(), "rate_limit") {
				time.Sleep(60 * time.Second)
			}
			continue
		}

		summary = strings.TrimSpace(summary)
		if summary != "" {
			summaries = append(summaries, fmt.Sprintf("=== Teil %d/%d ===\n%s", chunk.Index+1, chunk.Total, summary))
		}
	}

	if len(summaries) == 0 {
		return nil, nil
	}

	// Pass 2: Extract from combined summaries with Sonnet
	combined := strings.Join(summaries, "\n\n")

	response, err := e.extractClient.CompleteJSON(BuildExtractionSystemPrompt(), combined, ExtractionSchema())
	if err != nil {
		return nil, fmt.Errorf("extraction pass 2: %w", err)
	}

	if strings.TrimSpace(response) == "" {
		return nil, nil
	}

	learnings, err := parseExtractionResponse(response, sessionID, e.extractClient.Model())
	if err != nil {
		return learnings, err
	}

	// Set lineage — two-pass covers the full range of all chunks
	if len(chunks) > 0 {
		fromIdx := chunks[0].FromMsgIdx
		toIdx := chunks[len(chunks)-1].ToMsgIdx
		for i := range learnings {
			learnings[i].SourceMsgFrom = fromIdx
			learnings[i].SourceMsgTo = toIdx
		}
	}

	return learnings, nil
}

// ExtractAndStore extracts and stores learnings (same interface as single-pass Extractor).
// Note: autoResolve is intentionally ignored — two-pass mode relies on bulk evolution
// (Phase 3) rather than inline conflict resolution, since the extract client (Sonnet)
// is different from the evolution client (Haiku).
func (e *TwoPassExtractor) ExtractAndStore(sessionID, project string, msgs []models.Message, autoResolve bool) error {
	learnings, err := e.ExtractFromSession(sessionID, msgs)
	if err != nil {
		return err
	}

	// Filter junk, collect valid learnings for batch insert
	var valid []*models.Learning
	for i := range learnings {
		content := strings.TrimSpace(learnings[i].Content)
		if len(content) < 10 || content[0] == '{' || content[0] == '[' || strings.HasPrefix(content, "```") {
			continue
		}
		learnings[i].Project = project
		// Parse deadline trigger into ExpiresAt
		if expires := ParseDeadlineExpiry(learnings[i].TriggerRule); expires != nil {
			learnings[i].ExpiresAt = expires
		}
		valid = append(valid, &learnings[i])
	}

	// Single transaction for all learnings from this session
	ids, err := e.store.InsertLearningBatch(valid)
	if err != nil {
		log.Printf("warn: batch store learnings: %v", err)
	}

	log.Printf("Extracted %d learnings from session %s (two-pass)", len(ids), truncID(sessionID))
	return nil
}
