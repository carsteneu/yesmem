package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeFile is a test helper to write a file or panic.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFormatGuardOutput_PASS(t *testing.T) {
	out := formatGuardOutput(GuardDecision{Decision: "PASS"})
	if out != "" {
		t.Errorf("PASS should be silent, got %q", out)
	}
}

func TestFormatGuardOutput_SUGGEST(t *testing.T) {
	out := formatGuardOutput(GuardDecision{
		Decision:   "SUGGEST",
		Suggestion: "test-driven-development: write tests first",
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%s)", err, out)
	}
	hso, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput, got %v", parsed)
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("expected hookEventName=PreToolUse, got %v", hso["hookEventName"])
	}
	if hso["additionalContext"] != "test-driven-development: write tests first" {
		t.Errorf("additionalContext mismatch, got %v", hso["additionalContext"])
	}
}

func TestFormatGuardOutput_SUGGEST_EmptyIsSilent(t *testing.T) {
	out := formatGuardOutput(GuardDecision{Decision: "SUGGEST", Suggestion: ""})
	if out != "" {
		t.Errorf("SUGGEST without suggestion should be silent, got %q", out)
	}
}

func TestFormatGuardOutput_BLOCK(t *testing.T) {
	out := formatGuardOutput(GuardDecision{
		Decision:   "BLOCK",
		Violations: []string{"No Claude signature in commits", "rule 4"},
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%s)", err, out)
	}
	if parsed["decision"] != "block" {
		t.Errorf("expected decision=block, got %v", parsed["decision"])
	}
	reason, _ := parsed["reason"].(string)
	if !strings.Contains(reason, "Claude signature") || !strings.Contains(reason, "rule 4") {
		t.Errorf("reason should join violations, got %q", reason)
	}
	hso, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput, got %v", parsed)
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("expected hookEventName=PreToolUse, got %v", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected permissionDecision=deny, got %v", hso["permissionDecision"])
	}
	if hso["permissionDecisionReason"] != reason {
		t.Errorf("permissionDecisionReason should mirror reason, got %v", hso["permissionDecisionReason"])
	}
	if hso["additionalContext"] != reason {
		t.Errorf("additionalContext should mirror reason, got %v", hso["additionalContext"])
	}
}

func TestFormatGuardOutput_BLOCK_NoViolations(t *testing.T) {
	out := formatGuardOutput(GuardDecision{Decision: "BLOCK"})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%s)", err, out)
	}
	if parsed["decision"] != "block" {
		t.Errorf("expected decision=block, got %v", parsed["decision"])
	}
	if reason, _ := parsed["reason"].(string); reason == "" {
		t.Error("reason should fall back to default text when violations are empty")
	}
	hso, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput, got %v", parsed)
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected permissionDecision=deny, got %v", hso["permissionDecision"])
	}
	if reason, _ := hso["permissionDecisionReason"].(string); reason == "" {
		t.Error("permissionDecisionReason should fall back to default text when violations are empty")
	}
}

// runGuardWithInput runs RunGuard with the given stdin payload and a clean
// temp HOME/data dir (no config, no API keys), capturing stdout. Restores
// os.Stdin/os.Stdout afterwards.
func runGuardWithInput(t *testing.T, stdin string) string {
	t.Helper()
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "")
	return runGuardCapture(t, filepath.Join(td, ".claude", "yesmem"), stdin)
}

// runGuardCapture swaps os.Stdin/os.Stdout for pipes, runs RunGuard with the
// given dataDir and stdin payload, and returns captured stdout.
func runGuardCapture(t *testing.T, dataDir, stdin string) string {
	t.Helper()
	oldStdin, oldStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = oldStdin, oldStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	os.Stdin = inR
	os.Stdout = outW

	go func() {
		inW.WriteString(stdin)
		inW.Close()
	}()

	RunGuard(dataDir)

	outW.Close()
	out, _ := io.ReadAll(outR)
	inR.Close()
	return string(out)
}

// Issue #123 regression: invalid stdin must be silent (exit 0, no output),
// not {"decision":"PASS"} which Claude Code rejects as invalid JSON.
func TestRunGuard_InvalidStdin_IsSilent(t *testing.T) {
	if out := runGuardWithInput(t, "not json"); out != "" {
		t.Errorf("invalid stdin should be silent, got %q", out)
	}
}

