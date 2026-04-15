package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

func (h *Handler) handleRemember(params map[string]any) Response {
	text, _ := params["text"].(string)
	category := stringOr(params, "category", "explicit_teaching")
	project, _ := params["project"].(string)
	source := stringOr(params, "source", "user_stated")
	supersedesID, _ := params["supersedes"].(float64) // optional: ID of learning this replaces

	if !models.IsValidCategory(category) {
		category = "explicit_teaching"
	}

	l := &models.Learning{
		Category: category, Content: text, Project: project,
		Confidence: 1.0, CreatedAt: timeNow(), ModelUsed: rememberModel(params),
		Source: source,
	}

	// Session ID: try explicit param, then active session from idle_tick, then latest from DB
	if sid, ok := params["session_id"].(string); ok && sid != "" {
		l.SessionID = sid
	} else {
		h.activeSessionMu.Lock()
		sid = h.activeSessionID
		h.activeSessionMu.Unlock()
		if sid != "" {
			l.SessionID = sid
		} else if latest, err := h.store.GetLatestSessionID(project); err == nil && latest != "" {
			l.SessionID = latest
		}
	}

	// Parse optional V2 params
	if entitiesRaw, ok := params["entities"].([]any); ok {
		for _, e := range entitiesRaw {
			if s, ok := e.(string); ok {
				l.Entities = append(l.Entities, s)
			}
		}
	}
	if actionsRaw, ok := params["actions"].([]any); ok {
		for _, a := range actionsRaw {
			if s, ok := a.(string); ok {
				l.Actions = append(l.Actions, s)
			}
		}
	}
	if trigger, ok := params["trigger"].(string); ok {
		l.TriggerRule = trigger
		// Parse deadline trigger into ExpiresAt for automatic expiration
		if expires := extraction.ParseDeadlineExpiry(trigger); expires != nil {
			l.ExpiresAt = expires
		}
	}
	if ctx, ok := params["context"].(string); ok {
		l.Context = ctx
	}
	if aqRaw, ok := params["anticipated_queries"].([]any); ok {
		for _, q := range aqRaw {
			if s, ok := q.(string); ok {
				l.AnticipatedQueries = append(l.AnticipatedQueries, s)
			}
		}
	}
	if domain, ok := params["domain"].(string); ok {
		l.Domain = domain
	}
	if taskType, ok := params["task_type"].(string); ok {
		l.TaskType = taskType
	}

	// Generate enriched embedding text for V2 learnings
	if l.IsV2() {
		l.EmbeddingText = l.BuildEmbeddingText()
	}

	// Phase 0: exact content hash dedup (O(1), deterministic)
	hash := textutil.ContentHash(text)
	l.ContentHash = hash
	if existing, err := h.store.GetLearningByContentHash(hash); err == nil && existing != nil {
		h.store.IncrementMatchCounts([]int64{existing.ID})
		return jsonResponse(map[string]any{
			"id":           existing.ID,
			"message":      fmt.Sprintf("Exact duplicate (hash match). Bumped match_count for #%d.", existing.ID),
			"deduplicated": true,
		})
	}

	// Phase 1: fuzzy dedup via token similarity (Jaccard ≥ 0.5)
	if supersedesID == 0 {
		existing, _ := h.store.GetActiveLearnings(category, project, "", "")
		newTokens := textutil.Tokenize(text)
		for _, e := range existing {
			sim := textutil.TokenSimilarity(newTokens, textutil.Tokenize(e.Content))
			if sim >= 0.5 {
				h.store.IncrementMatchCounts([]int64{e.ID})
				return jsonResponse(map[string]any{
					"id":           e.ID,
					"message":      fmt.Sprintf("Already known (similarity %.2f). Bumped match_count for #%d.", sim, e.ID),
					"deduplicated": true,
				})
			}
		}
	}

	id, err := h.store.InsertLearning(l)
	if err != nil {
		return errorResponse(err.Error())
	}

	// If superseding an old learning, check trust level first
	var supersededMsg string
	if supersedesID > 0 {
		oldID := int64(supersedesID)
		oldLearning, err := h.store.GetLearning(oldID)
		if err == nil {
			trust := storage.TrustScore(oldLearning)
			level := storage.ClassifyTrust(trust)

			// Contradiction-Boost: superseding an injected learning is an "Unexpected Outcome"
			// — strongest learning signal (Pearce & Hall 1980). Boost the correcting learning.
			if l.Importance < 4 {
				l.Importance = 4
			}
			h.store.SetImportance(id, l.Importance)
			h.store.IncrementUseCounts([]int64{id})      // mark as immediately used
			h.store.SetStability(id, 45.0)               // start at 45 days instead of 30
			h.store.IncrementNoiseCounts([]int64{oldID}) // old learning was wrong in this context

			switch level {
			case storage.TrustLow:
				h.store.SupersedeLearning(oldID, id, "superseded by remember()")
				supersededMsg = fmt.Sprintf(" Supersedes #%d (trust: %.1f, low).", oldID, trust)

			case storage.TrustMedium:
				h.store.SupersedeLearning(oldID, id, "superseded by remember()")
				supersededMsg = fmt.Sprintf(" Supersedes #%d (trust: %.1f, medium). Vorher galt: %s", oldID, trust, truncate(oldLearning.Content, 80))

			case storage.TrustHigh:
				// High trust — don't supersede immediately, set pending confirmation
				h.store.SetSupersedeStatus(oldID, "pending_confirmation")
				supersededMsg = fmt.Sprintf(" Supersede pending confirmation (trust: %.1f, high). Old learning #%d stays active.", trust, oldID)
			}
		} else {
			// Old learning not found — supersede without trust check
			h.store.SupersedeLearning(oldID, id, "superseded by remember()")
			supersededMsg = fmt.Sprintf(" Supersedes #%d.", oldID)
		}
	}

	// Auto-embed into vector store (use enriched V2 text when available)
	embedText := text
	if l.EmbeddingText != "" {
		embedText = l.EmbeddingText
	}
	h.EmbedLearning(id, embedText, category, project)

	// Auto-resolve matching knowledge gaps
	h.autoResolveGaps(text, project, id)

	// Auto-broadcast critical learnings to all sessions on this project
	if category == "gotcha" || (category == "decision" && l.Importance >= 4) {
		go func() {
			msg := fmt.Sprintf("[%s] %s", category, truncate(text, 200))
			h.store.SendBroadcast(l.SessionID, project, msg)
		}()
	}

	// Cache for proxy injection into current session
	h.recentRememberMu.Lock()
	h.recentRemembered = append(h.recentRemembered, recentLearning{ID: id, Text: text})
	h.recentRememberMu.Unlock()

	resp := map[string]any{
		"id":         id,
		"category":   category,
		"project":    project,
		"content":    truncate(text, 120),
		"model_used": l.ModelUsed,
		"message":    fmt.Sprintf("Learning #%d saved (%s, model=%s).%s", id, category, l.ModelUsed, supersededMsg),
	}
	if supersedesID > 0 {
		resp["supersedes_id"] = int64(supersedesID)
	}
	return jsonResponse(resp)
}

