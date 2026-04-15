package proxy

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const minCollapseMessages = 1

// CalcCollapseCutoff finds the cutoff index so that messages[cutoff:] retain
// at least tokenFloor tokens. Walks backwards from the end, accumulating
// token estimates. Returns -1 if no collapse is needed.
func CalcCollapseCutoff(messages []any, keepRecent int, tokenFloor int, estimate TokenEstimateFunc) int {
	n := len(messages)
	if n < minCollapseMessages+keepRecent {
		return -1
	}

	// Sum tokens from the end (protected tail first)
	var kept int
	// Walk backwards from last message; stop when we've accumulated enough
	// to reach the floor. Everything before that index can be collapsed.
	// Index 0 is system message — never collapse it.
	cutoff := -1
	for i := n - 1; i >= 1; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			kept += perMessageOverhead
		} else {
			kept += estimateMessageContentTokens(msg, func(text string) int {
				return estimate(text)
			})
		}
		if kept >= tokenFloor {
			cutoff = i
			break
		}
	}

	if cutoff < minCollapseMessages {
		return -1
	}

	// Never split tool_use/tool_result pairs: if messages[cutoff] is a user
	// message containing tool_result blocks, its matching tool_use sits at
	// cutoff-1 and would be collapsed — leaving an orphan tool_result.
	// Advance cutoff to include the orphan in the collapse.
	for cutoff < n {
		msg, ok := messages[cutoff].(map[string]any)
		if !ok {
			break
		}
		if msg["role"] != "user" || !hasToolResultContent(msg["content"]) {
			break
		}
		cutoff++
	}

	// Also respect keepRecent: never collapse into the recent window
	maxCutoff := n - keepRecent
	if cutoff > maxCutoff {
		cutoff = maxCutoff
	}
	if cutoff < minCollapseMessages {
		return -1
	}

	return cutoff
}

// CollapseOldMessages replaces all messages before cutoffIdx with a single
// archive summary block. The system message (index 0) is preserved.
// Original messages are used for stats extraction, modified for size estimation.
// Returns a new slice: [system, archive_block, messages[cutoffIdx:]...]
func CollapseOldMessages(modified, original []any, cutoffIdx int, sessionStart, sessionEnd time.Time, learnings []ArchiveLearning, flavors []ArchiveSessionFlavor, threadID ...string) []any {
	if cutoffIdx < minCollapseMessages || cutoffIdx >= len(modified) {
		return modified
	}

	// Extract stats from original messages (1 to cutoffIdx-1)
	stats := extractStatsFromMessages(original, 1, cutoffIdx-1)
	stats.SessionStart = sessionStart
	stats.SessionEnd = sessionEnd
	stats.Learnings = learnings
	stats.Flavors = flavors

	// Extract event timeline from messages (fallback when no flavors available)
	stats.Digests = extractTimeline(original, 1, cutoffIdx-1)

	// Extract git commits with hashes from messages
	stats.Commits = extractGitCommits(original, 1, cutoffIdx-1)

	// Build archive content
	tid := "thread_id"
	if len(threadID) > 0 && threadID[0] != "" {
		tid = threadID[0]
	}
	archiveContent := buildArchiveBlock(1, cutoffIdx-1, stats, tid)

	// Build result: system + archive + recent
	result := make([]any, 0, 2+len(modified)-cutoffIdx)
	result = append(result, modified[0]) // system message
	result = append(result, map[string]any{
		"role":    "user",
		"content": archiveContent,
	})
	result = append(result, modified[cutoffIdx:]...)

	return result
}

