package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/guard"
)

// guardHandlerEnv builds a handler whose guard resolves an OpenAI-compatible provider at the stub endpoint, so
// no test reaches a real API.
func guardHandlerEnv(t *testing.T, endpoint string) (*Handler, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")

	writeGuardFile(t, filepath.Join(home, ".cache", "opencode", "models.json"), `{"deepseek":{"api":"`+endpoint+`","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`)
	writeGuardFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"deepseek":{"key":"sk-test"}}`)

	h, _ := mustHandler(t)
	dataDir := t.TempDir()
	h.dataDir = dataDir
	writeGuardFile(t, filepath.Join(dataDir, "config.yaml"), "extraction:\n  model: deepseek-v4-flash\n")

	cwd := t.TempDir()
	// 14 is advisory: it exists in the catalogue, but the policy does not class it as blockable. A citation of
	// a rule the catalogue does not carry is a different case and is covered in internal/guard.
	writeGuardFile(t, filepath.Join(cwd, "RULES.md"), "1. Never auto-commit.\n2. Never commit secrets.\n14. Verify before overwriting.\n25. No destructive shell commands.\n")
	return h, cwd
}

func writeGuardFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestHandleGuardCheckRequiresTool(t *testing.T) {
	_, cwd := guardHandlerEnv(t, "http://127.0.0.1:1")
	h, _ := mustHandler(t)

	if resp := h.handleGuardCheck(map[string]any{"cwd": cwd}); resp.Error == "" {
		t.Fatal("expected an error for a call without a tool")
	}
}

func TestHandleGuardCheckWithoutRulesIsPass(t *testing.T) {
	h, _ := guardHandlerEnv(t, "http://127.0.0.1:1")
	empty := t.TempDir()

	resp := h.handleGuardCheck(map[string]any{"tool": "shell", "cwd": empty})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var decision guard.Decision
	if err := json.Unmarshal(resp.Result, &decision); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decision.Decision != "PASS" {
		t.Fatalf("decision = %+v, want PASS without rules", decision)
	}
}

func TestHandleGuardCheckReturnsTheModelDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want the provider's completions endpoint", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages := body["messages"].([]any)
		user := messages[1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "PROPOSED TOOL CALL: Bash: git push origin main") {
			t.Errorf("prompt = %q, want the described call", user)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"decision":"BLOCK","violations":["Rule 2: secret on the command line"]}`,
			}}},
		})
	}))
	defer server.Close()
	h, cwd := guardHandlerEnv(t, server.URL)

	resp := h.handleGuardCheck(map[string]any{
		"tool":       "shell",
		"tool_input": map[string]any{"command": "git push origin main"},
		"cwd":        cwd,
		"session_id": "ses_test",
	})

	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var decision guard.Decision
	if err := json.Unmarshal(resp.Result, &decision); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decision.Decision != "BLOCK" {
		t.Fatalf("decision = %+v, want the model's BLOCK", decision)
	}
	if decision.Model != "deepseek-v4-flash" {
		t.Errorf("model = %q, want the resolved model reported back", decision.Model)
	}
	if !decision.Enforceable {
		t.Errorf("a rule 2 citation on a shell call must be enforceable, got %+v", decision)
	}
}

func TestHandleGuardCheckDowngradesAdvisoryShellBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"decision":"BLOCK","violations":["Rule 14: verify before overwriting"]}`,
			}}},
		})
	}))
	defer server.Close()
	h, cwd := guardHandlerEnv(t, server.URL)

	resp := h.handleGuardCheck(map[string]any{
		"tool":       "shell",
		"tool_input": map[string]any{"command": "git status"},
		"cwd":        cwd,
	})

	var decision guard.Decision
	if err := json.Unmarshal(resp.Result, &decision); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The policy narrowing happens once, in the core: an advisory citation is a check, not a refusal.
	if decision.Decision != "SUGGEST" {
		t.Fatalf("decision = %+v, want SUGGEST", decision)
	}
	if decision.Enforceable {
		t.Errorf("a downgraded block must not be enforceable")
	}
}
