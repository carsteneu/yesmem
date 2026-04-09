package proxy

import (
	"encoding/json"
	"log"
	"strings"
)

// daemonCaller abstracts the proxy→daemon RPC for testability.
type daemonCaller func(method string, params map[string]any) (json.RawMessage, error)

// ---------- learning_used ----------

type learningUsedHandler struct {
	logger *log.Logger
	call   daemonCaller
}

func (h *learningUsedHandler) Name() string { return "_signal_learning_used" }

func (h *learningUsedHandler) ToolDefinition() map[string]any {
	return map[string]any{
		"name":        "_signal_learning_used",
		"description": "After responding, briefly note which briefing learnings you actually referenced or found irrelevant.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"used_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "IDs of learnings you referenced in your response",
				},
				"noise_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "IDs of learnings that were irrelevant to the task",
				},
			},
		},
	}
}

func (h *learningUsedHandler) SystemInstruction() string {
	return "- _signal_learning_used: If your briefing contained learning IDs (e.g. [ID:123]), note which ones you used and which were noise."
}

func (h *learningUsedHandler) ShouldActivate(ctx RequestContext) bool {
	return ctx.HasLearnings
}

func (h *learningUsedHandler) HandleResult(_ RequestContext, call ToolCallResult) {
	if usedIDs := extractIDsFromInput(call.Input, "used_ids"); len(usedIDs) > 0 {
		go func() {
			if _, err := h.call("increment_use", map[string]any{"ids": toAnySlice(usedIDs)}); err != nil {
				h.logger.Printf("[signal:learning_used] increment_use error: %v", err)
			}
		}()
	}
	if noiseIDs := extractIDsFromInput(call.Input, "noise_ids"); len(noiseIDs) > 0 {
		go func() {
			if _, err := h.call("increment_noise", map[string]any{"ids": toAnySlice(noiseIDs)}); err != nil {
				h.logger.Printf("[signal:learning_used] increment_noise error: %v", err)
			}
		}()
	}
}

// ---------- knowledge_gap ----------

type knowledgeGapHandler struct {
	logger *log.Logger
	call   daemonCaller
}

func (h *knowledgeGapHandler) Name() string { return "_signal_knowledge_gap" }

func (h *knowledgeGapHandler) ToolDefinition() map[string]any {
	return map[string]any{
		"name":        "_signal_knowledge_gap",
		"description": "Report DOMAIN KNOWLEDGE gaps only — things that should be in learnings/docs but aren't. Do NOT report missing conversation context, truncated messages, or short user confirmations.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"topic": map[string]any{
					"type":        "string",
					"description": "Specific domain knowledge that was missing (e.g. 'how cache breakpoints work', 'SQLite WAL mode behavior'). Must be a concrete technical topic, not a description of missing context.",
				},
				"severity": map[string]any{
					"type":        "string",
					"enum":        []string{"blocking", "would_help"},
					"description": "blocking = could not answer correctly without it. would_help = answer was ok but could be better.",
				},
				"resolved_topics": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Exact topic strings from the open gaps list that were answered in this response",
				},
			},
		},
	}
}

func (h *knowledgeGapHandler) SystemInstruction() string {
	return `- _signal_knowledge_gap: ONLY report genuine domain knowledge gaps — technical facts, API behaviors, architecture details that SHOULD exist in learnings but don't. Do NOT report:
  * Missing conversation context (truncated messages, short user confirmations like "ja", "ok", "test")
  * Incomplete responses (cut-off text is normal truncation, not a knowledge gap)
  * User intent ambiguity (short messages are confirmations, not gaps)
  If open gaps are listed and your response answers any, include their exact topic text in resolved_topics.`
}

func (h *knowledgeGapHandler) ShouldActivate(_ RequestContext) bool {
	return true // always active
}

