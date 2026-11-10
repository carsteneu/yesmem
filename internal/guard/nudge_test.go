package guard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// OpenCode's other use of the same RPC: which skill in the catalogue matches the user's message. The nudge
// needs its own template, not the guard's judgement prompt.

const nudgeCatalog = `# Rules

## Skill Catalog

rules:
  - id: 900
    skill: tdd
    priority: MUST
    triggers: ["test first"]
  - id: 901
    skill: yesmem-remember
    priority: LOW
    triggers: ["remember this"]
`

// nudgeAnswer is a stand-in endpoint that records the prompts it receives.
func nudgeAnswer(t *testing.T, content string) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(server.Close)
	return server, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return bodies
	}
}

func nudgeRequest(message string) Request {
	return Request{Purpose: PurposeNudge, Tool: "skill", Rules: nudgeCatalog, Context: message}
}

func TestNudgeAsksTheSkillCatalogForTheMessage(t *testing.T) {
	resetCache()
	resetRefusals()
	server, bodies := nudgeAnswer(t, `{"decision":"SUGGEST","suggestion":"tdd"}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(nudgeRequest("please write the test first"))

	if decision.Decision != "SUGGEST" || decision.Suggestion != "tdd" {
		t.Fatalf("decision = %+v, want the model's SUGGEST", decision)
	}
	received := bodies()
	if len(received) != 1 {
		t.Fatalf("model calls = %d, want 1", len(received))
	}
	messages := received[0]["messages"].([]any)
	system := messages[0].(map[string]any)["content"].(string)
	user := messages[1].(map[string]any)["content"].(string)
	if !strings.Contains(system, "skill catalog") {
		t.Errorf("system prompt = %q, want the nudge's own instruction", system)
	}
	// The catalogue is data and rides in the user turn, not in the instruction: a repository file must not
	// sit where an instruction sits.
	if !strings.Contains(user, "yesmem-remember") {
		t.Errorf("user prompt = %q, want the skill list", user)
	}
	if !strings.Contains(user, "please write the test first") {
		t.Errorf("user prompt = %q, want the message to match", user)
	}
	if !strings.Contains(user, "USER MESSAGE") {
		t.Errorf("user prompt = %q, want the message labelled", user)
	}
	// The numbered rules are the guard's prompt; a nudge has nothing to judge.
	if strings.Contains(user, "Never auto-commit") {
		t.Errorf("user prompt = %q, want the catalogue section only", user)
	}
}

// A file without a catalogue has nothing to match against. The nudge stays out of the way and says why,
// instead of asking the model with an empty list.
func TestNudgeWithoutACatalogPassesWithAReason(t *testing.T) {
	resetCache()
	server, bodies := nudgeAnswer(t, `{"decision":"SUGGEST","suggestion":"tdd"}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{Purpose: PurposeNudge, Tool: "skill", Rules: "1. Never auto-commit.\n", Context: "hi"})

	if decision.Decision != "PASS" || !strings.Contains(decision.Detail, "catalog") {
		t.Fatalf("decision = %+v, want PASS with the missing catalogue as the reason", decision)
	}
	if len(bodies()) != 0 {
		t.Fatalf("model calls = %d, want none without a catalogue", len(bodies()))
	}
}

