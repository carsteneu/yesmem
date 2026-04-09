
package locomo

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/carsteneu/yesmem/internal/benchmark"
	"github.com/carsteneu/yesmem/internal/storage"
)

// SearchResult holds a single search hit from the memory system.
type SearchResult struct {
	Content string
	Score   float64
}

// QueryConfig controls how queries are executed against the memory system.
type QueryConfig struct {
	TopK          int
	UseMessages   bool
	MessageLimit  int
	FullContext    bool
	Concurrency   int
	Hybrid        bool           // Use hybrid_search (BM25+vector+RRF) instead of BM25-only
	Tiered        bool           // Use multi-tool tiered search (hybrid+deep+keyword)
	Agentic       bool           // LLM evaluates search results, escalates if insufficient
	AgenticEval   bool           // Agentic benchmark: LLM uses tools to search iteratively
	DaemonClient  *DaemonClient  // Connection to running daemon (fallback when LocalSearcher is nil)
	LocalSearcher *LocalSearcher // Direct local search against bench DB (preferred over DaemonClient)
}

// DefaultQueryConfig returns sensible defaults for query execution.
func DefaultQueryConfig() QueryConfig {
	return QueryConfig{
		TopK:         10,
		UseMessages:  false,
		MessageLimit: 5,
		FullContext:   false,
		Concurrency:  10,
	}
}

// SearchLearnings searches the learning store using BM25 full-text search.
func SearchLearnings(store *storage.Store, project, query string, limit int) ([]SearchResult, error) {
	results, err := store.SearchLearningsBM25Ctx(context.Background(), query, project, "", "", limit)
	if err != nil {
		return nil, fmt.Errorf("search learnings: %w", err)
	}
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, SearchResult{Content: r.Content, Score: r.Score})
	}
	return out, nil
}

// BuildContextFromMessages searches messages via FTS and filters by project prefix.
func BuildContextFromMessages(store *storage.Store, project, query string, limit int) (string, error) {
	results, err := store.SearchMessages(query, limit*3)
	if err != nil {
		return "", fmt.Errorf("search messages: %w", err)
	}

	var parts []string
	count := 0
	for _, r := range results {
		if !strings.HasPrefix(r.SessionID, project) {
			continue
		}
		parts = append(parts, r.Content)
		count++
		if count >= limit {
			break
		}
	}
	return strings.Join(parts, "\n"), nil
}

// FormatContext renders search results as a numbered list.
func FormatContext(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, r.Content)
	}
	return sb.String()
}

// evaluateResults asks the LLM whether search results are sufficient to answer the question.
// Returns true if the LLM thinks it can answer, false if it needs more context.
func evaluateResults(llm benchmark.LLMClient, question string, results []SearchResult) bool {
	if len(results) == 0 {
		return false
	}
	ctx := FormatContext(results)
	prompt := fmt.Sprintf("Can you answer this question based on the context below? Reply only YES or NO.\n\nContext:\n%s\nQuestion: %s", ctx, question)
	resp, err := llm.Complete("Reply only YES or NO. Nothing else.", prompt)
	if err != nil {
		return false
	}
	resp = strings.TrimSpace(strings.ToUpper(resp))
	return strings.HasPrefix(resp, "YES")
}

// agenticQuery implements LLM-driven retrieval escalation:
// 1. Search learnings (hybrid) → LLM evaluates
// 2. If insufficient → search messages (deep_search equivalent) → LLM evaluates
// 3. If still insufficient → keyword reformulation
// Returns combined search results for final answer generation.
func agenticQuery(store *storage.Store, llm benchmark.LLMClient, searcher *LocalSearcher, project, question string, topK int) []SearchResult {
	// Tier 1: Hybrid search on learnings
	results, _ := searcher.HybridSearch(question, project, topK)
	if evaluateResults(llm, question, results) {
		return results
	}

	// Tier 2: Message search (deep_search equivalent)
	msgResults, _ := searcher.SearchMessages(question, project, topK)
	combined := mergeDedup(results, msgResults)
	if evaluateResults(llm, question, combined) {
		return combined
	}

	// Tier 3: Keyword reformulation + learnings
	keywords := extractKeywords(question)
	if keywords != question && keywords != "" {
		kwResults, _ := searcher.HybridSearch(keywords, project, topK)
		combined = mergeDedup(combined, kwResults)
	}

	return combined
}

