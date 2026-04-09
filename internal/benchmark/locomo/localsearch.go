
package locomo

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/storage"
)

// LocalSearcher provides hybrid search (BM25 + Vector + RRF) directly against a store.
// Uses the same code paths as the daemon — no mocks, no wrappers.
type LocalSearcher struct {
	store       *storage.Store
	provider    embedding.Provider
	vectorStore *embedding.VectorStore
}

// NewLocalSearcher creates a searcher with BM25 + optional vector search.
// If provider is nil, falls back to BM25-only.
func NewLocalSearcher(store *storage.Store, provider embedding.Provider, vs *embedding.VectorStore) *LocalSearcher {
	return &LocalSearcher{store: store, provider: provider, vectorStore: vs}
}

// HybridSearch runs BM25 + Vector search with RRF merge — same as daemon handler_hybrid.go.
// When provider is nil, falls back to BM25-only (no vector component).
func (s *LocalSearcher) HybridSearch(query, project string, limit int) ([]SearchResult, error) {
	ctx := context.Background()

	// BM25 with term-existence-filter + IDF-sort + term-cap
	bm25Ranked := s.searchBM25ForProject(query, project, limit)

	// AQ-FTS as separate RRF lane (porter stemming catches synonyms like dog/puppy)
	aqRanked := s.searchAQFTS(query, project, limit)

	// Entity-boost: separate RRF input
	entityRanked := s.searchEntityBoost(query, project, limit)

	// Vector search (if available)
	var vectorRanked []embedding.RankedResult
	if s.provider != nil && s.provider.Enabled() && s.vectorStore != nil {
		vecs, embedErr := s.provider.Embed(ctx, []string{query})
		if embedErr == nil && len(vecs) > 0 {
			vecResults, vecErr := s.vectorStore.SearchWithProject(ctx, vecs[0], limit*2, project)
			if vecErr == nil {
				seen := make(map[string]bool)
				for _, r := range vecResults {
					content := r.Content
					learningID := r.ID
					if lc, ok := r.Metadata["learning_content"]; ok && lc != "" {
						content = lc
						learningID = r.Metadata["learning_id"]
					}
					if seen[learningID] {
						continue
					}
					seen[learningID] = true
					vectorRanked = append(vectorRanked, embedding.RankedResult{
						ID:            learningID,
						Content:       content,
						Score:         float64(r.Similarity),
						OriginalScore: float64(r.Similarity),
						Source:        "semantic",
					})
				}
			}
		}
	}

	merged := rrfMerge3Way(bm25Ranked, vectorRanked, entityRanked, 60, limit)

	// Inject AQ-FTS results into merged — boost existing entries or add new ones
	if len(aqRanked) > 0 {
		mergedIDs := make(map[string]int, len(merged))
		for i, r := range merged {
			mergedIDs[r.ID] = i
		}
		for rank, aq := range aqRanked {
			aqScore := 1.0 / float64(60+rank+1)
			if idx, exists := mergedIDs[aq.ID]; exists {
				merged[idx].Score += aqScore
			} else {
				merged = append(merged, embedding.RankedResult{
					ID:      aq.ID,
					Content: aq.Content,
					Score:   aqScore,
					Source:  "aq_keyword",
				})
			}
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
		if len(merged) > limit {
			merged = merged[:limit]
		}
	}

	// Debug: log search component hit counts + top merged IDs
	var topIDs []string
	for i, r := range merged {
		if i >= 5 { break }
		topIDs = append(topIDs, r.ID+"("+r.Source+")")
	}
	log.Printf("    [search] bm25=%d vec=%d entity=%d merged=%d top=[%s] q=%s", len(bm25Ranked), len(vectorRanked), len(entityRanked), len(merged), strings.Join(topIDs, ","), query[:min(len(query), 40)])

	results := make([]SearchResult, 0, len(merged))
	for _, r := range merged {
		results = append(results, SearchResult{Content: r.Content, Score: r.Score})
	}
	return results, nil
}

// termIDF holds a quoted FTS5 term and its document frequency (hit count).
type termIDF struct {
	quoted string
	hits   int
}

// searchBM25Optimized runs BM25 with three optimizations:
// 1. Term-Existence-Filter: drop terms with 0 hits in corpus
// 2. IDF-Sort: rarest terms first (most discriminative)
// 3. Term-Cap at 6: avoid overly specific AND queries
func (s *LocalSearcher) searchBM25Optimized(query string, limit int) []embedding.RankedResult {
	return s.searchBM25ForProject(query, "", limit)
}

// searchBM25ForProject runs BM25 with optional project filter.
func (s *LocalSearcher) searchBM25ForProject(query, project string, limit int) []embedding.RankedResult {
	db := s.store.DB()
	if db == nil {
		return nil
	}

	words := tokenizeQuery(query)
	if len(words) == 0 {
		return nil
	}

	// 1. Check term existence + get IDF (hit count per term)
	var alive []termIDF
	for _, w := range words {
		quoted := `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
		var hits int
		err := db.QueryRow(`SELECT COUNT(*) FROM learnings_fts WHERE learnings_fts MATCH ?`, quoted).Scan(&hits)
		if err != nil || hits == 0 {
			continue // term doesn't exist in corpus — skip
		}
		alive = append(alive, termIDF{quoted: quoted, hits: hits})
	}

	if len(alive) == 0 {
		return nil
	}

	// 2. Sort by IDF ascending (rarest terms first = most discriminative)
	sort.Slice(alive, func(i, j int) bool {
		return alive[i].hits < alive[j].hits
	})

	// 3. Cap at 6 terms
	if len(alive) > 6 {
		alive = alive[:6]
	}

	// Tiered AND query: try all terms, then progressively drop least-specific
	for len(alive) >= 2 {
		terms := make([]string, len(alive))
		for i, t := range alive {
			terms[i] = t.quoted
		}
		ftsQuery := strings.Join(terms, " AND ")
		results := runFTSQuery(db, ftsQuery, project, limit)
		if len(results) > 0 {
			return results
		}
		// Drop the MOST common term (last after IDF sort) and retry
		alive = alive[:len(alive)-1]
	}

	// Single term fallback
	if len(alive) == 1 {
		return runFTSQuery(db, alive[0].quoted, project, limit)
	}

	return nil
}

// runFTSQuery executes a FTS5 MATCH query and returns ranked results.
// If project is non-empty, filters to learnings of that project only.
func runFTSQuery(db *sql.DB, ftsQuery, project string, limit int) []embedding.RankedResult {
	var rows *sql.Rows
	var err error
	if project != "" {
		rows, err = db.Query(`SELECT l.id, l.content, bm25(learnings_fts) AS score FROM learnings_fts JOIN learnings l ON l.id = learnings_fts.rowid WHERE learnings_fts MATCH ? AND l.project = ? ORDER BY bm25(learnings_fts) LIMIT ?`, ftsQuery, project, limit*3)
	} else {
		rows, err = db.Query(`SELECT rowid, content, bm25(learnings_fts) AS score FROM learnings_fts WHERE learnings_fts MATCH ? ORDER BY bm25(learnings_fts) LIMIT ?`, ftsQuery, limit*3)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []embedding.RankedResult
	for rows.Next() {
		var id int64
		var content string
		var score float64
		if err := rows.Scan(&id, &content, &score); err != nil {
			continue
		}
		results = append(results, embedding.RankedResult{
			ID:            fmt.Sprintf("%d", id),
			Content:       content,
			Score:         -score,
			OriginalScore: -score,
			Source:        "keyword",
		})
	}
	return results
}

// searchEntityBoost finds learnings that share entities mentioned in the query.
// Extracts capitalized words (likely names/entities) from query, looks them up
// in learning_entities junction table, returns matching learnings with content.
func (s *LocalSearcher) searchEntityBoost(query, project string, limit int) []embedding.RankedResult {
	db := s.store.DB()
	if db == nil {
		return nil
	}

	// Extract likely entity names: capitalized words 3+ chars
	words := strings.Fields(query)
	var entities []string
	for _, w := range words {
		clean := strings.Trim(w, "?!.,;:'\"")
		if len(clean) >= 3 && clean[0] >= 'A' && clean[0] <= 'Z' {
			entities = append(entities, strings.ToLower(clean))
		}
	}
	if len(entities) == 0 {
		return nil
	}

	// Build query: find learnings with matching entities, ranked by match count
	placeholders := make([]string, len(entities))
	args := make([]interface{}, len(entities))
	for i, e := range entities {
		placeholders[i] = "?"
		args[i] = e
	}
	args = append(args, project, limit*2)

	q := fmt.Sprintf(`SELECT le.learning_id, l.content, COUNT(*) as match_count FROM learning_entities le JOIN learnings l ON l.id = le.learning_id WHERE LOWER(le.value) IN (%s) AND l.superseded_by IS NULL AND l.project = ? GROUP BY le.learning_id ORDER BY match_count DESC, le.learning_id DESC LIMIT ?`, strings.Join(placeholders, ","))

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []embedding.RankedResult
	for rows.Next() {
		var id int64
		var content string
		var matchCount int
		if err := rows.Scan(&id, &content, &matchCount); err != nil {
			continue
		}
		// Score proportional to entity match count (more matches = more relevant)
		score := float64(matchCount) * 50.0
		results = append(results, embedding.RankedResult{
			ID:            fmt.Sprintf("%d", id),
			Content:       content,
			Score:         score,
			OriginalScore: score,
			Source:        "entity",
		})
	}
	return results
}

// rrfMerge3Way combines BM25, Vector, and Entity results using 3-way Reciprocal Rank Fusion.
func rrfMerge3Way(bm25, vector, entity []embedding.RankedResult, k int, limit int) []embedding.RankedResult {
	type docInfo struct {
		rrfScore    float64
		vecScore    float64
		bm25Score   float64
		entityScore float64
		content     string
		sources     int // bitmask: 1=bm25, 2=vector, 4=entity
	}

	docs := make(map[string]*docInfo)

	addRanked := func(ranked []embedding.RankedResult, bit int) {
		for rank, r := range ranked {
			info, ok := docs[r.ID]
			if !ok {
				info = &docInfo{content: r.Content}
				docs[r.ID] = info
			}
			info.rrfScore += 1.0 / float64(k+rank+1)
			info.sources |= bit
			switch bit {
			case 1:
				if r.OriginalScore > info.bm25Score {
					info.bm25Score = r.OriginalScore
				}
			case 2:
				if r.OriginalScore > info.vecScore {
					info.vecScore = r.OriginalScore
				}
			case 4:
				if r.OriginalScore > info.entityScore {
					info.entityScore = r.OriginalScore
				}
			}
		}
	}

	addRanked(bm25, 1)
	addRanked(vector, 2)
	addRanked(entity, 4)

	results := make([]embedding.RankedResult, 0, len(docs))
	for id, info := range docs {
		// Final score: cosine*100 for semantic, BM25 score for keyword, entity as bonus
		var finalScore float64
		hasVec := info.sources&2 != 0
		hasBM25 := info.sources&1 != 0
		hasEntity := info.sources&4 != 0

		if hasVec {
			finalScore = info.vecScore * 100
		}
		if hasBM25 && info.bm25Score > finalScore {
			finalScore = info.bm25Score
		}
		// Entity bonus: +15 per source overlap, rewards appearing in multiple lists
		if hasEntity {
			finalScore += 15
		}
		// Multi-source bonus
		sourceCount := 0
		if hasBM25 { sourceCount++ }
		if hasVec { sourceCount++ }
		if hasEntity { sourceCount++ }
		if sourceCount >= 2 {
			finalScore += 5 * float64(sourceCount-1)
		}
		if finalScore > 100 {
			finalScore = 100
		}

		source := "keyword"
		if hasVec && hasBM25 {
			source = "hybrid"
		} else if hasVec {
			source = "semantic"
		} else if hasEntity && !hasBM25 {
			source = "entity"
		}

		results = append(results, embedding.RankedResult{
			ID:            id,
			Content:       info.content,
			Score:         finalScore,
			OriginalScore: info.vecScore,
			Source:        source,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// searchAQFTS searches anticipated_queries FTS (porter stemming) and returns parent learning content.
func (s *LocalSearcher) searchAQFTS(query, project string, limit int) []embedding.RankedResult {
	db := s.store.DB()
	if db == nil {
		return nil
	}
	words := tokenizeQuery(query)
	if len(words) == 0 {
		return nil
	}
	// Build quoted terms for FTS5 MATCH
	terms := make([]string, 0, len(words))
	for _, w := range words {
		terms = append(terms, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
	}
	// Try all terms first, then progressively drop
	for len(terms) >= 2 {
		ftsQuery := strings.Join(terms, " AND ")
		results := s.runAQFTSQuery(db, ftsQuery, project, limit)
		if len(results) > 0 {
			return results
		}
		terms = terms[:len(terms)-1]
	}
	if len(terms) == 1 {
		return s.runAQFTSQuery(db, terms[0], project, limit)
	}
	return nil
}

func (s *LocalSearcher) runAQFTSQuery(db *sql.DB, ftsQuery, project string, limit int) []embedding.RankedResult {
	rows, err := db.Query(`SELECT learning_id, learning_content, bm25(anticipated_queries_fts) AS score FROM anticipated_queries_fts WHERE anticipated_queries_fts MATCH ? ORDER BY bm25(anticipated_queries_fts) LIMIT ?`, ftsQuery, limit*3)
	if err != nil {
		return nil
	}
	defer rows.Close()
	seen := make(map[string]bool)
	var results []embedding.RankedResult
	for rows.Next() {
		var lid, content string
		var score float64
		if err := rows.Scan(&lid, &content, &score); err != nil {
			continue
		}
		if seen[lid] {
			continue
		}
		seen[lid] = true
		results = append(results, embedding.RankedResult{
			ID:            lid,
			Content:       content,
			Score:         -score,
			OriginalScore: -score,
			Source:        "aq_keyword",
		})
	}
	return results
}

// mergeRankedDedup appends b to a, skipping duplicates by ID.
func mergeRankedDedup(a, b []embedding.RankedResult) []embedding.RankedResult {
	seen := make(map[string]bool, len(a))
	for _, r := range a {
		seen[r.ID] = true
	}
	for _, r := range b {
		if !seen[r.ID] {
			a = append(a, r)
			seen[r.ID] = true
		}
	}
	return a
}

// tokenizeQuery splits a query into searchable tokens, filtering stopwords and short terms.
// Treats '_', '-', '.' as word characters (tokenchars equivalent).
func tokenizeQuery(query string) []string {
	// Split on whitespace and punctuation, keeping '_', '-', '.'
	var words []string
	var buf strings.Builder
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			buf.WriteRune(unicode.ToLower(r))
		} else if buf.Len() > 0 {
			words = append(words, buf.String())
			buf.Reset()
		}
	}
	if buf.Len() > 0 {
		words = append(words, buf.String())
	}

	// Filter stopwords and short terms
	var filtered []string
	for _, w := range words {
		if len(w) < 3 || isStopword(w) {
			continue
		}
		filtered = append(filtered, w)
	}
	return filtered
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "had": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"has": true, "his": true, "how": true, "its": true, "may": true,
	"who": true, "did": true, "get": true, "got": true, "him": true,
	"let": true, "say": true, "she": true, "too": true, "use": true,
	"that": true, "with": true, "have": true, "this": true, "will": true,
	"your": true, "from": true, "they": true, "been": true, "said": true,
	"each": true, "which": true, "their": true, "what": true, "about": true,
	"would": true, "there": true, "when": true, "make": true, "like": true,
	"does": true, "into": true, "than": true, "them": true, "some": true,
	"could": true, "other": true, "were": true, "more": true, "after": true,
}

func isStopword(w string) bool {
	return stopwords[w]
}

// SearchMessages runs BM25 on raw message history with ±3 message context window.
func (s *LocalSearcher) SearchMessages(query, project string, limit int) ([]SearchResult, error) {
	msgResults, err := s.store.SearchMessages(query, limit*3)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}

	db := s.store.MessagesDB()
	results := make([]SearchResult, 0, len(msgResults))
	seen := make(map[string]bool)
	for _, r := range msgResults {
		if !strings.HasPrefix(r.SessionID, project) {
			continue
		}
		// Build context window: matched message + ±3 neighbors
		content := s.buildMessageContext(db, r.SessionID, r.Sequence, r.Timestamp)
		if content == "" {
			// Fallback: just the matched message
			content = r.Content
			if r.Timestamp != "" && len(r.Timestamp) >= 10 {
				content = "[" + r.Timestamp[:10] + "] " + content
			}
		}
		// Dedup by session+sequence to avoid overlapping context windows
		key := r.SessionID + fmt.Sprintf("-%d", r.Sequence)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, SearchResult{Content: content, Score: -r.Rank})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

// buildMessageContext returns the matched message with ±3 surrounding messages for context.
func (s *LocalSearcher) buildMessageContext(db *sql.DB, sessionID string, sequence int, timestamp string) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`SELECT sequence, content, timestamp FROM messages WHERE session_id = ? AND sequence BETWEEN ? AND ? ORDER BY sequence`, sessionID, sequence-3, sequence+3)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	for rows.Next() {
		var seq int
		var content, ts string
		if err := rows.Scan(&seq, &content, &ts); err != nil {
			continue
		}
		date := ""
		if len(ts) >= 10 {
			date = "[" + ts[:10] + "] "
		}
		if seq == sequence {
			fmt.Fprintf(&sb, "%s>>> %s\n", date, content)
		} else {
			fmt.Fprintf(&sb, "%s%s\n", date, content)
		}
	}
	return sb.String()
}

// TieredLocalSearch executes multi-tier search: hybrid -> messages -> reformulated keywords.
func TieredLocalSearch(searcher *LocalSearcher, question, project string, cfg TieredConfig) []SearchResult {
	results, _ := searcher.HybridSearch(question, project, cfg.TopK)
	if countGood(results, cfg.ScoreThreshold) >= cfg.MinResults {
		return results
	}

	msgResults, _ := searcher.SearchMessages(question, project, cfg.TopK)
	results = mergeDedup(results, msgResults)
	if countGood(results, cfg.ScoreThreshold) >= cfg.MinResults {
		return results
	}

	keywords := extractKeywords(question)
	if keywords != question && keywords != "" {
		kwResults, _ := searcher.HybridSearch(keywords, project, cfg.TopK)
		results = mergeDedup(results, kwResults)
	}

	return results
}
