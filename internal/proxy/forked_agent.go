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
	APIFormat   string // "anthropic" (default) or "openai" — controls endpoint and response parsing
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
	// Determine actual model for this fork
	actualModel := cfg.Model
	if actualModel == "" {
		actualModel = extractModelFromBody(ctx.OriginalBody)
	}
	isOpenAI := cfg.APIFormat == "openai"
	if cfg.APIFormat == "" || cfg.APIFormat == "anthropic" {
		if strings.HasPrefix(strings.ToLower(actualModel), "deepseek") {
			isOpenAI = true
		}
	}

	if isOpenAI {
		return buildForkRequestOpenAI(ctx, cfg)
	}
	return buildForkRequestAnthropic(ctx, cfg)
}

// buildForkRequestOpenAI builds a fork request for DeepSeek/OpenAI.
// Preserves byte-identical prefix with main request by using bytes.Replace
// to swap only the messages array, keeping key order and all other fields
// exactly as the original body.
func buildForkRequestOpenAI(ctx ForkContext, cfg ForkConfig) ([]byte, error) {
	// Extract original messages as raw bytes
	var tmp struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(ctx.OriginalBody, &tmp); err != nil {
		return nil, fmt.Errorf("unmarshal to extract messages: %w", err)
	}

	// Parse message list
	var msgList []json.RawMessage
	if err := json.Unmarshal(tmp.Messages, &msgList); err != nil {
		return nil, fmt.Errorf("unmarshal messages: %w", err)
	}
	if msgList == nil {
		return nil, fmt.Errorf("no messages in original request")
	}

	// Append fork prompt as user message
	b, err := json.Marshal(map[string]any{
		"role": "user", "content": cfg.Prompt(ctx),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal fork prompt: %w", err)
	}
	msgList = append(msgList, json.RawMessage(b))

	newMsgs, err := json.Marshal(msgList)
	if err != nil {
		return nil, fmt.Errorf("marshal messages: %w", err)
	}

	// Replace messages in original body — preserves key order and all other fields
	result := bytes.Replace(ctx.OriginalBody, tmp.Messages, newMsgs, 1)

	return result, nil
}

// buildForkRequestAnthropic is the original implementation for Anthropic API forks.
// Maintains the existing behavior: strips anti_distillation/context_management,
// overrides max_tokens. Cache prefix preservation is not needed for Anthropic
// which uses explicit cache_control breakpoints, not automatic prefix hashing.
func buildForkRequestAnthropic(ctx ForkContext, cfg ForkConfig) ([]byte, error) {
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

	// Normalize effort to "high" for fork requests. The original request may
	// carry "xhigh" (e.g. from OpenCode CLI) which DeepSeek and other providers
	// reject with 400: "This model does not support effort level 'xhigh'".
	// "high" is the maximum universally supported level across all providers.
	if oc, ok := req["output_config"].(map[string]any); ok {
		oc["effort"] = "high"
	} else {
		req["output_config"] = map[string]any{"effort": "high"}
	}

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
				s.logger.Printf("[req %d %s] fork %s: gate blocked (tokens=%d, msgs=%d)",
					ctx.ReqIdx, s.version, cfg.Name, ctx.TokensUsed, ctx.MessageCount)
			}
			continue
		}

		reqBody, err := buildForkRequest(ctx, cfg)
		if err != nil {
			s.logger.Printf("[req %d %s] fork %s: build error: %v",
				ctx.ReqIdx, s.version, cfg.Name, err)
			s.forkState.RecordFailure(ctx.ThreadID)
			continue
		}

		actualModel := extractModelFromBody(reqBody)
		isOpenAI := cfg.APIFormat == "openai"
		if cfg.APIFormat == "" || cfg.APIFormat == "anthropic" {
			if strings.HasPrefix(strings.ToLower(actualModel), "deepseek") {
				isOpenAI = true
			}
		}
		endpoint := s.resolveAnthropicTarget(actualModel)
		if isOpenAI {
			endpoint = s.resolveOpenAITarget(actualModel)
		}

		// DeepSeek disk cache takes seconds to persist after request completion.
		// Without a delay the fork fires before the cache unit is written → 0% hit.
		if isOpenAI {
			time.Sleep(30 * time.Second)
		}

		resp, err := s.doForkCall(endpoint, s.cfg.APIKey, ctx.AuthHeader, reqBody, isOpenAI)
		if err != nil {
			s.logger.Printf("%s[req %d %s] fork %s: API error: %v%s",
				colorOrange, ctx.ReqIdx, s.version, cfg.Name, err, colorReset)
			s.forkState.RecordFailure(ctx.ThreadID)
			continue
		}
		resp.SessionID = ctx.SessionID
		resp.Project = ctx.Project
		resp.SourceMsgFrom = 0
		resp.SourceMsgTo = ctx.MessageCount - 1

		s.logger.Printf("[req %d %s] fork %s: %d in / %d cached / %d out tokens",
			ctx.ReqIdx, s.version, cfg.Name,
			resp.Usage.InputTokens, resp.Usage.CacheReadInputTokens, resp.Usage.OutputTokens)

		go s.queryDaemon("track_fork_usage", map[string]any{
			"input_tokens":          resp.Usage.InputTokens,
			"output_tokens":         resp.Usage.OutputTokens,
			"cache_read_tokens":     resp.Usage.CacheReadInputTokens,
			"cache_creation_tokens": resp.Usage.CacheCreationInputTokens,
		})

		if cfg.ParseResult != nil {
			if err := cfg.ParseResult(resp, s); err != nil {
				s.logger.Printf("[req %d %s] fork %s: parse error: %v",
					ctx.ReqIdx, s.version, cfg.Name, err)
			}
		}

		s.forkState.RecordFork(ctx.ThreadID)
	}
}

