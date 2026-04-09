package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildForkRequest_ClonesPrefix(t *testing.T) {
	originalBody := []byte(`{
		"model": "claude-opus-4-6",
		"max_tokens": 8192,
		"stream": true,
		"system": [{"type": "text", "text": "You are helpful."}],
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"}
		],
		"tools": [{"name": "Bash"}]
	}`)

	cfg := ForkConfig{
		Name:      "test_fork",
		Model:     "claude-sonnet-4-6",
		MaxTokens: 2048,
		Prompt: func(ctx ForkContext) string {
			return "Extract learnings from this conversation."
		},
	}

	ctx := ForkContext{
		OriginalBody:      originalBody,
		AssistantResponse: "Hi there",
		LastExtractedIdx:  0,
	}

	result, err := buildForkRequest(ctx, cfg)
	if err != nil {
		t.Fatalf("buildForkRequest error: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(result, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Model overridden
	if req["model"] != "claude-sonnet-4-6" {
		t.Errorf("expected model claude-sonnet-4-6, got %v", req["model"])
	}

	// Stream disabled
	if req["stream"] != false {
		t.Errorf("expected stream=false, got %v", req["stream"])
	}

	// Max tokens overridden
	if int(req["max_tokens"].(float64)) != 2048 {
		t.Errorf("expected max_tokens=2048, got %v", req["max_tokens"])
	}

	// Tools preserved (byte-identical prefix for cache hit with main thread)
	if req["tools"] == nil {
		t.Error("tools must be preserved for cache prefix compatibility")
	}

	// Messages: original + assistant response + fork prompt
	msgs := req["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}

	// Last message is the fork prompt
	lastMsg := msgs[3].(map[string]any)
	if lastMsg["role"] != "user" {
		t.Errorf("expected last role=user, got %v", lastMsg["role"])
	}

	content := lastMsg["content"].(string)
	if content != "Extract learnings from this conversation." {
		t.Errorf("unexpected fork prompt: %s", content)
	}

	// System prompt preserved (byte-identical for cache hit)
	if req["system"] == nil {
		t.Error("system prompt should be preserved")
	}
}

func TestBuildForkRequest_StripsAntiDistillation(t *testing.T) {
	originalBody := []byte(`{
		"model": "claude-opus-4-6",
		"max_tokens": 8192,
		"stream": true,
		"anti_distillation": ["fake_tools"],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)

	cfg := ForkConfig{
		Name:      "test",
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		Prompt:    func(ctx ForkContext) string { return "test" },
	}

	result, err := buildForkRequest(ForkContext{OriginalBody: originalBody}, cfg)
	if err != nil {
		t.Fatalf("buildForkRequest error: %v", err)
	}

	var req map[string]any
	json.Unmarshal(result, &req)

	if req["anti_distillation"] != nil {
		t.Error("anti_distillation should be stripped from fork request")
	}
}

func TestForkEndToEnd_MockedAPI(t *testing.T) {
	// Test the full flow: buildForkRequest → gate check → prompt with injected IDs → parse response
	originalBody := []byte(`{
		"model": "claude-opus-4-6",
		"max_tokens": 8192,
		"stream": true,
		"system": [{"type": "text", "text": "You are helpful."}],
		"messages": [
			{"role": "user", "content": "How does the proxy work?"},
			{"role": "assistant", "content": "The proxy intercepts requests and manages context."}
		]
	}`)

	cfg := NewExtractAndEvaluateConfig("claude-sonnet-4-6")
	ctx := ForkContext{
		OriginalBody:      originalBody,
		AssistantResponse: "The proxy intercepts requests and manages context.",
		CacheReadTokens:   50000,
		LastExtractedIdx:  0,
		InjectedIDs:       map[int64]string{42: "briefing"},
		Project:           "yesmem",
	}

	// Gate should pass (CacheReadTokens > 0)
	if !cfg.Gate(ctx) {
		t.Error("gate should pass with cache tokens > 0")
	}

	// Build request
	reqBody, err := buildForkRequest(ctx, cfg)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}

	// Verify request structure
	var req map[string]any
	if err := json.Unmarshal(reqBody, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	msgs := req["messages"].([]any)
	// Original 2 + assistant response + fork prompt = 4
	if len(msgs) != 4 {
		t.Errorf("expected 4 messages, got %d", len(msgs))
	}

	// Verify prompt contains injected IDs
	lastMsg := msgs[3].(map[string]any)
	prompt := lastMsg["content"].(string)
	if !strings.Contains(prompt, "42") {
		t.Error("prompt should reference injected learning ID 42")
	}

	// Simulate a response
	mockResponse := `{
		"learnings": [
			{"content": "Proxy manages context via stubbing and collapsing", "category": "pattern", "entities": ["proxy"]}
		],
		"evaluations": [
			{"learning_id": 42, "verdict": "useful", "reason": "Correctly described proxy architecture", "action": "boost"}
		]
	}`

	result, err := parseExtractionJSON(mockResponse)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(result.Learnings) != 1 {
		t.Errorf("expected 1 learning, got %d", len(result.Learnings))
	}
	if result.Learnings[0].Category != "pattern" {
		t.Errorf("expected category 'pattern', got %q", result.Learnings[0].Category)
	}
	if len(result.Evaluations) != 1 {
		t.Errorf("expected 1 evaluation, got %d", len(result.Evaluations))
	}
	if result.Evaluations[0].Action != "boost" {
		t.Errorf("expected action 'boost', got %q", result.Evaluations[0].Action)
	}
}
