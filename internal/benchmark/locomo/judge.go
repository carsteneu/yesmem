
package locomo

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/carsteneu/yesmem/internal/benchmark"
)

// QueryResult holds the output of querying the memory system for one QA pair.
type QueryResult struct {
	SampleID  string
	Question  string
	Gold      string
	Generated string
	Category  int
	ToolCalls []ToolCallLog // agentic mode only
}

// judgeSystemPromptTolerant is the lenient judge (original).
const judgeSystemPromptTolerant = `You are a judge comparing a generated answer to a gold-standard answer.
Be format-tolerant: "May 7th" and "7 May" mean the same thing. "Paris, France" and "Paris" are equivalent.
A less specific but factually correct answer is CORRECT: "3 pets" matches "Three dogs" because dogs are pets. "a book" matches "Charlotte's Web" only if wrong — but "novels" matches "books".
Focus on whether the core facts are correct, not on exact wording or level of detail.

Respond with JSON: {"reasoning": "<brief explanation>", "label": "CORRECT"} or {"reasoning": "<brief explanation>", "label": "WRONG"}
Nothing else.`

// judgeSystemPrompt is the strict judge — requires specific facts, not vague topic matches.
const judgeSystemPrompt = `You are a strict judge comparing a generated answer to a gold-standard answer.

Rules:
- Format-tolerant: "May 7th" = "7 May", "Paris, France" = "Paris". Different date formats are OK.
- The generated answer must contain the SPECIFIC facts from the gold answer, not just the right topic.
- Vague answers that match the topic but miss specific details are WRONG.
  Example: Gold="Banana split sundae, Peach cobbler" → "baked goods" is WRONG (right topic, missing specifics).
  Example: Gold="Seattle, Chicago, New York" → "Seattle and New York" is WRONG (incomplete list).
  Example: Gold="photography" → "a new creative hobby" is WRONG (too vague).
- Partial credit: if gold has multiple items and the answer contains at least 2/3 of them, it is CORRECT.
- Correct paraphrasing is fine: "adopted a rescue dog" = "got a dog from a shelter" is CORRECT.
- Extra correct information beyond the gold answer does NOT make it wrong.
- If the gold answer is a number, the generated number must match exactly.
- If the gold answer is a date, allow ±1 day tolerance but not more.

Respond with JSON: {"reasoning": "<brief explanation>", "label": "CORRECT"} or {"reasoning": "<brief explanation>", "label": "WRONG"}
Nothing else.`

// judgeResponse is the expected JSON structure from the judge LLM.
type judgeResponse struct {
	Reasoning string `json:"reasoning"`
	Label     string `json:"label"`
}

// JudgeAnswer uses an LLM to judge whether a generated answer matches the gold answer.
func JudgeAnswer(llm benchmark.LLMClient, question, gold, generated string) (JudgeResult, error) {
	userMsg := fmt.Sprintf("Question: %s\nGold answer: %s\nGenerated answer: %s", question, gold, generated)

	response, err := llm.Complete(judgeSystemPrompt, userMsg)
	if err != nil {
		return JudgeResult{}, fmt.Errorf("judge LLM call: %w", err)
	}

	label, reasoning := parseJudgeResponse(response)

	score := 0
	if label == "CORRECT" {
		score = 1
	}

	return JudgeResult{
		Question:  question,
		Gold:      gold,
		Generated: generated,
		Label:     label,
		Score:     score,
		Reasoning: reasoning,
	}, nil
}

// JudgeAll judges a slice of QueryResult items and returns JudgeResult for each.
func JudgeAll(llm benchmark.LLMClient, queries []QueryResult) ([]JudgeResult, error) {
	results := make([]JudgeResult, 0, len(queries))

	for _, q := range queries {
		r, err := JudgeAnswer(llm, q.Question, q.Gold, q.Generated)
		if err != nil {
			log.Printf("  [judge] warn: skipping %q: %v", q.Question[:min(len(q.Question), 40)], err)
			r = JudgeResult{Question: q.Question, Gold: q.Gold, Generated: q.Generated, Label: "ERROR", Score: 0}
		}
		r.Category = q.Category
		r.ToolCalls = q.ToolCalls
		results = append(results, r)
	}

	return results, nil
}

// JudgeAllConcurrent judges queries with N concurrent workers. Maintains result order.
func JudgeAllConcurrent(llm benchmark.LLMClient, queries []QueryResult, concurrency int) ([]JudgeResult, error) {
	if concurrency <= 1 {
		return JudgeAll(llm, queries)
	}

	results := make([]JudgeResult, len(queries))
	errs := make([]error, len(queries))
	total := len(queries)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var progress int64

	for i, q := range queries {
		wg.Add(1)
		go func(idx int, qr QueryResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r, err := JudgeAnswer(llm, qr.Question, qr.Gold, qr.Generated)
			if err == nil {
				r.Category = qr.Category
				r.ToolCalls = qr.ToolCalls
			}
			results[idx] = r
			errs[idx] = err

			n := atomic.AddInt64(&progress, 1)
			if n%50 == 0 {
				log.Printf("  [judge] %d/%d...", n, total)
			}
		}(i, q)
	}
	wg.Wait()

	skipped := 0
	for i, err := range errs {
		if err != nil {
			log.Printf("  [judge] warn: skipping %q: %v", queries[i].Question[:min(len(queries[i].Question), 40)], err)
			results[i] = JudgeResult{Question: queries[i].Question, Gold: queries[i].Gold, Generated: queries[i].Generated, Label: "ERROR", Score: 0, Category: queries[i].Category}
			skipped++
		}
	}
	if skipped > 0 {
		log.Printf("  [judge] %d/%d skipped due to errors", skipped, len(queries))
	}
	return results, nil
}

// parseJudgeResponse tries JSON parsing first, falls back to keyword detection.
func parseJudgeResponse(response string) (label, reasoning string) {
	// Try JSON parse first.
	var jr judgeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(response)), &jr); err == nil {
		normalized := strings.ToUpper(strings.TrimSpace(jr.Label))
		if normalized == "CORRECT" || normalized == "WRONG" {
			return normalized, jr.Reasoning
		}
	}

	// Fallback: keyword detection in free text.
	upper := strings.ToUpper(response)
	if strings.Contains(upper, "CORRECT") {
		return "CORRECT", response
	}
	if strings.Contains(upper, "WRONG") {
		return "WRONG", response
	}

	// Default to WRONG if we can't determine the label.
	return "WRONG", response
}
