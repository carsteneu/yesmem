package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/capfile"
)

// CapFileToParams flattens a parsed CAP.md into the param map expected by
// handleSaveCap. Scripts are passed as a JSON array string.
func CapFileToParams(cf capfile.CapFile) map[string]any {
	params := map[string]any{
		"name":        cf.Name,
		"description": cf.Description,
		"version":     float64(cf.Version),
	}

	if len(cf.Scripts) > 0 {
		scriptMetas := make([]ScriptMeta, len(cf.Scripts))
		for i, sc := range cf.Scripts {
			scriptMetas[i] = ScriptMeta{
				Name:    sc.Name,
				Kind:    sc.Kind,
				Runtime: sc.Runtime,
				Lang:    sc.Lang,
				Body:    sc.Body,
				Schema:  sc.Schema,
			}
		}
		scriptsJSON, _ := json.Marshal(scriptMetas)
		params["scripts"] = string(scriptsJSON)
	}

	if len(cf.Tags) > 0 {
		params["tags"] = strings.Join(cf.Tags, ",")
	}
	if len(cf.Requires) > 0 {
		params["requires"] = strings.Join(cf.Requires, ",")
	}
	if cf.Tested {
		params["tested"] = true
	}
	if cf.AutoActive {
		params["auto_active"] = true
	}
	if len(cf.Actions) > 0 {
		actionsJSON, _ := json.Marshal(cf.Actions)
		params["actions"] = string(actionsJSON)
	}
	return params
}

// CapMetaToCapFile converts stored cap metadata into the disk format. The
// project string indicates project-scoped caps when non-empty.
func CapMetaToCapFile(meta CapMeta, project string) capfile.CapFile {
	scope := "project"
	if project == "" {
		scope = "user"
	}

	scripts := make([]capfile.Script, len(meta.Scripts))
	for i, sm := range meta.Scripts {
		lang := sm.Lang
		if lang == "" {
			switch sm.Runtime {
			case "repl":
				lang = "javascript"
			case "bash":
				lang = "bash"
			}
		}
		scripts[i] = capfile.Script{
			Name:    sm.Name,
			Kind:    sm.Kind,
			Runtime: sm.Runtime,
			Lang:    lang,
			Body:    sm.Body,
			Schema:  sm.Schema,
		}
	}

	return capfile.CapFile{
		Name:        meta.Name,
		Description: meta.Description,
		Version:     meta.Version,
		Tags:        meta.Tags,
		Requires:    meta.Requires,
		Scope:       scope,
		Tested:      meta.Tested,
		AutoActive:  meta.AutoActive,
		Actions:     meta.Actions,
		Scripts:     scripts,
	}
}

// CapDDLLooker resolves the CREATE TABLE statements for a cap's data tables.
type CapDDLLooker interface {
	GetCapTableDDL(capName string) (string, error)
}

func WriteCapToDisk(meta CapMeta, project, baseDir string, ddl CapDDLLooker) error {
	cf := CapMetaToCapFile(meta, project)
	if cf.Version == 0 {
		cf.Version = 1
	}
	if cf.Purpose == "" {
		cf.Purpose = cf.Description
	}
	if ddl != nil {
		if sql, err := ddl.GetCapTableDDL(meta.Name); err == nil && sql != "" {
			cf.DatabaseSQL = sql
		}
	}
	dir := filepath.Join(baseDir, cf.Name)

	capPath := filepath.Join(dir, "CAP.md")
	if data, err := os.ReadFile(capPath); err == nil {
		if diskCF, parseErr := capfile.Parse(data); parseErr == nil && diskCF.Version >= cf.Version {
			log.Printf("WriteCapToDisk: skip %s (disk v%d >= db v%d)", cf.Name, diskCF.Version, cf.Version)
			return nil
		}
	}

	return capfile.WriteFile(&cf, dir)
}

func SyncCapsFromDisk(h *Handler, userDir, projectDir string) {
	caps, errs := capfile.ScanAll(userDir, projectDir)
	for _, err := range errs {
		log.Printf("[cap-sync] scan error: %v", err)
	}
	for _, cf := range caps {
		params := CapFileToParams(cf)
		if cf.Scope == "project" && projectDir != "" {
			params["project"] = filepath.Base(filepath.Dir(filepath.Dir(cf.SourcePath)))
		}
		resp := h.handleSaveCap(params)
		if resp.Error != "" {
			log.Printf("[cap-sync] save %s: %s", cf.Name, resp.Error)
		} else {
			log.Printf("[cap-sync] synced %s from %s", cf.Name, cf.SourcePath)
		}
	}
}

func ExportAllCaps(h *Handler, baseDir string) {
	learnings, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		log.Printf("[cap-export] query: %v", err)
		return
	}
	var exported int
	for _, l := range learnings {
		meta, err := ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if err := WriteCapToDisk(meta, l.Project, baseDir, h.store); err != nil {
			log.Printf("[cap-export] %s: %v", meta.Name, err)
		} else {
			exported++
		}
	}
	log.Printf("[cap-export] exported %d caps to %s", exported, baseDir)
}

type CapsDirWatcher struct {
	mtimes map[string]time.Time
}

func NewCapsDirWatcher() *CapsDirWatcher {
	return &CapsDirWatcher{mtimes: make(map[string]time.Time)}
}

func (w *CapsDirWatcher) ScanChanged(dir string) []capfile.CapFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var changed []capfile.CapFile
	seen := make(map[string]bool)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		capPath := filepath.Join(dir, entry.Name(), "CAP.md")
		info, err := os.Stat(capPath)
		if err != nil {
			continue
		}
		seen[capPath] = true
		mtime := info.ModTime()

		if prev, ok := w.mtimes[capPath]; ok && !mtime.After(prev) {
			continue
		}

		data, err := os.ReadFile(capPath)
		if err != nil {
			continue
		}
		cf, err := capfile.Parse(data)
		if err != nil {
			log.Printf("CapsDirWatcher: parse %s: %v", capPath, err)
			continue
		}
		cf.SourcePath = capPath
		w.mtimes[capPath] = mtime
		changed = append(changed, *cf)
	}

	for path := range w.mtimes {
		if !seen[path] {
			delete(w.mtimes, path)
		}
	}

	return changed
}

func (w *CapsDirWatcher) RefreshMtime(capPath string) {
	info, err := os.Stat(capPath)
	if err != nil {
		return
	}
	w.mtimes[capPath] = info.ModTime()
}
