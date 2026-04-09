package daemon

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/storage"
)

const yesmemMarker = "# --- YesMem Auto-Generated (do not edit below) ---"

// GenerateAllMemoryMDs creates/updates MEMORY.md for every project.
// Writes only a narrative redirect to yesmem MCP tools — no learning content.
func GenerateAllMemoryMDs(store *storage.Store) {
	projects, err := store.ListProjects()
	if err != nil {
		log.Printf("warn: list projects for MEMORY.md: %v", err)
		return
	}

	home, _ := os.UserHomeDir()
	claudeProjects := filepath.Join(home, ".claude", "projects")

	s := briefing.ResolveStrings(briefing.DefaultStringsPath())
	narrative := s.MemoryMDNarrative

	for _, p := range projects {
		projDir := findProjectDir(claudeProjects, p.Project)
		if projDir == "" {
			continue
		}

		memDir := filepath.Join(projDir, "memory")
		memFile := filepath.Join(memDir, "MEMORY.md")

		if err := writeMemoryMD(memDir, memFile, "\n"+narrative+"\n"); err != nil {
			log.Printf("warn: MEMORY.md for %s: %v", p.ProjectShort, err)
		}
	}

	log.Printf("MEMORY.md updated for %d projects", len(projects))
}

func writeMemoryMD(memDir, memFile, yesmemContent string) error {
	os.MkdirAll(memDir, 0755)

	// Read existing file
	existing, err := os.ReadFile(memFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var result string

	if len(existing) > 0 {
		content := string(existing)
		// Find marker
		idx := strings.Index(content, yesmemMarker)
		if idx >= 0 {
			// Replace everything from marker onwards
			result = content[:idx] + yesmemMarker + "\n" + yesmemContent
		} else {
			// Append marker + content
			result = content + "\n\n" + yesmemMarker + "\n" + yesmemContent
		}
	} else {
		// New file
		result = yesmemMarker + "\n" + yesmemContent
	}

	return os.WriteFile(memFile, []byte(result), 0644)
}

// findProjectDir maps a project path to its .claude/projects/ directory.
func findProjectDir(claudeProjects, projectPath string) string {
	// Convert /var/www/html/ccm19 → -var-www-html-ccm19
	dirName := strings.ReplaceAll(projectPath, "/", "-")
	if !strings.HasPrefix(dirName, "-") {
		dirName = "-" + dirName
	}

	full := filepath.Join(claudeProjects, dirName)
	if _, err := os.Stat(full); err == nil {
		return full
	}

	// Try to find by scanning
	entries, err := os.ReadDir(claudeProjects)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), filepath.Base(projectPath)) {
			return filepath.Join(claudeProjects, e.Name())
		}
	}
	return ""
}