// AnswerQuestion uses an LLM to answer a question given search results as context.
func AnswerQuestion(llm benchmark.LLMClient, question string, results []SearchResult) (string, error) {
	ctx := FormatContext(results)

	var prompt string
	if ctx == "" {
		prompt = fmt.Sprintf("Answer this question concisely based on your knowledge:\n%s", question)
	} else {
		prompt = fmt.Sprintf("Based on the following context, answer the question concisely.\n\nContext:\n%s\nQuestion: %s", ctx, question)
	}

	answer, err := llm.Complete("Answer questions in 1-3 sentences maximum. Be direct — give the answer immediately, no preamble. If the context contains relevant clues, reason from them rather than saying 'no information'. Never use markdown headings.", prompt)
	if err != nil {
		return "", fmt.Errorf("answer question: %w", err)
	}
	return answer, nil
}

// RunQueries iterates over scored QA pairs, searches memory, and generates answers.
func RunQueries(store *storage.Store, llm benchmark.LLMClient, sample Sample, cfg QueryConfig) ([]QueryResult, error) {
	project := fmt.Sprintf("locomo_%s", sample.ID)
	scored := sample.ScoredQA()
	results := make([]QueryResult, 0, len(scored))

	for i, qa := range scored {
		var searchResults []SearchResult

		if cfg.FullContext {
			// Variant C handled separately via FullContextAnswer
			answer, err := FullContextAnswer(store, llm, project, qa.Question)
			if err != nil {
				return nil, fmt.Errorf("full context answer for %q: %w", qa.Question, err)
			}
			results = append(results, QueryResult{
				SampleID:  sample.ID,
				Question:  qa.Question,
				Gold:      string(qa.Answer),
				Generated: answer,
				Category:  qa.Category,
			})
			continue
		}

		// Agentic mode: LLM uses tools to search iteratively
		if cfg.AgenticEval {
			openaiClient, ok := llm.(benchmark.ToolCapableClient)
			if !ok {
				return nil, fmt.Errorf("agentic-eval requires ToolCapableClient (OpenAI or Anthropic)")
			}
			result, err := AgenticAnswer(openaiClient, qa.Question, cfg.LocalSearcher, project, 5)
			if err != nil {
				log.Printf("  [agentic] warn: %v — falling back to static", err)
				result = AgenticResult{Answer: "No information available."}
			}
			log.Printf("  [query] %d/%d tools=%d q=%s", i+1, len(scored), len(result.ToolCalls), qa.Question[:min(len(qa.Question), 50)])
			results = append(results, QueryResult{
				SampleID:  sample.ID,
				Question:  qa.Question,
				Gold:      string(qa.Answer),
				Generated: result.Answer,
				Category:  qa.Category,
				ToolCalls: result.ToolCalls,
			})
			continue
		}

		if cfg.UseMessages {
			msgCtx, err := BuildContextFromMessages(store, project, qa.Question, cfg.MessageLimit)
			if err != nil {
				return nil, fmt.Errorf("build message context for %q: %w", qa.Question, err)
			}
			if msgCtx != "" {
				searchResults = append(searchResults, SearchResult{Content: msgCtx, Score: 1.0})
			}
		}

		var learningResults []SearchResult
		var err error
		if cfg.LocalSearcher != nil && cfg.Tiered {
			tieredCfg := DefaultTieredConfig()
			tieredCfg.TopK = cfg.TopK
			learningResults = TieredLocalSearch(cfg.LocalSearcher, qa.Question, project, tieredCfg)
		} else if cfg.LocalSearcher != nil && cfg.Hybrid {
			learningResults, err = cfg.LocalSearcher.HybridSearch(qa.Question, project, cfg.TopK)
		} else if cfg.Tiered && cfg.DaemonClient != nil {
			tieredCfg := DefaultTieredConfig()
			tieredCfg.TopK = cfg.TopK
			learningResults = TieredSearch(cfg.DaemonClient, qa.Question, project, tieredCfg)
		} else if cfg.Hybrid && cfg.DaemonClient != nil {
			learningResults, err = cfg.DaemonClient.HybridSearch(qa.Question, project, cfg.TopK)
		} else {
			learningResults, err = SearchLearnings(store, project, qa.Question, cfg.TopK)
		}
		if err != nil {
			return nil, fmt.Errorf("search learnings for %q: %w", qa.Question, err)
		}
		searchResults = append(searchResults, learningResults...)

		answer, err := AnswerQuestion(llm, qa.Question, searchResults)
		if err != nil {
			return nil, fmt.Errorf("answer %q: %w", qa.Question, err)
		}

		results = append(results, QueryResult{
			SampleID:  sample.ID,
			Question:  qa.Question,
			Gold:      string(qa.Answer),
			Generated: answer,
			Category:  qa.Category,
		})
	}

	return results, nil
}

