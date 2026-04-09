package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
)

// Status prints the current index status.
func Status(dataDir string) error {
	dbPath := filepath.Join(dataDir, "yesmem.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("YesMem is not initialized. Run: yesmem install")
		return nil
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer store.Close()

	fmt.Println("YesMem Status")
	fmt.Println("=============")

	home, _ := os.UserHomeDir()
	codexState := readCodexConfigState(home)
	sessionSources := sessionSourceCounts(store)

	// Projects
	projects, err := store.ListProjects()
	if err == nil {
		fmt.Printf("\nProjects: %d\n", len(projects))
		totalSessions := 0
		for _, p := range projects {
			totalSessions += p.SessionCount
			fmt.Printf("  %-30s %4d sessions (last: %s)\n", p.ProjectShort, p.SessionCount, p.LastActive[:10])
		}
		fmt.Printf("\nTotal sessions: %d\n", totalSessions)
		for _, line := range formatSessionSourceCounts(sessionSources) {
			fmt.Println(line)
		}
	}

	fmt.Println("\nCodex:")
	fmt.Printf("  config.toml:     %s\n", statusText(codexState.ConfigPresent, "present", "missing"))
	fmt.Printf("  provider:        %s\n", statusText(codexState.ProviderConfigured, "configured", "missing"))
	fmt.Printf("  MCP server:      %s\n", statusText(codexState.MCPConfigured, "configured", "missing"))
	fmt.Printf("  MCP auto-approve:%s\n", statusText(codexState.ApprovalConfigured, "configured", "missing"))
	fmt.Printf("  instructions:    %s\n", statusText(codexState.InstructionsReferenced && codexState.InstructionsPresent, "configured", "missing"))
	fmt.Printf("  proxy /health:   %s\n", statusText(codexProxyReachable(), "reachable", "unreachable"))

	// Learnings
	learnings, err := store.GetActiveLearnings("", "", "", "")
	if err == nil {
		cats := map[string]int{}
		for _, l := range learnings {
			cats[l.Category]++
		}
		fmt.Printf("\nActive learnings: %d\n", len(learnings))
		for cat, count := range cats {
			fmt.Printf("  %-20s %d\n", cat, count)
		}
	}

	// Superseded/resolved learnings
	var supersededCount, resolvedCount, totalLearnings int
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings`).Scan(&totalLearnings)
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by > 0`).Scan(&supersededCount)
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by < 0`).Scan(&resolvedCount)
	if supersededCount > 0 || resolvedCount > 0 {
		fmt.Printf("\nSuperseded: %d, Resolved: %d, Total: %d\n", supersededCount, resolvedCount, totalLearnings)
	}

	// Embedding / VectorStore
	vectorDir := filepath.Join(dataDir, "vectors")
	vectorFiles := countFiles(vectorDir)
	// Also count vectors stored in SQLite (new lightweight embed mode)
	var sqliteVectors int
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE embedding_vector IS NOT NULL`).Scan(&sqliteVectors)
	if sqliteVectors > vectorFiles {
		vectorFiles = sqliteVectors
	}
	embeddable := 0
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings`).Scan(&embeddable)

	// Also check embedding_status for a more accurate count
	var embeddedDone int
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE embedding_status = 'done'`).Scan(&embeddedDone)
	if embeddedDone > vectorFiles {
		vectorFiles = embeddedDone
	}
	if embeddable > 0 {
		pct := float64(vectorFiles) / float64(embeddable) * 100
		status := "✓ complete"
		if vectorFiles < embeddable {
			status = fmt.Sprintf("⏳ backfill running (%d remaining)", embeddable-vectorFiles)
		}
		fmt.Printf("\nEmbeddings: %d / %d (%.0f%%) — %s\n", vectorFiles, embeddable, pct, status)
	}

	// Pipeline status
	fmt.Println("\nPipeline:")
	var sessTotal, sessExtracted, sessNarrative, sessShort int
	store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessTotal)
	store.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE message_count <= 5`).Scan(&sessShort)
	store.DB().QueryRow(`SELECT COUNT(DISTINCT session_id) FROM learnings WHERE session_id IS NOT NULL AND session_id != ''`).Scan(&sessExtracted)
	store.DB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE category = 'narrative'`).Scan(&sessNarrative)
	sessEligible := sessTotal - sessShort
	fmt.Printf("  Extraction:      %d / %d eligible (%d total, %d short)\n", sessExtracted, sessEligible, sessTotal, sessShort)
	fmt.Printf("  Narratives:      %d\n", sessNarrative)

	var gapsOpen, gapsResolved int
	store.DB().QueryRow(`SELECT COUNT(*) FROM knowledge_gaps WHERE resolved_at IS NULL`).Scan(&gapsOpen)
	store.DB().QueryRow(`SELECT COUNT(*) FROM knowledge_gaps WHERE resolved_at IS NOT NULL`).Scan(&gapsResolved)
	fmt.Printf("  Knowledge gaps:  %d open, %d resolved\n", gapsOpen, gapsResolved)

	var clusters int
	store.DB().QueryRow(`SELECT COUNT(*) FROM learning_clusters`).Scan(&clusters)
	fmt.Printf("  Clusters:        %d\n", clusters)

	var traits int
	store.DB().QueryRow(`SELECT COUNT(*) FROM persona_traits`).Scan(&traits)
	fmt.Printf("  Persona traits:  %d\n", traits)

	var profiles, briefings int
	store.DB().QueryRow(`SELECT COUNT(*) FROM project_profiles`).Scan(&profiles)
	store.DB().QueryRow(`SELECT COUNT(*) FROM refined_briefings`).Scan(&briefings)
	fmt.Printf("  Profiles:        %d\n", profiles)
	fmt.Printf("  Briefings:       %d\n", briefings)

	var docSources, docChunks int
	store.DB().QueryRow(`SELECT COUNT(*) FROM doc_sources`).Scan(&docSources)
	store.DB().QueryRow(`SELECT COUNT(*) FROM doc_chunks`).Scan(&docChunks)
	if docSources > 0 {
		fmt.Printf("  Docs:            %d sources, %d chunks\n", docSources, docChunks)
	}

	// Disk usage
	fmt.Println("\nDisk usage:")
	printSize("  Database", dbPath)
	printSize("  Bleve index", filepath.Join(dataDir, "bleve-index"))
	printSize("  Vectors", filepath.Join(dataDir, "vectors"))
	printSize("  Archive", filepath.Join(dataDir, "archive"))

	return nil
}

