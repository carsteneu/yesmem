package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func guardStatusFixture(t *testing.T, provider, configYAML, registry, auth string) (home, dataDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("YESMEM_GUARD_MODEL", "")
	t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")
	dataDir = filepath.Join(home, ".claude", "yesmem")
	for _, dir := range []string{dataDir, filepath.Join(home, ".cache", "opencode"), filepath.Join(home, ".local", "share", "opencode")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeFixtureFile(t, filepath.Join(dataDir, "config.yaml"), configYAML)
	writeFixtureFile(t, filepath.Join(home, ".cache", "opencode", "models.json"), registry)
	writeFixtureFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), auth)
	return home, dataDir
}

func TestGuardStatusReportsModelAndHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-ant-setup" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
			{"id": "claude-haiku-5-5", "created_at": "2026-10-07T18:00:00Z"},
		}})
	}))
	t.Cleanup(server.Close)

	_, dataDir := guardStatusFixture(t, "api",
		"llm:\n  provider: api\n",
		`{"anthropic":{"api":"https://api.anthropic.com","npm":"@ai-sdk/anthropic","models":{}}}`,
		`{"anthropic":{"key":"sk-ant-setup"}}`)
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	lines := strings.Join(guardStatusLines(dataDir, "api"), "\n")

	// The newest haiku of the account, and where it is asked.
	if !strings.Contains(lines, "claude-haiku-5-5") {
		t.Errorf("report does not name the resolved model:\n%s", lines)
	}
	if !strings.Contains(lines, "api.anthropic.com") {
		t.Errorf("report does not name the endpoint:\n%s", lines)
	}
	if !strings.Contains(lines, "account model list") {
		t.Errorf("report does not name where the model came from:\n%s", lines)
	}
	// No model name is frozen into config.yaml: the guard resolves it on every call.
	config, err := os.ReadFile(filepath.Join(dataDir, "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml: %v", err)
	}
	if strings.Contains(string(config), "claude-") {
		t.Errorf("config.yaml carries a model name:\n%s", config)
	}
}

func TestGuardStatusNamesTheSubscriptionCase(t *testing.T) {
	_, dataDir := guardStatusFixture(t, "cli", "llm:\n  provider: cli\n", "{}", "{}")

	lines := strings.Join(guardStatusLines(dataDir, "cli"), "\n")

	if !strings.Contains(lines, "inactive") {
		t.Errorf("a subscription install must say the guard is inactive:\n%s", lines)
	}
	if !strings.Contains(lines, "subscription") {
		t.Errorf("the reason must be named:\n%s", lines)
	}
}

func TestGuardStatusReportsTheOpenCodePath(t *testing.T) {
	_, dataDir := guardStatusFixture(t, "opencode",
		"extraction:\n  model: deepseek-v4-flash\nllm:\n  provider: openai_compatible\n",
		`{"deepseek":{"api":"https://api.deepseek.com","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`,
		`{"deepseek":{"key":"sk-deepseek"}}`)

	lines := strings.Join(guardStatusLines(dataDir, "openai_compatible"), "\n")

	if !strings.Contains(lines, "deepseek-v4-flash") || !strings.Contains(lines, "api.deepseek.com") {
		t.Errorf("report does not name the opencode model and host:\n%s", lines)
	}
}