// doForkCall makes the API call and parses the text response.
// Uses apiKey for x-api-key auth; falls back to origHeaders for subscription forwarding.
// When isOpenAI is true, uses /v1/chat/completions endpoint and OpenAI response format.
// Uses s.httpClient to share the main request's connection pool and DeepSeek KV-cache.
func (s *Server) doForkCall(endpoint, apiKey string, origHeaders http.Header, reqBody []byte, isOpenAI bool) (ForkResponse, error) {
	client := s.httpClient

	var apiURL string
	if isOpenAI {
		apiURL = endpoint + "/v1/chat/completions"
	} else {
		apiURL = endpoint + "/v1/messages"
	}

	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return ForkResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if isOpenAI {
		if origHeaders != nil {
			if auth := origHeaders.Get("Authorization"); auth != "" {
				httpReq.Header.Set("Authorization", auth)
			} else if xKey := origHeaders.Get("X-Api-Key"); xKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+xKey)
			}
		}
		if apiKey != "" && httpReq.Header.Get("Authorization") == "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	} else {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		} else if origHeaders != nil {
			if auth := origHeaders.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				httpReq.Header.Set("x-api-key", strings.TrimPrefix(auth, "Bearer "))
			} else if xKey := origHeaders.Get("X-Api-Key"); xKey != "" {
				httpReq.Header.Set("x-api-key", xKey)
			}
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

	if isOpenAI {
		return parseOpenAIResponse(body)
	}
	return parseAnthropicResponse(body)
}

func parseAnthropicResponse(body []byte) (ForkResponse, error) {
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

func parseOpenAIResponse(body []byte) (ForkResponse, error) {
	type chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			CacheHitTokens   int `json:"prompt_cache_hit_tokens"`
			CacheMissTokens  int `json:"prompt_cache_miss_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	var apiResp chunk

	// Try plain JSON first (stream:false responses).
	err := json.Unmarshal(body, &apiResp)
	if err != nil {
		// SSE mode: collect content from all chunks, usage from last
		bodyStr := string(body)
		var lastErr error
		for _, line := range strings.Split(bodyStr, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := line[6:]
			if payload == "[DONE]" {
				continue
			}
			var ck chunk
			if err2 := json.Unmarshal([]byte(payload), &ck); err2 != nil {
				lastErr = err2
				continue
			}
			// Accumulate delta content
			for _, choice := range ck.Choices {
				apiResp.Choices = append(apiResp.Choices, choice)
			}
			// Usage: only last chunk carries it
			if ck.Usage.PromptTokens > 0 {
				apiResp.Usage = ck.Usage
			}
			if ck.Error != nil {
				apiResp.Error = ck.Error
			}
		}
		if len(apiResp.Choices) == 0 && apiResp.Error == nil {
			if lastErr != nil {
				return ForkResponse{}, fmt.Errorf("unmarshal SSE: %w", lastErr)
			}
			return ForkResponse{}, fmt.Errorf("unmarshal: %w (no JSON)", err)
		}
	}

	if apiResp.Error != nil {
		return ForkResponse{}, fmt.Errorf("api error: %s", apiResp.Error.Message)
	}

	var text string
	for _, choice := range apiResp.Choices {
		if choice.Message.Content != "" {
			text += choice.Message.Content
		} else {
			text += choice.Delta.Content
		}
	}

	return ForkResponse{
		Content: text,
		Usage: ReflectionUsage{
			InputTokens:              apiResp.Usage.PromptTokens,
			OutputTokens:             apiResp.Usage.CompletionTokens,
			CacheReadInputTokens:     apiResp.Usage.CacheHitTokens,
			CacheCreationInputTokens: apiResp.Usage.CacheMissTokens,
		},
	}, nil
}
