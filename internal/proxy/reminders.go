package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// reminderPattern matches <system-reminder>...</system-reminder> blocks including newlines.
var reminderPattern = regexp.MustCompile(`(?s)<system-reminder>\s*(.*?)\s*</system-reminder>`)

// fileChangePathPattern extracts path from "Note: /path/to/file was modified"
var fileChangePathPattern = regexp.MustCompile(`Note:\s+(/[^\s]+)\s+was modified`)

// fileChangeLinePattern extracts line numbers from "     123→" format
var fileChangeLinePattern = regexp.MustCompile(`(?m)^\s+(\d+)→`)

// StripReminders replaces redundant <system-reminder> blocks in older messages with compact keywords.
// The last user message keeps all reminders intact. Older messages get progressively stripped
// based on reminder type and age (requestIdx - message position).
//
// Protected (never stripped): SessionStart blocks only.
// Narrative and briefing now live in system block and are actively stripped from messages.
// Immediately stripped: skill-check, task-reminder, local-command-caveat, yesmem-context.
// Age-based: file-change diffs (full < 3 req, summary 3-10, minimal > 10).
func StripReminders(messages []any, requestIdx int) []any {
	// Find last user message index
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] == "user" {
			lastUserIdx = i
			break
		}
	}

	result := make([]any, len(messages))
	copy(result, messages)

	for i := 0; i < len(result); i++ {
		if i == lastUserIdx {
			continue // keep last user message intact
		}

		msg, ok := result[i].(map[string]any)
		if !ok {
			continue
		}

		// Estimate message age based on position (rough: 2 messages per request)
		msgAge := requestIdx - (i / 2)
		if msgAge < 0 {
			msgAge = 0
		}

		content := msg["content"]
		newContent := stripContentReminders(content, msgAge)
		// Always create new message with stripped content
		newMsg := make(map[string]any)
		for k, v := range msg {
			newMsg[k] = v
		}
		newMsg["content"] = newContent
		result[i] = newMsg
	}

	return result
}

// stripContentReminders processes content (string or []any blocks) and strips reminders.
func stripContentReminders(content any, msgAge int) any {
	switch c := content.(type) {
	case string:
		return stripRemindersFromText(c, msgAge)
	case []any:
		result := make([]any, 0, len(c))
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				result = append(result, block)
				continue
			}
			text, isText := b["text"].(string)
			if !isText || b["type"] != "text" {
				result = append(result, block)
				continue
			}
			stripped := stripRemindersFromText(text, msgAge)
			newBlock := make(map[string]any)
			for k, v := range b {
				newBlock[k] = v
			}
			newBlock["text"] = stripped
			result = append(result, newBlock)
		}
		return result
	default:
		return content
	}
}

// stripRemindersFromText replaces <system-reminder> blocks in a text string.
func stripRemindersFromText(text string, msgAge int) string {
	return reminderPattern.ReplaceAllStringFunc(text, func(match string) string {
		// Extract the inner content
		inner := reminderPattern.FindStringSubmatch(match)
		if len(inner) < 2 {
			return match
		}
		body := inner[1]

		return classifyAndReplace(body, msgAge)
	})
}

// classifyAndReplace determines the reminder type and returns the appropriate replacement.
func classifyAndReplace(body string, msgAge int) string {
	// Protected: never strip
	if isSessionStart(body) {
		return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", body)
	}

	// File-change: age-based
	if isFileChange(body) {
		return replaceFileChange(body, msgAge)
	}

	// Immediately replaceable types
	if isSkillCheck(body) {
		return "[skill-check]"
	}
	if isThinkReminder(body) {
		return "[think]"
	}
	if isYesMemContext(body) {
		return "[yesmem-context]"
	}
	if isTaskReminder(body) {
		return "[task-reminder]"
	}
	if isLocalCommandCaveat(body) {
		return "[local-cmd]"
	}

	// Unknown reminder type — keep it (safe default)
	return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", body)
}

// --- Type Detection ---

func isSessionStart(body string) bool {
	return strings.Contains(body, "SessionStart:")
}

func isFileChange(body string) bool {
	return strings.Contains(body, "was modified, either by the user or by a linter") ||
		strings.Contains(body, "was modified,")
}

func isSkillCheck(body string) bool {
	return strings.Contains(body, "MANDATORY SKILL ACTIVATION") ||
		strings.Contains(body, "UserPromptSubmit hook success")
}

func isThinkReminder(body string) bool {
	return strings.Contains(body, "Before acting") && strings.Contains(body, "hybrid_search()")
}

func isYesMemContext(body string) bool {
	return strings.Contains(body, "YesMem Gotchas:") ||
		strings.Contains(body, "PreToolUse:") ||
		strings.Contains(body, "hook additional context:")
}

func isTaskReminder(body string) bool {
	return strings.Contains(body, "task tools haven't been used recently")
}

func isLocalCommandCaveat(body string) bool {
	return strings.Contains(body, "local-command-caveat") ||
		strings.Contains(body, "Caveat: The messages below were generated by the user while running local commands")
}

// --- File Change Replacement ---

func replaceFileChange(body string, msgAge int) string {
	path := extractFilePath(body)
	shortPath := shortenPath(path)

	if msgAge < 3 {
		// Keep full diff
		return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", body)
	}

	if msgAge < 10 {
		// Summary: path + line numbers
		lines := extractLineNumbers(body)
		if len(lines) > 0 {
			return fmt.Sprintf("[file-changed: %s L%s]", shortPath, formatLineRanges(lines))
		}
		return fmt.Sprintf("[file-changed: %s]", shortPath)
	}

	// Minimal
	return fmt.Sprintf("[file-changed: %s]", shortPath)
}

func extractFilePath(body string) string {
	matches := fileChangePathPattern.FindStringSubmatch(body)
	if len(matches) >= 2 {
		return matches[1]
	}
	return "unknown"
}

func shortenPath(path string) string {
	// /home/user/project/internal/proxy/proxy.go → proxy.go
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

func extractLineNumbers(body string) []int {
	matches := fileChangeLinePattern.FindAllStringSubmatch(body, -1)
	seen := make(map[int]bool)
	var lines []int
	for _, m := range matches {
		if len(m) >= 2 {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			if !seen[n] {
				seen[n] = true
				lines = append(lines, n)
			}
		}
	}
	return lines
}

func formatLineRanges(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	if len(lines) == 1 {
		return fmt.Sprintf("%d", lines[0])
	}
	// Show first and last
	min, max := lines[0], lines[0]
	for _, l := range lines {
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
	}
	return fmt.Sprintf("%d-%d", min, max)
}