func (h *knowledgeGapHandler) HandleResult(ctx RequestContext, call ToolCallResult) {
	// Track new gap if topic provided
	topic, _ := call.Input["topic"].(string)
	if topic != "" {
		severity, _ := call.Input["severity"].(string)
		go func() {
			params := map[string]any{"topic": topic, "project": ctx.Project}
			if severity != "" {
				params["severity"] = severity
			}
			if _, err := h.call("track_gap", params); err != nil {
				h.logger.Printf("[signal:knowledge_gap] track_gap error: %v", err)
			}
		}()
	}

	// Resolve gaps that were answered
	if rawTopics, ok := call.Input["resolved_topics"].([]any); ok && len(rawTopics) > 0 {
		go func() {
			for _, rt := range rawTopics {
				if t, ok := rt.(string); ok && t != "" {
					if _, err := h.call("resolve_gap", map[string]any{"topic": t, "project": ctx.Project}); err != nil {
						h.logger.Printf("[signal:knowledge_gap] resolve_gap error: %v", err)
					} else {
						h.logger.Printf("[signal:knowledge_gap] resolved: %s", truncateStr(t, 60))
					}
				}
			}
		}()
	}
}

// ---------- contradiction ----------

type contradictionHandler struct {
	logger *log.Logger
	call   daemonCaller
}

func (h *contradictionHandler) Name() string { return "_signal_contradiction" }

func (h *contradictionHandler) ToolDefinition() map[string]any {
	return map[string]any{
		"name":        "_signal_contradiction",
		"description": "Report when you notice conflicting information between learnings or between a learning and reality.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"learning_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "integer"},
					"description": "IDs of contradicting learnings (if known)",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "What the contradiction is",
				},
			},
			"required": []string{"description"},
		},
	}
}

func (h *contradictionHandler) SystemInstruction() string {
	return "- _signal_contradiction: If you notice learnings that contradict each other or contradict what you observe, report it."
}

func (h *contradictionHandler) ShouldActivate(ctx RequestContext) bool {
	return ctx.HasLearnings
}

func (h *contradictionHandler) HandleResult(ctx RequestContext, call ToolCallResult) {
	desc, _ := call.Input["description"].(string)
	if desc == "" {
		return
	}
	learningIDs := extractIDsFromInput(call.Input, "learning_ids")
	go func() {
		params := map[string]any{
			"description": desc,
			"project":     ctx.Project,
			"thread_id":   ctx.ThreadID,
		}
		if len(learningIDs) > 0 {
			params["learning_ids"] = toAnySlice(learningIDs)
		}
		if _, err := h.call("flag_contradiction", params); err != nil {
			h.logger.Printf("[signal:contradiction] flag_contradiction error: %v", err)
		}
	}()
}

// ---------- self_prime (EXPERIMENTAL — disabled) ----------
//
// Self-priming was an experiment to transport cognitive state between turns via a
// reflection-based signal. The reflection agent (Haiku) would classify mode, next_step,
// open_decision, and user_tone after each turn, and the proxy would re-inject the cached
// anchor into the next user message.
//
// Findings (2026-03-13):
// - Haiku with a single truncated turn (2000 chars user + 4000 chars response) cannot
//   reliably predict what comes next. mode/tone were roughly OK, but next_step and
//   open_decision were systematically wrong.
// - The main model (Opus) already adapts to user tone and context organically from the
//   message history — the selfprime was largely redundant.
// - Inline generation (Claude writes a <self-prime> tag) would produce better quality but
//   remains visible in the terminal since SSE stream is already written to the client.
//   Extracting from messages[-1] on the next request avoids SSE parsing but doesn't hide it.
// - The only scenario where selfprime adds value is after compaction (history lost), but
//   stubs/archive-blocks solve that more cleanly.
//
// Kept as reference for potential future use (e.g. post-compaction anchoring).

// selfPrimeHandler captures mode/state anchors from Claude for re-injection in the next request.
// The proxy caches the anchor text and injects it as a system block on the next turn.
type selfPrimeHandler struct {
	logger *log.Logger
	// setCached is called with (threadID, anchor) to cache the self-prime
	setCached func(threadID, anchor string)
}

