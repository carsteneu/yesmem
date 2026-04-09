package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/hints"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// HookInput represents the JSON Claude Code sends on stdin for hook events.
type HookInput struct {
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
}

// BashInput represents the tool_input for Bash tool calls.
type BashInput struct {
	Command string `json:"command"`
}

// FileInput represents the tool_input for Edit/Write tool calls.
type FileInput struct {
	FilePath string `json:"file_path"`
}

// WebFetchInput represents the tool_input for WebFetch tool calls.
type WebFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

// buildWebFetchKeywords extracts matchable keywords from a WebFetch URL.
// Only uses the host — the tool name "webfetch" is excluded because it's
// always present for any WebFetch call (like "bash" for Bash), making it
// useless as a discriminator and causing false positive matches.
func buildWebFetchKeywords(rawURL string) []string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return extractKeywords(u.Host)
	}
	return nil
}

// matchedGotcha holds a gotcha with its match score for sorting.
type matchedGotcha struct {
	learning models.Learning
	score    int
}

// blockThreshold is the hit_count at which a gotcha escalates from warn to hard block.
const blockThreshold = 5

// RunCheck reads PreToolUse JSON from stdin, queries gotchas, outputs warning.
// Supports Bash, Edit, and Write tool calls.
// Gotchas with hit_count >= blockThreshold are hard-blocked (exit 2).
func RunCheck(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook HookInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	// Parse tool input to get inputStr (for save_count) and keywords (for matching)
	var keywords []string
	var inputStr string
	isFileOp := false
	switch hook.ToolName {
	case "send_to", "start_dialog", "end_dialog":
		// Dialog tools: register session_id so daemon knows who's calling
		// PreToolUse fires BEFORE the MCP call → daemon has the ID when send_to arrives
		if hook.SessionID != "" {
			registerPID(dataDir, hook.SessionID, os.Getppid())
			writePIDFile(dataDir, hook.SessionID, os.Getppid())
		}
		emitReminder("")
		return
	case "Bash":
		var bash BashInput
		if json.Unmarshal(hook.ToolInput, &bash) != nil || bash.Command == "" {
			return
		}
		inputStr = bash.Command
		keywords = extractKeywords(bash.Command)
	case "Edit", "Write":
		var file FileInput
		if json.Unmarshal(hook.ToolInput, &file) != nil || file.FilePath == "" {
			return
		}
		// Block edits to auto-generated files
		if isProtectedFile(file.FilePath) {
			blockEdit(file.FilePath)
			return
		}
		inputStr = file.FilePath
		keywords = extractPathKeywords(file.FilePath)
		isFileOp = true
	case "WebFetch":
		var wf WebFetchInput
		if json.Unmarshal(hook.ToolInput, &wf) != nil || wf.URL == "" {
			return
		}
		if u, err := url.Parse(wf.URL); err == nil && u.Host != "" {
			inputStr = "WebFetch " + u.Host
		} else {
			inputStr = "WebFetch"
		}
		keywords = buildWebFetchKeywords(wf.URL)
	default:
		return
	}

	if len(keywords) == 0 {
		emitReminder("")
		return
	}

	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		emitReminder("")
		return
	}
	defer store.Close()

	// Check if previous gotcha prevented an error (save_count heuristic)
	if inputStr != "" {
		checkSaveCount(store, hook.ToolName, hashInput(inputStr))
	}

	gotchas, err := store.GetActiveLearnings("gotcha", "", "", "")
	if err != nil || len(gotchas) == 0 {
		emitReminder("")
		return
	}

	// Batch-load V2 junction data (entities + actions) for all gotchas
	ids := make([]int64, len(gotchas))
	for i, g := range gotchas {
		ids[i] = g.ID
	}
	entitiesMap := store.BatchLoadEntities(ids)
	actionsMap := store.BatchLoadActions(ids)
	for i := range gotchas {
		if ents, ok := entitiesMap[gotchas[i].ID]; ok {
			gotchas[i].Entities = ents
		}
		if acts, ok := actionsMap[gotchas[i].ID]; ok {
			gotchas[i].Actions = acts
		}
	}

	// Derive project name from cwd
	project := projectFromCWD(hook.CWD)

	// Split matches into project-specific and global buckets
	// File ops use lower threshold: 1 match with filename (contains ".") suffices
	var projectMatches, globalMatches []matchedGotcha
	for _, g := range gotchas {
		score := matchScore(keywords, g.Content)
		matched := false
		if isFileOp {
			// For file ops: 1 match on a filename-like keyword (contains ".") or 2+ any matches
			matched = score >= 2 || (score >= 1 && hasFileKeywordMatch(keywords, g.Content))
		} else {
			// For bash: 2+ matches or 1 long keyword match (≥6 chars)
			matched = score >= 2 || (score >= 1 && hasLongKeywordMatch(keywords, g.Content))
		}
		// V2: entity/action matching (much more precise)
		if !matched && g.IsV2() {
			// Direct entity match
			for _, entity := range g.Entities {
				entityLower := strings.ToLower(entity)
				for _, kw := range keywords {
					if strings.Contains(entityLower, kw) || strings.Contains(kw, entityLower) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			// Direct action match
			if !matched {
				for _, action := range g.Actions {
					actionLower := strings.ToLower(action)
					for _, kw := range keywords {
						if strings.Contains(actionLower, kw) {
							matched = true
							break
						}
					}
					if matched {
						break
					}
				}
			}
		}
		if !matched {
			continue
		}
		mg := matchedGotcha{learning: g, score: score}
		if project != "" && models.ProjectMatches(g.Project, project) {
			projectMatches = append(projectMatches, mg)
		} else {
			globalMatches = append(globalMatches, mg)
		}
	}

	// Limits: 3 project + 2 global, if 0 project → 3 global
	maxProject := 3
	maxGlobal := 2
	if len(projectMatches) == 0 {
		maxGlobal = 3
	}

	if len(projectMatches) > maxProject {
		projectMatches = projectMatches[:maxProject]
	}
	if len(globalMatches) > maxGlobal {
		globalMatches = globalMatches[:maxGlobal]
	}

	if len(projectMatches) == 0 && len(globalMatches) == 0 {
		emitReminder("")
		return
	}

	// Check for block-worthy gotchas (hit_count >= threshold)
	allMatches := append(projectMatches, globalMatches...)
	if bg := findBlockableGotcha(allMatches); bg != nil {
		store.IncrementMatchCounts([]int64{bg.learning.ID})
		store.IncrementInjectCounts([]int64{bg.learning.ID})
		blockGotchaCall(bg.learning)
		return
	}

	// Collect hit IDs and build output
	var hitIDs []int64
	var lines []string

	if len(projectMatches) > 0 {
		lines = append(lines, fmt.Sprintf("[%s]", project))
		for _, mg := range projectMatches {
			lines = append(lines, "- "+mg.learning.Content)
			hitIDs = append(hitIDs, mg.learning.ID)
		}
	}

	if len(globalMatches) > 0 {
		if len(projectMatches) > 0 {
			lines = append(lines, "---")
			lines = append(lines, "[uebergreifend]")
		}
		for _, mg := range globalMatches {
			lines = append(lines, "- "+mg.learning.Content)
			hitIDs = append(hitIDs, mg.learning.ID)
		}
	}

	// Bump match + inject counts (fire-and-forget)
	store.IncrementMatchCounts(hitIDs)
	store.IncrementInjectCounts(hitIDs)

	// Persist state for save_count heuristic (one-shot, checked on next RunCheck)
	if len(hitIDs) > 0 {
		idsJSON, _ := json.Marshal(hitIDs)
		store.SetProxyState("last_gotcha_ids", string(idsJSON))
		store.SetProxyState("last_gotcha_tool", hook.ToolName)
		store.SetProxyState("last_gotcha_input_hash", hashInput(inputStr))
	} else {
		store.SetProxyState("last_gotcha_ids", "")
	}

	text := "YesMem Gotchas:\n" + strings.Join(lines, "\n")
	emitReminder(text)
}

// hashInput returns a deterministic 16-char hex hash of the input string.
func hashInput(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:8])
}

