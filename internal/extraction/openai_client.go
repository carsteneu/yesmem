package extraction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOpenAIResponsesURL = "https://api.openai.com/v1/responses"

// OpenAIClient is a minimal Responses API client used for extraction workloads.
type OpenAIClient struct {
	apiKey     string
	model      string
	endpoint   string
	name       string
	httpClient *http.Client
}

func NewOpenAIClient(apiKey, model, baseURL, name string) *OpenAIClient {
	if name == "" {
		name = "openai"
	}
	return &OpenAIClient{
		apiKey:   apiKey,
		model:    model,
		endpoint: normalizeOpenAIResponsesURL(baseURL),
		name:     name,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (c *OpenAIClient) Name() string  { return c.name }
func (c *OpenAIClient) Model() string { return c.model }

func (c *OpenAIClient) Complete(system, userMessage string, opts ...CallOption) (string, error) {
	return c.doRequest(system, userMessage, nil, opts...)
}

func (c *OpenAIClient) CompleteJSON(system, userMessage string, schema map[string]any, opts ...CallOption) (string, error) {
	strict := true
	return c.doRequest(system, userMessage, &openAITextConfig{
		Format: &openAIResponseFormat{
			Type:        "json_schema",
			Name:        "yesmem_output",
			Schema:      schema,
			Strict:      &strict,
			Description: "Structured extraction output for yesmem.",
		},
	}, opts...)
}

type openAIResponsesRequest struct {
	Model           string            `json:"model"`
	Input           any               `json:"input"`
	Instructions    string            `json:"instructions,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Store           *bool             `json:"store,omitempty"`
	Text            *openAITextConfig `json:"text,omitempty"`
}

type openAITextConfig struct {
	Format *openAIResponseFormat `json:"format,omitempty"`
}

type openAIResponseFormat struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Description string         `json:"description,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type openAIResponsesResponse struct {
	OutputText string                   `json:"output_text"`
	Output     []openAIResponsesMessage `json:"output"`
	Usage      *openAIResponsesUsage    `json:"usage,omitempty"`
	Error      *openAIErrorEnvelope     `json:"error,omitempty"`
}

type openAIResponsesMessage struct {
	Type    string                  `json:"type"`
	Content []openAIResponsesPart   `json:"content,omitempty"`
}

type openAIResponsesPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type openAIResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type openAIErrorEnvelope struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (c *OpenAIClient) doRequest(system, userMessage string, textCfg *openAITextConfig, opts ...CallOption) (string, error) {
	o := applyOpts(opts)
	store := false
	body := openAIResponsesRequest{
		Model:           c.model,
		Input:           userMessage,
		Instructions:    system,
		MaxOutputTokens: o.maxTokens,
		Store:           &store,
		Text:            textCfg,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var apiResp openAIResponsesResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.StatusCode >= 400 {
		if apiResp.Error != nil {
			return "", fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
		}
		return "", fmt.Errorf("api error: http %d", resp.StatusCode)
	}
	if apiResp.Error != nil {
		return "", fmt.Errorf("api error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	result := strings.TrimSpace(apiResp.OutputText)
	if result == "" {
		var parts []string
		for _, item := range apiResp.Output {
			if item.Type != "message" {
				continue
			}
			for _, part := range item.Content {
				if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
					parts = append(parts, part.Text)
				}
			}
		}
		result = strings.TrimSpace(strings.Join(parts, "\n"))
	}
	if result == "" {
		return "", fmt.Errorf("empty response")
	}

	if OnUsage != nil && apiResp.Usage != nil {
		OnUsage(c.model, apiResp.Usage.InputTokens, apiResp.Usage.OutputTokens)
	}

	return result, nil
}

func normalizeOpenAIResponsesURL(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return defaultOpenAIResponsesURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	switch {
	case strings.HasSuffix(baseURL, "/responses"):
		return baseURL
	case strings.HasSuffix(baseURL, "/v1"):
		return baseURL + "/responses"
	default:
		return baseURL + "/v1/responses"
	}
}
