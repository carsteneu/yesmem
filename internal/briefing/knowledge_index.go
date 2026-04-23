package briefing

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/codescan"
)

// HealthData holds counts for the health section of the briefing.
type HealthData struct {
	Contradictions int
	Unfinished     int
	Stale          int
}

// RecentFile represents a recently-touched file with recency metadata.
type RecentFile struct {
	Path         string
	LastSeen     time.Time
	SessionCount int
}

const maxRecentFiles = 10

// renderKnowledgeIndex assembles all Knowledge Index sections.
// Orchestrates data loading from store and renders each section.
// Code Map is built separately (see BuildCodeMap) and appended post-refine.
func (g *Generator) renderKnowledgeIndex(s Strings, projectDir, projectShort string, docSources []DocSourceSummary) string {
	var ki strings.Builder

	// Code Map — build it now (populates CodeGraph for MCP tools) but store separately.
	// It must bypass LLM refinement, so it's appended post-refine by the caller.
	if projectDir != "" {
		g.codeMapText = g.buildCodeMap(projectDir, projectShort)
	}

	// Doc Index (enriched from existing docSources)
	ki.WriteString(renderDocIndex(docSources, s))

	// Health
	health, err := g.store.GetBriefingHealth(projectShort)
	if err == nil {
		h := HealthData{
			Contradictions: health.Contradictions,
			Unfinished:     health.Unfinished,
			Stale:          health.Stale,
		}
		ki.WriteString(renderHealth(h, s))
	}

	// Recent Context
	recentFiles, err := g.store.GetRecentFiles(projectShort, maxRecentFiles)
	if err == nil {
		var files []RecentFile
		for _, f := range recentFiles {
			files = append(files, RecentFile{
				Path:         f.Path,
				LastSeen:     f.LastTouched,
				SessionCount: f.SessionCount,
			})
		}
		ki.WriteString(renderRecentContext(files, s))
	}

	// Append Knowledge Index sections to Code Map turn (not briefing)
	if extra := ki.String(); extra != "" {
		g.codeMapText += "\n" + extra
	}

	return ""
}

// buildCodeMap runs the directory scanner and renders the code map.
// Uses a cached scanner — re-scans only when git HEAD changes.
// Annotates packages with learning counts from the database.
// Builds CodeGraph for MCP tool access.
func (g *Generator) buildCodeMap(projectDir, projectShort string) string {
	scanner := g.getCodeScanner()
	result, err := scanner.Scan(projectDir)
	if err != nil {
		return ""
	}

	priority := g.buildPriority(projectShort, result)
	g.annotateLearnings(projectShort, result)
	g.annotateDescriptions(projectShort, result)

	// Build CodeGraph from scan result — available for MCP tools
	g.codeGraph = codescan.BuildCodeGraph(result)

	codeMap := codescan.RenderCodeMap(result, priority)

	// Code Health signals
	health := codescan.ScanHealth(projectDir)
	healthSection := codescan.RenderCodeHealth(health)

	return codeMap + healthSection
}

// getCodeScanner returns a cached scanner, creating one if needed.
// Uses CBMScanner (codebase-memory-mcp) with DirectoryScanner fallback.
func (g *Generator) getCodeScanner() *codescan.CachedScanner {
	if g.codeScanner == nil {
		if codescan.FindCBMBinary() != "" {
			g.codeScanner = codescan.NewCachedScanner(codescan.NewCBMScanner()).WithStore(g.store)
		} else {
			g.codeScanner = codescan.NewCachedScanner(&codescan.DirectoryScanner{}).WithStore(g.store)
		}
	}
	return g.codeScanner
}

// buildPriority creates a package priority map based on:
// 1. Learning density: packages with more learnings are more important
// 2. Recent file activity: recently-touched packages get a boost
func (g *Generator) buildPriority(projectShort string, result *codescan.ScanResult) map[string]int {
	priority := make(map[string]int)

	// Recent files boost
	recentFiles, err := g.store.GetRecentFiles(projectShort, 50)
	if err == nil {
		for _, f := range recentFiles {
			// Extract directory from file path
			dir := dirOf(f.Path)
			priority[dir] += f.SessionCount
		}
	}

	// Learning density boost via file coverage
	coverage, err := g.store.GetCoverageByProject(projectShort)
	if err == nil {
		for _, fc := range coverage {
			dir := dirOf(fc.FilePath)
			priority[dir] += fc.SessionCount
		}
	}

	return priority
}

