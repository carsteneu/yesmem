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
	m := buildAutoProviderTargets(providers, nil)
	if m["deepseek-chat"] != "https://api.deepseek.com" {
		t.Fatal("deepseek-chat mapping wrong")
	}
	if m["big-pickle"] != "https://opencode.ai/zen" {
		t.Fatal("big-pickle mapping wrong")
	}
}

func TestBuildAutoProviderTargetsRespectsProviderTargetsExact(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "zai-coding-plan", UpstreamURL: "https://api.z.ai/api/coding/paas/v4"},
		{ModelID: "glm-5.2", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
	}
	providerTargets := map[string]string{
		"glm-5.2": "https://api.z.ai/api/coding/paas/v4",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if _, present := m["glm-5.2"]; present {
		t.Fatal("bare glm-5.2 should be skipped — covered by provider_targets (exact match)")
	}
	if m["zai-coding-plan/glm-5.2"] != "https://api.z.ai/api/coding/paas/v4" {
		t.Fatalf("zai-coding-plan/glm-5.2 qualified key wrong: %s", m["zai-coding-plan/glm-5.2"])
	}
	if m["opencode/glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("opencode/glm-5.2 qualified key wrong: %s", m["opencode/glm-5.2"])
	}
}

func TestBuildAutoProviderTargetsRespectsProviderTargetsPrefix(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
	}
	providerTargets := map[string]string{
		"glm": "https://api.z.ai/api/coding/paas/v4",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if _, present := m["glm-5.2"]; present {
		t.Fatal("bare glm-5.2 should be skipped — covered by provider_targets prefix 'glm'")
	}
	if m["opencode/glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("opencode/glm-5.2 qualified key wrong: %s", m["opencode/glm-5.2"])
	}
}

func TestBuildAutoProviderTargetsFirstWinsDeterministic(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
		{ModelID: "glm-5.2", ProviderID: "zai-coding-plan", UpstreamURL: "https://api.z.ai/api/coding/paas/v4"},
	}
	m := buildAutoProviderTargets(providers, nil)
	if m["glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("bare glm-5.2 should resolve to alphabetically-first provider (opencode), got %s", m["glm-5.2"])
	}
	if m["opencode/glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("opencode/glm-5.2 wrong: %s", m["opencode/glm-5.2"])
	}
	if m["zai-coding-plan/glm-5.2"] != "https://api.z.ai/api/coding/paas/v4" {
		t.Fatalf("zai-coding-plan/glm-5.2 wrong: %s", m["zai-coding-plan/glm-5.2"])
	}
}

func TestBuildAutoProviderTargetsNotInProviderTargetsAdded(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "deepseek-chat", ProviderID: "deepseek", UpstreamURL: "https://api.deepseek.com"},
	}
	providerTargets := map[string]string{
		"glm-5.2": "https://api.z.ai/api/coding/paas/v4",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if m["deepseek-chat"] != "https://api.deepseek.com" {
		t.Fatalf("deepseek-chat should be added normally: %s", m["deepseek-chat"])
	}
	if m["deepseek/deepseek-chat"] != "https://api.deepseek.com" {
		t.Fatalf("deepseek/deepseek-chat qualified key wrong: %s", m["deepseek/deepseek-chat"])
	}
}

func TestBuildAutoProviderTargetsEmptyURLProviderTargetsIgnored(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
	}
	providerTargets := map[string]string{
		"glm-5.2": "",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if m["glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("empty-URL provider_targets entry should be ignored, bare glm-5.2 added: %s", m["glm-5.2"])
	}
}

func TestBuildAutoProviderTargetsCaseInsensitive(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "GLM-5.2", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
	}
	providerTargets := map[string]string{
		"glm-5.2": "https://api.z.ai/api/coding/paas/v4",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if _, present := m["glm-5.2"]; present {
		t.Fatal("GLM-5.2 should be skipped — case-insensitive match against provider_targets 'glm-5.2'")
	}
	if m["opencode/glm-5.2"] != "https://opencode.ai/zen/v1" {
		t.Fatalf("opencode/glm-5.2 qualified key wrong: %s", m["opencode/glm-5.2"])
	}
}

func TestBuildAutoProviderTargetsPrefixWithoutDash(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glmrock", ProviderID: "opencode", UpstreamURL: "https://opencode.ai/zen/v1"},
	}
	providerTargets := map[string]string{
		"glm": "https://api.z.ai/api/coding/paas/v4",
	}
	m := buildAutoProviderTargets(providers, providerTargets)
	if _, present := m["glmrock"]; present {
		t.Fatal("glmrock should be skipped — prefix match 'glm' covers it (no dash required)")
	}
}

func TestBuildModelProviderMap(t *testing.T) {
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "zai", UpstreamURL: "https://api.z.ai"},
		{ModelID: "glm-5.2", ProviderID: "zai-coding-plan", UpstreamURL: "https://api.z.ai"},
		{ModelID: "deepseek-v4-pro", ProviderID: "zai", UpstreamURL: "https://api.z.ai"},
	}
	m := buildModelProviderMap(providers)
	// Both zai and zai-coding-plan carry glm-5.2; alphabetically first wins → zai.
	if m["glm-5.2"] != "zai" {
		t.Fatalf("glm-5.2 should map to zai (alphabetically first), got %q", m["glm-5.2"])
	}
	if m["deepseek-v4-pro"] != "zai" {
		t.Fatalf("deepseek-v4-pro should map to zai, got %q", m["deepseek-v4-pro"])
	}
	if _, present := m["zai-coding-plan"]; present {
		t.Fatal("providerID should not appear as a key (only bare modelIDs are keys)")
	}
}

