package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIHandler_NonStreamingRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		// Verify OpenAI format received (not Anthropic)
		if _, ok := req["messages"]; !ok {
			t.Error("no messages in upstream request")
		}
		// Bearer token should be preserved (not converted to x-api-key)
		if r.Header.Get("Authorization") != "Bearer sk-test-123" {
			t.Errorf("Authorization = %q, want Bearer sk-test-123", r.Header.Get("Authorization"))
		}
		// No Anthropic headers
		if r.Header.Get("x-api-key") != "" {
			t.Error("x-api-key should not be set for OpenAI upstream")
		}
		if r.Header.Get("anthropic-version") != "" {
			t.Error("anthropic-version should not be set for OpenAI upstream")
		}

		// Return OpenAI format response
		resp := OpenAIChatResponse{
			ID:     "chatcmpl-123",
			Object: "chat.completion",
			Model:  "gpt-5.4",
			Choices: []OpenAIChoice{{
				Index:        0,
				Message:      OpenAIMessage{Role: "assistant", Content: "Hello from upstream!"},
				FinishReason: "stop",
			}},
			Usage: &OpenAIUsage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	s := &Server{
		cfg: Config{
			TargetURL:       upstream.URL,
			OpenAITargetURL: upstream.URL,
		},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: false,
	}
	body, _ := json.Marshal(oaiReq)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-123")
	w := httptest.NewRecorder()

	s.handleOpenAICompletions(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var oaiResp OpenAIChatResponse
	json.NewDecoder(resp.Body).Decode(&oaiResp)

	if len(oaiResp.Choices) == 0 {
		t.Fatal("no choices in response")
	}
	content, _ := oaiResp.Choices[0].Message.Content.(string)
	if content != "Hello from upstream!" {
		t.Errorf("content = %q", content)
	}
	if oaiResp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", oaiResp.Choices[0].FinishReason)
	}
	if oaiResp.Usage == nil || oaiResp.Usage.TotalTokens != 60 {
		t.Errorf("usage = %+v", oaiResp.Usage)
	}
}

func TestOpenAIHandler_StreamingRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return OpenAI SSE format (not Anthropic)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":" World"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	s := &Server{
		cfg: Config{
			TargetURL:       upstream.URL,
			OpenAITargetURL: upstream.URL,
		},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: true,
	}
	body, _ := json.Marshal(oaiReq)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-123")
	w := httptest.NewRecorder()

	s.handleOpenAICompletions(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	respBody := w.Body.String()
	if !bytes.Contains([]byte(respBody), []byte("data: ")) {
		t.Error("no SSE data lines in response")
	}
	if !bytes.Contains([]byte(respBody), []byte("[DONE]")) {
		t.Error("no [DONE] in response")
	}
	if !bytes.Contains([]byte(respBody), []byte("Hello")) {
		t.Error("no content in response")
	}
}

func TestOpenAIHandler_RoutingDetection(t *testing.T) {
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if !isOpenAIPath(req1) {
		t.Error("expected OpenAI path detection for /v1/chat/completions")
	}

	req2 := httptest.NewRequest("POST", "/v1/messages", nil)
	if isOpenAIPath(req2) {
		t.Error("Anthropic path misdetected as OpenAI")
	}

	req3 := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	if isOpenAIPath(req3) {
		t.Error("GET should not match OpenAI path")
	}
}
