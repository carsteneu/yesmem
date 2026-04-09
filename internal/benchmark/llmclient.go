
package benchmark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const maxRetries = 3

// ToolCapableClient extends LLMClient with tool-calling support.
// Both OpenAI and Anthropic adapters implement this for agentic mode.
type ToolCapableClient interface {
	LLMClient
	CompleteWithTools(messages []OpenAIMessage, tools []OpenAITool, toolChoice string) (OpenAIMessage, error)
}

// UsageTracker accumulates token usage across API calls (thread-safe).
type UsageTracker struct {
	InputTokens  int64
	OutputTokens int64
	Calls        int64
	Model        string
}

func NewUsageTracker(model string) *UsageTracker {
	return &UsageTracker{Model: model}
}

func (u *UsageTracker) Add(input, output int) {
	atomic.AddInt64(&u.InputTokens, int64(input))
	atomic.AddInt64(&u.OutputTokens, int64(output))
	atomic.AddInt64(&u.Calls, 1)
}

func (u *UsageTracker) GetInput() int64  { return atomic.LoadInt64(&u.InputTokens) }
func (u *UsageTracker) GetOutput() int64 { return atomic.LoadInt64(&u.OutputTokens) }
func (u *UsageTracker) GetCalls() int64  { return atomic.LoadInt64(&u.Calls) }

func (u *UsageTracker) Cost() float64 {
	pricing, ok := modelPricingMap[u.Model]
	if !ok {
		pricing = [2]float64{1.0, 5.0} // default haiku
	}
	return float64(u.GetInput())/1_000_000*pricing[0] + float64(u.GetOutput())/1_000_000*pricing[1]
}

func (u *UsageTracker) String() string {
	return fmt.Sprintf("%dK in + %dK out = $%.2f (%d calls)",
		u.GetInput()/1000, u.GetOutput()/1000, u.Cost(), u.GetCalls())
}

var modelPricingMap = map[string][2]float64{
	"claude-haiku-4-5-20251001":  {1.00, 5.00},
	"claude-sonnet-4-6":          {3.00, 15.00},
	"claude-opus-4-6":            {5.00, 25.00},
}

// retryableDo executes an HTTP request with retry on transient errors.
func retryableDo(client *http.Client, buildReq func() (*http.Request, error)) ([]byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			if attempt < maxRetries {
				wait := time.Duration(1<<uint(attempt)) * time.Second
				log.Printf("  retry %d/%d after %v: %v", attempt+1, maxRetries, wait, err)
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("api call (after %d retries): %w", maxRetries, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < maxRetries {
				// 429: longer backoff (5s, 15s, 30s) to let rate limit recover
				wait := time.Duration(5*(1<<uint(attempt))) * time.Second
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				// Check for Retry-After header
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if d, err := time.ParseDuration(ra + "s"); err == nil && d > 0 && d < 60*time.Second {
						wait = d
					}
				}
				log.Printf("  retry %d/%d after %v: HTTP %d", attempt+1, maxRetries, wait, resp.StatusCode)
				time.Sleep(wait)
				continue
			}
		}

		if resp.StatusCode != http.StatusOK {
			// Check for overloaded error in body
			if strings.Contains(string(body), "overloaded") && attempt < maxRetries {
				wait := time.Duration(1<<uint(attempt)) * time.Second
				log.Printf("  retry %d/%d after %v: overloaded", attempt+1, maxRetries, wait)
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("api error (HTTP %d): %s", resp.StatusCode, body)
		}
		return body, nil
	}
	return nil, fmt.Errorf("exhausted retries")
}

// LLMClient is the interface for LLM API calls in the benchmark.
type LLMClient interface {
	Complete(system, userMsg string) (string, error)
	Model() string
}

// --- OpenAI Client ---

// OpenAIClient calls OpenAI-compatible chat completion APIs.
type OpenAIClient struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIClient creates an OpenAI-compatible client.
// If baseURL is empty, defaults to "https://api.openai.com/v1".
func NewOpenAIClient(apiKey, model, baseURL string) LLMClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIClient{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Model returns the model identifier.
func (c *OpenAIClient) Model() string { return c.model }

// Complete sends a chat completion request and returns the assistant response.
func (c *OpenAIClient) Complete(system, userMsg string) (string, error) {
	messages := []OpenAIMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: userMsg},
	}
	reqBody := openAIRequest{Model: c.model, Messages: messages}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	body, err := retryableDo(c.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		return req, nil
	})
	if err != nil {
		return "", err
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty response: no choices")
	}
	return apiResp.Choices[0].Message.Content, nil
}

// CompleteWithTools sends a chat completion with tool definitions and returns the full response.
// Used for agentic benchmark mode where the LLM can call search tools.
// toolChoice controls tool usage: "required" forces tool use, "auto" lets the model decide, "" omits it.
func (c *OpenAIClient) CompleteWithTools(messages []OpenAIMessage, tools []OpenAITool, toolChoice string) (OpenAIMessage, error) {
	reqBody := openAIRequest{Model: c.model, Messages: messages, Tools: tools}
	if toolChoice != "" {
		reqBody.ToolChoice = toolChoice
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return OpenAIMessage{}, fmt.Errorf("marshal request: %w", err)
	}

	body, err := retryableDo(c.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		return req, nil
	})
	if err != nil {
		return OpenAIMessage{}, err
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return OpenAIMessage{}, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return OpenAIMessage{}, fmt.Errorf("empty response: no choices")
	}
	return apiResp.Choices[0].Message, nil
}

