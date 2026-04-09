
package locomo

import (
	"fmt"
	"io"
	"math/rand"
)

// Pricing per million tokens (USD).
var modelPricing = map[string][2]float64{
	"claude-haiku-4-5-20251001":  {1.00, 5.00},
	"claude-sonnet-4-6-20250514": {3.00, 15.00},
	"claude-opus-4-6-20250610":   {5.00, 25.00},
	"haiku":                      {1.00, 5.00},
	"sonnet":                     {3.00, 15.00},
	"opus":                       {5.00, 25.00},
}

// estimateContextTokens estimates the average input tokens per query for a sample.
func estimateContextTokens(sample Sample, fullContext bool) int {
	if fullContext {
		totalChars := 0
		for _, sess := range sample.Sessions {
			for _, turn := range sess.Turns {
				totalChars += len(turn.Text)
			}
		}
		return totalChars / 4 // rough chars-to-tokens
	}
	return 500 // BM25/hybrid retrieval: small context per query
}

// PrintCostEstimate calculates and prints estimated costs without making API calls.
func PrintCostEstimate(w io.Writer, samples []Sample, cfg QueryConfig, skipExtract bool, runs, samplePct int) {
	totalQ := CountTotalQuestions(samples)
	if samplePct > 0 && samplePct < 100 {
		totalQ = totalQ * samplePct / 100
	}

	// Estimate avg input tokens per query
	avgInputTokens := 0
	for _, s := range samples {
		avgInputTokens += estimateContextTokens(s, cfg.FullContext)
	}
	avgInputTokens /= len(samples)

	judgeInputTokens := 400 // question + gold + generated
	outputTokens := 150     // avg output per call

	var queryInputTotal, queryOutputTotal int
	if cfg.FullContext {
		// Batched: 1 call per sample with ALL questions, not 1 call per question
		for _, s := range samples {
			queryInputTotal += estimateContextTokens(s, true)
		}
		queryInputTotal *= runs
		queryOutputTotal = totalQ * 30 * runs // ~30 tokens per answer in batch
	} else {
		queryInputTotal = totalQ * avgInputTokens * runs
		queryOutputTotal = totalQ * outputTokens * runs
	}
	judgeInputTotal := totalQ * judgeInputTokens * runs
	judgeOutputTotal := totalQ * outputTokens * runs

	// Extraction estimate (one-time)
	extractInputTotal := 0
	extractOutputTotal := 0
	if !skipExtract {
		totalMsgs := 0
		for _, s := range samples {
			for _, sess := range s.Sessions {
				totalMsgs += len(sess.Turns)
			}
		}
		extractInputTotal = totalMsgs * 200 // avg tokens per message in extraction context
		extractOutputTotal = totalMsgs * 50
	}

	totalInput := queryInputTotal + judgeInputTotal + extractInputTotal
	totalOutput := queryOutputTotal + judgeOutputTotal + extractOutputTotal

	fmt.Fprintf(w, "\n=== Cost Estimate (DRY RUN) ===\n")
	fmt.Fprintf(w, "Questions:      %d (sample-pct=%d%%)\n", totalQ, samplePct)
	fmt.Fprintf(w, "Runs:           %d\n", runs)
	fmt.Fprintf(w, "Avg input/query: %dK tokens", avgInputTokens/1000)
	if cfg.FullContext {
		fmt.Fprintf(w, " (FULL CONTEXT — entire conversation per query!)")
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "\nToken breakdown:\n")
	fmt.Fprintf(w, "  Query input:   %dM tokens\n", queryInputTotal/1_000_000)
	fmt.Fprintf(w, "  Query output:  %.1fM tokens\n", float64(queryOutputTotal)/1_000_000)
	fmt.Fprintf(w, "  Judge input:   %.1fM tokens\n", float64(judgeInputTotal)/1_000_000)
	fmt.Fprintf(w, "  Judge output:  %.1fM tokens\n", float64(judgeOutputTotal)/1_000_000)
	if !skipExtract {
		fmt.Fprintf(w, "  Extract input: %.1fM tokens (one-time)\n", float64(extractInputTotal)/1_000_000)
		fmt.Fprintf(w, "  Extract output:%.1fM tokens (one-time)\n", float64(extractOutputTotal)/1_000_000)
	}
	fmt.Fprintf(w, "  TOTAL:         %dM input + %.1fM output\n", totalInput/1_000_000, float64(totalOutput)/1_000_000)

	fmt.Fprintf(w, "\nEstimated cost by model:\n")
	for name, pricing := range modelPricing {
		if len(name) > 10 {
			continue // skip full model IDs, show only short names
		}
		inputCost := float64(totalInput) / 1_000_000 * pricing[0]
		outputCost := float64(totalOutput) / 1_000_000 * pricing[1]
		fmt.Fprintf(w, "  %-8s $%.2f (input $%.2f + output $%.2f)\n", name, inputCost+outputCost, inputCost, outputCost)
	}
	fmt.Fprintf(w, "\n")
}

// SubsampleQA returns copies of samples with only a percentage of QA questions (deterministic seed).
func SubsampleQA(samples []Sample, pct int) []Sample {
	if pct >= 100 {
		return samples
	}
	rng := rand.New(rand.NewSource(42)) // deterministic for reproducibility
	out := make([]Sample, len(samples))
	for i, s := range samples {
		scored := s.ScoredQA()
		keep := len(scored) * pct / 100
		if keep < 1 {
			keep = 1
		}
		// Shuffle and take first N
		rng.Shuffle(len(scored), func(a, b int) { scored[a], scored[b] = scored[b], scored[a] })
		out[i] = Sample{
			ID:           s.ID,
			Sessions:     s.Sessions,
			QA:           scored[:keep],
			Observations: s.Observations,
		}
	}
	return out
}
