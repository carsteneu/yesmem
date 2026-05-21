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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
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
	effScore float64
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
			RegisterPID(dataDir, hook.SessionID, os.Getppid())
			WritePIDFile(dataDir, hook.SessionID, os.Getppid())
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
	case "REPL":
		var repl struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(hook.ToolInput, &repl) != nil || repl.Code == "" {
			return
		}
		inputStr = repl.Code
		keywords = extractKeywords(repl.Code)
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

	gotchas, err := store.GetActiveLearnings("gotcha", "", "", "", 0)
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

	// Code-nav detection: block shell navigation of indexed project files
	if hook.ToolName == "Bash" && inputStr != "" {
		codeNavMode := "block"
		cfgPath := filepath.Join(dataDir, "config.yaml")
		if cfg, err := config.Load(cfgPath); err == nil && cfg.Proxy.CodeNavMode != "" {
			codeNavMode = cfg.Proxy.CodeNavMode
		}
		if codeNavMode != "off" && !store.IsCodeNavDismissed(hook.SessionID, 5) {
			if reason, block := CheckCodeNav(inputStr, hook.CWD, project, hook.SessionID, store.IsFileInCodeIndex, false); block {
				blockCodeNav(reason)
				return
			}
		}
	}

	if hook.ToolName == "REPL" && inputStr != "" {
		codeNavMode := "block"
		cfgPath := filepath.Join(dataDir, "config.yaml")
		if cfg, err := config.Load(cfgPath); err == nil && cfg.Proxy.CodeNavMode != "" {
			codeNavMode = cfg.Proxy.CodeNavMode
		}
		if codeNavMode != "off" && !store.IsCodeNavDismissed(hook.SessionID, 5) {
			for _, cmd := range ParseREPLNavCommands(inputStr) {
				if reason, block := CheckCodeNav(cmd, hook.CWD, project, hook.SessionID, store.IsFileInCodeIndex, false); block {
					blockCodeNav(reason)
					return
				}
			}
		}
	}

	// Split matches into project-specific and global buckets
	// File ops use lower threshold: 1 match with filename (contains ".") suffices
	var projectMatches, globalMatches []matchedGotcha
	for _, g := range gotchas {
		// Skip gotchas with destructive actions that aren't relevant to this tool type.
		if !isRelevantForTool(&g, hook.ToolName) {
			continue
		}
		score := matchScore(keywords, g.Content)
		decay := injectionDecay(g.InjectCount, g.UseCount, g.SaveCount)
		effScore := float64(score) * decay
		matched := false
		if isFileOp {
			matched = effScore >= 2.0 || (effScore >= 1.0 && hasFileKeywordMatch(keywords, g.Content))
		} else {
			matched = effScore >= 2.0 || (effScore >= 1.0 && hasLongKeywordMatch(keywords, g.Content))
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
		if effScore < 2.0 {
			effScore = 2.0
		}
		mg := matchedGotcha{learning: g, score: score, effScore: effScore}
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

	sort.Slice(projectMatches, func(i, j int) bool { return projectMatches[i].effScore > projectMatches[j].effScore })
	sort.Slice(globalMatches, func(i, j int) bool { return globalMatches[i].effScore > globalMatches[j].effScore })
	if len(projectMatches) > maxProject {
		projectMatches = projectMatches[:maxProject]
	}
	if len(globalMatches) > maxGlobal {
		globalMatches = globalMatches[:maxGlobal]
	}

	// Filter out old info gotchas that are already in the session-start briefing.
	// Keep: (1) failure-based (FailCount>0), (2) new since session start, (3) file-specific (entity matches input),
	// (4) bypass if no session data.
	if hook.SessionID != "" {
		if sess, err := store.GetSession(hook.SessionID); err == nil && !sess.StartedAt.IsZero() {
			projectMatches = filterAlreadyBriefedGotchas(projectMatches, sess.StartedAt, inputStr)
			globalMatches = filterAlreadyBriefedGotchas(globalMatches, sess.StartedAt, "")
		}
	}

	if len(projectMatches) == 0 && len(globalMatches) == 0 {
		emitReminder("")
		return
	}

	// Check for block-worthy gotchas (hit_count >= threshold)
	allMatches := append(projectMatches, globalMatches...)

	// Per-session cap: skip gotchas already shown 3+ times this session.
	// Reset on session restart (new SessionID → new keys). Global injectionDecay
	// still handles cross-session staleness independently.
	if hook.SessionID != "" && len(allMatches) > 0 {
		var capped []matchedGotcha
		for _, mg := range allMatches {
			key := fmt.Sprintf("gotcha_count:%s:%d", hook.SessionID, mg.learning.ID)
			countStr, _ := store.GetProxyState(key)
			count, _ := strconv.Atoi(countStr)
			if count >= 3 {
				continue
			}
			capped = append(capped, mg)
		}
		allMatches = capped
	}

	if bg := findBlockableGotcha(allMatches); bg != nil {
		store.IncrementMatchCounts([]int64{bg.learning.ID})
		store.IncrementInjectCounts([]int64{bg.learning.ID})
		blockGotchaCall(bg.learning)
		return
	}

	// Build tiered output: top-1 full text, rest as summary
	text, injectedIDs, matchedIDs := buildGotchaOutput(allMatches)

	// Bump counts: match for all, inject only for fully injected top match
	allIDs := make([]int64, 0, len(injectedIDs)+len(matchedIDs))
	allIDs = append(allIDs, injectedIDs...)
	allIDs = append(allIDs, matchedIDs...)
	store.IncrementMatchCounts(allIDs)
	store.IncrementInjectCounts(injectedIDs)

	// Persist state for save_count heuristic
	if len(injectedIDs) > 0 {
		idsJSON, _ := json.Marshal(injectedIDs)
		store.SetProxyState("last_gotcha_ids", string(idsJSON))
		store.SetProxyState("last_gotcha_tool", hook.ToolName)
		store.SetProxyState("last_gotcha_input_hash", hashInput(inputStr))

		// Per-session cap: increment injection counter for each injected gotcha
		for _, id := range injectedIDs {
			key := fmt.Sprintf("gotcha_count:%s:%d", hook.SessionID, id)
			countStr, _ := store.GetProxyState(key)
			count, _ := strconv.Atoi(countStr)
			store.SetProxyState(key, strconv.Itoa(count+1))
		}
	} else {
		store.SetProxyState("last_gotcha_ids", "")
	}

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

func buildGotchaOutput(matches []matchedGotcha) (text string, injectedIDs []int64, matchedIDs []int64) {
	if len(matches) == 0 {
		return "", nil, nil
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].effScore > matches[j].effScore })
	top := matches[0]
	injectedIDs = append(injectedIDs, top.learning.ID)
	var buf strings.Builder
	buf.WriteString("YesMem Gotchas:\n- ")
	buf.WriteString(top.learning.Content)
	if len(matches) > 1 {
		rest := matches[1:]
		for _, mg := range rest {
			matchedIDs = append(matchedIDs, mg.learning.ID)
		}
		buf.WriteString(fmt.Sprintf("\n+%d more — use query_facts(keyword=\"...\") for details", len(rest)))
	}
	return buf.String(), injectedIDs, matchedIDs
}

