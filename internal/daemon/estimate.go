package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Exported pricing constants for reuse in setup wizard.
const (
	AvgChunksPerSession = 4
	TokensPerChunk      = 8200  // ~25K chars / 3 chars per token
	OutputPerChunk      = 800   // typical extraction response

	// Per-million-token pricing (USD)
	HaikuInputPerM  = 1.0
	HaikuOutputPerM = 5.0
	SonnetInputPerM  = 3.0
	SonnetOutputPerM = 15.0
	OpusInputPerM    = 5.0
	OpusOutputPerM   = 25.0
)

// CostEstimate holds the estimated cost for extraction.
type CostEstimate struct {
	Sessions        int
	AvgChunks       int
	TotalChunks     int
	EstTokensInput  int64
	EstTokensOutput int64
	EstCostUSD      float64
	DataSizeMB      float64
}

// EstimateExtractionCost calculates expected API costs for unextracted sessions.
// model should be "haiku", "sonnet", or "opus".
func EstimateExtractionCost(store *storage.Store, sessions []models.Session, model string) CostEstimate {
	if len(sessions) == 0 {
		return CostEstimate{}
	}

	// Estimate based on average session size
	// Average session: ~25KB text content after filtering → ~1 chunk
	// But some sessions are much larger → avg ~4 chunks (based on real data)
	totalChunks := len(sessions) * AvgChunksPerSession
	inputTokens := int64(totalChunks) * TokensPerChunk
	outputTokens := int64(totalChunks) * OutputPerChunk

	inputPerM, outputPerM := modelPricing(model)
	inputCost := float64(inputTokens) / 1_000_000 * inputPerM
	outputCost := float64(outputTokens) / 1_000_000 * outputPerM

	// Add narrative + evolution costs (~$0.01 per session)
	narrativeCost := float64(len(sessions)) * 0.01

	return CostEstimate{
		Sessions:        len(sessions),
		AvgChunks:       AvgChunksPerSession,
		TotalChunks:     totalChunks,
		EstTokensInput:  inputTokens,
		EstTokensOutput: outputTokens,
		EstCostUSD:      inputCost + outputCost + narrativeCost,
	}
}

// modelPricing returns input/output per-million-token pricing for the given model.
func modelPricing(model string) (inputPerM, outputPerM float64) {
	switch model {
	case "opus":
		return OpusInputPerM, OpusOutputPerM
	case "sonnet":
		return SonnetInputPerM, SonnetOutputPerM
	default:
		return HaikuInputPerM, HaikuOutputPerM
	}
}

// LogExtractionEstimate prints cost estimate to the daemon log.
func LogExtractionEstimate(est CostEstimate, client extraction.LLMClient) {
	if est.Sessions == 0 {
		return
	}

	backendName := "api"
	if client != nil {
		backendName = client.Name()
	}

	log.Println("━━━ LLM Extraction Estimate ━━━")
	log.Printf("  Backend:          %s", backendName)
	log.Printf("  Unextracted:      %d sessions (~%d chunks each)", est.Sessions, est.AvgChunks)
	log.Printf("  Est. API calls:   ~%d", est.TotalChunks)
	log.Printf("  Est. input:       ~%.1f M tokens", float64(est.EstTokensInput)/1_000_000)
	log.Printf("  Est. output:      ~%.1f M tokens", float64(est.EstTokensOutput)/1_000_000)

	if backendName == "cli" {
		log.Printf("  Note:             Pro/Max plans use subscription quota")
	} else {
		log.Printf("  Est. cost:        ~$%.2f", est.EstCostUSD)
	}

	if est.Sessions > 100 {
		log.Printf("  ⚠ Large backfill! Consider setting extraction.max_age_days in config.yaml")
	}

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// ModelMonthlyCost holds estimated monthly cost for a single model.
type ModelMonthlyCost struct {
	Model   string
	CostUSD float64
}

// EstimateMonthlyFromDir scans a Claude Code projects directory for JSONL files
// modified in the last 30 days and estimates monthly extraction cost per model.
// This works without a DB — intended for the setup wizard.
func EstimateMonthlyFromDir(projectsDir string) ([]ModelMonthlyCost, int, error) {
	cutoff := time.Now().AddDate(0, 0, -30)
	var totalBytes int64
	var sessionCount int

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, 0, fmt.Errorf("read projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projPath := filepath.Join(projectsDir, entry.Name())
		files, err := os.ReadDir(projPath)
		if err != nil {
			continue
		}
		for _, f := range files {
			if filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(cutoff) {
				totalBytes += info.Size()
				sessionCount++
			}
		}
	}

	if sessionCount == 0 {
		return nil, 0, nil
	}

	// ~3 chars per token, each session gets extracted in chunks
	totalTokensInput := totalBytes / 3
	totalTokensOutput := int64(sessionCount) * int64(AvgChunksPerSession) * OutputPerChunk

	costs := []ModelMonthlyCost{
		{
			Model:   "haiku",
			CostUSD: float64(totalTokensInput)/1_000_000*HaikuInputPerM + float64(totalTokensOutput)/1_000_000*HaikuOutputPerM,
		},
		{
			Model:   "sonnet",
			CostUSD: float64(totalTokensInput)/1_000_000*SonnetInputPerM + float64(totalTokensOutput)/1_000_000*SonnetOutputPerM,
		},
		{
			Model:   "opus",
			CostUSD: float64(totalTokensInput)/1_000_000*OpusInputPerM + float64(totalTokensOutput)/1_000_000*OpusOutputPerM,
		},
	}

	return costs, sessionCount, nil
}

// FilterByMaxAge filters sessions to only include those within maxAgeDays.
// Returns all sessions if maxAgeDays <= 0.
func FilterByMaxAge(sessions []models.Session, maxAgeDays int) []models.Session {
	if maxAgeDays <= 0 {
		return sessions
	}

	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	var filtered []models.Session
	for _, s := range sessions {
		if s.StartedAt.After(cutoff) {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) < len(sessions) {
		log.Printf("  max_age_days=%d: %d/%d sessions in scope (cutoff: %s)",
			maxAgeDays, len(filtered), len(sessions),
			cutoff.Format("2006-01-02"))
	}

	return filtered
}

// FormatCostEstimate returns a human-readable string for setup display.
func FormatCostEstimate(est CostEstimate, backendName string) string {
	if est.Sessions == 0 {
		return "No sessions need extraction."
	}

	s := fmt.Sprintf("  Sessions:     %d (avg ~%d chunks each)\n", est.Sessions, est.AvgChunks)
	s += fmt.Sprintf("  Est. calls:   ~%d\n", est.TotalChunks)
	s += fmt.Sprintf("  Est. tokens:  ~%.1fM input + ~%.1fM output\n",
		float64(est.EstTokensInput)/1_000_000,
		float64(est.EstTokensOutput)/1_000_000)

	if backendName == "cli" {
		s += "  Cost:         Uses Pro/Max subscription quota\n"
	} else {
		s += fmt.Sprintf("  Est. cost:    ~$%.2f (one-time)\n", est.EstCostUSD)
		s += "  Per session:  ~$0.30-0.50 (ongoing)\n"
	}

	return s
}
