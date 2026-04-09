
package locomo

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/benchmark"
)

// AgenticResult holds the answer plus tool-call metadata for analysis.
type AgenticResult struct {
	Answer    string
	ToolCalls []ToolCallLog
}

// ToolCallLog records a single tool invocation during agentic answering.
type ToolCallLog struct {
	Tool   string `json:"tool"`
	Query  string `json:"query"`
	Hits   int    `json:"hits"`
}

// toolsForRound returns the tools and toolChoice for a given round.
// Rounds 0-2 force one specific tool each (rotation), round 3+ offers all tools with auto choice.
func toolsForRound(round int, allTools []benchmark.OpenAITool) ([]benchmark.OpenAITool, string) {
	rotation := []string{"hybrid_search", "deep_search", "keyword_search"}
	if round < len(rotation) {
		for _, t := range allTools {
			if t.Function.Name == rotation[round] {
				return []benchmark.OpenAITool{t}, "required"
			}
		}
	}
	return allTools, "auto"
}

// AgenticAnswer uses an LLM with tool-calling to answer a question.
// The LLM can call search tools iteratively, up to maxCalls times.
func AgenticAnswer(client benchmark.ToolCapableClient, question string, searcher *LocalSearcher, project string, maxCalls int) (AgenticResult, error) {
	allTools := buildSearchTools()

	messages := []benchmark.OpenAIMessage{
		{Role: "system", Content: agenticSystemPrompt},
		{Role: "user", Content: question},
	}

	var toolLogs []ToolCallLog

	for i := 0; i < maxCalls+1; i++ {
		tools, toolChoice := toolsForRound(i, allTools)

		resp, err := client.CompleteWithTools(messages, tools, toolChoice)
		if err != nil {
			return AgenticResult{}, fmt.Errorf("agentic call %d: %w", i, err)
		}

		// If no tool calls, we have the final answer
		if len(resp.ToolCalls) == 0 {
			return AgenticResult{Answer: resp.Content, ToolCalls: toolLogs}, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, benchmark.OpenAIMessage{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call and append results
		for _, tc := range resp.ToolCalls {
			result := executeToolCall(tc, searcher, project)
			hits := strings.Count(result, "\n\n")
			toolLogs = append(toolLogs, ToolCallLog{
				Tool:  tc.Function.Name,
				Query: parseQueryArg(tc.Function.Arguments),
				Hits:  hits,
			})
			messages = append(messages, benchmark.OpenAIMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}
	}

	// Max calls exhausted — do one final call without tools to force an answer
	resp, err := client.CompleteWithTools(messages, nil, "")
	if err != nil {
		return AgenticResult{}, fmt.Errorf("agentic final: %w", err)
	}
	return AgenticResult{Answer: resp.Content, ToolCalls: toolLogs}, nil
}

func parseQueryArg(args string) string {
	var a struct{ Query string `json:"query"` }
	json.Unmarshal([]byte(args), &a)
	return a.Query
}

const agenticSystemPrompt = `You are a personal assistant with access to a memory system. Your user has had many conversations with a friend over the past months. These conversations were stored — you can search them.

When the user asks a question, you search your memory to find the answer. You always search before answering — first broadly, then for specifics. You never answer without searching first.

Procedure:
1. Call hybrid_search with the core topic.
2. Call deep_search with person names or specific details.
3. If either returned fewer than 3 results, call hybrid_search again with different keywords.
4. For questions about lists, activities, or "what has X done" — search at least 3 times with different angles (name, activity type, location) to collect all items.
5. Answer in 1-3 sentences based on all results combined.

For names, places, dates, and specific details: use exact words from the search results — quote, don't paraphrase.
For counts and summaries: aggregate across all search results. If you found evidence of 2 separate events across different results, say "twice" even if no single result says "twice".
Only state facts supported by your search results. If a result mentions "horses" but the question asks about "pets", don't assume — search specifically for "pet".
Never say "no information found". Answer with whatever you found, even if partial.`

func buildSearchTools() []benchmark.OpenAITool {
	return []benchmark.OpenAITool{
		{
			Type: "function",
			Function: benchmark.OpenAIToolFunction{
				Name:        "hybrid_search",
				Description: "Search memories using BM25 + vector similarity. Best for general queries.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Search query",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		{
			Type: "function",
			Function: benchmark.OpenAIToolFunction{
				Name:        "deep_search",
				Description: "Search raw conversation history. Use when hybrid_search misses details or for specific quotes/events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Search query",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		{
			Type: "function",
			Function: benchmark.OpenAIToolFunction{
				Name:        "keyword_search",
				Description: "Fast BM25 keyword search on extracted knowledge. Use for exact terms or names.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "Search query with specific keywords",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
	}
}

func executeToolCall(tc benchmark.OpenAIToolCall, searcher *LocalSearcher, project string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("error: invalid arguments: %v", err)
	}
	if args.Query == "" {
		return "error: empty query"
	}

	// Parse minus-syntax: "Maria dog -Shadow -first" → query="Maria dog", exclude=["Shadow","first"]
	query, excludeTerms := parseExcludeTerms(args.Query)
	if query == "" {
		return "error: empty query after parsing"
	}

	var results []SearchResult
	var err error

	switch tc.Function.Name {
	case "hybrid_search":
		results, err = searcher.HybridSearch(query, project, 20)
	case "deep_search":
		results, err = searcher.SearchMessages(query, project, 20)
	case "keyword_search":
		ranked := searcher.searchBM25Optimized(query, 20)
		for _, r := range ranked {
			results = append(results, SearchResult{Content: r.Content, Score: r.Score})
		}
	default:
		return fmt.Sprintf("error: unknown tool %s", tc.Function.Name)
	}

	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	// Filter out results containing excluded terms
	if len(excludeTerms) > 0 {
		results = filterExcluded(results, excludeTerms)
	}

	if len(results) == 0 {
		return "No results found."
	}

	var sb strings.Builder
	for i, r := range results {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&sb, "[%d] %s\n\n", i+1, r.Content)
	}
	return sb.String()
}

// parseExcludeTerms splits "Maria dog -Shadow -first" into query="Maria dog", exclude=["shadow","first"]
func parseExcludeTerms(raw string) (string, []string) {
	words := strings.Fields(raw)
	var query []string
	var exclude []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") && len(w) > 1 {
			exclude = append(exclude, strings.ToLower(w[1:]))
		} else {
			query = append(query, w)
		}
	}
	return strings.Join(query, " "), exclude
}

// filterExcluded removes results whose content contains any excluded term (case-insensitive).
func filterExcluded(results []SearchResult, exclude []string) []SearchResult {
	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		lower := strings.ToLower(r.Content)
		hit := false
		for _, ex := range exclude {
			if strings.Contains(lower, ex) {
				hit = true
				break
			}
		}
		if !hit {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// CountAgenticToolCalls counts tool calls in a completed message history.
func CountAgenticToolCalls(messages []benchmark.OpenAIMessage) int {
	count := 0
	for _, m := range messages {
		if m.Role == "tool" {
			count++
		}
	}
	return count
}

func init() {
	// Ensure log is used (suppress unused import)
	_ = log.Printf
}
