package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/capfile"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
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

	// 3. Config migration: insert missing fields as commented snippets (preserves comments)
	cfgPath := filepath.Join(dataDir, "config.yaml")
	if n, err := config.MigrateConfig(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Config migration warning: %v\n", err)
	} else if n > 0 {
		fmt.Printf("  Config migration: OK (%d fields added)\n", n)
	} else {
		fmt.Println("  Config migration: OK (no changes needed)")
	}

	// 3b. Proxy keys sitting on the top level are inert: the loader reads them
	// under proxy:. Move them there (timestamped backup) instead of leaving a
	// setting that looks configured and does nothing.
	if moved, err := config.MoveMisplacedProxyKeys(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Config placement warning: %v\n", err)
	} else if len(moved) > 0 {
		for _, key := range moved {
			fmt.Printf("  Config placement: moved %q to proxy.%s (the proxy applies config.yaml at start)\n", key, key)
		}
	}

	// 3c. Report what the schema cannot read. Unknown keys stay untouched, a
	// misplaced key that proxy: already sets stays where it is as well. `yesmem
	// setup` prints the same lines from the same function.
	setup.PrintConfigCheck(cfgPath, os.Stdout, os.Stderr)

	// 4. Hooks: ensure all yesmem hooks are registered in settings.json
	if err := setup.EnsureHooks(); err != nil {
		fmt.Fprintf(os.Stderr, "  Hooks update warning: %v\n", err)
	} else {
		fmt.Println("  Hooks: OK")
	}

	// 5. Bundled skills. Every step below writes into the home directory, so an
	// unusable one stops the migration here instead of scattering files relative
	// to the working directory (YM-350).
	home, homeErr := setup.UserHome()
	if homeErr != nil {
		fmt.Fprintf(os.Stderr, "  Home directory warning: %v\n", homeErr)
		fmt.Printf("Migration incomplete (version: %s)\n", buildinfo.Version)
		return
	}
	if n, err := setup.InstallBundledSkills(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Skills warning: %v\n", err)
	} else {
		fmt.Printf("  Skills: %d updated\n", n)
	}

	// 5b. Bundled capabilities
	// The installer has to write where the daemon imports from: setup always wrote
	// ~/.claude/caps and ignored a configured caps_dir (YM-353). A cap whose body
	// the bundle was not derived from is kept and reported here (YM-450).
	appCfg, _ := config.Load(cfgPath)
	if n, conflicts, err := setup.InstallBundledCaps(config.CapsDirPath(appCfg.CapsDir, dataDir)); err != nil {
		fmt.Fprintf(os.Stderr, "  Caps warning: %v\n", err)
	} else {
		fmt.Printf("  Caps: %d updated\n", n)
		setup.PrintCapConflicts(os.Stdout, conflicts)
	}

	// 5b1. The installation's own RULES.md — the copy the guard enforces from. Setup writes it, but a
	// machine that only ever updates never re-runs setup, so a release could not deliver a standard rule
	// and a new installation could end up with no catalogue at all. Absent is written, a provably
	// untouched copy refreshed, an operator-edited one kept.
	if path, err := setup.InstallRULESmd(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "  Rules warning: %v\n", err)
	} else if path != "" {
		fmt.Println("  Rules: installed")
	} else {
		fmt.Println("  Rules: OK")
	}

	// 5b2. Retired caps leave the catalog. The import path ignores their CAP.md
	// and the export path never rewrites it; the row an earlier version stored is
	// superseded here (YM-181, YM-197).
	if store, err := storage.Open(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Retired caps warning: %v\n", err)
	} else {
		n, retireErr := retireObsoleteCaps(store, os.Stdout)
		store.Close()
		if retireErr != nil {
			fmt.Fprintf(os.Stderr, "  Retired caps warning: %v\n", retireErr)
		} else if n == 0 {
			fmt.Println("  Retired caps: OK")
		}
	}

	// 5b3. Database files nothing opens any more (YM-180).
	if removed := setup.RemoveLegacyDBFiles(dataDir); len(removed) > 0 {
		fmt.Printf("  Legacy database files: %s\n", setup.DescribeLegacyDBRemoval(removed))
	} else {
		fmt.Println("  Legacy database files: OK")
	}

	// 5c. Opencode plugin (rule_guard, code_nav, auto_resolve)
	if n, detail, err := setup.InstallOpencodePlugin(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Plugin warning: %v\n", err)
	} else if detail != "" {
		fmt.Printf("  Plugin: %s\n", detail)
	} else {
		fmt.Printf("  Plugin: %d files updated\n", n)
	}
	// 5d. Opencode window plugin (session → terminal window registration)
	if n, err := setup.InstallWindowPlugin(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Window plugin warning: %v\n", err)
	} else if n > 0 {
		fmt.Println("  Window plugin: installed to ~/.config/opencode/plugins/yesmem-window/")
	}
	// 5e. Opencode agent themes (type accent for spawned agent TUIs)
	if n, err := setup.InstallThemes(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Themes warning: %v\n", err)
	} else if n > 0 {
		fmt.Printf("  Themes: %d updated in ~/.config/opencode/themes/\n", n)
	}
	// 5f. Opencode commands (~/.config/opencode/commands). A full setup writes
	// them, so an update left them stale or missing entirely; after an uninstall
	// only another full setup restored them (YM-386).
	if n, err := setup.InstallOpencodeCommands(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Opencode commands warning: %v\n", err)
	} else if n > 0 {
		fmt.Printf("  Opencode commands: %d updated in ~/.config/opencode/commands/\n", n)
	} else {
		fmt.Println("  Opencode commands: OK")
	}

	// 6. Hook-guard state: clean up legacy JSON cooldown (now DB-backed)
	if removed, err := setup.MigrateGuardState(home); err != nil {
		fmt.Fprintf(os.Stderr, "  Guard state warning: %v\n", err)
	} else if removed != "" {
		fmt.Printf("  Guard state: removed legacy %s\n", removed)
	} else {
		fmt.Println("  Guard state: OK")
	}

	// 7. systemd units: drop obsolete network-online.target wait (local-first services)
	// and upgrade the daemon unit to the notify type with its watchdog. systemd
	// only reads a rewritten unit after a reload, so a changed file needs one.
	// 7b. The code graph daemon: an installation that predates this step has no unit, so
	// whichever client touched the binary first started the daemon and nothing knows it.
	// Without a running daemon every scan pays the full cold start — measured 4.5 s
	// against 1.1 s — and without a version check the installation stays on whatever
	// release setup downloaded once (0.10.8 while 0.12.1 was current, 2026-11-13).
	if runtime.GOOS == "linux" {
		if changed, err := setup.EnsureCBMUnit(home, setup.CBMBinaryPath(dataDir)); err != nil {
			fmt.Fprintf(os.Stderr, "  CBM unit warning: %v\n", err)
		} else if changed {
			fmt.Println("  CBM daemon: unit written and started")
		} else {
			fmt.Println("  CBM daemon: unit OK")
		}
		if changed, err := setup.EnsureCBMVersion(dataDir, setup.InstalledCBMVersion(dataDir)); err != nil {
			fmt.Fprintf(os.Stderr, "  CBM version warning: %v\n", err)
		} else if changed {
			fmt.Println("  CBM version: upgraded")
		}
	}

	if n, err := setup.MigrateSystemdUnits(home, dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "  systemd units warning: %v\n", err)
	} else {
		if n > 0 {
			fmt.Printf("  systemd units: rewrote %d unit(s) (network wait, daemon watchdog)\n", n)
		} else {
			fmt.Println("  systemd units: OK")
		}
		// systemd reads a rewritten unit only after a reload, and an install whose
		// unit an earlier run migrated needs it too: without it the notify unit's
		// watchdog is never armed.
		if daemonUnitInstalled(home) {
			if err := setup.ReloadSystemdUnits(); err != nil {
				fmt.Fprintf(os.Stderr, "  systemd units warning: %v\n", err)
			}
		}
		if hint := systemdRollbackHint(home); hint != "" {
			fmt.Print(hint)
		}
	}

	fmt.Printf("Migration complete (version: %s)\n", buildinfo.Version)
}

