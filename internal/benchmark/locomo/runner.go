
package locomo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/carsteneu/yesmem/internal/benchmark"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Runner orchestrates the full LoCoMo benchmark pipeline: Ingest -> Extract -> Embed -> Query -> Judge -> Report.
type Runner struct {
	store     *storage.Store
	answerLLM benchmark.LLMClient
	judgeLLM  benchmark.LLMClient
	extractor extraction.SessionExtractor // optional, for extraction step
	indexer   *embedding.Indexer           // optional, for embedding after extraction
	GoldMode    bool // use gold observations instead of extraction
	GenAQTarget int  // generate aq's up to this count per learning (0 = skip)
	GenAQLLM    benchmark.LLMClient // LLM for AQ generation (defaults to answerLLM)
	DumpResults string // path to dump per-question JSON results (empty = skip)
}

// NewRunner creates a Runner with the given store and LLM clients.
func NewRunner(store *storage.Store, answerLLM, judgeLLM benchmark.LLMClient) *Runner {
	return &Runner{
		store:     store,
		answerLLM: answerLLM,
		judgeLLM:  judgeLLM,
	}
}

// SetExtractor sets an optional extractor for the extraction step.
func (r *Runner) SetExtractor(ext extraction.SessionExtractor) {
	r.extractor = ext
}

// SetIndexer sets an optional embedding indexer for post-extraction embedding.
func (r *Runner) SetIndexer(idx *embedding.Indexer) {
	r.indexer = idx
}

// Ingest delegates to IngestAll, ingesting all samples into the store.
func (r *Runner) Ingest(samples []Sample) ([]IngestStats, error) {
	return IngestAll(r.store, samples)
}

// Extract iterates over projects, loads sessions and messages, and calls extractor.ExtractAndStore.
// Uses concurrent workers for parallel extraction.
func (r *Runner) Extract(samples []Sample, concurrency int) error {
	if r.extractor == nil {
		return fmt.Errorf("no extractor configured — call SetExtractor first or use skipExtract=true")
	}
	if concurrency < 1 {
		concurrency = 3
	}

	type extractJob struct {
		sessionID string
		project   string
		msgs      []models.Message
	}

	var jobs []extractJob
	for _, sample := range samples {
		project := fmt.Sprintf("locomo_%s", sample.ID)
		sessions, err := r.store.ListAllSessions(project, 100)
		if err != nil {
			return fmt.Errorf("list sessions for %s: %w", project, err)
		}
		for _, sess := range sessions {
			msgs, err := r.store.GetMessagesBySession(sess.ID)
			if err != nil {
				return fmt.Errorf("get messages for %s: %w", sess.ID, err)
			}
			if len(msgs) == 0 {
				continue
			}
			jobs = append(jobs, extractJob{sessionID: sess.ID, project: project, msgs: msgs})
		}
	}

	log.Printf("  Extracting %d sessions with %d workers...", len(jobs), concurrency)

	var wg sync.WaitGroup
	jobCh := make(chan extractJob, len(jobs))
	var extracted int64

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if err := r.extractor.ExtractAndStore(job.sessionID, job.project, job.msgs, false); err != nil {
					log.Printf("warn: extract session %s: %v", job.sessionID, err)
				}
				n := atomic.AddInt64(&extracted, 1)
				if n%10 == 0 {
					log.Printf("  Extracted %d/%d sessions", n, len(jobs))
				}
			}
		}()
	}

	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()
	log.Printf("  Extracted %d sessions total", len(jobs))

	return nil
}

// GenerateAQ generates anticipated_queries for learnings that have fewer than targetCount.
// Uses the answer LLM to produce search phrases for each learning's content.
func (r *Runner) GenerateAQ(samples []Sample, targetCount int) (int, error) {
	if r.answerLLM == nil {
		return 0, fmt.Errorf("no answer LLM configured")
	}
	db := r.store.DB()
	total := 0

	allLearnings, err := r.store.GetAllLearningsForEmbedding()
	if err != nil {
		return 0, fmt.Errorf("get learnings: %w", err)
	}

	for _, l := range allLearnings {
		// Count existing aq's
		var existing int
		db.QueryRow("SELECT COUNT(*) FROM learning_anticipated_queries WHERE learning_id = ?", l.ID).Scan(&existing)
		if existing >= targetCount {
			continue
		}
		need := targetCount - existing

		prompt := fmt.Sprintf("Generate exactly %d search queries that someone would use to find this information. Return ONLY the queries, one per line, no numbering, no explanation.\n\nFact: %s", need, l.Content)
		llm := r.answerLLM
		if r.GenAQLLM != nil {
			llm = r.GenAQLLM
		}
		resp, err := llm.Complete("You generate search queries for a memory retrieval system.", prompt)
		if err != nil {
			log.Printf("  [aq-gen] skip learning %d: %v", l.ID, err)
			continue
		}

		lines := strings.Split(strings.TrimSpace(resp), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || len(line) < 5 {
				continue
			}
			db.Exec("INSERT INTO learning_anticipated_queries (learning_id, value) VALUES (?, ?)", l.ID, line)
			total++
		}
	}
	return total, nil
}

