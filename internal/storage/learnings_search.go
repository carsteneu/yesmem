package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// QueryFactsOpts defines filters for structured fact queries on learning metadata.
type QueryFactsOpts struct {
	Entity   string // LIKE match on learning_entities.value
	Action   string // LIKE match on learning_actions.value
	Keyword  string // LIKE match on learning_keywords.value
	Domain   string // Exact match on learnings.domain
	Project  string // Project filter
	Category string // Category filter
	Limit    int    // Default 20
}

// QueryFacts returns active learnings matching structured metadata filters (entities, actions, keywords).
func (s *Store) QueryFacts(opts QueryFactsOpts) ([]models.Learning, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	selectCols := `l.id, l.session_id, l.category, l.content, l.project, l.confidence,
		l.superseded_by, l.supersede_reason, l.created_at, l.expires_at, l.model_used, l.source,
		COALESCE(l.hit_count, 0), COALESCE(l.emotional_intensity, 0.0), l.last_hit_at, COALESCE(l.session_flavor, ''), l.valid_until, l.supersedes, COALESCE(l.importance, 3), l.supersede_status, COALESCE(l.noise_count, 0), COALESCE(l.fail_count, 0),
		COALESCE(l.match_count, 0), COALESCE(l.inject_count, 0), COALESCE(l.use_count, 0), COALESCE(l.save_count, 0), COALESCE(l.stability, 30.0),
		COALESCE(l.context, ''), COALESCE(l.domain, 'code'), COALESCE(l.trigger_rule, ''), COALESCE(l.embedding_text, ''),
		COALESCE(l.source_file, ''), COALESCE(l.source_hash, ''), COALESCE(l.doc_chunk_ref, 0), COALESCE(l.task_type, ''), COALESCE(l.turns_at_creation, 0), COALESCE(l.source_msg_from, -1), COALESCE(l.source_msg_to, -1)`

	query := `SELECT DISTINCT ` + selectCols + ` FROM learnings l`
	var joins []string
	var where []string
	var args []any

	where = append(where, `l.superseded_by IS NULL`)
	where = append(where, `(l.expires_at IS NULL OR l.expires_at > ?)`)
	args = append(args, fmtTime(time.Now()))
	where = append(where, `COALESCE(l.quarantined_at, '') = ''`)

	if opts.Entity != "" {
		joins = append(joins, `JOIN learning_entities le ON le.learning_id = l.id`)
		where = append(where, `le.value LIKE ?`)
		args = append(args, "%"+opts.Entity+"%")
	}
	if opts.Action != "" {
		joins = append(joins, `JOIN learning_actions la ON la.learning_id = l.id`)
		where = append(where, `la.value LIKE ?`)
		args = append(args, "%"+opts.Action+"%")
	}
	if opts.Keyword != "" {
		joins = append(joins, `JOIN learning_keywords lk ON lk.learning_id = l.id`)
		where = append(where, `lk.value LIKE ?`)
		args = append(args, "%"+opts.Keyword+"%")
	}
	if opts.Domain != "" {
		where = append(where, `l.domain = ?`)
		args = append(args, opts.Domain)
	}
	if opts.Project != "" {
		where = append(where, `l.project = ?`)
		args = append(args, opts.Project)
	}
	if opts.Category != "" {
		where = append(where, `l.category = ?`)
		args = append(args, opts.Category)
	}

	for _, j := range joins {
		query += " " + j
	}
	query += " WHERE " + strings.Join(where, " AND ")
	query += ` ORDER BY l.importance DESC, l.created_at DESC LIMIT ?`
	args = append(args, opts.Limit)

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()

	return scanLearnings(rows)
}

