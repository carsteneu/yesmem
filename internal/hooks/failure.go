package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// RunFailure combines hook-learn + hook-assist into a single hook.
// Reads stdin once, runs learn (store gotcha) then assist (deep search).
// Supports all tool types: Bash, WebFetch, Read, Grep, etc.
// Also supports PostToolUse format (tool_response instead of tool_output).
func RunFailure(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook FailureInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	normalizeToolOutput(&hook)

	if hook.ToolOutput == "" {
		return
	}

	// Extract tool-specific context for search query
	toolContext := extractToolContext(&hook)
	if toolContext == "" {
		return
	}

	// --- Phase 1: Learn (store gotcha) ---
	learnFromAnyFailure(dataDir, &hook, toolContext)

	// --- Phase 2: Assist (deep search for context) ---
	if hook.ToolName == "Bash" {
		var bash BashInput
		if json.Unmarshal(hook.ToolInput, &bash) == nil && isHarmlessExit(bash.Command, hook.ToolOutput) {
			return
		}
	}
	assistFromAnyFailure(dataDir, &hook, toolContext)
}

// extractToolContext builds a human-readable context string from any tool failure.
func extractToolContext(hook *FailureInput) string {
	switch hook.ToolName {
	case "Bash":
		var bash BashInput
		if json.Unmarshal(hook.ToolInput, &bash) != nil || bash.Command == "" {
			return ""
		}
		return bash.Command
	case "WebFetch":
		var input struct {
			URL    string `json:"url"`
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(hook.ToolInput, &input) != nil {
			return ""
		}
		if u, err := url.Parse(input.URL); err == nil && u.Host != "" {
			return "WebFetch " + u.Host
		}
		return "WebFetch"
	case "Read":
		var input struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(hook.ToolInput, &input) != nil {
			return ""
		}
		return input.FilePath
	case "Grep":
		var input struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if json.Unmarshal(hook.ToolInput, &input) != nil {
			return ""
		}
		return input.Pattern + " " + input.Path
	default:
		// Generic: use tool name + first 120 chars of output
		return hook.ToolName
	}
}

func learnFromAnyFailure(dataDir string, hook *FailureInput, toolContext string) {
	errorOutput := hook.ToolOutput
	if len(errorOutput) > 200 {
		errorOutput = errorOutput[:200] + "..."
	}

	content := fmt.Sprintf("%s error: `%s` → %s", hook.ToolName, truncateCmd(toolContext, 120), errorOutput)

	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		return
	}
	defer store.Close()

	existing, _ := store.GetActiveLearnings("gotcha", "", "", "")
	newTokens := textutil.Tokenize(content)

	for _, g := range existing {
		if textutil.TokenSimilarity(newTokens, textutil.Tokenize(g.Content)) >= 0.5 {
			store.IncrementMatchCounts([]int64{g.ID})
			store.IncrementFailCounts([]int64{g.ID})
			return
		}
	}

	store.InsertLearning(&models.Learning{
		SessionID:  hook.SessionID,
		Category:   "gotcha",
		Content:    content,
		Confidence: 0.7,
		CreatedAt:  time.Now(),
		ModelUsed:  "hook-auto",
		Source:     "hook_auto_learned",
	})
}

func assistFromAnyFailure(dataDir string, hook *FailureInput, toolContext string) {
	query := buildSearchQuery(toolContext, hook.ToolOutput)
	if query == "" {
		return
	}

	client, err := daemon.Dial(dataDir)
	if err != nil {
		return
	}
	defer client.Close()

	result, err := client.Call("hybrid_search", map[string]any{
		"query": query,
		"limit": float64(3),
	})
	if err != nil {
		return
	}

	// Parse hybrid_search response: {results: [{content, score, ...}]}
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

	text := "[YesMem Assist] Similar known issues:\n" + strings.Join(lines, "\n")
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUseFailure",
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// normalizeToolOutput fills ToolOutput from ToolResponse when empty.
// This handles PostToolUse format where errors come in tool_response.
func normalizeToolOutput(hook *FailureInput) {
	if hook.ToolOutput != "" {
		return
	}
	// WebFetch/WebSearch PostToolUseFailure: CC sends "error" not "tool_output"
	if hook.Error != "" {
		hook.ToolOutput = hook.Error
		return
	}
	// PostToolUse format: tool_response with error field
	if len(hook.ToolResponse) == 0 {
		return
	}
	// Try {"error": "..."} object
	var resp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(hook.ToolResponse, &resp) == nil && resp.Error != "" {
		hook.ToolOutput = resp.Error
		return
	}
	// Try plain string with error pattern
	var s string
	if json.Unmarshal(hook.ToolResponse, &s) == nil && containsErrorPattern(s) {
		hook.ToolOutput = s
	}
}

var errorPatterns = []string{"unable to fetch", "blocked", "403", "request failed", "cannot fetch"}

func containsErrorPattern(s string) bool {
	lower := strings.ToLower(s)
	for _, p := range errorPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
