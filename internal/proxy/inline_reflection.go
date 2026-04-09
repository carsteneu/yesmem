package proxy

import (
	"regexp"
	"strconv"
	"strings"
)

// InlineReflectionHint is appended to every learning injection point (Bohrhammer principle).
const InlineReflectionHint = "Learnings have [ID:xxx]. At the end of every response:\n<!-- [IDs: 123, 456] --> (used learning IDs)\n<!-- [gap: topic] --> (missing domain knowledge)\n<!-- [contradiction: ID1 vs ID2: description] --> (contradiction between learnings)\nFormat ALWAYS [ID:xxx] — not 'Learning #123' or 'Learning [123]'."

var (
	// idPattern matches [ID:123] style references in thinking blocks and free text.
	idPattern = regexp.MustCompile(`\[ID:(\d+)\]`)
	// learningRefPattern matches informal "Learning #123" or "Learning [123]" references.
	// Captures any digit sequence as ID — flexible since IDs are just DB integers.
	learningRefPattern = regexp.MustCompile(`[Ll]earning\s+[#\[](\d+)\]?`)
	// idsCommentPattern matches <!-- [IDs: 123, 456] --> style HTML comment summaries.
	idsCommentPattern = regexp.MustCompile(`<!--\s*\[IDs:\s*([\d,\s]+)\]\s*-->`)
	// gapPattern matches <!-- [gap: topic text] --> knowledge-gap annotations.
	gapPattern = regexp.MustCompile(`(?s)<!--\s*\[gap:\s*(.+?)\]\s*-->`)
	// contradictionPattern matches <!-- [contradiction: description] --> annotations.
	contradictionPattern = regexp.MustCompile(`(?s)<!--\s*\[contradiction:\s*(.+?)\]\s*-->`)
)

// InlineSignals holds all signals extracted from assistant messages.
type InlineSignals struct {
	UsedIDs        []int64
	GapTopics      []string
	Contradictions []string
	HasToolErrors  bool // true if last user message contains tool_result with is_error:true
}

// ScanAssistantSignals is the exported wrapper around scanAssistantSignals.
// It is used by the httpapi package to extract inline reflection signals
// without importing the proxy package internals.
func ScanAssistantSignals(messages []any) InlineSignals {
	return scanAssistantSignals(messages)
}

// scanAssistantSignals scans the last assistant message(s) in the message history
// for inline reflection signals. It looks at both thinking blocks and text blocks.
// It only scans assistant messages that come after the last user message.
func scanAssistantSignals(messages []any) InlineSignals {
	// Collect text from all assistant messages since the last user turn.
	var texts []string
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "user" {
			// Stop: only scan assistant messages after the last user message.
			break
		}
		if role != "assistant" {
			continue
		}
		content := msg["content"]
		switch c := content.(type) {
		case string:
			texts = append(texts, c)
		case []any:
			for _, block := range c {
				blk, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blk["type"].(string)
				switch blockType {
				case "thinking":
					if t, ok := blk["thinking"].(string); ok {
						texts = append(texts, t)
					}
				case "text":
					if t, ok := blk["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
		}
	}

	if len(texts) == 0 {
		return InlineSignals{}
	}

	// Deduplicate IDs via a seen map.
	seenIDs := make(map[int64]bool)
	var usedIDs []int64

	for _, text := range texts {
		// Extract [ID:xxx] patterns (thinking blocks, free text).
		for _, m := range idPattern.FindAllStringSubmatch(text, -1) {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				if !seenIDs[id] {
					seenIDs[id] = true
					usedIDs = append(usedIDs, id)
				}
			}
		}
		// Extract informal "Learning #123" or "Learning [123]" references.
		for _, m := range learningRefPattern.FindAllStringSubmatch(text, -1) {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				if !seenIDs[id] {
					seenIDs[id] = true
					usedIDs = append(usedIDs, id)
				}
			}
		}
		// Extract <!-- [IDs: 123, 456] --> patterns (HTML comment summaries).
		for _, m := range idsCommentPattern.FindAllStringSubmatch(text, -1) {
			for _, part := range strings.Split(m[1], ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				if id, err := strconv.ParseInt(part, 10, 64); err == nil {
					if !seenIDs[id] {
						seenIDs[id] = true
						usedIDs = append(usedIDs, id)
					}
				}
			}
		}
	}

	// Extract gap topics.
	var gapTopics []string
	for _, text := range texts {
		for _, m := range gapPattern.FindAllStringSubmatch(text, -1) {
			topic := strings.TrimSpace(m[1])
			if topic != "" {
				gapTopics = append(gapTopics, topic)
			}
		}
	}

	// Extract contradictions.
	var contradictions []string
	for _, text := range texts {
		for _, m := range contradictionPattern.FindAllStringSubmatch(text, -1) {
			desc := strings.TrimSpace(m[1])
			if desc != "" {
				contradictions = append(contradictions, desc)
			}
		}
	}

	return InlineSignals{
		UsedIDs:        usedIDs,
		GapTopics:      gapTopics,
		Contradictions: contradictions,
		HasToolErrors:  hasToolErrors(messages),
	}
}

// hasToolErrors checks the last user message for tool_result blocks with is_error:true.
// This detects whether Claude's previous action resulted in errors.
func hasToolErrors(messages []any) bool {
	// Find last user message (contains tool_results from previous assistant turn)
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] != "user" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			break
		}
		for _, block := range blocks {
			blk, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blk["type"] != "tool_result" {
				continue
			}
			if isErr, ok := blk["is_error"].(bool); ok && isErr {
				return true
			}
		}
		break // only check the last user message
	}
	return false
}