// checkSaveCount checks if a previous gotcha warning prevented an error.
// If the same tool type was used but with a DIFFERENT input hash, the user
// changed their approach after seeing the warning → save_count++.
// State is always cleared after check (one-shot).
func checkSaveCount(store *storage.Store, toolName, inputHash string) {
	prevIDs, _ := store.GetProxyState("last_gotcha_ids")
	if prevIDs == "" || prevIDs == "[]" {
		return
	}
	prevTool, _ := store.GetProxyState("last_gotcha_tool")
	prevHash, _ := store.GetProxyState("last_gotcha_input_hash")

	// Same tool type but different input → user changed approach → save!
	if toolName == prevTool && inputHash != prevHash {
		var ids []int64
		json.Unmarshal([]byte(prevIDs), &ids)
		if len(ids) > 0 {
			store.IncrementSaveCounts(ids)
		}
	}
	// Clear after check (one-shot — regardless of outcome)
	store.SetProxyState("last_gotcha_ids", "")
}

// emitReminder outputs the PreToolUse JSON with gotcha warnings.
// Silent when gotchaText is empty — no generic reminder injection.
func emitReminder(gotchaText string) {
	if gotchaText == "" {
		return
	}
	tsHint := hints.NextTimestampHint()
	text := gotchaText + "\n---\n" + tsHint
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// protectedFiles lists auto-generated files that must not be edited directly.
// Fix the source (learnings via remember/supersede) instead.
var protectedFiles = []string{
	"yesmem-ops.md",
}

// isProtectedFile checks if a file path matches any protected auto-generated file.
func isProtectedFile(filePath string) bool {
	base := filepath.Base(filePath)
	for _, p := range protectedFiles {
		if base == p {
			return true
		}
	}
	return false
}

// blockMinScore is the minimum match score for a gotcha to escalate to a hard block.
// Warning threshold is score >= 2; blocking requires stronger evidence to avoid
// cross-matching unrelated commands that share generic keywords.
const blockMinScore = 4

// findBlockableGotcha returns the first match whose fail_count >= blockThreshold,
// match score >= blockMinScore, AND was auto-learned from an actual failure, or nil.
// Uses fail_count (real failures) not hit_count (view count from warnings).
func findBlockableGotcha(matches []matchedGotcha) *matchedGotcha {
	for i := range matches {
		if matches[i].score >= blockMinScore && matches[i].learning.FailCount >= blockThreshold && matches[i].learning.Source == "hook_auto_learned" {
			return &matches[i]
		}
	}
	return nil
}

// blockGotchaCall outputs a block response for a gotcha that exceeded the fail threshold.
func blockGotchaCall(g models.Learning) {
	reason := fmt.Sprintf(
		"BLOCKED: This error occurred %dx — automatic block.\n"+
			"Gotcha: %s\n"+
			"Use hybrid_search() to find alternatives, "+
			"or resolve_by_text() if the issue is resolved.",
		g.FailCount, g.Content,
	)
	out := map[string]any{
		"decision": "block",
		"reason":   reason,
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
	os.Exit(2)
}

// blockEdit outputs a JSON response that blocks the tool call (exit 2).
func blockEdit(filePath string) {
	reason := fmt.Sprintf(
		"BLOCKED: %s is auto-generated from learnings. "+
			"Use remember() with supersedes parameter to update the source learning, "+
			"then regenerate via 'yesmem claudemd'.",
		filepath.Base(filePath),
	)
	out := map[string]any{
		"decision": "block",
		"reason":   reason,
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
	os.Exit(2)
}

// projectFromCWD extracts the project name from a working directory path.
// Uses filepath.Base, matching how yesmem indexes sessions.
func projectFromCWD(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}