func rememberModel(params map[string]any) string {
	if model, ok := params["model"].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	if model, ok := params["_client_model"].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return "self"
}

func (h *Handler) handleGetLearnings(params map[string]any) Response {
	// Single learning by ID
	if id, ok := params["id"].(float64); ok && id > 0 {
		l, err := h.store.GetLearning(int64(id))
		if err != nil {
			return errorResponse(err.Error())
		}
		h.store.LoadJunctionData(l)
		return jsonResponse([]models.Learning{*l})
	}

	category, _ := params["category"].(string)
	project, _ := params["project"].(string)
	since, _ := params["since"].(string)
	before, _ := params["before"].(string)
	taskType, _ := params["task_type"].(string)
	limit := intOr(params, "limit", 30)
	learnings, err := h.store.GetActiveLearnings(category, project, since, before)
	if err != nil {
		return errorResponse(err.Error())
	}
	// Filter by task_type if specified
	if taskType != "" {
		filtered := learnings[:0]
		for _, l := range learnings {
			if l.TaskType == taskType {
				filtered = append(filtered, l)
			}
		}
		learnings = filtered
	}
	// Enrich with session fixation ratios before scoring
	h.store.EnrichSessionFixationScores(learnings)
	// Deduplicate, score, and limit to prevent flooding context
	// Skip dedup for unfinished — they share phrasing ("not implemented", "still open")
	// but are semantically distinct tasks
	if category != "unfinished" {
		learnings = briefing.Deduplicate(learnings, 0.4)
	}
	ctx := models.QueryContext{Project: project}
	models.ContextualScoreAndSort(learnings, ctx)
	if len(learnings) > limit {
		learnings = learnings[:limit]
	}

	// Increment hit counts for returned learnings (async, fire-and-forget)
	go func() {
		var ids []int64
		for _, l := range learnings {
			ids = append(ids, l.ID)
		}
		h.store.IncrementInjectCounts(ids)
	}()

	return jsonResponse(learnings)
}

