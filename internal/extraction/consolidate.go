package extraction

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// ConsolidateConfig controls the consolidation behavior.
type ConsolidateConfig struct {
	MaxRounds     int
	RuleBasedOnly bool
	AfterID       int64
	ThroughID     int64
}

// ConsolidateResult holds the outcome of a consolidation run.
type ConsolidateResult struct {
	Rounds               int
	TotalChecked         int
	TotalSuperseded      int
	BigramComparisons    int
	EmbeddingComparisons int
	EmbeddingDimensions  int
	Errors               int
	HighWatermark        int64
	PerRound             []RoundResult
}

// RoundResult holds stats for a single consolidation round.
type RoundResult struct {
	Checked    int
	Superseded int
}

// DistillResult holds the outcome of a cluster distillation run.
type DistillResult struct {
	ClustersProcessed int
	Distilled         int
	Superseded        int
	Skipped           int
	Errors            int
}

// RunClusterDistillation loads learning clusters and uses an LLM to distill each cluster
// into a single consolidated learning. Requires an LLM client (Haiku/Sonnet sufficient).
func RunClusterDistillation(store *storage.Store, client LLMClient, minClusterSize int) DistillResult {
	if minClusterSize <= 0 {
		minClusterSize = 3
	}

	// Get all projects that have clusters
	projects, err := store.ListProjects()
	if err != nil {
		log.Printf("warn: distillation list projects: %v", err)
		return DistillResult{}
	}

	var result DistillResult
	for _, p := range projects {
		clusters, err := store.GetLearningClusters(p.Project)
		if err != nil {
			continue
		}

		for _, cluster := range clusters {
			if cluster.LearningCount < minClusterSize {
				continue
			}
			result.ClustersProcessed++

			dr := distillCluster(store, client, cluster)
			result.Distilled += dr.Distilled
			result.Superseded += dr.Superseded
			result.Skipped += dr.Skipped
			result.Errors += dr.Errors
		}
	}

	return result
}

func distillCluster(store *storage.Store, client LLMClient, cluster models.LearningCluster) DistillResult {
	// Parse learning IDs from JSON array
	var ids []int64
	if err := json.Unmarshal([]byte(cluster.LearningIDs), &ids); err != nil {
		return DistillResult{Errors: 1}
	}

	// Load actual learnings (only active ones)
	var learnings []models.Learning
	for _, id := range ids {
		l, err := store.GetLearning(id)
		if err != nil || l == nil || l.SupersededBy != nil {
			continue
		}
		learnings = append(learnings, *l)
	}

	if len(learnings) < 2 {
		return DistillResult{Skipped: 1}
	}

	// Batch distillation: max 30 learnings per LLM call to avoid timeouts
	const batchSize = 30
	var result DistillResult
	for i := 0; i < len(learnings); i += batchSize {
		end := i + batchSize
		if end > len(learnings) {
			end = len(learnings)
		}
		batch := learnings[i:end]
		if len(batch) < 2 {
			result.Skipped++
			continue
		}

		dr := distillBatch(store, client, cluster, batch, ids)
		result.Distilled += dr.Distilled
		result.Superseded += dr.Superseded
		result.Skipped += dr.Skipped
		result.Errors += dr.Errors
	}

	return result
}