func emitReminder(gotchaText string) {
	if gotchaText == "" {
		return
	}
	tsHint := hints.NextTimestampHint()
	text := gotchaText + "\n---\n" + tsHint
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
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

// isRelevantForTool checks whether a gotcha's actions make it relevant for the current tool.
// Gotchas with destructive/bash-only actions (rm, mv, git push, make, deploy) only fire on Bash/Write/Edit.
// Gotchas with no actions are always relevant (backward compatible).
// Gotchas with non-destructive actions fire on all tools.
func isRelevantForTool(g *models.Learning, toolName string) bool {
	if len(g.Actions) == 0 {
		return true
	}
	// Tools that can perform destructive/file operations
	toolName = strings.ToLower(toolName)
	fileOpTools := map[string]bool{"bash": true, "write": true, "edit": true}
	if !fileOpTools[toolName] {
		for _, a := range g.Actions {
			al := strings.ToLower(a)
			if strings.Contains(al, "rm") || strings.Contains(al, "mv") ||
				strings.Contains(al, "git") || strings.Contains(al, "make") ||
				strings.Contains(al, "deploy") || strings.Contains(al, "cp") ||
				strings.Contains(al, "chmod") || strings.Contains(al, "chown") ||
				strings.Contains(al, "write") || strings.Contains(al, "edit") ||
				strings.Contains(al, "delete") || strings.Contains(al, "remove") {
				return false
			}
		}
	}
	return true
}

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

// blockCodeNav outputs a JSON response that blocks a shell navigation command
// when yesmem MCP tools should be used instead.
func blockCodeNav(reason string) {
	out := map[string]any{
		"decision": "block",
		"reason":   reason,
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
			"additionalContext": reason,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
	os.Exit(2)
}

// filterAlreadyBriefedGotchas removes gotchas that are already present in the
// session-start briefing. Keeps: failure-based gotchas (FailCount>0 — real errors),
// gotchas created since session start (new discoveries), and file-specific gotchas
// (entity matches the current input, e.g. file path for Edit/Write). Old info-level
// gotchas are redundant with the briefing and get skipped.
func filterAlreadyBriefedGotchas(matches []matchedGotcha, sessionStart time.Time, inputStr string) []matchedGotcha {
	var filtered []matchedGotcha
	for _, m := range matches {
		if m.learning.FailCount > 0 {
			filtered = append(filtered, m)
		} else if m.learning.CreatedAt.After(sessionStart) {
			filtered = append(filtered, m)
		} else if inputStr != "" && entitiesMatchInput(m.learning.Entities, inputStr) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// entitiesMatchInput returns true if any entity substring-matches the input.
func entitiesMatchInput(entities []string, input string) bool {
	inputLower := strings.ToLower(input)
	for _, e := range entities {
		el := strings.ToLower(e)
		if el != "" && (strings.Contains(inputLower, el) || strings.Contains(el, inputLower)) {
			return true
		}
	}
	return false
}

// projectFromCWD extracts the project name from a working directory path.
// Uses filepath.Base, matching how yesmem indexes sessions.
func projectFromCWD(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}
