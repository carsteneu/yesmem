package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/guard"
	"github.com/carsteneu/yesmem/internal/hooks"
)

// TestGuardParityCLIAndRPC is the contract of the split: the Claude Code hook and the daemon RPC are two
// wirings of one core, so the same call must reach the same verdict through both. A difference here means the
// judgement drifted back into a harness.
func TestGuardParityCLIAndRPC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")

	var mu sync.Mutex
	var prompts []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if len(body.Messages) > 1 {
			prompts = append(prompts, body.Messages[1].Content)
		}
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"decision":"BLOCK","violations":["Rule 2: secret on the command line"]}`,
			}}},
		})
	}))
	defer model.Close()

	// One installation, one project, one rules file: both sides resolve through it.
	writeAt(t, filepath.Join(home, ".cache", "opencode", "models.json"),
		`{"deepseek":{"api":"`+model.URL+`","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`)
	writeAt(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"deepseek":{"key":"sk-test"}}`)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	writeAt(t, filepath.Join(dataDir, "config.yaml"), "extraction:\n  model: deepseek-v4-flash\n")
	project := t.TempDir()
	writeAt(t, filepath.Join(project, "RULES.md"), "1. Never auto-commit.\n2. Never commit secrets.\n25. No destructive commands.\n")

	// CLI: Claude Code's PreToolUse payload on stdin.
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push origin main"},"cwd":"` + project + `","session_id":"ses_parity"}`
	cliOut := runHook(t, dataDir, payload)
	var hookOut struct {
		Decision           string `json:"decision"`
		Reason             string `json:"reason"`
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
			Reason             string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(cliOut), &hookOut); err != nil {
		t.Fatalf("hook output is not JSON: %v (%q)", err, cliOut)
	}

	// Forget the 5s cache: both paths must reach the model, otherwise the comparison proves nothing.
	guard.ResetState()

	// RPC: what the opencode plugin sends.
	handler := daemon.NewHandler(nil, nil)
	resp := handler.Handle(daemon.Request{
		Method: "guard_check",
		Params: map[string]any{
			"tool":       "shell",
			"tool_input": map[string]any{"command": "git push origin main"},
			"cwd":        project,
			"session_id": "ses_parity",
		},
	})
	if resp.Error != "" {
		t.Fatalf("guard_check: %s", resp.Error)
	}
	var rpcDecision struct {
		Decision    string   `json:"decision"`
		Violations  []string `json:"violations"`
		Enforceable bool     `json:"enforceable"`
	}
	if err := json.Unmarshal(resp.Result, &rpcDecision); err != nil {
		t.Fatalf("guard_check result: %v", err)
	}

	if hookOut.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("CLI did not deny: %q", cliOut)
	}
	if rpcDecision.Decision != "BLOCK" || !rpcDecision.Enforceable {
		t.Fatalf("RPC decision = %+v, want an enforceable BLOCK", rpcDecision)
	}
	// Both name the same violation, and both saw the same rules: the policy block reaches the prompt on either
	// path, which is the fix for the policy never arriving (#107412).
	if !strings.Contains(hookOut.Reason, "Rule 2") || strings.Join(rpcDecision.Violations, " ") != "Rule 2: secret on the command line" {
		t.Fatalf("the two paths disagree: hook=%q rpc=%v", hookOut.Reason, rpcDecision.Violations)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("model calls = %d, want one per path", len(prompts))
	}
	if prompts[0] != prompts[1] {
		t.Fatalf("prompts differ:\nCLI: %s\nRPC: %s", prompts[0], prompts[1])
	}
}

// runHook drives the Claude Code entry point with a payload on stdin and returns what it printed.
func runHook(t *testing.T, dataDir, payload string) string {
	t.Helper()
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	originalIn, originalOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinRead, stdoutWrite
	defer func() {
		os.Stdin, os.Stdout = originalIn, originalOut
	}()

	if _, err := stdinWrite.WriteString(payload); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	stdinWrite.Close()
	hooks.RunGuard(dataDir)
	stdoutWrite.Close()

	out, err := io.ReadAll(stdoutRead)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(out)
}

func writeAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
