package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildReflectionRequest(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)
	bus.Register(&selfPrimeHandler{logger: logger, setCached: func(string, string) {}})
	bus.Register(&learningUsedHandler{logger: logger, call: func(string, map[string]any) (json.RawMessage, error) { return nil, nil }})

	s := &Server{
		cfg: Config{
			SignalsModel: "claude-sonnet-4-6",
			APIKey:       "test-key",
		},
		signalBus: bus,
		logger:    logger,
	}

	ctx := ReflectionContext{
		UserQuery:         "Explain the bug",
		AssistantResponse: "The bug is in proxy.go line 42...",
		InjectedLearnings: "[ID:1] SQLite busy fix\n[ID:2] Go PATH issue",
		ReqIdx:            5,
		ThreadID:          "t-123",
		Project:           "memory",
		HasLearnings:      true,
	}

	reqBody, err := s.buildReflectionRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}

	// Must have tools
	tools, ok := parsed["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("expected tools in request")
	}

	// Must have tool_choice auto (not forced)
	tc, ok := parsed["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "auto" {
		t.Fatalf("expected tool_choice auto, got %v", tc)
	}

	// Model must match config
	if parsed["model"] != "claude-sonnet-4-6" {
		t.Errorf("expected sonnet model, got %v", parsed["model"])
	}

	// max_tokens should be small
	if mt, ok := parsed["max_tokens"].(float64); !ok || mt > 2048 {
		t.Errorf("max_tokens should be <=2048, got %v", parsed["max_tokens"])
	}
}

func TestBuildReflectionRequest_NoHandlers(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)
	// Register handler that requires learnings but context has none
	bus.Register(&learningUsedHandler{logger: logger, call: func(string, map[string]any) (json.RawMessage, error) { return nil, nil }})

	s := &Server{
		cfg:       Config{SignalsModel: "test"},
		signalBus: bus,
		logger:    logger,
	}

	ctx := ReflectionContext{
		HasLearnings: false, // learningUsedHandler.ShouldActivate returns false
	}

	_, err := s.buildReflectionRequest(ctx)
	// knowledgeGap and selfPrime are always active, but we only registered learningUsed
	// which needs HasLearnings=true. So with HasLearnings=false, no handlers activate.
	if err == nil {
		t.Error("expected error when no handlers activate")
	}
}

func TestDoReflectionCall_ParsesToolUseAndUsage(t *testing.T) {
	apiResp := `{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "_signal_self_prime", "input": {"anchor": "debugging proxy"}},
			{"type": "tool_use", "id": "tu_2", "name": "_signal_learning_used", "input": {"used_ids": [1], "noise_ids": [2]}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 500, "output_tokens": 100}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("expected x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("expected anthropic-version header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(apiResp))
	}))
	defer srv.Close()

	calls, usage, err := doReflectionCall(srv.URL, "test-key", []byte(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
	if calls[0].Name != "_signal_self_prime" {
		t.Errorf("expected self_prime, got %s", calls[0].Name)
	}
	if calls[1].Name != "_signal_learning_used" {
		t.Errorf("expected learning_used, got %s", calls[1].Name)
	}

	// Verify input parsing
	anchor, ok := calls[0].Input["anchor"].(string)
	if !ok || anchor != "debugging proxy" {
		t.Errorf("expected anchor='debugging proxy', got %v", calls[0].Input["anchor"])
	}

	// Verify usage is returned
	if usage.InputTokens != 500 {
		t.Errorf("expected 500 input tokens, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 100 {
		t.Errorf("expected 100 output tokens, got %d", usage.OutputTokens)
	}
}

func TestDoReflectionCall_IgnoresNonSignalTools(t *testing.T) {
	apiResp := `{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "some_regular_tool", "input": {}},
			{"type": "tool_use", "id": "tu_2", "name": "_signal_self_prime", "input": {"anchor": "test"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(apiResp))
	}))
	defer srv.Close()

	calls, _, err := doReflectionCall(srv.URL, "key", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 signal call (non-signal filtered), got %d", len(calls))
	}
	if calls[0].Name != "_signal_self_prime" {
		t.Errorf("expected self_prime, got %s", calls[0].Name)
	}
}

