package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

// stalenessDecision is the LLM's verdict on a single learning.
type stalenessDecision struct {
	ID     int64  `json:"id"`
	Action string `json:"action"` // "stale" or "valid"
	Reason string `json:"reason"`
}

func (h *Handler) handleInvalidateOnCommit(params map[string]any) Response {
	hash := stringOr(params, "hash", "")
	project := stringOr(params, "project", "")
	workdir := stringOr(params, "workdir", "")
	if hash == "" {
		return errorResponse("hash required")
	}
	if h.CommitEvalClient == nil {
		return errorResponse("no LLM client for commit evaluation")
	}

	// Step 1: Get changed files from git
	changedFiles := gitChangedFiles(hash, workdir)
	if len(changedFiles) == 0 {
		return jsonResponse(map[string]any{"affected": 0, "reason": "no changed files"})
	}

	// Step 2: Find affected learnings by entity match
	affected, err := h.store.FindLearningsByEntityMatch(changedFiles, project)
	if err != nil {
		return errorResponse(fmt.Sprintf("entity search: %v", err))
	}
	if len(affected) == 0 {
		return jsonResponse(map[string]any{"affected": 0, "checked_files": len(changedFiles)})
	}

	// Step 3: Get truncated diff
	diff := gitDiffTruncated(hash, workdir, 4000)
	if diff == "" {
		return jsonResponse(map[string]any{"affected": len(affected), "resolved": 0, "reason": "empty diff"})
	}

	// Step 4: LLM evaluation
	decisions, err := evaluateStaleness(h.CommitEvalClient, diff, affected)
	if err != nil {
		log.Printf("[commit-invalidate] LLM eval failed for %s: %v", hash, err)
		return errorResponse(fmt.Sprintf("LLM evaluation: %v", err))
	}

	// Step 5: Apply decisions
	var resolvedIDs []int64
	for _, d := range decisions {
		if d.Action == "stale" {
			shortHash := hash
			if len(shortHash) > 7 {
				shortHash = shortHash[:7]
			}
			reason := fmt.Sprintf("invalidated by commit %s: %s", shortHash, d.Reason)
			if err := h.store.ResolveLearning(d.ID, reason); err != nil {
				log.Printf("[commit-invalidate] resolve %d: %v", d.ID, err)
			} else {
				resolvedIDs = append(resolvedIDs, d.ID)
			}
		}
	}

	if len(resolvedIDs) > 0 {
		go h.onMutation()
		log.Printf("[commit-invalidate] %s: %d stale, %d valid (of %d affected)",
			hash[:min(7, len(hash))], len(resolvedIDs), len(affected)-len(resolvedIDs), len(affected))
	}

	return jsonResponse(map[string]any{
		"affected": len(affected),
		"resolved": len(resolvedIDs),
		"hash":     hash,
	})
}

// gitChangedFiles returns basenames of files changed in a commit.
func gitChangedFiles(hash, workdir string) []string {
	args := []string{"show", hash, "--stat", "--name-only", "--pretty=format:"}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[commit-invalidate] git show failed: %v", err)
		return nil
	}
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		base := filepath.Base(line)
		if base != "." && !seen[base] {
			seen[base] = true
			files = append(files, base)
		}
	}
	return files
}

// gitDiffTruncated returns the diff for a commit, truncated to maxBytes.
func gitDiffTruncated(hash, workdir string, maxBytes int) string {
	args := []string{"diff", hash + "~1.." + hash}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[commit-invalidate] git diff failed: %v", err)
		return ""
	}
	s := string(out)
	if len(s) > maxBytes {
		s = s[:maxBytes] + "\n... (truncated)"
	}
	return s
}

// evaluateStaleness asks an LLM to evaluate which learnings are invalidated by a diff.
func evaluateStaleness(client extraction.LLMClient, diff string, learnings []models.Learning) ([]stalenessDecision, error) {
	system := `You evaluate whether existing knowledge about a codebase is still valid after a code change.
For each learning, decide: "stale" (no longer accurate due to this change) or "valid" (still accurate).
Only mark "stale" if the change DIRECTLY contradicts or invalidates the learning content.
Respond ONLY with a JSON array: [{"id": <int>, "action": "stale"|"valid", "reason": "<brief>"}]`

	var learningLines []string
	for _, l := range learnings {
		content := l.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		learningLines = append(learningLines, fmt.Sprintf("[ID:%d] %s", l.ID, content))
	}

	userMsg := fmt.Sprintf("## Code Diff\n%s\n\n## Potentially Affected Learnings\n%s\n\nWhich learnings are invalidated by this change?",
		diff, strings.Join(learningLines, "\n"))

	response, err := client.Complete(system, userMsg, extraction.WithMaxTokens(1024))
	if err != nil {
		return nil, err
	}

	// Parse JSON from response (may contain markdown fences)
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var decisions []stalenessDecision
	if err := json.Unmarshal([]byte(response), &decisions); err != nil {
		return nil, fmt.Errorf("parse LLM response: %w (raw: %s)", err, response[:min(200, len(response))])
	}
	return decisions, nil
}
