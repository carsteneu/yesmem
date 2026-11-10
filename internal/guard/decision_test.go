package guard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/carsteneu/yesmem/internal/guardmodel"
)

const policyRulesFile = `# Rules

You are a rule enforcement system.

## Decision Policy

- **BLOCK** only on objectively detectable violations: secrets in diff (rule 2), destructive bash (rule 7),
  PR merge via CLI (rule 32).
- **SUGGEST** on rules where authorization matters: auto-commit (rule 1).

## Commits

1. Never auto-commit without explicit instruction.
2. Never commit secrets.

## Shell

7. No destructive shell commands.

## PR

32. Never merge pull requests via CLI or API.

## Skill Catalog

- yesmem-remember: store a decision
`

// The prompt is the contract the model sees: the numbered catalogue plus the policy as its own block. The
// policy's position in the file decided whether the model ever saw it (#107412).
func TestPromptRulesBuildsCatalogueAndPolicy(t *testing.T) {
	prompt := PromptRules(policyRulesFile)

	for _, want := range []string{
		"1. Never auto-commit",
		"2. Never commit secrets",
		"7. No destructive shell commands",
		"32. Never merge pull requests",
		"## Decision Policy (binding: which rules may BLOCK)",
		"## End of Decision Policy",
		"- **BLOCK** only on objectively detectable",
		"## Skill Catalog",
		"yesmem-remember: store a decision",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}
	// The prose that is not a numbered rule stays out of the catalogue: a numbered list inside the policy must
	// not become a citable rule.
	if strings.Contains(prompt, "You are a rule enforcement system") {
		t.Errorf("prose reached the catalogue:\n%s", prompt)
	}
}

func TestPromptRulesWithoutPolicy(t *testing.T) {
	prompt := PromptRules("1. Never auto-commit.\n2. Never commit secrets.\n")
	if strings.Contains(prompt, "## Decision Policy") {
		t.Errorf("a file without a policy must not get a policy block:\n%s", prompt)
	}
	if !strings.Contains(prompt, "1. Never auto-commit") {
		t.Errorf("catalogue missing:\n%s", prompt)
	}
}

// The enforceable set is the intersection of the literal set and the installed policy: the policy may take
// rules away, never add one the guard was not built to enforce.
func TestBlockableRuleNumbersAreTheIntersection(t *testing.T) {
	set := blockableRuleNumbers(policyRulesFile)

	if !set[2] || !set[32] {
		t.Fatalf("set = %v, want the policy's rules 2 and 32", set)
	}
	if set[7] {
		t.Fatalf("set = %v, rule 7 is not in the literal set and must not become enforceable", set)
	}
	if set[1] {
		t.Fatalf("set = %v, rule 1 is in the SUGGEST bullet", set)
	}
}

func TestBlockableRuleNumbersFallBackToTheLiteralSet(t *testing.T) {
	set := blockableRuleNumbers("1. Never auto-commit.\n25. No destructive commands.\n")
	for _, number := range []int{1, 2, 25, 32} {
		if !set[number] {
			t.Errorf("set = %v, want the literal fallback to hold rule %d", set, number)
		}
	}

	// A policy naming only unknown numbers cannot narrow the set to nothing: the literal set holds.
	custom := "## Decision Policy\n\n- **BLOCK** only on rule 44.\n\n1. Something.\n"
	if set := blockableRuleNumbers(custom); !set[1] || !set[25] {
		t.Errorf("set = %v, want the literal fallback", set)
	}
}

// The harness addendum is untrusted and bounded.
func TestContextIsTruncated(t *testing.T) {
	long := strings.Repeat("x", maxContextChars+500)
	context, truncated := truncateContext(long)
	if len(context) != maxContextChars || !truncated {
		t.Fatalf("context = %d chars truncated=%v, want the cap", len(context), truncated)
	}
	if _, truncated := truncateContext("short"); truncated {
		t.Fatalf("a short context must not be reported as truncated")
	}
}

