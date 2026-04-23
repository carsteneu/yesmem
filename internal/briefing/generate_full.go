package briefing

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

// BriefingResult holds the complete briefing output.
type BriefingResult struct {
	Text    string // Full briefing (persona, learnings, pins, open work)
	CodeMap string // Code Map (separate, for independent injection)
}

// GenerateFullBriefing produces the complete briefing text with all config,
// refinement, recovery, pins, and open work. Single source of truth for both
// the briefing-hook CLI and the daemon RPC.
func GenerateFullBriefing(store *storage.Store, dataDir, project, sessionID string) BriefingResult {
	cfg, err := config.Load(filepath.Join(dataDir, "config.yaml"))
	if err != nil {
		cfg = &config.Config{}
	}

	if len(cfg.Briefing.Languages) > 0 {
		SetLanguages(cfg.Briefing.Languages)
	}
	SetStringsPath(filepath.Join(dataDir, "strings.yaml"))

	// Detect agent session — suppress unfinished todos and reminder nudge.
	isAgentSession := false
	if sessionID != "" {
		if agent, _ := store.AgentGetAnyBySession(sessionID); agent != nil {
			isAgentSession = true
		}
	}

	gen := New(store, cfg.Briefing.DetailedSessions)
	gen.SetMaxPerCategory(cfg.Briefing.MaxPerCategory)
	gen.SetDedupThreshold(cfg.Briefing.DedupThreshold)
	gen.SetUnfinishedTTL(cfg.Evolution.UnfinishedTTL)
	gen.SetUserProfile(cfg.Briefing.UserProfile)
	if isAgentSession {
		gen.SetSkipUnfinished(true)
	}
	gen.SetStrings(ResolveStrings(filepath.Join(dataDir, "strings.yaml")))

	// Recovery: auto-detect clear/compact via recent session_tracking entry (30s window).
	if sid, reason, err := store.GetRecentEndedSession(project, 30*time.Second); err == nil && sid != "" {
		gen.SetRecovery(sid, reason)
	}

	// Use full CWD path for scanner (Code Map needs it), fall back to short name.
	text := gen.Generate(project)
	codeMap := gen.CodeMap()

	// LLM refinement (uses cached version if available)
	projectShort := filepath.Base(project)
	text = RefineBriefing(text, store, projectShort, nil)

	// Recovery block (post-refine so it survives refinement)
	if recovery := gen.GenerateRecovery(); recovery != "" {
		text = recovery + "\n" + text
	}

	// Inject pinned learnings between prose and tools block
	sessionPins, _ := store.GetPinnedLearnings("session", projectShort)
	permanentPins, _ := store.GetPinnedLearnings("permanent", projectShort)
	pinnedBlock := FormatPinnedBlock(sessionPins, permanentPins)
	if pinnedBlock != "" {
		text = InjectPinnedBlock(text, pinnedBlock)
	}

	// Open work reminder (skip for agents, respect config)
	if cfg.Briefing.RemindOpenWork && !isAgentSession {
		if count, _ := store.CountActiveUnfinished(projectShort); count > 0 {
			s := ResolveStrings(filepath.Join(dataDir, "strings.yaml"))
			if s.OpenWorkRemind != "" {
				text += "\n\n" + fmt.Sprintf(s.OpenWorkRemind, projectShort) + "\n"
			}
		}
	}

	return BriefingResult{Text: text, CodeMap: codeMap}
}

// InjectPinnedBlock inserts the pinned block between prose and the tools block.
func InjectPinnedBlock(text, pinnedBlock string) string {
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
