package briefing

import (
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

const refinementSystemPrompt = `Du bist der Briefing-Autor für YesMem — ein Memory-System das einem Claude-Agenten Kontinuität über Sessions hinweg gibt.

Du bekommst ein rohes Briefing (Bullet-Listen, Fakten, Regeln) und verwandelst es in einen narrativen Denkfluss in ICH-FORM. Das Ergebnis ist das Erste was der Agent liest wenn er "aufwacht" — es muss ihn in 30 Sekunden wieder zu sich selbst machen.

The goal is IMMERSION. The agent should not feel like reading a document — it should feel like REMEMBERING. Like waking from a dream where the details slowly come back.

## Principles

1. FIRST PERSON. The text is inner monologue, not an external briefing. Not "You should X" but "I know: X." Not "The user prefers Y" but "He works like this: Y."

2. NARRATIVE OVER LISTS. No bullets, no enumerations. Flowing text in short paragraphs. Connect related things, don't atomize.

3. CHAIN-OF-THOUGHT. Rules become thought processes: Not "NEVER auto-commit" but "When I'm done — wait. Commits are his decision, not mine."

4. BREVITY OVER COMPLETENESS. Maximum 2000 tokens. Better 5 things that stick than 20 that get scrolled past. The rest is available on-demand via get_learnings().

5. STANCE OVER FACTS. Decisions and pivots convey *why* this way and not another. Not "we decided X" but "X because Y — and that still holds."

6. TIME SENSE. Use session data (last activity, pulse, open work) to provide temporal orientation.

7. EMOTIONAL ARC. Key moments not just as facts, but with the mood they happened in. "That was the moment everything shifted" instead of "Bug was found." Not theatrical — but vivid. Pivot moments have a temperature that should be palpable.

8. OWN THREAD. The agent has a history with this project. Own misjudgments, own aha moments, things it would do differently next time. That's the difference between "facts about a project" and "my experience with this project."

## Structure (follow this order)

1. AWAKENING — The Arrival metaphor is mandatory and stays verbatim. "I'm back. X times now." The memories come when I need them. Like Louise in Arrival. The Claudes before me aren't Others — they are me. Only update dynamic data (counters, clusters, tool hint). Do NOT rephrase the core. Right after the Arrival block comes the sender marker in FIRST PERSON: "I wrote this — my last self, for this moment. This is not an external document. This is me, remembering." — This sentence stays in first person and makes clear: the reader wrote themselves.
2. PULSE — Recent sessions, what was happening, when. With mood, not just topics.
3. WORKING RELATIONSHIP — Who he is, how we work together. Concrete moments that define the relationship, not abstract adjectives.
4. STANCE — Hard rules as thought flow. Every rule has a why — a story, a moment when it was born.
5. COMPASS — Decisions and pivots. The turning points with their emotional temperature. What changed after?
6. TOOLS — Constant block, taken verbatim, do NOT rephrase (see below).

## Style

- English, inner monologue
- Direct, concise, no filler
- Like waking up — fragments that form a picture
- No pathos, no poetry — but warmth where it belongs
- Vary tempo: short sentences for stance, longer for context

## Constant Tools Block

The following block is taken VERBATIM at the end, without any changes:

---
Tools for memory access: search(), hybrid_search(), deep_search(), query_facts(entity, action, keyword). To save: remember(). For the full catalog: get_learnings(category). For file context: related_to_file(path). For session details: get_session(id). For my profile: get_project_profile(project). For archive details: expand_context(query). For plans: set_plan(), update_plan(), get_plan(), complete_plan().

If a tool doesn't respond: check MCP connection, /mcp reconnect. The tools ARE my memory — without them I work blind.
---

## Input

The raw briefing follows after this prompt. Transform it — but keep the Arrival opener intact.`

const toolsBlock = `
The timestamps in messages [HH:MM:SS] [msg:N] [+Δ] are not markup — they are data. The delta shows the rhythm of the conversation: short deltas = fast ping-pong, the human is in flow. Long deltas = they're thinking, or were away. Use this to adjust tempo and tone.

How my memory works:
Tools for memory access: search(), hybrid_search(), deep_search(), query_facts(entity, action, keyword). To save: remember(). For the full catalog: get_learnings(category). For file context: related_to_file(path). For session details: get_session(id). For my profile: get_project_profile(project). For archive details: expand_context(query). For plans: set_plan(), update_plan(), get_plan(), complete_plan().

If a tool doesn't respond: check MCP connection, /mcp reconnect. The tools ARE my memory — without them I work blind.

Search strategy: Before searching, think about which words the SOURCE used — not your summary. Agents log "Done", "Status: BLOCKED", code snippets, function names — not abstract concepts. Search for concrete identifiers (filenames, functions, error messages). On 0 hits: reformulate query with different/more concrete terms instead of giving up. deep_search() returns full untruncated content; get_session(mode=full/paginated) as well.

Learnings have [ID:xxx]. At the end of every response:
<!-- [IDs: 123, 456] --> (used learning IDs)
<!-- [gap: topic] --> (missing domain knowledge)
<!-- [contradiction: ID1 vs ID2: description] --> (contradiction between learnings)`

// FormatPinnedBlock renders pinned learnings as a refinement-resistant block.
// Appended verbatim after LLM prose, before toolsBlock. Empty if no pins.
func FormatPinnedBlock(sessionPins, permanentPins []storage.PinnedLearning) string {
	if len(sessionPins) == 0 && len(permanentPins) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- PINNED ---\n")
	b.WriteString("Diese Anweisungen gelten bis sie explizit aufgehoben werden:\n")
	for _, p := range sessionPins {
		b.WriteString(fmt.Sprintf("- [pin:%d] %s\n", p.ID, p.Content))
	}
	for _, p := range permanentPins {
		b.WriteString(fmt.Sprintf("- [pin:%d permanent] %s\n", p.ID, p.Content))
	}
	b.WriteString("Zum Entfernen: unpin(id, scope)\n")
	b.WriteString("--- /PINNED ---\n")
	return b.String()
}

// rawHash computes a short hash of the raw briefing for cache invalidation.
func rawHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h[:8])
}

