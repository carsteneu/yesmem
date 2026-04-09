package proxy

import "regexp"

// commitPattern matches git commit output: [branch hash] message
var commitPattern = regexp.MustCompile(`\[([^\]]+)\s+([0-9a-f]{7,40})\]\s+(.+)`)

// CommitInfo holds parsed data from a git commit output.
type CommitInfo struct {
	Hash    string
	Branch  string
	Message string
}

// detectGitCommit scans the last user message's tool_result blocks
// for git commit output. Returns nil if no commit detected.
func detectGitCommit(messages []any) *CommitInfo {
	// Scan backwards for last user message
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] != "user" {
			continue
		}

		// Check content blocks for tool_result
		blocks, ok := msg["content"].([]any)
		if !ok {
			// String content — no tool_result
			break
		}

		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] != "tool_result" {
				continue
			}

			text := extractToolResultText(b)
			if m := commitPattern.FindStringSubmatch(text); m != nil {
				return &CommitInfo{
					Branch:  m[1],
					Hash:    m[2],
					Message: m[3],
				}
			}
		}
		break // only check the last user message
	}
	return nil
}

// extractToolResultText is defined in compress_context.go
