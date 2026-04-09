package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runBriefing() {
	dataDir := yesmemDataDir()

	// Load config for briefing settings
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	// Apply language settings from config
	if len(cfg.Briefing.Languages) > 0 {
		briefing.SetLanguages(cfg.Briefing.Languages)
	}
	briefing.SetStringsPath(filepath.Join(dataDir, "strings.yaml"))

	// Parse flags: --project, --recover, --source
	project := os.Getenv("PWD")
	var recoverSessionID, source string
	for i, arg := range os.Args {
		if arg == "--project" && i+1 < len(os.Args) {
			project = os.Args[i+1]
		}
		if arg == "--recover" && i+1 < len(os.Args) {
			recoverSessionID = os.Args[i+1]
		}
		if arg == "--source" && i+1 < len(os.Args) {
			source = os.Args[i+1]
		}
	}

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	gen := briefing.New(store, cfg.Briefing.DetailedSessions)
	gen.SetMaxPerCategory(cfg.Briefing.MaxPerCategory)
	gen.SetDedupThreshold(cfg.Briefing.DedupThreshold)
	gen.SetUnfinishedTTL(cfg.Evolution.UnfinishedTTL)
	gen.SetUserProfile(cfg.Briefing.UserProfile)
	gen.SetStrings(briefing.ResolveStrings(filepath.Join(dataDir, "strings.yaml")))
	// Recovery: explicit session_id, tracked session, or heuristic fallback
	if recoverSessionID != "" {
		gen.SetRecovery(recoverSessionID, source)
	} else if source == "clear" || source == "compact" {
		if sid, err := store.GetLastEndedSession(project); err == nil && sid != "" {
			gen.SetRecovery(sid, source)
		} else if sessions, err := store.ListSessions(project, 1); err == nil && len(sessions) > 0 {
			gen.SetRecovery(sessions[0].ID, source)
		}
	}
	text := gen.Generate(project)

	// Post-process: use cached refined briefing if available, otherwise raw
	projectShort := filepath.Base(project)
	text = briefing.RefineBriefing(text, store, projectShort, nil)

	// Recovery block (post-refine so it survives refinement)
	if recovery := gen.GenerateRecovery(); recovery != "" {
		text = recovery + "\n" + text
	}

	// Inject pinned learnings (refinement-resistant, verbatim)
	sessionPins, _ := store.GetPinnedLearnings("session", projectShort)
	permanentPins, _ := store.GetPinnedLearnings("permanent", projectShort)
	pinnedBlock := briefing.FormatPinnedBlock(sessionPins, permanentPins)
	if pinnedBlock != "" {
		text = injectPinnedBlock(text, pinnedBlock)
	}

	// Inject open work reminder instruction (refinement-resistant, after refine pass)
	if cfg.Briefing.RemindOpenWork {
		if count, _ := store.CountActiveUnfinished(projectShort); count > 0 {
			s := briefing.ResolveStrings(filepath.Join(dataDir, "strings.yaml"))
			text += "\n\n" + fmt.Sprintf(s.OpenWorkRemind, projectShort) + "\n"
		}
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// runBriefingHook reads Claude Code's SessionStart hook JSON from stdin directly.
// No shell script wrapper needed — this replaces yesmem-briefing.sh.
func runBriefingHook() {
	// Auto-start proxy if not running
	ensureProxyRunning()

	// Parse hook input from stdin
	var hookInput struct {
		CWD       string `json:"cwd"`
		Source    string `json:"source"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&hookInput); err != nil {
		// Fallback: no stdin or bad JSON — just generate normal briefing
		hookInput.Source = "startup"
	}

	project := hookInput.CWD
	if project == "" {
		project = os.Getenv("CLAUDE_PROJECT_DIR")
		if project == "" {
			project = os.Getenv("PWD")
		}
	}

	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	if len(cfg.Briefing.Languages) > 0 {
		briefing.SetLanguages(cfg.Briefing.Languages)
	}
	briefing.SetStringsPath(filepath.Join(dataDir, "strings.yaml"))

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Detect agent session — suppress unfinished todos and reminder nudge.
	isAgentSession := false
	if hookInput.SessionID != "" {
		if agent, _ := store.AgentGetAnyBySession(hookInput.SessionID); agent != nil {
			isAgentSession = true
		}
	}

	gen := briefing.New(store, cfg.Briefing.DetailedSessions)
	gen.SetMaxPerCategory(cfg.Briefing.MaxPerCategory)
	gen.SetDedupThreshold(cfg.Briefing.DedupThreshold)
	gen.SetUnfinishedTTL(cfg.Evolution.UnfinishedTTL)
	gen.SetUserProfile(cfg.Briefing.UserProfile)
	if isAgentSession {
		gen.SetSkipUnfinished(true)
	}
	gen.SetStrings(briefing.ResolveStrings(filepath.Join(dataDir, "strings.yaml")))

	// Recovery: auto-detect clear/compact via recent session_tracking entry (30s window).
	// Also honors explicit source from hook input.
	if hookInput.Source == "clear" || hookInput.Source == "compact" {
		if sid, _, err := store.GetRecentEndedSession(project, 30*time.Second); err == nil && sid != "" {
			gen.SetRecovery(sid, hookInput.Source)
		} else if sid, err := store.GetLastEndedSession(project); err == nil && sid != "" {
			gen.SetRecovery(sid, hookInput.Source)
		}
	} else {
		// Normal startup — still check if a clear just happened (within 30s)
		if sid, reason, err := store.GetRecentEndedSession(project, 30*time.Second); err == nil && sid != "" {
			gen.SetRecovery(sid, reason)
		}
	}

	text := gen.Generate(project)

	// Post-process: use cached refined briefing if available, otherwise raw
	projectShort := filepath.Base(project)
	text = briefing.RefineBriefing(text, store, projectShort, nil)

	// Recovery block (post-refine so it survives refinement)
	if recovery := gen.GenerateRecovery(); recovery != "" {
		text = recovery + "\n" + text
	}

	// Inject pinned learnings (refinement-resistant, verbatim)
	sessionPins, _ := store.GetPinnedLearnings("session", projectShort)
	permanentPins, _ := store.GetPinnedLearnings("permanent", projectShort)
	pinnedBlock := briefing.FormatPinnedBlock(sessionPins, permanentPins)
	if pinnedBlock != "" {
		text = injectPinnedBlock(text, pinnedBlock)
	}

	// Inject open work reminder instruction — skip for agent sessions.
	if cfg.Briefing.RemindOpenWork && !isAgentSession {
		if count, _ := store.CountActiveUnfinished(projectShort); count > 0 {
			s := briefing.ResolveStrings(filepath.Join(dataDir, "strings.yaml"))
			text += "\n\n" + fmt.Sprintf(s.OpenWorkRemind, projectShort) + "\n"
		}
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// injectPinnedBlock inserts the pinned block between prose and toolsBlock.
func injectPinnedBlock(text, pinnedBlock string) string {
	markers := []string{
		"Die Zeitstempel in den Nachrichten",
		"So funktioniert mein Gedächtnis",
		"The timestamps in the messages",
		"How my memory works",
	}
	for _, m := range markers {
		if idx := strings.Index(text, m); idx >= 0 {
			start := idx
			for start > 0 && (text[start-1] == '\n' || text[start-1] == '-' || text[start-1] == ' ') {
				start--
			}
			return text[:start] + "\n" + pinnedBlock + "\n" + text[start:]
		}
	}
	return text + "\n" + pinnedBlock
}

func runMicroReminder() {
	reminder := `You have a long-term memory (yesmem). For EVERY non-trivial task (implementation, debugging, architecture, configuration, error analysis): FIRST search(topic) — get context before you start, alternatively use hybrid_search for associative search. When you discover something important (bug, decision, user preference): remember(text, category). Categories: gotcha, decision, pattern, preference, explicit_teaching. More tools: deep_search, related_to_file, get_session, get_learnings.`

	out := map[string]string{"additionalContext": reminder}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}
