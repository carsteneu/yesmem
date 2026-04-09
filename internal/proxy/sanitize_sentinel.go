package proxy

import (
	"regexp"
	"sync"
)

// sentinelPattern matches billing sentinel hashes in message content.
// CC's Bun fork replaces the first `cch=XXXXX` in the serialized JSON body.
// If messages contain this pattern (e.g., user discussing CC internals),
// the wrong occurrence gets replaced → message content varies per request → cache miss.
// Pattern: cch= followed by 5-10 hex chars (covers both short hashes and raw sentinels like d6c5c00000).
var sentinelOnce sync.Once
var sentinelRe *regexp.Regexp

func getSentinelRegex() *regexp.Regexp {
	sentinelOnce.Do(func() {
		sentinelRe = regexp.MustCompile(`cch=[0-9a-f]{5,10}`)
	})
	return sentinelRe
}

// SanitizeBillingSentinel normalizes billing sentinel patterns in message content.
// Replaces all cch=[hex] occurrences in text blocks with a fixed value (cch=XXXXX)
// so the cache prefix stays stable even when CC's Bun fork corrupts a sentinel in messages.
// Only touches "text" type content blocks — tool_use, tool_result, images are untouched.
// Returns true if any content was modified.
func SanitizeBillingSentinel(messages []any) bool {
	re := getSentinelRegex()
	changed := false

	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			changed = sanitizeBlock(b, re) || changed
		}
	}
	return changed
}

// sanitizeBlock replaces cch= sentinel patterns in a single content block.
// Handles text blocks (text field), tool_result blocks (string or nested content),
// and skips tool_use/image blocks.
func sanitizeBlock(b map[string]any, re *regexp.Regexp) bool {
	blockType, _ := b["type"].(string)
	changed := false

	switch blockType {
	case "text":
		text, ok := b["text"].(string)
		if !ok {
			return false
		}
		replaced := re.ReplaceAllString(text, "cch=XXXXX")
		if replaced != text {
			b["text"] = replaced
			changed = true
		}

	case "tool_result":
		switch c := b["content"].(type) {
		case string:
			replaced := re.ReplaceAllString(c, "cch=XXXXX")
			if replaced != c {
				b["content"] = replaced
				changed = true
			}
		case []any:
			for _, nested := range c {
				nb, ok := nested.(map[string]any)
				if !ok {
					continue
				}
				changed = sanitizeBlock(nb, re) || changed
			}
		}
	}

	return changed
}
