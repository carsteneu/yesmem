package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/fsnotify/fsnotify"
)

// RunRulesRefresh checks all projects with rules and re-condenses if CLAUDE.md changed.
func RunRulesRefresh(store *storage.Store, client extraction.LLMClient) {
	if client == nil {
		return
	}
	projects, err := store.ListRulesProjects()
	if err != nil || len(projects) == 0 {
		return
	}
	for _, rp := range projects {
		if _, err := os.Stat(rp.Path); err != nil {
			continue
		}
		_, hash, err := CondenseRules(rp.Path, rp.Project, client, store)
		if err != nil {
			if err.Error() == "unchanged" {
				log.Printf("[rules-refresh] %s: up to date", rp.Project)
			} else {
				log.Printf("[rules-refresh] %s: %v", rp.Project, err)
			}
			continue
		}
		log.Printf("[rules-refresh] %s: re-condensed (hash=%s)", rp.Project, hash[:12])
	}
}

// startRulesWatch watches CLAUDE.md files for projects with rules and re-condenses on change.
func startRulesWatch(ctx context.Context, store *storage.Store, client extraction.LLMClient) {
	if client == nil {
		return
	}

	projects, err := store.ListRulesProjects()
	if err != nil || len(projects) == 0 {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[rules-watch] init: %v", err)
		return
	}

	type rulesProject struct {
		project    string
		sourcePath string
	}
	pathToProject := make(map[string]rulesProject)

	watched := 0
	for _, rp := range projects {
		if _, err := os.Stat(rp.Path); err != nil {
			continue
		}
		dir := filepath.Dir(rp.Path)
		if err := watcher.Add(dir); err != nil {
			log.Printf("[rules-watch] watch %s: %v", dir, err)
			continue
		}
		pathToProject[rp.Path] = rulesProject{rp.Project, rp.Path}
		watched++
	}

	if watched == 0 {
		watcher.Close()
		return
	}
	log.Printf("[rules-watch] watching %d CLAUDE.md files for rules changes", watched)

	go func() {
		defer watcher.Close()
		const debounce = 30 * time.Second
		timers := make(map[string]*time.Timer)
		var mu sync.Mutex

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				rp, ok := pathToProject[event.Name]
				if !ok {
					continue
				}
				mu.Lock()
				if t, exists := timers[rp.project]; exists {
					t.Reset(debounce)
				} else {
					timers[rp.project] = time.AfterFunc(debounce, func() {
						mu.Lock()
						delete(timers, rp.project)
						mu.Unlock()
						log.Printf("[rules-watch] CLAUDE.md changed: re-condensing %s", rp.project)
						_, hash, err := CondenseRules(rp.sourcePath, rp.project, client, store)
						if err != nil {
							if err.Error() == "unchanged" {
								log.Printf("[rules-watch] %s: content unchanged after edit", rp.project)
							} else {
								log.Printf("[rules-watch] %s: %v", rp.project, err)
							}
							return
						}
						log.Printf("[rules-watch] %s: re-condensed (hash=%s)", rp.project, hash[:12])
					})
				}
				mu.Unlock()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[rules-watch] error: %v", err)
			}
		}
	}()
}
