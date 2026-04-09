package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// harmlessCommands: exit code != 0 is expected/normal for these.
var harmlessCommands = []string{"grep", "diff", "test ", "[ ", "rg "}

// RunAssist reads PostToolUseFailure JSON from stdin, queries daemon hybrid_search
// (FTS5 + vector over Learnings), and outputs matching knowledge as additionalContext.
//
// Cognitive role: PRIMING — activates relevant semantic knowledge before the agent
// reasons about the error.
func RunAssist(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	// FailureInput already defined in learn.go
	var hook FailureInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	if hook.ToolName != "Bash" {
		return
	}

	var bash BashInput // defined in check.go
	if json.Unmarshal(hook.ToolInput, &bash) != nil {
		return
	}

	if bash.Command == "" {
		return
	}

	if isHarmlessExit(bash.Command, hook.ToolOutput) {
		return
	}

	query := buildSearchQuery(bash.Command, hook.ToolOutput)
	if query == "" {
		return
	}

	client, err := daemon.Dial(dataDir)
	if err != nil {
		return // Daemon unreachable — silent exit
	}
	defer client.Close()

	result, err := client.Call("hybrid_search", map[string]any{
		"query": query,
		"limit": float64(3),
	})
	if err != nil {
		return
	}

	// Parse hybrid_search response: {results: [{id, content, score, source}]}
	var wrapped struct {
		Results []struct {
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}
	if json.Unmarshal(result, &wrapped) != nil {
		return
	}

	var lines []string
	for _, r := range wrapped.Results {
		if r.Score < 0.01 {
			continue
		}
		snippet := r.Content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		snippet = stripANSI(snippet)
		lines = append(lines, "- "+snippet)
	}

	if len(lines) == 0 {
		return
	}

	text := "YesMem knows similar issues:\n" + strings.Join(lines, "\n")
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// isHarmlessExit returns true for commands where exit code != 0 is expected.
func isHarmlessExit(cmd, output string) bool {
	trimmed := strings.TrimSpace(cmd)
	lower := strings.ToLower(trimmed)
	for _, prefix := range harmlessCommands {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// buildSearchQuery creates a search query from command + error output.
func buildSearchQuery(cmd, errorOutput string) string {
	cmdKW := extractKeywords(cmd)
	errKW := extractErrorKeywords(errorOutput, 5)

	all := append(cmdKW, errKW...)
	if len(all) == 0 {
		return ""
	}
	query := strings.Join(all, " ")
	if len(query) > 200 {
		query = query[:200]
	}
	return query
}

// extractErrorKeywords gets the most significant words from the first line of error output.
func extractErrorKeywords(errMsg string, maxWords int) []string {
	if errMsg == "" {
		return nil
	}
	lines := strings.SplitN(errMsg, "\n", 2)
	words := extractKeywords(lines[0])
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	return words
}
