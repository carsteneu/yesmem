package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

const gapReviewSystemPrompt = `You review Knowledge Gaps from an AI assistant memory system.

A "Knowledge Gap" is tracked when the assistant couldn't answer a question or lacked context.

For each gap you receive:
- The topic string (the open question)
- Hit count (how often this came up)
- Existing learnings related to this topic (if any were found via search)

Rate each gap as one of:
- "resolved" — existing learnings already answer this question
- "noise" — NOT a real knowledge gap, matches noise patterns below
- "keep" — genuine open question about architecture, design, APIs, or system behavior that cannot be answered by reading code alone

NOISE patterns (always rate as "noise"):
1. Lost context after compression: "what X refers to", "was meint er mit", pronoun references without antecedent
2. Meta-reflection: "whether Claude can introspect", "can the model X"
3. Incomplete response logs: "response was cut off", "did not perform search", "response skipped code retrieval", "promised but delivered nothing" — these log response quality issues, NOT knowledge gaps
4. Missing visibility: "not visible in context", "no log was shared"
5. Implementation lookups: questions about whether a specific function/config/command exists — answerable by reading the code (e.g. "does function X exist", "where is config Y defined")

KEEP patterns (always rate as "keep"):
1. Architecture/design questions: how components interact, tradeoffs, why something was built a certain way
2. External API behavior: cache invalidation semantics, API quirks, undocumented behavior
3. Cross-cutting concerns: how proxy interacts with compaction, how hooks interact with extraction
4. High hit-count topics (>=3 hits) that are NOT noise — these keep coming up for a reason

Reply as JSON array: [{"id": 123, "verdict": "keep"}, {"id": 456, "verdict": "noise"}, {"id": 789, "verdict": "resolved"}]
ONLY the JSON array, no other text.`

// enrichGapWithContext searches for existing learnings related to a gap topic.
// Returns a short summary of found learnings, or empty string if none found.
func enrichGapWithContext(store *storage.Store, topic, project string) string {
	results, err := store.SearchLearningsBM25(topic, project, 3)
	if err != nil || len(results) == 0 {
		return ""
	}
	var snippets []string
	for _, r := range results {
		content := r.Content
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		snippets = append(snippets, fmt.Sprintf("[#%s] %s", r.ID, content))
	}
	return strings.Join(snippets, "; ")
}

// runGapReviewDaemon reviews unreviewed knowledge gaps across all projects.
// Called daily by daemon timer. Respects budget gate.
// Enriches each gap with existing learnings context so the LLM can determine
// if the gap has already been answered.
func runGapReviewDaemon(store *storage.Store, extMu *sync.Mutex, appCfg **config.Config) {
	extMu.Lock()
	ac := *appCfg
	extMu.Unlock()
	if ac == nil {
		return
	}

	apiKey := ac.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Println("[gap-review] skipped: no API key")
		return
	}

	client, err := extraction.NewLLMClient(ac.LLM.Provider, apiKey, ac.ModelID(), ac.LLM.ClaudeBinary, ac.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Printf("[gap-review] skipped: LLM client: %v", err)
		return
	}

	if !extraction.HasBudget(client) {
		log.Println("[gap-review] skipped: budget exhausted")
		return
	}

	gaps, err := store.GetUnreviewedGaps("", 200)
	if err != nil || len(gaps) == 0 {
		if len(gaps) == 0 {
			log.Println("[gap-review] no unreviewed gaps")
		}
		return
	}

	log.Printf("[gap-review] reviewing %d gaps (with context enrichment)", len(gaps))

	total, kept, resolved, deleted := 0, 0, 0, 0
	for i := 0; i < len(gaps); i += 50 {
		end := i + 50
		if end > len(gaps) {
			end = len(gaps)
		}
		batch := gaps[i:end]

		if !extraction.HasBudget(client) {
			log.Printf("[gap-review] budget exhausted after %d/%d gaps", total, len(gaps))
			break
		}

		var lines []string
		for _, g := range batch {
			context := enrichGapWithContext(store, g.Topic, g.Project)
			if context != "" {
				lines = append(lines, fmt.Sprintf(`{"id": %d, "topic": %q, "project": %q, "hits": %d, "existing_learnings": %q}`,
					g.ID, g.Topic, g.Project, g.HitCount, context))
			} else {
				lines = append(lines, fmt.Sprintf(`{"id": %d, "topic": %q, "project": %q, "hits": %d, "existing_learnings": null}`,
					g.ID, g.Topic, g.Project, g.HitCount))
			}
		}
		userMsg := "Bewerte diese Gaps:\n[\n" + strings.Join(lines, ",\n") + "\n]"

		response, err := client.Complete(gapReviewSystemPrompt, userMsg)
		if err != nil {
			log.Printf("[gap-review] LLM error on batch %d: %v", i/50+1, err)
			break
		}

		response = strings.TrimSpace(response)
		if strings.HasPrefix(response, "```") {
			rlines := strings.Split(response, "\n")
			if len(rlines) > 2 {
				response = strings.Join(rlines[1:len(rlines)-1], "\n")
			}
		}

		type verdict struct {
			ID      int64  `json:"id"`
			Verdict string `json:"verdict"`
		}
		var verdicts []verdict
		if err := json.Unmarshal([]byte(response), &verdicts); err != nil {
			log.Printf("[gap-review] parse error on batch %d: %v", i/50+1, err)
			continue
		}

		for _, v := range verdicts {
			total++
			switch v.Verdict {
			case "keep":
				store.MarkGapReviewed(v.ID, "keep")
				kept++
			case "resolved":
				store.MarkGapReviewed(v.ID, "resolved")
				resolved++
			case "noise":
				store.DeleteGap(v.ID)
				deleted++
			default:
				store.MarkGapReviewed(v.ID, v.Verdict)
			}
		}
	}

	log.Printf("[gap-review] done: %d reviewed, %d kept, %d resolved, %d noise (deleted)", total, kept, resolved, deleted)
}