// dumpPerQuestionResults writes per-question JSON for failure analysis.
func dumpPerQuestionResults(path string, results []JudgeResult) {
	type entry struct {
		Category  int           `json:"category"`
		CatName   string        `json:"cat_name"`
		Question  string        `json:"question"`
		Gold      string        `json:"gold"`
		Generated string        `json:"generated"`
		Correct   bool          `json:"correct"`
		ToolCalls []ToolCallLog `json:"tool_calls,omitempty"`
	}
	var entries []entry
	for _, r := range results {
		entries = append(entries, entry{
			Category:  r.Category,
			CatName:   CategoryName(r.Category),
			Question:  r.Question,
			Gold:      r.Gold,
			Generated: r.Generated,
			Correct:   r.Score == 1,
			ToolCalls: r.ToolCalls,
		})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.Printf("  [warn] dump results failed: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("  [warn] write dump failed: %v", err)
		return
	}
	log.Printf("  Dumped %d results to %s", len(entries), path)
}

// dumpQueryResults writes intermediate answers (before judging) as crash recovery.
func dumpQueryResults(path string, results []QueryResult) {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		log.Printf("  [warn] dump answers failed: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("  [warn] write answers failed: %v", err)
		return
	}
	log.Printf("  Saved %d answers to %s (intermediate)", len(results), path)
}

// CountTotalQuestions returns the total number of scored QA pairs across all samples.
func CountTotalQuestions(samples []Sample) int {
	total := 0
	for _, s := range samples {
		total += len(s.ScoredQA())
	}
	return total
}

// EmbedLearnings embeds all learnings for benchmark samples into the vector store.
// Embeds content as primary vector + each anticipated_query as separate vector for precise retrieval.
func (r *Runner) EmbedLearnings(samples []Sample) (int, error) {
	if r.indexer == nil {
		return 0, fmt.Errorf("no indexer configured — call SetIndexer first")
	}
	ctx := context.Background()
	var total int
	allLearnings, err := r.store.GetAllLearningsForEmbedding()
	if err != nil {
		return 0, fmt.Errorf("get learnings: %w", err)
	}
	// Group by project
	byProject := make(map[string][]models.Learning)
	for _, l := range allLearnings {
		byProject[l.Project] = append(byProject[l.Project], l)
	}
	for _, sample := range samples {
		project := fmt.Sprintf("locomo_%s", sample.ID)
		learnings := byProject[project]
		if len(learnings) == 0 {
			continue
		}
		var items []embedding.IndexItem
		for _, l := range learnings {
			idStr := fmt.Sprintf("%d", l.ID)
			// Primary: embed the learning content itself
			items = append(items, embedding.IndexItem{
				ID:      idStr,
				Content: l.Content,
				Metadata: map[string]string{"project": l.Project, "type": "content"},
			})
			// Load junction data for anticipated_queries — ID is already int64
			full, jErr := r.store.GetLearning(l.ID)
			if jErr != nil {
				continue
			}
			r.store.LoadJunctionData(full)
			// Each anticipated_query as separate vector → precise query-to-query matching
			for i, aq := range full.AnticipatedQueries {
				items = append(items, embedding.IndexItem{
					ID:      fmt.Sprintf("%d_aq_%d", l.ID, i),
					Content: aq,
					Metadata: map[string]string{"project": l.Project, "type": "anticipated_query", "learning_id": idStr, "learning_content": l.Content},
				})
			}
		}
		if len(items) > 0 {
			if err := r.indexer.IndexBatch(ctx, items); err != nil {
				log.Printf("  [embed] %s batch error: %v", project, err)
			} else {
				total += len(items)
			}
		}
	}
	return total, nil
}

// Query delegates to RunQueriesConcurrent for each sample and collects all results.
func (r *Runner) Query(samples []Sample, cfg QueryConfig) ([]QueryResult, error) {
	total := CountTotalQuestions(samples)

	// Full-context: batch all questions per sample in ONE call
	if cfg.FullContext {
		var all []QueryResult
		for i, sample := range samples {
			project := fmt.Sprintf("locomo_%s", sample.ID)
			scored := sample.ScoredQA()
			questions := make([]string, len(scored))
			for j, qa := range scored {
				questions[j] = qa.Question
			}
			log.Printf("  [query] sample %d/%d (%s): %d questions in 1 batch call...", i+1, len(samples), sample.ID, len(questions))
			answers, err := FullContextAnswerBatch(r.store, r.answerLLM, project, questions)
			if err != nil {
				if strings.Contains(err.Error(), "prompt is too long") {
					log.Printf("  [skip] sample %s: exceeds context window, skipping", sample.ID)
					continue
				}
				return nil, fmt.Errorf("batch query sample %s: %w", sample.ID, err)
			}
			logUsage("batch", r.answerLLM)
			for _, qa := range scored {
				all = append(all, QueryResult{
					SampleID:  sample.ID,
					Question:  qa.Question,
					Gold:      string(qa.Answer),
					Generated: answers[qa.Question],
					Category:  qa.Category,
				})
			}
		}
		return all, nil
	}

	// Normal path: per-question search + answer
	var progress int64
	var all []QueryResult
	for _, sample := range samples {
		results, err := RunQueriesConcurrent(r.store, r.answerLLM, sample, cfg, total, &progress)
		if err != nil {
			return nil, fmt.Errorf("query sample %s: %w", sample.ID, err)
		}
		all = append(all, results...)
	}
	return all, nil
}

// Judge delegates to JudgeAll using the judge LLM (sequential, for backward compatibility).
func (r *Runner) Judge(queries []QueryResult) ([]JudgeResult, error) {
	return JudgeAll(r.judgeLLM, queries)
}

// logUsage logs the current cost of an LLM tracker if available.
func logUsage(label string, llm benchmark.LLMClient) {
	if a, ok := llm.(*benchmark.AnthropicAdapter); ok && a.Tracker != nil {
		log.Printf("  [cost] %s: %s", label, a.Tracker.String())
	}
}

// PrintTotalCost prints the final cost summary for both LLMs.
func PrintTotalCost(answerLLM, judgeLLM benchmark.LLMClient) {
	log.Printf("=== Cost Summary ===")
	logUsage("Answer LLM", answerLLM)
	logUsage("Judge LLM", judgeLLM)
	var total float64
	if a, ok := answerLLM.(*benchmark.AnthropicAdapter); ok && a.Tracker != nil {
		total += a.Tracker.Cost()
	}
	if a, ok := judgeLLM.(*benchmark.AnthropicAdapter); ok && a.Tracker != nil {
		total += a.Tracker.Cost()
	}
	log.Printf("  [cost] TOTAL: $%.2f", total)
}
func logIngestStats(stats []IngestStats) {
	totalSessions := 0
	totalMsgs := 0
	for _, s := range stats {
		totalSessions += s.Sessions
		totalMsgs += s.Messages
	}
	log.Printf("  Ingested %d sessions, %d messages", totalSessions, totalMsgs)
}

// RunFull orchestrates all 5 steps: Ingest -> Extract -> Query -> Judge -> Report.
// If skipExtract is true, extraction is skipped and cfg.UseMessages is set as fallback.
func (r *Runner) RunFull(samples []Sample, cfg QueryConfig, skipExtract bool) (Report, error) {
	return r.runFullInternal(samples, cfg, skipExtract, true)
}

// RunMultiple runs RunFull N times and computes statistics across runs.
// On the first run, data is ingested and optionally extracted.
// Subsequent runs skip ingest (upsert handles duplicates) and always skip extraction.
func (r *Runner) RunMultiple(samples []Sample, cfg QueryConfig, skipExtract bool, runs int) (RunStats, error) {
	scores := make([]float64, 0, runs)

	for i := 0; i < runs; i++ {
		log.Printf("\n=== Run %d/%d ===", i+1, runs)

		runCfg := cfg // copy per run

		// Skip extract on subsequent runs OR if flag is set
		runSkipExtract := skipExtract || i > 0

		report, err := r.runFullInternal(samples, runCfg, runSkipExtract, i == 0)
		if err != nil {
			return RunStats{}, fmt.Errorf("run %d: %w", i+1, err)
		}

		scores = append(scores, report.OverallMean)
		log.Printf("  Run %d score: %.4f", i+1, report.OverallMean)
	}

	return ComputeRunStats(scores), nil
}

// runFullInternal is the internal version of RunFull that controls whether ingest runs.
func (r *Runner) runFullInternal(samples []Sample, cfg QueryConfig, skipExtract, doIngest bool) (Report, error) {
	// Step 1: Ingest (only on first run)
	if doIngest {
		log.Printf("Step 1/5: Ingesting %d samples...", len(samples))
		stats, err := r.Ingest(samples)
		if err != nil {
			return Report{}, fmt.Errorf("ingest: %w", err)
		}
		logIngestStats(stats)
	} else {
		log.Printf("Step 1/5: Ingest skipped (data already loaded)")
	}

	// Step 2: Extract or Gold-Ingest
	if r.GoldMode {
		log.Printf("Step 2/5: Gold mode — ingesting observations as learnings...")
		goldCount, goldErr := IngestGoldObservations(r.store, samples)
		if goldErr != nil {
			return Report{}, fmt.Errorf("gold ingest: %w", goldErr)
		}
		log.Printf("  Ingested %d gold observations", goldCount)
	} else if skipExtract {
		log.Printf("Step 2/5: Extraction skipped — using messages as fallback")
		cfg.UseMessages = true
	} else {
		log.Printf("Step 2/5: Extracting learnings...")
		if err := r.Extract(samples, cfg.Concurrency); err != nil {
			return Report{}, fmt.Errorf("extract: %w", err)
		}
	}

	// Step 2b: FTS enrichment DISABLED — proven to hurt BM25 precision [ID:43589]
	// RebuildFTSEnriched adds keywords/queries to FTS content, which dilutes scores.
	// Content-only FTS + vector aq-search is the better architecture.
	log.Printf("Step 2b: FTS enrichment skipped (content-only FTS)")

	// Step 2b2: Generate anticipated_queries (if requested)
	if r.GenAQTarget > 0 {
		log.Printf("Step 2b2: Generating anticipated_queries (target=%d per learning)...", r.GenAQTarget)
		aqCount, aqErr := r.GenerateAQ(samples, r.GenAQTarget)
		if aqErr != nil {
			log.Printf("  [warn] AQ generation failed: %v", aqErr)
		} else {
			log.Printf("  Generated %d new anticipated_queries", aqCount)
		}
	}

	// Step 2c: Embed learnings (always — VectorStore is in-memory, needs rebuild each run)
	if r.indexer != nil {
		log.Printf("Step 2c: Embedding learnings for vector search...")
		count, err := r.EmbedLearnings(samples)
		if err != nil {
			log.Printf("  [warn] embedding failed: %v (continuing with BM25-only)", err)
		} else {
			log.Printf("  Embedded %d learnings", count)
		}
	}

	// Step 3: Query
	totalQ := CountTotalQuestions(samples)
	log.Printf("Step 3/5: Running %d queries (concurrency=%d)...", totalQ, cfg.Concurrency)
	queryResults, err := r.Query(samples, cfg)
	if err != nil {
		return Report{}, fmt.Errorf("query: %w", err)
	}
	log.Printf("  Generated %d answers", len(queryResults))

	// Dump answers immediately (before judging) so they survive judge crashes
	answersPath := ""
	if r.DumpResults != "" {
		answersPath = r.DumpResults + ".answers"
		dumpQueryResults(answersPath, queryResults)
	}

	// Step 4: Judge
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	log.Printf("Step 4/5: Judging %d answers (concurrency=%d)...", len(queryResults), concurrency)
	judgeResults, err := JudgeAllConcurrent(r.judgeLLM, queryResults, concurrency)
	if err != nil {
		return Report{}, fmt.Errorf("judge: %w", err)
	}
	log.Printf("  Judged %d answers", len(judgeResults))

	// Dump per-question results if requested
	if r.DumpResults != "" {
		dumpPerQuestionResults(r.DumpResults, judgeResults)
	}
	// Clean up intermediate answers file
	if answersPath != "" {
		os.Remove(answersPath)
	}

	// Step 5: Aggregate
	log.Printf("Step 5/5: Aggregating results...")
	report := Aggregate(judgeResults)

	return report, nil
}
