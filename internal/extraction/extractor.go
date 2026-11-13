package extraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// SessionExtractor is the common interface for single-pass and two-pass extractors.
type SessionExtractor interface {
	ExtractFromSession(sessionID string, msgs []models.Message) ([]models.Learning, error)
	ExtractAndStore(sessionID, project string, msgs []models.Message, autoResolve bool) error
	// SetChunkSize sets the token budget per extraction chunk
	// (extraction.chunk_size). Zero or negative restores the default.
	SetChunkSize(tokens int)
}

// DefaultChunkSize is the token budget per extraction chunk when the config
// leaves extraction.chunk_size empty.
const DefaultChunkSize = 25000

// ErrParse marks a failure in which the model answered but the answer could not
// be read as extraction output. The API is up in that case, so it is not an
// outage; callers gate the outage breaker on IsOutage and treat every non-nil
// extraction error as a failed attempt.
var ErrParse = errors.New("unparseable model output")

// ErrPartial marks a pass in which at least one chunk produced a usable result
// and at least one failed. What was extracted is real and must be stored, but
// the pass proved nothing about the failed chunks, so the caller must not
// certify the session as extracted either.
var ErrPartial = errors.New("partial extraction")

// chunkFailures carries the failure classes of one extraction pass. A pass can
// fail in both ways at once: a chunk whose call failed is a transport failure,
// one that answered unreadably is a content failure. The class stays explicit so
// a mixed pass is neither reported as a pure content failure nor read as an
// outage by the breaker.
type chunkFailures struct {
	chunks       int
	succeeded    int
	transport    int
	parse        int
	outage       bool
	transportErr error
	parseErr     error
}

func (f *chunkFailures) Error() string {
	parts := []string{fmt.Sprintf("%d of %d extraction steps failed (%d transport, %d content)",
		f.transport+f.parse, f.chunks, f.transport, f.parse)}
	if f.transportErr != nil {
		parts = append(parts, "transport: "+textutil.SingleLine(f.transportErr.Error(), 300))
	}
	if f.parseErr != nil {
		parts = append(parts, "content: "+textutil.SingleLine(f.parseErr.Error(), 300))
	}
	return strings.Join(parts, "; ")
}

// Unwrap reports both failure causes, so errors.Is reaches either of them.
func (f *chunkFailures) Unwrap() []error {
	out := make([]error, 0, 2)
	if f.transportErr != nil {
		out = append(out, f.transportErr)
	}
	if f.parseErr != nil {
		out = append(out, f.parseErr)
	}
	return out
}

// Is answers the classes callers gate on. ErrPartial means at least one chunk
// produced a usable result. ErrParse means the model answered unreadably and no
// transport failure occurred in the same pass.
func (f *chunkFailures) Is(target error) bool {
	switch target {
	case ErrPartial:
		return f.succeeded > 0
	case ErrParse:
		return f.parse > 0 && f.transport == 0
	}
	return false
}

// IsOutage reports whether err is evidence that the transport, not the model,
// failed: every chunk's call failed, or the caller marked a single decisive call
// as an outage (the two-pass extraction call). A chunk-level answer, even an
// unparseable one, proves the API is reachable and must not trip the breaker.
func IsOutage(err error) bool {
	var f *chunkFailures
	if !errors.As(err, &f) {
		return false
	}
	return f.outage || (f.chunks > 0 && f.transport == f.chunks)
}

// chunkBudget resolves a configured chunk size to the token budget the chunker
// accepts.
func chunkBudget(tokens int) int {
	if tokens <= 0 {
		return DefaultChunkSize
	}
	return tokens
}

// Extractor orchestrates LLM-based knowledge extraction from sessions (single-pass).
type Extractor struct {
	client    LLMClient
	store     *storage.Store
	model     string
	chunkSize int
}

// SetChunkSize sets the token budget per extraction chunk.
func (e *Extractor) SetChunkSize(tokens int) {
	e.chunkSize = tokens
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
	Facts             []learningItem `json:"facts"`
	Gotchas           []learningItem `json:"gotchas"`
	ExplicitTeachings []learningItem `json:"explicit_teachings"`
	Decisions         []learningItem `json:"decisions"`
	Patterns          []learningItem `json:"patterns"`
	UserPreferences   []learningItem `json:"user_preferences"`
	Unfinished        []learningItem `json:"unfinished"`
	Relationships     []learningItem `json:"relationships"`
	PivotMoments      []learningItem `json:"pivot_moments"`
	Syntheses         []learningItem `json:"syntheses"`
}