// annotateLearnings enriches ScanResult packages with learning/gotcha counts.
// Matches entity values from learnings against package names and file basenames.
func (g *Generator) annotateLearnings(projectShort string, result *codescan.ScanResult) {
	counts, err := g.store.GetLearningCountsByEntity(projectShort)
	if err != nil || len(counts) == 0 {
		return
	}

	for i := range result.Packages {
		pkg := &result.Packages[i]
		var totalLearnings, totalGotchas int

		// Match against package name (e.g. entity "proxy" matches package "proxy")
		if ec, ok := counts[pkg.Name]; ok {
			totalLearnings += ec.Total
			totalGotchas += ec.Gotchas
		}

		// Match against file basenames in this package
		for _, f := range pkg.Files {
			base := filepath.Base(f.Path)
			if ec, ok := counts[base]; ok {
				totalLearnings += ec.Total
				totalGotchas += ec.Gotchas
			}
			// Also try relative path match
			if ec, ok := counts[f.Path]; ok {
				totalLearnings += ec.Total
				totalGotchas += ec.Gotchas
			}
		}

		pkg.LearningCount = totalLearnings
		pkg.GotchaCount = totalGotchas
	}
}

// annotateDescriptions populates PackageInfo.Description and AntiPatterns
// from the code_descriptions cache in SQLite.
func (g *Generator) annotateDescriptions(project string, result *codescan.ScanResult) {
	descs, err := g.store.GetCodeDescriptions(project)
	if err != nil || len(descs) == 0 {
		return
	}
	for i := range result.Packages {
		pkg := &result.Packages[i]
		if d, ok := descs[pkg.Name]; ok {
			pkg.Description = d.Description
			pkg.AntiPatterns = d.AntiPatterns
		}
	}
}

// dirOf extracts the directory part of a relative path.
func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

// renderDocIndex renders the documentation index section.
// Shows registered doc sources with type, trigger extensions, and search hint.
func renderDocIndex(sources []DocSourceSummary, s Strings) string {
	if len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### Indexed Documentation\n")
	b.WriteString("Use `docs_search()` to query these sources.\n\n")

	for _, src := range sources {
		b.WriteString(fmt.Sprintf("- **%s** (%s", src.Name, src.DocType))
		if src.TriggerExts != "" {
			b.WriteString(fmt.Sprintf(", auto-inject: %s", src.TriggerExts))
		}
		b.WriteString(fmt.Sprintf(", %d chunks)\n", src.ChunkCount))
	}
	return b.String()
}

// renderKnowledgeTopology renders knowledge depth per cluster area.
func renderKnowledgeTopology(strong, weak []ClusterSummary, s Strings) string {
	if len(strong) == 0 && len(weak) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### Knowledge Topology\n")

	if len(strong) > 0 {
		b.WriteString("**Strong coverage:**\n")
		for _, c := range strong {
			b.WriteString(fmt.Sprintf("- %s (%d learnings)\n", c.Label, c.Count))
		}
	}
	if len(weak) > 0 {
		b.WriteString("**Thin coverage:**\n")
		for _, c := range weak {
			b.WriteString(fmt.Sprintf("- %s (%d learnings)\n", c.Label, c.Count))
		}
	}
	return b.String()
}

// renderHealth renders the knowledge health section.
// Only shows non-zero items. Returns empty string if everything is clean.
func renderHealth(h HealthData, s Strings) string {
	if h.Contradictions == 0 && h.Unfinished == 0 && h.Stale == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### Health\n")

	if h.Contradictions > 0 {
		b.WriteString(fmt.Sprintf("- %d contradicting learnings — resolve with `get_learnings()`\n", h.Contradictions))
	}
	if h.Unfinished > 0 {
		b.WriteString(fmt.Sprintf("- %d open tasks — check with `get_learnings(task_type='task')`\n", h.Unfinished))
	}
	if h.Stale > 0 {
		b.WriteString(fmt.Sprintf("- %d stale learnings (>90 days, never cited)\n", h.Stale))
	}
	return b.String()
}

// renderRecentContext renders recently-touched files for spatial awareness.
func renderRecentContext(files []RecentFile, s Strings) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### Recent Context\n")

	limit := len(files)
	if limit > maxRecentFiles {
		limit = maxRecentFiles
	}
	for _, f := range files[:limit] {
		ago := formatTimeAgo(f.LastSeen)
		b.WriteString(fmt.Sprintf("- `%s` — %s, %d sessions\n", f.Path, ago, f.SessionCount))
	}
	return b.String()
}

// formatTimeAgo returns a human-readable relative time string.
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
