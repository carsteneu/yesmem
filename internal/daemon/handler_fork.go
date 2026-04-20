package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

// handleForkSetSessionFlavor sets session_flavor on learnings without one.
// Preserves earlier phase flavors when extraction runs multiple times on long sessions.
// Called by the proxy's extract_and_evaluate fork type via queryDaemon.
func (h *Handler) handleForkSetSessionFlavor(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	flavor, _ := params["flavor"].(string)
	if sessionID == "" || flavor == "" {
		return errorResponse("session_id and flavor required")
	}
	n, err := h.store.UpdateSessionFlavorOnlyEmpty(sessionID, flavor)
	if err != nil {
		return errorResponse(fmt.Sprintf("update session flavor: %v", err))
	}
	log.Printf("fork set session flavor: %q on %d learnings (session=%s)", flavor, n, sessionID)
	return jsonResponse(map[string]any{"ok": true, "updated": n, "flavor": flavor})
}

// handleForkExtractLearnings saves learnings extracted by a forked agent.
// Called by the proxy's extract_and_evaluate fork type via queryDaemon.
func (h *Handler) handleForkExtractLearnings(params map[string]any) Response {
	learningsJSON, _ := params["learnings"].(string)
	if learningsJSON == "" {
		return errorResponse("learnings (JSON string) required")
	}
	sessionID, _ := params["session_id"].(string)
	project, _ := params["project"].(string)
	sourceMsgFrom := intOr(params, "source_msg_from", -1)
	sourceMsgTo := intOr(params, "source_msg_to", -1)

	var learnings []struct {
		Content            string   `json:"content"`
		Category           string   `json:"category"`
		Entities           []string `json:"entities"`
		Status             string   `json:"status"`
		Context            string   `json:"context"`
		Actions            []string `json:"actions"`
		Keywords           []string `json:"keywords"`
		AnticipatedQueries []string `json:"anticipated_queries"`
		Importance         int      `json:"importance"`
		EmotionalIntensity float64  `json:"emotional_intensity"`
	}
	if err := json.Unmarshal([]byte(learningsJSON), &learnings); err != nil {
		return errorResponse(fmt.Sprintf("unmarshal learnings: %v", err))
	}

	saved := 0
	skipped := 0
	for _, l := range learnings {
		if l.Content == "" || l.Category == "" {
			continue
		}
		// Skip invalidated learnings — the LLM says this is no longer true
		if l.Status == "invalidated" {
			skipped++
			continue
		}
		if !models.IsValidCategory(l.Category) {
			log.Printf("fork extract: invalid category %q, skipping", l.Category)
			continue
		}
		learning := &models.Learning{
			Content:            l.Content,
			Category:           l.Category,
			Source:             "fork",
			SessionID:          sessionID,
			Project:            project,
			Domain:             "code",
			Confidence:         1.0,
			Importance:         l.Importance,
			EmotionalIntensity: l.EmotionalIntensity,
			CreatedAt:          time.Now(),
			Entities:           l.Entities,
			Context:            l.Context,
			Actions:            l.Actions,
			Keywords:           l.Keywords,
			AnticipatedQueries: l.AnticipatedQueries,
			SourceMsgFrom:      sourceMsgFrom,
			SourceMsgTo:        sourceMsgTo,
		}

		// Pre-admission dedup: skip near-duplicates, update enrichments
		pa := extraction.CheckPreAdmission(h.store, learning)
		switch pa.Action {
		case extraction.PreAdmissionSkip:
			skipped++
			h.store.IncrementMatchCounts([]int64{pa.ExistingID})
			continue
		case extraction.PreAdmissionUpdate:
			if err := h.store.UpdateLearningContent(pa.ExistingID, l.Content); err == nil {
				h.store.IncrementMatchCounts([]int64{pa.ExistingID})
				saved++
			}
			continue
		}

		id, err := h.store.InsertLearning(learning)
		if err != nil {
			log.Printf("fork extract: save error: %v", err)
			continue
		}

		// Auto-embed if available
		h.EmbedLearning(id, l.Content, l.Category, project)

		// Auto-generate relates_to edges for learnings sharing entities
		if len(l.Entities) > 0 {
			if matches, err := h.store.GetLearningsWithEntityOverlap(l.Entities, id, 10); err == nil {
				for _, matchID := range matches {
					h.store.InsertTypedAssociation(id, matchID, "relates_to")
				}
			}
		}

		saved++
	}

	// Record fork coverage for post-session dedup
	if sourceMsgFrom >= 0 && sourceMsgTo >= 0 && sessionID != "" {
		forkIdx := 0
		if row := h.store.DB().QueryRow(`SELECT COALESCE(MAX(fork_index), 0) + 1 FROM fork_coverage WHERE session_id = ?`, sessionID); row != nil {
			row.Scan(&forkIdx)
		}
		h.store.InsertForkCoverage(sessionID, sourceMsgFrom, sourceMsgTo, forkIdx)
	}

	return jsonResponse(map[string]any{"saved": saved, "skipped": skipped})
}

