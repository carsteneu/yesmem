package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
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

func runGapReview() {
	fs := flag.NewFlagSet("gap-review", flag.ExitOnError)
	project := fs.String("project", "", "filter by project")
	fs.StringVar(project, "p", "", "filter by project (short)")
	dryRun := fs.Bool("dry-run", false, "show gaps without reviewing")
	fs.BoolVar(dryRun, "n", false, "dry-run (short)")
	limit := fs.Int("limit", 50, "max gaps per batch")
	fs.Parse(os.Args[2:])

	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	gaps, err := store.GetUnreviewedGaps(*project, *limit)
	if err != nil {
		log.Fatalf("get gaps: %v", err)
	}

	if len(gaps) == 0 {
		fmt.Fprintln(os.Stderr, "No unreviewed gaps found.")
		return
	}

	fmt.Fprintf(os.Stderr, "Found %d unreviewed gaps", len(gaps))
	if *project != "" {
		fmt.Fprintf(os.Stderr, " (project: %s)", *project)
	}
	fmt.Fprintln(os.Stderr)

	if *dryRun {
		for _, g := range gaps {
			fmt.Fprintf(os.Stderr, "  #%d [%dx] %s\n", g.ID, g.HitCount, truncStr(g.Topic, 100))
		}
		fmt.Fprintf(os.Stderr, "\n--dry-run: no changes made.\n")
		return
	}

	// Build LLM client
	apiKey := cfg.ResolvedAPIKey()
	if apiKey == "" {
		apiKey = daemon.ReadClaudeCodeAPIKey()
	}
	if apiKey == "" {
		log.Fatal("No API key available. Set ANTHROPIC_API_KEY or OPENAI_API_KEY, or configure in config.yaml.")
	}

	client, err := extraction.NewLLMClient(cfg.LLM.Provider, apiKey, cfg.ModelID(), cfg.LLM.ClaudeBinary, cfg.ResolvedOpenAIBaseURL())
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}

	// Build batch prompt with context enrichment
	var lines []string
	for _, g := range gaps {
		results, _ := store.SearchLearningsBM25(g.Topic, g.Project, 3)
		if len(results) > 0 {
			var snippets []string
			for _, r := range results {
				content := r.Content
				if len(content) > 120 {
					content = content[:120] + "..."
				}
				snippets = append(snippets, fmt.Sprintf("[#%s] %s", r.ID, content))
			}
			context := strings.Join(snippets, "; ")
			lines = append(lines, fmt.Sprintf(`{"id": %d, "topic": %q, "project": %q, "hits": %d, "existing_learnings": %q}`,
				g.ID, g.Topic, g.Project, g.HitCount, context))
		} else {
			lines = append(lines, fmt.Sprintf(`{"id": %d, "topic": %q, "project": %q, "hits": %d, "existing_learnings": null}`,
				g.ID, g.Topic, g.Project, g.HitCount))
		}
	}
	userMsg := "Bewerte diese Gaps:\n[\n" + strings.Join(lines, ",\n") + "\n]"

	fmt.Fprintf(os.Stderr, "Sending %d gaps to %s for review...\n", len(gaps), cfg.ModelID())

	response, err := client.Complete(gapReviewSystemPrompt, userMsg)
	if err != nil {
		log.Fatalf("LLM call: %v", err)
	}

	// Parse response
	response = strings.TrimSpace(response)
	// Strip markdown code fences if present
	if strings.HasPrefix(response, "```") {
		lines := strings.Split(response, "\n")
		if len(lines) > 2 {
			response = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	type verdict struct {
		ID      int64  `json:"id"`
		Verdict string `json:"verdict"`
	}
	var verdicts []verdict
	if err := json.Unmarshal([]byte(response), &verdicts); err != nil {
		log.Fatalf("parse LLM response: %v\nRaw: %s", err, response[:min(len(response), 500)])
	}

	kept, resolved, noised := 0, 0, 0
	for _, v := range verdicts {
		switch v.Verdict {
		case "keep":
			store.MarkGapReviewed(v.ID, "keep")
			kept++
		case "resolved":
			store.MarkGapReviewed(v.ID, "resolved")
			resolved++
		case "noise":
			store.DeleteGap(v.ID)
			noised++
		default:
			store.MarkGapReviewed(v.ID, v.Verdict)
		}
	}

	fmt.Fprintf(os.Stderr, "Done: %d kept, %d resolved, %d noise (deleted), %d total reviewed\n", kept, resolved, noised, len(verdicts))
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