// buildArchiveBlock creates the summary text for collapsed messages.
// Uses session flavors + git commits + learnings when available,
// falls back to mechanical timeline when no extraction data exists.
func buildArchiveBlock(start, end int, stats compactionStats, threadID string) string {
	msgCount := end - start + 1
	var sb strings.Builder

	fmt.Fprintf(&sb, "[Archiv: Messages %d-%d (%d msgs) — get_compacted_stubs('%s', %d, %d) zum Reinzoomen]", start, end, msgCount, threadID, start, end)

	if stats.ToolStats != "" {
		fmt.Fprintf(&sb, "\nTools: %s", stats.ToolStats)
	}
	if stats.FileStats != "" {
		fmt.Fprintf(&sb, "\nFiles: %s", stats.FileStats)
	}

	// --- Sessions (from extraction flavors) ---
	if len(stats.Flavors) > 0 {
		sb.WriteString("\n\nSessions:")
		lastDate := ""
		for _, f := range stats.Flavors {
			ts, _ := time.Parse(time.RFC3339, f.CreatedAt)
			date := ts.Format("2006-01-02")
			timeStr := ts.Format("15:04")

			if date != lastDate {
				fmt.Fprintf(&sb, "\n  (%s):", date)
				lastDate = date
			}
			fmt.Fprintf(&sb, "\n  [%s] %s", timeStr, f.Flavor)
		}
	}

	// --- Commits (from git commit tool_use blocks) ---
	if len(stats.Commits) > 0 {
		sb.WriteString("\n\nCommits:")
		lastDate := ""
		for _, c := range stats.Commits {
			date := c.Date
			if date == "" {
				// Fallback: use session start date
				if !stats.SessionStart.IsZero() {
					date = stats.SessionStart.Format("2006-01-02")
				}
			}
			if date != lastDate && date != "" {
				fmt.Fprintf(&sb, "\n  (%s):", date)
				lastDate = date
			}
			hashStr := ""
			if c.Hash != "" {
				hashStr = c.Hash + " "
			}
			timeStr := ""
			if c.Time != "" {
				timeStr = c.Time + " "
			}
			msg := c.Message
			if len(msg) > 80 {
				msg = msg[:80] + "..."
			}
			if timeStr != "" {
				fmt.Fprintf(&sb, "\n  [%s] %s%s", timeStr, hashStr, msg)
			} else {
				fmt.Fprintf(&sb, "\n  %s%s", hashStr, msg)
			}
		}
	}

	// --- Learnings: Pivots + Gotchas + Unfinished ---
	var pivots, gotchas, unfinished []ArchiveLearning
	for _, l := range stats.Learnings {
		switch l.Category {
		case "pivot_moment":
			pivots = append(pivots, l)
		case "gotcha":
			gotchas = append(gotchas, l)
		case "unfinished":
			unfinished = append(unfinished, l)
		}
	}

	if len(pivots) > 0 {
		sb.WriteString("\n\nWendepunkte:")
		for _, p := range pivots {
			content := strings.ReplaceAll(p.Content, "\n", " ")
			fmt.Fprintf(&sb, "\n  - %s", content)
		}
	}

	if len(gotchas) > 0 {
		sb.WriteString("\n\nGotchas:")
		for _, g := range gotchas {
			content := strings.ReplaceAll(g.Content, "\n", " ")
			fmt.Fprintf(&sb, "\n  - %s", content)
		}
	}

	if len(unfinished) > 0 {
		sb.WriteString("\n\nOffen:")
		for _, u := range unfinished {
			content := strings.ReplaceAll(u.Content, "\n", " ")
			fmt.Fprintf(&sb, "\n  - %s", content)
		}
	}

	// --- Fallback: mechanical timeline when no flavors available ---
	if len(stats.Flavors) == 0 && len(stats.Digests) > 0 {
		sb.WriteString("\n\nTimeline:\n")
		for _, d := range stats.Digests {
			sb.WriteString(d)
			sb.WriteByte('\n')
		}
	}

	return sb.String()
}

// extractTimeline builds an event-based timeline from collapsed messages.
// Replaces the old extractDigests (first 120 chars of long messages) with
// meaningful events: user messages, tool actions, git commits.
// User messages are ALWAYS included (they're the steering signals).
// Assistant text-only responses are skipped (fragments are useless without context).
func extractTimeline(messages []any, start, end int) []string {
	var events []string

	for i := start; i <= end && i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)

		// User messages: always include (they're decisions/steering)
		if role == "user" {
			text := extractTextFromMessage(msg)
			if text == "" || strings.HasPrefix(text, "[") {
				continue // skip system-reminders, task-notifications
			}
			text = strings.TrimSpace(text)
			if len(text) > 200 {
				text = text[:200] + "..."
			}
			events = append(events, fmt.Sprintf("  [%d] U: %s", i, text))
			continue
		}

		// Assistant messages: extract tool_use events, skip raw text
		if role == "assistant" {
			toolEvents := extractToolEvents(msg, i)
			events = append(events, toolEvents...)
		}
	}

	// Budget: max 120 events to stay within token limits
	if len(events) > 120 {
		// Keep first 20 + last 100 (recent context is more important)
		kept := make([]string, 0, 121)
		kept = append(kept, events[:20]...)
		kept = append(kept, fmt.Sprintf("  [...%d events omitted...]", len(events)-120))
		kept = append(kept, events[len(events)-100:]...)
		events = kept
	}

	// Deduplicate consecutive identical-type events (SSH spam, deploy runs, builds)
	events = deduplicateEvents(events)

	return events
}

