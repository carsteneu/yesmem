package daemon

import (
	"log"
	"os"
	"strings"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

// GenerateCodeDescriptions runs Phase 4.75: LLM-generated package descriptions.
// Processes at most 3 projects per extraction cycle, prioritized by last activity.
// ListProjects already returns projects sorted by last_active DESC.
func GenerateCodeDescriptions(store *storage.Store, cfg *config.Config, client extraction.LLMClient) {
	const maxProjectsPerRun = 1

	if client == nil {
		log.Printf("  code-describe: skipped (no LLM client)")
		return
	}

	projects, err := store.ListProjects()
	if err != nil {
		log.Printf("  code-describe: skipped (list projects: %v)", err)
		return
	}

	// CBMScanner (codebase-memory-mcp CLI) → RegexScanner fallback
	var scanner codescan.Scanner
	if codescan.FindCBMBinary() != "" {
		scanner = codescan.NewCBMScanner()
	} else {
		scanner = &codescan.DirectoryScanner{}
	}
	scanner = codescan.NewCachedScanner(scanner).WithStore(store)
	generated := 0

	for _, p := range projects {
		if generated >= maxProjectsPerRun {
			log.Printf("  code-describe: pausing after %d projects (limit %d per run)", generated, maxProjectsPerRun)
			break
		}

		projectDir := p.Project
		if _, err := os.Stat(projectDir); err != nil {
			continue
		}

		gitHead := codescan.ReadGitHead(projectDir)
		if gitHead == "" {
			continue
		}

		counts, _ := store.GetLearningCountsByEntity(p.ProjectShort)
		totalLearnings := 0
		for _, ec := range counts {
			totalLearnings += ec.Total
		}

		if !store.IsCodeDescriptionStale(p.ProjectShort, gitHead, totalLearnings) {
			continue
		}

		log.Printf("  code-describe %s: generating (HEAD=%s, learnings=%d)", p.ProjectShort, gitHead[:min(8, len(gitHead))], totalLearnings)

		result, scanErr := scanner.Scan(projectDir)
		if scanErr != nil {
			log.Printf("  code-describe %s: scan error: %v", p.ProjectShort, scanErr)
			continue
		}
		log.Printf("  code-describe %s: scan complete (%d files, %d packages)", p.ProjectShort, len(result.Files), len(result.Packages))

		descs, err := extraction.GeneratePackageDescriptions(client, result, counts)
		if err != nil {
			log.Printf("  code-describe %s: generation error: %v", p.ProjectShort, err)
			continue
		}

		for pkgName, desc := range descs {
			antiPatterns := strings.Join(desc.AntiPatterns, "\n")
			if err := store.UpsertCodeDescription(p.ProjectShort, pkgName, desc.Description, antiPatterns, gitHead, totalLearnings); err != nil {
				log.Printf("  code-describe %s/%s: store error: %v", p.ProjectShort, pkgName, err)
			}
		}

		generated++
		log.Printf("  code-describe %s: done (%d packages)", p.ProjectShort, len(descs))
	}

	if generated == 0 {
		log.Printf("  code-describe: all projects up to date")
	}
}
