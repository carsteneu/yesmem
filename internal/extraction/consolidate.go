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
}

// ConsolidateResult holds the outcome of a consolidation run.
type ConsolidateResult struct {
	Rounds          int
	TotalChecked    int
	TotalSuperseded int
	PerRound        []RoundResult
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

	result := ConsolidateResult{}

	for round := 1; round <= cfg.MaxRounds; round++ {
		log.Printf("━━━ Consolidation Round %d/%d ━━━", round, cfg.MaxRounds)

		var checked, superseded int

		if extractor != nil && !cfg.RuleBasedOnly {
			checked, superseded = extractor.RunEvolution(store, onSupersede)
		} else {
			checked, superseded = runRuleBasedEvolution(store, onSupersede)
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

	log.Printf("━━━ Consolidation complete: %d rounds, %d checked, %d superseded ━━━",
		result.Rounds, result.TotalChecked, result.TotalSuperseded)
	return result
}

// runRuleBasedEvolution applies BigramJaccard + embedding dedup without LLM.
func runRuleBasedEvolution(store *storage.Store, onSupersede func(int64)) (int, int) {
	categories, err := store.GetActiveCategories()
	if err != nil {
		return 0, 0
	}

	totalChecked, totalSuperseded := 0, 0

	for _, cat := range categories {
		if cat == "narrative" {
			continue
		}
		learnings, err := store.GetActiveLearnings(cat, "", "", "")
		if err != nil || len(learnings) < 2 {
			continue
		}

		totalChecked += len(learnings)

		// Pass 1: IsSubstanzlos + BigramJaccard
		var cleaned []models.Learning
		for _, l := range learnings {
			if IsSubstanzlos(l.Content) {
				if err := store.SupersedeLearning(l.ID, 0, "rule-based: substanzlos"); err == nil {
					totalSuperseded++
					if onSupersede != nil {
						onSupersede(l.ID)
					}
				}
				continue
			}
			isDupe := false
			for _, kept := range cleaned {
				if BigramJaccard(l.Content, kept.Content) > 0.85 {
					loserID, winnerID := l.ID, kept.ID
					if l.ID > kept.ID {
						loserID, winnerID = kept.ID, l.ID
						for ci := range cleaned {
							if cleaned[ci].ID == kept.ID {
								cleaned[ci] = l
								break
							}
						}
					}
					if err := store.SupersedeLearning(loserID, winnerID, "rule-based: near-duplicate"); err == nil {
						totalSuperseded++
						if onSupersede != nil {
							onSupersede(loserID)
						}
					}
					isDupe = true
					break
				}
			}
			if !isDupe {
				cleaned = append(cleaned, l)
			}
		}

		// Pass 2: Embedding cosine similarity
		if len(cleaned) >= 2 {
			ids := make([]int64, len(cleaned))
			for i, l := range cleaned {
				ids[i] = l.ID
			}
			vectors := LoadVectorsForLearnings(store, ids)
			if len(vectors) >= 2 {
				embDupes := FindEmbeddingDuplicates(cleaned, vectors, 0.92)
				for loserID, winnerID := range embDupes {
					if err := store.SupersedeLearning(loserID, winnerID, "rule-based: embedding near-duplicate"); err == nil {
						totalSuperseded++
						if onSupersede != nil {
							onSupersede(loserID)
						}
					}
				}
			}
		}
	}

	return totalChecked, totalSuperseded
}
