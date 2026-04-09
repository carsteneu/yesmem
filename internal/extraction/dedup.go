package extraction

import (
	"strings"
	"unicode/utf8"
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