func (h *Handler) handleQueryFacts(params map[string]any) Response {
	opts := storage.QueryFactsOpts{
		Entity:   stringOr(params, "entity", ""),
		Action:   stringOr(params, "action", ""),
		Keyword:  stringOr(params, "keyword", ""),
		Domain:   stringOr(params, "domain", ""),
		Project:  stringOr(params, "project", ""),
		Category: stringOr(params, "category", ""),
		Limit:    intOr(params, "limit", 20),
	}

	if opts.Entity == "" && opts.Action == "" && opts.Keyword == "" && opts.Domain == "" && opts.Category == "" {
		return errorResponse("at least one filter required: entity, action, keyword, domain, or category")
	}

	learnings, err := h.store.QueryFacts(opts)
	if err != nil {
		return errorResponse(fmt.Sprintf("query_facts: %v", err))
	}

	// Batch-load junction data for richer output
	for i := range learnings {
		h.store.LoadJunctionData(&learnings[i])
	}

	return jsonResponse(learnings)
}

// extractIDs parses params["ids"] (array of numbers) into []int64.
func extractIDs(params map[string]any) []int64 {
	idsRaw, ok := params["ids"].([]any)
	if !ok || len(idsRaw) == 0 {
		return nil
	}
	var ids []int64
	for _, v := range idsRaw {
		if f, ok := v.(float64); ok {
			ids = append(ids, int64(f))
		}
	}
	return ids
}

func (h *Handler) handleIncrementNoise(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	// Filter out behavioral rules (gotcha, feedback) — their value is in being
	// injected, not in being explicitly referenced. Only active-reference categories
	// (pattern, teaching, decision, etc.) get noise_count bumped.
	var activeRefIDs []int64
	for _, id := range ids {
		l, err := h.store.GetLearning(id)
		if err != nil || l == nil {
			continue
		}
		switch l.Category {
		case "gotcha", "feedback", "narrative", "pivot_moment", "unfinished":
			// Behavioral/contextual = seatbelts/anchors. Injection IS the value.
			continue
		default:
			activeRefIDs = append(activeRefIDs, id)
		}
	}
	if len(activeRefIDs) == 0 {
		return jsonResponse(map[string]any{"bumped": 0, "skipped_behavioral": len(ids)})
	}
	if err := h.store.IncrementNoiseCounts(activeRefIDs); err != nil {
		return errorResponse(fmt.Sprintf("increment noise: %v", err))
	}
	// Stufe 4: propagate noise signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(activeRefIDs)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "noise")
	}
	return jsonResponse(map[string]any{"bumped": len(activeRefIDs), "skipped_behavioral": len(ids) - len(activeRefIDs)})
}

func (h *Handler) handleResolveProject(params map[string]any) Response {
	projectDir, _ := params["project_dir"].(string)
	if projectDir == "" {
		return errorResponse("project_dir (string) required")
	}
	resolved := h.store.ResolveProjectShort(projectDir)
	return jsonResponse(map[string]any{"project_short": resolved})
}

func (h *Handler) handleQuarantineSession(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id (string) required")
	}
	affected, err := h.store.QuarantineSession(sessionID)
	if err != nil {
		return errorResponse(fmt.Sprintf("quarantine: %v", err))
	}
	return jsonResponse(map[string]any{"quarantined": affected, "session_id": sessionID})
}

func (h *Handler) handleSkipIndexing(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		// Skip current session — caller passes thread ID
		return errorResponse("session_id (string) required")
	}
	if err := h.store.SkipExtraction(sessionID); err != nil {
		return errorResponse(fmt.Sprintf("skip_indexing: %v", err))
	}
	return jsonResponse(map[string]any{"skipped": true, "session_id": sessionID})
}

func (h *Handler) handleIncrementMatch(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	if err := h.store.IncrementMatchCounts(ids); err != nil {
		return errorResponse(fmt.Sprintf("increment match: %v", err))
	}
	// Stufe 4: propagate match signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(ids)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "match")
	}
	return jsonResponse(map[string]any{"bumped": len(ids)})
}

