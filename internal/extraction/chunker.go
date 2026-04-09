package extraction

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// Noise patterns to strip from text messages before extraction.
var noisePatterns = []*regexp.Regexp{
	// <system-reminder>...</system-reminder> — Claude Code hook injections, skill checks
	regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
	// [yesmem-briefing]...[/yesmem-briefing] — session briefing block
	regexp.MustCompile(`(?s)\[yesmem-briefing\].*?\[/yesmem-briefing\]`),
	// [yesmem associative context]...[/yesmem context] — associative injections
	regexp.MustCompile(`(?s)\[yesmem associative context\].*?\[/yesmem context\]`),
	// [self-prime]...(to end of line or next block)
	regexp.MustCompile(`(?s)\[self-prime\]\n?\[Self-Prime[^\]]*\]:[^\n]*(?:\n[^\[]*)*`),
	// [yesmem-context]...[/yesmem-context] or to end
	regexp.MustCompile(`(?s)\[yesmem-context\].*?(?:\[/yesmem-context\]|$)`),
	// [Metamemory: ...] blocks
	regexp.MustCompile(`(?s)\[Metamemory:.*?(?:\n\n|\z)`),
	// [task-reminder] lines
	regexp.MustCompile(`(?m)^\[task-reminder\].*$`),
}

// sanitizeStructuredData strips raw JSON, Go map output, and other structured
// data from text content. Claude 4.6 models reject requests containing embedded
// JSON from tool results. Also saves tokens.
func sanitizeStructuredData(s string) string {
	var cleaned []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip Go map output: [map[key:value ...]]
		if strings.HasPrefix(trimmed, "[map[") {
			continue
		}
		// Skip raw JSON objects: {"key":...}
		if len(trimmed) > 20 && strings.HasPrefix(trimmed, "{\"") && strings.HasSuffix(trimmed, "}") {
			continue
		}
		// Skip raw JSON arrays: [{"key":...}]
		if len(trimmed) > 20 && strings.HasPrefix(trimmed, "[{\"") {
			continue
		}
		// Skip lines that are mostly JSON-like (high density of "key": patterns)
		if len(trimmed) > 100 && strings.Count(trimmed, "\":") > 5 {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

// StripNoise removes injected system/hook/briefing blocks from text content.
// These blocks have zero extraction value but inflate token counts.
func StripNoise(s string) string {
	for _, re := range noisePatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = sanitizeStructuredData(s)
	// Clean up residual multiple spaces/newlines
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Chunk represents a portion of session content ready for LLM extraction.
type Chunk struct {
	Index       int
	Total       int
	Content     string
	PrevSummary string // Summary of previous chunks for context
	FromMsgIdx  int    // First message index in this chunk
	ToMsgIdx    int    // Last message index in this chunk
}

// ChunkMessages splits session messages into chunks of approximately maxTokens.
// Rough estimation: 1 token ≈ 4 characters.
func ChunkMessages(msgs []models.Message, maxTokens int) []Chunk {
	if maxTokens <= 0 {
		maxTokens = 50000
	}
	maxChars := maxTokens * 4

	// Build text for each message, preserving index mapping
	type indexedPart struct {
		text   string
		msgIdx int
	}
	var parts []indexedPart
	for i, m := range msgs {
		text := formatMessage(m)
		if text != "" {
			parts = append(parts, indexedPart{text: text, msgIdx: i})
		}
	}

	if len(parts) == 0 {
		return nil
	}

	// Build chunks by accumulating messages until maxChars
	var chunks []Chunk
	var currentTexts []string
	currentChars := 0
	firstMsgIdx := parts[0].msgIdx
	lastMsgIdx := parts[0].msgIdx

	for _, p := range parts {
		lineLen := len(p.text) + 1 // +1 for newline
		if currentChars+lineLen > maxChars && len(currentTexts) > 0 {
			// Flush current chunk
			chunks = append(chunks, Chunk{
				Index:      len(chunks),
				Content:    strings.Join(currentTexts, "\n"),
				FromMsgIdx: firstMsgIdx,
				ToMsgIdx:   lastMsgIdx,
			})
			currentTexts = nil
			currentChars = 0
			firstMsgIdx = p.msgIdx
		}
		currentTexts = append(currentTexts, p.text)
		currentChars += lineLen
		lastMsgIdx = p.msgIdx
	}

	// Flush remaining
	if len(currentTexts) > 0 {
		chunks = append(chunks, Chunk{
			Index:      len(chunks),
			Content:    strings.Join(currentTexts, "\n"),
			FromMsgIdx: firstMsgIdx,
			ToMsgIdx:   lastMsgIdx,
		})
	}

	// Set totals and generate prev summaries
	total := len(chunks)
	for i := range chunks {
		chunks[i].Total = total
		if i > 0 {
			chunks[i].PrevSummary = fmt.Sprintf(
				"Dies ist Teil %d/%d einer Konversation. Die vorherigen Teile behandelten: %s",
				i+1, total, summarizeChunk(chunks[i-1].Content))
		}
	}

	return chunks
}

func formatMessage(m models.Message) string {
	// Prefix with date if timestamp is set (enables temporal reasoning in extraction)
	datePrefix := ""
	if !m.Timestamp.IsZero() {
		datePrefix = m.Timestamp.Format("2006-01-02") + " "
	}
	switch m.MessageType {
	case "text":
		content := m.Content
		if len(content) > 1500 {
			content = truncateText(content, m.Role)
		}
		return fmt.Sprintf("[%s%s] %s", datePrefix, m.Role, content)
	case "tool_use":
		if m.ToolName != "" {
			content := m.Content
			if len(content) > 100 {
				content = content[:100]
			}
			return fmt.Sprintf("[%s%s:tool_use:%s] %s", datePrefix, "assistant", m.ToolName, content)
		}
		return ""
	case "tool_result":
		content := m.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return fmt.Sprintf("[%s%s:tool_result] %s", datePrefix, "user", content)
	case "thinking":
		return ""
	case "bash_output":
		content := m.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return fmt.Sprintf("[bash_output] %s", content)
	default:
		return ""
	}
}

func summarizeChunk(content string) string {
	// Take first 200 chars as a rough summary
	if len(content) > 200 {
		return content[:200] + "..."
	}
	return content
}

// PreFilterMessages removes noise messages that don't contribute to learnings.
// Keeps: user text, assistant text, tool_use (name+path only).
// Removes: tool_result, bash_output, thinking.
func PreFilterMessages(msgs []models.Message) []models.Message {
	var filtered []models.Message
	skipNext := false
	for _, m := range msgs {
		// Skip messages that are responses to dialog injections
		if m.MessageType == "text" && m.Role == "user" && isDialogInjection(m.Content) {
			skipNext = true // skip the assistant's response to this injection
			continue
		}
		if skipNext && m.Role == "assistant" {
			skipNext = false
			continue
		}
		skipNext = false

		switch m.MessageType {
		case "text":
			m.Content = StripNoise(m.Content)
			if m.Content != "" {
				filtered = append(filtered, m)
			}
		case "tool_use":
			m.Content = ""
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// isDialogInjection detects dialog/broadcast system-reminder injections from think.go.
func isDialogInjection(content string) bool {
	return strings.Contains(content, "send_to(") ||
		strings.Contains(content, "start_dialog(") ||
		strings.Contains(content, "Dialog-Partner") ||
		strings.Contains(content, "BROADCAST von") ||
		strings.Contains(content, "check_messages")
}

// EstimateTokens gives a rough token count for a string.
func EstimateTokens(s string) int {
	return len(s) / 4
}