// runSingleQuery executes search + answer for a single QA pair. Used by both sequential and concurrent paths.
func runSingleQuery(store *storage.Store, llm benchmark.LLMClient, project string, sampleID string, qa QA, cfg QueryConfig) (QueryResult, error) {
	if cfg.FullContext {
		log.Printf("  [path] fullContext q=%s", qa.Question[:min(len(qa.Question), 40)])
		answer, err := FullContextAnswer(store, llm, project, qa.Question)
		if err != nil {
			return QueryResult{}, fmt.Errorf("full context answer for %q: %w", qa.Question, err)
		}
		return QueryResult{SampleID: sampleID, Question: qa.Question, Gold: string(qa.Answer), Generated: answer, Category: qa.Category}, nil
	}

	// Agentic mode: LLM uses tools to search iteratively
	if cfg.AgenticEval {
		openaiClient, ok := llm.(benchmark.ToolCapableClient)
		if !ok {
			return QueryResult{}, fmt.Errorf("agentic-eval requires ToolCapableClient")
		}
		result, err := AgenticAnswer(openaiClient, qa.Question, cfg.LocalSearcher, project, 5)
		if err != nil {
			log.Printf("  [agentic] warn: %v — falling back to static", err)
			result = AgenticResult{Answer: "No information available."}
		}
		log.Printf("  [path] agentic tools=%d q=%s", len(result.ToolCalls), qa.Question[:min(len(qa.Question), 40)])
		return QueryResult{SampleID: sampleID, Question: qa.Question, Gold: string(qa.Answer), Generated: result.Answer, Category: qa.Category, ToolCalls: result.ToolCalls}, nil
	}

	var searchResults []SearchResult
	if cfg.UseMessages {
		msgCtx, err := BuildContextFromMessages(store, project, qa.Question, cfg.MessageLimit)
		if err != nil {
			return QueryResult{}, fmt.Errorf("build message context for %q: %w", qa.Question, err)
		}
		if msgCtx != "" {
			searchResults = append(searchResults, SearchResult{Content: msgCtx, Score: 1.0})
		}
	}

	var learningResults []SearchResult
	var lerr error
	if cfg.Agentic && cfg.LocalSearcher != nil {
		log.Printf("  [path] agentic-retrieval q=%s", qa.Question[:min(len(qa.Question), 40)])
		learningResults = agenticQuery(store, llm, cfg.LocalSearcher, project, qa.Question, cfg.TopK)
	} else if cfg.LocalSearcher != nil && cfg.Tiered {
		log.Printf("  [path] tiered q=%s", qa.Question[:min(len(qa.Question), 40)])
		tieredCfg := DefaultTieredConfig()
		tieredCfg.TopK = cfg.TopK
		learningResults = TieredLocalSearch(cfg.LocalSearcher, qa.Question, project, tieredCfg)
	} else if cfg.LocalSearcher != nil && cfg.Hybrid {
		log.Printf("  [path] hybrid q=%s", qa.Question[:min(len(qa.Question), 40)])
		learningResults, lerr = cfg.LocalSearcher.HybridSearch(qa.Question, project, cfg.TopK)
	} else if cfg.Tiered && cfg.DaemonClient != nil {
		tieredCfg := DefaultTieredConfig()
		tieredCfg.TopK = cfg.TopK
		learningResults = TieredSearch(cfg.DaemonClient, qa.Question, project, tieredCfg)
	} else if cfg.Hybrid && cfg.DaemonClient != nil {
		learningResults, lerr = cfg.DaemonClient.HybridSearch(qa.Question, project, cfg.TopK)
	} else {
		learningResults, lerr = SearchLearnings(store, project, qa.Question, cfg.TopK)
	}
	if lerr != nil {
		return QueryResult{}, fmt.Errorf("search learnings for %q: %w", qa.Question, lerr)
	}
	searchResults = append(searchResults, learningResults...)

	answer, err := AnswerQuestion(llm, qa.Question, searchResults)
	if err != nil {
		return QueryResult{}, fmt.Errorf("answer %q: %w", qa.Question, err)
	}

	return QueryResult{SampleID: sampleID, Question: qa.Question, Gold: string(qa.Answer), Generated: answer, Category: qa.Category}, nil
}

