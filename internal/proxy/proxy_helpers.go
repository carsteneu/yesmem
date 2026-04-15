package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// extractSessionID extracts the Claude Code session_id.
// Prefers the X-Claude-Code-Session-Id HTTP header (CC v2.1.86+).
// Falls back to parsing metadata.user_id from the request body for
// older clients or subscription accounts that may not send the header.
func extractSessionID(req map[string]any, headerSessionID string) string {
	if headerSessionID != "" {
		return headerSessionID
	}
	meta, ok := req["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	userID, ok := meta["user_id"].(string)
	if !ok {
		return ""
	}
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(userID), &parsed) == nil && parsed.SessionID != "" {
		return parsed.SessionID
	}
	return ""
}

// DeriveThreadID creates a stable identifier for a conversation thread
// from the system prompt and session ID.
func DeriveThreadID(req map[string]any) string {
	if tid := deriveThreadIDFromMessages(req); tid != "" {
		return tid
	}

	h := sha256.New()
	wrote := false

	// Session ID from metadata.user_id — unique per CC session
	if sid := extractSessionID(req, ""); sid != "" {
		h.Write([]byte(sid))
		wrote = true
	}

	// Hash the working directory from system prompt (stable per project session)
	if sys, ok := req["system"].([]any); ok {
		for _, block := range sys {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			text, _ := b["text"].(string)
			if idx := strings.Index(text, "Primary working directory:"); idx >= 0 {
				end := strings.Index(text[idx:], "\n")
				if end < 0 {
					end = min(len(text)-idx, 200)
				}
				h.Write([]byte(text[idx : idx+end]))
				wrote = true
				break
			}
		}
		if !wrote {
			for _, block := range sys {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				text, _ := b["text"].(string)
				text = strings.TrimSpace(text)
				if text == "" {
					continue
				}
				if len(text) > 400 {
					text = text[:400]
				}
				h.Write([]byte(text))
				wrote = true
				break
			}
		}
	}

	// Use first actual user text from msg[0], skipping injected blocks
	if !wrote {
		if msgs, ok := req["messages"].([]any); ok && len(msgs) > 0 {
			if m, ok := msgs[0].(map[string]any); ok {
				if content, ok := m["content"].([]any); ok {
					for _, block := range content {
						b, ok := block.(map[string]any)
						if !ok {
							continue
						}
						text, _ := b["text"].(string)
						trimmed := strings.TrimSpace(text)
						if text != "" && !strings.HasPrefix(trimmed, "<system-reminder>") &&
							!strings.HasPrefix(text, "[yesmem-") && !strings.HasPrefix(text, "[skill-check]") &&
							!strings.HasPrefix(text, "[task-reminder]") {
							snippet := text
							if len(snippet) > 200 {
								snippet = snippet[:200]
							}
							h.Write([]byte(snippet))
							wrote = true
							break
						}
					}
				} else if content, ok := m["content"].(string); ok {
					snippet := content
					if len(snippet) > 200 {
						snippet = snippet[:200]
					}
					h.Write([]byte(snippet))
					wrote = true
				}
			}
		}
	}

	if !wrote {
		return ""
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}

// deriveThreadIDFromMessages handles OpenAI/Codex-style requests that anchor the
// conversation in developer/system instructions or cwd markers instead of the
// Claude-Code-specific "Primary working directory" prompt pattern.
func deriveThreadIDFromMessages(req map[string]any) string {
	h := sha256.New()
	wrote := false

	// Session ID from metadata.user_id — unique per CC session
	if sid := extractSessionID(req, ""); sid != "" {
		h.Write([]byte(sid))
	}

	if msgs, ok := req["messages"].([]any); ok {
		for _, raw := range msgs {
			msg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msg["role"].(string)
			if role != "developer" && role != "system" {
				continue
			}
			if text := extractTextFromContent(msg["content"]); text != "" {
				snippet := text
				if len(snippet) > 400 {
					snippet = snippet[:400]
				}
				h.Write([]byte(snippet))
				wrote = true
				break
			}
		}
	}

	if !wrote {
		if cwd := extractWorkingDirectory(req); cwd != "" {
			h.Write([]byte(cwd))
			wrote = true
		}
	}

	if !wrote {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// extractProjectName extracts the project name from the system prompt.
// Looks for "Primary working directory: /path/to/project" and returns the last path segment.
func extractProjectName(req map[string]any) string {
	path := extractWorkingDirectory(req)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// extractWorkingDirectory extracts the full working directory path from the request.
// Supports two formats:
// - Claude Code: "Primary working directory: /path/to/project" in system prompt
// - Codex CLI: "<cwd>/path/to/project</cwd>" in input messages
func extractWorkingDirectory(req map[string]any) string {
	// Try top-level system blocks first.
	for _, text := range extractSystemTexts(req["system"]) {
		if path := extractWorkingDirectoryFromText(text); path != "" {
			return path
		}
	}

	// Try Codex format: <cwd>/path</cwd> in input messages
	if msgs, ok := req["messages"].([]any); ok {
		for _, msg := range msgs {
			m, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			// Extract text from message content — handle both string and content blocks
			var texts []string
			switch c := m["content"].(type) {
			case string:
				texts = append(texts, c)
			case []any:
				for _, block := range c {
					if bm, ok := block.(map[string]any); ok {
						if t, ok := bm["text"].(string); ok {
							texts = append(texts, t)
						}
					}
				}
			}
			for _, text := range texts {
				if path := extractWorkingDirectoryFromText(text); path != "" {
					return path
				}
			}
		}
	}

	return ""
}

func extractSystemTexts(system any) []string {
	switch v := system.(type) {
	case string:
		return []string{v}
	case []any:
		var texts []string
		for _, block := range v {
			bm, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := bm["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
		return texts
	default:
		return nil
	}
}

func extractWorkingDirectoryFromText(text string) string {
	const marker = "Primary working directory: "
	if idx := strings.Index(text, marker); idx >= 0 {
		rest := text[idx+len(marker):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[:nl]
		} else if nl := strings.Index(rest, `\n`); nl >= 0 {
			rest = rest[:nl]
		}
		return strings.TrimSpace(rest)
	}

	const cwdOpen = "<cwd>"
	const cwdClose = "</cwd>"
	if idx := strings.Index(text, cwdOpen); idx >= 0 {
		rest := text[idx+len(cwdOpen):]
		if end := strings.Index(rest, cwdClose); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	return ""
}

// extractArchivedTopic creates an ArchivedTopic from stubbed messages.
// Extracts file paths first, falls back to tool names and command summaries.
func extractArchivedTopic(archived []any, reqIdx int) *ArchivedTopic {
	if len(archived) == 0 {
		return nil
	}

	var labels []string
	seen := make(map[string]bool)
	for _, msg := range archived {
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
			name, _ := b["name"].(string)
			input, _ := b["input"].(map[string]any)

			// Prefer file path
			if fp, ok := input["file_path"].(string); ok {
				short := shortPath(fp)
				if !seen[short] {
					seen[short] = true
					labels = append(labels, short)
				}
				continue
			}
			// Fallback: tool-specific summaries
			var summary string
			switch name {
			case "Bash":
				cmd, _ := input["command"].(string)
				summary = "Bash: " + truncateStr(cmd, 30)
			case "WebSearch":
				q, _ := input["query"].(string)
				summary = "WebSearch: " + q
			case "WebFetch":
				url, _ := input["url"].(string)
				summary = "WebFetch: " + truncateStr(url, 40)
			case "Agent":
				desc, _ := input["description"].(string)
				summary = "Agent: " + desc
			default:
				summary = name
			}
			if summary != "" && !seen[summary] {
				seen[summary] = true
				labels = append(labels, summary)
			}
		}
	}

	if len(labels) == 0 {
		return nil
	}
	if len(labels) > 3 {
		labels = labels[:3]
	}

	label := strings.Join(labels, ", ")
	return &ArchivedTopic{
		Label:      label,
		MsgCount:   len(archived),
		ReqRange:   fmt.Sprintf("bis Req %d", reqIdx),
		SearchHint: strings.Join(labels, " "),
	}
}

// extractToolUseIDs collects all tool_use IDs from the messages array.
func extractToolUseIDs(messages []any) []string {
	var ids []string
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
			if b["type"] == "tool_use" {
				if id, ok := b["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// isUserInputTurn checks if the last user message contains real user text,
// not just tool_result blocks or system-reminder injections.
// Returns false for continuation requests (tool results only).
func isUserInputTurn(messages []any) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "user" {
			continue
		}
		// Check content blocks for real text (not tool_result)
		content, ok := m["content"].([]any)
		if !ok {
			// String content = real user text
			if s, ok := m["content"].(string); ok && strings.TrimSpace(s) != "" {
				return true
			}
			return false
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "text" {
				text, _ := b["text"].(string)
				// Strip system-reminder tags to check for real content
				cleaned := reminderPattern.ReplaceAllString(text, "")
				cleaned = strings.TrimSpace(cleaned)
				if cleaned != "" {
					return true
				}
			}
		}
		return false // last user message has no real text
	}
	return false
}

// createLogger sets up logging to both stderr and a log file.
func createLogger(dataDir string) *log.Logger {
	writers := []io.Writer{os.Stderr}

	if dataDir != "" {
		logDir := filepath.Join(dataDir, "logs")
		os.MkdirAll(logDir, 0755)
		logPath := filepath.Join(logDir, "proxy.log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			writers = append(writers, f)
		}
	}

	return log.New(io.MultiWriter(writers...), "[proxy] ", log.LstdFlags)
}

// countTokens returns token count using the lib tokenizer,
// falling back to byte-length heuristic if tokenizer is unavailable.
func (s *Server) countTokens(text string) int {
	if s.tokenizer != nil {
		return s.tokenizer.Tokens(text)
	}
	return int(float64(len(text)) / bytesPerToken)
}

// measureOverhead computes system+tools token count from the request.
// Cached after first measurement (these don't change within a session).
func (s *Server) measureOverhead(req map[string]any) int {
	var total int
	if sys, ok := req["system"]; ok {
		data, _ := json.Marshal(sys)
		total += s.countTokens(string(data))
	}
	if tools, ok := req["tools"]; ok {
		data, _ := json.Marshal(tools)
		total += s.countTokens(string(data))
	}

	// Floor at 5000 to prevent weird edge cases
	if total < 5000 {
		total = 5000
	}

	return total
}

// countMessageTokens estimates tokens by extracting text content from messages.
// Uses content extraction (not JSON marshaling) for accurate API-aligned counts.
func (s *Server) countMessageTokens(messages []any) int {
	return countContentTokens(messages, func(text string) int {
		return s.countTokens(text)
	})
}

// estimateTotalTokens returns the best token estimate for the current request.
// If a previous API response actual is available, uses: lastActual + delta(new messages).
// The actual-based path does NOT add overhead because lastActual (from API input_tokens)
// already includes system prompt and tool schema tokens.
// Otherwise falls back to full countMessageTokens + overhead.
func (s *Server) estimateTotalTokens(threadID string, messages []any, overhead int) int {
	if s.sawtoothTrigger == nil {
		return s.countMessageTokens(messages) + overhead
	}

	lastActual := s.sawtoothTrigger.GetLastTokens(threadID)
	lastMsgCount := s.sawtoothTrigger.GetLastMessageCount(threadID)

	// No previous data or message count shrank (new session?) → full count
	if lastActual == 0 || lastMsgCount == 0 || lastMsgCount > len(messages) {
		return s.countMessageTokens(messages) + overhead
	}

	// Count only new messages (tail since last response)
	newMessages := messages[lastMsgCount:]
	deltaTokens := s.countMessageTokens(newMessages)

	return lastActual + deltaTokens
}

// persistCompactedBlocks sends compacted blocks to the daemon for DB storage.
func (s *Server) persistCompactedBlocks(threadID string, blocks []CompactedBlock) {
	for _, b := range blocks {
		_, err := s.queryDaemon("store_compacted_block", map[string]any{
			"thread_id": threadID,
			"start_idx": b.StartIdx,
			"end_idx":   b.EndIdx,
			"content":   b.Content,
		})
		if err != nil {
			s.logger.Printf("persist compacted block %d-%d: %v", b.StartIdx, b.EndIdx, err)
		}
	}
}

// countCompactedMsgs returns the total number of messages replaced by compacted blocks.
func countCompactedMsgs(blocks []CompactedBlock) int {
	total := 0
	for _, b := range blocks {
		total += b.EndIdx - b.StartIdx + 1
	}
	return total
}

// prependMeta prepends a metadata string to a message's first text content.
// Handles both string content and content-block arrays. Skips if already annotated.
func prependMeta(msg map[string]any, meta string) {
	prefix := meta + " "

	switch content := msg["content"].(type) {
	case string:
		if hasMetaPrefix(content) {
			return
		}
		msg["content"] = prefix + content
	case []any:
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "text" {
				continue
			}
			text, _ := b["text"].(string)
			if hasMetaPrefix(text) {
				return
			}
			b["text"] = prefix + text
			return
		}
	}
}

// hasMetaPrefix returns true if text already starts with a timestamp/msg annotation.
func hasMetaPrefix(text string) bool {
	if len(text) < 6 || text[0] != '[' {
		return false
	}
	end := len(text)
	if end > 40 {
		end = 40
	}
	return strings.Contains(text[:end], "[msg:")
}

// stripMetaPrefixText removes any leading [timestamp] [msg:N] [+delta] annotations from text.
func stripMetaPrefixText(text string) string {
	if !hasMetaPrefix(text) {
		return text
	}
	// Strip consecutive [...] blocks at the start
	s := text
	for strings.HasPrefix(s, "[") {
		end := strings.Index(s, "] ")
		if end < 0 {
			break
		}
		s = s[end+2:]
	}
	return s
}

// stripMetaPrefix removes any [timestamp] [msg:N] [+delta] prefix from a message's text content.
func stripMetaPrefix(msg map[string]any) {
	switch content := msg["content"].(type) {
	case string:
		if hasMetaPrefix(content) {
			msg["content"] = stripMetaPrefixText(content)
		}
	case []any:
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "text" {
				continue
			}
			text, _ := b["text"].(string)
			if hasMetaPrefix(text) {
				b["text"] = stripMetaPrefixText(text)
			}
			return // only first text block
		}
	}
}

// formatDelta formats a time.Duration as a compact human-readable string like "4m12s" or "1h23m".
// shortWeekday returns a 2-letter German weekday abbreviation.
func shortWeekday(w time.Weekday) string {
	return [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}[w]
}

func formatDelta(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func (s *Server) setRawEstimate(threadID string, tokens int) {
	s.rawSavingsMu.Lock()
	if s.rawSavings == nil {
		s.rawSavings = make(map[string]int)
	}
	s.rawSavings[threadID] = tokens
	s.rawSavingsMu.Unlock()
}

func (s *Server) getRawEstimate(threadID string) int {
	s.rawSavingsMu.Lock()
	defer s.rawSavingsMu.Unlock()
	if s.rawSavings == nil {
		return 0
	}
	return s.rawSavings[threadID]
}
