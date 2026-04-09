package proxy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type stubMatch struct {
	msgIdx     int
	stubText   string
	searchHint string
	score      int
}

// reexpandStubs checks if any stubs match the user's current query and
// temporarily replaces them with original content from search.
// Max 3 stubs, max 25% of token threshold.
func (s *Server) reexpandStubsFor(messages []any, threshold int, userQuery string) []any {
	if len(userQuery) < 10 {
		return messages
	}

	queryWords := significantWords(userQuery)
	var matches []stubMatch

	// Scan for stubs with deep_search hints
	for i, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		blocks, ok := m["content"].([]any)
		if !ok {
			// String content — check if it's a stub
			if text, ok := m["content"].(string); ok {
				if hint := extractSearchHint(text); hint != "" {
					overlap := countOverlap(queryWords, significantWords(hint))
					if overlap >= 2 {
						matches = append(matches, stubMatch{i, text, hint, overlap})
					}
				}
			}
			continue
		}

		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			text, _ := b["text"].(string)
			hint := extractSearchHint(text)
			if hint == "" {
				continue
			}
			overlap := countOverlap(queryWords, significantWords(hint))
			if overlap >= 2 {
				matches = append(matches, stubMatch{i, text, hint, overlap})
			}
		}
	}

	if len(matches) == 0 {
		s.logger.Printf("  re-expand: no stub matches for query %q", truncateStr(userQuery, 60))
		return messages
	}

	// Sort by score descending, take top 3
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	if len(matches) > 3 {
		matches = matches[:3]
	}
	s.logger.Printf("  re-expand: %d candidates (top: %s, overlap=%d)",
		len(matches), truncateStr(matches[0].searchHint, 40), matches[0].score)

	// Query search for each match, respecting re-expansion budget (Task #14)
	budget := NewBudget(threshold)

	for _, match := range matches {
		result, err := s.queryDaemon("search", map[string]any{
			"query": match.searchHint,
			"limit": float64(1),
		})
		if err != nil {
			s.logger.Printf("  re-expand: search failed for %q: %v", truncateStr(match.searchHint, 40), err)
			continue
		}

		content := extractFirstSearchContent(result)
		if content == "" {
			s.logger.Printf("  re-expand: no content found for %q", truncateStr(match.searchHint, 40))
			continue
		}
		if len(content) <= len(match.stubText) {
			s.logger.Printf("  re-expand: content too short (%d <= %d) for %q", len(content), len(match.stubText), truncateStr(match.searchHint, 40))
			continue
		}

		// Truncate to reasonable size
		if len([]rune(content)) > 2000 {
			content = string([]rune(content)[:2000]) + "..."
		}

		contentTokens := s.countTokens(content)
		if !budget.CanSpendReExpansion(contentTokens) {
			s.logger.Printf("  re-expand: budget exceeded for %q (%d tokens)", truncateStr(match.searchHint, 40), contentTokens)
			continue
		}
		budget.SpendReExpansion(contentTokens)

		messages = replaceStubInMessage(messages, match.msgIdx, match.stubText,
			fmt.Sprintf("[re-expanded from archive]\n%s", content))

		s.logger.Printf("re-expanded stub #%d (%s) for this request (%d tokens)",
			match.msgIdx, truncateStr(match.searchHint, 40), contentTokens)
	}

	return messages
}

// extractSearchHint extracts the query from a deep_search('...') hint in a stub.
func extractSearchHint(stub string) string {
	idx := strings.Index(stub, "deep_search('")
	if idx < 0 {
		return ""
	}
	start := idx + len("deep_search('")
	end := strings.Index(stub[start:], "')")
	if end < 0 {
		return ""
	}
	return stub[start : start+end]
}

// extractFirstSearchContent gets the best content from a search result.
func extractFirstSearchContent(raw json.RawMessage) string {
	results := parseSearchResults(raw)
	if len(results) == 0 {
		return ""
	}
	return results[0]
}

// replaceStubInMessage replaces a specific stub text in a message's content blocks.
func replaceStubInMessage(messages []any, msgIdx int, oldStub, newContent string) []any {
	msg, ok := messages[msgIdx].(map[string]any)
	if !ok {
		return messages
	}

	// Handle string content
	if text, ok := msg["content"].(string); ok && text == oldStub {
		newMsg := make(map[string]any)
		for k, v := range msg {
			newMsg[k] = v
		}
		newMsg["content"] = newContent
		messages[msgIdx] = newMsg
		return messages
	}

	// Handle block content
	blocks, ok := msg["content"].([]any)
	if !ok {
		return messages
	}

	newBlocks := make([]any, len(blocks))
	replaced := false
	for i, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			newBlocks[i] = block
			continue
		}
		text, _ := b["text"].(string)
		if text == oldStub && !replaced {
			newBlocks[i] = map[string]any{
				"type": "text",
				"text": newContent,
			}
			replaced = true
		} else {
			newBlocks[i] = block
		}
	}

	newMsg := make(map[string]any)
	for k, v := range msg {
		newMsg[k] = v
	}
	newMsg["content"] = newBlocks
	messages[msgIdx] = newMsg
	return messages
}
