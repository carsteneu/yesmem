package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors a directory for new/changed JSONL files.
type Watcher struct {
	dir       string
	onChange  func(path string) // fires after 3s quiet (index only)
	onSettled func(path string) // fires after 5min quiet (LLM extraction)
	stop      chan struct{}
	wg        sync.WaitGroup
}

// NewWatcher creates a file watcher for a Claude Code projects directory.
// onChange fires quickly (3s debounce) for indexing.
// onSettled fires after 5min quiet for LLM extraction (session likely done).
func NewWatcher(dir string, onChange, onSettled func(path string)) *Watcher {
	return &Watcher{
		dir:       dir,
		onChange:  onChange,
		onSettled: onSettled,
		stop:      make(chan struct{}),
	}
}

// Start begins watching for file changes.
func (w *Watcher) Start() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := w.addRecursiveWatches(watcher, w.dir); err != nil {
		watcher.Close()
		return err
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer watcher.Close()
		w.run(watcher)
	}()

	return nil
}

// Stop stops the watcher.
func (w *Watcher) Stop() {
	close(w.stop)
	w.wg.Wait()
}

func (w *Watcher) run(watcher *fsnotify.Watcher) {
	// Debounce: track last change time per file.
	// Indexing triggers after 3s quiet (fast, no LLM).
	// LLM extraction triggers after 5min quiet (session likely done).
	pending := map[string]time.Time{}
	indexed := map[string]time.Time{} // tracks when we last indexed a file
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	const indexDebounce = 3 * time.Second
	const extractDebounce = 5 * time.Minute

	for {
		select {
		case <-w.stop:
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only care about JSONL files
			if !strings.HasSuffix(event.Name, ".jsonl") {
				// If a new directory was created, watch it (and subagent dirs)
				if event.Has(fsnotify.Create) {
					info, err := os.Stat(event.Name)
					if err == nil && info.IsDir() {
						if err := w.addRecursiveWatches(watcher, event.Name); err == nil {
							log.Printf("watching new dir: %s", event.Name)
						}
					}
				}
				continue
			}

			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				pending[event.Name] = time.Now()
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)

		case <-ticker.C:
			now := time.Now()
			for path, lastChange := range pending {
				quiet := now.Sub(lastChange)

				// Quick index after 3s (no LLM, makes search work immediately)
				if quiet > indexDebounce {
					if lastIdx, ok := indexed[path]; !ok || lastChange.After(lastIdx) {
						indexed[path] = now
						w.onChange(path) // this only indexes, extraction is separate
					}
				}

				// LLM extraction after 5min quiet (session is likely done)
				if quiet > extractDebounce {
					delete(pending, path)
					if w.onSettled != nil {
						w.onSettled(path)
					}
				}
			}
		}
	}
}

func (w *Watcher) addRecursiveWatches(watcher *fsnotify.Watcher, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}

	cleanRoot := filepath.Clean(root)
	isCodexTree := strings.Contains(cleanRoot, string(os.PathSeparator)+".codex"+string(os.PathSeparator)+"sessions") ||
		strings.HasSuffix(cleanRoot, string(os.PathSeparator)+".codex"+string(os.PathSeparator)+"sessions")
	cutoff := time.Now().AddDate(0, 0, -7)

	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == ".git" || base == "archive" {
			return filepath.SkipDir
		}
		if isCodexTree && path != root {
			info, err := d.Info()
			if err == nil && info.ModTime().Before(cutoff) {
				return filepath.SkipDir
			}
		}
		if err := watcher.Add(path); err != nil && !strings.Contains(err.Error(), "already exists") {
			log.Printf("warn: watch dir %s: %v", path, err)
		}
		return nil
	})
}
