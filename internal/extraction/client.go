package extraction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// OnUsage is called after every successful API call with actual token counts.
// Set by the daemon to feed real usage data into budget tracking.
var OnUsage func(model string, inputTokens, outputTokens int)

// Client is a simple Anthropic Messages API client.
// Implements the LLMClient interface.
type Client struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// Name returns "api" for logging.
func (c *Client) Name() string { return "api" }

// Model returns the full model ID.
func (c *Client) Model() string { return c.model }

// NewClient creates an Anthropic API client.
func NewClient(apiKey, model string) *Client {
	return &Client{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Message represents a message in the API request.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// request is the API request body.
type request struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	Messages     []Message     `json:"messages"`
	System       []systemBlock `json:"system,omitempty"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

// systemBlock is a system prompt block with optional cache control.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl enables prompt caching for a block.
type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// outputConfig controls structured output format.
type outputConfig struct {
	Format *outputFormat `json:"format,omitempty"`
}

// outputFormat specifies the JSON schema for structured output.
type outputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema,omitempty"`
}

// response is the API response body.
type response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
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

// Complete sends a message to the Anthropic API and returns the response text.
func (c *Client) Complete(system string, userMessage string, opts ...CallOption) (string, error) {
	return c.doRequest(system, userMessage, nil, opts...)
}

// CompleteJSON sends a message with structured output (JSON schema enforcement).
// The response is guaranteed to be valid JSON matching the schema.
func (c *Client) CompleteJSON(system string, userMessage string, schema map[string]any, opts ...CallOption) (string, error) {
	oc := &outputConfig{
		Format: &outputFormat{
			Type:   "json_schema",
			Schema: schema,
		},
	}
	return c.doRequest(system, userMessage, oc, opts...)
}

func (c *Client) doRequest(system string, userMessage string, oc *outputConfig, opts ...CallOption) (string, error) {
	o := applyOpts(opts)

	var systemBlocks []systemBlock
	if system != "" {
		systemBlocks = []systemBlock{{
			Type:         "text",
			Text:         system,
			CacheControl: &cacheControl{Type: "ephemeral"},
		}}
	}

	body := request{
		Model:        c.model,
		MaxTokens:    o.maxTokens,
		System:       systemBlocks,
		Messages:     []Message{{Role: "user", Content: userMessage}},
		OutputConfig: oc,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp response
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if apiResp.Error != nil {
		log.Printf("API error response (HTTP %d): %s", resp.StatusCode, string(respBody))
		return "", fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}

	// Report actual token usage to budget tracking
	if OnUsage != nil {
		OnUsage(c.model, apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens)
	}

	return apiResp.Content[0].Text, nil
}
