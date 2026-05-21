package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/wikirender"
)

const wikiTickInterval = 5 * time.Minute

// wikiSnapshot tracks what the last wiki render saw, so we skip re-renders when nothing changed.
type wikiSnapshot struct {
	LearningCount int    `json:"learnings_count"`
	MaxUpdated    string `json:"max_updated"` // max(updated_at) of active learnings
	MaxSession    string `json:"max_session"` // max(at) of sessions
}

func startWikiTicker(ctx context.Context, store *storage.Store) {
	scanner := codescan.NewCachedScanner(codescan.NewCBMScanner()).WithStore(store)
	go func() {
		ticker := time.NewTicker(wikiTickInterval)
		defer ticker.Stop()
		home, _ := os.UserHomeDir()
		outRoot := filepath.Join(home, ".claude", "yesmem", "wiki")
		runWikiOnce(ctx, store, outRoot, scanner)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runWikiOnce(ctx, store, outRoot, scanner)
			}
		}
	}()
}

func runWikiOnce(ctx context.Context, store *storage.Store, outRoot string, scanner *codescan.CachedScanner) {
	projects, err := activeProjects(ctx, store)
	if err != nil {
		log.Printf("wiki-tick: list projects: %v", err)
		return
	}

	for _, p := range projects {
		dir := store.ResolveProjectPath(p)
		out := filepath.Join(outRoot, p)

		// Skip when nothing changed since last render.
		if !wikiNeedsRender(ctx, store, p, out) {
			continue
		}

		// Only scan code when we're actually going to render and it's a git repo.
		var graph *codescan.CodeGraph
		var graphMs int64
		if dir != "" && isLikelyGitRepo(dir) {
			gStart := time.Now()
			if sr, err := scanner.Scan(dir); err == nil {
				graph = scanner.GetCachedGraph(dir)
				if graph == nil {
					graph = codescan.BuildCodeGraph(sr)
				}
			}
			graphMs = time.Since(gStart).Milliseconds()
		}

		r, err := wikirender.Render(ctx, wikirender.RenderConfig{
			Project:   p,
			OutputDir: out,
			Store:     store,
			CodeGraph: graph,
			Quiet:     true,
		})
		if err != nil {
			log.Printf("wiki-tick: render %s: %v", p, err)
			continue
		}
		log.Printf("wiki-tick: %s learnings=%d topics=%d files=%d sessions=%d load=%dms compute=%dms write=%dms tpl=%dms graph=%dms total=%dms skipped=%d",
			r.Project, r.Learnings, r.Topics, r.Files, r.Sessions,
			r.LoadMs, r.ComputeMs, r.WriteMs, r.TplMs, graphMs, r.DurationMs, r.SkippedWrites)

		// Persist snapshot so we can skip next tick if nothing changed.
		writeWikiSnapshot(ctx, store, p, out)
	}
}

// wikiNeedsRender returns false if the wiki is up-to-date (learnings unchanged since last render).
func wikiNeedsRender(ctx context.Context, store *storage.Store, project, outDir string) bool {
	wikiIndex := filepath.Join(outDir, "index.md")
	if _, err := os.Stat(wikiIndex); os.IsNotExist(err) {
		return true // never rendered
	}

	snap := loadWikiSnapshot(outDir)
	if snap == nil {
		return true // no snapshot = render
	}

	// Check if learnings changed
	var lc int
	var maxUpd string
	if err := store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(created_at), '') FROM learnings WHERE project=? AND superseded_by IS NULL`, project,
	).Scan(&lc, &maxUpd); err != nil {
		return true // can't determine, render to be safe
	}

	if lc != snap.LearningCount || maxUpd != snap.MaxUpdated {
		return true
	}

	// Check if sessions changed
	var maxSess string
	if err := store.DB().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(started_at), '') FROM sessions WHERE project_short=?`, project,
	).Scan(&maxSess); err != nil {
		return true
	}
	if maxSess != snap.MaxSession {
		return true
	}

	return false // nothing changed
}

func loadWikiSnapshot(outDir string) *wikiSnapshot {
	data, err := os.ReadFile(filepath.Join(outDir, ".wiki-snapshot.json"))
	if err != nil {
		return nil
	}
	var snap wikiSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	return &snap
}

func writeWikiSnapshot(ctx context.Context, store *storage.Store, project, outDir string) {
	snap := wikiSnapshot{}
	store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(created_at), '') FROM learnings WHERE project=? AND superseded_by IS NULL`, project,
	).Scan(&snap.LearningCount, &snap.MaxUpdated)
	store.DB().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(started_at), '') FROM sessions WHERE project_short=?`, project,
	).Scan(&snap.MaxSession)

	data, _ := json.Marshal(snap)
	os.WriteFile(filepath.Join(outDir, ".wiki-snapshot.json"), data, 0644)
}

func activeProjects(ctx context.Context, store *storage.Store) ([]string, error) {
	rows, err := store.DB().QueryContext(ctx, `
		SELECT DISTINCT project FROM learnings
		WHERE project IS NOT NULL AND project != '' AND superseded_by IS NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

