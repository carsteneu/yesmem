package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// parseTimeArg parses a time argument: ISO date (2026-03-18), relative (7d, 30d), or empty.
func parseTimeArg(s string) string {
	if s == "" {
		return ""
	}
	s = strings.TrimSpace(s)
	// Relative: "7d", "30d", "24h"
	if len(s) >= 2 {
		unit := s[len(s)-1]
		numStr := s[:len(s)-1]
		if n, err := strconv.Atoi(numStr); err == nil {
			switch unit {
			case 'd':
				return time.Now().AddDate(0, 0, -n).Format(time.RFC3339)
			case 'h':
				return time.Now().Add(-time.Duration(n) * time.Hour).Format(time.RFC3339)
			}
		}
	}
	// Try ISO date
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format(time.RFC3339)
	}
	// Try full ISO datetime
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format(time.RFC3339)
	}
	return s
}

func runStats() {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	project := fs.String("project", "", "filter by project")
	fs.StringVar(project, "p", "", "filter by project (short)")
	since := fs.String("since", "", "only learnings created after (ISO date or 7d/30d)")
	before := fs.String("before", "", "only learnings created before (ISO date or 7d/30d)")
	jsonOut := fs.Bool("json", false, "output as JSON")
	fs.Parse(os.Args[2:])

	f := storage.StatsFilter{
		Project: *project,
		Since:   parseTimeArg(*since),
		Before:  parseTimeArg(*before),
	}

	dataDir := yesmemDataDir()
	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	stats, _ := store.GetLearningStatsF(f)
	topPerformers, _ := store.GetTopPerformers(3, *project)
	toxicLearnings, _ := store.GetToxicLearnings(3, *project)
	persona, _ := store.GetPersonaConfidenceStats()
	counts, _ := store.GetLearningCounts(*project)
	coverage, _ := store.GetCoverageStats()

	if *jsonOut {
		out := map[string]any{
			"filter":         f,
			"stats":          stats,
			"top_performers": topPerformers,
			"toxic":          toxicLearnings,
			"persona":        persona,
			"categories":     counts,
			"coverage":       coverage,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	// Header
	projLabel := "all"
	if *project != "" {
		projLabel = *project
	}
	header := fmt.Sprintf("=== YesMem Health (%s)", projLabel)
	if *since != "" || *before != "" {
		parts := []string{}
		if *since != "" {
			parts = append(parts, "since "+*since)
		}
		if *before != "" {
			parts = append(parts, "before "+*before)
		}
		header += " [" + strings.Join(parts, ", ") + "]"
	}
	header += " ==="
	fmt.Fprintf(os.Stderr, "%s\n", header)

	fmt.Fprintf(os.Stderr, "Learnings:     %d active | %d archived | %d superseded\n",
		stats.ActiveCount, stats.ArchivedCount, stats.SupersededCount)

	signalRate := 0.0
	if stats.TotalInjectCount > 0 {
		signalRate = float64(stats.TotalInjectCount-stats.TotalNoiseCount) / float64(stats.TotalInjectCount) * 100
	}
	fmt.Fprintf(os.Stderr, "Precision:     %.1f%% (avg use/inject bei inject>=1)\n", stats.AvgPrecision*100)

	noiseRate := 0.0
	if stats.TotalInjectCount > 0 {
		noiseRate = float64(stats.TotalNoiseCount) / float64(stats.TotalInjectCount) * 100
	}
	fmt.Fprintf(os.Stderr, "Noise-Rate:    %.1f%% (%d noisy / %d injected)\n",
		noiseRate, stats.TotalNoiseCount, stats.TotalInjectCount)

	fmt.Fprintf(os.Stderr, "Use-Count:     %d total\n", stats.TotalUseCount)
	fmt.Fprintf(os.Stderr, "Save-Count:    %d (gotcha warnings that prevented errors)\n", stats.TotalSaveCount)
	fmt.Fprintf(os.Stderr, "Fail-Count:    %d (used but did not prevent error)\n", stats.TotalFailCount)

	if len(topPerformers) > 0 {
		var parts []string
		for _, l := range topPerformers {
			parts = append(parts, fmt.Sprintf("#%d (%d uses)", l.ID, l.UseCount))
		}
		fmt.Fprintf(os.Stderr, "Top-Performer: %s\n", strings.Join(parts, ", "))
	}

	fmt.Fprintf(os.Stderr, "Dead Weight:   %d Learnings mit 0 uses bei >=10 injections\n", stats.DeadWeightCount)

	if stats.ToxicCount > 0 {
		fmt.Fprintf(os.Stderr, "Toxic:         %d Learnings mit fail_count >= 3\n", stats.ToxicCount)
		for _, t := range toxicLearnings {
			content := t.Content
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			fmt.Fprintf(os.Stderr, "               #%d (%d fails) %s\n", t.ID, t.FailCount, content)
		}
	}

	fmt.Fprintf(os.Stderr, "Persona:       Confidence Ø %.2f | volatilste: %s (%.2f) | stabilste: %s (%.2f)\n",
		persona.AvgConfidence, persona.LowestDim, persona.LowestConf, persona.HighestDim, persona.HighestConf)

	type catCount struct {
		cat   string
		count int
	}
	var sorted []catCount
	for cat, count := range counts {
		sorted = append(sorted, catCount{cat, count})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	var catParts []string
	for _, cc := range sorted {
		catParts = append(catParts, fmt.Sprintf("%s:%d", cc.cat, cc.count))
	}
	fmt.Fprintf(os.Stderr, "Categories:    %s\n", strings.Join(catParts, " "))

	if coverage != nil {
		fmt.Fprintf(os.Stderr, "\n--- Coverage ---\n")
		embPct := 0.0
		if coverage.EmbeddingTotal > 0 {
			embPct = float64(coverage.EmbeddingDone) / float64(coverage.EmbeddingTotal) * 100
		}
		fmt.Fprintf(os.Stderr, "Embeddings:    %d/%d (%.0f%%)\n", coverage.EmbeddingDone, coverage.EmbeddingTotal, embPct)
		narPct := 0.0
		if coverage.NarrativeTotal > 0 {
			narPct = float64(coverage.NarrativeDone) / float64(coverage.NarrativeTotal) * 100
		}
		fmt.Fprintf(os.Stderr, "Narratives:    %d/%d (%.0f%%)\n", coverage.NarrativeDone, coverage.NarrativeTotal, narPct)
		fmt.Fprintf(os.Stderr, "Profiles:      %d/%d\n", coverage.ProfileDone, coverage.ProfileTotal)
		fmt.Fprintf(os.Stderr, "Gaps:          %d offen | %d auto-resolved | %d review-resolved\n", coverage.GapsOpen, coverage.GapsResolved, coverage.GapsReviewResolved)
	}

	fmt.Fprintf(os.Stderr, "Decay:         %d Learnings unter Stability 5.0\n", stats.DecayingCount)
	fmt.Fprintf(os.Stderr, "Signal-Rate:   %.1f%%\n", signalRate)
}
