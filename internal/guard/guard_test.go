package guard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/carsteneu/yesmem/internal/guardmodel"
)

func resetCache() {
	cacheMu.Lock()
	cache = make(map[string]cacheEntry)
	cacheMu.Unlock()
}

func openAIResolution(url string) *guardmodel.Resolution {
	return &guardmodel.Resolution{
		Model:   "deepseek-v4-flash",
		BaseURL: url,
		APIKey:  "sk-test",
		APIType: guardmodel.APITypeOpenAI,
		Host:    "127.0.0.1",
	}
}

// openAIAnswer is a stand-in endpoint that answers with one decision and counts its calls.
func openAIAnswer(t *testing.T, content string) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("missing or wrong Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing Content-Type header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func bashRequest(command, rules string) Request {
	input, _ := json.Marshal(map[string]string{"command": command})
	return Request{Tool: "Bash", ToolInput: input, Rules: rules}
}

func TestCallModelParsesJSON(t *testing.T) {
	resetCache()
	server, _ := openAIAnswer(t, `{"decision":"SUGGEST","suggestion":"test-skill: use test pattern"}`)
	res := openAIResolution(server.URL)

	decision, _ := callModel(res, res.Model, bashRequest("git push", "Some rules content"), "rules", maxOptionalLevel)

	if decision.Decision != "SUGGEST" || decision.Suggestion != "test-skill: use test pattern" {
		t.Fatalf("decision = %+v, want the model's SUGGEST", decision)
	}
}

func TestCallModelHandlesCodeFences(t *testing.T) {
	resetCache()
	server, _ := openAIAnswer(t, "```json\n{\"decision\":\"PASS\"}\n```")
	res := openAIResolution(server.URL)

	if decision, _ := callModel(res, res.Model, bashRequest("ls", "rules"), "rules", maxOptionalLevel); decision.Decision != "PASS" {
		t.Fatalf("decision = %s, want PASS from a fenced answer", decision.Decision)
	}
}

