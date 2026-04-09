
package locomo

import "strings"

// TieredConfig controls the multi-tool search strategy.
type TieredConfig struct {
	MinResults     int     // minimum good results before stopping (default: 3)
	ScoreThreshold float64 // minimum score to count as "good" (default: 0.3)
	TopK           int     // results per tier (default: 10)
}

// DefaultTieredConfig returns sensible defaults for tiered search.
func DefaultTieredConfig() TieredConfig {
	return TieredConfig{MinResults: 3, ScoreThreshold: 0.3, TopK: 10}
}

// TieredSearch executes a multi-tool search strategy, escalating through tiers.
func TieredSearch(dc *DaemonClient, question, project string, cfg TieredConfig) []SearchResult {
	// Tier 1: hybrid_search (primary)
	results, _ := dc.HybridSearch(question, project, cfg.TopK)
	if countGood(results, cfg.ScoreThreshold) >= cfg.MinResults {
		return results
	}

	// Tier 2: deep_search (thinking blocks, command outputs)
	deep, _ := dc.DeepSearch(question, project, cfg.TopK)
	results = mergeDedup(results, deep)
	if countGood(results, cfg.ScoreThreshold) >= cfg.MinResults {
		return results
	}

	// Tier 3: keyword search with reformulated query
	keywords := extractKeywords(question)
	if keywords != question {
		kw, _ := dc.Search(keywords, project, cfg.TopK)
		results = mergeDedup(results, kw)
	}

	return results
}

// countGood counts results above the score threshold.
func countGood(results []SearchResult, threshold float64) int {
	n := 0
	for _, r := range results {
		if r.Score >= threshold {
			n++
		}
	}
	return n
}

// mergeDedup merges two result sets, removing near-duplicates by content similarity.
func mergeDedup(existing, additions []SearchResult) []SearchResult {
	for _, add := range additions {
		isDup := false
		for _, ex := range existing {
			if bigramJaccard(ex.Content, add.Content) > 0.7 {
				isDup = true
				break
			}
		}
		if !isDup {
			existing = append(existing, add)
		}
	}
	return existing
}

// bigramJaccard computes bigram Jaccard similarity between two strings.
func bigramJaccard(a, b string) float64 {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	setBigrams := func(s string) map[string]bool {
		s = strings.ToLower(s)
		m := map[string]bool{}
		for i := 0; i < len(s)-1; i++ {
			m[s[i:i+2]] = true
		}
		return m
	}
	ba, bb := setBigrams(a), setBigrams(b)
	inter, union := 0, 0
	for k := range ba {
		if bb[k] {
			inter++
		}
		union++
	}
	for k := range bb {
		if !ba[k] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// extractKeywords strips common question words, returns key nouns/terms.
func extractKeywords(question string) string {
	stopwords := map[string]bool{
		"what": true, "when": true, "where": true, "who": true, "how": true,
		"did": true, "does": true, "do": true, "is": true, "was": true, "were": true,
		"are": true, "the": true, "a": true, "an": true, "in": true, "on": true,
		"at": true, "to": true, "for": true, "of": true, "with": true, "by": true,
		"about": true, "has": true, "have": true, "had": true, "that": true, "this": true,
	}
	words := strings.Fields(strings.ToLower(question))
	var kept []string
	for _, w := range words {
		w = strings.Trim(w, "?.,!\"'")
		if !stopwords[w] && len(w) > 1 {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return question
	}
	return strings.Join(kept, " ")
}