// allLearnings merges categorized fields into the flat learnings array,
// setting category from the field name if not already set.
func (r *extractionResult) allLearnings() []learningItem {
	all := append([]learningItem{}, r.Learnings...)
	for cat, items := range map[string][]learningItem{
		"fact":              r.Facts,
		"gotcha":            r.Gotchas,
		"explicit_teaching": r.ExplicitTeachings,
		"decision":          r.Decisions,
		"pattern":           r.Patterns,
		"user_preference":   r.UserPreferences,
		"unfinished":        r.Unfinished,
		"relationship":      r.Relationships,
		"pivot_moment":      r.PivotMoments,
		"synthesis":         r.Syntheses,
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
	chunks := ChunkMessages(msgs, chunkBudget(e.chunkSize))

	var allLearnings []models.Learning
	var failed, succeeded, transportFailed, parseFailed int
	var transportErr, parseErr error
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
			log.Printf("warn: extraction chunk %d/%d for %s: %v", chunk.Index+1, chunk.Total, sessionID, textutil.SingleLine(err.Error(), 300))
			failed++
			transportFailed++
			transportErr = err
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
			log.Printf("warn: parse extraction for %s chunk %d: %v", sessionID, chunk.Index+1, textutil.SingleLine(err.Error(), 300))
			failed++
			parseFailed++
			parseErr = err
			continue
		}

		// Set lineage — which messages this chunk covered
		for i := range learnings {
			learnings[i].SourceMsgFrom = chunk.FromMsgIdx
			learnings[i].SourceMsgTo = chunk.ToMsgIdx
		}
		succeeded++

		allLearnings = append(allLearnings, learnings...)
	}

	// A pass in which no chunk produced a result proves nothing about the
	// session. Reported as success, the caller marks the session as extracted
	// and certifies its message range, so it is never retried and whatever it
	// held is lost.
	//
	// A whitespace-only answer is counted as no result, not as a failure: it
	// cannot be told apart from a model that was reached and had nothing to
	// keep, and counting it would keep such a session pending forever. An answer
	// that arrives but does not parse IS a failure, above. Zero chunks is no
	// failure either: the pre-filter left nothing to extract, which is a
	// finished pass rather than a broken one.
	//
	// A partly failed pass is reported as well (ErrPartial). The learnings of the
	// answering chunks are real and are kept, but the failed chunks were never
	// read, so this pass cannot certify the session.
	if len(chunks) > 0 && failed > 0 {
		failure := &chunkFailures{chunks: len(chunks), succeeded: succeeded,
			transport: transportFailed, parse: parseFailed,
			transportErr: transportErr, parseErr: parseErr}
		switch {
		case failed == len(chunks):
			return nil, fmt.Errorf("extraction failed for all %d chunks of %s: %w", len(chunks), truncID(sessionID), failure)
		case succeeded > 0:
			return allLearnings, fmt.Errorf("partial extraction for %s: %w", truncID(sessionID), failure)
		default:
			return nil, fmt.Errorf("extraction produced no result for %s: %w", truncID(sessionID), failure)
		}
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
	learnings, extractErr := e.ExtractFromSession(sessionID, msgs)
	// A partial pass still carries the learnings of the chunks that answered.
	// They are stored, and the failure is returned afterwards so the caller
	// counts the attempt and retries the session (YM-362).
	partial := errors.Is(extractErr, ErrPartial)
	if extractErr != nil && !partial {
		return extractErr
	}

	// Filter junk, collect valid learnings for batch insert
	var valid []*models.Learning
	for i := range learnings {
		content := strings.TrimSpace(learnings[i].Content)
		if len(content) < 10 || content[0] == '{' || content[0] == '[' || strings.HasPrefix(content, "```") {
			continue
		}
		learnings[i].Project = project
		learnings[i].CanonicalProject = models.CanonicalProject(project)
		// Parse deadline trigger into ExpiresAt
		if expires := ParseDeadlineExpiry(learnings[i].TriggerRule); expires != nil {
			learnings[i].ExpiresAt = expires
		}
		valid = append(valid, &learnings[i])
	}

	// Pre-admission dedup: check each learning against existing corpus
	toInsert := ApplyPreAdmission(e.store, valid, sessionID)

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
	return extractErr
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
	return e.runEvolution(store, onSupersede, nil, 0)
}

