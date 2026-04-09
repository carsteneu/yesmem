package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
)

func runExport() {
	path := "yesmem-export.json"
	if len(os.Args) > 2 {
		path = os.Args[2]
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if err := store.ExportLearnings(path); err != nil {
		log.Fatalf("export: %v", err)
	}

	learnings, _ := store.GetNonRecoverableLearnings()
	fmt.Printf("Exported %d learnings to %s\n", len(learnings), path)
}

func runImport() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: yesmem import <file.json>")
		os.Exit(1)
	}
	path := os.Args[2]

	dataDir := yesmemDataDir()
	store, err := storage.Open(dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	imported, skipped, err := store.ImportLearnings(path)
	if err != nil {
		log.Fatalf("import: %v", err)
	}
	fmt.Printf("Imported %d learnings, skipped %d duplicates from %s\n", imported, skipped, path)
}

func runCost() {
	dataDir := yesmemDataDir()
	dbPath := filepath.Join(dataDir, "yesmem.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	days := 7
	if len(os.Args) > 2 {
		if d, err := fmt.Sscanf(os.Args[2], "%d", &days); d == 0 || err != nil {
			days = 7
		}
	}

	history, err := store.GetSpendHistory(days)
	if err != nil {
		log.Fatalf("get spend: %v", err)
	}

	if len(history) == 0 {
		fmt.Println("No spend data yet. Budget tracking starts after next daemon restart.")
		return
	}

	// Group by day
	type dayTotal struct {
		extract storage.DailySpend
		quality storage.DailySpend
	}
	byDay := make(map[string]*dayTotal)
	var dayOrder []string
	for _, ds := range history {
		if _, ok := byDay[ds.Day]; !ok {
			byDay[ds.Day] = &dayTotal{}
			dayOrder = append(dayOrder, ds.Day)
		}
		switch ds.Bucket {
		case "extract":
			byDay[ds.Day].extract = ds
		case "quality":
			byDay[ds.Day].quality = ds
		}
	}

	fmt.Printf("%-12s %10s %8s %10s %8s %10s\n", "Day", "Extract $", "Calls", "Quality $", "Calls", "Total $")
	fmt.Println(strings.Repeat("─", 62))

	var totalExtract, totalQuality float64
	var totalExtractCalls, totalQualityCalls int
	for _, day := range dayOrder {
		dt := byDay[day]
		total := dt.extract.SpentUSD + dt.quality.SpentUSD
		totalExtract += dt.extract.SpentUSD
		totalQuality += dt.quality.SpentUSD
		totalExtractCalls += dt.extract.Calls
		totalQualityCalls += dt.quality.Calls
		fmt.Printf("%-12s %9.2f %8d %9.2f %8d %9.2f\n",
			day, dt.extract.SpentUSD, dt.extract.Calls,
			dt.quality.SpentUSD, dt.quality.Calls, total)
	}

	fmt.Println(strings.Repeat("─", 62))
	fmt.Printf("%-12s %9.2f %8d %9.2f %8d %9.2f\n",
		"Total", totalExtract, totalExtractCalls, totalQuality, totalQualityCalls, totalExtract+totalQuality)
	if len(dayOrder) > 1 {
		avg := (totalExtract + totalQuality) / float64(len(dayOrder))
		fmt.Printf("%-12s %9s %8s %9s %8s %9.2f\n", "Avg/day", "", "", "", "", avg)
	}
}
