package daemon

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/repo"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// maxGetLearningsLimit caps a caller supplied limit for get_learnings. The value
// feeds an oversampling multiply and the final slice: an absurd limit wraps that
// multiply negative (which drops the LIMIT clause and returns the whole table), and
// a negative limit reaches learnings[:limit] and panics.
const maxGetLearningsLimit = 200

func (h *Handler) handleRemember(params map[string]any) Response {
	text, _ := params["text"].(string)
	category := stringOr(params, "category", "explicit_teaching")
	projectParam, _ := params["project"].(string)
	source := stringOr(params, "source", "user_stated")
	supersedesID, _ := params["supersedes"].(float64) // optional: ID of learning this replaces

	if !models.IsValidCategory(category) {
		category = "explicit_teaching"
	}

	origin, _ := params["origin"].(string)
	if origin == "" {
		origin = "user"
	}

	// Entities are parsed before learning construction: they feed the
	// content-signal half of project attribution.
	var entities []string
	if entitiesRaw, ok := params["entities"].([]any); ok {
		for _, e := range entitiesRaw {
			if s, ok := e.(string); ok {
				entities = append(entities, s)
			}
		}
	}

	// Project attribution (repo-root semantics + content signal); the basis
	// lands in learnings.project_source for auditability.
	cwd, _ := params["_cwd"].(string)
	project, projectSource := h.attributeLearningProject(text, entities, projectParam, "", cwd)

	// The supersede target is resolved once, before anything is written, so a
	// rejected override can honestly say "nothing was saved".
	var target *models.Learning
	var targetErr error
	if supersedesID > 0 {
		if supersedesID != float64(int64(supersedesID)) {
			return errorResponse("supersedes must be an integer learning ID; nothing was saved")
		}
		target, targetErr = h.store.GetLearning(int64(supersedesID))
	}

	// Explicit override of the trust gate. It is an override, not a convenience
	// flag: without a target and a justification it would let any caller retire
	// a high-trust learning silently, which is what the gate exists to prevent.
	forceSupersede := false
	if raw, ok := params["force_supersede"]; ok && raw != nil {
		b, isBool := raw.(bool)
		if !isBool {
			return errorResponse("force_supersede must be a boolean; nothing was saved")
		}
		forceSupersede = b
	}
	supersedeReason, _ := params["supersede_reason"].(string)
	// The reason is stored and later printed line by line (get_learnings history,
	// MCP box): line breaks would forge lines there.
	supersedeReason = strings.Join(strings.Fields(supersedeReason), " ")
	if runes := []rune(supersedeReason); len(runes) > 500 {
		supersedeReason = string(runes[:500]) + "..."
	}
	if forceSupersede {
		if supersedesID <= 0 {
			return errorResponse("force_supersede requires supersedes: name the learning to retire; nothing was saved")
		}
		if supersedeReason == "" {
			return errorResponse("force_supersede requires supersede_reason: state why the old learning is wrong; nothing was saved")
		}
		if targetErr != nil {
			return errorResponse(fmt.Sprintf("force_supersede: learning #%d could not be loaded: %v; nothing was saved", int64(supersedesID), targetErr))
		}
		// A forced supersede retires knowledge of one project. The two sides use
		// different representations of the same project (short name versus path,
		// see attributeLearningProject below), so compare the canonical form, and
		// the repository root when both sides name one. A missing project on
		// either side fails closed: there is nothing to prove ownership with.
		if !sameProjectForSupersede(target.Project, project) {
			return errorResponse(fmt.Sprintf("force_supersede refused: #%d belongs to project %q, not %q; nothing was saved", int64(supersedesID), target.Project, project))
		}
	}

	l := &models.Learning{
		Category: category, Content: text, Project: project,
		Confidence: 1.0, CreatedAt: timeNow(), ModelUsed: h.rememberModel(params),
		Source: source, OriginTool: origin, ProjectSource: projectSource,
		CanonicalProject: canonicalProjectFor(project),
	}

	// Session ID: the caller's own session first — explicit param, then the
	// per-call _session_id the MCP layer injects, then PID/cwd — and only then
	// the global active session. activeSessionID is whichever session wrote
	// last and, under opencode v2 where one `opencode serve --service` hosts
	// every session, it routinely belongs to a different session than the caller.
	if proven := h.provenSessionID(params, "session_id"); proven != "" {
		l.SessionID = proven
		// The emotional intensity is a session rating (extraction produces one
		// per session) and remember() has no rating of its own — it inherits the
		// one its session carries. Only a proven caller session may lend it: the
		// fallbacks name whichever session wrote last, not the caller's.
		l.EmotionalIntensity = h.store.GetSessionMaxIntensity(proven)
	} else if sid := h.resolveActiveSessionFallback(params); sid != "" {
		l.SessionID = sid
	} else if latest, err := h.store.GetLatestSessionID(project); err == nil && latest != "" {
		l.SessionID = latest
	}

	// Parse optional V2 params
	l.Entities = entities
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
	if attribution, ok := params["attribution"].(string); ok {
		l.Attribution = attribution
	}

	// Collect metadata-sparseness warnings for the response message
	var metadataWarnings []string
	if len(l.AnticipatedQueries) == 0 {
		metadataWarnings = append(metadataWarnings, "anticipated_queries empty (hybrid_search will struggle)")
	}
	if len(l.Entities) == 0 {
		metadataWarnings = append(metadataWarnings, "entities empty (query_facts can't find by entity)")
	}
	if l.TriggerRule == "" {
		metadataWarnings = append(metadataWarnings, "trigger empty (no proactive surface)")
	}
	if l.Domain == "" {
		metadataWarnings = append(metadataWarnings, "domain defaulted to 'general'")
	}
	codeFingerprint, _ := params["code_fingerprint"].(string)

	// Auto-generate code fingerprint from entity file paths when not explicitly provided.
	if codeFingerprint == "" && len(l.Entities) > 0 {
		codeFingerprint = h.computeCodeFingerprint(l.Entities)
	}

	// Generate enriched embedding text for V2 learnings
	if l.IsV2() {
		l.EmbeddingText = l.BuildEmbeddingText()
	}

	// Reject raw bash transcripts — session artifacts, never durable knowledge.
	if textutil.IsRawBashTranscript(text) {
		return jsonResponse(map[string]any{
			"rejected": true,
			"message":  "Raw bash transcript without durable insight — not saved.",
		})
	}

	// Volatile sentinel: live-derivable state snapshots are not durable knowledge.
	// Fail-open — sentinel errors never block remembering.
	if evalClient := h.commitEvalLLM(); evalClient != nil && extraction.LooksLikeStateSnapshot(text) {
		verdicts, vErr := extraction.ClassifyVolatileBatch(evalClient, []models.Learning{{Content: text}})
		if vErr == nil && len(verdicts) == 1 {
			switch verdicts[0].Verdict {
			case "volatile":
				return jsonResponse(map[string]any{
					"rejected": true,
					"message":  fmt.Sprintf("Volatile state snapshot — not saved (%s). Live queries answer this correctly; extract the durable insight instead.", verdicts[0].Reason),
				})
			case "bounded":
				ttl := time.Now().AddDate(0, 0, 30)
				l.ExpiresAt = &ttl
			}
		}
	}

	// Phase 0: exact content hash dedup (O(1), deterministic)
	hash := textutil.ContentHash(text)
	l.ContentHash = hash
	if existing, err := h.store.GetLearningByContentHash(hash); err == nil && existing != nil {
		h.store.IncrementMatchCounts([]int64{existing.ID})
		msg := fmt.Sprintf("Exact duplicate (hash match). Bumped match_count for #%d.", existing.ID)
		resp := map[string]any{
			"id":           existing.ID,
			"deduplicated": true,
		}
		if supersedesID > 0 {
			// This early return happens before the supersede is applied. The
			// caller must not assume the old learning was retired.
			resp["supersedes_id"] = int64(supersedesID)
			resp["supersede_outcome"] = "duplicate"
			msg += fmt.Sprintf(" The requested supersede of #%d was NOT performed.", int64(supersedesID))
		}
		resp["message"] = msg
		return jsonResponse(resp)
	}

	// Phase 1: fuzzy dedup via token similarity (Jaccard ≥ 0.5). TokenSimilarity
	// is a containment metric (fraction of the SHORTER text's tokens found in the
	// longer): for texts of very different length it wrongly reports "similar"
	// when a long text merely subsumes a short one's vocabulary. Only compare
	// against existing learnings of comparable size (match #85112).
	if supersedesID == 0 {
		existing, _ := h.store.GetActiveLearnings(category, project, "", "", 0)
		newTokens := textutil.Tokenize(text)
		for _, e := range existing {
			eTokens := textutil.Tokenize(e.Content)
			if len(eTokens) == 0 || len(newTokens) == 0 {
				continue
			}
			ratio := float64(len(eTokens)) / float64(len(newTokens))
			if ratio < 0.5 || ratio > 2.0 {
				continue
			}
			sim := textutil.TokenSimilarity(newTokens, eTokens)
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

	// Store code fingerprint if provided (staleness detection)
	if codeFingerprint != "" {
		h.store.StoreCodeFingerprint(id, codeFingerprint)
	}

	// If superseding an old learning, check trust level first
	var supersededMsg string
	// supersedeOutcome is the machine-readable result of the supersede attempt:
	// replaced, blocked, forced or not_found. Prose in "message" is not enough,
	// the MCP formatter drops it and used to render a blocked supersede as done.
	var supersedeOutcome string
	var supersedeTrust float64
	var supersedeErr string
	if supersedesID > 0 {
		oldID := int64(supersedesID)
		// Re-read right before the trust verdict: TrustScore depends on counts that
		// a concurrent hit can bump between the pre-insert check and this point.
		oldLearning, err := h.store.GetLearning(oldID)
		if err == nil {
			trust := storage.TrustScore(oldLearning)
			level := storage.ClassifyTrust(trust)
			supersedeTrust = trust

			// Contradiction-Boost: superseding an injected learning is an "Unexpected Outcome"
			// — strongest learning signal (Pearce & Hall 1980). Boost the correcting learning.
			if l.Importance < 4 {
				l.Importance = 4
			}
			h.store.SetImportance(id, l.Importance)
			h.store.IncrementUseCounts([]int64{id}) // mark as immediately used
			h.store.SetStability(id, 45.0)          // start at 45 days instead of 30

			if forceSupersede {
				// Caller confirmed the old learning is wrong. The gate's verdict
				// stays visible; the justification becomes part of the chain.
				if serr := h.supersedeNow(oldID, id, "forced supersede: "+supersedeReason); serr != nil {
					supersedeOutcome = "failed"
					supersedeErr = serr.Error()
					supersededMsg = fmt.Sprintf(" Supersede of #%d failed (trust: %.1f, forced): %v. Old learning stays active.", oldID, trust, serr)
				} else {
					supersedeOutcome = "forced"
					supersededMsg = fmt.Sprintf(" Supersedes #%d (trust: %.1f, forced: %s). Vorher galt: %s", oldID, trust, supersedeReason, truncate(oldLearning.Content, 80))
				}
			} else {
				switch level {
				case storage.TrustLow:
					if serr := h.supersedeNow(oldID, id, "superseded by remember()"); serr != nil {
						supersedeOutcome = "failed"
						supersedeErr = serr.Error()
						supersededMsg = fmt.Sprintf(" Supersede of #%d failed (trust: %.1f, low): %v.", oldID, trust, serr)
					} else {
						supersedeOutcome = "replaced"
						supersededMsg = fmt.Sprintf(" Supersedes #%d (trust: %.1f, low).", oldID, trust)
					}

				case storage.TrustMedium:
					if serr := h.supersedeNow(oldID, id, "superseded by remember()"); serr != nil {
						supersedeOutcome = "failed"
						supersedeErr = serr.Error()
						supersededMsg = fmt.Sprintf(" Supersede of #%d failed (trust: %.1f, medium): %v.", oldID, trust, serr)
					} else {
						supersedeOutcome = "replaced"
						supersededMsg = fmt.Sprintf(" Supersedes #%d (trust: %.1f, medium). Vorher galt: %s", oldID, trust, truncate(oldLearning.Content, 80))
					}

				case storage.TrustHigh:
					// High trust — the old learning stays active and the conflict is
					// recorded as a contradicts edge. Decision D6: nothing ever read
					// the parked pending_confirmation status, so the pair must stay
					// visible instead of being silently marked.
					supersedeOutcome = "blocked"
					if err := h.store.InsertTypedAssociation(oldID, id, "contradicts"); err != nil {
						supersededMsg = fmt.Sprintf(" Supersede blocked (trust: %.1f, high). Old learning #%d stays active; contradiction edge failed: %v.", trust, oldID, err)
					} else {
						supersededMsg = fmt.Sprintf(" Supersede blocked (trust: %.1f, high). Old learning #%d stays active; contradiction recorded.", trust, oldID)
					}
				}
			}

			// The target counts as wrong in this context only when a conflict was
			// established; a failed write says nothing about it.
			switch supersedeOutcome {
			case "replaced", "forced", "blocked":
				h.store.IncrementNoiseCounts([]int64{oldID})
			}
		} else {
			// Nothing to retire: the target could not be loaded. The write is
			// attempted anyway (the read may have failed transiently) and the
			// outcome follows from the stored state, not from the read error.
			if serr := h.supersedeNow(oldID, id, "superseded by remember()"); serr == nil {
				supersedeOutcome = "replaced"
				supersededMsg = fmt.Sprintf(" Supersedes #%d.", oldID)
			} else {
				supersedeOutcome = "not_found"
				supersedeErr = serr.Error()
				supersededMsg = fmt.Sprintf(" Supersedes #%d not applied: %v.", oldID, serr)
			}
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

	warning := ""
	if len(metadataWarnings) > 0 {
		warning = " ⚠ " + strings.Join(metadataWarnings, ", ")
	}
	resp := map[string]any{
		"id":             id,
		"category":       category,
		"project":        project,
		"project_source": projectSource,
		"content":        truncate(text, 120),
		"model_used":     l.ModelUsed,
		"message":        fmt.Sprintf("Learning #%d saved (%s, model=%s).%s%s", id, category, l.ModelUsed, supersededMsg, warning),
	}
	if supersedesID > 0 {
		resp["supersedes_id"] = int64(supersedesID)
		if supersedeOutcome != "" {
			resp["supersede_outcome"] = supersedeOutcome
		}
		if target != nil {
			resp["supersede_trust"] = supersedeTrust
		}
		if supersedeErr != "" {
			resp["supersede_error"] = supersedeErr
		}
	}
	return jsonResponse(resp)
}

// supersedeNow links the new learning to the target and confirms the write by
// reading the row back. SupersedeLearningBatch's UPDATE matches only an active
// row and reports no error when it matches none (learnings.go:1146), so a nil
// error is not evidence of a replacement.
func (h *Handler) supersedeNow(oldID, newID int64, reason string) error {
	if err := h.store.SupersedeLearning(oldID, newID, reason); err != nil {
		return err
	}
	after, err := h.store.GetLearning(oldID)
	if err != nil {
		return fmt.Errorf("read back #%d: %w", oldID, err)
	}
	if after.SupersededBy == nil || *after.SupersededBy != newID {
		return fmt.Errorf("#%d was not linked to #%d (already superseded?)", oldID, newID)
	}
	return nil
}

// rememberModel resolves the model attribution for an in-session remember() call.
// Resolution order (first wins):
//  1. explicit `model` param (caller override)
//  2. `_client_model` (MCP server injects from env vars: CODEX_MODEL, OPENAI_MODEL,
//     ANTHROPIC_MODEL, CLAUDE_MODEL, MODEL, or the new YESMEM_MODEL_ID)
//  3. `session_model:<sid>` from proxy_state — proxy.go persists this per CC API
//     request, so CC sessions always have a real model here even when the MCP
//     layer doesn't inject one. See learning #76567.
//  4. "self" (legacy fallback — should rarely trigger after fix #3)
func (h *Handler) rememberModel(params map[string]any) string {
	if model, ok := params["model"].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	if model, ok := params["_client_model"].(string); ok && strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	// Fall back to session_model:<sid> from proxy_state. Try _session_id (MCP
	// server injects) first, then session_id (legacy). Matches the daemon-side
	// session resolution logic at handler_learnings.go:43.
	sid, _ := params["_session_id"].(string)
	if sid == "" {
		sid, _ = params["session_id"].(string)
	}
	if sid != "" && h.store != nil {
		if model, err := h.store.GetProxyState("session_model:" + sid); err == nil && strings.TrimSpace(model) != "" {
			return model
		}
	}
	return "self"
}

func (h *Handler) handleGetLearnings(params map[string]any) Response {
	// Single learning by ID
	if id, ok := params["id"].(float64); ok && id > 0 {
		if history, _ := params["history"].(bool); history {
			chain, err := h.store.GetLearningChain(int64(id))
			if err != nil {
				return errorResponse(err.Error())
			}
			for i := range chain {
				h.store.LoadJunctionData(&chain[i])
			}
			return jsonResponse(chain)
		}
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
	if limit < 0 {
		limit = 0
	}
	if limit > maxGetLearningsLimit {
		limit = maxGetLearningsLimit
	}
	if since != "" && storage.ParseFilterTime(since).IsZero() {
		return errorResponse(fmt.Sprintf("since is not a parseable timestamp: %q", truncate(since, 80)))
	}
	if before != "" && storage.ParseFilterTime(before).IsZero() {
		return errorResponse(fmt.Sprintf("before is not a parseable timestamp: %q", truncate(before, 80)))
	}
	// limit=0 has always meant "nothing"; answer it without touching the table.
	// GetActiveLearnings without a LIMIT clause would read every active row.
	if limit == 0 {
		return jsonResponse([]models.Learning{})
	}
	learnings, err := h.store.GetActiveLearningsFiltered(storage.LearningsFilter{
		Category: category,
		Project:  project,
		Since:    since,
		Before:   before,
		TaskType: taskType,
	}, limit*5)
	if err != nil {
		return errorResponse(err.Error())
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

// bumpClusterScore propagates a signal to the cluster scores. These rows feed the
// cluster affinity (handler_hybrid.go), so a failed write must not pass silently.
func (h *Handler) bumpClusterScore(learningID, clusterID int64, field string) {
	if err := h.store.IncrementClusterScore(learningID, clusterID, field); err != nil {
		log.Printf("cluster score %s for learning %d (cluster %d): %v", field, learningID, clusterID, err)
	}
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
		h.bumpClusterScore(lid, cid, "noise")
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
	// The session id is the caller's claim, and every row under it is quarantined.
	// A caller that names its project scopes the write to that project; a caller
	// that names none keeps the session-only match, which is what the crash path
	// needs for the backend addresses a row carries.
	project, _ := params["project"].(string)
	var affected int64
	var err error
	if project == "" {
		affected, err = h.store.QuarantineSession(sessionID)
	} else {
		affected, err = h.store.QuarantineSessionInProject(project, sessionID)
	}
	if err != nil {
		return errorResponse(fmt.Sprintf("quarantine: %v", err))
	}
	return jsonResponse(map[string]any{"quarantined": affected, "session_id": sessionID})
}

// handleUnquarantineSession lifts a session quarantine. Without it, a quarantine
// applied by mistake is a one-way door. The learnings come back immediately because
// quarantining never deleted their vectors; a scratchpad taint written by crash
// recovery is a separate marker and stays.
func (h *Handler) handleUnquarantineSession(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id (string) required")
	}
	affected, err := h.store.UnquarantineSession(sessionID)
	if err != nil {
		return errorResponse(fmt.Sprintf("unquarantine: %v", err))
	}
	// Audited on purpose: lifting the quarantine is the one call that can undo the
	// protection against a session's own poisoned learnings.
	log.Printf("unquarantine_session: lifted quarantine for %s (%d learnings)", sessionID, affected)
	return jsonResponse(map[string]any{"unquarantined": affected, "session_id": sessionID})
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
		h.bumpClusterScore(lid, cid, "match")
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
		h.bumpClusterScore(lid, cid, "inject")
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
		h.bumpClusterScore(lid, cid, "use")
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
		h.bumpClusterScore(lid, cid, "save")
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
		h.bumpClusterScore(lid, cid, "fail")
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

	found, err := h.store.SearchUnfinishedRanked(text, project)
	if err != nil {
		return errorResponse(err.Error())
	}

	type candidate struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Project string `json:"project"`
	}
	candidates := []candidate{}
	for _, m := range found.Items {
		candidates = append(candidates, candidate{ID: m.ID, Content: m.Content, Project: m.Project})
	}

	best, ok := found.AutoResolveTarget()
	if !ok {
		return jsonResponse(map[string]any{
			"resolved":   false,
			"reason":     found.SkipReason(),
			"message":    "Not resolved: " + found.SkipReason(),
			"candidates": candidates,
		})
	}

	reason := fmt.Sprintf("resolved by text match: %s", text)
	if err := h.store.ResolveLearning(best.ID, reason); err != nil {
		return errorResponse(err.Error())
	}

	go h.onMutation()

	return jsonResponse(map[string]any{
		"id":         best.ID,
		"resolved":   true,
		"category":   best.Category,
		"project":    best.Project,
		"content":    truncate(best.Content, 120),
		"reason":     reason,
		"message":    fmt.Sprintf("Resolved #%d: %s", best.ID, truncate(best.Content, 120)),
		"candidates": candidates,
	})
}

// handleResolveByCommit closes the open task a commit message names. It is the
// commit-shaped sibling of resolve: it closes the open task the commit SUBJECT
// names, by learning id or ticket key, and resolves nothing without such an
// anchor.
func (h *Handler) handleResolveByCommit(params map[string]any) Response {
	message, _ := params["message"].(string)
	project, _ := params["project"].(string)

	if message == "" {
		return errorResponse("message is required")
	}

	decision, err := h.store.ResolveCommit(message, project)
	if err != nil {
		return errorResponse(err.Error())
	}

	type candidate struct {
		ID      int64  `json:"id"`
		Content string `json:"content"`
		Project string `json:"project"`
	}
	candidates := []candidate{}
	for _, m := range decision.Candidates {
		candidates = append(candidates, candidate{ID: m.ID, Content: m.Content, Project: m.Project})
	}

	if decision.Target == nil {
		reason := decision.Reason
		if reason == "" {
			reason = "no learning id or ticket key in the commit subject"
		}
		return jsonResponse(map[string]any{
			"resolved":   false,
			"reason":     reason,
			"message":    "Not resolved: " + reason,
			"candidates": candidates,
		})
	}

	best := decision.Target
	go h.onMutation()

	return jsonResponse(map[string]any{
		"id":         best.ID,
		"resolved":   true,
		"category":   best.Category,
		"project":    best.Project,
		"content":    truncate(best.Content, 120),
		"reason":     decision.Recorded,
		"message":    fmt.Sprintf("Resolved #%d: %s", best.ID, truncate(best.Content, 120)),
		"candidates": candidates,
	})
}

// handleReopen undoes a resolution. It is the way back when resolve or
// resolve_by_text closed the wrong item.
func (h *Handler) handleReopen(params map[string]any) Response {
	id := int64(intOr(params, "learning_id", 0))
	if id == 0 {
		return errorResponse("learning_id is required")
	}

	if err := h.store.ReopenLearning(id); err != nil {
		return errorResponse(err.Error())
	}

	go h.onMutation()

	resp := map[string]any{
		"id":      id,
		"message": fmt.Sprintf("Learning #%d reopened", id),
	}
	if learning, _ := h.store.GetLearning(id); learning != nil {
		resp["category"] = learning.Category
		resp["project"] = learning.Project
		resp["content"] = truncate(learning.Content, 120)
	}
	return jsonResponse(resp)
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

	// A self-link promotes a learning towards itself and says nothing; the id ordering
	// carries no meaning, so this is always a mistake.
	if idA == idB {
		return errorResponse(fmt.Sprintf("relate: learning %d cannot relate to itself — name the two learnings that belong together", idA))
	}
	// An edge to a learning that does not exist can never surface: the neighbour lookup
	// skips missing rows, so the caller would believe in a relation that does nothing.
	for _, id := range []int64{idA, idB} {
		learning, err := h.store.GetLearning(id)
		if errors.Is(err, sql.ErrNoRows) {
			return errorResponse(fmt.Sprintf("relate: no learning with id %d — look the id up with hybrid_search or query_facts", id))
		}
		if err != nil {
			return errorResponse(fmt.Sprintf("relate: %v", err))
		}
		if learning == nil {
			return errorResponse(fmt.Sprintf("relate: no learning with id %d — look the id up with hybrid_search or query_facts", id))
		}
	}

	previous, err := h.store.AssociationPairType(idA, idB)
	if err != nil {
		return errorResponse(fmt.Sprintf("relate: %v", err))
	}

	if err := h.store.InsertTypedAssociation(idA, idB, relType); err != nil {
		return errorResponse(fmt.Sprintf("relate: %v", err))
	}
	out := map[string]any{"status": "ok", "learning_id_a": idA, "learning_id_b": idB, "relation_type": relType}
	// A pair carries one type. Replacing is legitimate; doing it without a word is not,
	// because the caller believes it added a relation where it removed one.
	if previous != "" && previous != relType {
		out["replaced"] = previous
		out["note"] = fmt.Sprintf("a pair carries one type: %s replaced %s on %d -> %d", relType, previous, idA, idB)
	}
	return jsonResponse(out)
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

// handleGetSessionFlavorsForSession returns all distinct flavors for a single session.
// Unlike handleGetSessionFlavorsSince (which groups by session_id for multi-session overview),
// this returns all phase flavors within one session to show its evolution.
func (h *Handler) handleGetSessionFlavorsForSession(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id required")
	}

	flavors, err := h.store.GetSessionFlavorsForSession(sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(flavors)
}

// canonicalProjectFor derives the canonical project basename from a project string.
// If project contains .worktrees/, returns the parent directory basename.
func canonicalProjectFor(project string) string {
	return models.CanonicalProject(project)
}

// sameProjectForSupersede answers whether a forced supersede may retire a
// learning of targetProject from project. It is deliberately NOT capProjectMatches:
// reachability decides whether a caller may run someone's code, so a bare-name
// caller must not reach a path-bound row (YM-359). Retiring a memory is the weaker
// action, and refusing a caller that names its project the way the user speaks it
// would only block a legitimate correction. It fails closed on a missing project: an
// unassigned value is not a project, so it must not match another unassigned one.
func sameProjectForSupersede(targetProject, project string) bool {
	if project == "" || targetProject == "" {
		return false
	}
	if canonicalProjectFor(targetProject) != canonicalProjectFor(project) {
		return false
	}
	// Two repositories can share a basename (/a/yesmem and /b/yesmem).
	targetRoot, projectRoot := models.RepositoryRoot(targetProject), models.RepositoryRoot(project)
	if targetRoot != "" && projectRoot != "" && targetRoot != projectRoot {
		return false
	}
	return true
}

// knownProjectPaths returns the universe of projects known to this instance.
func (h *Handler) knownProjectPaths() []string {
	paths, err := h.store.ListProjectPaths()
	if err != nil {
		log.Printf("attributeProject: list project paths: %v", err)
		return nil
	}
	return paths
}

// attributeLearningProject decides project attribution for one learning.
// Order: explicit > content > repo/session fallback. Content reassignment
// requires ≥2 path mentions of exactly one foreign project and 0 of the
// session project; anything else prominent keeps the session project and is
// flagged project_source="ambiguous". The returned source lands in
// learnings.project_source (explicit|repo|session|content|ambiguous).
func (h *Handler) attributeLearningProject(text string, entities []string, explicitProject, sessionProject, cwd string) (string, string) {
	if explicitProject != "" {
		// A name without a leading slash is what resolveProjectParam could not
		// resolve to a project path; storing it verbatim produces project values
		// no lookup matches (live: "v2-release-notes", "wal", "chief"). The
		// caller's working directory still identifies the repository, so use its
		// root and keep the original name in the log.
		if !strings.HasPrefix(explicitProject, "/") && cwd != "" {
			if base := repo.Root(cwd); base != "" {
				log.Printf("attributeProject: unresolved project %q resolved to %q from cwd", explicitProject, base)
				return base, "repo"
			}
		}
		return explicitProject, "explicit"
	}
	base, baseSource := sessionProject, "session"
	if base != "" && strings.HasPrefix(base, "/") {
		// A worktree-directory session entry counts as its main repo, so
		// mentions of the same repo are recognized as session mentions.
		// Guard: a short project name is not a filesystem path — only
		// absolute paths may be coalesced through repo.Root.
		if root := repo.Root(base); root != "" {
			base = root
		}
	} else if cwd != "" {
		base, baseSource = repo.Root(cwd), "repo"
	}
	if base == "" {
		baseSource = ""
	}

	counts := countProjectPathMentions(text, entities, h.knownProjectPaths())
	sessionCount := counts[base]
	bestPath, bestN, secondN := "", 0, 0
	for p, n := range counts {
		if p == base || n < 1 {
			continue
		}
		if n > bestN {
			bestPath, bestN, secondN = p, n, bestN
		} else if n > secondN {
			secondN = n
		}
	}
	switch {
	case bestN >= 2 && sessionCount == 0 && secondN == 0:
		return bestPath, "content"
	case bestN >= 2:
		return base, "ambiguous"
	}
	return base, baseSource
}

// countProjectPathMentions counts occurrences of project paths in text and
// entities. Paths match as prefixes against known projects (longest wins);
// unmatched absolute tokens fall back to repo.Root so worktrees living outside
// the main tree are attributed to their main repo. Known paths are themselves
// coalesced through repo.Root, so worktree-session project entries count as
// their main repo.
func countProjectPathMentions(text string, entities []string, known []string) map[string]int {
	counts := map[string]int{}
	if text == "" && len(entities) == 0 {
		return counts
	}

	counted := make(map[string]bool, len(known))
	knownByLen := make([]string, 0, len(known))
	for _, p := range known {
		if r := repo.Root(p); r != "" {
			p = r
		}
		if counted[p] {
			continue
		}
		counted[p] = true
		knownByLen = append(knownByLen, p)
	}
	sort.Slice(knownByLen, func(i, j int) bool { return len(knownByLen[i]) > len(knownByLen[j]) })

	tokens := pathTokens(text)
	for _, e := range entities {
		tokens = append(tokens, pathTokens(e)...)
	}
	seen := make(map[string]bool, len(tokens))
	for _, tok := range tokens {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		if !strings.HasPrefix(tok, "/") {
			continue
		}
		matched := ""
		for _, p := range knownByLen {
			if tok == p || strings.HasPrefix(tok, p+"/") {
				matched = p
				break
			}
		}
		if matched != "" {
			counts[matched]++
			continue
		}
		if root := repo.Root(tok); root != "" {
			counts[root]++
		}
	}
	return counts
}

// pathTokens splits s into path-like tokens, keeping '/', '.', '-' and '_'
// intact so absolute paths survive as single tokens. Trailing sentence
// punctuation is stripped so "in /home/x/repo." still resolves to the path.
func pathTokens(s string) []string {
	tokens := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) &&
			r != '/' && r != '.' && r != '-' && r != '_'
	})
	for i, tok := range tokens {
		tokens[i] = strings.TrimRight(tok, ".,;:!?")
	}
	return tokens
}

// computeCodeFingerprint generates a SHA256 hash of the content of entity file paths.
// Only entities that look like file paths (containing '/' or '\' and an extension) are included.
// The fingerprint is used by the staleness detection system to detect code changes
// that may invalidate a learning.
func (h *Handler) computeCodeFingerprint(entities []string) string {
	hasher := sha256.New()
	fileCount := 0
	for _, entity := range entities {
		// Heuristic: a file path contains a directory separator and a file extension
		if !strings.ContainsAny(entity, "/\\") || !strings.Contains(entity, ".") {
			continue
		}
		data, err := os.ReadFile(entity)
		if err != nil {
			continue // file moved/deleted — skip
		}
		fmt.Fprintf(hasher, "%s:%d:", entity, len(data))
		hasher.Write(data)
		fileCount++
	}
	if fileCount == 0 {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