// Non-matching tools (not Bash|REPL|Edit|Write) must be silent.
func TestRunGuard_NonMatchedTool_IsSilent(t *testing.T) {
	payload := `{"tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}`
	if out := runGuardWithInput(t, payload); out != "" {
		t.Errorf("non-matched tool should be silent, got %q", out)
	}
}

// No guard config must be silent (guard cannot evaluate without an API key).
func TestRunGuard_NoConfig_IsSilent(t *testing.T) {
	payload := `{"tool_name":"Bash","tool_input":{"command":"git status"}}`
	if out := runGuardWithInput(t, payload); out != "" {
		t.Errorf("missing config should be silent, got %q", out)
	}
}

// The legacy PASS literal must never appear in any output — it is invalid
// for the Claude Code PreToolUse schema (decision only accepts approve/block).
func TestFormatGuardOutput_NeverEmitsLegacyPASS(t *testing.T) {
	for _, d := range []GuardDecision{
		{Decision: "PASS"},
		{Decision: "BLOCK", Violations: []string{"x"}},
		{Decision: "SUGGEST", Suggestion: "x"},
		{Decision: "PASS", Violations: []string{"x"}}, // ill-formed
	} {
		if out := formatGuardOutput(d); strings.Contains(out, `"decision":"PASS"`) {
			t.Errorf("legacy PASS literal leaked for %+v: %q", d, out)
		}
	}
}

// guardEnv writes the config fixtures RunGuard needs to reach the guard
// pipeline (config resolution + RULES.md load). apiURL is written into
// models.json as the provider endpoint; pass an httptest server URL to
// control the LLM response, or "" for the destructive pre-check tests that
// never reach the LLM.
func guardEnv(t *testing.T, apiURL string) string {
	t.Helper()
	if apiURL == "" {
		apiURL = "http://127.0.0.1:1" // unreachable; pre-check BLOCKs first
	}
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-flash\n")
	writeFile(t, filepath.Join(td, ".cache", "opencode", "models.json"),
		fmt.Sprintf(`{"deepseek":{"api":%q,"models":{"deepseek-v4-flash":{}}}}`, apiURL))
	writeFile(t, filepath.Join(td, ".local", "share", "opencode", "auth.json"),
		`{"deepseek":{"key":"sk-test123"}}`)
	// RunGuard resolves RULES.md as dataDir/../../memory/yesmem/RULES.md.
	writeFile(t, filepath.Join(td, "memory", "yesmem", "RULES.md"),
		"1. Never run destructive commands.\n")
	return filepath.Join(td, ".claude", "yesmem")
}

// guardDecisionServer returns an httptest server that answers every guard
// LLM call with the given decision JSON (a GuardDecision body).
func guardDecisionServer(t *testing.T, decisionJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test123" {
			t.Errorf("missing or wrong Authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, decisionJSON)
	}))
}