// OpenAI request/response types

type OpenAIMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type OpenAITool struct {
	Type     string              `json:"type"`
	Function OpenAIToolFunction  `json:"function"`
}

type OpenAIToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type openAIRequest struct {
	Model      string          `json:"model"`
	Messages   []OpenAIMessage `json:"messages"`
	Tools      []OpenAITool    `json:"tools,omitempty"`
	ToolChoice interface{}     `json:"tool_choice,omitempty"`
}

type openAIResponse struct {
	Choices []OpenAIChoice `json:"choices"`
}

type OpenAIChoice struct {
	Message      OpenAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// --- Anthropic Adapter ---

// AnthropicAdapter calls the Anthropic Messages API directly.
type AnthropicAdapter struct {
	apiKey     string
	model      string
	httpClient *http.Client
	Tracker    *UsageTracker
}

// ResolveModel maps short model names to full Anthropic model IDs.
// ResolveModel maps short model names to full Anthropic model IDs.
func ResolveModel(name string) string {
	switch name {
	case "haiku":
		return "claude-haiku-4-5-20251001"
	case "sonnet":
		return "claude-sonnet-4-6"
	case "opus":
		return "claude-opus-4-6"
	default:
		return name // pass through full model IDs
	}
}

// NewAnthropicAdapter creates an Anthropic API client.
// Short names like "haiku", "sonnet", "opus" are resolved to full model IDs.
func NewAnthropicAdapter(apiKey, model string) LLMClient {
	resolved := ResolveModel(model)
	return &AnthropicAdapter{
		apiKey:     apiKey,
		model:      resolved,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		Tracker:    NewUsageTracker(resolved),
	}
}

// Model returns the model identifier.
func (c *AnthropicAdapter) Model() string { return c.model }

// Complete sends a message to the Anthropic Messages API.
func (c *AnthropicAdapter) Complete(system, userMsg string) (string, error) {
	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 4096,
		Messages:  []anthropicMessage{{Role: "user", Content: userMsg}},
	}
	if system != "" {
		reqBody.System = system
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	body, err := retryableDo(c.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		return req, nil
	})
	if err != nil {
		return "", err
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response: no content blocks")
	}
	c.Tracker.Add(apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens)
	return apiResp.Content[0].Text, nil
}

// CompleteWithTools sends a message with tool definitions to the Anthropic Messages API.
// Translates OpenAI tool format to Anthropic format and back.
func (c *AnthropicAdapter) CompleteWithTools(messages []OpenAIMessage, tools []OpenAITool, toolChoice string) (OpenAIMessage, error) {
	// Convert OpenAI messages to Anthropic format
	var system string
	var anthropicMsgs []anthropicMessage
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		if m.Role == "tool" {
			// OpenAI tool result → Anthropic tool_result content block in user message
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role: "user",
				Content: []anthropicMessageContent{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Assistant with tool calls → Anthropic tool_use content blocks
			var blocks []anthropicMessageContent
			if m.Content != "" {
				blocks = append(blocks, anthropicMessageContent{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicMessageContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: "assistant", Content: blocks})
			continue
		}
		anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	// Convert OpenAI tools to Anthropic format
	var anthropicTools []anthropicTool
	for _, t := range tools {
		anthropicTools = append(anthropicTools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 4096,
		System:    system,
		Messages:  anthropicMsgs,
	}
	if len(anthropicTools) > 0 {
		reqBody.Tools = anthropicTools
	}
	// Map tool_choice: "required" → {"type": "any"}, "auto" → {"type": "auto"}
	if toolChoice == "required" {
		reqBody.ToolChoice = map[string]string{"type": "any"}
	} else if toolChoice == "auto" {
		reqBody.ToolChoice = map[string]string{"type": "auto"}
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return OpenAIMessage{}, fmt.Errorf("marshal request: %w", err)
	}

	body, err := retryableDo(c.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		return req, nil
	})
	if err != nil {
		return OpenAIMessage{}, err
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return OpenAIMessage{}, fmt.Errorf("unmarshal response: %w\n%s", err, string(body[:min(len(body), 300)]))
	}
	c.Tracker.Add(apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens)

	// Convert Anthropic response back to OpenAI format
	var result OpenAIMessage
	result.Role = "assistant"
	var toolCalls []OpenAIToolCall
	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			toolCalls = append(toolCalls, OpenAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}
	result.ToolCalls = toolCalls
	return result, nil
}

// Anthropic request/response types

type anthropicMessageContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []anthropicMessageContent
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     string             `json:"system,omitempty"`
	Messages   []anthropicMessage `json:"messages"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice interface{}        `json:"tool_choice,omitempty"`
}

type anthropicResponse struct {
	Content  []anthropicMessageContent `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage    struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}
