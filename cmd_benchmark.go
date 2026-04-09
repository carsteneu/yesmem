package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
)

func runBenchmark() {
	fs := flag.NewFlagSet("benchmark", flag.ExitOnError)
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
	explorationCount, _ := store.CountExplorationLearningsF(f)
	persona, _ := store.GetPersonaConfidenceStats()
	volatility, _ := store.GetPersonaVolatility(30)
	catPrecision, _ := store.GetCategoryPrecisionF(f)
	evolution, _ := store.GetEvolutionStatsF(f)
	coverage, _ := store.GetCoverageStatsF(f)
	sourceAttrib, _ := store.GetSourceAttribution(f)
	spendSummary, _ := store.GetSpendSummary(f)
	recall, _ := store.GetCrossSessionRecallStats(f)

	signalRate := 0.0
	noiseRate := 0.0
	if stats.TotalInjectCount > 0 {
		noiseRate = float64(stats.TotalNoiseCount) / float64(stats.TotalInjectCount) * 100
		signalRate = 100 - noiseRate
	}

	if *jsonOut {
		out := map[string]any{
			"filter": f,
			"wirkungsquote": map[string]any{
				"precision_pct":     stats.AvgPrecision * 100,
				"signal_rate_pct":   signalRate,
				"noise_rate_pct":    noiseRate,
				"exploration_count": explorationCount,
				"active_count":      stats.ActiveCount,
				"total_use":         stats.TotalUseCount,
				"total_save":        stats.TotalSaveCount,
				"total_fail":        stats.TotalFailCount,
			},
			"category_precision": catPrecision,
			"evolution":          evolution,
			"coverage":           coverage,
			"persona": map[string]any{
				"avg_confidence":     persona.AvgConfidence,
				"trait_count":        persona.TraitCount,
				"highest_dimension":  persona.HighestDim,
				"highest_confidence": persona.HighestConf,
				"lowest_dimension":   persona.LowestDim,
				"lowest_confidence":  persona.LowestConf,
				"volatility_30d":     volatility,
			},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}

	// Header
	header := "=== YesMem Benchmark"
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

	fmt.Fprintf(os.Stderr, "Effectiveness:\n")
	fmt.Fprintf(os.Stderr, "  Precision (use/inject):  %.1f%% (avg over %d active learnings)\n",
		stats.AvgPrecision*100, stats.ActiveCount)
	fmt.Fprintf(os.Stderr, "  Signal-Rate:             %.1f%%\n", signalRate)
	fmt.Fprintf(os.Stderr, "  Noise-Rate:              %.1f%%\n", noiseRate)
	fmt.Fprintf(os.Stderr, "  Use-Count total:         %d\n", stats.TotalUseCount)
	fmt.Fprintf(os.Stderr, "  Save-Count total:        %d (errors prevented)\n", stats.TotalSaveCount)
	fmt.Fprintf(os.Stderr, "  Fail-Count total:        %d (used but failed anyway)\n", stats.TotalFailCount)
	fmt.Fprintf(os.Stderr, "  Exploration (< 3 inj.):  %d learnings still on probation\n", explorationCount)

	if len(catPrecision) > 0 {
		fmt.Fprintf(os.Stderr, "\nPer-Category Effectiveness:\n")
		behavioral := map[string]bool{"gotcha": true, "feedback": true}
		contextual := map[string]bool{"narrative": true, "pivot_moment": true, "unfinished": true}

		fmt.Fprintf(os.Stderr, "  Active-Reference (cited by Claude):\n")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %6s %8s %8s\n", "Category", "Count", "Used", "Prec%", "ØFail")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %6s %8s %8s\n", "────────────────────", "──────", "──────", "────────", "────────")
		for _, cp := range catPrecision {
			if behavioral[cp.Category] || contextual[cp.Category] {
				continue
			}
			fmt.Fprintf(os.Stderr, "  %-20s %6d %6d %7.1f%% %8.1f\n",
				cp.Category, cp.Count, cp.WithUses, cp.Precision*100, cp.AvgFail)
		}

		fmt.Fprintf(os.Stderr, "\n  Behavioral (warnings that prevent errors):\n")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %8s %8s %10s %10s\n", "Category", "Count", "Injects", "Saves", "Save-Rate", "Inj/Msg%")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %8s %8s %10s %10s\n", "────────────────────", "──────", "────────", "────────", "──────────", "──────────")
		for _, cp := range catPrecision {
			if !behavioral[cp.Category] {
				continue
			}
			saveRate := 0.0
			if cp.TotalInject > 0 {
				saveRate = float64(cp.TotalSave) / float64(cp.TotalInject) * 100
			}
			injRate := 0.0
			if coverage != nil && coverage.TotalMessages > 0 {
				injRate = float64(cp.TotalInject) / float64(coverage.TotalMessages) * 100
			}
			fmt.Fprintf(os.Stderr, "  %-20s %6d %8d %8d %9.1f%% %9.1f%%\n",
				cp.Category, cp.Count, cp.TotalInject, cp.TotalSave, saveRate, injRate)
		}

		fmt.Fprintf(os.Stderr, "\n  Contextual (background context, injection = value):\n")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %8s %10s\n", "Category", "Count", "Injects", "Inj/Msg%")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %8s %10s\n", "────────────────────", "──────", "────────", "──────────")
		for _, cp := range catPrecision {
			if !contextual[cp.Category] {
				continue
			}
			injRate := 0.0
			if coverage != nil && coverage.TotalMessages > 0 {
				injRate = float64(cp.TotalInject) / float64(coverage.TotalMessages) * 100
			}
			fmt.Fprintf(os.Stderr, "  %-20s %6d %8d %9.1f%%\n",
				cp.Category, cp.Count, cp.TotalInject, injRate)
		}
	}

	if evolution != nil && evolution.TotalSuperseded > 0 {
		ruleBasedPct := float64(evolution.RuleBasedCount) / float64(evolution.TotalSuperseded) * 100
		fmt.Fprintf(os.Stderr, "\nEvolution:\n")
		fmt.Fprintf(os.Stderr, "  Total superseded:        %d\n", evolution.TotalSuperseded)
		fmt.Fprintf(os.Stderr, "  Rule-based:              %d (%.0f%%)\n", evolution.RuleBasedCount, ruleBasedPct)
		fmt.Fprintf(os.Stderr, "  LLM-based:               %d (%.0f%%)\n", evolution.LLMCount, 100-ruleBasedPct)
		fmt.Fprintf(os.Stderr, "  Avg chain length:        %.1f\n", evolution.AvgChainLength)
	}

	if coverage != nil {
		fmt.Fprintf(os.Stderr, "\nCoverage:\n")
		fmt.Fprintf(os.Stderr, "  Sessions:                %d (%d messages)\n", coverage.TotalSessions, coverage.TotalMessages)
		embPct := 0.0
		if coverage.EmbeddingTotal > 0 {
			embPct = float64(coverage.EmbeddingDone) / float64(coverage.EmbeddingTotal) * 100
		}
		narPct := 0.0
		if coverage.NarrativeTotal > 0 {
			narPct = float64(coverage.NarrativeDone) / float64(coverage.NarrativeTotal) * 100
		}
		fmt.Fprintf(os.Stderr, "  Embeddings:              %d/%d (%.0f%%)\n", coverage.EmbeddingDone, coverage.EmbeddingTotal, embPct)
		fmt.Fprintf(os.Stderr, "  Narratives:              %d/%d (%.0f%%)\n", coverage.NarrativeDone, coverage.NarrativeTotal, narPct)
		fmt.Fprintf(os.Stderr, "  Profiles:                %d/%d\n", coverage.ProfileDone, coverage.ProfileTotal)
		fmt.Fprintf(os.Stderr, "  Gaps:                    %d offen | %d auto-resolved | %d review-resolved\n", coverage.GapsOpen, coverage.GapsResolved, coverage.GapsReviewResolved)
	}

	fmt.Fprintf(os.Stderr, "\nPersona-Konvergenz:\n")
	convergedLabel := "nicht erreicht"
	if persona.AvgConfidence >= 0.8 {
		convergedLabel = "erreicht"
	}
	fmt.Fprintf(os.Stderr, "  Current confidence:      avg %.2f (%s)\n", persona.AvgConfidence, convergedLabel)
	fmt.Fprintf(os.Stderr, "  Traits:                  %d active\n", persona.TraitCount)
	fmt.Fprintf(os.Stderr, "  Trait volatility 30d:    %.2f changes/day\n", volatility)
	fmt.Fprintf(os.Stderr, "  Most stable dimension:   %s (%.2f)\n", persona.HighestDim, persona.HighestConf)
	fmt.Fprintf(os.Stderr, "  Volatilste Dimension:    %s (%.2f)\n", persona.LowestDim, persona.LowestConf)

	// Source Attribution
	if len(sourceAttrib) > 0 {
		fmt.Fprintf(os.Stderr, "\nSource Attribution:\n")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %6s %8s %8s\n", "Source", "Count", "Used", "Saves", "Fails")
		fmt.Fprintf(os.Stderr, "  %-20s %6s %6s %8s %8s\n", "────────────────────", "──────", "──────", "────────", "────────")
		for _, sa := range sourceAttrib {
			fmt.Fprintf(os.Stderr, "  %-20s %6d %6d %8d %8d\n",
				sa.Source, sa.Count, sa.WithUses, sa.TotalSave, sa.TotalFail)
		}
	}

	// API Cost Savings
	if spendSummary != nil && spendSummary.TotalCalls > 0 {
		yesmemOwn := spendSummary.ExtractUSD + spendSummary.QualityUSD + spendSummary.ForkUSD
		ownPerDay := 0.0
		if spendSummary.Days > 0 {
			ownPerDay = yesmemOwn / float64(spendSummary.Days)
		}
		fmt.Fprintf(os.Stderr, "\nAPI Cost (%dd):\n", spendSummary.Days)
		fmt.Fprintf(os.Stderr, "  YesMem-eigen:            $%.2f ($%.2f/Tag)\n", yesmemOwn, ownPerDay)
		fmt.Fprintf(os.Stderr, "    Extraction:            $%.2f\n", spendSummary.ExtractUSD)
		fmt.Fprintf(os.Stderr, "    Quality/Reflection:    $%.2f\n", spendSummary.QualityUSD)
		fmt.Fprintf(os.Stderr, "    Fork (real-time):      $%.2f\n", spendSummary.ForkUSD)
		fmt.Fprintf(os.Stderr, "  Proxy-Durchlauf (User):  $%.2f ($%.2f/Tag)\n", spendSummary.ProxyUSD, spendSummary.ProxyUSD/float64(max(spendSummary.Days, 1)))
		if stats.TotalSaveCount > 0 {
			costPerSave := yesmemOwn / float64(stats.TotalSaveCount)
			fmt.Fprintf(os.Stderr, "  Cost per Error Prevented: $%.4f (nur YesMem-eigen)\n", costPerSave)
		}
	}

	// Cross-Session Recall
	if recall != nil && recall.TotalSessions > 0 {
		recallPct := 0.0
		if recall.TotalSessions > 0 {
			recallPct = float64(recall.SessionsWithRecall) / float64(recall.TotalSessions) * 100
		}
		fmt.Fprintf(os.Stderr, "\nCross-Session Recall:\n")
		fmt.Fprintf(os.Stderr, "  Sessions total:          %d (%d Projekte)\n", recall.TotalSessions, recall.UniqueProjectsWithMemory)
		fmt.Fprintf(os.Stderr, "  Sessions mit Recall:     %d (%.1f%%)\n", recall.SessionsWithRecall, recallPct)
		fmt.Fprintf(os.Stderr, "  Ø Learnings/Session:     %.1f\n", recall.AvgLearningsPerSession)
	}
}
