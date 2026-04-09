package proxy

import (
	"regexp"
	"strings"
	"sync"
)

// billingHashPattern matches the cch= hash in the billing header.
// Covers both short hashes (cch=627d9) and raw sentinels (cch=d6c5c00000).
var billingOnce sync.Once
var billingRe *regexp.Regexp

func getBillingRegex() *regexp.Regexp {
	billingOnce.Do(func() {
		billingRe = regexp.MustCompile(`cch=[0-9a-f]{5,10}`)
	})
	return billingRe
}

// NormalizeBillingHeader replaces the volatile cch= hash in system[0] with a fixed value.
// On --resume, CC computes a different hash because messages[0] content differs from fresh sessions.
// By normalizing system[0], the cache prefix stays stable across fresh/resume boundaries.
// Only touches system[0] and only if it contains the billing header marker.
// Returns true if the header was modified.
func NormalizeBillingHeader(req map[string]any) bool {
	sys, ok := req["system"].([]any)
	if !ok || len(sys) == 0 {
		return false
	}

	block, ok := sys[0].(map[string]any)
	if !ok {
		return false
	}

	text, ok := block["text"].(string)
	if !ok {
		return false
	}

	// Only touch the billing header block
	if !strings.Contains(text, "x-anthropic-billing-header") {
		return false
	}

	re := getBillingRegex()
	replaced := re.ReplaceAllString(text, "cch=00000")
	if replaced == text {
		return false
	}

	block["text"] = replaced
	return true
}
