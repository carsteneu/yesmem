package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// FailureInput represents the JSON Claude Code sends for PostToolUseFailure.
// Also supports PostToolUse format via ToolResponse field.
type FailureInput struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolOutput    string          `json:"tool_output"`
	ToolResponse  json.RawMessage `json:"tool_response"`
	Error         string          `json:"error"`
}

// RunLearn reads PostToolUseFailure JSON from stdin and stores a new gotcha.
// Always exits 0.
func RunLearn(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook FailureInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	if hook.ToolName != "Bash" {
		return
	}

	var bash BashInput
	if json.Unmarshal(hook.ToolInput, &bash) != nil {
		return
	}

	if bash.Command == "" || hook.ToolOutput == "" {
		return
	}

	// Truncate long outputs
	errorOutput := hook.ToolOutput
	if len(errorOutput) > 200 {
		errorOutput = errorOutput[:200] + "..."
	}

	content := fmt.Sprintf("Bash error: `%s` → %s", truncateCmd(bash.Command, 120), errorOutput)

	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		return
	}
	defer store.Close()

	// Dedup: check similarity against existing gotchas
	existing, _ := store.GetActiveLearnings("gotcha", "", "", "")
	newTokens := textutil.Tokenize(content)

	for _, g := range existing {
		if textutil.TokenSimilarity(newTokens, textutil.Tokenize(g.Content)) >= 0.5 {
			// Already known — bump match count instead
			store.IncrementMatchCounts([]int64{g.ID})
			return
		}
	}

	learning := &models.Learning{
		SessionID:  hook.SessionID,
		Category:   "gotcha",
		Content:    content,
		Confidence: 0.7,
		CreatedAt:  time.Now(),
		ModelUsed:  "hook-auto",
		Source:     "hook_auto_learned",
	}
	store.InsertLearning(learning)
}

func truncateCmd(cmd string, maxLen int) string {
	cmd = strings.TrimSpace(cmd)
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen] + "..."
}
