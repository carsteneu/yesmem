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

func TestResponsesHandler_NonStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-123" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("x-api-key should not be set")
		}

		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["input"] == nil {
			t.Error("no input in upstream request")
		}

		resp := ResponsesResponse{
			ID:     "resp_123",
			Object: "response",
			Model:  "gpt-5.4",
			Status: "completed",
			Output: []ResponsesOutputItem{{
				Type: "message",
				Role: "assistant",
				Content: []ResponsesContent{{
					Type: "output_text",
					Text: "Hello from Responses API!",
				}},
			}},
			Usage: &ResponsesUsage{InputTokens: 50, OutputTokens: 10, TotalTokens: 60},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	s := &Server{
		cfg:        Config{TargetURL: upstream.URL, OpenAITargetURL: upstream.URL},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":             "gpt-5.4",
		"instructions":      "Be helpful",
		"input":             "Hello",
		"max_output_tokens": 1024,
	})

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-123")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp ResponsesResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "resp_123" {
		t.Errorf("id = %q", resp.ID)
	}
	if len(resp.Output) == 0 {
		t.Fatal("no output")
	}
	if resp.Output[0].Type != "message" {
		t.Errorf("output type = %q", resp.Output[0].Type)
	}
}

func TestResponsesHandler_Streaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		events := []string{
			`{"type":"response.created","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"in_progress"}}`,
			`{"type":"response.output_item.added","item":{"type":"message","role":"assistant"}}`,
			`{"type":"response.content_part.delta","delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"response.content_part.delta","delta":{"type":"text_delta","text":" World"}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	s := &Server{
		cfg:        Config{TargetURL: upstream.URL, OpenAITargetURL: upstream.URL},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":  "gpt-5.4",
		"input":  "Hello",
		"stream": true,
	})

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test-123")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("response.created")) {
		t.Error("no response.created event")
	}
	if !bytes.Contains([]byte(body), []byte("Hello")) {
		t.Error("no content in stream")
	}
}

func TestResponsesHandler_RoutingDetection(t *testing.T) {
	req1 := httptest.NewRequest("POST", "/v1/responses", nil)
	if !isResponsesPath(req1) {
		t.Error("expected Responses path detection for /v1/responses")
	}

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if isResponsesPath(req2) {
		t.Error("Chat Completions misdetected as Responses")
	}

	req3 := httptest.NewRequest("POST", "/v1/messages", nil)
	if isResponsesPath(req3) {
		t.Error("Anthropic misdetected as Responses")
	}

	req4 := httptest.NewRequest("GET", "/v1/responses", nil)
	if isResponsesPath(req4) {
		t.Error("GET should not match Responses path")
	}
}

func TestResponsesHandler_PreservesPassthroughFields(t *testing.T) {
	var receivedReq map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ResponsesResponse{ID: "resp_1", Object: "response"})
	}))
	defer upstream.Close()

	s := &Server{
		cfg:        Config{TargetURL: upstream.URL, OpenAITargetURL: upstream.URL},
		httpClient: http.DefaultClient,
		logger:     log.Default(),
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model":                "gpt-5.4",
		"input":                "Hello",
		"store":                false,
		"previous_response_id": "resp_prev_1",
		"reasoning": map[string]any{
			"effort": "high",
		},
	})

	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleResponses(w, req)

	if receivedReq["store"] != false {
		t.Errorf("store = %v, want false", receivedReq["store"])
	}
	if receivedReq["previous_response_id"] != "resp_prev_1" {
		t.Errorf("previous_response_id = %v", receivedReq["previous_response_id"])
	}
	reasoning, ok := receivedReq["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %#v", receivedReq["reasoning"])
	}
}