// Issue #123: the destructive-pattern BLOCK (the path that previously used
// os.Exit(2)) must now emit the deny JSON on exit 0 — and must never print
// {"decision":"PASS"} on the way out.
func TestRunGuard_DestructivePattern_EmitsDeny(t *testing.T) {
	dataDir := guardEnv(t, "")
	out := runGuardCapture(t, dataDir,
		`{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%q)", err, out)
	}
	if strings.Contains(out, `"decision":"PASS"`) {
		t.Errorf("legacy PASS literal leaked: %q", out)
	}
	hso, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput, got %v", parsed)
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected permissionDecision=deny, got %v", hso["permissionDecision"])
	}
	reason, _ := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "destructive pattern") {
		t.Errorf("reason should name the destructive pattern, got %q", reason)
	}
}

// Non-destructive Bash must not be blocked by the pattern pre-check; the LLM
// (mocked via httptest) returns PASS, so the guard must stay silent — never
// deny, never the invalid legacy PASS literal.
func TestRunGuard_NonDestructiveBash_LLMPassIsSilent(t *testing.T) {
	server := guardDecisionServer(t, `{"decision":"PASS"}`)
	defer server.Close()
	dataDir := guardEnv(t, server.URL)
	out := runGuardCapture(t, dataDir,
		`{"tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/build"}}`)
	if out != "" {
		t.Errorf("LLM PASS must be silent, got %q", out)
	}
	if strings.Contains(out, `"decision":"PASS"`) {
		t.Errorf("legacy PASS literal leaked: %q", out)
	}
}

// An LLM-path BLOCK on a canBlock tool (Write/Edit) must reach the host as
// permissionDecision:deny JSON on exit 0 — the second half of the "BLOCK
// must not be lost" guarantee.
func TestRunGuard_LLMPathBlock_EmitsDeny(t *testing.T) {
	server := guardDecisionServer(t,
		`{"decision":"BLOCK","violations":["rule 7: no force-push"]}`)
	defer server.Close()
	dataDir := guardEnv(t, server.URL)
	out := runGuardCapture(t, dataDir,
		`{"tool_name":"Write","tool_input":{"file_path":"/tmp/x.go"}}`)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%q)", err, out)
	}
	hso, ok := parsed["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput, got %v", parsed)
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected permissionDecision=deny, got %v", hso["permissionDecision"])
	}
	reason, _ := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "rule 7") {
		t.Errorf("reason should carry the violation, got %q", reason)
	}
}

func TestResolveGuardConfig_UsesModelFromConfig(t *testing.T) {
	td := t.TempDir()
	// Override HOME so resolveGuardConfig reads from our temp dir
	t.Setenv("HOME", td)

	// Create config.yaml with custom model
	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-pro\n")

	// Create models.json
	writeFile(t, filepath.Join(td, ".cache", "opencode", "models.json"),
		`{"deepseek":{"api":"https://api.deepseek.com","models":{"deepseek-v4-pro":{}}}}`)

	// Create auth.json
	writeFile(t, filepath.Join(td, ".local", "share", "opencode", "auth.json"),
		`{"deepseek":{"key":"sk-test123"}}`)

	dataDir := filepath.Join(td, ".claude", "yesmem")
	cfg, err := resolveGuardConfig(dataDir)
	if err != nil {
		t.Fatalf("resolveGuardConfig: %v", err)
	}
	if cfg.Model != "deepseek-v4-pro" {
		t.Errorf("expected model deepseek-v4-pro, got %s", cfg.Model)
	}
	if cfg.APIKey != "sk-test123" {
		t.Errorf("expected API key sk-test123, got %s", cfg.APIKey)
	}
	if !strings.Contains(cfg.APIURL, "api.deepseek.com") {
		t.Errorf("expected URL containing api.deepseek.com, got %s", cfg.APIURL)
	}
}

func TestResolveGuardConfig_PrefersProviderWithKey(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)

	// Config with default model
	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-flash\n")

	// Two providers: auriko first (no key), deepseek second (has key)
	writeFile(t, filepath.Join(td, ".cache", "opencode", "models.json"),
		`{
			"auriko":{"api":"https://api.auriko.ai/v1","models":{"deepseek-v4-flash":{}}},
			"deepseek":{"api":"https://api.deepseek.com","models":{"deepseek-v4-flash":{}}}
		}`)

	writeFile(t, filepath.Join(td, ".local", "share", "opencode", "auth.json"),
		`{"deepseek":{"key":"sk-test123"}}`)

	dataDir := filepath.Join(td, ".claude", "yesmem")
	cfg, err := resolveGuardConfig(dataDir)
	if err != nil {
		t.Fatalf("resolveGuardConfig: %v", err)
	}
	if !strings.Contains(cfg.APIURL, "deepseek.com") {
		t.Errorf("expected deepseek URL (provider with key), got %s", cfg.APIURL)
	}
	if cfg.APIKey != "sk-test123" {
		t.Errorf("expected api key from deepseek, got %s", cfg.APIKey)
	}
}

func TestResolveOpenCodeConfig_FallsBackToProviderWithoutKey(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)

	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-flash\n")

	// Only auriko, no auth key
	writeFile(t, filepath.Join(td, ".cache", "opencode", "models.json"),
		`{"auriko":{"api":"https://api.auriko.ai/v1","models":{"deepseek-v4-flash":{}}}}`)

	// Empty auth
	writeFile(t, filepath.Join(td, ".local", "share", "opencode", "auth.json"),
		`{}`)

	dataDir := filepath.Join(td, ".claude", "yesmem")
	cfg, err := resolveOpenCodeConfig(dataDir)
	if err != nil {
		t.Fatalf("resolveOpenCodeConfig: %v", err)
	}
	if !strings.Contains(cfg.APIURL, "auriko") {
		t.Errorf("expected fallback to auriko URL, got %s", cfg.APIURL)
	}
	if cfg.APIKey != "" {
		t.Errorf("expected empty API key for fallback provider, got %s", cfg.APIKey)
	}
}

func TestResolveGuardConfig_FallsBackToAnthropic(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)

	// OpenCode config missing entirely — only create a config.yaml (which fails because no models.json)
	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-flash\n")

	// But set ANTHROPIC_API_KEY in env
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-fallback")

	dataDir := filepath.Join(td, ".claude", "yesmem")
	cfg, err := resolveGuardConfig(dataDir)
	if err != nil {
		t.Fatalf("resolveGuardConfig: %v", err)
	}
	if cfg.APIType != "anthropic" {
		t.Errorf("expected anthropic api type, got %s", cfg.APIType)
	}
	if cfg.APIKey != "sk-ant-fallback" {
		t.Errorf("expected Anthropic API key, got %s", cfg.APIKey)
	}
	if !strings.Contains(cfg.APIURL, "anthropic.com") {
		t.Errorf("expected anthropic URL, got %s", cfg.APIURL)
	}
}

func TestResolveGuardConfig_OpenCodeTakesPriority(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-should-not-be-used")

	// OpenCode has everything it needs
	writeFile(t, filepath.Join(td, ".claude", "yesmem", "config.yaml"),
		"extraction:\n  model: deepseek-v4-flash\n")
	writeFile(t, filepath.Join(td, ".cache", "opencode", "models.json"),
		`{"deepseek":{"api":"https://api.deepseek.com","models":{"deepseek-v4-flash":{}}}}`)
	writeFile(t, filepath.Join(td, ".local", "share", "opencode", "auth.json"),
		`{"deepseek":{"key":"sk-opencode"}}`)

	dataDir := filepath.Join(td, ".claude", "yesmem")
	cfg, err := resolveGuardConfig(dataDir)
	if err != nil {
		t.Fatalf("resolveGuardConfig: %v", err)
	}
	if cfg.APIType != "opencode" {
		t.Errorf("expected opencode api type, got %s", cfg.APIType)
	}
	if cfg.APIKey != "sk-opencode" {
		t.Errorf("expected opencode API key, got %s", cfg.APIKey)
	}
}

func TestResolveAnthropicConfig_FromEnvVar(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env-test")
	t.Setenv("HOME", "/nonexistent")

	cfg, err := resolveAnthropicConfig()
	if err != nil {
		t.Fatalf("resolveAnthropicConfig: %v", err)
	}
	if cfg.APIKey != "sk-ant-env-test" {
		t.Errorf("expected env var key, got %s", cfg.APIKey)
	}
	if cfg.Model != "claude-3-haiku-20240307" {
		t.Errorf("expected haiku model, got %s", cfg.Model)
	}
}

func TestResolveAnthropicConfig_FromClaudeConfig(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "") // clear env

	writeFile(t, filepath.Join(td, ".claude", "config.json"),
		`{"primaryApiKey":"sk-ant-config-file"}`)

	cfg, err := resolveAnthropicConfig()
	if err != nil {
		t.Fatalf("resolveAnthropicConfig: %v", err)
	}
	if cfg.APIKey != "sk-ant-config-file" {
		t.Errorf("expected config file key, got %s", cfg.APIKey)
	}
}

func TestResolveAnthropicConfig_FromClaudeDotJSON(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "")

	writeFile(t, filepath.Join(td, ".claude.json"),
		`{"primaryApiKey":"sk-ant-dotjson"}`)

	cfg, err := resolveAnthropicConfig()
	if err != nil {
		t.Fatalf("resolveAnthropicConfig: %v", err)
	}
	if cfg.APIKey != "sk-ant-dotjson" {
		t.Errorf("expected ~/.claude.json key, got %s", cfg.APIKey)
	}
}

func TestResolveAnthropicConfig_NotFound(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := resolveAnthropicConfig()
	if err == nil {
		t.Fatal("expected error when no key found, got nil")
	}
}

func TestEvaluateGuard_ParsesJSON(t *testing.T) {
	// Reset guard cache for clean test
	guardCacheMu.Lock()
	guardCache = make(map[string]guardCacheEntry)
	guardCacheMu.Unlock()

	// Start a test HTTP server that returns a valid decision
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request has correct headers
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("missing or wrong Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing Content-Type header")
		}
		// Return a SUGGEST decision
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"decision":"SUGGEST","suggestion":"test-skill: use test pattern"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &guardConfig{
		Model:  "deepseek-v4-flash",
		APIURL: server.URL + "/v1/chat/completions",
		APIKey: "sk-test",
	}

	decision := evaluateGuard(cfg, "Some rules content", "Bash: git push", "Bash")
	if decision.Decision != "SUGGEST" {
		t.Errorf("expected SUGGEST, got %s", decision.Decision)
	}
	if decision.Suggestion != "test-skill: use test pattern" {
		t.Errorf("expected 'test-skill: use test pattern', got %s", decision.Suggestion)
	}
}

func TestEvaluateGuard_HandlesCodeFences(t *testing.T) {
	guardCacheMu.Lock()
	guardCache = make(map[string]guardCacheEntry)
	guardCacheMu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": "```json\n{\"decision\":\"PASS\"}\n```",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &guardConfig{
		Model:  "deepseek-v4-flash",
		APIURL: server.URL + "/v1/chat/completions",
		APIKey: "sk-test",
	}

	decision := evaluateGuard(cfg, "rules", "Bash: ls", "Bash")
	if decision.Decision != "PASS" {
		t.Errorf("expected PASS, got %s", decision.Decision)
	}
}

func TestEvaluateGuard_RetriesOnFailure(t *testing.T) {
	guardCacheMu.Lock()
	guardCache = make(map[string]guardCacheEntry)
	guardCacheMu.Unlock()

	var callCount int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		count := callCount
		mu.Unlock()
		if count == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"decision":"PASS"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &guardConfig{
		Model:  "deepseek-v4-flash",
		APIURL: server.URL + "/v1/chat/completions",
		APIKey: "sk-test",
	}

	decision := evaluateGuard(cfg, "rules", "Bash: retry-test", "Bash")
	if decision.Decision != "PASS" {
		t.Errorf("expected PASS after retry, got %s", decision.Decision)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 success), got %d", callCount)
	}
}

