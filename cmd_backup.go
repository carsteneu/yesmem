package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func runBackup() {
	dataDir := yesmemDataDir()
	now := time.Now().Format("20060102-150405")

	// Default backup dir
	backupDir := filepath.Join(dataDir, "backups")

	// Optional override: yesmem backup [path]
	if len(os.Args) > 2 {
		backupDir = os.Args[2]
	}

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		log.Fatalf("create backup dir: %v", err)
	}

	// Back up main DB
	mainDB := filepath.Join(dataDir, "yesmem.db")
	mainDest := filepath.Join(backupDir, fmt.Sprintf("yesmem-%s.db", now))
	if err := vacuumInto(mainDB, mainDest); err != nil {
		log.Fatalf("backup yesmem.db: %v", err)
	}
	printResult("yesmem.db", mainDB, mainDest, backupDir)

	// Back up runtime DB
	runtimeDB := filepath.Join(dataDir, "runtime.db")
	if info, err := os.Stat(runtimeDB); err == nil && info.Size() > 0 {
		runtimeDest := filepath.Join(backupDir, fmt.Sprintf("yesmem-%s-runtime.db", now))
		if err := vacuumInto(runtimeDB, runtimeDest); err != nil {
			log.Fatalf("backup runtime.db: %v", err)
		}
		printResult("runtime.db", runtimeDB, runtimeDest, backupDir)
	}
}

func vacuumInto(srcPath, destPath string) error {
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer db.Close()

	_, err = db.Exec("VACUUM INTO ?", destPath)
	if err != nil {
		return fmt.Errorf("VACUUM INTO: %w", err)
	}
	return nil
}

func printResult(name, srcPath, destPath, backupDir string) {
	srcInfo, _ := os.Stat(srcPath)
	destInfo, _ := os.Stat(destPath)

	srcSize := humanSize(srcInfo.Size())
	destSize := humanSize(destInfo.Size())

	// Show relative path from backupDir's parent for cleaner output
	rel, err := filepath.Rel(filepath.Dir(backupDir), destPath)
	if err != nil {
		rel = destPath
	}

	fmt.Printf("✓ Backed up %s (%s) → %s (%s)\n", name, srcSize, rel, destSize)
}

func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KB", b/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
