package briefing

import (
	"sort"
	"strings"
	"unicode"

	"github.com/bbalet/stopwords"
	"github.com/carsteneu/yesmem/internal/models"
)

// Deduplicate removes near-duplicate learnings using hybrid similarity
// (max of Jaccard and containment) on normalized word tokens. Keeps the
// newest formulation when duplicates are found.
func Deduplicate(learnings []models.Learning, threshold float64) []models.Learning {
	if threshold <= 0 {
		threshold = 0.4
	}
	if len(learnings) <= 1 {
		return learnings
	}

	// Sort by CreatedAt DESC — newest first wins
	sorted := make([]models.Learning, len(learnings))
	copy(sorted, learnings)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	var result []models.Learning
	var keptTokens [][]string

	for _, l := range sorted {
		tokens := normalize(l.Content)
		if len(tokens) == 0 {
			result = append(result, l)
			continue
		}

		isDuplicate := false
		for _, existing := range keptTokens {
			if similarity(tokens, existing) >= threshold {
				isDuplicate = true
				break
			}
		}

		if !isDuplicate {
			result = append(result, l)
			keptTokens = append(keptTokens, tokens)
		}
	}

	return result
}

// DefaultLanguages used for stop-word filtering when none configured.
var DefaultLanguages = []string{"de", "en"}

// Languages is the active list used by normalize(). Set via SetLanguages().
var activeLanguages = DefaultLanguages

// SetLanguages configures which languages are used for stop-word filtering.
func SetLanguages(langs []string) {
	if len(langs) > 0 {
		activeLanguages = langs
	}
}

// GetLanguages returns the active stop-word languages.
func GetLanguages() []string {
	return activeLanguages
}

// normalize lowercases text, removes stop-words for configured languages
// (via bbalet/stopwords), strips punctuation (keeping technical tokens
// like CGO_ENABLED=0), and splits on whitespace.
func normalize(s string) []string {
	lower := strings.ToLower(s)

	// Remove stop words for configured languages only
	cleaned := lower
	for _, lang := range activeLanguages {
		cleaned = stopwords.CleanString(cleaned, lang, false)
	}

	// Replace punctuation with space, but keep = _ - / . (technical tokens)
	cleaned = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '=' || r == '_' || r == '-' || r == '/' || r == '.' {
			return r
		}
		return ' '
	}, cleaned)

	words := strings.Fields(cleaned)

	var tokens []string
	for _, w := range words {
		if len(w) <= 1 {
			continue
		}
		tokens = append(tokens, w)
	}

	return tokens
}

// similarity computes a hybrid metric: max(jaccard, containment).
// Containment catches cases where a shorter phrase is mostly contained
// in a longer one (e.g. "CGO_ENABLED=0 setzen" inside a longer explanation).
func similarity(a, b []string) float64 {
	j := jaccard(a, b)

	// Containment: fraction of the shorter set found in the longer
	shorter, longer := a, b
	if len(a) > len(b) {
		shorter, longer = b, a
	}
	setLonger := make(map[string]bool, len(longer))
	for _, t := range longer {
		setLonger[t] = true
	}
	shared := 0
	for _, t := range shorter {
		if setLonger[t] {
			shared++
		}
	}
	c := float64(0)
	if len(shorter) > 0 {
		c = float64(shared) / float64(len(shorter))
	}

	if c > j {
		return c
	}
	return j
}

// jaccard computes the Jaccard similarity coefficient between two token sets.
// Returns 0.0 for disjoint sets, 1.0 for identical sets.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}

	setB := make(map[string]bool, len(b))
	for _, t := range b {
		setB[t] = true
	}

	intersection := 0
	for t := range setA {
		if setB[t] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// Stop words are handled by github.com/bbalet/stopwords (24 languages).