func (h *Handler) handleIncrementInject(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	if err := h.store.IncrementInjectCounts(ids); err != nil {
		return errorResponse(fmt.Sprintf("increment inject: %v", err))
	}
	// Stufe 4: propagate inject signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(ids)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "inject")
	}
	return jsonResponse(map[string]any{"bumped": len(ids)})
}

func (h *Handler) handleIncrementUse(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	if err := h.store.IncrementUseCounts(ids); err != nil {
		return errorResponse(fmt.Sprintf("increment use: %v", err))
	}
	// Stufe 4: propagate use signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(ids)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "use")
	}
	return jsonResponse(map[string]any{"bumped": len(ids)})
}

func (h *Handler) handleIncrementSave(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	if err := h.store.IncrementSaveCounts(ids); err != nil {
		return errorResponse(fmt.Sprintf("increment save: %v", err))
	}
	// Stufe 4: propagate save signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(ids)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "save")
	}
	return jsonResponse(map[string]any{"bumped": len(ids)})
}

func (h *Handler) handleIncrementFail(params map[string]any) Response {
	ids := extractIDs(params)
	if len(ids) == 0 {
		return errorResponse("ids (array of numbers) required")
	}
	if err := h.store.IncrementFailCounts(ids); err != nil {
		return errorResponse(fmt.Sprintf("increment fail: %v", err))
	}
	// Stufe 4: propagate fail signal to cluster scores
	clusterMap := h.store.GetRecentClusterForLearnings(ids)
	for lid, cid := range clusterMap {
		h.store.IncrementClusterScore(lid, cid, "fail")
	}
	return jsonResponse(map[string]any{"bumped": len(ids)})
}

func (h *Handler) handleIncrementTurn(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}
	count, err := h.store.IncrementTurnCount(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("increment turn: %v", err))
	}
	return jsonResponse(map[string]any{"turn_count": count})
}

func (h *Handler) handleFlagContradiction(params map[string]any) Response {
	desc, _ := params["description"].(string)
	if desc == "" {
		return errorResponse("description required")
	}
	project, _ := params["project"].(string)
	threadID, _ := params["thread_id"].(string)

	learningIDs := "[]"
	var ids []int64
	if idsRaw, ok := params["learning_ids"].([]any); ok && len(idsRaw) > 0 {
		for _, v := range idsRaw {
			if f, ok := v.(float64); ok {
				ids = append(ids, int64(f))
			}
		}
		if b, err := json.Marshal(ids); err == nil {
			learningIDs = string(b)
		}
	}

	if err := h.store.InsertContradiction(learningIDs, desc, project, threadID); err != nil {
		return errorResponse(fmt.Sprintf("flag contradiction: %v", err))
	}

	// Write pairwise contradicts edges to association graph
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			h.store.InsertTypedAssociation(ids[i], ids[j], "contradicts")
		}
	}

	return jsonResponse(map[string]any{"status": "ok"})
}

func (h *Handler) handleResolve(params map[string]any) Response {
	id := int64(intOr(params, "learning_id", 0))
	reason := stringOr(params, "reason", "resolved")

	if id == 0 {
		return errorResponse("learning_id is required")
	}

	// Fetch learning details before resolving
	learning, _ := h.store.GetLearning(id)

	if err := h.store.ResolveLearning(id, reason); err != nil {
		return errorResponse(err.Error())
	}

	// Update MEMORY.md
	go h.onMutation()

	resp := map[string]any{
		"id":      id,
		"reason":  reason,
		"message": fmt.Sprintf("Learning #%d resolved: %s", id, reason),
	}
	if learning != nil {
		resp["category"] = learning.Category
		resp["project"] = learning.Project
		resp["content"] = truncate(learning.Content, 120)
	}
	return jsonResponse(resp)
}

func (h *Handler) handleResolveByText(params map[string]any) Response {
	text, _ := params["text"].(string)
	project, _ := params["project"].(string)

	if text == "" {
		return errorResponse("text is required")
	}

	matches, err := h.store.SearchUnfinished(text, project)
	if err != nil {
		return errorResponse(err.Error())
	}

	if len(matches) == 0 {
		return jsonResponse(map[string]any{
			"message":    "No matching unfinished items found",
			"candidates": []any{},
		})
	}

	best := matches[0]
	if err := h.store.ResolveLearning(best.ID, fmt.Sprintf("resolved by text match: %s", text)); err != nil {
		return errorResponse(err.Error())
	}

	go h.onMutation()

	type candidate struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Project string `json:"project"`
	}
	var candidates []candidate
	for _, m := range matches {
		candidates = append(candidates, candidate{ID: m.ID, Content: m.Content, Project: m.Project})
	}

	return jsonResponse(map[string]any{
		"id":         best.ID,
		"category":   best.Category,
		"project":    best.Project,
		"content":    truncate(best.Content, 120),
		"reason":     fmt.Sprintf("resolved by text match: %s", text),
		"message":    fmt.Sprintf("Resolved #%d: %s", best.ID, best.Content),
		"candidates": candidates,
	})
}

