package claimtypes

import "strings"

// splitArrow separates the two halves of a marker body on the last arrow.
//
// A body without an arrow names no claim, so the caller skips it rather than
// guessing which half is the assertion.
func splitArrow(body string) (left, right string, ok bool) {
	ascii := strings.LastIndex(body, "->")
	unicode := strings.LastIndex(body, "\u2192")
	switch {
	case ascii < 0 && unicode < 0:
		return "", "", false
	case unicode >= 0 && unicode > ascii:
		return strings.TrimSpace(body[:unicode]), strings.TrimSpace(body[unicode+len("\u2192"):]), true
	default:
		return strings.TrimSpace(body[:ascii]), strings.TrimSpace(body[ascii+len("->"):]), true
	}
}

// trimQuotes removes the backticks a report may carry around one value.
func trimQuotes(value string) string {
	return strings.Trim(strings.TrimSpace(value), "`")
}
