package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// embedJob represents a learning to be embedded asynchronously.
type embedJob struct {
	id       int64
	content  string
	category string
	project  string
}

// VectorStore returns the handler's vector store (may be nil if not configured).
func (h *Handler) VectorStore() *embedding.VectorStore {
	return h.vectorStore
}

// SetEmbedding configures the handler with vector search capabilities.
// If not called, hybrid_search falls back to BM25-only.
func (h *Handler) SetEmbedding(indexer *embedding.Indexer, store *embedding.VectorStore, provider embedding.Provider) {
	h.indexer = indexer
	h.vectorStore = store
	h.embedProvider = provider
}

// SetSearchEmbeddingProvider configures a dedicated provider for query embeddings.
func (h *Handler) SetSearchEmbeddingProvider(provider embedding.Provider) {
	h.searchEmbedProvider = provider
}

// SaveIVF persists the IVF index to disk if configured.
func (h *Handler) SaveIVF() {
	if h.vectorStore == nil || h.ivfPath == "" {
		return
	}
	if err := h.vectorStore.SaveIVF(h.ivfPath); err != nil {
		log.Printf("SaveIVF: %v", err)
	}
}

// SetEmbedProcess stores the embed-learnings subprocess for lifecycle management.
func (h *Handler) SetEmbedProcess(p *os.Process) {
	h.embedProcessMu.Lock()
	h.embedProcess = p
	h.embedProcessMu.Unlock()
}

// StopEmbedProcess kills the embed-learnings subprocess if running.
// Called during daemon shutdown. Per-batch commit ensures no progress is lost.
func (h *Handler) StopEmbedProcess() {
	h.embedProcessMu.Lock()
	p := h.embedProcess
	h.embedProcess = nil
	h.embedProcessMu.Unlock()
	if p != nil {
		log.Printf("Stopping embed-learnings subprocess (PID %d)", p.Pid)
		p.Kill()
	}
}

// EmbedProvider returns the handler's embedding provider (may be nil).
func (h *Handler) EmbedProvider() embedding.Provider {
	return h.embedProvider
}

func (h *Handler) queryEmbedProvider() embedding.Provider {
	if h.searchEmbedProvider != nil {
		return h.searchEmbedProvider
	}
	return h.embedProvider
}

// StartEmbedWorker starts a background goroutine that processes embed jobs.
// Call this after SetEmbedding. The worker stops when ctx is cancelled.
func (h *Handler) StartEmbedWorker(ctx context.Context) {
	if h.indexer == nil {
		return
	}
	h.embedQueue = make(chan embedJob, 100)
	go func() {
		for {
			select {
			case <-ctx.Done():
				// Drain remaining jobs before exit
				for {
					select {
					case job := <-h.embedQueue:
						h.processEmbedJob(context.Background(), job)
					default:
						return
					}
				}
			case job := <-h.embedQueue:
				h.processEmbedJob(ctx, job)
			}
		}
	}()
}

func (h *Handler) processEmbedJob(ctx context.Context, job embedJob) {
	err := h.indexer.Index(ctx, fmt.Sprintf("%d", job.id), job.content, map[string]string{
		"category": job.category,
		"project":  job.project,
	})
	if err != nil {
		log.Printf("auto-embed learning #%d: %v", job.id, err)
		return
	}
	if h.store != nil {
		if err := h.store.MarkEmbeddingsDone([]int64{job.id}); err != nil {
			log.Printf("auto-embed mark done #%d: %v", job.id, err)
		}
	}
	runtime.Gosched() // yield after inference so other goroutines can run
}

// RemoveEmbedding removes a learning's embedding from the vector store.
// Safe to call even when embedding is not configured (no-op).
func (h *Handler) RemoveEmbedding(id int64) {
	if h.vectorStore == nil {
		return
	}
	ctx := context.Background()
	if err := h.vectorStore.Delete(ctx, fmt.Sprintf("%d", id)); err != nil {
		log.Printf("remove embedding #%d: %v", id, err)
	}
}

// EmbedLearning queues a learning for async embedding.
// Safe to call even when embedding is not configured (no-op).
func (h *Handler) EmbedLearning(id int64, content, category, project string) {
	if h.embedQueue == nil {
		return
	}
	select {
	case h.embedQueue <- embedJob{id: id, content: content, category: category, project: project}:
	default:
		log.Printf("auto-embed: queue full, dropping learning #%d", id)
	}
}

