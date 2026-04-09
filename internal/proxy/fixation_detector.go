package proxy

import (
	"strings"
)

// FixationAnalysis holds the result of analyzing a message history for fixation patterns.
type FixationAnalysis struct {
	TotalMessages    int
	FixationMessages int     // Messages spent in fixation runs
	Ratio            float64 // FixationMessages / TotalMessages
}

// errorRunResult tracks consecutive error runs.
type errorRunResult struct {
	runs             int
	affectedMessages int
}

// AnalyzeFixation analyzes a message history for fixation patterns.
// Returns ratio of messages spent in fixation vs total messages.
func AnalyzeFixation(messages []any) FixationAnalysis {
	total := len(messages)
	if total == 0 {
		return FixationAnalysis{}
	}

	fixationMsgs := 0

	// Signal 1: Consecutive error runs (≥8)
	errRuns := countConsecutiveErrorRuns(messages, 8)
	fixationMsgs += errRuns.affectedMessages

	// Signal 2: Edit-Build-Error cycles (≥6)
	ebCycles := countEditBuildErrorCycles(messages, 6)
	fixationMsgs += ebCycles.affectedMessages

	// Signal 3: Excessive file retries (≥10 edits same file)
	retries := countFileRetries(messages, 10)
	for _, count := range retries {
		fixationMsgs += count * 2 // edit + result per retry
	}

	// Deduplicate: cap at total (signals can overlap)
	if fixationMsgs > total {
		fixationMsgs = total
	}

	ratio := float64(fixationMsgs) / float64(total)

	return FixationAnalysis{
		TotalMessages:    total,
		FixationMessages: fixationMsgs,
		Ratio:            ratio,
	}
}

// countConsecutiveErrorRuns counts runs of consecutive tool_result errors
// that are at least minRun long. Returns run count and total affected messages.
func countConsecutiveErrorRuns(messages []any, minRun int) errorRunResult {
	var result errorRunResult
	streak := 0
	streakStartIdx := 0

	for i, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if hasErrorInToolResult(msg) {
			if streak == 0 {
				streakStartIdx = i
			}
			streak++
		} else if isToolResultMsg(msg) && !hasErrorInToolResult(msg) {
			// Non-error tool result breaks the streak
			if streak >= minRun {
				result.runs++
				affected := i - streakStartIdx
				if affected < streak*2 {
					affected = streak * 2
				}
				result.affectedMessages += affected
			}
			streak = 0
		}
		// Assistant messages (tool_use) between errors are part of the cycle — don't reset
	}
	// Check trailing streak (session ended during fixation)
	if streak >= minRun {
		result.runs++
		affected := len(messages) - streakStartIdx
		if affected < streak*2 {
			affected = streak * 2
		}
		result.affectedMessages += affected
	}

	return result
}

// countEditBuildErrorCycles counts sequences of Edit→Bash(build)→Error
// that repeat at least minCycles times consecutively.
func countEditBuildErrorCycles(messages []any, minCycles int) errorRunResult {
	var result errorRunResult

	// State machine: looking for Edit→Build→Error patterns
	type cycleState int
	const (
		stateIdle cycleState = iota
		stateEdited
		stateBuilding
	)

	state := stateIdle
	cycleCount := 0

	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}

		switch state {
		case stateIdle:
			if isEditToolUse(msg) {
				state = stateEdited
			}
		case stateEdited:
			if isBuildToolUse(msg) {
				state = stateBuilding
			} else if isEditToolUse(msg) {
				// Another edit — stay in edited state
			} else if !isToolResultMsg(msg) {
				// Something else entirely — reset
				if cycleCount >= minCycles {
					result.runs++
					result.affectedMessages += cycleCount * 4
				}
				cycleCount = 0
				state = stateIdle
			}
		case stateBuilding:
			if hasErrorInToolResult(msg) {
				cycleCount++
				state = stateIdle // back to looking for next Edit
			} else if isToolResultMsg(msg) {
				// Build succeeded — end cycle
				if cycleCount >= minCycles {
					result.runs++
					result.affectedMessages += cycleCount * 4
				}
				cycleCount = 0
				state = stateIdle
			}
		}
	}

	// Trailing cycle
	if cycleCount >= minCycles {
		result.runs++
		result.affectedMessages += cycleCount * 4
	}

	return result
}

// countFileRetries counts how many times each file was edited.
// Returns only files with edits >= minRetries.
func countFileRetries(messages []any, minRetries int) map[string]int {
	fileCounts := make(map[string]int)

	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if fp := extractEditFilePath(msg); fp != "" {
			fileCounts[fp]++
		}
	}

	// Filter to only excessive retries
	result := make(map[string]int)
	for fp, count := range fileCounts {
		if count >= minRetries {
			result[fp] = count
		}
	}
	return result
}

// --- Message inspection helpers ---

func hasErrorInToolResult(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_result" && b["is_error"] == true {
			return true
		}
	}
	return false
}

func isToolResultMsg(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_result" {
			return true
		}
	}
	return false
}

func isEditToolUse(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			name, _ := b["name"].(string)
			if name == "Edit" || name == "Write" {
				return true
			}
		}
	}
	return false
}

func isBuildToolUse(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			name, _ := b["name"].(string)
			if name == "Bash" {
				input, _ := b["input"].(map[string]any)
				cmd, _ := input["command"].(string)
				if isBuildCommand(cmd) {
					return true
				}
			}
		}
	}
	return false
}

func isBuildCommand(cmd string) bool {
	lower := strings.ToLower(cmd)
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return false
	}
	first := fields[0]
	return first == "make" ||
		strings.Contains(lower, "go build") ||
		strings.Contains(lower, "go test") ||
		strings.Contains(lower, "npm run build") ||
		strings.Contains(lower, "cargo build") ||
		first == "tsc" ||
		first == "gcc" ||
		first == "g++"
}

func extractEditFilePath(msg map[string]any) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			name, _ := b["name"].(string)
			if name == "Edit" || name == "Write" {
				input, _ := b["input"].(map[string]any)
				fp, _ := input["file_path"].(string)
				return fp
			}
		}
	}
	return ""
}

// persistFixationRatio writes the fixation ratio to the daemon for session storage.
func (s *Server) persistFixationRatio(threadID string, ratio float64) {
	if threadID == "" {
		return
	}
	_, err := s.queryDaemon("update_fixation_ratio", map[string]any{
		"session_id":      threadID,
		"fixation_ratio":  ratio,
	})
	if err != nil && s.logger != nil {
		s.logger.Printf("  fixation: failed to persist ratio for %s: %v", threadID[:8], err)
	}
}