// deduplicateEvents collapses consecutive same-type events into one line.
// "deploy" x3 → "deploy (3x)", "ssh host: ..." x8 → "ssh host (8x)"
func deduplicateEvents(events []string) []string {
	if len(events) <= 1 {
		return events
	}

	var result []string
	i := 0
	for i < len(events) {
		eventType := classifyEvent(events[i])
		if eventType == "" {
			result = append(result, events[i])
			i++
			continue
		}

		// Count consecutive events of the same type
		count := 1
		for i+count < len(events) && classifyEvent(events[i+count]) == eventType {
			count++
		}

		if count <= 2 {
			// Keep individual events
			for j := 0; j < count; j++ {
				result = append(result, events[i+j])
			}
		} else {
			// Collapse: keep first, note count
			result = append(result, fmt.Sprintf("%s (%dx)", events[i], count))
		}
		i += count
	}
	return result
}

// classifyEvent returns a dedup key for events that should be collapsed.
// Returns "" for events that should never be collapsed.
func classifyEvent(event string) string {
	// Never collapse user messages or git commits
	if strings.Contains(event, "] U:") || strings.Contains(event, "git commit:") {
		return ""
	}
	// Collapse by prefix type
	if strings.Contains(event, "] deploy") {
		return "deploy"
	}
	if strings.Contains(event, "] build") {
		return "build"
	}
	if strings.Contains(event, "] ssh ") {
		// Group by host
		parts := strings.SplitN(event, "ssh ", 2)
		if len(parts) == 2 {
			hostEnd := strings.Index(parts[1], ":")
			if hostEnd > 0 {
				return "ssh:" + parts[1][:hostEnd]
			}
			return "ssh"
		}
	}
	return ""
}

// extractToolEvents extracts meaningful events from assistant tool_use blocks.
func extractToolEvents(msg map[string]any, msgIdx int) []string {
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}

	var events []string
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] != "tool_use" {
			continue
		}

		name, _ := b["name"].(string)
		input, _ := b["input"].(map[string]any)
		if input == nil {
			continue
		}

		event := formatToolEvent(name, input, msgIdx)
		if event != "" {
			events = append(events, event)
		}
	}
	return events
}

// formatToolEvent creates a concise one-line description of a tool action.
func formatToolEvent(toolName string, input map[string]any, msgIdx int) string {
	switch toolName {
	case "Edit":
		fp, _ := input["file_path"].(string)
		return fmt.Sprintf("  [%d] Edit: %s", msgIdx, basePath(fp))

	case "Write":
		fp, _ := input["file_path"].(string)
		return fmt.Sprintf("  [%d] Write: %s", msgIdx, basePath(fp))

	case "Read":
		// Skip reads — too noisy, not actionable
		return ""

	case "Bash":
		cmd, _ := input["command"].(string)
		return formatBashEvent(cmd, msgIdx)

	case "Grep", "Glob":
		// Skip search operations — noisy
		return ""

	default:
		// MCP tools and others — include with short name
		if strings.HasPrefix(toolName, "mcp__yesmem__") {
			short := strings.TrimPrefix(toolName, "mcp__yesmem__")
			return fmt.Sprintf("  [%d] yesmem:%s", msgIdx, short)
		}
		if strings.HasPrefix(toolName, "mcp__") {
			return "" // skip other MCP tools
		}
		return fmt.Sprintf("  [%d] %s", msgIdx, toolName)
	}
}

// formatBashEvent extracts meaningful info from bash commands.
func formatBashEvent(cmd string, msgIdx int) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}

	// Git commits — extract message (handles both simple -m and heredoc format)
	if strings.Contains(cmd, "git commit") {
		msg := extractGitCommitMessage(cmd)
		if msg != "" {
			if len(msg) > 100 {
				msg = msg[:100] + "..."
			}
			return fmt.Sprintf("  [%d] git commit: %s", msgIdx, msg)
		}
		return fmt.Sprintf("  [%d] git commit", msgIdx)
	}

	// Deployments
	if strings.Contains(cmd, "make deploy") || (strings.Contains(cmd, "scp ") && strings.Contains(cmd, "yesmem")) {
		return fmt.Sprintf("  [%d] deploy", msgIdx)
	}

	// SSH commands
	if strings.HasPrefix(cmd, "ssh ") {
		return formatSSHEvent(cmd, msgIdx)
	}

	// Build commands
	if strings.Contains(cmd, "make build") || strings.Contains(cmd, "go build") {
		return fmt.Sprintf("  [%d] build", msgIdx)
	}

	// Service management
	if strings.Contains(cmd, "systemctl") {
		return fmt.Sprintf("  [%d] %s", msgIdx, truncateCmd(cmd, 80))
	}

	// Git operations (non-commit)
	if strings.HasPrefix(cmd, "git ") {
		return fmt.Sprintf("  [%d] %s", msgIdx, truncateCmd(cmd, 80))
	}

	// Skip everything else (tests, cat, ls, etc.) — too noisy
	return ""
}

