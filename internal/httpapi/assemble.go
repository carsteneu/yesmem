package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/proxy"
)

// AssembleRequest is the input for the POST /api/assemble endpoint.
type AssembleRequest struct {
	SessionID   string `json:"session_id"`
	Project     string `json:"project"`
	Messages    []any  `json:"messages"`
	TokenBudget int    `json:"token_budget,omitempty"`
	KeepRecent  int    `json:"keep_recent,omitempty"`
}

// AssembleResponse is the output of the POST /api/assemble endpoint.
type AssembleResponse struct {
	Messages        []any   `json:"messages"`
	InjectedIDs     []int64 `json:"injected_ids"`
	EstimatedTokens int     `json:"estimated_tokens"`
	BriefingLength  int     `json:"briefing_length"`
}

// handleAssemble runs the full context-assembly pipeline and returns enriched messages.
func (s *Server) handleAssemble(w http.ResponseWriter, r *http.Request) {
	var req AssembleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, `{"error":"messages must not be empty"}`, http.StatusBadRequest)
		return
	}

	// Defaults
	keepRecent := req.KeepRecent
	if keepRecent <= 0 {
		keepRecent = 20
	}
	tokenBudget := req.TokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 180_000
	}

	// Simple token estimator (heuristic, no tokenizer available here)
	estimateFn := func(text string) int {
		return int(float64(len(text)) / 3.6)
	}

	messages := make([]any, len(req.Messages))
	copy(messages, req.Messages)

	originalMessages := make([]any, len(messages))
	copy(originalMessages, messages)

	var allInjectedIDs []int64

	// Step 1: Generate briefing via daemon RPC
	briefingText := ""
	if s.handler != nil {
		resp := s.handler.Handle(RPCRequest{
			Method: "generate_briefing",
			Params: map[string]any{"project": req.Project},
		})
		if resp.Error == "" && len(resp.Result) > 0 {
			var br struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(resp.Result, &br) == nil && br.Text != "" {
				briefingText = br.Text
			} else {
				// Fallback: raw string
				briefingText = strings.Trim(string(resp.Result), `"`)
			}
		}
	}

	// Step 2: Inject briefing as user/assistant message pair after system message (if present)
	if briefingText != "" {
		insertIdx := 0
		if len(messages) > 0 {
			if first, ok := messages[0].(map[string]any); ok {
				if first["role"] == "system" || first["role"] == nil {
					// Check if it looks like a system block
					insertIdx = 1
				}
			}
		}
		briefingPair := []any{
			map[string]any{
				"role":    "user",
				"content": briefingText,
			},
			map[string]any{
				"role":    "assistant",
				"content": "Briefing gelesen.",
			},
		}
		newMessages := make([]any, 0, len(messages)+2)
		newMessages = append(newMessages, messages[:insertIdx]...)
		newMessages = append(newMessages, briefingPair...)
		newMessages = append(newMessages, messages[insertIdx:]...)
		messages = newMessages
	}

	// Step 3: Strip redundant system-reminders
	messages = proxy.StripReminders(messages, len(messages)-1)

	// Step 4: Compress old thinking/tool_results
	compressResult := proxy.CompressContext(messages, keepRecent, req.SessionID, estimateFn)
	messages = compressResult.Messages

	// Step 5: Budget-based cutoff
	cutoff := proxy.CalcCollapseCutoff(messages, keepRecent, tokenBudget, estimateFn)

	// Step 6: Collapse if cutoff > 0
	if cutoff > 0 {
		startTime := time.Time{} // no session start time in stateless handler
		endTime := time.Time{}
		messages = proxy.CollapseOldMessages(messages, originalMessages, cutoff, startTime, endTime, nil, nil, req.SessionID)
	}

	// Step 7: Associative context — hybrid_search on last user message
	if s.handler != nil {
		userQuery := lastUserText(messages)
		if userQuery != "" {
			searchResp := s.handler.Handle(RPCRequest{
				Method: "hybrid_search",
				Params: map[string]any{
					"query":   truncateStr(userQuery, 200),
					"limit":   float64(6),
					"project": req.Project,
				},
			})
			if searchResp.Error == "" && len(searchResp.Result) > 0 {
				injectedIDs := injectAssociativeContext(&messages, searchResp.Result, req.Project)
				allInjectedIDs = append(allInjectedIDs, injectedIDs...)
			}
		}
	}

	// Step 8: Fresh remember — pop recently saved learnings
	// NOTE: Disabled in proxy path (echo-loop: Claude sees own remember() output twice).
	// Kept here for OpenClaw/HTTP-API where remember() has no MCP tool_result feedback.
	// TODO: In OpenClaw, evaluate whether the calling environment provides tool_result
	// feedback for remember(). If yes, this becomes redundant here too.
	if s.handler != nil {
		remResp := s.handler.Handle(RPCRequest{
			Method: "pop_recent_remember",
			Params: map[string]any{},
		})
		if remResp.Error == "" && len(remResp.Result) > 0 {
			injectedIDs := injectFreshRemember(&messages, remResp.Result)
			allInjectedIDs = append(allInjectedIDs, injectedIDs...)
		}
	}

	// Step 9: Inject InlineReflectionHint (Bohrhammer principle)
	injectReflectionHint(&messages)

	// Step 10: Estimate token count
	estimatedTokens := 0
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			estimatedTokens += estimateFn(c)
		case []any:
			for _, block := range c {
				if b, ok := block.(map[string]any); ok {
					if text, ok := b["text"].(string); ok {
						estimatedTokens += estimateFn(text)
					}
				}
			}
		}
	}

	if allInjectedIDs == nil {
		allInjectedIDs = []int64{}
	}

	resp := AssembleResponse{
		Messages:        messages,
		InjectedIDs:     allInjectedIDs,
		EstimatedTokens: estimatedTokens,
		BriefingLength:  len(briefingText),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// lastUserText returns the text content of the last user message.
func lastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "user" {
			continue
		}
		return extractTextFromContent(m["content"])
	}
	return ""
}