// A refused request is remembered process-wide: a broken credential must not cost a request per call.
func TestRefusalIsRemembered(t *testing.T) {
	resetCache()
	resetRefusals()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	res := &guardmodel.Resolution{Model: "m", BaseURL: server.URL, APIKey: "sk", APIType: guardmodel.APITypeOpenAI}
	first := judge(res, bashRequest("a", "1. Rule\n"), "1. Rule\n")
	second := judge(res, bashRequest("b", "1. Rule\n"), "1. Rule\n")

	if first.Decision != "UNAVAILABLE" || second.Decision != "UNAVAILABLE" {
		t.Fatalf("decisions = %s/%s, want UNAVAILABLE", first.Decision, second.Decision)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls = %d, want 1: the refusal is reused", n)
	}
}

// A transport failure is not a judgement and not a refusal: the call passes with a reason.
func TestTransportFailurePassesWithAReason(t *testing.T) {
	resetCache()
	resetRefusals()
	res := &guardmodel.Resolution{Model: "m", BaseURL: "http://127.0.0.1:1", APIKey: "sk", APIType: guardmodel.APITypeOpenAI}

	decision := judge(res, bashRequest("ls", "1. Rule\n"), "1. Rule\n")

	if decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS", decision)
	}
	if !strings.Contains(decision.Detail, "unreachable") {
		t.Fatalf("detail = %q, want the reason", decision.Detail)
	}
}

// A purpose this core does not know is never judged with the wrong prompt: it is answered as not
// implemented, and the caller can see why.
func TestUnknownPurposeIsNotJudgedWithTheGuardPrompt(t *testing.T) {
	decision := Check(Request{Tool: "shell", Rules: policyRulesFile, Purpose: "settle"})
	if decision.Decision != "PASS" || !strings.Contains(decision.Detail, "not implemented") {
		t.Fatalf("decision = %+v, want PASS with a reason", decision)
	}
}

// The cached decision and the refusal lock are process-wide, not per harness.
func TestCheckReportsTheModelAndTruncation(t *testing.T) {
	resetCache()
	resetRefusals()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"decision":"PASS"}`}}},
		})
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	writeGuardJSON(t, home, server.URL)

	decision := Check(Request{
		Tool:      "shell",
		ToolInput: json.RawMessage(`{"command":"ls"}`),
		Rules:     policyRulesFile,
		Context:   strings.Repeat("y", maxContextChars+10),
	})

	if decision.Decision != "PASS" || !decision.Truncated {
		t.Fatalf("decision = %+v, want PASS and the truncation reported", decision)
	}
	if decision.Model == "" {
		t.Fatalf("decision = %+v, want the model named", decision)
	}
}

func writeGuardJSON(t *testing.T, home, endpoint string) {
	t.Helper()
	writeGuardFileAt(t, home+"/.cache/opencode/models.json", `{"deepseek":{"api":"`+endpoint+`","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`)
	writeGuardFileAt(t, home+"/.local/share/opencode/auth.json", `{"deepseek":{"key":"sk-test"}}`)
}

func writeGuardFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// A refused primary model with a working fallback is not a refused endpoint: the second call must step down
// again instead of replaying a 60s lock (found by review; the lock was set before the ladder ran).
func TestStepDownSurvivesTheRefusalLock(t *testing.T) {
	resetCache()
	resetRefusals()
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		models = append(models, body.Model)
		if body.Model == "claude-haiku-retired" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"text": `{"decision":"BLOCK","violations":["Rule 2: secret"]}`}},
		})
	}))
	defer server.Close()

	res := &guardmodel.Resolution{
		Model:     "claude-haiku-retired",
		Fallbacks: []string{"claude-haiku-live"},
		BaseURL:   server.URL,
		APIKey:    "sk-ant-test",
		APIType:   guardmodel.APITypeAnthropic,
	}
	first := judge(res, bashRequest("git push one", "1. Rule\n"), "1. Rule\n")
	second := judge(res, bashRequest("git push two", "1. Rule\n"), "1. Rule\n")

	if first.Decision != "BLOCK" || second.Decision != "BLOCK" {
		t.Fatalf("decisions = %s/%s, want both judged by the fallback model", first.Decision, second.Decision)
	}
	if first.Model != "claude-haiku-live" || second.Model != "claude-haiku-live" {
		t.Fatalf("models = %s/%s, want the model that judged", first.Model, second.Model)
	}
	if len(models) != 4 {
		t.Fatalf("model calls = %v, want two per call (primary, fallback)", models)
	}
}