// RunQueriesConcurrent runs queries with N concurrent workers. Maintains result order.
// progress is an optional shared atomic counter for cross-sample progress logging (pass nil to disable).
func RunQueriesConcurrent(store *storage.Store, llm benchmark.LLMClient, sample Sample, cfg QueryConfig, total int, progress *int64) ([]QueryResult, error) {
	project := fmt.Sprintf("locomo_%s", sample.ID)
	scored := sample.ScoredQA()
	results := make([]QueryResult, len(scored))
	errs := make([]error, len(scored))

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency == 1 {
		return RunQueries(store, llm, sample, cfg)
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, qa := range scored {
		wg.Add(1)
		go func(idx int, q QA) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := runSingleQuery(store, llm, project, sample.ID, q, cfg)
			results[idx] = result
			errs[idx] = err

			if progress != nil {
				n := atomic.AddInt64(progress, 1)
				if n%50 == 0 {
					log.Printf("  [query] %d/%d (sample %s)...", n, total, sample.ID)
				}
			}
		}(i, qa)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

// FullContextAnswer sends ALL messages + ALL questions for a project in ONE call.
// Returns a map of question→answer. This is Variant C — no retrieval, full conversation context.
func FullContextAnswer(store *storage.Store, llm benchmark.LLMClient, project, question string) (string, error) {
	// Single-question wrapper for backward compatibility in sequential path.
	answers, err := FullContextAnswerBatch(store, llm, project, []string{question})
	if err != nil {
		return "", err
	}
	if a, ok := answers[question]; ok {
		return a, nil
	}
	return "", fmt.Errorf("no answer for question %q", question)
}

// FullContextAnswerBatch sends the conversation ONCE with all questions batched.
// Returns map[question]answer. One API call per sample instead of one per question.
func FullContextAnswerBatch(store *storage.Store, llm benchmark.LLMClient, project string, questions []string) (map[string]string, error) {
	sessions, err := store.ListAllSessions(project, 100)
	if err != nil {
		return nil, fmt.Errorf("list sessions for %s: %w", project, err)
	}

	var sb strings.Builder
	for _, sess := range sessions {
		msgs, err := store.GetMessagesBySession(sess.ID)
		if err != nil {
			return nil, fmt.Errorf("get messages for session %s: %w", sess.ID, err)
		}
		for _, m := range msgs {
			fmt.Fprintf(&sb, "[%s] %s\n", m.Role, m.Content)
		}
	}

	fullContext := sb.String()
	if fullContext == "" {
		return nil, fmt.Errorf("no messages found for project %s", project)
	}

	// Truncate if context exceeds model limit (~180K tokens, leave room for questions+system)
	const maxContextTokens = 180000
	estimatedTokens := len(fullContext) / 4
	if estimatedTokens > maxContextTokens {
		cutAt := maxContextTokens * 4 // back to chars
		fullContext = fullContext[len(fullContext)-cutAt:]
		log.Printf("  [truncate] %s: %dK→%dK tokens (kept recent)", project, estimatedTokens/1000, maxContextTokens/1000)
	}

	// Build numbered question list
	var qb strings.Builder
	for i, q := range questions {
		fmt.Fprintf(&qb, "%d. %s\n", i+1, q)
	}

	prompt := fmt.Sprintf("Based on the following conversation history, answer each question concisely (1-2 sentences max).\n\nConversation:\n%s\n\nQuestions:\n%s\nAnswer each question on its own line, prefixed with the number. Format:\n1. answer\n2. answer\n...", fullContext, qb.String())

	resp, err := llm.Complete("You are a helpful assistant. Answer each numbered question concisely based only on the provided conversation. One line per answer, prefixed with the question number and a period.", prompt)
	if err != nil {
		return nil, fmt.Errorf("full context batch answer: %w", err)
	}

	// Parse numbered answers
	answers := make(map[string]string, len(questions))
	lines := strings.Split(resp, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Match "N. answer" or "N: answer"
		for i, q := range questions {
			prefix1 := fmt.Sprintf("%d.", i+1)
			prefix2 := fmt.Sprintf("%d:", i+1)
			if strings.HasPrefix(line, prefix1) {
				answers[q] = strings.TrimSpace(strings.TrimPrefix(line, prefix1))
				break
			} else if strings.HasPrefix(line, prefix2) {
				answers[q] = strings.TrimSpace(strings.TrimPrefix(line, prefix2))
				break
			}
		}
	}

	// Fallback: if no numbered answers parsed, use whole response for single question
	// or split by lines for multiple questions
	parsed := 0
	for _, a := range answers {
		if a != "" {
			parsed++
		}
	}
	if parsed == 0 && len(questions) == 1 {
		answers[questions[0]] = strings.TrimSpace(resp)
	} else if parsed == 0 {
		// Best effort: assign non-empty lines to questions in order
		nonEmpty := []string{}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				nonEmpty = append(nonEmpty, line)
			}
		}
		for i, q := range questions {
			if i < len(nonEmpty) {
				answers[q] = nonEmpty[i]
			}
		}
	}

	return answers, nil
}
