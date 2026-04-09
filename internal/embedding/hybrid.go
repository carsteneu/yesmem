package embedding

import (
	"sort"
)

// RankedResult represents a search result with score and source tracking.
type RankedResult struct {
	ID            string
	Content       string
	Score         float64 // final 0-100 score
	OriginalScore float64 // raw score before normalization: cosine (0-1) for vector, negated BM25 for keyword
	Source        string  // "keyword", "semantic", or "hybrid"
	Project       string  // originating project (empty = global)
}

// RRFMerge combines BM25 and vector search results using Reciprocal Rank Fusion.
// k is the RRF constant (typically 60). limit caps the number of returned results.
// RRF_score(d) = Σ 1/(k + rank_i(d))
func RRFMerge(bm25, vector []RankedResult, k int, limit int) []RankedResult {
	if len(bm25) == 0 && len(vector) == 0 {
		return nil
	}

	type docInfo struct {
		rrfScore      float64
		originalVec   float64 // cosine similarity (0-1), best across ranks
		originalBM25  float64 // negated BM25 score (higher=better), best across ranks
		content       string
		project       string
		inBM25        bool
		inVec         bool
	}

	docs := make(map[string]*docInfo)

	// Add BM25 results with RRF scores
	for rank, r := range bm25 {
		info, ok := docs[r.ID]
		if !ok {
			info = &docInfo{content: r.Content, project: r.Project}
			docs[r.ID] = info
		}
		info.rrfScore += 1.0 / float64(k+rank+1)
		if r.OriginalScore > info.originalBM25 {
			info.originalBM25 = r.OriginalScore
		}
		info.inBM25 = true
	}

	// Add vector results with RRF scores
	for rank, r := range vector {
		info, ok := docs[r.ID]
		if !ok {
			info = &docInfo{content: r.Content, project: r.Project}
			docs[r.ID] = info
		}
		info.rrfScore += 1.0 / float64(k+rank+1)
		if r.OriginalScore > info.originalVec {
			info.originalVec = r.OriginalScore
		}
		info.inVec = true
	}

	// Convert to slice, assign source and final score
	// Scoring strategy:
	// - semantic/hybrid: cosine * 100 (direct semantic relevance, 0-100)
	// - keyword-only: BM25 normalized relative to top BM25 score, capped at 60
	//   (keyword match is less reliable than semantic similarity)
	var topBM25 float64
	for _, info := range docs {
		if info.inBM25 && !info.inVec && info.originalBM25 > topBM25 {
			topBM25 = info.originalBM25
		}
	}

	results := make([]RankedResult, 0, len(docs))
	for id, info := range docs {
		source := "keyword"
		if info.inBM25 && info.inVec {
			source = "hybrid"
		} else if info.inVec {
			source = "semantic"
		}

		var finalScore float64
		switch source {
		case "semantic":
			finalScore = info.originalVec * 100
		case "hybrid":
			// Use cosine as primary signal, small RRF boost for appearing in both lists
			finalScore = info.originalVec*100 + 5
			if finalScore > 100 {
				finalScore = 100
			}
		case "keyword":
			// Score is pre-normalized in SearchLearningsBM25Ctx by tier:
			// 100% terms match → up to 100, 66% → up to 60, 33% → up to 40
			finalScore = info.originalBM25
		}

		results = append(results, RankedResult{
			ID:            id,
			Content:       info.content,
			Score:         finalScore,
			OriginalScore: info.originalVec,
			Source:        source,
			Project:       info.project,
		})
	}

	// Sort by final score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}
