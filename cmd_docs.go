package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/ingest"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runAddDocs(args []string) {
	fs := flag.NewFlagSet("add-docs", flag.ExitOnError)
	name := fs.String("name", "", "Documentation source name (required)")
	version := fs.String("version", "", "Version string")
	path := fs.String("path", "", "Directory path to index")
	file := fs.String("file", "", "Single file to index")
	url := fs.String("url", "", "Git URL to clone (e.g. https://github.com/user/repo)")
	project := fs.String("project", "", "Project name")
	domain := fs.String("domain", "code", "Domain (code, marketing, legal, finance, general)")
	dryRun := fs.Bool("dry-run", false, "Show what would happen without writing")
	destill := fs.Bool("destill", false, "Run LLM destillation to create low-trust learnings")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --name is required")
		os.Exit(1)
	}

	var paths []string
	if *url != "" {
		// Clone git repo to persistent docs directory
		clonePath := cloneOrPullRepo(*name, *url)
		if clonePath == "" {
			log.Fatalf("Failed to clone %s", *url)
		}
		paths = append(paths, clonePath)
	}
	if *path != "" {
		paths = append(paths, *path)
	}
	if *file != "" {
		paths = append(paths, *file)
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: --path, --file, or --url is required")
		os.Exit(1)
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cfg := ingest.Config{
		Name:    *name,
		Version: *version,
		Project: *project,
		Domain:  *domain,
		DryRun:  *dryRun,
		Destill: *destill,
	}

	// Create LLM client for destillation if requested
	if cfg.Destill && !*dryRun {
		appCfg, cfgErr := config.Load(filepath.Join(dataDir, "config.yaml"))
		if cfgErr != nil {
			log.Fatalf("config (needed for --destill): %v", cfgErr)
		}
		model := appCfg.SummarizeModelID()
		client, clientErr := extraction.NewLLMClient(appCfg.LLM.Provider, appCfg.ResolvedAPIKey(), model, appCfg.LLM.ClaudeBinary, appCfg.ResolvedOpenAIBaseURL())
		if clientErr != nil {
			log.Fatalf("llm client for destillation: %v", clientErr)
		}
		if client == nil {
			log.Fatalf("no LLM client available for --destill: set ANTHROPIC_API_KEY/OPENAI_API_KEY or configure claude_binary")
		}
		cfg.LLMClient = client
	}

	result, err := ingest.Run(cfg, paths, store)
	if err != nil {
		log.Fatalf("ingest failed: %v", err)
	}

	fmt.Printf("\nDone.\n")
	fmt.Printf("  Files processed: %d\n", result.FilesProcessed)
	fmt.Printf("  Files skipped:   %d (unchanged)\n", result.FilesSkipped)
	fmt.Printf("  Chunks created:  %d\n", result.ChunksCreated)
	if result.LearningsSuperseded > 0 {
		fmt.Printf("  Learnings superseded: %d\n", result.LearningsSuperseded)
	}
	if result.LearningsCreated > 0 {
		fmt.Printf("  Learnings created:    %d (from destillation)\n", result.LearningsCreated)
	}
}