func TestEvaluateGuard_CacheHit(t *testing.T) {
	guardCacheMu.Lock()
	guardCache = make(map[string]guardCacheEntry)
	guardCacheMu.Unlock()

	var callCount int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"decision":"PASS"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := &guardConfig{
		Model:  "deepseek-v4-flash",
		APIURL: server.URL + "/v1/chat/completions",
		APIKey: "sk-test",
	}

	// First call
	d1 := evaluateGuard(cfg, "cache rules", "Bash: cache-test", "Bash")
	if d1.Decision != "PASS" {
		t.Errorf("expected PASS, got %s", d1.Decision)
	}

	// Second call with same params — should hit cache
	d2 := evaluateGuard(cfg, "cache rules", "Bash: cache-test", "Bash")
	if d2.Decision != "PASS" {
		t.Errorf("expected PASS from cache, got %s", d2.Decision)
	}

	if callCount != 1 {
		t.Errorf("expected 1 HTTP call (cached), got %d", callCount)
	}
}

func TestDescribeToolCall_Bash(t *testing.T) {
	hook := &HookInput{
		ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"git status"}`),
	}
	desc := describeToolCall(hook)
	if desc != "Bash: git status" {
		t.Errorf("expected 'Bash: git status', got %s", desc)
	}
}

func TestDowngradeUnauthorizedBlock_BashBlockBecomesSuggest(t *testing.T) {
	d := GuardDecision{Decision: "BLOCK", Violations: []string{"Rule 1: auto-commit"}}
	got := downgradeUnauthorizedBlock(d, "Bash")
	if got.Decision != "SUGGEST" {
		t.Errorf("expected SUGGEST, got %s", got.Decision)
	}
	if !strings.Contains(got.Suggestion, "yesmem-remember") {
		t.Errorf("expected mandatory-check skill prefix, got %q", got.Suggestion)
	}
	if !strings.Contains(got.Suggestion, "Rule 1: auto-commit") {
		t.Errorf("expected violations preserved in suggestion, got %q", got.Suggestion)
	}
}

func TestDowngradeUnauthorizedBlock_REPLBlockBecomesSuggest(t *testing.T) {
	d := GuardDecision{Decision: "BLOCK"}
	got := downgradeUnauthorizedBlock(d, "REPL")
	if got.Decision != "SUGGEST" {
		t.Errorf("REPL BLOCK should downgrade, got %s", got.Decision)
	}
	if !strings.Contains(got.Suggestion, "RULES.md") {
		t.Errorf("default suggestion should reference RULES.md, got %q", got.Suggestion)
	}
}

func TestDowngradeUnauthorizedBlock_EditBlockSurvives(t *testing.T) {
	d := GuardDecision{Decision: "BLOCK", Violations: []string{"Rule 2: secret"}}
	got := downgradeUnauthorizedBlock(d, "Edit")
	if got.Decision != "BLOCK" {
		t.Errorf("Edit BLOCK must be honoured, got %s", got.Decision)
	}
}

func TestDowngradeUnauthorizedBlock_WriteBlockSurvives(t *testing.T) {
	d := GuardDecision{Decision: "BLOCK"}
	got := downgradeUnauthorizedBlock(d, "Write")
	if got.Decision != "BLOCK" {
		t.Errorf("Write BLOCK must be honoured, got %s", got.Decision)
	}
}

func TestDowngradeUnauthorizedBlock_NonBlockPassesThrough(t *testing.T) {
	for _, dec := range []string{"PASS", "SUGGEST", ""} {
		d := GuardDecision{Decision: dec, Suggestion: "tdd: x"}
		got := downgradeUnauthorizedBlock(d, "Bash")
		if got.Decision != dec {
			t.Errorf("non-BLOCK %q should pass through, got %s", dec, got.Decision)
		}
	}
}

func TestDescribeToolCall_REPL(t *testing.T) {
	hook := &HookInput{
		ToolName:  "REPL",
		ToolInput: json.RawMessage(`{"code":"sh('git status')"}`),
	}
	desc := describeToolCall(hook)
	if desc != "REPL: sh('git status')" {
		t.Errorf("expected 'REPL: sh(\\'git status\\')', got %s", desc)
	}
}

func TestDescribeToolCall_Edit(t *testing.T) {
	hook := &HookInput{
		ToolName: "Edit",
		ToolInput: json.RawMessage(`{"file_path":"/path/to/file.go"}`),
	}
	desc := describeToolCall(hook)
	if desc != "Edit: /path/to/file.go" {
		t.Errorf("expected 'Edit: /path/to/file.go', got %s", desc)
	}
}

func TestDescribeToolCall_Write(t *testing.T) {
	hook := &HookInput{
		ToolName: "Write",
		ToolInput: json.RawMessage(`{"file_path":"/path/to/new.go"}`),
	}
	desc := describeToolCall(hook)
	if desc != "Write: /path/to/new.go" {
		t.Errorf("expected 'Write: /path/to/new.go', got %s", desc)
	}
}

func TestLoadRulesFile_UsesCWD(t *testing.T) {
	td := t.TempDir()
	cwdRule := filepath.Join(td, "RULES.md")
	os.WriteFile(cwdRule, []byte("cwd rules"), 0644)

	rules := loadRulesFile("/nonexistent/RULES.md", td)
	if rules != "cwd rules" {
		t.Errorf("expected 'cwd rules', got %s", rules)
	}
}

func TestLoadRulesFile_Fallback(t *testing.T) {
	td := t.TempDir()
	fallback := filepath.Join(td, "RULES.md")
	os.WriteFile(fallback, []byte("fallback rules"), 0644)

	rules := loadRulesFile(fallback, "")
	if rules != "fallback rules" {
		t.Errorf("expected 'fallback rules', got %s", rules)
	}
}

func TestBuildGuardPrompt_CanBlock(t *testing.T) {
	prompt := buildGuardPrompt("test rule", "Bash: git push", true)
	if !strings.Contains(prompt, "BLOCK") {
		t.Error("expected BLOCK option in prompt for canBlock=true")
	}
	if !strings.Contains(prompt, "SUGGEST") {
		t.Error("expected SUGGEST option in prompt")
	}
	if !strings.Contains(prompt, "PASS") {
		t.Error("expected PASS option in prompt")
	}
}

func TestBuildGuardPrompt_NoBlock(t *testing.T) {
	prompt := buildGuardPrompt("test rule", "Bash: git push", false)
	if strings.Contains(prompt, "BLOCK") {
		t.Error("expected no BLOCK option for canBlock=false")
	}
}

func TestHashStrings_Deterministic(t *testing.T) {
	h1 := hashStrings("a", "b", "c")
	h2 := hashStrings("a", "b", "c")
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %s vs %s", h1, h2)
	}
	if len(h1) != 16 {
		t.Errorf("expected 16 char hash, got %d: %s", len(h1), h1)
	}
}
