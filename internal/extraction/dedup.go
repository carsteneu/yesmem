package extraction

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// IsSubstanzlos returns true if the content is too short, a JSON fragment,
// a code block, or an incomplete sentence fragment.
func IsSubstanzlos(content string) bool {
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) < 15 {
		return true
	}
	// JSON fragments
	if len(content) > 0 && (content[0] == '{' || content[0] == '[') {
		return true
	}
	// Code blocks
	if strings.HasPrefix(content, "```") {
		return true
	}
	// Sentence fragment: less than 4 words
	words := strings.Fields(content)
	if len(words) <= 3 {
		return true
	}
	return false
}

// BigramJaccard computes Jaccard similarity on word-level bigrams.
func BigramJaccard(a, b string) float64 {
	bigramsA := wordBigrams(strings.ToLower(a))
	bigramsB := wordBigrams(strings.ToLower(b))
	if len(bigramsA) == 0 && len(bigramsB) == 0 {
		return 1.0
	}
	intersection := 0
	for bg := range bigramsA {
		if bigramsB[bg] {
			intersection++
		}
	}
	union := len(bigramsA) + len(bigramsB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func wordBigrams(s string) map[string]bool {
	words := strings.Fields(s)
	bgs := make(map[string]bool)
	for i := 0; i < len(words)-1; i++ {
		bgs[words[i]+" "+words[i+1]] = true
	}
	return bgs
}

// findBigramDuplicates returns quality-ranked duplicate links while avoiding an
// all-pairs scan. For a set with n bigrams, any match above threshold must
// share more than threshold*n bigrams. Querying n-floor(threshold*n) rare
// bigrams is therefore exact: a matching set cannot avoid every queried token.
func findBigramDuplicates(learnings []models.Learning, threshold float64, afterID int64) (map[int64]int64, int) {
	type preparedLearning struct {
		learning models.Learning
		bigrams  map[string]bool
	}
	prepared := make([]preparedLearning, 0, len(learnings))
	frequencies := make(map[string]int)
	for _, learning := range learnings {
		bigrams := wordBigrams(strings.ToLower(learning.Content))
		prepared = append(prepared, preparedLearning{learning: learning, bigrams: bigrams})
		for bigram := range bigrams {
			frequencies[bigram]++
		}
	}

	postings := make(map[string][]int)
	for i, item := range prepared {
		for bigram := range item.bigrams {
			postings[bigram] = append(postings[bigram], i)
		}
	}

	groups := newDuplicateGroups(learnings)
	comparisons := 0
	for i, item := range prepared {
		if afterID > 0 && item.learning.ID <= afterID {
			continue
		}
		if len(item.bigrams) == 0 {
			continue
		}
		queryBigrams := make([]string, 0, len(item.bigrams))
		for bigram := range item.bigrams {
			queryBigrams = append(queryBigrams, bigram)
		}
		sort.Slice(queryBigrams, func(i, j int) bool {
			if frequencies[queryBigrams[i]] == frequencies[queryBigrams[j]] {
				return queryBigrams[i] < queryBigrams[j]
			}
			return frequencies[queryBigrams[i]] < frequencies[queryBigrams[j]]
		})
		queryBigrams = queryBigrams[:len(queryBigrams)-int(math.Floor(threshold*float64(len(queryBigrams))))]

		candidates := make(map[int]bool)
		for _, bigram := range queryBigrams {
			for _, candidate := range postings[bigram] {
				if candidate != i {
					candidates[candidate] = true
				}
			}
		}
		for candidate := range candidates {
			other := prepared[candidate]
			// Old rows never query, so a new/old pair appears once. For two
			// query-side rows, the lower ID owns the pair without a global seen map.
			if (afterID == 0 || other.learning.ID > afterID) && item.learning.ID > other.learning.ID {
				continue
			}

			shorter, longer := len(item.bigrams), len(other.bigrams)
			if shorter > longer {
				shorter, longer = longer, shorter
			}
			if float64(shorter)/float64(longer) <= threshold {
				continue
			}
			comparisons++
			intersection := 0
			for bigram := range item.bigrams {
				if other.bigrams[bigram] {
					intersection++
				}
			}
			union := len(item.bigrams) + len(other.bigrams) - intersection
			if union == 0 || float64(intersection)/float64(union) <= threshold {
				continue
			}
			groups.add(item.learning.ID, other.learning.ID)
		}
	}

	return groups.links(), comparisons
}

type duplicateGroups struct {
	learnings map[int64]models.Learning
	parent    map[int64]int64
	size      map[int64]int
	winner    map[int64]int64
}

func newDuplicateGroups(learnings []models.Learning) *duplicateGroups {
	groups := &duplicateGroups{
		learnings: make(map[int64]models.Learning, len(learnings)),
		parent:    make(map[int64]int64),
		size:      make(map[int64]int),
		winner:    make(map[int64]int64),
	}
	for _, learning := range learnings {
		groups.learnings[learning.ID] = learning
	}
	return groups
}

func (g *duplicateGroups) add(a, b int64) {
	rootA := g.root(a)
	rootB := g.root(b)
	if rootA == rootB {
		return
	}
	if g.size[rootA] < g.size[rootB] {
		rootA, rootB = rootB, rootA
	}
	g.parent[rootB] = rootA
	g.size[rootA] += g.size[rootB]
	winnerA := g.winner[rootA]
	winnerB := g.winner[rootB]
	if preferDuplicateWinner(g.learnings[winnerB], g.learnings[winnerA]) {
		g.winner[rootA] = winnerB
	}
	delete(g.size, rootB)
	delete(g.winner, rootB)
}

func (g *duplicateGroups) root(id int64) int64 {
	parent, exists := g.parent[id]
	if !exists {
		g.parent[id] = id
		g.size[id] = 1
		g.winner[id] = id
		return id
	}
	if parent == id {
		return id
	}
	g.parent[id] = g.root(parent)
	return g.parent[id]
}

// links chooses one winner for each connected duplicate group. The grouping is
// streamed as matches arrive, keeping memory linear even for dense duplicates.
func (g *duplicateGroups) links() map[int64]int64 {
	dupes := make(map[int64]int64)
	for id := range g.parent {
		winner := g.winner[g.root(id)]
		if id != winner {
			dupes[id] = winner
		}
	}
	return dupes
}

// preferDuplicateWinner is deliberately lexicographic: trust class, explicit
// provenance, confidence, content detail, then freshness as the final tie-breaker.
func preferDuplicateWinner(a, b models.Learning) bool {
	trustA := storage.ClassifyTrust(storage.TrustScore(&a))
	trustB := storage.ClassifyTrust(storage.TrustScore(&b))
	if trustA != trustB {
		return trustA > trustB
	}
	if sourceA, sourceB := explicitSourceRank(a.Source), explicitSourceRank(b.Source); sourceA != sourceB {
		return sourceA > sourceB
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	uniqueA, wordsA, lengthA := contentDetail(a.Content)
	uniqueB, wordsB, lengthB := contentDetail(b.Content)
	if uniqueA != uniqueB {
		return uniqueA > uniqueB
	}
	if wordsA != wordsB {
		return wordsA > wordsB
	}
	if lengthA != lengthB {
		return lengthA > lengthB
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

func explicitSourceRank(source string) int {
	switch source {
	case "user_stated":
		return 2
	case "agreed_upon":
		return 1
	default:
		return 0
	}
}

func contentDetail(content string) (int, int, int) {
	words := strings.Fields(strings.ToLower(content))
	unique := make(map[string]bool, len(words))
	for _, word := range words {
		unique[word] = true
	}
	return len(unique), len(words), utf8.RuneCountInString(strings.TrimSpace(content))
}
