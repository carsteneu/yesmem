package extraction

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// evolutionAction represents what to do with a new learning vs existing ones.
type evolutionAction struct {
	NewLearning   string  `json:"new_learning"`
	SupersedesIDs []int64 `json:"supersedes_ids"`
	Reason        string  `json:"reason"`
	Type          string  `json:"type"` // supersede, independent, update, confirmation
}

type evolutionResponse struct {
	Actions []evolutionAction `json:"actions"`
}

// resolveConflicts checks a new learning against existing active learnings.
// Uses BM25 text search to find the Top-10 semantically similar learnings
// instead of comparing against the 30 most recent. Falls back to recency
// if BM25 yields insufficient results.
func (e *Extractor) resolveConflicts(newID int64, newLearning *models.Learning) {
	others := e.findConflictCandidates(newID, newLearning)
	if len(others) == 0 {
		return
	}

	// Build comparison prompt
	var existingList strings.Builder
	for _, l := range others {
		existingList.WriteString(fmt.Sprintf("#%d: %s\n", l.ID, l.Content))
	}

	prompt := fmt.Sprintf(`Bestehende Learnings (Kategorie: %s):
%s

Neues Learning (#%d):
%s

Widerspricht das neue Learning einem bestehenden? Ersetzt es eines?
Antwort als JSON.`, newLearning.Category, existingList.String(), newID, newLearning.Content)

	response, err := e.client.CompleteJSON(EvolutionSystemPrompt, prompt, EvolutionSchema())
	if err != nil {
		log.Printf("warn: evolution check for #%d: %v", newID, err)
		if strings.Contains(err.Error(), "rate_limit") {
			log.Printf("  Rate limited on evolution check — waiting 60s")
			time.Sleep(60 * time.Second)
		}
		return
	}

	var evoResp evolutionResponse
	if err := json.Unmarshal([]byte(extractJSON(response)), &evoResp); err != nil {
		log.Printf("warn: parse evolution response: %v", err)
		return
	}

	// Apply actions
	for _, action := range evoResp.Actions {
		switch action.Type {
		case "supersede":
			for _, oldID := range action.SupersedesIDs {
				// Trust check: don't supersede high-trust learnings
				oldLearning, err := e.store.GetLearning(oldID)
				if err != nil {
					log.Printf("warn: get learning #%d for trust check: %v", oldID, err)
					continue
				}
				trust := storage.TrustScore(oldLearning)
				level := storage.ClassifyTrust(trust)

				switch level {
				case storage.TrustHigh:
					e.store.SetSupersedeStatus(oldID, "pending_confirmation")
					log.Printf("Learning #%d: supersede blocked (trust %.1f, high) — set pending_confirmation", oldID, trust)
				default:
					if err := e.store.SupersedeLearning(oldID, newID, action.Reason); err != nil {
						log.Printf("warn: supersede #%d by #%d: %v", oldID, newID, err)
					} else {
						log.Printf("Learning #%d supersedes #%d: %s (trust %.1f)", newID, oldID, action.Reason, trust)
					}
				}
			}

		case "update":
			for _, targetID := range action.SupersedesIDs {
				if err := e.store.UpdateLearningContent(targetID, action.NewLearning); err != nil {
					log.Printf("warn: update learning #%d: %v", targetID, err)
				} else {
					e.store.IncrementMatchCounts([]int64{targetID})
					log.Printf("Learning #%d updated in-place: %s", targetID, action.Reason)
				}
			}

		case "confirmation":
			if len(action.SupersedesIDs) > 0 {
				e.store.IncrementUseCounts(action.SupersedesIDs)
				log.Printf("Learnings %v confirmed: %s", action.SupersedesIDs, action.Reason)
			}

		case "independent":
			// no-op

		default:
			log.Printf("warn: unknown evolution action type: %s", action.Type)
		}
	}
}

// findConflictCandidates returns learnings to compare against for conflict resolution.
// Primary: BM25 text search on the new learning's content (finds semantically related learnings
// across the entire history, not just recent ones). Fallback: recency-based if BM25 yields < 3 results.
func (e *Extractor) findConflictCandidates(newID int64, newLearning *models.Learning) []models.Learning {
	const bm25Limit = 20 // fetch more, post-filter by category
	const maxCandidates = 10
	const minBM25Results = 3 // below this, augment with recency fallback

	// Try BM25 search using the new learning's content as query
	bm25Results, err := e.store.SearchLearningsBM25(newLearning.Content, newLearning.Project, bm25Limit)
	if err != nil {
		log.Printf("warn: BM25 search for evolution candidates: %v — falling back to recency", err)
		return e.findCandidatesByRecency(newID, newLearning, maxCandidates)
	}

	// Post-filter: same category, exclude self, resolve to full Learning objects
	seen := make(map[int64]bool)
	seen[newID] = true
	var others []models.Learning
	for _, r := range bm25Results {
		id, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		l, err := e.store.GetLearning(id)
		if err != nil || l == nil {
			continue
		}
		// Must match category — BM25 search doesn't filter by category
		if l.Category != newLearning.Category {
			continue
		}
		// Skip already-superseded learnings
		if l.SupersededBy != nil {
			continue
		}
		others = append(others, *l)
		if len(others) >= maxCandidates {
			break
		}
	}

	// If BM25 yielded too few results, augment with recency fallback
	if len(others) < minBM25Results {
		recency := e.findCandidatesByRecency(newID, newLearning, maxCandidates)
		for _, l := range recency {
			if seen[l.ID] {
				continue
			}
			seen[l.ID] = true
			others = append(others, l)
			if len(others) >= maxCandidates {
				break
			}
		}
	}

	if len(others) > 0 {
		log.Printf("evolution: #%d comparing against %d candidates (BM25+recency)", newID, len(others))
	}
	return others
}

// findCandidatesByRecency is the original recency-based candidate selection.
// Returns the most recent active learnings in the same category/project, excluding newID.
func (e *Extractor) findCandidatesByRecency(newID int64, newLearning *models.Learning, limit int) []models.Learning {
	existing, err := e.store.GetActiveLearnings(newLearning.Category, newLearning.Project, "", "")
	if err != nil {
		return nil
	}
	var others []models.Learning
	for _, l := range existing {
		if l.ID != newID {
			others = append(others, l)
		}
	}
	if len(others) > limit {
		others = others[len(others)-limit:]
	}
	return others
}