// handleGetLearningsSince returns learnings created after a given timestamp.
func (h *Handler) handleGetLearningsSince(params map[string]any) Response {
	project, _ := params["project"].(string)
	sinceStr, _ := params["since"].(string)
	limit := intOr(params, "limit", 10)

	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return errorResponse("invalid 'since' timestamp: " + err.Error())
	}

	learnings, err := h.store.GetLearningsSince(project, since, limit)
	if err != nil {
		return errorResponse(err.Error())
	}

	var items []map[string]any
	for _, l := range learnings {
		items = append(items, map[string]any{
			"id":         l.ID,
			"category":   l.Category,
			"content":    l.Content,
			"created_at": l.CreatedAt.Format(time.RFC3339),
		})
	}
	return jsonResponse(items)
}

// handleGetSessionFlavorsSince returns distinct session flavors with timestamps since a given time.
func (h *Handler) handleGetSessionFlavorsSince(params map[string]any) Response {
	project, _ := params["project"].(string)
	sinceStr, _ := params["since"].(string)
	limit := intOr(params, "limit", 20)

	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return errorResponse("invalid 'since' timestamp: " + err.Error())
	}

	flavors, err := h.store.GetSessionFlavorsSince(project, since, limit)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(flavors)
}

func (h *Handler) handleGetPulseLearningsSince(params map[string]any) Response {
	project, _ := params["project"].(string)
	sinceStr, _ := params["since"].(string)
	limit := intOr(params, "limit", 20)

	since, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return errorResponse("invalid 'since' timestamp: " + err.Error())
	}

	pulses, err := h.store.GetPulseLearningsSince(project, since, limit)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(pulses)
}

// autoResolveGaps resolves any knowledge gaps that match terms in the new learning content.
func (h *Handler) autoResolveGaps(content, project string, learningID int64) {
	gaps, err := h.store.GetActiveGaps(project, 100)
	if err != nil || len(gaps) == 0 {
		return
	}
	contentLower := strings.ToLower(content)
	for _, g := range gaps {
		if strings.Contains(contentLower, strings.ToLower(g.Topic)) {
			h.store.ResolveGap(g.Topic, project, learningID)
		}
	}
}

func (h *Handler) handleRelate(params map[string]any) Response {
	idA := int64(intOr(params, "learning_id_a", 0))
	idB := int64(intOr(params, "learning_id_b", 0))
	relType := stringOr(params, "relation_type", "")

	if idA == 0 || idB == 0 {
		return errorResponse("learning_id_a and learning_id_b required")
	}
	validTypes := map[string]bool{"supports": true, "contradicts": true, "depends_on": true, "relates_to": true}
	if !validTypes[relType] {
		return errorResponse("relation_type must be one of: supports, contradicts, depends_on, relates_to")
	}

	if err := h.store.InsertTypedAssociation(idA, idB, relType); err != nil {
		return errorResponse(fmt.Sprintf("relate: %v", err))
	}
	return jsonResponse(map[string]any{"status": "ok", "learning_id_a": idA, "learning_id_b": idB, "relation_type": relType})
}

func (h *Handler) handleGetContradictingPairs(params map[string]any) Response {
	newIDs := parseIntSlice(params, "new_ids")
	previousIDs := parseIntSlice(params, "previous_ids")

	pairs, err := h.store.GetContradictingPairs(newIDs, previousIDs)
	if err != nil {
		return errorResponse(fmt.Sprintf("get contradicting pairs: %v", err))
	}

	pairSlices := make([][]int64, len(pairs))
	for i, p := range pairs {
		pairSlices[i] = []int64{p[0], p[1]}
	}
	return jsonResponse(map[string]any{"pairs": pairSlices})
}

func parseIntSlice(params map[string]any, key string) []int64 {
	raw, ok := params[key].([]any)
	if !ok {
		return nil
	}
	var ids []int64
	for _, v := range raw {
		if f, ok := v.(float64); ok {
			ids = append(ids, int64(f))
		}
	}
	return ids
}