func TestDoReflectionCall_HandlesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"type":"rate_limit","message":"too fast"}}`))
	}))
	defer srv.Close()

	calls, _, err := doReflectionCall(srv.URL, "key", []byte(`{}`))
	if err == nil {
		t.Error("expected error on 429")
	}
	if len(calls) != 0 {
		t.Error("expected no calls on error")
	}
}

func TestDoReflectionCall_HandlesEmptyResponse(t *testing.T) {
	apiResp := `{
		"content": [{"type": "text", "text": "I reflected but used no tools"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 100, "output_tokens": 20}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(apiResp))
	}))
	defer srv.Close()

	calls, _, err := doReflectionCall(srv.URL, "key", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Errorf("expected 0 calls for text-only response, got %d", len(calls))
	}
}

func TestBuildReflectionRequest_IncludesActiveGaps(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)
	bus.Register(&selfPrimeHandler{logger: logger, setCached: func(string, string) {}})
	bus.Register(&knowledgeGapHandler{logger: logger, call: func(string, map[string]any) (json.RawMessage, error) { return nil, nil }})

	s := &Server{
		cfg:       Config{SignalsModel: "test"},
		signalBus: bus,
		logger:    logger,
	}

	ctx := ReflectionContext{
		UserQuery:         "Where are the stopwords?",
		AssistantResponse: "The stopwords are in briefing.go...",
		ActiveGaps:        []string{"Where stopwords are defined", "Hook trigger logic"},
		ReqIdx:            5,
		ThreadID:          "t-123",
		Project:           "memory",
	}

	reqBody, err := s.buildReflectionRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Parse and check user message contains gaps
	var parsed map[string]any
	if err := json.Unmarshal(reqBody, &parsed); err != nil {
		t.Fatal(err)
	}

	messages := parsed["messages"].([]any)
	userMsg := messages[0].(map[string]any)["content"].(string)

	if !strings.Contains(userMsg, "Open knowledge gaps") {
		t.Error("expected user message to contain 'Open knowledge gaps' section")
	}
	if !strings.Contains(userMsg, "Where stopwords are defined") {
		t.Error("expected user message to contain gap topic")
	}
	if !strings.Contains(userMsg, "Hook trigger logic") {
		t.Error("expected user message to contain second gap topic")
	}
}

func TestBuildReflectionRequest_NoGapsSection(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)
	// Use knowledgeGapHandler (always active) instead of selfPrimeHandler (disabled)
	bus.Register(&knowledgeGapHandler{logger: logger, call: func(string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}})

	s := &Server{
		cfg:       Config{SignalsModel: "test"},
		signalBus: bus,
		logger:    logger,
	}

	ctx := ReflectionContext{
		UserQuery:         "Hello",
		AssistantResponse: "Hi there",
		ActiveGaps:        nil, // no gaps
		ReqIdx:            1,
		ThreadID:          "t-1",
		Project:           "memory",
	}

	reqBody, err := s.buildReflectionRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(reqBody, &parsed)
	messages := parsed["messages"].([]any)
	userMsg := messages[0].(map[string]any)["content"].(string)

	if strings.Contains(userMsg, "Open knowledge gaps") {
		t.Error("should NOT include gaps section when no gaps")
	}
}

func TestKnowledgeGapHandler_ResolvesTopics(t *testing.T) {
	var resolvedTopics []string
	var resolvedProjects []string
	mockCall := func(method string, params map[string]any) (json.RawMessage, error) {
		if method == "resolve_gap" {
			topic, _ := params["topic"].(string)
			project, _ := params["project"].(string)
			resolvedTopics = append(resolvedTopics, topic)
			resolvedProjects = append(resolvedProjects, project)
		}
		return json.RawMessage(`{"status":"ok"}`), nil
	}

	h := &knowledgeGapHandler{
		logger: log.New(io.Discard, "", 0),
		call:   mockCall,
	}

	ctx := RequestContext{Project: "memory", ThreadID: "t-1"}
	call := ToolCallResult{
		Name: "_signal_knowledge_gap",
		Input: map[string]any{
			"resolved_topics": []any{"Where stopwords are defined", "Hook trigger logic"},
		},
	}

	h.HandleResult(ctx, call)

	// Give goroutines time to complete
	// (HandleResult uses go func() internally)
	for i := 0; i < 50; i++ {
		if len(resolvedTopics) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(resolvedTopics) != 2 {
		t.Fatalf("expected 2 resolved topics, got %d", len(resolvedTopics))
	}
	if resolvedTopics[0] != "Where stopwords are defined" {
		t.Errorf("expected first topic 'Where stopwords are defined', got %q", resolvedTopics[0])
	}
	if resolvedProjects[0] != "memory" {
		t.Errorf("expected project 'memory', got %q", resolvedProjects[0])
	}
}