func runSyncDocs(args []string) {
	fs := flag.NewFlagSet("sync-docs", flag.ExitOnError)
	name := fs.String("name", "", "Sync specific source")
	project := fs.String("project", "", "Sync all sources for project")
	all := fs.Bool("all", false, "Sync all registered sources")
	fs.Parse(args)

	if *name == "" && *project == "" && !*all {
		fmt.Fprintln(os.Stderr, "Error: --name, --project, or --all is required")
		os.Exit(1)
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	// Get sources to sync
	filterProject := ""
	if !*all {
		filterProject = *project
	}
	sources, err := store.ListDocSources(filterProject)
	if err != nil {
		log.Fatalf("list sources: %v", err)
	}

	for _, src := range sources {
		if *name != "" && src.Name != *name {
			continue
		}
		if src.Path == "" {
			log.Printf("Skipping %s (no path)", src.Name)
			continue
		}

		// Git-aware: if source path is inside a git repo, pull first
		gitUpdated := tryGitPull(src.Path)
		if gitUpdated {
			log.Printf("  Git pull: updates found")
		}

		log.Printf("Syncing %s (%s)...", src.Name, src.Path)
		cfg := ingest.Config{
			Name:    src.Name,
			Version: src.Version,
			Project: src.Project,
			Destill: false, // no re-destillation on sync
		}

		// Wire LLM client for re-destillation if needed
		if cfg.Destill {
			appCfg, cfgErr := config.Load(filepath.Join(dataDir, "config.yaml"))
			if cfgErr == nil {
				model := appCfg.SummarizeModelID()
				client, clientErr := extraction.NewLLMClient(appCfg.LLM.Provider, appCfg.ResolvedAPIKey(), model, appCfg.LLM.ClaudeBinary, appCfg.ResolvedOpenAIBaseURL())
				if clientErr == nil && client != nil {
					cfg.LLMClient = client
				}
			}
		}

		result, err := ingest.Run(cfg, []string{src.Path}, store)
		if err != nil {
			log.Printf("  ERROR: %v", err)
			continue
		}
		log.Printf("  Files: %d processed, %d skipped. Chunks: %d created.",
			result.FilesProcessed, result.FilesSkipped, result.ChunksCreated)
		if result.LearningsCreated > 0 {
			log.Printf("  Learnings: %d created (from destillation)", result.LearningsCreated)
		}
	}
}

func runListDocs(args []string) {
	fs := flag.NewFlagSet("list-docs", flag.ExitOnError)
	project := fs.String("project", "", "Filter by project")
	fs.Parse(args)

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	sources, err := store.ListDocSources(*project)
	if err != nil {
		log.Fatalf("list sources: %v", err)
	}

	if len(sources) == 0 {
		fmt.Println("No documentation sources registered.")
		return
	}

	fmt.Printf("%-15s %-10s %8s %-20s %s\n", "NAME", "VERSION", "CHUNKS", "LAST SYNC", "PROJECT")
	for _, s := range sources {
		proj := s.Project
		if proj == "" {
			proj = "(global)"
		}
		fmt.Printf("%-15s %-10s %8d %-20s %s\n", s.Name, s.Version, s.ChunkCount, s.LastSync.Format("2006-01-02 15:04"), proj)
	}
}

func runRemoveDocs(args []string) {
	fs := flag.NewFlagSet("remove-docs", flag.ExitOnError)
	name := fs.String("name", "", "Source name to remove (required)")
	project := fs.String("project", "", "Project filter")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --name is required")
		os.Exit(1)
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	delResult, err := store.DeleteDocSource(*name, *project)
	if err != nil {
		log.Fatalf("delete source: %v", err)
	}

	deleted := len(delResult.DeletedLearningIDs)
	fmt.Printf("Removed %s — chunks + %d learnings deleted.\n", *name, deleted)

	// Clean up clone directory if it was a URL import
	if delResult != nil && delResult.SourcePath != "" {
		cloneDir := filepath.Join(dataDir, "docs", *name)
		if _, statErr := os.Stat(cloneDir); statErr == nil {
			if removeErr := os.RemoveAll(cloneDir); removeErr != nil {
				log.Printf("warn: remove clone dir: %v", removeErr)
			} else {
				log.Printf("Clone directory removed: %s", cloneDir)
			}
		}
	}
}

// tryGitPull checks if the path is inside a git repo and pulls if so.
// Returns true if there were updates.
func tryGitPull(path string) bool {
	// Walk up from path to find .git directory
	gitRoot := findGitRoot(path)
	if gitRoot == "" {
		return false
	}

	// Check for updates: fetch first, then compare
	fetchCmd := exec.Command("git", "-C", gitRoot, "fetch", "--quiet")
	if err := fetchCmd.Run(); err != nil {
		log.Printf("  Git fetch failed: %v", err)
		return false
	}

	// Check if there are updates
	diffCmd := exec.Command("git", "-C", gitRoot, "diff", "HEAD", "FETCH_HEAD", "--stat")
	output, err := diffCmd.Output()
	if err != nil || len(output) == 0 {
		return false // no updates or error
	}

	// Pull the updates
	pullCmd := exec.Command("git", "-C", gitRoot, "pull", "--quiet")
	if err := pullCmd.Run(); err != nil {
		log.Printf("  Git pull failed: %v", err)
		return false
	}

	return true
}

// findGitRoot walks up from path to find the nearest .git directory.
func findGitRoot(path string) string {
	absPath, _ := filepath.Abs(path)
	info, err := os.Stat(absPath)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}
	for {
		if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
			return absPath
		}
		parent := filepath.Dir(absPath)
		if parent == absPath {
			return ""
		}
		absPath = parent
	}
}

// cloneOrPullRepo clones a git URL to ~/.claude/yesmem/docs/<name>/ or pulls if exists.
func cloneOrPullRepo(name, url string) string {
	dataDir := yesmemDataDir()
	docsDir := filepath.Join(dataDir, "docs", name)

	if _, err := os.Stat(filepath.Join(docsDir, ".git")); err == nil {
		// Already cloned — pull
		log.Printf("Pulling updates for %s...", name)
		cmd := exec.Command("git", "-C", docsDir, "pull", "--quiet")
		if output, err := cmd.CombinedOutput(); err != nil {
			log.Printf("  Git pull failed: %v: %s", err, output)
		}
		return docsDir
	}

	// Clone fresh
	log.Printf("Cloning %s → %s", url, docsDir)
	if err := os.MkdirAll(filepath.Dir(docsDir), 0755); err != nil {
		log.Printf("  mkdir: %v", err)
		return ""
	}
	cmd := exec.Command("git", "clone", "--depth", "1", url, docsDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("  Git clone failed: %v: %s", err, output)
		return ""
	}

	return docsDir
}