func distillBatch(store *storage.Store, client LLMClient, cluster models.LearningCluster, learnings []models.Learning, allClusterIDs []int64) DistillResult {
	// Build prompt
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Cluster: %q (Batch: %d Learnings)\n\n", cluster.Label, len(learnings)))
	for _, l := range learnings {
		sb.WriteString(fmt.Sprintf("[ID:%d] [%s] [%s] %s\n", l.ID, l.Category, l.CreatedAt.Format(time.DateOnly), l.Content))
	}

	response, err := client.CompleteJSON(DistillationSystemPrompt, sb.String(), DistillationSchema())
	if err != nil {
		log.Printf("  warn: distillation for cluster %q: %v", cluster.Label, err)
		return DistillResult{Errors: 1}
	}

	// Parse response
	var resp struct {
		Actions []struct {
			DistilledText string  `json:"distilled_text"`
			Category      string  `json:"category"`
			SupersedesIDs []int64 `json:"supersedes_ids"`
			Reason        string  `json:"reason"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(response)), &resp); err != nil {
		log.Printf("  warn: distillation parse for cluster %q: %v", cluster.Label, err)
		return DistillResult{Errors: 1}
	}

	if len(resp.Actions) == 0 {
		return DistillResult{Skipped: 1}
	}

	var result DistillResult
	for _, action := range resp.Actions {
		if action.DistilledText == "" || len(action.SupersedesIDs) < 2 {
			result.Skipped++
			continue
		}

		// Validate supersede IDs exist in this cluster
		validIDs := filterValidIDs(action.SupersedesIDs, allClusterIDs)
		if len(validIDs) < 2 {
			result.Skipped++
			continue
		}

		// Insert consolidated learning
		cat := action.Category
		if cat == "" {
			cat = learnings[0].Category
		}
		project := cluster.Project
		if project == "" && len(learnings) > 0 {
			project = learnings[0].Project
		}

		newLearning := &models.Learning{
			Content:  action.DistilledText,
			Category: cat,
			Project:  project,
			Source:   "consolidated",
		}
		newID, err := store.InsertLearning(newLearning)
		if err != nil {
			log.Printf("  warn: insert consolidated learning: %v", err)
			result.Errors++
			continue
		}

		// Supersede source learnings
		for _, oldID := range validIDs {
			reason := fmt.Sprintf("distilled into #%d: %s", newID, action.Reason)
			if err := store.SupersedeLearning(oldID, newID, reason); err != nil {
				log.Printf("  warn: supersede #%d: %v", oldID, err)
			} else {
				result.Superseded++
			}
		}

		result.Distilled++
		log.Printf("  distilled cluster %q batch: %d learnings → #%d", cluster.Label, len(validIDs), newID)
	}

	return result
}

func filterValidIDs(requested, allowed []int64) []int64 {
	set := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		set[id] = true
	}
	var valid []int64
	for _, id := range requested {
		if set[id] {
			valid = append(valid, id)
		}
	}
	return valid
}

// RunConsolidation runs iterative evolution until convergence or max rounds.
// Convergence: <5% new supersedes relative to checked in the last round.
func RunConsolidation(store *storage.Store, extractor *Extractor, onSupersede func(int64), cfg ConsolidateConfig) ConsolidateResult {
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 3
	}

	result := ConsolidateResult{HighWatermark: cfg.ThroughID}
	if result.HighWatermark == 0 {
		var err error
		result.HighWatermark, err = store.GetMaxLearningID()
		if err != nil {
			result.Errors++
			log.Printf("warn: consolidation high-watermark: %v", err)
			return result
		}
	}

	for round := 1; round <= cfg.MaxRounds; round++ {
		log.Printf("━━━ Consolidation Round %d/%d ━━━", round, cfg.MaxRounds)

		var checked, superseded int

		if extractor != nil && !cfg.RuleBasedOnly {
			checked, superseded = extractor.RunEvolution(store, onSupersede)
		} else {
			var bigramComparisons, embeddingComparisons, embeddingDimensions, errors int
			checked, superseded, bigramComparisons, embeddingComparisons, embeddingDimensions, errors = runRuleBasedEvolution(store, onSupersede, cfg.AfterID, result.HighWatermark)
			result.BigramComparisons += bigramComparisons
			result.EmbeddingComparisons += embeddingComparisons
			result.EmbeddingDimensions += embeddingDimensions
			result.Errors += errors
		}

		roundResult := RoundResult{Checked: checked, Superseded: superseded}
		result.PerRound = append(result.PerRound, roundResult)
		result.TotalChecked += checked
		result.TotalSuperseded += superseded
		result.Rounds = round

		log.Printf("  Round %d: %d checked, %d superseded", round, checked, superseded)

		if checked == 0 || float64(superseded)/float64(checked) < 0.05 {
			log.Printf("  Converged after %d rounds (%.1f%% supersede rate)", round,
				100*float64(superseded)/float64(max(checked, 1)))
			break
		}
	}

	log.Printf("━━━ Consolidation complete: %d rounds, %d checked, %d superseded, %d bigram candidates, %d embedding comparisons (%d dimensions) ━━━",
		result.Rounds, result.TotalChecked, result.TotalSuperseded, result.BigramComparisons, result.EmbeddingComparisons, result.EmbeddingDimensions)
	return result
}

// runRuleBasedEvolution applies BigramJaccard + embedding dedup without LLM.
func runRuleBasedEvolution(store *storage.Store, onSupersede func(int64), afterID, throughID int64) (int, int, int, int, int, int) {
	learnings, err := store.GetConsolidationLearnings(afterID, throughID)
	if err != nil {
		log.Printf("warn: get incremental consolidation scopes: %v", err)
		return 0, 0, 0, 0, 0, 1
	}

	type groupKey struct{ project, category string }
	groups := make(map[groupKey][]models.Learning)
	for _, learning := range learnings {
		project := learning.CanonicalProject
		if project == "" {
			project = learning.Project
		}
		key := groupKey{project: project, category: learning.Category}
		groups[key] = append(groups[key], learning)
	}

	totalChecked, totalSuperseded := 0, 0
	totalBigramComparisons, totalEmbeddingComparisons, totalEmbeddingDimensions, totalErrors := 0, 0, 0, 0
	for _, group := range groups {
		dirty := 0
		for _, learning := range group {
			if afterID == 0 || learning.ID > afterID {
				dirty++
			}
		}
		totalChecked += dirty
		if len(group) < 2 && dirty == 0 {
			continue
		}

		// Pass 1: remove new junk and compare only pairs involving new rows.
		cleaned := make([]models.Learning, 0, len(group))
		for _, l := range group {
			if IsSubstanzlos(l.Content) {
				if afterID > 0 && l.ID <= afterID {
					continue
				}
				if err := store.SupersedeLearning(l.ID, 0, "rule-based: substanzlos"); err == nil {
					totalSuperseded++
					if onSupersede != nil {
						onSupersede(l.ID)
					}
				} else {
					totalErrors++
				}
				continue
			}
			cleaned = append(cleaned, l)
		}
		lexicalDupes, comparisons := findBigramDuplicates(cleaned, 0.85, afterID)
		totalBigramComparisons += comparisons
		for loserID, winnerID := range lexicalDupes {
			if err := store.SupersedeLearning(loserID, winnerID, "rule-based: near-duplicate"); err == nil {
				totalSuperseded++
				if onSupersede != nil {
					onSupersede(loserID)
				}
			} else {
				totalErrors++
			}
		}
		if len(lexicalDupes) > 0 {
			remaining := cleaned[:0]
			for _, learning := range cleaned {
				if _, loser := lexicalDupes[learning.ID]; !loser {
					remaining = append(remaining, learning)
				}
			}
			cleaned = remaining
		}

		// Pass 2: Embedding cosine similarity
		if len(cleaned) >= 2 {
			ids := make([]int64, len(cleaned))
			for i, l := range cleaned {
				ids[i] = l.ID
			}
			vectors, err := LoadVectorsForLearnings(store, ids)
			if err != nil {
				log.Printf("warn: load consolidation vectors: %v", err)
				totalErrors++
				continue
			}
			if len(vectors) >= 2 {
				embDupes, stats := findEmbeddingDuplicatesSince(cleaned, vectors, 0.92, afterID)
				totalEmbeddingComparisons += stats.Comparisons
				totalEmbeddingDimensions += stats.Dimensions
				for loserID, winnerID := range embDupes {
					if err := store.SupersedeLearning(loserID, winnerID, "rule-based: embedding near-duplicate"); err == nil {
						totalSuperseded++
						if onSupersede != nil {
							onSupersede(loserID)
						}
					} else {
						totalErrors++
					}
				}
			}
		}
	}

	return totalChecked, totalSuperseded, totalBigramComparisons, totalEmbeddingComparisons, totalEmbeddingDimensions, totalErrors
}
