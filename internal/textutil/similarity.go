package textutil

import (
	"strings"
	"unicode"
)

// Tokenize splits text into lowercase tokens for similarity comparison.
func Tokenize(s string) []string {
	lower := strings.ToLower(s)
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/' || r == '.' {
			return r
		}
		return ' '
	}, lower)
	words := strings.Fields(normalized)
	var tokens []string
	for _, w := range words {
		if len(w) > 1 {
			tokens = append(tokens, w)
		}
	}
	return tokens
}

// TokenSimilarity computes containment similarity (fraction of shorter set found in longer).
func TokenSimilarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shorter, longer := a, b
	if len(a) > len(b) {
		shorter, longer = b, a
	}
	longSet := make(map[string]bool, len(longer))
	for _, t := range longer {
		longSet[t] = true
	}
	shared := 0
	for _, t := range shorter {
		if longSet[t] {
			shared++
		}
	}
	return float64(shared) / float64(len(shorter))
}
