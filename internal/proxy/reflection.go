package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ReflectionContext holds the data needed for a signal reflection call.
type ReflectionContext struct {
	UserQuery         string
	AssistantResponse string
	InjectedLearnings string // the briefing learnings block that was injected
	ActiveGaps        []string // open knowledge gap topics for this project
	ReqIdx            int
	ThreadID          string
	Project           string
	HasLearnings      bool
}

// buildReflectionRequest constructs the API request body for the reflection call.
func (s *Server) buildReflectionRequest(ctx ReflectionContext) ([]byte, error) {
	// Get active signal handlers
	reqCtx := RequestContext{
		ReqIdx:       ctx.ReqIdx,
		ThreadID:     ctx.ThreadID,
		Project:      ctx.Project,
		HasLearnings: ctx.HasLearnings,
	}
	activeHandlers := s.signalBus.Evaluate(reqCtx)
	if len(activeHandlers) == 0 {
		return nil, fmt.Errorf("no active signal handlers")
	}

	// Build tool definitions from active handlers
	toolDefs := s.signalBus.BuildToolDefs(activeHandlers)

	// Build user message with context
	var userMsg strings.Builder
	userMsg.WriteString("You just responded to a user. Reflect on your response and use the signal tools.\n\n")
	userMsg.WriteString("## User's question\n")
	userMsg.WriteString(truncateStr(ctx.UserQuery, 2000))
	userMsg.WriteString("\n\n## Your response\n")
	userMsg.WriteString(truncateStr(ctx.AssistantResponse, 4000))
	if ctx.InjectedLearnings != "" {
		userMsg.WriteString("\n\n## Briefing learnings that were injected\n")
		userMsg.WriteString(truncateStr(ctx.InjectedLearnings, 2000))
	}
	if len(ctx.ActiveGaps) > 0 {
		userMsg.WriteString("\n\n## Open knowledge gaps\nIf your response answers any of these, include them in resolved_topics:\n")
		for _, gap := range ctx.ActiveGaps {
			userMsg.WriteString("- ")
			userMsg.WriteString(gap)
			userMsg.WriteString("\n")
		}
	}

	reqBody := map[string]any{
		"model":      s.cfg.SignalsModel,
		"max_tokens": 1024,
		"system":     "You are a reflection agent analyzing a conversation turn. Use signal tools ONLY when genuinely applicable. Do NOT force tool usage — if nothing noteworthy happened, use no tools. Specifically: only report knowledge gaps for missing DOMAIN KNOWLEDGE (not missing conversation context). Only use self_prime when the interaction mode actually shifted.",
		"messages": []map[string]any{
			{"role": "user", "content": userMsg.String()},
		},
		"tools":       toolDefs,
		"tool_choice": map[string]any{"type": "auto"},
	}

	return json.Marshal(reqBody)
}

// reflectionResponse represents the relevant parts of the API response.
type reflectionResponse struct {
	Content []struct {
		Type  string         `json:"type"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ReflectionUsage holds token counts from the reflection API call.
type ReflectionUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// doReflectionCall makes the HTTP request to Anthropic and parses tool_use blocks.
func doReflectionCall(endpoint, apiKey string, reqBody []byte) ([]ToolCallResult, ReflectionUsage, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	url := endpoint + "/v1/messages"
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, ReflectionUsage{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return nil, ReflectionUsage{}, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ReflectionUsage{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, ReflectionUsage{}, fmt.Errorf("api error %d: %s", resp.StatusCode, preview)
	}

	var apiResp reflectionResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, ReflectionUsage{}, fmt.Errorf("unmarshal: %w", err)
	}

	if apiResp.Error != nil {
		return nil, ReflectionUsage{}, fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	usage := ReflectionUsage{
		InputTokens:  apiResp.Usage.InputTokens,
		OutputTokens: apiResp.Usage.OutputTokens,
	}

	// Extract tool_use blocks
	var calls []ToolCallResult
	for _, block := range apiResp.Content {
		if block.Type == "tool_use" && IsSignalTool(block.Name) {
			input := block.Input
			if input == nil {
				input = map[string]any{}
			}
			calls = append(calls, ToolCallResult{
				ID:    block.ID,
				Name:  block.Name,
				Input: input,
			})
		}
	}

	return calls, usage, nil
}

// fireReflectionCall is the async entry point called from forwardWithAnnotation.
// It builds the request, calls the API, and routes results through the signal bus.
func (s *Server) fireReflectionCall(ctx ReflectionContext) {
	// Fetch active gaps for this project to include in the reflection context
	if ctx.Project != "" {
		if raw, err := s.queryDaemon("get_active_gaps", map[string]any{"project": ctx.Project, "limit": 10}); err == nil {
			var gapResp struct {
				Gaps []struct {
					Topic string `json:"topic"`
				} `json:"gaps"`
			}
			if json.Unmarshal(raw, &gapResp) == nil {
				for _, g := range gapResp.Gaps {
					ctx.ActiveGaps = append(ctx.ActiveGaps, g.Topic)
				}
			}
		}
	}

	reqBody, err := s.buildReflectionRequest(ctx)
	if err != nil {
		s.logger.Printf("[req %d] reflection: skip (%v)", ctx.ReqIdx, err)
		return
	}

	// Always use the real Anthropic endpoint for reflection calls
	endpoint := "https://api.anthropic.com"

	calls, usage, err := doReflectionCall(endpoint, s.cfg.APIKey, reqBody)
	if err != nil {
		s.logger.Printf("%s[req %d] reflection: API error: %v%s", colorOrange, ctx.ReqIdx, err, colorReset)
		return
	}

	s.logger.Printf("[req %d] reflection: %d signals, %d in / %d out tokens, model=%s",
		ctx.ReqIdx, len(calls), usage.InputTokens, usage.OutputTokens, s.cfg.SignalsModel)

	// Route through existing signal bus handlers.
	busCtx := RequestContext{
		ReqIdx:       ctx.ReqIdx,
		ThreadID:     ctx.ThreadID,
		Project:      ctx.Project,
		HasLearnings: ctx.HasLearnings,
	}
	for _, call := range calls {
		if routed := s.signalBus.RouteToolCall(busCtx, call); routed {
			s.logger.Printf("[req %d] reflection signal dispatched: %s", ctx.ReqIdx, call.Name)
		}
	}
}