// extractGitCommitMessage extracts the commit message from various git commit formats:
// - git commit -m "message"
// - git commit -m "$(cat <<'EOF'\nmessage\nEOF\n)"
// - git commit -m 'message'
func extractGitCommitMessage(cmd string) string {
	// Heredoc format: cat <<'EOF'\n...\nEOF
	if strings.Contains(cmd, "<<") {
		// Find content between first newline after << and the EOF marker
		parts := strings.SplitN(cmd, "\n", 3)
		if len(parts) >= 3 {
			// parts[1] is the start of the message, parts[2] has the rest
			rest := parts[1] + "\n" + parts[2]
			// Find EOF marker
			for _, marker := range []string{"EOF", "COMMIT_MSG"} {
				if eofIdx := strings.Index(rest, "\n"+marker); eofIdx >= 0 {
					msg := strings.TrimSpace(rest[:eofIdx])
					// Take first line as summary
					if nlIdx := strings.Index(msg, "\n"); nlIdx > 0 {
						return strings.TrimSpace(msg[:nlIdx])
					}
					return msg
				}
			}
			// No EOF found, take first meaningful line
			firstLine := strings.TrimSpace(parts[1])
			if firstLine != "" && firstLine != "'EOF'" && firstLine != "EOF" {
				return firstLine
			}
		}
	}

	// Simple -m "message" format
	if idx := strings.Index(cmd, "-m "); idx >= 0 {
		rest := cmd[idx+3:]
		rest = strings.TrimSpace(rest)
		// Strip outer quotes
		if len(rest) > 1 {
			if (rest[0] == '"' && strings.Contains(rest[1:], "\"")) {
				end := strings.Index(rest[1:], "\"")
				return rest[1 : end+1]
			}
			if (rest[0] == '\'' && strings.Contains(rest[1:], "'")) {
				end := strings.Index(rest[1:], "'")
				return rest[1 : end+1]
			}
		}
		// No quotes — take until end of line or next flag
		if nlIdx := strings.IndexAny(rest, "\n"); nlIdx > 0 {
			return strings.TrimSpace(rest[:nlIdx])
		}
		return strings.TrimSpace(rest)
	}

	return ""
}

// formatSSHEvent formats an SSH command concisely.
func formatSSHEvent(cmd string, msgIdx int) string {
	// Extract host from "ssh user@host ..."
	parts := strings.Fields(cmd)
	host := "remote"
	if len(parts) >= 2 {
		host = parts[1]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}

	// Extract remote command (after the host, usually in quotes)
	if qIdx := strings.Index(cmd, "\""); qIdx >= 0 {
		remoteCmd := cmd[qIdx+1:]
		if endQ := strings.LastIndex(remoteCmd, "\""); endQ > 0 {
			remoteCmd = remoteCmd[:endQ]
		}
		if len(remoteCmd) > 60 {
			remoteCmd = remoteCmd[:60] + "..."
		}
		return fmt.Sprintf("  [%d] ssh %s: %s", msgIdx, host, remoteCmd)
	}

	return fmt.Sprintf("  [%d] ssh %s", msgIdx, host)
}

// extractTextFromMessage gets the text content from a message, handling both
// string content and structured content arrays.
func extractTextFromMessage(msg map[string]any) string {
	switch c := msg["content"].(type) {
	case string:
		return c
	case []any:
		for _, block := range c {
			if b, ok := block.(map[string]any); ok {
				if b["type"] == "text" {
					if t, ok := b["text"].(string); ok {
						return t
					}
				}
			}
		}
	}
	return ""
}