func (h *selfPrimeHandler) Name() string { return "_signal_self_prime" }

func (h *selfPrimeHandler) ToolDefinition() map[string]any {
	return map[string]any{
		"name":        "_signal_self_prime",
		"description": "Capture actionable cognitive state for the next turn. Focus on WHAT COMES NEXT, not what just happened.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"debugging", "coding", "analysis", "planning", "discussion", "review"},
					"description": "Current interaction mode",
				},
				"next_step": map[string]any{
					"type":        "string",
					"description": "What the user likely expects next (max 1 sentence)",
				},
				"open_decision": map[string]any{
					"type":        "string",
					"description": "Unresolved decision or question, if any (empty string if none)",
				},
				"user_tone": map[string]any{
					"type":        "string",
					"enum":        []string{"focused", "frustrated", "exploratory", "directive", "reflective"},
					"description": "User's current communication tone",
				},
			},
			"required": []string{"mode", "next_step"},
		},
	}
}

func (h *selfPrimeHandler) SystemInstruction() string {
	return "- _signal_self_prime: Capture what comes NEXT, not what happened. Report current mode, the likely next step, any open decision, and user tone."
}

func (h *selfPrimeHandler) ShouldActivate(_ RequestContext) bool {
	return false // DISABLED — see comment block above
}

func (h *selfPrimeHandler) HandleResult(ctx RequestContext, call ToolCallResult) {
	// Build structured anchor from individual fields
	mode, _ := call.Input["mode"].(string)
	nextStep, _ := call.Input["next_step"].(string)
	openDecision, _ := call.Input["open_decision"].(string)
	userTone, _ := call.Input["user_tone"].(string)

	if mode == "" && nextStep == "" {
		// Fallback: try legacy "anchor" field
		if anchor, ok := call.Input["anchor"].(string); ok && anchor != "" {
			if len(anchor) > 500 {
				anchor = anchor[:500]
			}
			h.setCached(ctx.ThreadID, anchor)
			h.logger.Printf("[signal:self_prime] cached legacy anchor (%d chars) for thread %s", len(anchor), ctx.ThreadID)
			return
		}
		return
	}

	var parts []string
	if mode != "" {
		parts = append(parts, "Modus: "+mode)
	}
	if nextStep != "" {
		parts = append(parts, "Next step: "+nextStep)
	}
	if openDecision != "" {
		parts = append(parts, "Offene Entscheidung: "+openDecision)
	}
	if userTone != "" {
		parts = append(parts, "User-Ton: "+userTone)
	}

	anchor := strings.Join(parts, " · ")
	if len(anchor) > 500 {
		anchor = anchor[:500]
	}
	h.setCached(ctx.ThreadID, anchor)
	h.logger.Printf("[signal:self_prime] cached anchor (%d chars) for thread %s", len(anchor), ctx.ThreadID)
}

// ---------- helpers ----------

func extractIDsFromInput(input map[string]any, key string) []int64 {
	raw, ok := input[key].([]any)
	if !ok {
		return nil
	}
	var ids []int64
	for _, v := range raw {
		switch n := v.(type) {
		case float64:
			ids = append(ids, int64(n))
		case json.Number:
			if i, err := n.Int64(); err == nil {
				ids = append(ids, i)
			}
		}
	}
	return ids
}

func toAnySlice(ids []int64) []any {
	s := make([]any, len(ids))
	for i, id := range ids {
		s[i] = float64(id)
	}
	return s
}

// registerSignalHandlers registers all built-in signal handlers on the bus.
func registerSignalHandlers(bus *SignalBus, logger *log.Logger, call daemonCaller, selfPrimeSetter func(string, string)) {
	bus.Register(&learningUsedHandler{logger: logger, call: call})
	bus.Register(&knowledgeGapHandler{logger: logger, call: call})
	bus.Register(&contradictionHandler{logger: logger, call: call})
	bus.Register(&selfPrimeHandler{logger: logger, setCached: selfPrimeSetter})
}
