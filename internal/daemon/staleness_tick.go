package daemon

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// startStalenessTick runs a background ticker that discovers git repos under
// projectsDir and evaluates whether new commits stale any learnings via the
// existing evaluateStaleness LLM pipeline.
func startStalenessTick(ctx context.Context, handler *Handler, projectsDir string) {
	if handler.CommitEvalClient == nil {
		log.Printf("[staleness-tick] no commit eval client, skipping")
		return
	}

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Discover git repos from projectsDir
		entries, err := os.ReadDir(projectsDir)
		if err != nil {
			log.Printf("[staleness-tick] read projects dir: %v", err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			project := entry.Name()
			workdir := filepath.Join(projectsDir, project)

			// Only process directories that are git repos
			gitDir := filepath.Join(workdir, ".git")
			if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
				continue
			}

			lastChecked, err := handler.store.GetProxyState("staleness_last_checked:" + project)
			if err != nil {
				log.Printf("[staleness-tick] %s: get last checked: %v", project, err)
				continue
			}

			hashes, err := latestCommitsSince(workdir, lastChecked)
			if err != nil {
				log.Printf("[staleness-tick] %s: git log: %v", project, err)
				continue
			}

			var newestHash string
			for _, hash := range hashes {
				resp := handler.handleInvalidateOnCommit(map[string]any{
					"hash":    hash,
					"project": project,
					"workdir": workdir,
				})
				if resp.Error != "" {
					log.Printf("[staleness-tick] %s: evaluate %s: %s", project, hash[:min(7, len(hash))], resp.Error)
					continue
				}
				newestHash = hash
			}

			if newestHash != "" {
				if err := handler.store.SetProxyState("staleness_last_checked:"+project, newestHash); err != nil {
					log.Printf("[staleness-tick] %s: update last checked: %v", project, err)
				}
			}
		}
	}
}

// latestCommitsSince returns all commit hashes since `since` in chronological
// order (oldest first). Returns all hashes if `since` is empty.
func latestCommitsSince(workdir, since string) ([]string, error) {
	rangeSpec := "HEAD"
	if since != "" {
		rangeSpec = since + "..HEAD"
	}
	args := []string{"log", "--reverse", "--format=%H", rangeSpec}
	if workdir != "" {
		args = append([]string{"-C", workdir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}
