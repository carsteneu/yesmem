package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func main() {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".claude", "yesmem", "yesmem.db")

	store, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Sessions
	sessions, _ := store.ListSessions("", 0)
	fmt.Printf("Sessions: %d total\n", len(sessions))

	// Which have learnings
	extracted := map[string]bool{}
	allLearnings, _ := store.GetActiveLearnings("", "", "", "")
	for _, l := range allLearnings {
		if l.SessionID != "" {
			extracted[l.SessionID] = true
		}
	}

	withLearnings := 0
	withoutLearnings := 0
	tooSmall := 0
	for _, s := range sessions {
		if s.MessageCount <= 5 {
			tooSmall++
		} else if extracted[s.ID] {
			withLearnings++
		} else {
			withoutLearnings++
		}
	}

	fmt.Printf("  With learnings:    %d\n", withLearnings)
	fmt.Printf("  Without learnings: %d (pending extraction)\n", withoutLearnings)
	fmt.Printf("  Too small (<6 msgs): %d\n", tooSmall)

	// Learnings by category
	fmt.Printf("\nActive learnings: %d\n", len(allLearnings))
	cats := map[string]int{}
	for _, l := range allLearnings {
		cats[l.Category]++
	}
	for cat, cnt := range cats {
		fmt.Printf("  %-20s %d\n", cat, cnt)
	}

	// Narratives
	narratives, _ := store.GetActiveLearnings("narrative", "", "", "")
	fmt.Printf("\nNarratives: %d\n", len(narratives))

	// One-time migrations
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--backfill":
			affected, bErr := store.BackfillProjects()
			if bErr != nil {
				fmt.Printf("Backfill error: %v\n", bErr)
			} else {
				fmt.Printf("\nBackfill: %d learnings tagged with project\n", affected)
			}
		case "--cleanup":
			ids, cErr := store.CleanupJunkLearnings()
			if cErr != nil {
				fmt.Printf("Cleanup error: %v\n", cErr)
			} else {
				fmt.Printf("\nCleanup: %d junk learnings removed\n", len(ids))
			}
		case "--supersede":
			// Usage: --supersede <newID> <reason> <oldID1> <oldID2> ...
			if len(os.Args) < 5 {
				fmt.Println("Usage: dbstats --supersede <newID> <reason> <oldID1> [oldID2...]")
				os.Exit(1)
			}
			var newID int64
			fmt.Sscanf(os.Args[2], "%d", &newID)
			reason := os.Args[3]
			var oldIDs []int64
			for _, arg := range os.Args[4:] {
				var id int64
				fmt.Sscanf(arg, "%d", &id)
				oldIDs = append(oldIDs, id)
			}
			if err := store.SupersedeByIDs(oldIDs, newID, reason); err != nil {
				fmt.Printf("Supersede error: %v\n", err)
			} else {
				fmt.Printf("Superseded %d learnings by #%d\n", len(oldIDs), newID)
			}
		case "--update-memory":
			daemon.GenerateAllMemoryMDs(store)
		case "--set-profile":
			// Usage: --set-profile <project> <profile-text>
			if len(os.Args) < 4 {
				fmt.Println("Usage: dbstats --set-profile <project> <profile-text>")
				os.Exit(1)
			}
			proj := os.Args[2]
			text := os.Args[3]
			now := time.Now()
			p := &models.ProjectProfile{
				Project:     proj,
				ProfileText: text,
				GeneratedAt: now,
				UpdatedAt:   now,
				SessionCount: 0,
				ModelUsed:   "manual",
			}
			if err := store.UpsertProjectProfile(p); err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Printf("Profile for %s updated (manual)\n", proj)
			}
		case "--profiles":
			// Generate/regenerate project profiles using LLMClient
			dataDir := filepath.Join(home, ".claude", "yesmem")
			cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
			apiKey := cfg.ResolvedAPIKey()
			if apiKey == "" {
				data, _ := os.ReadFile(filepath.Join(home, ".claude", "config.json"))
				var cc map[string]any
				if json.Unmarshal(data, &cc) == nil {
					if key, ok := cc["primaryApiKey"].(string); ok {
						apiKey = key
					}
				}
			}
			client, cErr := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
			if cErr != nil || client == nil {
				fmt.Printf("No LLM client available: %v\n", cErr)
				os.Exit(1)
			}
			fmt.Printf("Using %s backend (%s)\n", client.Name(), client.Model())
			projects, _ := store.ListProjects()
			for _, p := range projects {
				if p.SessionCount < 3 {
					continue
				}
				fmt.Printf("  Generating profile: %s (%d sessions)...", p.ProjectShort, p.SessionCount)
				if err := extraction.GenerateProjectProfile(client, store, p.ProjectShort); err != nil {
					fmt.Printf(" error: %v\n", err)
				} else {
					fmt.Printf(" done\n")
				}
			}
		}
	}

	// Re-read after potential backfill
	allLearnings, _ = store.GetActiveLearnings("", "", "", "")

	// Project distribution
	withProject := 0
	withoutProject := 0
	projectCounts := map[string]int{}
	for _, l := range allLearnings {
		if l.Project != "" {
			withProject++
			projectCounts[l.Project]++
		} else {
			withoutProject++
		}
	}
	fmt.Printf("\nLearnings by project:\n")
	fmt.Printf("  With project:    %d\n", withProject)
	fmt.Printf("  Without project: %d\n", withoutProject)
	for p, cnt := range projectCounts {
		if cnt >= 20 {
			fmt.Printf("  %-30s %d\n", p, cnt)
		}
	}
}