// basePath returns the last 2 path components for readability.
func basePath(fp string) string {
	if fp == "" {
		return "?"
	}
	parts := strings.Split(fp, "/")
	if len(parts) <= 2 {
		return fp
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// truncateCmd shortens a command string.
func truncateCmd(cmd string, maxLen int) string {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.ReplaceAll(cmd, "\n", " ")
	if len(cmd) > maxLen {
		return cmd[:maxLen] + "..."
	}
	return cmd
}

// --- Timestamp parsing ---

var timestampRe = regexp.MustCompile(`\[(\d{1,2}:\d{2}:\d{2})\]`)
var dateRe = regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2})\s`)

// extractTimestamp parses [HH:MM:SS] and optionally [YYYY-MM-DD HH:MM:SS] from message text.
// Returns time string "HH:MM" and date string "YYYY-MM-DD" (empty if not found).
func extractTimestamp(msg map[string]any) (timeStr, dateStr string) {
	text := extractTextFromMessage(msg)
	if text == "" {
		return "", ""
	}
	if m := timestampRe.FindStringSubmatch(text); len(m) > 1 {
		// Return HH:MM (drop seconds for compactness)
		parts := strings.SplitN(m[1], ":", 3)
		if len(parts) >= 2 {
			timeStr = parts[0] + ":" + parts[1]
		}
	}
	if m := dateRe.FindStringSubmatch(text); len(m) > 1 {
		dateStr = m[1]
	}
	return
}

// --- Git commit extraction ---

// GitCommit represents a commit found in collapsed messages.
type GitCommit struct {
	Hash    string // short hash (e.g. "15f3db1")
	Message string // first line of commit message
	MsgIdx  int    // message index
	Time    string // HH:MM if available
	Date    string // YYYY-MM-DD if available
}

var commitHashRe = regexp.MustCompile(`\[(?:main|master|HEAD|feature/[^\]]+)\s+([0-9a-f]{7,12})\]`)

// extractGitCommits finds git commits in collapsed messages by matching
// Bash tool_use (git commit) with the next tool_result containing the commit hash.
func extractGitCommits(messages []any, start, end int) []GitCommit {
	var commits []GitCommit

	for i := start; i <= end && i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)

		if role == "assistant" {
			content, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok || b["type"] != "tool_use" || b["name"] != "Bash" {
					continue
				}
				input, _ := b["input"].(map[string]any)
				if input == nil {
					continue
				}
				cmd, _ := input["command"].(string)
				if !strings.Contains(cmd, "git commit") {
					continue
				}

				commitMsg := extractGitCommitMessage(cmd)
				toolUseID, _ := b["id"].(string)

				// Find the matching tool_result to get the commit hash
				hash := findCommitHash(messages, i+1, end, toolUseID)

				// Get timestamp from surrounding user messages
				ts, date := findNearestTimestamp(messages, i, start)

				commits = append(commits, GitCommit{
					Hash:    hash,
					Message: commitMsg,
					MsgIdx:  i,
					Time:    ts,
					Date:    date,
				})
			}
		}
	}
	return commits
}

// findCommitHash looks for a tool_result matching the tool_use_id and extracts
// the commit hash from output like "[main 15f3db1] fix: message"
func findCommitHash(messages []any, start, end int, toolUseID string) string {
	if toolUseID == "" {
		return ""
	}
	for i := start; i <= end && i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_result" {
				continue
			}
			if id, _ := b["tool_use_id"].(string); id != toolUseID {
				continue
			}
			// Extract commit hash from result text
			resultText := ""
			switch c := b["content"].(type) {
			case string:
				resultText = c
			case []any:
				for _, rb := range c {
					if rbb, ok := rb.(map[string]any); ok {
						if t, ok := rbb["text"].(string); ok {
							resultText = t
							break
						}
					}
				}
			}
			if m := commitHashRe.FindStringSubmatch(resultText); len(m) > 1 {
				return m[1]
			}
			return ""
		}
	}
	return ""
}

// findNearestTimestamp looks backwards from msgIdx to find the nearest user message with a timestamp.
func findNearestTimestamp(messages []any, msgIdx, start int) (timeStr, dateStr string) {
	for i := msgIdx; i >= start; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "user" {
			continue
		}
		ts, date := extractTimestamp(msg)
		if ts != "" {
			return ts, date
		}
	}
	return "", ""
}

// --- Session flavor data for archive block ---

// ArchiveSessionFlavor represents a session summary for the archive block.
type ArchiveSessionFlavor struct {
	Flavor    string
	CreatedAt string // RFC3339
	SessionID string
}

// injectMetamemoryBlock appends a metamemory text block to the last user message.
// Used in the compress-only path when no collapse happens.
func injectMetamemoryBlock(messages []any, text string) {
	// Find last user message
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		// Append as text block
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
		msg["content"] = content
		return
	}
}
