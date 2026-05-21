package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModelsJSON(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")

	// Write test data
	testData := map[string]modelsJSONEntry{
		"deepseek": {
			ID:  "deepseek",
			NPM: openaiCompatibleNPM,
			API: "https://api.deepseek.com",
			Models: map[string]modelsJSONModel{
				"deepseek-chat":   {ID: "deepseek-chat"},
				"deepseek-v4-pro": {ID: "deepseek-v4-pro"},
			},
		},
		"opencode": {
			ID:  "opencode",
			NPM: openaiCompatibleNPM,
			API: "https://opencode.ai/zen/v1",
			Models: map[string]modelsJSONModel{
				"big-pickle":        {ID: "big-pickle"},
				"qwen3.6-plus-free": {ID: "qwen3.6-plus-free"},
			},
		},
		"anthropic": {
			ID:  "anthropic",
			NPM: "@ai-sdk/anthropic",
			API: "", // First-party, no API in models.json
			Models: map[string]modelsJSONModel{
				"claude-sonnet-4-6": {ID: "claude-sonnet-4-6"},
			},
		},
	}
	data, _ := json.Marshal(testData)
	os.WriteFile(modelsPath, data, 0644)

	entries, err := loadModelsJSON(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries["deepseek"].API != "https://api.deepseek.com" {
		t.Fatalf("deepseek API mismatch: %s", entries["deepseek"].API)
	}
	if entries["opencode"].Models["big-pickle"].ID != "big-pickle" {
		t.Fatal("opencode model mismatch")
	}
}

func TestLoadModelsJSONMissing(t *testing.T) {
	entries, err := loadModelsJSON("/nonexistent/models.json")
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatal("expected nil for missing file")
	}
}

func TestDiscoverOpenAICompatibleProviders(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"deepseek": {
			ID: "deepseek", NPM: openaiCompatibleNPM, API: "https://api.deepseek.com",
			Models: map[string]modelsJSONModel{"deepseek-chat": {ID: "deepseek-chat"}},
		},
		"opencode": {
			ID: "opencode", NPM: openaiCompatibleNPM, API: "https://opencode.ai/zen/v1",
			Models: map[string]modelsJSONModel{"big-pickle": {ID: "big-pickle"}},
		},
		"anthropic": {
			ID: "anthropic", NPM: "@ai-sdk/anthropic",
			Models: map[string]modelsJSONModel{"claude-sonnet-4-6": {ID: "claude-sonnet-4-6"}},
		},
	}

	opencode := map[string]opencodeProviderBlock{
		"deepseek":  {APIKey: "sk-deepseek-key"},
		"opencode":  {Env: []string{"OPENCODE_API_KEY"}},
		"anthropic": {APIKey: "sk-ant-key"},
	}
	// Set env for opencode provider
	os.Setenv("OPENCODE_API_KEY", "test-key")
	defer os.Unsetenv("OPENCODE_API_KEY")

	active, inactive := discoverOpenAICompatibleProviders(models, opencode, nil)

	if len(active) != 2 {
		t.Fatalf("expected 2 active models (deepseek-chat + big-pickle), got %d", len(active))
	}
	if len(inactive) != 1 {
		t.Fatalf("expected 1 inactive model (claude-sonnet-4-6), got %d", len(inactive))
	}

	// Check active: deepseek-chat
	found := false
	for _, p := range active {
		if p.ModelID == "deepseek-chat" && p.UpstreamURL == "https://api.deepseek.com" {
			found = true
		}
	}
	if !found {
		t.Fatal("deepseek-chat not in active providers")
	}

	// Check inactive: anthropic
	if len(inactive) > 0 && (inactive[0].ModelID != "claude-sonnet-4-6" || inactive[0].IsOpenAICompat) {
		t.Fatal("anthropic should be inactive and non-OpenAI-compatible")
	}
}

