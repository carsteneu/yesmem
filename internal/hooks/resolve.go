package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
)

// RunResolveCheck reads PostToolUse JSON from stdin.
// If the tool was a git commit, checks the commit message against
// active unfinished items and resolves matches.
// Always exits 0 (never blocks).
func RunResolveCheck(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook HookInput
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

	// Only act on git commit commands
	if !isGitCommit(bash.Command) {
		return
	}

	// Extract commit message from command
	msg := extractCommitMessage(bash.Command)
	if msg == "" {
		return
	}

	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		return
	}
	defer store.Close()

	project, _ := os.Getwd()
	result := resolveMatchingTask(store, msg, project)
	if result == "" {
		outputHookResult(fmt.Sprintf("hook-resolve: commit detected, no matching open tasks (msg: %s)", truncate(msg, 60)))
		return
	}
	outputHookResult(result)
}

// resolveMatchingTask searches for an unfinished item matching the commit message
// within the given project and resolves it. Returns the output message or "".
func resolveMatchingTask(store *storage.Store, msg, project string) string {
	matches, err := store.SearchUnfinished(msg, project)
	if err != nil || len(matches) == 0 {
		return ""
	}
	best := matches[0]
	reason := "git commit: " + msg
	if len(reason) > 200 {
		reason = reason[:200]
	}
	if err := store.ResolveLearning(best.ID, reason); err != nil {
		return ""
	}
	return fmt.Sprintf("hook-resolve: auto-resolved #%d (%s) via commit", best.ID, truncate(best.Content, 80))
}

// outputHookResult writes a JSON response that Claude Code displays as hook output.
func outputHookResult(text string) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// isGitCommit checks if a bash command is a git commit.
func isGitCommit(cmd string) bool {
	return strings.Contains(cmd, "git commit")
}

// extractCommitMessage tries to extract the -m message from a git commit command.
func extractCommitMessage(cmd string) string {
	// Handle heredoc pattern: git commit -m "$(cat <<'EOF'\n...\nEOF\n)"
	if idx := strings.Index(cmd, "<<'EOF'"); idx != -1 {
		start := idx + len("<<'EOF'")
		// Find content between <<'EOF' and EOF
		rest := cmd[start:]
		if end := strings.Index(rest, "EOF"); end != -1 {
			msg := strings.TrimSpace(rest[:end])
			return msg
		}
	}

	// Handle simple -m "message" or -m 'message'
	for _, flag := range []string{"-m ", "-m\t"} {
		idx := strings.Index(cmd, flag)
		if idx == -1 {
			continue
		}
		rest := strings.TrimSpace(cmd[idx+len(flag):])
		if len(rest) == 0 {
			continue
		}

		// Quoted message
		if rest[0] == '"' || rest[0] == '\'' {
			quote := rest[0]
			end := strings.IndexByte(rest[1:], quote)
			if end != -1 {
				return rest[1 : end+1]
			}
		}

		// Unquoted — take until next space or end
		if end := strings.IndexByte(rest, ' '); end != -1 {
			return rest[:end]
		}
		return rest
	}

	return ""
}
