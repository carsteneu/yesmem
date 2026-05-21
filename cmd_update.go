package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/setup"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/update"
)

func runCheckUpdate() {
	fmt.Printf("Current version: %s\n", buildinfo.Version)
	fmt.Println("Checking for updates...")

	info, err := update.CheckForUpdate(buildinfo.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !info.Available {
		fmt.Println("Already up to date.")
		return
	}

	fmt.Printf("New version available: %s\n", info.Version)
	if info.Changelog != "" {
		fmt.Printf("\nChangelog:\n%s\n", info.Changelog)
	}
}

func runUpdate() {
	fmt.Printf("Current version: %s\n", buildinfo.Version)

	newVersion, err := update.RunUpdate(buildinfo.Version, log.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}

	if newVersion == "" {
		fmt.Println("Already up to date.")
		return
	}

	fmt.Printf("Updated to %s\n", newVersion)
	fmt.Println("Restarting services...")
	if err := update.RunRestart(log.Default()); err != nil {
		fmt.Fprintf(os.Stderr, "Restart failed: %v\n", err)
		fmt.Println("Please restart manually: yesmem restart")
		os.Exit(1)
	}
	fmt.Println("Done.")
}

func runMigrateCmd() {
	dataDir := yesmemDataDir()

	fmt.Println("Running post-update migration...")

	// 1. DB schema migration (automatic via storage.Open)
	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	store.Close()
	fmt.Println("  DB schema: OK")

	// 2. Ensure directories exist
	dirs := []string{
		filepath.Join(dataDir, "models"),
		filepath.Join(dataDir, "backups"),
	}
	for _, d := range dirs {
		os.MkdirAll(d, 0755)
	}
	fmt.Println("  Directories: OK")

	// 3. Config-Merge: load existing config (fills defaults for new fields), write back
	cfgPath := filepath.Join(dataDir, "config.yaml")
	if err := config.MergeDefaults(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Config merge warning: %v\n", err)
	} else {
		fmt.Println("  Config merge: OK")
	}

	// 4. Hooks: ensure all yesmem hooks are registered in settings.json
	if err := setup.EnsureHooks(); err != nil {
		fmt.Fprintf(os.Stderr, "  Hooks update warning: %v\n", err)
	} else {
		fmt.Println("  Hooks: OK")
	}

	// 5. Bundled skills
	home, _ := os.UserHomeDir()
	if n, err := setup.InstallBundledSkills(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Skills warning: %v\n", err)
	} else {
		fmt.Printf("  Skills: %d updated\n", n)
	}

	// 5b. Bundled capabilities
	if n, err := setup.InstallBundledCaps(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Caps warning: %v\n", err)
	} else {
		fmt.Printf("  Caps: %d updated\n", n)
	}

	// 5c. Opencode plugin (rule_guard, code_nav, auto_resolve)
	if n, err := setup.InstallOpencodePlugin(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Plugin warning: %v\n", err)
	} else {
		fmt.Printf("  Plugin: %d files updated\n", n)
	}

	fmt.Printf("Migration complete (version: %s)\n", buildinfo.Version)
}