// RunEvolutionForScope limits the evolution pass to project/category pairs touched by new extraction work.
// Cross-project evolution is still allowed, but only for categories seen in the scoped delta.
func (e *Extractor) RunEvolutionForScope(store *storage.Store, scope map[string]map[string]struct{}, onSupersede func(int64)) (int, int) {
	return e.RunEvolutionForScopeSince(store, scope, onSupersede, 0)
}

// RunEvolutionForScopeSince restricts rule-based pair checks to comparisons
// involving a learning inserted after afterID.
func (e *Extractor) RunEvolutionForScopeSince(store *storage.Store, scope map[string]map[string]struct{}, onSupersede func(int64), afterID int64) (int, int) {
	if len(scope) == 0 {
		return 0, 0
	}
	return e.runEvolution(store, onSupersede, scope, afterID)
}

// evolutionBulkMaxTokens is the output budget of one bulk evolution call. The call hands
// the model up to fifty learnings and expects a JSON verdict per candidate pair; the
// 8192-token default cut that off, and a truncated or empty answer aborted the run
// (2026-11-13, greenTile and ccm19_webpage). The model's own output limit is 65536.
const evolutionBulkMaxTokens = 65536

func (e *Extractor) runEvolution(store *storage.Store, onSupersede func(int64), scope map[string]map[string]struct{}, afterID int64) (int, int) {
	categories, err := store.GetActiveCategories()
	if err != nil {
		log.Printf("warn: get categories for evolution: %v", err)
		return 0, 0
	}
	// Exclude narrative — handled separately by SupersedeNarrativesBySession
	// Exclude capability — managed by save_capability auto-supersede, not LLM evolution
	var filtered []string
	for _, c := range categories {
		if c != "narrative" && c != "cap" {
			filtered = append(filtered, c)
		}
	}
	categories = filtered

	totalChecked := 0
	totalSuperseded := 0

	// Collect all active learnings, group by project+category
	type groupKey struct{ project, category string }
	groups := map[groupKey][]models.Learning{}
	groupIDs := map[groupKey]map[int64]bool{}
	scopedCategories := map[string]struct{}{}

	for _, cat := range categories {
		if scope == nil {
			learnings, err := store.GetActiveLearnings(cat, "", "", "", 0)
			if err != nil {
				continue
			}
			for _, l := range learnings {
				project := l.CanonicalProject
				if project == "" {
					project = l.Project
				}
				key := groupKey{project: project, category: cat}
				groups[key] = append(groups[key], l)
			}
			continue
		}

		for project, categories := range scope {
			if _, ok := categories[cat]; !ok {
				continue
			}
			learnings, err := store.GetActiveLearnings(cat, project, "", "", 0)
			if err != nil {
				continue
			}
			if len(learnings) > 0 {
				scopedCategories[cat] = struct{}{}
			}
			for _, l := range learnings {
				canonicalProject := l.CanonicalProject
				if canonicalProject == "" {
					canonicalProject = l.Project
				}
				key := groupKey{project: canonicalProject, category: cat}
				if groupIDs[key] == nil {
					groupIDs[key] = make(map[int64]bool)
				}
				if groupIDs[key][l.ID] {
					continue
				}
				groupIDs[key][l.ID] = true
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
		if afterID > 0 {
			dirty := false
			for _, learning := range learnings {
				if learning.ID > afterID {
					dirty = true
					break
				}
			}
			if !dirty {
				continue
			}
		}

		label := fmt.Sprintf("%s/%s", key.project, key.category)
		if key.project == "" {
			label = "(global)/" + key.category
		}

		// Cross-chunk rule-based dedup BEFORE chunking. Bigram sets are prepared
		// once and only exact sparse candidates involving the extraction delta run.
		preCleaned := make([]models.Learning, 0, len(learnings))
		byID := make(map[int64]models.Learning, len(learnings))
		preSuperseded := make(map[int64]int64)
		for _, l := range learnings {
			byID[l.ID] = l
			if IsSubstanzlos(l.Content) {
				if afterID == 0 || l.ID > afterID {
					preSuperseded[l.ID] = 0
				}
				continue
			}
			preCleaned = append(preCleaned, l)
		}
		bigramDupes, comparisons := findBigramDuplicates(preCleaned, 0.85, afterID)
		for loserID, winnerID := range bigramDupes {
			preSuperseded[loserID] = winnerID
		}
		if len(bigramDupes) > 0 {
			remaining := preCleaned[:0]
			for _, learning := range preCleaned {
				if _, loser := bigramDupes[learning.ID]; !loser {
					remaining = append(remaining, learning)
				}
			}
			preCleaned = remaining
		}
		if len(preSuperseded) > 0 {
			log.Printf("  Evolution: %s cross-chunk pre-dedup: %d superseded from %d candidates", label, len(preSuperseded), comparisons)
			for loserID, winnerID := range preSuperseded {
				if ok, err := supersedeUnlessHighTrust(store, byID[loserID], winnerID, "rule-based: cross-chunk near-duplicate", "relates_to"); err != nil {
					log.Printf("warn: %v", err)
				} else if ok {
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
		vectors, err := LoadVectorsForLearnings(store, embIDs)
		if err != nil {
			log.Printf("warn: load evolution vectors for %s: %v", label, err)
		}
		if err == nil && len(vectors) >= 2 {
			embDupes, stats := findEmbeddingDuplicatesSince(learnings, vectors, 0.92, afterID)
			if len(embDupes) > 0 {
				log.Printf("  Evolution: %s embedding pre-dedup: %d superseded from %d comparisons", label, len(embDupes), stats.Comparisons)
				for loserID, winnerID := range embDupes {
					if ok, err := supersedeUnlessHighTrust(store, byID[loserID], winnerID, "rule-based: embedding near-duplicate", "relates_to"); err != nil {
						log.Printf("warn: %v", err)
					} else if ok {
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

			// Defensive chunk-local pass for LLM inputs. The cross-chunk pass above
			// normally leaves no lexical duplicates here.
			cleaned := make([]models.Learning, 0, len(chunk))
			chunkByID := make(map[int64]models.Learning, len(chunk))
			autoSuperseded := make(map[int64]int64) // loserID -> winnerID (0 = no winner for substanzlos)
			for _, l := range chunk {
				chunkByID[l.ID] = l
				if IsSubstanzlos(l.Content) {
					if afterID == 0 || l.ID > afterID {
						autoSuperseded[l.ID] = 0
					}
					continue
				}
				cleaned = append(cleaned, l)
			}
			chunkDupes, _ := findBigramDuplicates(cleaned, 0.85, afterID)
			for loserID, winnerID := range chunkDupes {
				autoSuperseded[loserID] = winnerID
			}
			if len(chunkDupes) > 0 {
				remaining := cleaned[:0]
				for _, learning := range cleaned {
					if _, loser := chunkDupes[learning.ID]; !loser {
						remaining = append(remaining, learning)
					}
				}
				cleaned = remaining
			}

			for loserID, winnerID := range autoSuperseded {
				if ok, err := supersedeUnlessHighTrust(store, chunkByID[loserID], winnerID, "rule-based: substanzlos or near-duplicate", "relates_to"); err != nil {
					log.Printf("warn: %v", err)
				} else if ok {
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
				trustLevel := []string{"low", "medium", "high"}[storage.ClassifyTrust(storage.TrustScore(&l))]
				list.WriteString(fmt.Sprintf("#%d [trust=%s source=%s confidence=%.2f]: %s\n", l.ID, trustLevel, l.Source, l.Confidence, l.Content))
			}

			prompt := fmt.Sprintf("Project: %s, Category: %s (%d learnings)\n\n%s\nFind duplicates, near-duplicates, and contradictions. Apply the quality ranking from the system prompt.",
				key.project, key.category, len(chunk), list.String())

			response, err := e.client.CompleteJSON(BulkEvolutionSystemPrompt, prompt, EvolutionSchema(), WithMaxTokens(evolutionBulkMaxTokens))
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
		all, err := store.GetActiveLearnings(cat, "", "", "", 0)
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
	if err := json.Unmarshal([]byte(extractJSON(response)), &evoResp); err != nil {
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
			candidates := make([]models.Learning, 0, len(action.SupersedesIDs))
			seen := make(map[int64]bool, len(action.SupersedesIDs))
			for _, id := range action.SupersedesIDs {
				if seen[id] {
					continue
				}
				learning, err := store.GetLearning(id)
				if err != nil {
					log.Printf("  warn: get learning #%d for quality ranking: %v", id, err)
					continue
				}
				seen[id] = true
				candidates = append(candidates, *learning)
			}
			if len(candidates) < 2 {
				continue
			}
			superseded += applyQualityRankedSupersede(store, candidates, action.Reason, onSupersede)

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
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		} else {
			s = s[3:]
		}
		s = strings.TrimSpace(s)
		if strings.HasSuffix(s, "```") {
			s = strings.TrimSpace(s[:len(s)-3])
		}
	}
	return s
}

// extractJSON extracts a JSON array or object from mixed text.
// Handles code fences, natural language wrapping, and trailing text.
func extractJSON(s string) string {
	s = stripMarkdownFence(s)

	// Try to find JSON object {...} first — most extraction responses are objects.
	if start := strings.IndexByte(s, '{'); start >= 0 {
		end := findMatchingBracket(s, start)
		if end > start {
			return strings.TrimSpace(s[start : end+1])
		}
	}

	// Try to find JSON array [...]
	if start := strings.IndexByte(s, '['); start >= 0 {
		end := findMatchingBracket(s, start)
		if end > start {
			return strings.TrimSpace(s[start : end+1])
		}
	}

	return s
}

func findMatchingBracket(s string, start int) int {
	open := s[start]
	closeB := byte('}')
	if open == '[' {
		closeB = ']'
	}

	depth := 0
	for i := start; i < len(s); i++ {
		if s[i] == open {
			depth++
		} else if s[i] == closeB {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseExtractionResponse(response, sessionID, model string) ([]models.Learning, error) {
	jsonStr := extractJSON(response)

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
	flavor := result.SessionFlavor

	domain := result.Domain
	if domain == "" {
		domain = "general"
	}

	var learnings []models.Learning

	// Category mapping from schema enum to internal names
	categoryMap := map[string]string{
		"fact":              "fact",
		"explicit_teaching": "explicit_teaching",
		"gotcha":            "gotcha",
		"decision":          "decision",
		"pattern":           "pattern",
		"user_preference":   "preference",
		"unfinished":        "unfinished",
		"relationship":      "relationship",
		"pivot_moment":      "pivot_moment",
		"synthesis":         "synthesis",
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
			OriginTool:         "llm_extracted_session",
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
			CanonicalProject:   models.CanonicalProject(project),
			Confidence:         1.0,
			CreatedAt:          ts,
			ModelUsed:          model,
			Source:             "llm_extracted",
			OriginTool:         "llm_extracted_session",
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

// TwoPassExtractor uses two LLM passes: summarize (Haiku) then extract (Sonnet).
type TwoPassExtractor struct {
	summarizeClient LLMClient // Pass 1: chunk → summary (Haiku)
	extractClient   LLMClient // Pass 2: summaries → learnings (Sonnet)
	store           *storage.Store
	chunkSize       int
}

// SetChunkSize sets the token budget per extraction chunk.
func (e *TwoPassExtractor) SetChunkSize(tokens int) {
	e.chunkSize = tokens
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
	chunks := ChunkMessages(filtered, chunkBudget(e.chunkSize))

	// Pass 1: Summarize each chunk with Haiku
	var summaries []string
	var failed int
	var lastErr error
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
			log.Printf("warn: summarize chunk %d/%d for %s: %v", chunk.Index+1, chunk.Total, sessionID, textutil.SingleLine(err.Error(), 300))
			failed++
			lastErr = err
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
		// Same rule as the single-pass extractor: no summary from any chunk
		// means the pass proved nothing, so pass 2 cannot certify the session.
		// Nothing was produced here, so this is not a partial success even when
		// only some chunks failed and the rest answered with nothing.
		if len(chunks) > 0 && failed > 0 {
			failure := &chunkFailures{chunks: len(chunks), transport: failed, transportErr: lastErr}
			if failed == len(chunks) {
				return nil, fmt.Errorf("summarize failed for all %d chunks of %s: %w", len(chunks), truncID(sessionID), failure)
			}
			return nil, fmt.Errorf("no summary for %s: %w", truncID(sessionID), failure)
		}
		return nil, nil
	}

	// Pass 2: Extract from combined summaries with Sonnet
	combined := strings.Join(summaries, "\n\n")

	// Pass 2 is one more step in the denominator. Pass 1 already answered, so
	// none of its failures below is an outage: the transport verdict needs every
	// call to have failed, and one of them succeeded.
	pass2Chunks := len(chunks) + 1

	response, err := e.extractClient.CompleteJSON(BuildExtractionSystemPrompt(), combined, ExtractionSchema())
	if err != nil {
		// The extraction call itself failed, so nothing was produced. Unlike a
		// failed chunk, this is the decisive call of the pass, so it counts as an
		// outage (the behavior before YM-362).
		return nil, fmt.Errorf("extraction pass 2: %w",
			&chunkFailures{chunks: pass2Chunks, transport: failed + 1, outage: true, transportErr: err})
	}

	if strings.TrimSpace(response) == "" {
		if failed > 0 {
			return nil, fmt.Errorf("no learnings from pass 2 for %s: %w", truncID(sessionID),
				&chunkFailures{chunks: pass2Chunks, transport: failed, transportErr: lastErr})
		}
		return nil, nil
	}

	learnings, err := parseExtractionResponse(response, sessionID, e.extractClient.Model())
	if err != nil {
		// Pass 1 already proved the API answers, so this is a parse failure, not
		// an outage (see ErrParse). Nothing was extracted, so it is no partial
		// success either.
		return learnings, fmt.Errorf("extraction pass 2 unreadable for %s: %w", truncID(sessionID),
			&chunkFailures{chunks: pass2Chunks, transport: failed, parse: 1,
				transportErr: lastErr, parseErr: err})
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

	if failed > 0 {
		// The failed chunks are missing from the combined text, so pass 2 never
		// saw them. The learnings stay, the pass is not a certification.
		return learnings, fmt.Errorf("partial summarize for %s: %w", truncID(sessionID),
			&chunkFailures{chunks: pass2Chunks, succeeded: 1, transport: failed, transportErr: lastErr})
	}

	return learnings, nil
}

// ExtractAndStore extracts and stores learnings (same interface as single-pass Extractor).
// Note: autoResolve is intentionally ignored — two-pass mode relies on bulk evolution
// (Phase 3) rather than inline conflict resolution, since the extract client (Sonnet)
// is different from the evolution client (Haiku).
func (e *TwoPassExtractor) ExtractAndStore(sessionID, project string, msgs []models.Message, autoResolve bool) error {
	learnings, extractErr := e.ExtractFromSession(sessionID, msgs)
	// Same contract as the single-pass extractor: the learnings of a partial
	// pass are stored, the failure is returned afterwards (YM-362).
	partial := errors.Is(extractErr, ErrPartial)
	if extractErr != nil && !partial {
		return extractErr
	}

	// Filter junk, collect valid learnings for batch insert
	var valid []*models.Learning
	for i := range learnings {
		content := strings.TrimSpace(learnings[i].Content)
		if len(content) < 10 || content[0] == '{' || content[0] == '[' || strings.HasPrefix(content, "```") {
			continue
		}
		learnings[i].Project = project
		learnings[i].CanonicalProject = models.CanonicalProject(project)
		// Parse deadline trigger into ExpiresAt
		if expires := ParseDeadlineExpiry(learnings[i].TriggerRule); expires != nil {
			learnings[i].ExpiresAt = expires
		}
		valid = append(valid, &learnings[i])
	}

	// Pre-admission dedup — same contract as single-pass ExtractAndStore
	toInsert := ApplyPreAdmission(e.store, valid, sessionID)

	// Single transaction for all learnings from this session
	ids, err := e.store.InsertLearningBatch(toInsert)
	if err != nil {
		log.Printf("warn: batch store learnings: %v", err)
	}

	log.Printf("Extracted %d learnings from session %s (two-pass)", len(ids), truncID(sessionID))
	return extractErr
}