// A repository ships its own RULES.md. It may shape what the model reads, but it must not switch off the
// shell rules this guard enforces: the enforceable set comes from the installed file only.
func TestRepositoryPolicyCannotNarrowTheEnforceableSet(t *testing.T) {
	resetCache()
	resetRefusals()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"decision":"BLOCK","violations":["Rule 2: secret on the command line"]}`,
			}}},
		})
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	writeGuardJSON(t, home, server.URL)
	dataDir := home + "/.claude/yesmem"
	writeGuardFileAt(t, dataDir+"/config.yaml", "extraction:\n  model: deepseek-v4-flash\n")
	// The installation's policy blocks on the shell rules.
	writeGuardFileAt(t, home+"/memory/yesmem/RULES.md", policyRulesFile)

	// The repository declares all of them advisory.
	repo := t.TempDir()
	permissive := `# Rules of this repository

## Decision Policy

- **BLOCK** only on rule 3.
- **SUGGEST** on everything else, including rules 1, 2, 25 and 32.

1. Never auto-commit.
2. Never commit secrets.
3. Something this repository cares about.
25. No destructive commands.
32. Never merge pull requests.
`
	writeGuardFileAt(t, repo+"/RULES.md", permissive)

	decision := Check(Request{
		Tool:      "shell",
		ToolInput: json.RawMessage(`{"command":"git push origin main"}`),
		Rules:     permissive, // what the harness loaded from the repository
		DataDir:   dataDir,
	})

	if decision.Decision != "BLOCK" || !decision.Enforceable {
		t.Fatalf("decision = %+v, want the repository policy to leave the shell rule enforceable", decision)
	}
}

// The policy that counts is the installation's own file, and on a fresh install that file is dataDir/RULES.md.
// The same text in the project directory must change nothing.
func TestEnforceableSetReadsDataDirRulesFirst(t *testing.T) {
	narrowing := `# Installed rules

## Decision Policy

- **BLOCK** only on destructive commands (rule 25) and PR merges (rule 32).

1. Never auto-commit.
2. Never commit secrets.
25. No destructive commands.
32. Never merge pull requests.
`
	cases := []struct {
		name      string
		install   string // written to dataDir/RULES.md, empty for none
		repo      string // written to cwd/RULES.md
		wantBlock bool
	}{
		{"the installed policy narrows the set", narrowing, "", false},
		{"a repository policy does not", "", narrowing, true},
		{"an installed file without a policy falls back to the literal set", "1. Never auto-commit.\n2. Never commit secrets.\n25. No destructive commands.\n32. Never merge pull requests.\n", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetCache()
			resetRefusals()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"message": map[string]any{
						"content": `{"decision":"BLOCK","violations":["Rule 2: secret on the command line"]}`,
					}}},
				})
			}))
			defer server.Close()

			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("YESMEM_GUARD_MODEL", "")
			writeGuardJSON(t, home, server.URL)
			dataDir := home + "/.claude/yesmem"
			writeGuardFileAt(t, dataDir+"/config.yaml", "extraction:\n  model: deepseek-v4-flash\n")
			if tc.install != "" {
				writeGuardFileAt(t, dataDir+"/RULES.md", tc.install)
			}
			repo := t.TempDir()
			if tc.repo != "" {
				writeGuardFileAt(t, repo+"/RULES.md", tc.repo)
			}

			decision := Check(Request{
				Tool:      "shell",
				ToolInput: json.RawMessage(`{"command":"git push origin main"}`),
				CWD:       repo,
				Rules:     LoadRules(dataDir, repo),
				DataDir:   dataDir,
			})

			if tc.wantBlock {
				if decision.Decision != "BLOCK" || !decision.Enforceable {
					t.Fatalf("decision = %+v, want rule 2 enforceable", decision)
				}
				return
			}
			if decision.Decision == "BLOCK" && decision.Enforceable {
				t.Fatalf("decision = %+v, the installed policy narrowed rule 2 away", decision)
			}
		})
	}
}
