package extraction

import (
	"strconv"
	"strings"
)

// looksLikePaste returns true if the content is copy-pasted output with no extraction value.
func looksLikePaste(s string) bool {
	if len(s) == 0 {
		return false
	}
	head := s
	if len(head) > 500 {
		head = head[:500]
	}

	// HTML content
	if strings.Contains(head, "<h1>") || strings.Contains(head, "<div") || strings.Contains(head, "<html") {
		return true
	}
	// Stdout captures
	if strings.HasPrefix(s, "<local-command-stdout>") {
		return true
	}
	// Code blocks (triple backtick at start)
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```") {
		return true
	}
	// Repetitive lines (logs, stack traces): >50% of lines share a 10-char prefix
	lines := strings.SplitN(s, "\n", 30)
	if len(lines) > 10 {
		prefixes := map[string]int{}
		counted := 0
		for _, l := range lines {
			if len(l) > 10 {
				prefixes[l[:10]]++
				counted++
			}
		}
		for _, count := range prefixes {
			if counted > 0 && count > counted/2 {
				return true
			}
		}
	}
	return false
}

// looksLikePlan returns true if the content is a structured plan or architecture doc.
func looksLikePlan(s string) bool {
	head := s
	if len(head) > 2000 {
		head = head[:2000]
	}
	headLower := strings.ToLower(head)
	return strings.Contains(headLower, "implementation plan") ||
		strings.Contains(headLower, "### task") ||
		strings.Contains(headLower, "## step")
}

// keepFirstAndLast returns the first n chars and last m chars of s,
// joined with a separator indicating content was removed.
func keepFirstAndLast(s string, firstN, lastN int) string {
	if len(s) <= firstN+lastN {
		return s
	}
	removed := (len(s) - firstN - lastN) / 1000
	return s[:firstN] + "\n... (" + strconv.Itoa(removed) + "K chars truncated) ...\n" + s[len(s)-lastN:]
}

// truncateText applies content-aware truncation.
// Only called for content > 1500 chars.
func truncateText(content, role string) string {
	// Plans/architecture → keep decision (first) + summary (last)
	// Check BEFORE paste detection — plans have repetitive lines but are semantically valuable
	if role == "assistant" && looksLikePlan(content) {
		return keepFirstAndLast(content, 1000, 500)
	}
	// Pasted content (HTML, logs, stdout, code blocks) → aggressive
	if looksLikePaste(content) {
		if len(content) > 1000 {
			return content[:1000] + "... (truncated paste)"
		}
		return content
	}
	// Natural text — role-based limits
	if role == "assistant" && len(content) > 1500 {
		return content[:1500] + "... (truncated)"
	}
	if role == "user" && len(content) > 3000 {
		return content[:3000] + "... (truncated)"
	}
	return content
}
