
package locomo

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
)

// JudgeResult holds the outcome of judging a single QA pair.
type JudgeResult struct {
	Question  string
	Gold      string
	Generated string
	Category  int
	Label     string // "CORRECT" or "WRONG"
	Score     int    // 1 or 0
	Reasoning string
	ToolCalls []ToolCallLog // agentic mode only
}

// CategoryScore holds aggregated results for one question category.
type CategoryScore struct {
	Category int     `json:"category"`
	Name     string  `json:"name"`
	Count    int     `json:"count"`
	Correct  int     `json:"correct"`
	Score    float64 `json:"score"`
}

// Report holds the full benchmark report across all categories.
type Report struct {
	Categories   []CategoryScore `json:"categories"`
	OverallMean  float64         `json:"overall_mean"`
	TotalCount   int             `json:"total_count"`
	TotalCorrect int             `json:"total_correct"`
}

// RunStats holds statistics across multiple benchmark runs.
type RunStats struct {
	Runs   int     `json:"runs"`
	Mean   float64 `json:"mean"`
	Stddev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// Baselines maps system names to their published LoCoMo scores.
var Baselines = map[string]float64{
	"TSM":   0.77,
	"Mem0":  0.66,
	"A-Mem": 0.62,
	"Zep":   0.58,
}

// Aggregate computes per-category and overall scores from judge results.
// Category 5 (adversarial) is excluded from scoring per LoCoMo convention.
func Aggregate(results []JudgeResult) Report {
	catMap := make(map[int]*CategoryScore)

	for _, r := range results {
		if r.Category == 5 {
			continue
		}
		cs, ok := catMap[r.Category]
		if !ok {
			cs = &CategoryScore{
				Category: r.Category,
				Name:     CategoryName(r.Category),
			}
			catMap[r.Category] = cs
		}
		cs.Count++
		cs.Correct += r.Score
	}

	// Sort categories by number.
	cats := make([]CategoryScore, 0, len(catMap))
	for _, cs := range catMap {
		if cs.Count > 0 {
			cs.Score = float64(cs.Correct) / float64(cs.Count)
		}
		cats = append(cats, *cs)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Category < cats[j].Category })

	var totalCount, totalCorrect int
	for _, cs := range cats {
		totalCount += cs.Count
		totalCorrect += cs.Correct
	}

	var overallMean float64
	if totalCount > 0 {
		overallMean = float64(totalCorrect) / float64(totalCount)
	}

	return Report{
		Categories:   cats,
		OverallMean:  overallMean,
		TotalCount:   totalCount,
		TotalCorrect: totalCorrect,
	}
}

// ComputeRunStats calculates mean, stddev, min, max across multiple run scores.
func ComputeRunStats(scores []float64) RunStats {
	n := len(scores)
	if n == 0 {
		return RunStats{}
	}

	var sum float64
	minVal := math.MaxFloat64
	maxVal := -math.MaxFloat64

	for _, s := range scores {
		sum += s
		if s < minVal {
			minVal = s
		}
		if s > maxVal {
			maxVal = s
		}
	}

	mean := sum / float64(n)

	var variance float64
	for _, s := range scores {
		diff := s - mean
		variance += diff * diff
	}
	variance /= float64(n)
	stddev := math.Sqrt(variance)

	return RunStats{
		Runs:   n,
		Mean:   mean,
		Stddev: stddev,
		Min:    minVal,
		Max:    maxVal,
	}
}

// PrintReport writes a formatted report to w.
func PrintReport(w io.Writer, report Report, runNum, totalRuns int) {
	if totalRuns > 1 {
		fmt.Fprintf(w, "\n=== Run %d/%d ===\n", runNum, totalRuns)
	}
	fmt.Fprintf(w, "\nLoCoMo Benchmark Results\n")
	fmt.Fprintf(w, "%-15s %6s %8s %8s\n", "Category", "Count", "Correct", "Score")
	fmt.Fprintf(w, "%-15s %6s %8s %8s\n", "----------", "-----", "-------", "-----")

	for _, cs := range report.Categories {
		fmt.Fprintf(w, "%-15s %6d %8d %8.4f\n", cs.Name, cs.Count, cs.Correct, cs.Score)
	}

	fmt.Fprintf(w, "%-15s %6s %8s %8s\n", "----------", "-----", "-------", "-----")
	fmt.Fprintf(w, "%-15s %6d %8d %8.4f\n", "Overall", report.TotalCount, report.TotalCorrect, report.OverallMean)

	fmt.Fprintf(w, "\nBaselines:\n")
	names := make([]string, 0, len(Baselines))
	for name := range Baselines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %.2f\n", name, Baselines[name])
	}
}

// PrintMultiRunReport writes multi-run statistics to w.
func PrintMultiRunReport(w io.Writer, stats RunStats) {
	fmt.Fprintf(w, "\n=== Multi-Run Summary (%d runs) ===\n", stats.Runs)
	fmt.Fprintf(w, "  Mean:   %.4f\n", stats.Mean)
	fmt.Fprintf(w, "  Stddev: %.4f\n", stats.Stddev)
	fmt.Fprintf(w, "  Min:    %.4f\n", stats.Min)
	fmt.Fprintf(w, "  Max:    %.4f\n", stats.Max)
}

// jsonReport is the combined JSON output structure.
type jsonReport struct {
	Report   Report    `json:"report"`
	RunStats *RunStats `json:"run_stats,omitempty"`
}

// PrintJSON writes the report (and optional run stats) as JSON to w.
func PrintJSON(w io.Writer, report Report, stats *RunStats) {
	out := jsonReport{
		Report:   report,
		RunStats: stats,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
