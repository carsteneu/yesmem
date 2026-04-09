
package benchmark

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIClientComplete(t *testing.T) {
	// Mock OpenAI-compatible server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key-123" {
			t.Errorf("expected Authorization 'Bearer test-key-123', got %q", auth)
		}

		// Verify content type
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got %q", ct)
		}

		// Verify method and path
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}

		// Parse and verify request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var reqBody struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		if reqBody.Model != "gpt-4o" {
			t.Errorf("expected model 'gpt-4o', got %q", reqBody.Model)
		}

		// Verify system message
		if len(reqBody.Messages) < 2 {
			t.Fatalf("expected at least 2 messages (system+user), got %d", len(reqBody.Messages))
		}
		if reqBody.Messages[0].Role != "system" {
			t.Errorf("expected first message role 'system', got %q", reqBody.Messages[0].Role)
		}
		if reqBody.Messages[0].Content != "You are a helpful assistant." {
			t.Errorf("unexpected system message: %q", reqBody.Messages[0].Content)
		}
		if reqBody.Messages[1].Role != "user" {
			t.Errorf("expected second message role 'user', got %q", reqBody.Messages[1].Role)
		}
		if reqBody.Messages[1].Content != "Hello" {
			t.Errorf("unexpected user message: %q", reqBody.Messages[1].Content)
		}

		// Return valid OpenAI response
		resp := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"model":   "gpt-4o",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "Hello back!"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAIClient("test-key-123", "gpt-4o", server.URL)

	// Verify Model()
	if client.Model() != "gpt-4o" {
		t.Errorf("Model() = %q, want 'gpt-4o'", client.Model())
	}

	// Test Complete
	result, err := client.Complete("You are a helpful assistant.", "Hello")
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if result != "Hello back!" {
		t.Errorf("Complete() = %q, want 'Hello back!'", result)
	}
}

func TestOpenAIClientError(t *testing.T) {
	// Mock server returning 429 rate limit
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		resp := map[string]any{
			"error": map[string]any{
				"message": "Rate limit exceeded",
				"type":    "rate_limit_error",
				"code":    "rate_limit_exceeded",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewOpenAIClient("test-key", "gpt-4o", server.URL)

	_, err := client.Complete("system", "hello")
	if err == nil {
		t.Fatal("expected error for 429 response, got nil")
	}

	// Verify error message contains useful info
	errMsg := err.Error()
	if errMsg == "" {
		t.Error("error message should not be empty")
	}
}

func TestOpenAIClientDefaultBaseURL(t *testing.T) {
	client := NewOpenAIClient("key", "model", "")
	oc := client.(*OpenAIClient)
	if oc.baseURL != "https://api.openai.com/v1" {
		t.Errorf("default baseURL = %q, want 'https://api.openai.com/v1'", oc.baseURL)
	}
}

func TestLLMClientInterface(t *testing.T) {
	// Compile-time check that both types implement LLMClient
	var _ LLMClient = (*OpenAIClient)(nil)
	var _ LLMClient = (*AnthropicAdapter)(nil)
}
