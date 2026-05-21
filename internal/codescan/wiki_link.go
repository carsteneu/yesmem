package codescan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func renderWikiLink(project string) string {
	wikiPath := fmt.Sprintf("~/.claude/yesmem/wiki/%s/", project)
	timestamp := "not yet rendered — will appear after first wiki-tick"
	if home, err := os.UserHomeDir(); err == nil {
		healthPath := filepath.Join(home, ".claude", "yesmem", "wiki", project, "health.md")
		if info, err := os.Stat(healthPath); err == nil {
			timestamp = fmt.Sprintf("last build: %s", info.ModTime().Format("15:04"))
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n**Full code map:** `%s` (%s)\n\n", wikiPath, timestamp))
	b.WriteString("> BEFORE editing any file, check its wiki page for per-file\n")
	b.WriteString("> learnings, gotchas, and co-edit context: `files/<path>.md`\n")
	b.WriteString("> (symbols, imports, related sessions). Path encoding: `/` → `_`.\n")
	b.WriteString("> Package overview: `packages.md` (file counts, gotchas, TODOs).\n")
	b.WriteString("> Full file tree: `index.md`.\n")
	b.WriteString("> Falls back to `search_code_index` / `get_code_snippet` / raw grep.\n\n")
	return b.String()
}