// extractTextFromContent extracts plain text from a message content field.
// Handles both string content and []any block arrays.
func extractTextFromContent(content any) string {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var parts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "text" {
				if text, ok := b["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// truncateStr truncates s to at most maxLen runes.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

// injectAssociativeContext parses hybrid_search results, filters by threshold,
// injects above-threshold results into the last user message, and returns injected IDs.
func injectAssociativeContext(messages *[]any, searchResult json.RawMessage, project string) []int64 {
	const threshold = 0.020
	const maxResults = 2

	type hybridResult struct {
		ID      string  `json:"id"`
		Content string  `json:"content"`
		Snippet string  `json:"snippet"`
		Score   float64 `json:"score"`
		Project string  `json:"project"`
	}
	var wrapped struct {
		Results []hybridResult `json:"results"`
	}
	if err := json.Unmarshal(searchResult, &wrapped); err != nil {
		return nil
	}

	var lines []string
	var injectedIDs []int64

	for _, r := range wrapped.Results {
		if len(injectedIDs) >= maxResults {
			break
		}
		if r.Score < threshold {
			continue
		}
		text := r.Content
		if text == "" {
			text = r.Snippet
		}
		if text == "" {
			continue
		}
		var line string
		if r.ID != "" {
			var idVal int64
			if _, err := fmt.Sscanf(r.ID, "%d", &idVal); err == nil && idVal > 0 {
				line = fmt.Sprintf("[ID:%d] %s", idVal, truncateStr(text, 200))
				injectedIDs = append(injectedIDs, idVal)
			} else {
				line = truncateStr(text, 200)
			}
		} else {
			line = truncateStr(text, 200)
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return nil
	}

	contextText := fmt.Sprintf("[yesmem associative context]\n%s\n%s\n[/yesmem context]",
		strings.Join(lines, "\n"),
		proxy.InlineReflectionHint,
	)

	*messages = appendToLastUserMessage(*messages, contextText)
	return injectedIDs
}

// injectFreshRemember parses pop_recent_remember results and injects them above the last user message.
func injectFreshRemember(messages *[]any, result json.RawMessage) []int64 {
	type recentItem struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
	}
	var resp struct {
		Items []recentItem `json:"items"`
	}
	if err := json.Unmarshal(result, &resp); err != nil || len(resp.Items) == 0 {
		return nil
	}

	var lines []string
	var injectedIDs []int64
	for _, item := range resp.Items {
		if item.ID > 0 {
			lines = append(lines, fmt.Sprintf("- [ID:%d] %s", item.ID, truncateStr(item.Text, 200)))
			injectedIDs = append(injectedIDs, item.ID)
		} else {
			lines = append(lines, "- "+truncateStr(item.Text, 200))
		}
	}

	contextText := fmt.Sprintf("[yesmem fresh memory]\nGerade gelernt:\n%s\n%s\n[/yesmem fresh memory]",
		strings.Join(lines, "\n"),
		proxy.InlineReflectionHint,
	)

	*messages = appendToLastUserMessage(*messages, contextText)
	return injectedIDs
}

// appendToLastUserMessage adds a text content block to the last user message.
// Converts string content to content blocks if needed.
func appendToLastUserMessage(messages []any, text string) []any {
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return messages
	}

	msg := messages[lastUserIdx].(map[string]any)
	var blocks []any
	switch c := msg["content"].(type) {
	case string:
		blocks = []any{
			map[string]any{"type": "text", "text": c},
		}
	case []any:
		blocks = make([]any, len(c))
		copy(blocks, c)
	default:
		return messages
	}

	blocks = append(blocks, map[string]any{
		"type": "text",
		"text": "\n" + text,
	})

	newMsg := make(map[string]any, len(msg))
	for k, v := range msg {
		newMsg[k] = v
	}
	newMsg["content"] = blocks

	result := make([]any, len(messages))
	copy(result, messages)
	result[lastUserIdx] = newMsg
	return result
}

// injectReflectionHint appends the Bohrhammer InlineReflectionHint to the last user message.
func injectReflectionHint(messages *[]any) {
	*messages = appendToLastUserMessage(*messages, proxy.InlineReflectionHint)
}