// handleForkEvaluateLearning applies a verdict action to an existing learning.
// Called by the proxy's extract_and_evaluate fork type via queryDaemon.
func (h *Handler) handleForkEvaluateLearning(params map[string]any) Response {
	learningID := int64(intOr(params, "learning_id", 0))
	if learningID == 0 {
		return errorResponse("learning_id required")
	}
	action, _ := params["action"].(string)
	reason, _ := params["reason"].(string)

	switch action {
	case "boost":
		if err := h.store.IncrementUseCounts([]int64{learningID}); err != nil {
			log.Printf("fork evaluate: boost learning %d: %v", learningID, err)
		}
	case "save":
		if err := h.store.IncrementSaveCounts([]int64{learningID}); err != nil {
			log.Printf("fork evaluate: save learning %d: %v", learningID, err)
		}
	case "noise":
		if err := h.store.IncrementNoiseCounts([]int64{learningID}); err != nil {
			log.Printf("fork evaluate: noise learning %d: %v", learningID, err)
		}
	case "flag":
		if err := h.store.IncrementFailCounts([]int64{learningID}); err != nil {
			log.Printf("fork evaluate: flag learning %d: %v", learningID, err)
		}
	case "supersede":
		if err := h.store.SupersedeLearning(learningID, 0, reason); err != nil {
			log.Printf("fork evaluate: supersede learning %d: %v", learningID, err)
		}
	case "skip":
		// No action needed
	default:
		return errorResponse(fmt.Sprintf("unknown action: %q", action))
	}

	// Store impact score if provided
	if impactScore, ok := params["impact_score"].(float64); ok {
		h.store.UpdateImpactScore(learningID, impactScore)
	}

	return jsonResponse(map[string]any{"ok": true, "action": action, "learning_id": learningID})
}

// handleForkUpdateImpact updates the impact score for a learning.
func (h *Handler) handleForkUpdateImpact(params map[string]any) Response {
	learningID := int64(intOr(params, "learning_id", 0))
	if learningID == 0 {
		return errorResponse("learning_id required")
	}
	score, _ := params["impact_score"].(float64)
	if err := h.store.UpdateImpactScore(learningID, score); err != nil {
		return errorResponse(fmt.Sprintf("update impact: %v", err))
	}
	return jsonResponse(map[string]any{"ok": true, "learning_id": learningID, "impact_score": score})
}

// handleGetForkLearnings returns all fork-extracted learnings for a session.
func (h *Handler) handleGetForkLearnings(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id required")
	}
	learnings, err := h.store.GetForkLearnings(sessionID)
	if err != nil {
		return errorResponse(fmt.Sprintf("get fork learnings: %v", err))
	}
	items := make([]map[string]any, len(learnings))
	for i, l := range learnings {
		items[i] = map[string]any{
			"id":       l.ID,
			"content":  l.Content,
			"category": l.Category,
		}
	}
	return jsonResponse(map[string]any{"learnings": items})
}

// handleForkResolveContradiction increments fail_count on both sides of a contradiction.
func (h *Handler) handleForkResolveContradiction(params map[string]any) Response {
	learningA := int64(intOr(params, "learning_a", 0))
	learningB := int64(intOr(params, "learning_b", 0))
	if learningA == 0 || learningB == 0 {
		return errorResponse("learning_a and learning_b required")
	}
	description, _ := params["description"].(string)
	if err := h.store.IncrementFailCounts([]int64{learningA, learningB}); err != nil {
		return errorResponse(fmt.Sprintf("increment fail counts: %v", err))
	}
	h.store.InsertTypedAssociation(learningA, learningB, "contradicts")
	log.Printf("fork contradiction: #%d vs #%d — %s", learningA, learningB, description)
	return jsonResponse(map[string]any{"ok": true, "learning_a": learningA, "learning_b": learningB})
}