func TestCallModelRetriesOnceOnFailure(t *testing.T) {
	resetCache()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		count := calls
		mu.Unlock()
		if count == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"decision":"PASS"}`}}},
		})
	}))
	defer server.Close()

	res := openAIResolution(server.URL)
	decision, _ := callModel(res, res.Model, bashRequest("retry-test", "rules"), "rules", maxOptionalLevel)

	if decision.Decision != "PASS" {
		t.Fatalf("decision = %s", decision.Decision)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one failure, one success)", calls)
	}
}

func TestCallModelCachesTheAnswer(t *testing.T) {
	resetCache()
	server, calls := openAIAnswer(t, `{"decision":"PASS"}`)
	res := openAIResolution(server.URL)

	if decision, _ := callModel(res, res.Model, bashRequest("cache-test", "cache rules"), "rules", maxOptionalLevel); decision.Decision != "PASS" {
		t.Fatalf("decision = %s", decision.Decision)
	}
	if decision, _ := callModel(res, res.Model, bashRequest("cache-test", "cache rules"), "rules", maxOptionalLevel); decision.Decision != "PASS" {
		t.Fatalf("second decision = %s", decision.Decision)
	}
	if n := calls(); n != 1 {
		t.Fatalf("calls = %d, want 1: the second call was cached", n)
	}
}

// The Anthropic path is the one the plugin could not speak before: /v1/messages, x-api-key, the system prompt
// in its own field, and the answer under content[0].text.
func TestCallModelSpeaksAnthropic(t *testing.T) {
	resetCache()
	var body map[string]any
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("x-api-key = %q, want the Anthropic key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("an Anthropic request must not carry a Bearer header")
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content":     []map[string]any{{"type": "text", "text": `{"decision":"BLOCK","violations":["Rule 2: secret"]}`}},
			"stop_reason": "end_turn",
		})
	}))
	defer server.Close()

	res := &guardmodel.Resolution{Model: "claude-haiku-5-5", BaseURL: server.URL, APIKey: "sk-ant-test", APIType: guardmodel.APITypeAnthropic}
	decision, _ := callModel(res, res.Model, bashRequest("git push", "rules"), "rules", maxOptionalLevel)

	if path != "/v1/messages" {
		t.Fatalf("path = %s, want /v1/messages", path)
	}
	if decision.Decision != "BLOCK" || len(decision.Violations) != 1 {
		t.Fatalf("decision = %+v, want the model's BLOCK", decision)
	}
	if body["system"] == nil || body["messages"] == nil || body["max_tokens"] == nil || body["temperature"] != float64(0) {
		t.Fatalf("body = %v, want the messages format", body)
	}
	if body["response_format"] != nil || body["thinking"] != nil {
		t.Fatalf("body = %v, OpenAI-only fields must not be sent to Anthropic", body)
	}
}

// One step down the account's haiku list on 404/403: a retired model name is a name, not an outage.
func TestJudgeStepsOneModelDown(t *testing.T) {
	resetCache()
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		models = append(models, body["model"].(string))
		if body["model"] == "claude-haiku-5-5" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"text": `{"decision":"BLOCK","violations":["Rule 2: secret"]}`}},
		})
	}))
	defer server.Close()

	res := &guardmodel.Resolution{
		Model:     "claude-haiku-5-5",
		Fallbacks: []string{"claude-haiku-4-5-20251001"},
		BaseURL:   server.URL,
		APIKey:    "sk-ant-test",
		APIType:   guardmodel.APITypeAnthropic,
	}
	decision := judge(res, bashRequest("git push", "rules"), "rules")

	if decision.Decision != "BLOCK" {
		t.Fatalf("decision = %+v, want the fallback model's BLOCK", decision)
	}
	if len(models) != 2 || models[0] != "claude-haiku-5-5" || models[1] != "claude-haiku-4-5-20251001" {
		t.Fatalf("models = %v, want one step down the list", models)
	}
}

// The hard pre-check needs no credential: a subscription installation without a key must still not pass a
// destructive command. It is the one control that does not depend on anything reachable.
func TestDestructivePrecheckNeedsNoCredential(t *testing.T) {
	resetCache()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")

	for _, command := range []string{"rm -rf /", "rm -rf ~", "psql -c 'DROP TABLE users'"} {
		decision := Check(bashRequest(command, "1. Never auto-commit.\n"))
		if decision.Decision != "BLOCK" || !decision.Enforceable {
			t.Fatalf("%q: decision = %+v, want an enforceable BLOCK without a credential", command, decision)
		}
	}
	// A harmless command still passes: without a credential nothing else can judge it.
	if decision := Check(bashRequest("ls -la", "1. Never auto-commit.\n")); decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS for a harmless call", decision)
	}
}

func TestCheckWithoutRulesIsPass(t *testing.T) {
	decision := Check(Request{Tool: "Write", Rules: ""})
	if decision.Decision != "PASS" || !strings.Contains(decision.Detail, "rules") {
		t.Fatalf("decision = %+v, want PASS with a reason", decision)
	}
}

// A gateway of unknown provenance never receives the DeepSeek-only thinking switch: an endpoint that does not
// know the parameter rejects the whole request, and that rejection used to be read as a PASS (YM-410).
func TestRequestShapeKeepsThinkingOffUnknownHosts(t *testing.T) {
	resetCache()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"decision":"PASS"}`}}},
		})
	}))
	defer server.Close()

	res := &guardmodel.Resolution{
		Model:   "glm-5.2",
		BaseURL: server.URL,
		APIKey:  "sk-test",
		APIType: guardmodel.APITypeOpenAI,
		NPM:     "@ai-sdk/openai-compatible",
	}
	if decision, _ := callModel(res, res.Model, bashRequest("ls", "rules"), "rules", maxOptionalLevel); decision.Decision != "PASS" {
		t.Fatalf("decision = %+v", decision)
	}
	if body["thinking"] != nil {
		t.Fatalf("body = %v, a gateway must not receive the thinking switch", body)
	}
	// The JSON answer format stays: the gateway accepted it.
	if body["response_format"] == nil {
		t.Fatalf("body = %v, want the JSON answer format", body)
	}
}

// A gateway that refuses response_format still gets the call judged: one step down the ladder, not a refusal.
func TestRequestLadderStepsDownOn400(t *testing.T) {
	resetCache()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if body["response_format"] != nil || body["thinking"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": `{"decision":"PASS"}`}}},
		})
	}))
	defer server.Close()

	res := &guardmodel.Resolution{
		Model:   "glm-5.2",
		BaseURL: server.URL,
		APIKey:  "sk-test",
		APIType: guardmodel.APITypeOpenAI,
		NPM:     "@ai-sdk/openai-compatible",
	}
	if decision, _ := callModel(res, res.Model, bashRequest("ls", "rules"), "rules", maxOptionalLevel); decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS at the bottom of the ladder", decision)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3 (both fields, JSON format only, neither)", len(bodies))
	}
	if bodies[0]["response_format"] == nil || bodies[2]["response_format"] != nil {
		t.Fatalf("bodies = %v, want the ladder from both fields to neither", bodies)
	}
}
