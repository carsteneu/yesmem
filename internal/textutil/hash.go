package textutil

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

var whitespaceRE = regexp.MustCompile(`\s+`)

// NormalizeForHash normalizes text for content hashing:
// lowercase, collapse all whitespace to single space, trim.
func NormalizeForHash(text string) string {
	lower := strings.ToLower(text)
	collapsed := whitespaceRE.ReplaceAllString(lower, " ")
	return strings.TrimSpace(collapsed)
}

// ContentHash returns the hex-encoded SHA-256 of the normalized text.
func ContentHash(text string) string {
	normalized := NormalizeForHash(text)
	h := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", h)
}