func (h *Handler) handleHybridSearch(params map[string]any) Response {
	t0 := time.Now()
	query, _ := params["query"].(string)
	project, _ := params["project"].(string)
	since, _ := params["since"].(string)
	before, _ := params["before"].(string)
	limit := intOr(params, "limit", 10)

	if query == "" {
		return errorResponse("query is required")
	}

	const searchTimeout = 5 * time.Second

	// Dynamic lane limit: scale with corpus size so decay-reranking has enough candidates
	laneLimit := limit
	if h.store != nil {
		var corpusSize int
		h.store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by IS NULL`).Scan(&corpusSize)
		if dl := corpusSize / 10; dl > laneLimit {
			laneLimit = dl
		}
		if laneLimit > 1000 {
			laneLimit = 1000
		}
	}

	// Run BM25 and Vector search in parallel with individual timeouts
	type bm25Result struct {
		results []embedding.RankedResult
		err     error
	}
	type vectorResult struct {
		results  []embedding.RankedResult
		queryVec []float32
		err      error
	}

	bm25Ch := make(chan bm25Result, 1)
	vectorCh := make(chan vectorResult, 1)
	aqCh := make(chan bm25Result, 1)

	// BM25 goroutine
	go func() {
		if h.store == nil {
			bm25Ch <- bm25Result{}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		results, err := h.store.SearchLearningsBM25Ctx(ctx, query, project, since, before, laneLimit)
		if err != nil {
			bm25Ch <- bm25Result{err: err}
			return
		}
		var ranked []embedding.RankedResult
		for _, r := range results {
			ranked = append(ranked, embedding.RankedResult{
				ID: r.ID, Content: r.Content, Score: r.Score,
				OriginalScore: r.Score, // negated BM25 score (higher=better)
				Project: r.Project,
			})
		}
		bm25Ch <- bm25Result{results: ranked}
	}()

	// Vector goroutine
	go func() {
		queryProvider := h.queryEmbedProvider()
		if queryProvider == nil || !queryProvider.Enabled() || h.vectorStore == nil {
			vectorCh <- vectorResult{}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		vectors, err := queryProvider.Embed(ctx, []string{query})
		if err != nil {
			vectorCh <- vectorResult{err: err}
			return
		}
		if len(vectors) == 0 {
			vectorCh <- vectorResult{}
			return
		}
		searchResults, err := h.vectorStore.SearchWithProject(ctx, vectors[0], laneLimit, project)
		if err != nil {
			vectorCh <- vectorResult{err: err}
			return
		}
		var ranked []embedding.RankedResult
		for _, r := range searchResults {
			ranked = append(ranked, embedding.RankedResult{
				ID: r.ID, Content: r.Content,
				Score:         float64(r.Similarity),
				OriginalScore: float64(r.Similarity),
			})
		}
		vectorCh <- vectorResult{results: ranked, queryVec: vectors[0]}
	}()

	// AQ-FTS goroutine (porter stemming — catches synonyms like dog/puppy)
	go func() {
		if h.store == nil {
			aqCh <- bm25Result{}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		_ = ctx // SearchAnticipatedQueries doesn't take context yet — timeout via store's busy_timeout
		results, err := h.store.SearchAnticipatedQueries(query, project, limit)
		if err != nil {
			aqCh <- bm25Result{err: err}
			return
		}
		var ranked []embedding.RankedResult
		for _, r := range results {
			ranked = append(ranked, embedding.RankedResult{
				ID: r.ID, Content: r.Content, Score: r.Score,
				OriginalScore: r.Score, Project: r.Project,
			})
		}
		aqCh <- bm25Result{results: ranked}
	}()

	// Collect results
	bm25Res := <-bm25Ch
	vectorRes := <-vectorCh
	aqRes := <-aqCh

	bm25Results := bm25Res.results
	vectorResults := vectorRes.results
	aqResults := aqRes.results

	// Merge AQ-FTS results into BM25 (dedup by ID, boost existing entries)
	if len(aqResults) > 0 {
		bm25IDs := make(map[string]int, len(bm25Results))
		for i, r := range bm25Results {
			bm25IDs[r.ID] = i
		}
		for _, aq := range aqResults {
			if _, exists := bm25IDs[aq.ID]; !exists {
				bm25Results = append(bm25Results, aq)
			}
		}
	}

	if bm25Res.err != nil {
		log.Printf("hybrid_search: bm25 failed in %v: %v", time.Since(t0), bm25Res.err)
	}
	if vectorRes.err != nil {
		log.Printf("hybrid_search: vector failed in %v: %v", time.Since(t0), vectorRes.err)
	}
	if aqRes.err != nil {
		log.Printf("hybrid_search: aq-fts failed: %v", aqRes.err)
	}
	tSearch := time.Since(t0)

	// Post-filter vector results by time range (vector store has no native temporal filtering)
	if len(vectorResults) > 0 && (since != "" || before != "") {
		filtered := h.filterResultsByTime(vectorResults, since, before)
		log.Printf("hybrid_search: vector time-filtered %d → %d", len(vectorResults), len(filtered))
		vectorResults = filtered
	}

	// If both paths failed, return error
	if len(bm25Results) == 0 && len(vectorResults) == 0 && (bm25Res.err != nil || vectorRes.err != nil) {
		return errorResponse(fmt.Sprintf("search failed: bm25=%v, vector=%v", bm25Res.err, vectorRes.err))
	}

	// Determine fusion method
	fusionMethod := "rrf"
	if len(vectorResults) == 0 {
		fusionMethod = "bm25_only"
	}

	// Merge via RRF or normalize BM25-only scores
	if fusionMethod == "bm25_only" {
		for i := range bm25Results {
			bm25Results[i].Source = "keyword"
		}
		if len(bm25Results) > limit {
			bm25Results = bm25Results[:limit]
		}
		// Scores already pre-normalized by tier in SearchLearningsBM25Ctx (0-100).
		// Just assign source and cap to limit.
		if len(bm25Results) > limit {
			bm25Results = bm25Results[:limit]
		}
	}

	var merged []embedding.RankedResult
	if fusionMethod == "rrf" {
		merged = embedding.RRFMerge(bm25Results, vectorResults, 60, limit)
	} else {
		merged = bm25Results
	}

	// Resolve superseded learnings: redirect to active successor
	tMerge := time.Since(t0)
	merged = h.resolveSupersededResults(merged)
	tSupersede := time.Since(t0)

	// Filter/down-rank by category — consistent with other search functions.
	// Narratives are session-level briefing context, not inline search results.
	if len(merged) > 0 {
		ids := make([]string, len(merged))
		for i, r := range merged {
			ids[i] = r.ID
		}
		if cats, err := h.store.GetCategoriesBulk(ids); err == nil {
			filtered := merged[:0]
			for _, r := range merged {
				switch cats[r.ID] {
				case "narrative":
					continue // excluded from search, belongs in briefing only
				case "unfinished":
					r.Score *= 0.7
					filtered = append(filtered, r)
				default:
					filtered = append(filtered, r)
				}
			}
			merged = filtered
		}
	}
	tCategory := time.Since(t0)

	// Cooldown filter: skip learnings created less than 30 minutes ago.
	// Prevents echo-injection where a just-stored learning is immediately
	// retrieved because it semantically matches the ongoing conversation.
	if h.store != nil && len(merged) > 0 {
		ids := make([]string, len(merged))
		for i, r := range merged {
			ids[i] = r.ID
		}
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		freshIDs := map[string]bool{}
		rows, err := h.store.ReaderDB().Query(
			"SELECT CAST(id AS TEXT) FROM learnings WHERE id IN ("+strings.Join(placeholders, ",")+") AND created_at > datetime('now', '-30 minutes')", args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					freshIDs[id] = true
				}
			}
		}
		if len(freshIDs) > 0 {
			filtered := merged[:0]
			for _, r := range merged {
				if freshIDs[r.ID] {
					log.Printf("hybrid_search: [SKIP-cooldown] id=%s (created <30min ago)", r.ID)
					continue
				}
				filtered = append(filtered, r)
			}
			merged = filtered
		}
	}

	// Source-boost: user-stated learnings are epistemically more valuable.
	// Consistent with proxy/associate.go source-boost multipliers.
	if h.store != nil && len(merged) > 0 {
		var sids []string
		for _, r := range merged {
			sids = append(sids, r.ID)
		}
		placeholders := make([]string, len(sids))
		args := make([]any, len(sids))
		for i, id := range sids {
			placeholders[i] = "?"
			args[i] = id
		}
		srcMap := map[string]string{}
		rows, err := h.store.ReaderDB().Query("SELECT CAST(id AS TEXT), COALESCE(source, '') FROM learnings WHERE id IN ("+strings.Join(placeholders, ",")+") ", args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, src string
				if rows.Scan(&id, &src) == nil {
					srcMap[id] = src
				}
			}
		}
		for i, r := range merged {
			switch srcMap[r.ID] {
			case "user_stated":
				merged[i].Score *= 1.25
			case "agreed_upon":
				merged[i].Score *= 1.15
			case "hook_auto_learned":
				merged[i].Score *= 1.10
			}
		}
	}

	// Apply cluster-affinity boost: learnings that perform well in the current query cluster rank higher
	// Cluster-affinity needs a query vector to find the nearest cluster.
	if h.store != nil && len(merged) > 0 && len(vectorRes.queryVec) > 0 {
		clusters, err := h.store.GetQueryClusters("")
		if err != nil {
			log.Printf("hybrid_search: cluster load failed: %v", err)
		}
		if len(clusters) > 0 {
			nearestCluster := storage.FindNearestCluster(vectorRes.queryVec, clusters, 0.75)
			if nearestCluster > 0 {
				var lids []string
				for _, r := range merged {
					lids = append(lids, r.ID)
				}
				clusterScores, err := h.store.GetClusterScoresForLearnings(lids)
				if err != nil {
					log.Printf("hybrid_search: cluster scores failed: %v", err)
				}
				affinityApplied := false
				for i, r := range merged {
					scores, ok := clusterScores[r.ID]
					if !ok {
						continue
					}
					for _, cs := range scores {
						if cs.ClusterID != nearestCluster || cs.InjectCount < 3 {
							continue
						}
						useRate := float64(cs.UseCount) / float64(cs.InjectCount)
						noiseRate := float64(cs.NoiseCount) / float64(cs.InjectCount)
						affinity := 1.0 + useRate - noiseRate*0.5
						if affinity < 0.3 {
							affinity = 0.3
						}
						if affinity > 1.8 {
							affinity = 1.8
						}
						merged[i].Score *= affinity
						affinityApplied = true
					}
				}
				if affinityApplied {
					sort.Slice(merged, func(i, j int) bool {
						return merged[i].Score > merged[j].Score
					})
				}
			}
		}
	}

	// Hub dampening: learnings appearing in many clusters are generic — penalize regardless of vector availability.
	// Runs independently of cluster-affinity (which needs a query vector).
	if h.store != nil && len(merged) > 0 {
		var hubIDs []string
		for _, r := range merged {
			hubIDs = append(hubIDs, r.ID)
		}
		if hubScores, err := h.store.GetClusterScoresForLearnings(hubIDs); err == nil {
			for i, r := range merged {
				spread := len(hubScores[r.ID])
				if spread > 3 {
					hubPenalty := 1.0 - 0.1*float64(spread-3)
					if hubPenalty < 0.5 {
						hubPenalty = 0.5
					}
					merged[i].Score *= hubPenalty
				}
			}
		}
	}

	// Apply project-recency boost: fresh same-project results rank higher
	merged = h.applyTurnBasedDecay(merged, project)

	// Graph augmentation: expand results with association neighbors
	merged = h.augmentWithGraphNeighbors(merged)

	// Enrich with subagent metadata
	enrichedResults := h.enrichRankedResults(merged)
	tEnrich := time.Since(t0)

	log.Printf("hybrid_search: done in %v (bm25=%d, vector=%d) [search=%v merge=%v supersede=%v category=%v enrich=%v]",
		tEnrich, len(bm25Results), len(vectorResults),
		tSearch, tMerge-tSearch, tSupersede-tMerge, tCategory-tSupersede, tEnrich-tCategory)

	// Stufe 1: Persist query vector + injected IDs for cluster-scoring (async, non-blocking)
	if h.store != nil && len(vectorRes.queryVec) > 0 {
		var injectedIDs []string
		for _, r := range merged {
			injectedIDs = append(injectedIDs, r.ID)
		}
		qv := vectorRes.queryVec
		go h.store.InsertQueryLog(project, query, qv, injectedIDs)
	}

	return jsonResponse(map[string]any{
		"results":       enrichedResults,
		"bm25_count":    len(bm25Results),
		"vector_count":  len(vectorResults),
		"merged_count":  len(merged),
		"fusion_method": fusionMethod,
		"fusion_k":      60,
		"message":       fmt.Sprintf("Hybrid search: %d BM25 + %d vector → %d merged results", len(bm25Results), len(vectorResults), len(merged)),
		"hint":          fmt.Sprintf("Search for concrete terms from the source (filenames, functions, error messages), not your own summaries. On 0 hits: try different terms.\nIf these results don't answer your question, call deep_search('%s') for raw conversation history (full untruncated content).\nFor structured metadata (files, commands, tags): query_facts(entity=..., action=..., keyword=...).", query),
	})
}

// handleVectorSearch performs vector-only search with cosine similarity scores.
// Returns results with similarity (0-1) for threshold-based filtering.
func (h *Handler) handleVectorSearch(params map[string]any) Response {
	query, _ := params["query"].(string)
	if query == "" {
		return errorResponse("query is required")
	}

	limit := 3
	if l, ok := params["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	queryProvider := h.queryEmbedProvider()
	if queryProvider == nil || !queryProvider.Enabled() || h.vectorStore == nil {
		return errorResponse("vector search not enabled")
	}

	ctx := context.Background()
	vectors, err := queryProvider.Embed(ctx, []string{query})
	if err != nil {
		log.Printf("vector_search: embed failed: %v", err)
		return errorResponse("embedding failed")
	}
	if len(vectors) == 0 {
		return errorResponse("no embedding produced")
	}

	searchResults, err := h.vectorStore.Search(ctx, vectors[0], limit)
	if err != nil {
		log.Printf("vector_search: search failed: %v", err)
		return errorResponse("vector search failed")
	}

	type vectorResult struct {
		ID              string            `json:"id"`
		Content         string            `json:"content"`
		Similarity      float64           `json:"similarity"`
		Metadata        map[string]string `json:"metadata,omitempty"`
		AgentType       string            `json:"agent_type,omitempty"`
		ParentSessionID string            `json:"parent_session_id,omitempty"`
	}

	// Bulk-load subagent metadata (deduplicate IDs)
	seen := make(map[string]bool, len(searchResults))
	var ids []string
	for _, r := range searchResults {
		if r.ID != "" && !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	meta, _ := h.store.GetSessionMetaBulk(ids)

	results := make([]vectorResult, 0, len(searchResults))
	for _, r := range searchResults {
		vr := vectorResult{
			ID:         r.ID,
			Content:    r.Content,
			Similarity: float64(r.Similarity),
			Metadata:   r.Metadata,
		}
		if m, ok := meta[r.ID]; ok {
			vr.AgentType = m.AgentType
			vr.ParentSessionID = m.ParentSessionID
		}
		results = append(results, vr)
	}

	return jsonResponse(results)
}

// resolveSupersededResults replaces superseded learning hits with their active
// successors. Follows supersede chains (A→B→C) and deduplicates results.
// Learnings with negative superseded_by (bulk-resolved, no valid successor) are dropped.
func (h *Handler) resolveSupersededResults(results []embedding.RankedResult) []embedding.RankedResult {
	if h.store == nil || len(results) == 0 {
		return results
	}

	// Collect numeric IDs
	var ids []int64
	for _, r := range results {
		if id, err := strconv.ParseInt(r.ID, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}

	redirects, err := h.store.ResolveSupersededIDs(ids)
	if err != nil || len(redirects) == 0 {
		return results
	}

	// Build output with redirects applied, dedup by active ID
	seen := map[string]bool{}
	out := make([]embedding.RankedResult, 0, len(results))
	for _, r := range results {
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		if target, ok := redirects[id]; ok {
			if target <= 0 {
				continue // negative supersede ID = bulk-resolved, no valid successor
			}
			// Superseded → load active successor
			active, err := h.store.GetLearning(target)
			if err != nil {
				continue // successor not found, skip
			}
			activeID := strconv.FormatInt(active.ID, 10)
			if seen[activeID] {
				continue // already in results
			}
			seen[activeID] = true
			out = append(out, embedding.RankedResult{
				ID:      activeID,
				Content: active.Content,
				Score:   r.Score * 0.7, // penalty: found via old content, new content may have drifted
				Source:  r.Source,
				Project: active.Project,
			})
		} else {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out
}

// augmentWithGraphNeighbors expands search results with graph-connected learnings.
// Looks up all outgoing association edges for result IDs, loads missing neighbors,
// and inserts them with a derived score. Max 3 per source, 5 total.
func (h *Handler) augmentWithGraphNeighbors(merged []embedding.RankedResult) []embedding.RankedResult {
	if h.store == nil || len(merged) == 0 {
		return merged
	}

	const maxPerSource = 3
	const maxTotal = 5

	var ids []string
	existingIDs := make(map[string]bool)
	for _, r := range merged {
		ids = append(ids, r.ID)
		existingIDs[r.ID] = true
	}

	neighbors, err := h.store.GetAssociationNeighbors(ids, maxPerSource)
	if err != nil {
		log.Printf("graph augment: neighbor lookup failed: %v", err)
		return merged
	}

	added := 0
	for _, r := range merged {
		if added >= maxTotal {
			break
		}
		edges, ok := neighbors[r.ID]
		if !ok {
			continue
		}
		for _, edge := range edges {
			if added >= maxTotal {
				break
			}
			if existingIDs[edge.TargetID] {
				continue
			}

			targetID, err := strconv.ParseInt(edge.TargetID, 10, 64)
			if err != nil {
				continue
			}
			// GetAssociationNeighbors already filters superseded+quarantined via JOIN
			learning, err := h.store.GetLearning(targetID)
			if err != nil || learning == nil {
				continue
			}

			// Derive score: parent * base multiplier, modulated by weight (capped at 3.0).
			// depends_on/supports edges get higher multipliers — prerequisites and
			// supporting evidence are more valuable than generic associations.
			weight := edge.Weight
			if weight > 3.0 {
				weight = 3.0
			}
			var baseMultiplier float64
			var source string
			switch edge.RelationType {
			case "depends_on":
				baseMultiplier = 0.8
				source = "graph:depends_on"
			case "supports":
				baseMultiplier = 0.7
				source = "graph:supports"
			default:
				baseMultiplier = 0.6
				source = "graph"
			}
			derivedScore := r.Score * baseMultiplier * weight / 3.0

			merged = append(merged, embedding.RankedResult{
				ID:      edge.TargetID,
				Content: learning.Content,
				Score:   derivedScore,
				Source:  source,
				Project: learning.Project,
			})
			existingIDs[edge.TargetID] = true
			added++
			log.Printf("graph augment: added [ID:%s] via %s edge from [ID:%s] (score=%.2f, weight=%.1f)",
				edge.TargetID, edge.RelationType, r.ID, derivedScore, edge.Weight)
		}
	}

	if added > 0 {
		sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	}
	return merged
}

// applyTurnBasedDecay re-ranks merged results by applying turn-based decay
// to the RRF score. Learnings with many project turns since creation decay,
// learnings in paused projects stay at full relevance.
func (h *Handler) applyTurnBasedDecay(results []embedding.RankedResult, project string) []embedding.RankedResult {
	if h.store == nil || len(results) == 0 {
		return results
	}

	// Bulk lookup decay-relevant fields for all result IDs
	placeholders := make([]string, len(results))
	args := make([]any, len(results))
	for i, r := range results {
		placeholders[i] = "?"
		args[i] = r.ID
	}
	rows, err := h.store.DB().Query("SELECT CAST(id AS TEXT), COALESCE(project, ''), COALESCE(source, ''), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0), COALESCE(turns_at_creation, 0) FROM learnings WHERE id IN ("+strings.Join(placeholders, ",")+") AND valid_until IS NULL", args...)
	if err != nil {
		log.Printf("applyTurnBasedDecay: query failed: %v", err)
		return results
	}
	defer rows.Close()

	type decayMeta struct {
		project         string
		source          string
		useCount        int
		saveCount       int
		stability       float64
		turnsAtCreation int64
	}
	metaMap := make(map[string]decayMeta, len(results))
	projectSet := make(map[string]bool)
	hasGlobal := false
	for rows.Next() {
		var id, proj, source string
		var useCount, saveCount int
		var stability float64
		var turnsAtCreation int64
		if rows.Scan(&id, &proj, &source, &useCount, &saveCount, &stability, &turnsAtCreation) == nil {
			metaMap[id] = decayMeta{project: proj, source: source, useCount: useCount, saveCount: saveCount, stability: stability, turnsAtCreation: turnsAtCreation}
			if proj != "" {
				projectSet[proj] = true
			} else {
				hasGlobal = true
			}
		}
	}

	// Bulk-fetch current turn counts for all projects in results
	projectList := make([]string, 0, len(projectSet)+1)
	for p := range projectSet {
		projectList = append(projectList, p)
	}
	if hasGlobal {
		projectList = append(projectList, "__global__")
	}
	turnCounts, _ := h.store.GetTurnCountsBulk(projectList)

	// Apply turn-based decay as multiplicative factor
	for i, r := range results {
		m, ok := metaMap[r.ID]
		if !ok {
			continue
		}
		turnKey := m.project
		if turnKey == "" {
			turnKey = "__global__"
		}
		currentTurn := turnCounts[turnKey]
		decay := models.TurnBasedDecay(m.turnsAtCreation, currentTurn, m.stability, m.source, m.useCount, m.saveCount)
		results[i].Score *= decay
	}

	// Re-sort by decayed score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// filterResultsByTime filters RankedResults by created_at using a bulk SQLite lookup.
// Same pattern as BM25 post-filter in SearchLearningsBM25Ctx.
func (h *Handler) filterResultsByTime(results []embedding.RankedResult, since, before string) []embedding.RankedResult {
	if len(results) == 0 {
		return results
	}
	placeholders := make([]string, len(results))
	args := make([]any, len(results))
	for i, r := range results {
		placeholders[i] = "?"
		args[i] = r.ID
	}
	rows, err := h.store.DB().Query("SELECT CAST(id AS TEXT), created_at FROM learnings WHERE id IN ("+strings.Join(placeholders, ",")+") AND valid_until IS NULL", args...)
	if err != nil {
		log.Printf("filterResultsByTime: query failed: %v", err)
		return results
	}
	defer rows.Close()
	timeMap := make(map[string]string, len(results))
	for rows.Next() {
		var id, created string
		if rows.Scan(&id, &created) == nil {
			timeMap[id] = created
		}
	}
	var filtered []embedding.RankedResult
	for _, r := range results {
		created, ok := timeMap[r.ID]
		if !ok {
			continue
		}
		if since != "" && created < since {
			continue
		}
		if before != "" && created >= before {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// enrichRankedResults adds subagent metadata and agent_role to RankedResult slices (used by hybrid_search).
func (h *Handler) enrichRankedResults(results []embedding.RankedResult) []map[string]any {
	seen := make(map[string]bool, len(results))
	var ids []string
	for _, r := range results {
		if r.ID != "" && !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	roles, _ := h.store.GetAgentRolesBulk(ids)

	// Bulk-fetch learning source + category fields plus the owning session metadata.
	sources := map[string]string{}
	categories := map[string]string{}
	agentTypes := map[string]string{}
	parentSessionIDs := map[string]string{}
	sourceAgents := map[string]string{}
	if h.store != nil && len(ids) > 0 {
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := h.store.ReaderDB().Query(`
			SELECT CAST(l.id AS TEXT),
				COALESCE(l.source, ''),
				COALESCE(l.category, ''),
				COALESCE(s.agent_type, ''),
				COALESCE(s.parent_session_id, ''),
				COALESCE(s.source_agent, 'claude')
			FROM learnings l
			LEFT JOIN sessions s ON s.id = l.session_id
			WHERE l.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, src, cat, agentType, parentSessionID, sourceAgent string
				if rows.Scan(&id, &src, &cat, &agentType, &parentSessionID, &sourceAgent) == nil {
					sources[id] = src
					categories[id] = cat
					agentTypes[id] = agentType
					parentSessionIDs[id] = parentSessionID
					sourceAgents[id] = sourceAgent
				}
			}
		}
	}

	out := make([]map[string]any, len(results))
	for i, r := range results {
		m := map[string]any{
			"id":      r.ID,
			"content": r.Content,
			"score":   r.Score,
			"source":  r.Source,
			"project": r.Project,
		}
		if src, ok := sources[r.ID]; ok {
			m["learning_source"] = src
		}
		if cat, ok := categories[r.ID]; ok && cat != "" {
			m["category"] = cat
		}
		if sourceAgent, ok := sourceAgents[r.ID]; ok && sourceAgent != "" {
			m["source_agent"] = sourceAgent
		}
		if agentType, ok := agentTypes[r.ID]; ok && agentType != "" {
			m["agent_type"] = agentType
		}
		if parentSessionID, ok := parentSessionIDs[r.ID]; ok && parentSessionID != "" {
			m["parent_session_id"] = parentSessionID
		}
		if role, ok := roles[r.ID]; ok && role != "" {
			m["agent_role"] = role
		}
		out[i] = m
	}
	return out
}
