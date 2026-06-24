package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModels_ValidFile(t *testing.T) {
	dir := t.TempDir()
	modelsContent := `{
  "deepseek": {
    "npm": "@ai-sdk/openai-compatible",
    "api": "https://api.deepseek.com",
    "models": {
      "deepseek-chat": {"id": "deepseek-chat"},
      "deepseek-reasoner": {"id": "deepseek-reasoner"}
    }
  },
  "openai": {
    "npm": "@ai-sdk/openai",
    "models": {"gpt-5.2": {"id": "gpt-5.2"}}
  }
}`
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"), modelsContent)

	models, err := loadModels(dir)
	if err != nil {
		t.Fatalf("loadModels failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(models))
	}
	if models["deepseek"].NPM != "@ai-sdk/openai-compatible" {
		t.Errorf("deepseek NPM mismatch: %s", models["deepseek"].NPM)
	}
	if models["deepseek"].API != "https://api.deepseek.com" {
		t.Errorf("deepseek API URL mismatch: %s", models["deepseek"].API)
	}
	if len(models["deepseek"].Models) != 2 {
		t.Errorf("deepseek model count mismatch: %d", len(models["deepseek"].Models))
	}
}

func TestLoadModels_FileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := loadModels(dir)
	if err == nil {
		t.Fatalf("expected error when models.json missing")
	}
}

func TestLoadModels_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"), `{invalid json}`)
	_, err := loadModels(dir)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}

func TestLoadAuth_ValidFile(t *testing.T) {
	dir := t.TempDir()
	authContent := `{
  "deepseek": {"type": "api", "key": "sk-deepseek-test"},
  "openai": {"type": "api", "key": "sk-openai-test"},
  "emptykey": {"type": "api", "key": ""}
}`
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"), authContent)

	auth, err := loadAuth(dir)
	if err != nil {
		t.Fatalf("loadAuth failed: %v", err)
	}
	if len(auth) != 2 {
		t.Fatalf("expected 2 providers with non-empty keys, got %d", len(auth))
	}
	if auth["deepseek"] != "sk-deepseek-test" {
		t.Errorf("deepseek key mismatch: %s", auth["deepseek"])
	}
	if auth["openai"] != "sk-openai-test" {
		t.Errorf("openai key mismatch: %s", auth["openai"])
	}
	if _, exists := auth["emptykey"]; exists {
		t.Errorf("emptykey should be filtered out")
	}
}

func TestLoadAuth_FileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := loadAuth(dir)
	if err == nil {
		t.Fatalf("expected error when auth.json missing")
	}
}

func TestLoadAuth_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"), `not json`)
	_, err := loadAuth(dir)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
}

// TestResolveOpenCodeProvider_NoProviders verifies error when no openai-compatible
// providers with API keys are found.
func TestResolveOpenCodeProvider_NoProviders(t *testing.T) {
	dir := t.TempDir()
	// models.json has openai-compatible provider but auth.json has no matching key
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"),
		`{"deepseek":{"npm":"@ai-sdk/openai-compatible","api":"https://api.deepseek.com","models":{"x":{"id":"x"}}}}`)
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"),
		`{"other":{"type":"api","key":"sk-other"}}`)

	_, _, _, _, err := resolveOpenCodeProvider(dir)
	if err == nil {
		t.Fatalf("expected error: no matching providers")
	}
}

func TestResolveOpenCodeProvider_TemplateVariableURL(t *testing.T) {
	dir := t.TempDir()
	// Provider has template variable URL (not http), should be filtered out
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"),
		`{"weird":{"npm":"@ai-sdk/openai-compatible","api":"${BASE_URL}","models":{"x":{"id":"x"}}}}`)
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"),
		`{"weird":{"type":"api","key":"sk-weird"}}`)

	_, _, _, _, err := resolveOpenCodeProvider(dir)
	if err == nil {
		t.Fatalf("expected error: template URL provider should be filtered out")
	}
}

func TestLoadModels_RealFileFromMachine(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	path := filepath.Join(home, ".cache", "opencode", "models.json")
	if _, err := os.Stat(path); err != nil {
		t.Skip("models.json not present on this machine")
	}
	models, err := loadModels(home)
	if err != nil {
		t.Fatalf("loadModels on real file failed: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("expected non-empty models map from real file")
	}
	// Verify deepseek exists and is openai-compatible (expected on this machine)
	if ds, ok := models["deepseek"]; ok {
		if ds.NPM != "@ai-sdk/openai-compatible" {
			t.Errorf("deepseek NPM expected @ai-sdk/openai-compatible, got %s", ds.NPM)
		}
	}
}

func TestEnsureV1(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com//", "https://api.deepseek.com/v1"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1"}, // idempotent
		{"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1"},   // already v1
		{"https://api.openai.com/v1", "https://api.openai.com/v1"},     // already v1
		{"https://api.z.ai/api/paas/v4", "https://api.z.ai/api/paas/v4/v1"}, // v4 endpoint, still needs v1
	}
	for _, c := range cases {
		got := ensureV1(c.in)
		if got != c.want {
			t.Errorf("ensureV1(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
