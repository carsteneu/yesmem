package extraction

import (
	"log"
	"strconv"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// PreAdmissionAction describes what to do with a learning before insert.
type PreAdmissionAction int

const (
	PreAdmissionInsert PreAdmissionAction = iota
	PreAdmissionSkip
	PreAdmissionUpdate
)

// PreAdmissionResult holds the decision for a single learning.
type PreAdmissionResult struct {
	Action     PreAdmissionAction
	ExistingID int64
	Reason     string
}

// CheckPreAdmission checks a learning against existing active learnings.
func CheckPreAdmission(store *storage.Store, learning *models.Learning) PreAdmissionResult {
	// Phase 0: Exact content hash check
	if existing, err := store.GetLearningByContentHash(textutil.ContentHash(learning.Content)); err == nil && existing != nil {
		return PreAdmissionResult{Action: PreAdmissionSkip, ExistingID: existing.ID, Reason: "exact duplicate"}
	}

	// Phase 1: BM25 search for candidates
	candidates, err := store.SearchLearningsBM25(learning.Content, learning.Project, 20)
	if err != nil {
		log.Printf("pre-admission: BM25 search failed: %v", err)
	}

	// Phase 2: Check BM25 candidates via BigramJaccard + TokenSimilarity
	if result := checkCandidates(store, learning, candidates); result != nil {
		return *result
	}

	// Phase 3: Fallback — scan category directly (catches cases BM25 misses
	// due to stemming/conjugation differences). Limited to 100 for performance.
	catLearnings, err := store.GetActiveLearnings(learning.Category, learning.Project, "", "")
	if err == nil && len(catLearnings) > 0 {
		limit := len(catLearnings)
		if limit > 100 {
			limit = 100
		}
		var fallbackCandidates []storage.LearningSearchResult
		for _, l := range catLearnings[:limit] {
			fallbackCandidates = append(fallbackCandidates, storage.LearningSearchResult{
				ID:      strconv.FormatInt(l.ID, 10),
				Content: l.Content,
			})
		}
		if result := checkCandidates(store, learning, fallbackCandidates); result != nil {
			return *result
		}
	}

	return PreAdmissionResult{Action: PreAdmissionInsert}
}

// checkCandidates evaluates a list of candidates against a new learning.
// Returns conflict flag when similar content exists from a different agent_role.
func checkCandidates(store *storage.Store, learning *models.Learning, candidates []storage.LearningSearchResult) *PreAdmissionResult {
	newTokens := textutil.Tokenize(learning.Content)

	for _, candidate := range candidates {
		id, err := strconv.ParseInt(candidate.ID, 10, 64)
		if err != nil {
			continue
		}
		existing, err := store.GetLearning(id)
		if err != nil || existing == nil || existing.SupersededBy != nil {
			continue
		}
		if existing.Category != learning.Category {
			continue
		}

		jaccard := BigramJaccard(learning.Content, existing.Content)
		if jaccard > 0.85 {
			// High similarity, same topic — check if different agent wrote it
			if learning.AgentRole != "" && existing.AgentRole != "" && learning.AgentRole != existing.AgentRole {
				log.Printf("pre-admission: conflict detected — new (%s) vs existing #%d (%s), jaccard=%.2f",
					learning.AgentRole, existing.ID, existing.AgentRole, jaccard)
				// Still skip the duplicate, but log the cross-agent conflict
			}
			return &PreAdmissionResult{Action: PreAdmissionSkip, ExistingID: existing.ID, Reason: "near-duplicate (jaccard)"}
		}

		existTokens := textutil.Tokenize(existing.Content)
		sim := textutil.TokenSimilarity(newTokens, existTokens)
		if sim >= 0.5 {
			// Medium similarity — same topic, different wording
			if learning.AgentRole != "" && existing.AgentRole != "" && learning.AgentRole != existing.AgentRole {
				// Different agent, same topic, different content = potential conflict
				// Allow both to exist — don't skip, don't update
				log.Printf("pre-admission: cross-agent divergence — new (%s) vs #%d (%s), sim=%.2f — keeping both",
					learning.AgentRole, existing.ID, existing.AgentRole, sim)
				return nil // insert as new, don't deduplicate
			}
			newLen := len([]rune(learning.Content))
			existLen := len([]rune(existing.Content))
			if newLen > existLen*3/2 {
				return &PreAdmissionResult{Action: PreAdmissionUpdate, ExistingID: existing.ID, Reason: "enrichment update"}
			}
			return &PreAdmissionResult{Action: PreAdmissionSkip, ExistingID: existing.ID, Reason: "already known (token similarity)"}
		}
	}

	return nil
}