func TestBuildModelProviderMapCodingVariantIncludedWhenOnlyProvider(t *testing.T) {
	// Only a coding-variant provider carries this modelID — it must be included.
	// This is the real-world glm-5.2 case: zai-coding-plan is the only provider.
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "zai-coding-plan", UpstreamURL: "https://api.z.ai"},
	}
	m := buildModelProviderMap(providers)
	if m["glm-5.2"] != "zai-coding-plan" {
		t.Fatalf("glm-5.2 should map to zai-coding-plan when it's the only provider, got %q", m["glm-5.2"])
	}
}

func TestBuildModelProviderMapEmpty(t *testing.T) {
	m := buildModelProviderMap(nil)
	if m == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestBuildModelProviderMapDuplicateDeterministic(t *testing.T) {
	// Two providers serve the same bare modelID. The alphabetically
	// first ProviderID must win deterministically regardless of input order,
	// because buildModelProviderMap sorts before dedup.
	providers := []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "zai", UpstreamURL: "https://api.z.ai"},
		{ModelID: "glm-5.2", ProviderID: "openrouter", UpstreamURL: "https://openrouter.ai"},
	}
	m := buildModelProviderMap(providers)
	if m["glm-5.2"] != "openrouter" {
		t.Fatalf("alphabetically first ProviderID should win; got %q, want openrouter", m["glm-5.2"])
	}

	// Reverse input order — same result (sort makes it order-independent).
	providers = []autoDiscoveredProvider{
		{ModelID: "glm-5.2", ProviderID: "openrouter", UpstreamURL: "https://openrouter.ai"},
		{ModelID: "glm-5.2", ProviderID: "zai", UpstreamURL: "https://api.z.ai"},
	}
	m = buildModelProviderMap(providers)
	if m["glm-5.2"] != "openrouter" {
		t.Fatalf("alphabetically first ProviderID should win regardless of input order; got %q, want openrouter", m["glm-5.2"])
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

func TestDiscoverOpenAICompatibleProviders_OpencodeFreeTierIncluded(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"opencode": {
			ID: "opencode", NPM: openaiCompatibleNPM, API: "https://opencode.ai/zen/v1",
			Models: map[string]modelsJSONModel{
				"big-pickle":        {ID: "big-pickle"},
				"qwen3.6-plus-free": {ID: "qwen3.6-plus-free"},
			},
		},
	}
	opencode := map[string]opencodeProviderBlock{
		"opencode": {Options: opencodeProviderOptions{BaseURL: "http://localhost:9099/v1"}},
	}

	active, inactive := discoverOpenAICompatibleProviders(models, opencode, nil)
	if len(active) != 2 {
		t.Fatalf("expected 2 active free-tier opencode models (big-pickle + qwen3.6-plus-free), got %d: %v", len(active), active)
	}
	if len(inactive) != 0 {
		t.Fatalf("expected 0 inactive, got %d", len(inactive))
	}
	for _, p := range active {
		if p.ProviderID != "opencode" {
			t.Fatalf("expected opencode provider, got %s", p.ProviderID)
		}
		if p.UpstreamURL != "https://opencode.ai/zen/v1" {
			t.Fatalf("opencode upstream URL mismatch, got %s", p.UpstreamURL)
		}
	}
}

func TestDiscoverOpenAICompatibleProviders_OpencodeAbsentNotIncluded(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"opencode": {
			ID: "opencode", NPM: openaiCompatibleNPM, API: "https://opencode.ai/zen/v1",
			Models: map[string]modelsJSONModel{"big-pickle": {ID: "big-pickle"}},
		},
	}
	opencode := map[string]opencodeProviderBlock{
		"deepseek": {APIKey: "sk-deepseek-key"},
	}

	active, inactive := discoverOpenAICompatibleProviders(models, opencode, nil)
	if len(active) != 0 {
		t.Fatalf("expected 0 active (opencode not in user config), got %d: %v", len(active), active)
	}
	if len(inactive) != 0 {
		t.Fatalf("expected 0 inactive (opencode not in user config), got %d", len(inactive))
	}
}

func TestDiscoverOpenAICompatibleProviders_NonOpencodeStillRequiresCredentials(t *testing.T) {
	models := map[string]modelsJSONEntry{
		"deepseek": {
			ID: "deepseek", NPM: openaiCompatibleNPM, API: "https://api.deepseek.com",
			Models: map[string]modelsJSONModel{"deepseek-chat": {ID: "deepseek-chat"}},
		},
		"openai": {
			ID: "openai", NPM: "@ai-sdk/openai",
			Models: map[string]modelsJSONModel{"gpt-5.5": {ID: "gpt-5.5"}},
		},
	}
	opencode := map[string]opencodeProviderBlock{
		"deepseek": {Options: opencodeProviderOptions{BaseURL: "http://localhost:9099/v1"}},
		"openai":   {Options: opencodeProviderOptions{BaseURL: "http://localhost:9099/v1"}},
	}

	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	active, inactive := discoverOpenAICompatibleProviders(models, opencode, nil)
	if len(active) != 0 {
		t.Fatalf("expected 0 active (non-opencode providers need credentials), got %d: %v", len(active), active)
	}
	if len(inactive) != 0 {
		t.Fatalf("expected 0 inactive, got %d", len(inactive))
	}
}
