package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

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
		text = briefing.InjectPinnedBlock(text, pinnedBlock)
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

// runBriefingHook ensures the proxy is running and returns a slim hint.
// The full briefing is injected by the proxy as a user/assistant turn.
func runBriefingHook() {
	ensureProxyRunning()

	// Drain stdin (Claude Code sends hook JSON)
	json.NewDecoder(os.Stdin).Decode(&json.RawMessage{})

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": "Your full session briefing has been injected as a conversation turn by the proxy. Read it carefully before responding.",
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// runCodemapHook generates only the Code Map as a separate SessionStart attachment.
// Split from briefing-hook so the Code Map is visible without preview truncation.
func runCodemapHook() {
	var hookInput struct {
		CWD       string `json:"cwd"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&hookInput); err != nil {
		hookInput.CWD = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	project := hookInput.CWD
	if project == "" {
		project = os.Getenv("CLAUDE_PROJECT_DIR")
		if project == "" {
			project = os.Getenv("PWD")
		}
	}
	if project == "" {
		return
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		return
	}
	defer store.Close()

	gen := briefing.New(store, 3)
	gen.Generate(project)
	cm := gen.CodeMap()
	if cm == "" {
		return
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": cm,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

// injectPinnedBlock inserts the pinned block between prose and toolsBlock.
func runMicroReminder() {
	reminder := `You have a long-term memory (yesmem). For EVERY non-trivial task (implementation, debugging, architecture, configuration, error analysis): FIRST search(topic) — get context before you start, alternatively use hybrid_search for associative search. When you discover something important (bug, decision, user preference): remember(text, category). Categories: gotcha, decision, pattern, preference, explicit_teaching. More tools: deep_search, related_to_file, get_session, get_learnings.`

	out := map[string]string{"additionalContext": reminder}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}