func TestBuildAutoProviderTargets(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "deepseek-chat", UpstreamURL: "https://api.deepseek.com"},
		{ModelID: "big-pickle", UpstreamURL: "https://opencode.ai/zen"},
	}
	m := buildAutoProviderTargets(providers)
	if m["deepseek-chat"] != "https://api.deepseek.com" {
		t.Fatal("deepseek-chat mapping wrong")
	}
	if m["big-pickle"] != "https://opencode.ai/zen" {
		t.Fatal("big-pickle mapping wrong")
	}
}

func TestResolveOpenAITargetWithAutoDiscovery(t *testing.T) {
	s := &Server{
		autoProviderTargets: map[string]string{
			"big-pickle": "https://opencode.ai/zen",
		},
		cfg: Config{
			OpenAITargetURL: "https://api.openai.com",
			TargetURL:       "https://api.anthropic.com",
		},
	}

	// Auto-discovered target
	if url := s.resolveOpenAITarget("big-pickle"); url != "https://opencode.ai/zen" {
		t.Fatalf("expected opencode upstream, got %s", url)
	}

	// Fallback to openai_target for unknown model
	if url := s.resolveOpenAITarget("gpt-5.5"); url != "https://api.openai.com" {
		t.Fatalf("expected openai fallback, got %s", url)
	}
}

func TestResolveOpenAITargetExplicitTakesPriority(t *testing.T) {
	s := &Server{
		autoProviderTargets: map[string]string{
			"deepseek-v4-pro": "https://auto-discovered.com",
		},
		cfg: Config{
			ProviderTargets: map[string]string{
				"deepseek": "https://explicit-config.com",
			},
		},
	}

	// Explicit provider_targets takes priority
	if url := s.resolveOpenAITarget("deepseek-v4-pro"); url != "https://explicit-config.com" {
		t.Fatalf("explicit config should take priority, got %s", url)
	}
}

func TestDiscoverProvidersFirstPartyFallback(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"openai": {
			ID: "openai", NPM: "@ai-sdk/openai", API: "", // no API field
			Models: map[string]modelsJSONModel{"gpt-5.5": {ID: "gpt-5.5"}},
		},
	}
	opencode := map[string]opencodeProviderBlock{
		"openai": {APIKey: "sk-openai-key"},
	}

	active, _ := discoverOpenAICompatibleProviders(models, opencode, nil)
	if len(active) != 1 {
		t.Fatalf("expected 1 active OpenAI model, got %d", len(active))
	}
	if active[0].UpstreamURL != "https://api.openai.com" {
		t.Fatalf("expected firstPartyDefault for openai, got %s", active[0].UpstreamURL)
	}
}

func TestDiscoverProvidersSkipsInactive(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"deepseek": {
			ID: "deepseek", NPM: openaiCompatibleNPM, API: "https://api.deepseek.com",
			Models: map[string]modelsJSONModel{"deepseek-chat": {ID: "deepseek-chat"}},
		},
	}
	// No opencode config entries at all — provider not configured
	opencode := map[string]opencodeProviderBlock{}

	active, inactive := discoverOpenAICompatibleProviders(models, opencode, nil)
	if len(active) != 0 {
		t.Fatalf("expected 0 active models (no providers configured), got %d", len(active))
	}
	if len(inactive) != 0 {
		t.Fatalf("expected 0 inactive (no providers configured), got %d", len(inactive))
	}
}

func TestMaybePatchOpenCodeBaseURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// Write opencode.json with no baseURL
	initial := `{
  "provider": {
    "deepseek": {
      "apiKey": "sk-key",
      "options": {}
    }
  }
}`
	os.WriteFile(path, []byte(initial), 0644)

	active := []autoDiscoveredProvider{
		{ProviderID: "deepseek", ModelID: "deepseek-chat", UpstreamURL: "https://api.deepseek.com", IsOpenAICompat: true},
	}

	modified, err := maybePatchOpenCodeBaseURL(path, active)
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected opencode.json to be modified")
	}

	// Verify baseURL was set
	data, _ := os.ReadFile(path)
	if !containsJSONKey(data, "baseURL") {
		t.Fatalf("opencode.json missing baseURL: %s", string(data))
	}
}

func TestMaybePatchOpenCodeBaseURLSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	// Write opencode.json with existing baseURL
	initial := `{
  "provider": {
    "deepseek": {
      "apiKey": "sk-key",
      "options": {
        "baseURL": "https://custom-proxy.com"
      }
    }
  }
}`
	os.WriteFile(path, []byte(initial), 0644)

	active := []autoDiscoveredProvider{
		{ProviderID: "deepseek", ModelID: "deepseek-chat", UpstreamURL: "https://api.deepseek.com", IsOpenAICompat: true},
	}

	modified, err := maybePatchOpenCodeBaseURL(path, active)
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Fatal("expected opencode.json NOT to be modified when baseURL already exists")
	}
}

// containsJSONKey is a simple check for whether a JSON key string appears in the content.
func containsJSONKey(data []byte, key string) bool {
	return containsJSONKeyStr(string(data), key)
}

func containsJSONKeyStr(content, key string) bool {
	for i := 0; i < len(content); i++ {
		if content[i] == '"' && i+len(key)+2 <= len(content) && content[i:i+len(key)+2] == `"`+key+`"` {
			return true
		}
	}
	return false
}

func TestLoadOpenCodeAuth(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// Write test auth file
	authData := `{
  "deepseek": {
    "type": "api",
    "key": "sk-deepseek-key"
  },
  "openai": {
    "type": "api",
    "key": "sk-openai-key"
  }
}`
	os.WriteFile(authPath, []byte(authData), 0644)

	auth, err := loadOpenCodeAuth(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(auth) != 2 {
		t.Fatalf("expected 2 auth entries, got %d", len(auth))
	}
	if auth["deepseek"] != "sk-deepseek-key" {
		t.Fatalf("deepseek key mismatch: %s", auth["deepseek"])
	}
	if auth["openai"] != "sk-openai-key" {
		t.Fatalf("openai key mismatch: %s", auth["openai"])
	}
}

func TestLoadOpenCodeAuthMissing(t *testing.T) {
	auth, err := loadOpenCodeAuth("/nonexistent/auth.json")
	if err != nil {
		t.Fatal(err)
	}
	if auth != nil {
		t.Fatal("expected nil for missing file")
	}
}

func TestHasProviderCredentialsWithAuth(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"deepseek": {
			ID: "deepseek", NPM: openaiCompatibleNPM, API: "https://api.deepseek.com",
			Env:    []string{"DEEPSEEK_API_KEY"},
			Models: map[string]modelsJSONModel{"deepseek-chat": {ID: "deepseek-chat"}},
		},
	}
	auth := map[string]string{
		"deepseek": "sk-deepseek-key",
	}
	block := opencodeProviderBlock{Options: opencodeProviderOptions{BaseURL: "http://localhost:9099/v1"}}

	if !hasProviderCredentials("deepseek", block, models, auth) {
		t.Fatal("deepseek should have credentials via auth.json")
	}

	// No auth entry for unknown provider
	if hasProviderCredentials("unknown", block, models, nil) {
		t.Fatal("unknown provider should not have credentials")
	}
}

func TestDiscoverWithAuthJSON(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"deepseek": {
			ID: "deepseek", NPM: openaiCompatibleNPM, API: "https://api.deepseek.com",
			Models: map[string]modelsJSONModel{"deepseek-chat": {ID: "deepseek-chat"}},
		},
	}
	opencode := map[string]opencodeProviderBlock{
		"deepseek": {Options: opencodeProviderOptions{BaseURL: "http://localhost:9099/v1"}},
	}
	auth := map[string]string{
		"deepseek": "sk-deepseek-key",
	}

	active, _ := discoverOpenAICompatibleProviders(models, opencode, auth)
	if len(active) != 1 {
		t.Fatalf("expected 1 active model via auth.json, got %d", len(active))
	}
	if active[0].ModelID != "deepseek-chat" {
		t.Fatalf("expected deepseek-chat, got %s", active[0].ModelID)
	}
}