// retireObsoleteCaps supersedes the cap learning of every cap in
// capfile.RetiredBundledCaps. The cap then leaves the catalog (which reads the
// active cap learnings) and can no longer be executed. The import and export
// paths ignore those names, so the CAP.md that stays on disk is inert; this step
// clears the row an earlier version already stored.
//
// supersededByID 0: a retired cap has no successor. Note that SupersedeLearning
// with a zero winner also sets valid_until, which is what withdraws the row.
func retireObsoleteCaps(store *storage.Store, out io.Writer) (int, error) {
	if len(capfile.RetiredBundledCaps) == 0 {
		return 0, nil
	}
	learnings, err := store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		return 0, fmt.Errorf("list caps: %w", err)
	}
	retired := 0
	for _, l := range learnings {
		meta, err := daemon.ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if !capfile.IsRetiredCap(meta.Name) {
			continue
		}
		if err := store.SupersedeLearning(l.ID, 0, "retired cap: yesmem no longer ships it (the wiki moved to yesmem wiki-render)"); err != nil {
			return retired, fmt.Errorf("supersede cap %q: %w", meta.Name, err)
		}
		fmt.Fprintf(out, "  Retired cap: %s (learning %d)\n", meta.Name, l.ID)
		retired++
	}
	return retired, nil
}

// systemdRollbackHint names the way back for a binary that predates the notify
// unit. The migration keeps the unit as it was before the rewrite; without the
// hint the copy is only found by someone who already knows about it.
//
// It also names the installs this fix cannot help: a unit an earlier binary already
// migrated to the notify type has no copy, and reconstructing one would invent
// bytes that were never there. Saying so is the honest half of that gap.
func systemdRollbackHint(home string) string {
	unit := filepath.Join(home, ".config", "systemd", "user", "yesmem.service")
	backup := setup.UnitBackupPath(unit)
	if _, err := os.Stat(backup); err == nil {
		return fmt.Sprintf("  systemd units: an older binary needs the unit back: cp %s %s && systemctl --user daemon-reload\n", backup, unit)
	}
	content, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(content), "Type=notify") {
		return ""
	}
	return fmt.Sprintf("  systemd units: %s was migrated to the notify type by an earlier version and has no copy; an older binary needs Type=simple in that file and a daemon-reload\n", unit)
}

// daemonUnitInstalled reports whether this user has a daemon unit file at all.
func daemonUnitInstalled(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "yesmem.service"))
	return err == nil
}