// SearchUnfinished searches active unfinished items using FTS5 full-text search.
// Words are joined with OR so partial matches work (e.g. "Signal-to-Noise verbessern"
// finds "Signal-to-Noise 60/40→90/10" even though "verbessern" is absent).
// Results are ranked by FTS5 relevance (bm25).
func (s *Store) SearchUnfinished(query, project string) ([]models.Learning, error) {
	words := splitWords(query)
	if len(words) == 0 {
		return nil, nil
	}

	// Build FTS5 query: "word1" OR "word2" OR "word3"
	// Quoting prevents FTS5 from interpreting hyphens as column operators
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}
	ftsQuery := strings.Join(quoted, " OR ")

	base := `SELECT l.id, l.session_id, l.category, l.content, l.project, l.confidence,
		l.superseded_by, l.supersede_reason, l.created_at, l.expires_at, l.model_used, l.source,
		COALESCE(l.hit_count, 0), COALESCE(l.emotional_intensity, 0.0), l.last_hit_at, COALESCE(l.session_flavor, ''), l.valid_until, l.supersedes, COALESCE(l.importance, 3), l.supersede_status, COALESCE(l.noise_count, 0), COALESCE(l.fail_count, 0),
		COALESCE(l.match_count, 0), COALESCE(l.inject_count, 0), COALESCE(l.use_count, 0), COALESCE(l.save_count, 0), COALESCE(l.stability, 30.0),
		COALESCE(l.context, ''), COALESCE(l.domain, 'code'), COALESCE(l.trigger_rule, ''), COALESCE(l.embedding_text, ''),
		COALESCE(l.source_file, ''), COALESCE(l.source_hash, ''), COALESCE(l.doc_chunk_ref, 0), COALESCE(l.task_type, ''), COALESCE(l.turns_at_creation, 0), COALESCE(l.source_msg_from, -1), COALESCE(l.source_msg_to, -1)
		FROM learnings_fts
		JOIN learnings l ON l.id = learnings_fts.rowid
		WHERE learnings_fts MATCH ?
		AND l.superseded_by IS NULL AND l.category = 'unfinished'`
	args := []any{ftsQuery}

	if project != "" {
		base += ` AND (l.project = ? OR l.project IS NULL OR l.project = '')`
		args = append(args, project)
	}

	base += ` ORDER BY bm25(learnings_fts) LIMIT 10`

	rows, err := s.readerDB().Query(base, args...)
	if err != nil {
		return nil, fmt.Errorf("search unfinished: %w", err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// LearningSearchResult is a lightweight result for hybrid search fusion.
type LearningSearchResult struct {
	ID      string
	Content string
	Score   float64
	Project string
}

// SearchLearningsBM25 searches active learnings using FTS5 and returns ranked results
// with BM25 scores. Designed for RRF fusion with vector search (same ID space).
func (s *Store) SearchLearningsBM25(query, project string, limit int) ([]LearningSearchResult, error) {
	return s.SearchLearningsBM25Ctx(context.Background(), query, project, "", "", limit)
}

// SearchLearningsBM25Ctx is like SearchLearningsBM25 but accepts a context for cancellation/timeout.
// Uses a tiered AND-query approach:
//   - Tier 1 (100% terms AND) → score max 100
//   - Tier 2 (66% terms AND) → score max 60
//   - Tier 3 (33% terms AND) → score max 40
func (s *Store) SearchLearningsBM25Ctx(ctx context.Context, query, project, since, before string, limit int) ([]LearningSearchResult, error) {
	words := splitWords(query)
	if len(words) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}

	// Quote each word for FTS5
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
	}

	type ftsHit struct {
		id      string
		content string
		score   float64
	}

	runFTS := func(terms []string) ([]ftsHit, error) {
		ftsQuery := strings.Join(terms, " AND ")
		ftsSQL := `SELECT rowid, content, bm25(learnings_fts) AS score FROM learnings_fts WHERE learnings_fts MATCH ? ORDER BY bm25(learnings_fts) LIMIT ?`
		rows, err := s.readerDB().QueryContext(ctx, ftsSQL, ftsQuery, limit*5)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var hits []ftsHit
		for rows.Next() {
			var h ftsHit
			if rows.Scan(&h.id, &h.content, &h.score) == nil {
				h.score = -h.score // FTS5 bm25() is negative (lower=better), negate
				hits = append(hits, h)
			}
		}
		return hits, rows.Err()
	}

	// Tiered AND search: cap at 5, 4, 3, 2 terms.
	// Terms sorted by IDF (rarest first) so when we drop for lower tiers,
	// we lose the most common (least specific) terms first.
	// Scores: 5 terms = 100, 4 terms = 90, 3 terms = 70, 2 terms = 50.

	// IDF-sort: count document frequency per term, filter out zero-hit terms, sort rarest first
	type termFreq struct {
		term  string
		count int
	}
	var freqs []termFreq
	for _, q := range quoted {
		var cnt int
		_ = s.readerDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM learnings_fts WHERE learnings_fts MATCH ?`, q).Scan(&cnt)
		if cnt > 0 {
			freqs = append(freqs, termFreq{q, cnt})
		}
	}
	if len(freqs) == 0 {
		return nil, nil
	}
	sort.Slice(freqs, func(i, j int) bool {
		return freqs[i].count < freqs[j].count
	})
	sortedTerms := make([]string, len(freqs))
	for i, f := range freqs {
		sortedTerms[i] = f.term
	}

	type tier struct {
		terms    []string
		maxScore float64
	}
	n := len(sortedTerms)
	seen := make(map[int]bool)
	var tiers []tier
	for _, count := range []int{5, 4, 3, 2} {
		c := count
		if c > n {
			c = n
		}
		if c < 2 || seen[c] {
			continue
		}
		seen[c] = true
		score := map[int]float64{5: 100, 4: 90, 3: 70, 2: 50}[count]
		if c < count {
			// fewer terms available than tier target — downgrade score proportionally
			score = score * float64(c) / float64(count)
		}
		tiers = append(tiers, tier{sortedTerms[:c], score})
	}

	var hits []ftsHit
	var maxScore float64
	for _, t := range tiers {
		h, err := runFTS(t.terms)
		if err != nil {
			return nil, fmt.Errorf("search learnings bm25 tier: %w", err)
		}
		if len(h) > 0 {
			hits = h
			maxScore = t.maxScore
			break
		}
	}
	if len(hits) == 0 {
		return nil, nil
	}

	// Normalize BM25 scores within tier: top hit = maxScore, others scale proportionally
	topBM25 := hits[0].score
	if topBM25 > 0 {
		for i := range hits {
			hits[i].score = (hits[i].score / topBM25) * maxScore
		}
	}

	// Step 2: Metadata lookup for filtering
	placeholders := make([]string, len(hits))
	args := make([]any, len(hits))
	for i, h := range hits {
		placeholders[i] = "?"
		args[i] = h.id
	}

	metaSQL := `SELECT CAST(id AS TEXT), COALESCE(project, ''), category, created_at FROM learnings WHERE id IN (` + strings.Join(placeholders, ",") + `) AND quarantined_at IS NULL`
	metaRows, err := s.readerDB().QueryContext(ctx, metaSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search learnings meta: %w", err)
	}

	type meta struct {
		project   string
		category  string
		createdAt string
	}
	metaMap := make(map[string]meta, len(hits))
	for metaRows.Next() {
		var id, proj, cat, created string
		if metaRows.Scan(&id, &proj, &cat, &created) == nil {
			metaMap[id] = meta{project: proj, category: cat, createdAt: created}
		}
	}
	metaRows.Close()

	// Step 3: Merge and filter
	var results []LearningSearchResult
	for _, h := range hits {
		m, ok := metaMap[h.id]
		if !ok {
			continue
		}
		score := h.score
		switch m.category {
		case "narrative":
			score *= 0.4
		case "unfinished":
			score *= 0.7
		}
		if project != "" && m.project != "" && !models.ProjectMatches(m.project, project) {
			continue
		}
		if since != "" && m.createdAt < since {
			continue
		}
		if before != "" && m.createdAt >= before {
			continue
		}
		results = append(results, LearningSearchResult{
			ID:      h.id,
			Content: h.content,
			Score:   score,
			Project: m.project,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

// FindLearningsByEntityMatch finds active learnings whose entities match the given values.
// Uses the learning_entities junction table for lookup, returns full Learning structs via scanLearnings.
func (s *Store) FindLearningsByEntityMatch(entities []string, project string) ([]models.Learning, error) {
	if len(entities) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(entities))
	args := make([]any, len(entities))
	for i, e := range entities {
		placeholders[i] = "?"
		args[i] = e
	}

	query := `SELECT DISTINCT l.id, l.session_id, l.category, l.content, l.project, l.confidence,
		l.superseded_by, l.supersede_reason, l.created_at, l.expires_at, l.model_used, l.source,
		COALESCE(l.hit_count, 0), COALESCE(l.emotional_intensity, 0.0), l.last_hit_at, COALESCE(l.session_flavor, ''), l.valid_until, l.supersedes, COALESCE(l.importance, 3), l.supersede_status, COALESCE(l.noise_count, 0), COALESCE(l.fail_count, 0),
		COALESCE(l.match_count, 0), COALESCE(l.inject_count, 0), COALESCE(l.use_count, 0), COALESCE(l.save_count, 0), COALESCE(l.stability, 30.0),
		COALESCE(l.context, ''), COALESCE(l.domain, 'code'), COALESCE(l.trigger_rule, ''), COALESCE(l.embedding_text, ''),
		COALESCE(l.source_file, ''), COALESCE(l.source_hash, ''), COALESCE(l.doc_chunk_ref, 0), COALESCE(l.task_type, ''), COALESCE(l.turns_at_creation, 0), COALESCE(l.source_msg_from, -1), COALESCE(l.source_msg_to, -1)
		FROM learnings l
		JOIN learning_entities le ON le.learning_id = l.id
		WHERE le.value IN (` + strings.Join(placeholders, ",") + `)
		AND l.superseded_by IS NULL
		AND l.valid_until IS NULL`

	if project != "" {
		query += ` AND (l.project = ? OR l.project IS NULL OR l.project = '')`
		args = append(args, project)
	}

	query += ` ORDER BY l.created_at DESC LIMIT 20`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanLearnings(rows)
}

// SearchAnticipatedQueries searches the anticipated_queries_fts table (porter stemming).
// Returns parent learning content for matching AQs, deduplicated by learning_id.
func (s *Store) SearchAnticipatedQueries(query, project string, limit int) ([]LearningSearchResult, error) {
	words := splitWords(query)
	if len(words) < 2 {
		return nil, nil
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.ReplaceAll(w, `"`, "")
		if w == "" {
			continue
		}
		terms = append(terms, `"`+w+`"`)
	}
	for len(terms) >= 2 {
		ftsQuery := strings.Join(terms, " AND ")
		results, err := s.runAQFTSQuery(ftsQuery, project, limit)
		if err != nil {
			return nil, err
		}
		if len(results) > 0 {
			return results, nil
		}
		terms = terms[:len(terms)-1]
	}
	return nil, nil
}

func (s *Store) runAQFTSQuery(ftsQuery, project string, limit int) ([]LearningSearchResult, error) {
	rows, err := s.readerDB().Query(`
		SELECT aq.learning_id, l.content, bm25(anticipated_queries_fts) AS score, COALESCE(l.project, '')
		FROM anticipated_queries_fts aq
		JOIN learnings l ON l.id = aq.learning_id
		WHERE anticipated_queries_fts MATCH ?
		AND l.superseded_by IS NULL
		ORDER BY bm25(anticipated_queries_fts)
		LIMIT ?`, ftsQuery, limit*3)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[int64]bool)
	var results []LearningSearchResult
	for rows.Next() {
		var lid int64
		var content string
		var score float64
		var proj string
		if err := rows.Scan(&lid, &content, &score, &proj); err != nil {
			continue
		}
		if project != "" && proj != project {
			continue
		}
		if seen[lid] {
			continue
		}
		seen[lid] = true
		results = append(results, LearningSearchResult{
			ID:      fmt.Sprintf("%d", lid),
			Content: content,
			Score:   -score,
			Project: proj,
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}
