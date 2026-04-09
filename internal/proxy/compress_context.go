package proxy

import (
	"fmt"
	"strings"
)

// CompressResult holds the outcome of context compression.
type CompressResult struct {
	Messages             []any
	ThinkingCompressed   int
	ToolResultsCompressed int
	TokensSaved          int
}

// turnAge constants for compression thresholds.
const (
	compressMinTokens = 500 // minimum tokens to consider for compression
)

// CompressContext proactively compresses old thinking blocks and tool_results
// before the budget-based cutoff runs. This recovers context window space from
// content that has been processed and summarized in assistant responses.
//
// Messages within the keepRecent window are never touched.
// All older messages get thinking blocks removed and tool_results summarized.
//
// Only blocks > 500 tokens are compressed. Messages are modified in-place.
func CompressContext(messages []any, keepRecent int, threadID string, estimateTokens TokenEstimateFunc) CompressResult {
	result := CompressResult{
		Messages: messages,
	}

	if len(messages) < 4 {
		return result
	}

	// Build tool_use_id → {name, keywords} map for summary generation
	toolInfo := buildToolUseInfoExtended(messages)

	// Calculate turn boundaries.
	// A "turn" is roughly a user+assistant message pair.
	// Count from the end to determine age.
	totalMsgs := len(messages)

	// Calculate protected tail: messages within keepRecent window stay untouched
	protectedTail := totalMsgs - keepRecent
	if protectedTail < 1 {
		protectedTail = 1 // always skip system message at index 0
	}

	for i := 1; i < protectedTail; i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		content := msg["content"]

		blocks, ok := content.([]any)
		if !ok {
			continue // string content — skip
		}

		modified := false
		newBlocks := make([]any, 0, len(blocks))

		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				newBlocks = append(newBlocks, block)
				continue
			}

			blockType, _ := b["type"].(string)

			switch blockType {
			case "thinking":
				thinking, _ := b["thinking"].(string)
				tokens := estimateTokens(thinking)
				if tokens < compressMinTokens {
					newBlocks = append(newBlocks, block)
					continue
				}

				// All messages outside keepRecent: summary stub
				compressed := "[context compressed: thinking block]"
				newBlocks = append(newBlocks, map[string]any{
					"type":     "thinking",
					"thinking": compressed,
				})
				result.TokensSaved += tokens - estimateTokens(compressed)
				result.ThinkingCompressed++
				modified = true

			case "tool_result":
				// tool_result content can be string or nested blocks
				resultText := extractToolResultText(b)
				tokens := estimateTokens(resultText)
				if tokens < compressMinTokens {
					newBlocks = append(newBlocks, block)
					continue
				}

				toolUseID, _ := b["tool_use_id"].(string)
				info := toolInfo[toolUseID]

				// All messages outside keepRecent: summary stub with deep_search hint
				summary := buildToolResultSummary(resultText, info, i, threadID)
				newBlock := shallowCopyMap(b)
				newBlock["content"] = summary
				newBlocks = append(newBlocks, newBlock)
				result.TokensSaved += tokens - estimateTokens(summary)
				result.ToolResultsCompressed++
				modified = true

			default:
				newBlocks = append(newBlocks, block)
			}
		}

		if modified {
			newMsg := shallowCopyMap(msg)
			newMsg["content"] = newBlocks
			messages[i] = newMsg
			_ = role // used for potential role-specific logic later
		}
	}

	result.Messages = messages
	return result
}

// toolUseInfoExtended holds tool name and keywords for summary generation.
type toolUseInfoExtended struct {
	Name     string
	Keywords string
}

// buildToolUseInfoExtended builds tool_use_id → {name, keywords} map.
func buildToolUseInfoExtended(messages []any) map[string]toolUseInfoExtended {
	info := make(map[string]toolUseInfoExtended)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] != "tool_use" {
				continue
			}
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input, _ := b["input"].(map[string]any)
			info[id] = toolUseInfoExtended{
				Name:     name,
				Keywords: extractToolKeywords(name, input),
			}
		}
	}
	return info
}

// extractToolResultText extracts text from a tool_result's content field.
func extractToolResultText(block map[string]any) string {
	content := block["content"]
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					sb.WriteString(text)
					sb.WriteByte('\n')
				}
			}
		}
		return sb.String()
	}
	return ""
}

// truncateMiddle keeps the first headChars and last tailChars, with a marker in between.
func truncateMiddle(text string, headChars, tailChars int) string {
	runes := []rune(text)
	if len(runes) <= headChars+tailChars+50 {
		return text // not worth truncating
	}
	head := string(runes[:headChars])
	tail := string(runes[len(runes)-tailChars:])
	dropped := len(runes) - headChars - tailChars
	return fmt.Sprintf("%s\n[...%d chars compressed...]\n%s", head, dropped, tail)
}

// truncateToolResult truncates a tool_result keeping head and tail with context.
func truncateToolResult(text string, info toolUseInfoExtended) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= 30 {
		return text // small enough to keep
	}

	var sb strings.Builder
	// Keep first 15 lines
	for i := 0; i < 15 && i < len(lines); i++ {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	sb.WriteString(fmt.Sprintf("\n[...%d lines compressed...]\n\n", len(lines)-25))
	// Keep last 10 lines
	start := len(lines) - 10
	if start < 15 {
		start = 15
	}
	for i := start; i < len(lines); i++ {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	return sb.String()
}

// buildToolResultSummary creates a summary stub for a tool_result.
func buildToolResultSummary(text string, info toolUseInfoExtended, msgIdx int, threadID string) string {
	lines := strings.Split(text, "\n")
	lineCount := len(lines)

	// Extract some structure from the content
	var hints []string

	// For code: look for func/class/type definitions
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") ||
			strings.HasPrefix(trimmed, "type ") ||
			strings.HasPrefix(trimmed, "class ") ||
			strings.HasPrefix(trimmed, "def ") ||
			strings.HasPrefix(trimmed, "export ") {
			sig := truncateStr(trimmed, 60)
			hints = append(hints, sig)
			if len(hints) >= 5 {
				break
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[context compressed: %s, %d lines", info.Name, lineCount))
	if len(hints) > 0 {
		sb.WriteString(", key: ")
		sb.WriteString(strings.Join(hints, "; "))
	}
	sb.WriteString("]")
	if threadID != "" {
		sb.WriteString(fmt.Sprintf(" → get_session('%s', mode=paginated, offset=%d, limit=1)", threadID, msgIdx))
	} else if info.Keywords != "" {
		sb.WriteString(fmt.Sprintf(" → deep_search('%s')", info.Keywords))
	}
	return sb.String()
}

// shallowCopyMap creates a shallow copy of a map.
func shallowCopyMap(m map[string]any) map[string]any {
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