func printSize(label, path string) {
	size := dirSize(path)
	if size < 0 {
		info, err := os.Stat(path)
		if err == nil {
			size = info.Size()
		}
	}
	if size >= 0 {
		if size > 1024*1024*1024 {
			fmt.Printf("%-20s %6.1f GB\n", label, float64(size)/1024/1024/1024)
		} else if size > 1024*1024 {
			fmt.Printf("%-20s %6.1f MB\n", label, float64(size)/1024/1024)
		} else {
			fmt.Printf("%-20s %6.1f KB\n", label, float64(size)/1024)
		}
	}
}

func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Try subdirectories (vectors have a collection subdir)
		subs, _ := os.ReadDir(dir)
		total := 0
		for _, sub := range subs {
			if sub.IsDir() {
				subEntries, _ := os.ReadDir(filepath.Join(dir, sub.Name()))
				total += len(subEntries)
			}
		}
		return total
	}
	// Count files, recurse into subdirs
	total := 0
	for _, e := range entries {
		if e.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(dir, e.Name()))
			total += len(subEntries)
		} else {
			total++
		}
	}
	return total
}

func dirSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	if size == 0 {
		return -1
	}
	return size
}

func statusText(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func sessionSourceCounts(store *storage.Store) map[string]int {
	rows, err := store.DB().Query(`SELECT COALESCE(source_agent, 'claude'), COUNT(*) FROM sessions GROUP BY COALESCE(source_agent, 'claude')`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var source string
		var count int
		if rows.Scan(&source, &count) == nil {
			counts[source] = count
		}
	}
	return counts
}

func formatSessionSourceCounts(counts map[string]int) []string {
	if len(counts) == 0 {
		return nil
	}
	names := make([]string, 0, len(counts))
	maxLabel := 0
	for source := range counts {
		label := displaySourceAgent(source)
		names = append(names, source)
		if len(label) > maxLabel {
			maxLabel = len(label)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left := displaySourceAgent(names[i])
		right := displaySourceAgent(names[j])
		if left == right {
			return names[i] < names[j]
		}
		return left < right
	})

	lines := make([]string, 0, len(names))
	for _, source := range names {
		label := displaySourceAgent(source)
		lines = append(lines, fmt.Sprintf("  %-*s %d", maxLabel+1, label+":", counts[source]))
	}
	return lines
}

func displaySourceAgent(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "claude"
	}
	return strings.Title(source)
}
