package proxy

import "strings"

// ensureSystemArray converts req["system"] from string to array format if needed.
// Creates an empty array when system is missing so callers can inject blocks
// into requests that started without a system prompt.
func ensureSystemArray(req map[string]any) []any {
	sys, ok := req["system"]
	if !ok {
		arr := []any{}
		req["system"] = arr
		return arr
	}
	switch v := sys.(type) {
	case string:
		arr := []any{map[string]any{"type": "text", "text": v}}
		req["system"] = arr
		return arr
	case []any:
		return v
	}
	arr := []any{}
	req["system"] = arr
	return arr
}

// AppendSystemBlock adds a tagged text block to the system array.
func AppendSystemBlock(req map[string]any, tag, content string) {
	blocks := ensureSystemArray(req)
	if blocks == nil {
		return
	}
	block := map[string]any{
		"type": "text",
		"text": "[" + tag + "]\n" + content,
	}
	req["system"] = append(blocks, block)
}

// AppendSystemBlockCached adds a tagged text block with cache_control: ephemeral.
func AppendSystemBlockCached(req map[string]any, tag, content string) {
	blocks := ensureSystemArray(req)
	if blocks == nil {
		return
	}
	block := map[string]any{
		"type":          "text",
		"text":          "[" + tag + "]\n" + content,
		"cache_control": cacheControlBlock(),
	}
	req["system"] = append(blocks, block)
}

// UpsertSystemBlockCached appends or replaces a tagged system block.
// It adds cache_control only when the request still has breakpoint budget.
// Existing cache_control on a matching block is preserved.
func UpsertSystemBlockCached(req map[string]any, tag, content string) {
	blocks := ensureSystemArray(req)
	if blocks == nil {
		return
	}

	prefix := "[" + tag + "]"
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		if strings.HasPrefix(text, prefix) {
			bm["text"] = prefix + "\n" + content
			if _, has := bm["cache_control"]; !has && countCacheBreakpoints(req) < maxCacheBreakpoints {
				bm["cache_control"] = cacheControlBlock()
			}
			blocks[i] = bm
			req["system"] = blocks
			return
		}
	}

	block := map[string]any{
		"type": "text",
		"text": prefix + "\n" + content,
	}
	if countCacheBreakpoints(req) < maxCacheBreakpoints {
		block["cache_control"] = cacheControlBlock()
	}
	req["system"] = append(blocks, block)
}

// ReplaceSystemBlock finds a block tagged with [tag] and replaces its content.
// If no block with that tag exists, appends a new one.
// Preserves cache_control if present on the existing block.
func ReplaceSystemBlock(req map[string]any, tag, content string) {
	blocks := ensureSystemArray(req)
	if blocks == nil {
		return
	}
	prefix := "[" + tag + "]"
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		if strings.HasPrefix(text, prefix) {
			bm["text"] = prefix + "\n" + content
			blocks[i] = bm
			req["system"] = blocks
			return
		}
	}
	// Not found — append
	AppendSystemBlock(req, tag, content)
}

// disclaimerPrefix is the CLAUDE.md subordination disclaimer injected by Claude Code.
// It tells the model to treat CLAUDE.md as optional/low-priority, undermining user instructions.
const disclaimerPrefix = "IMPORTANT: this context may or may not be relevant"

// stripDisclaimerFromText removes the disclaimer sentence (and the follow-up
// "You should not respond..." line) from a text string. Returns the cleaned
// text and whether anything was changed.
func stripDisclaimerFromText(text string) (string, bool) {
	idx := strings.Index(text, disclaimerPrefix)
	if idx < 0 {
		return text, false
	}
	stripped := text[:idx]
	end := strings.Index(text[idx:], "\n")
	if end >= 0 {
		rest := text[idx+end+1:]
		if strings.HasPrefix(strings.TrimSpace(rest), "You should not respond to this context") {
			end2 := strings.Index(rest, "\n")
			if end2 >= 0 {
				stripped += rest[end2+1:]
			}
		} else {
			stripped += rest
		}
	}
	return strings.TrimRight(stripped, " \t\n"), true
}

// StripCLAUDEMDDisclaimer removes the disclaimer that Claude Code injects to
// subordinate CLAUDE.md content. Scans both system blocks and user messages
// (where CLAUDE.md is typically injected inside <system-reminder> tags).
// Returns true if any modification was made.
func StripCLAUDEMDDisclaimer(req map[string]any) bool {
	modified := false

	// 1. Scan system blocks
	blocks := ensureSystemArray(req)
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		if cleaned, changed := stripDisclaimerFromText(text); changed {
			bm["text"] = cleaned
			blocks[i] = bm
			modified = true
		}
	}
	if modified {
		req["system"] = blocks
	}

	// 2. Scan messages (CLAUDE.md arrives as <system-reminder> in user messages)
	msgs, _ := req["messages"].([]any)
	for i, msg := range msgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if cleaned, changed := stripDisclaimerFromText(c); changed {
				m["content"] = cleaned
				msgs[i] = m
				modified = true
			}
		case []any:
			for j, block := range c {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				text, _ := bm["text"].(string)
				if cleaned, changed := stripDisclaimerFromText(text); changed {
					bm["text"] = cleaned
					c[j] = bm
					modified = true
				}
			}
		}
	}

	return modified
}