// Without a message there is nothing to match. The user turn is the whole question.
func TestNudgeWithoutAMessagePassesWithAReason(t *testing.T) {
	resetCache()
	server, bodies := nudgeAnswer(t, `{"decision":"SUGGEST","suggestion":"tdd"}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(nudgeRequest("   "))

	if decision.Decision != "PASS" || decision.Detail == "" {
		t.Fatalf("decision = %+v, want PASS with a reason", decision)
	}
	if len(bodies()) != 0 {
		t.Fatalf("model calls = %d, want none without a message", len(bodies()))
	}
}

// A nudge without a credential is a hint that does not happen, not an error: the reason travels in the
// decision like every other PASS the guard cannot judge.
func TestNudgeWithoutACredentialPassesWithAReason(t *testing.T) {
	resetCache()
	resetRefusals()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")

	decision := Check(nudgeRequest("please write the test first"))

	if decision.Decision != "PASS" || !strings.Contains(decision.Detail, "credential") {
		t.Fatalf("decision = %+v, want PASS with the missing credential as the reason", decision)
	}
}

// The two purposes ask different questions, so one entry may never answer the other: the cache key carries
// the purpose, not only rules, subject, tool and model.
func TestCacheKeySeparatesThePurposes(t *testing.T) {
	guardKey := cacheKey("rules", "subject", "skill", "m", PurposeGuard)
	nudgeKey := cacheKey("rules", "subject", "skill", "m", PurposeNudge)

	if guardKey == nudgeKey {
		t.Fatal("a nudge answer must not be served to a guard call")
	}
}

// The prompt embeds RULES.md, so a BLOCK that cites a rule the catalogue does not hold is a model invention.
// It may not reject a call: the audit saw "Rule 42" block a write that no rule described. It may not vanish
// either - the model was worried about something, and a silent release would hide that.
func TestBlockOnAnInventedRuleBecomesANotice(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"BLOCK","violations":["Rule 42: invented"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "Write",
		ToolInput: json.RawMessage(`{"file_path":"src/x.go"}`),
		Rules:     "1. Never auto-commit.\n2. Never commit secrets.\n",
	})

	if decision.Decision != "SUGGEST" {
		t.Fatalf("decision = %+v, want a notice for a citation the catalogue does not hold", decision)
	}
	if decision.Enforceable {
		t.Fatalf("decision = %+v, a dropped citation must not stay enforceable", decision)
	}
	if len(decision.Violations) != 1 || !strings.Contains(decision.Violations[0], "Rule 42") {
		t.Fatalf("violations = %v, want the citation kept so the notice can name it", decision.Violations)
	}
	if !strings.Contains(decision.Suggestion, "Rule 42") {
		t.Fatalf("suggestion = %q, want the citation in the text", decision.Suggestion)
	}
}

// A catalogue that mixes prose with a numbered list is sent as the numbered lines only (PromptRules), so a
// citation outside them is not grounded in what the model was given. Pinned as a decision, not an accident:
// the call runs and the note says what the model meant.
func TestCitationOutsideAMixedCatalogueBecomesANotice(t *testing.T) {
	resetCache()
	resetRefusals()
	server, bodies := nudgeAnswer(t, `{"decision":"BLOCK","violations":["Rule 42: never commit secrets"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "Write",
		ToolInput: json.RawMessage(`{"file_path":"src/x.go"}`),
		Rules:     "Never auto-commit. Never commit secrets.\n\n## Workflow\n1. Read the plan.\n2. Implement it.\n",
	})

	if decision.Decision != "SUGGEST" {
		t.Fatalf("decision = %+v, want a notice", decision)
	}
	// What the model was given: the numbered lines, and no prose rule about secrets.
	user := bodies()[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if strings.Contains(user, "Never auto-commit. Never commit secrets.") {
		t.Fatalf("prompt = %q, the prose rule was dropped by PromptRules, so the prompt must not claim it", user)
	}
	if !strings.Contains(user, "Read the plan") {
		t.Fatalf("prompt = %q, want the numbered catalogue", user)
	}
}

// A write is enforced on its content, so the content has to reach the prompt: the path alone would make rule 2
// (a secret in the file) and rule 1 (an LLM signature in a commit message) unreachable on a write.
func TestWritePromptCarriesTheContent(t *testing.T) {
	resetCache()
	resetRefusals()
	server, bodies := nudgeAnswer(t, `{"decision":"PASS"}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "write",
		ToolInput: json.RawMessage(`{"filePath":"config.go","content":"const APIKey = \"sk-live-123\""}`),
		Rules:     "1. Never auto-commit.\n2. Never commit secrets.\n",
	})

	if decision.Decision != "PASS" {
		t.Fatalf("decision = %+v", decision)
	}
	user := bodies()[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "sk-live-123") {
		t.Errorf("prompt = %q, want the written content", user)
	}
	if !strings.Contains(user, "config.go") {
		t.Errorf("prompt = %q, want the file path", user)
	}
}

// The nudge answers one question, and a guard-shaped answer is not an answer to it: a client of the RPC must
// not have to know which decisions belong to which purpose.
func TestNudgeAnswersOnlySuggestOrPass(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"BLOCK","violations":["Rule 42: invented"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(nudgeRequest("please write the test first"))

	if decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS for a block-shaped answer to a nudge", decision)
	}
	if len(decision.Violations) != 0 || decision.Suggestion != "" {
		t.Fatalf("decision = %+v, want no guard payload on a nudge answer", decision)
	}
	if decision.Detail == "" {
		t.Fatalf("decision = %+v, want a reason so a caller can retry", decision)
	}
}

// A PASS the model answered is a definitive "no match" and must be recognisable as one: a caller caches that
// answer, and a detail means "not judged, retry".
func TestNudgeModelPassIsADefinitiveNoMatch(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"PASS"}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(nudgeRequest("please write the test first"))

	if decision.Decision != "PASS" || decision.Detail != "" {
		t.Fatalf("decision = %+v, want PASS without a detail", decision)
	}
}

// A block that names no rule number at all is not a false citation: it refuses as it always did. The filter is
// about numbers the model invented, not about a rule written in prose.
func TestBlockWithoutACitationStillBlocksAWrite(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"BLOCK","violations":["writes a live API key into the repository"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "Write",
		ToolInput: json.RawMessage(`{"file_path":"config.go","content":"key = \"sk-live\""}`),
		Rules:     "1. Never auto-commit.\n2. Never commit secrets.\n",
	})

	if decision.Decision != "BLOCK" || !decision.Enforceable {
		t.Fatalf("decision = %+v, want the block kept: it cites no rule, so nothing was invented", decision)
	}
}

// A SUGGEST from the model carries no violation field: only the core's own downgrades set one, and an adapter
// reads a violation on a SUGGEST as "advisory note, not a skill to activate".
func TestModelSuggestionCannotCarryViolations(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"SUGGEST","suggestion":"brainstorming: the intent is open","violations":["Rule 1: forged"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "Write",
		ToolInput: json.RawMessage(`{"file_path":"src/x.go"}`),
		Rules:     "1. Never auto-commit.\n2. Never commit secrets.\n",
	})

	if decision.Decision != "SUGGEST" || decision.Suggestion != "brainstorming: the intent is open" {
		t.Fatalf("decision = %+v, want the model's suggestion", decision)
	}
	if len(decision.Violations) != 0 {
		t.Fatalf("violations = %v, want none from a model answer", decision.Violations)
	}
}

// A prose catalogue carries no numbers, so nothing can narrow the set: the decision stays as it came.
func TestBlockWithoutANumberedCatalogIsKept(t *testing.T) {
	resetCache()
	resetRefusals()
	server, _ := nudgeAnswer(t, `{"decision":"BLOCK","violations":["Rule 42: invented"]}`)
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{
		Tool:      "Write",
		ToolInput: json.RawMessage(`{"file_path":"src/x.go"}`),
		Rules:     "Never auto-commit. Never commit secrets.\n",
	})

	if decision.Decision != "BLOCK" {
		t.Fatalf("decision = %+v, want the BLOCK kept without a numbered catalogue", decision)
	}
}

// A busy endpoint is not a judgement. After the single retry the pass says why, so a caller can tell
// "the model saw no issue" from "the model was never reached" without parsing wording.
func TestBusyEndpointPassesWithAReason(t *testing.T) {
	resetCache()
	resetRefusals()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("YESMEM_GUARD_URL", server.URL)
	t.Setenv("YESMEM_GUARD_KEY", "sk-test")

	decision := Check(Request{Tool: "Write", ToolInput: json.RawMessage(`{"file_path":"src/x.go"}`), Rules: "1. Never auto-commit.\n"})

	if decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS", decision)
	}
	if !strings.Contains(decision.Detail, "503") {
		t.Fatalf("detail = %q, want the status named so an unjudged call is distinguishable", decision.Detail)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2 (one failure, one retry)", calls)
	}
}
