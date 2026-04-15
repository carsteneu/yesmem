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

// ForkConfig defines a fork type with its trigger, prompt, and response handler.
type ForkConfig struct {
	Name        string
	Model       string
	MaxTokens   int
	Prompt      func(ForkContext) string
	ParseResult func(ForkResponse, *Server) error
	Gate        func(ForkContext) bool
}

// PreviousForkLearning is a learning from a prior fork in this session.
type PreviousForkLearning struct {
	Content  string
	Category string
}

// ForkContext holds everything the fork needs from the main request/response cycle.
type ForkContext struct {
	OriginalBody       []byte
	AssistantResponse  string
	ThreadID           string
	Project            string
	ReqIdx             int
	TokensUsed         int
	CacheReadTokens    int
	MessageCount       int
	ToolUseSummary     []string
	InjectedIDs           map[int64]string // learning ID → source
	LastExtractedIdx      int              // message index of last extraction
	HasTrackedDocReads    bool
	PreviousForkLearnings []PreviousForkLearning
	SessionID             string
	AuthHeader            http.Header // original request auth headers for subscription forwarding
}

// ForkResponse holds the parsed fork response.
type ForkResponse struct {
	Content       string
	Usage         ReflectionUsage
	SessionID     string
	Project       string
	SourceMsgFrom int
	SourceMsgTo   int
}

// buildForkRequest clones the original request, appends the fork prompt, and overrides model/stream.
func buildForkRequest(ctx ForkContext, cfg ForkConfig) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(ctx.OriginalBody, &req); err != nil {
		return nil, fmt.Errorf("unmarshal original: %w", err)
	}

	// Get existing messages (byte-identical prefix for cache hit)
	messages, _ := req["messages"].([]any)
	if messages == nil {
		return nil, fmt.Errorf("no messages in original request")
	}

	// Append: assistant response from main conversation + fork task prompt
	if ctx.AssistantResponse != "" {
		messages = append(messages, map[string]any{
			"role":    "assistant",
			"content": ctx.AssistantResponse,
		})
	}
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": cfg.Prompt(ctx),
	})
	req["messages"] = messages

	// Override: model, max_tokens, stream=false (no SSE needed)
	// Empty cfg.Model = keep the model from the original request (same as main thread)
	if cfg.Model != "" {
		req["model"] = cfg.Model
	}
	req["max_tokens"] = cfg.MaxTokens
	req["stream"] = false

	// Keep tools and tool_choice intact — the fork prompt asks for JSON output,
	// not tool calls. Preserving them keeps the cache prefix byte-identical
	// to the main thread request, so Anthropic serves a cache read instead
	// of a full cache write (~$43/day savings at current fork volume).

	// Strip anti-distillation (fake tools would pollute fork response)
	delete(req, "anti_distillation")
	// Strip context_management — rejected by Anthropic API with 400
	delete(req, "context_management")

	return json.Marshal(req)
}

// fireForkedAgents evaluates all fork configs and fires matching ones.
func (s *Server) fireForkedAgents(ctx ForkContext, configs []ForkConfig) {
	// Fetch previous fork learnings for session-aware prompt
	if ctx.SessionID != "" {
		raw, err := s.queryDaemon("get_fork_learnings", map[string]any{"session_id": ctx.SessionID})
		if err == nil && raw != nil {
			var resp struct {
				Learnings []struct {
					Content  string `json:"content"`
					Category string `json:"category"`
				} `json:"learnings"`
			}
			if json.Unmarshal(raw, &resp) == nil {
				for _, l := range resp.Learnings {
					ctx.PreviousForkLearnings = append(ctx.PreviousForkLearnings, PreviousForkLearning{
						Content:  l.Content,
						Category: l.Category,
					})
				}
			}
		}
	}

	debugFork := s.cfg.ForkedAgentsDebug

	for _, cfg := range configs {
		if cfg.Gate != nil && !cfg.Gate(ctx) {
			if debugFork {
				s.logger.Printf("[req %d] fork %s: gate blocked (tokens=%d, msgs=%d)", ctx.ReqIdx, cfg.Name, ctx.TokensUsed, ctx.MessageCount)
			}
			continue
		}

		reqBody, err := buildForkRequest(ctx, cfg)
		if err != nil {
			s.logger.Printf("[req %d] fork %s: build error: %v", ctx.ReqIdx, cfg.Name, err)
			s.forkState.RecordFailure(ctx.ThreadID)
			continue
		}

		endpoint := s.cfg.TargetURL
		resp, err := doForkCall(endpoint, s.cfg.APIKey, ctx.AuthHeader, reqBody)
		if err != nil {
			s.logger.Printf("%s[req %d] fork %s: API error: %v%s", colorOrange, ctx.ReqIdx, cfg.Name, err, colorReset)
			s.forkState.RecordFailure(ctx.ThreadID)
			continue
		}
		resp.SessionID = ctx.SessionID
		resp.Project = ctx.Project
		resp.SourceMsgFrom = 0
		resp.SourceMsgTo = ctx.MessageCount - 1

		s.logger.Printf("[req %d] fork %s: %d in / %d cached / %d out tokens",
			ctx.ReqIdx, cfg.Name, resp.Usage.InputTokens, resp.Usage.CacheReadInputTokens, resp.Usage.OutputTokens)

		go s.queryDaemon("track_fork_usage", map[string]any{
			"input_tokens":          resp.Usage.InputTokens,
			"output_tokens":         resp.Usage.OutputTokens,
			"cache_read_tokens":     resp.Usage.CacheReadInputTokens,
			"cache_creation_tokens": resp.Usage.CacheCreationInputTokens,
		})

		if cfg.ParseResult != nil {
			if err := cfg.ParseResult(resp, s); err != nil {
				s.logger.Printf("[req %d] fork %s: parse error: %v", ctx.ReqIdx, cfg.Name, err)
			}
		}

		s.forkState.RecordFork(ctx.ThreadID)
	}
}

// doForkCall makes the API call and parses the text response.
// Uses apiKey for x-api-key auth; falls back to origHeaders for subscription forwarding.
func doForkCall(endpoint, apiKey string, origHeaders http.Header, reqBody []byte) (ForkResponse, error) {
	client := &http.Client{Timeout: 120 * time.Second}

	apiURL := endpoint + "/v1/messages"
	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return ForkResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	} else if origHeaders != nil {
		// Subscription: OAuth token must be sent as x-api-key, not Authorization Bearer
		if auth := origHeaders.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			httpReq.Header.Set("x-api-key", strings.TrimPrefix(auth, "Bearer "))
		} else if xKey := origHeaders.Get("X-Api-Key"); xKey != "" {
			httpReq.Header.Set("x-api-key", xKey)
		}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return ForkResponse{}, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ForkResponse{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return ForkResponse{}, fmt.Errorf("api error %d: %s", resp.StatusCode, preview)
	}

	var apiResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &apiResp); err != nil {
		return ForkResponse{}, fmt.Errorf("unmarshal: %w", err)
	}

	if apiResp.Error != nil {
		return ForkResponse{}, fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	var text string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	return ForkResponse{
		Content: text,
		Usage: ReflectionUsage{
			InputTokens:              apiResp.Usage.InputTokens,
			OutputTokens:             apiResp.Usage.OutputTokens,
			CacheReadInputTokens:     apiResp.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: apiResp.Usage.CacheCreationInputTokens,
		},
	}, nil
}