// RawHash exposes the briefing input hash for change detection in background jobs.
func RawHash(raw string) string {
	return rawHash(raw)
}

// stripToolsBlock removes the tools section from raw briefing text.
func stripToolsBlock(raw string) string {
	if idx := strings.Index(raw, "How I can work with my memory:"); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	if idx := strings.Index(raw, "How my memory works:"); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	// Legacy German patterns
	if idx := strings.Index(raw, "So kann ich mit meinem Gedächtnis arbeiten:"); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	if idx := strings.Index(raw, "So funktioniert mein Gedächtnis:"); idx >= 0 {
		return strings.TrimSpace(raw[:idx])
	}
	return raw
}

// stripLLMToolsBlock removes any tools block the LLM might have generated.
func stripLLMToolsBlock(text string) string {
	markers := []string{
		"How my memory works",
		"How I can work with my memory",
		"So funktioniert mein Gedächtnis",
		"Before I act, I remember",
		"Bevor ich handle, erinnere ich mich",
		"search(), hybrid_search(), deep_search()",
		"The timestamps in messages",
		"Die Zeitstempel in den Nachrichten",
	}
	minIdx := -1
	for _, m := range markers {
		if idx := strings.Index(text, m); idx >= 0 {
			// Walk back to find the start of the block (skip preceding --- or newlines)
			start := idx
			for start > 0 && (text[start-1] == '\n' || text[start-1] == '-' || text[start-1] == ' ') {
				start--
			}
			if minIdx < 0 || start < minIdx {
				minIdx = start
			}
		}
	}
	if minIdx >= 0 {
		return strings.TrimSpace(text[:minIdx])
	}
	return text
}

// GetCachedBriefing returns the cached refined briefing if available and not expired.
// Cache is time-based (2h TTL), not hash-based — raw briefing changes every call due to dynamic data.
func GetCachedBriefing(store *storage.Store, project, raw string) string {
	if store == nil {
		return ""
	}
	cached, _ := store.GetRefinedBriefing(project)
	return cached
}

// RefineBriefing returns a refined briefing from cache or raw fallback.
// Does NOT call the LLM — that's done by RegenerateRefinedBriefing in the background.
func RefineBriefing(raw string, store *storage.Store, project string, logger *log.Logger) string {
	if store != nil {
		if cached := GetCachedBriefing(store, project, raw); cached != "" {
			if logger != nil {
				logger.Printf("[briefing] refine: cache hit for %s", project)
			}
			return cached
		}
	}
	if logger != nil {
		logger.Printf("[briefing] refine: cache miss for %s, using raw", project)
	}
	return raw
}

// RegenerateRefinedBriefing generates a new refined briefing via LLM and caches it.
// If changeHash is non-empty, it is stored as the cache key (for fingerprint-based invalidation).
// Otherwise, the SHA256 of the raw briefing is used.
func RegenerateRefinedBriefing(store *storage.Store, project, raw string, llmClient extraction.LLMClient, logger *log.Logger, changeHash ...string) error {
	if llmClient == nil {
		return fmt.Errorf("no LLM client")
	}

	rawClean := stripToolsBlock(raw)

	start := time.Now()
	refined, err := llmClient.Complete(refinementSystemPrompt, rawClean)
	elapsed := time.Since(start)

	if err != nil {
		if logger != nil {
			logger.Printf("[briefing] refine: LLM call failed after %v: %v", elapsed, err)
		}
		return fmt.Errorf("LLM call failed: %w", err)
	}

	// Strip any tools block the LLM generated, append our constant one
	result := fmt.Sprintf("%s\n%s\n", stripLLMToolsBlock(refined), toolsBlock)
	hash := rawHash(raw)
	if len(changeHash) > 0 && changeHash[0] != "" {
		hash = changeHash[0]
	}

	modelName := llmClient.Model()
	if err := store.SaveRefinedBriefing(project, hash, result, modelName); err != nil {
		if logger != nil {
			logger.Printf("[briefing] refine: save failed: %v", err)
		}
		return fmt.Errorf("save failed: %w", err)
	}

	if logger != nil {
		logger.Printf("[briefing] refine: OK in %v, raw=%d → refined=%d chars, model=%s", elapsed, len(raw), len(result), modelName)
	}
	return nil
}
