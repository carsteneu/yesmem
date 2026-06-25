package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// configYAML is a minimal struct to verify the generated config parses as valid YAML
// and contains the expected top-level structure.
type configYAML struct {
	Extraction struct {
		SummarizeModel string `yaml:"summarize_model"`
		Model          string `yaml:"model"`
		NarrativeModel string `yaml:"narrative_model"`
		QualityModel   string `yaml:"quality_model"`
		Mode           string `yaml:"mode"`
		ChunkSize      int    `yaml:"chunk_size"`
		AutoExtract    bool   `yaml:"auto_extract"`
	} `yaml:"extraction"`
	LLM struct {
		Provider        string `yaml:"provider"`
		OpenAIBaseURL   string `yaml:"openai_base_url"`
		CompleteProvider string `yaml:"complete_provider"`
	} `yaml:"llm"`
	API struct {
		APIKey       string `yaml:"api_key"`
		OpenAIAPIKey string `yaml:"openai_api_key"`
		OpenAIBaseURL string `yaml:"openai_base_url"`
	} `yaml:"api"`
	Proxy struct {
		Enabled              bool                   `yaml:"enabled"`
		ProviderTargets      map[string]string      `yaml:"provider_targets"`
		AutoConfigureProviders bool                  `yaml:"auto_configure_providers"`
	} `yaml:"proxy"`
}

// parseGeneratedConfig generates config with the given parameters and parses it as YAML.
// Fails the test if the config cannot be parsed.
func parseGeneratedConfig(t *testing.T, model string, autoExtract bool, apiKey, openaiKey, openaiBaseURL, provider, terminal, narrativeModel, qualityModel, summarizeModel string) configYAML {
	t.Helper()
	cfgStr := generateConfig(model, autoExtract, apiKey, openaiKey, openaiBaseURL, provider, terminal, narrativeModel, qualityModel, summarizeModel)
	var cfg configYAML
	if err := yaml.Unmarshal([]byte(cfgStr), &cfg); err != nil {
		t.Fatalf("generated config is not valid YAML: %v\n--- config ---\n%s", err, cfgStr)
	}
	return cfg
}

// E2E Test 1: Claude + Codex path (provider=cli, narrativeModel=opus-4-7)
func TestE2E_ClaudeCodexPath_GeneratesValidYAML(t *testing.T) {
	cfg := parseGeneratedConfig(t,
		"sonnet",            // extraction model
		true,                // autoExtract
		"sk-ant-test",       // apiKey (Anthropic)
		"",                  // openaiKey
		"",                  // openaiBaseURL
		"cli",               // provider
		"ghostty",           // terminal
		"opus-4-7",          // narrativeModel
		"sonnet",            // qualityModel
		"haiku",             // summarizeModel
	)

	// Verify extraction models
	if cfg.Extraction.Model != "sonnet" {
		t.Errorf("extraction.model: expected sonnet, got %s", cfg.Extraction.Model)
	}
	if cfg.Extraction.NarrativeModel != "opus-4-7" {
		t.Errorf("extraction.narrative_model: expected opus-4-7, got %s", cfg.Extraction.NarrativeModel)
	}
	if cfg.Extraction.QualityModel != "sonnet" {
		t.Errorf("extraction.quality_model: expected sonnet, got %s", cfg.Extraction.QualityModel)
	}
	if cfg.Extraction.SummarizeModel != "haiku" {
		t.Errorf("extraction.summarize_model: expected haiku, got %s", cfg.Extraction.SummarizeModel)
	}

	// Verify LLM provider
	if cfg.LLM.Provider != "cli" {
		t.Errorf("llm.provider: expected cli, got %s", cfg.LLM.Provider)
	}

	// Verify API key
	if cfg.API.APIKey != "sk-ant-test" {
		t.Errorf("api.api_key: expected sk-ant-test, got %s", cfg.API.APIKey)
	}

	// Verify NO openai fields for cli provider
	if cfg.API.OpenAIAPIKey != "" {
		t.Errorf("api.openai_api_key should be empty for cli, got %s", cfg.API.OpenAIAPIKey)
	}
	if cfg.API.OpenAIBaseURL != "" {
		t.Errorf("api.openai_base_url should be empty for cli, got %s", cfg.API.OpenAIBaseURL)
	}

	// Verify provider_targets is empty
	if len(cfg.Proxy.ProviderTargets) != 0 {
		t.Errorf("proxy.provider_targets should be empty, got %v", cfg.Proxy.ProviderTargets)
	}
}

// E2E Test 2: OpenCode path (provider=openai_compatible with DeepSeek URL)
func TestE2E_OpenCodePath_GeneratesValidYAML(t *testing.T) {
	cfg := parseGeneratedConfig(t,
		"deepseek-v4-flash",  // extraction model
		true,
		"sk-ant-fallback",    // apiKey (Anthropic fallback)
		"sk-deepseek-key",    // openaiKey
		"https://api.deepseek.com", // openaiBaseURL
		"openai_compatible",
		"gnome-terminal",
		"deepseek-v4-pro",    // narrativeModel
		"deepseek-v4-pro",    // qualityModel
		"deepseek-v4-flash",  // summarizeModel
	)

	// Verify openai fields
	if cfg.API.OpenAIAPIKey != "sk-deepseek-key" {
		t.Errorf("api.openai_api_key: expected sk-deepseek-key, got %s", cfg.API.OpenAIAPIKey)
	}
	if cfg.API.OpenAIBaseURL != "https://api.deepseek.com" {
		t.Errorf("api.openai_base_url: expected https://api.deepseek.com, got %s", cfg.API.OpenAIBaseURL)
	}
	// Verify llm.openai_base_url is set
	if cfg.LLM.OpenAIBaseURL != "https://api.deepseek.com" {
		t.Errorf("llm.openai_base_url: expected https://api.deepseek.com, got %s", cfg.LLM.OpenAIBaseURL)
	}

	// Verify extraction models
	if cfg.Extraction.Model != "deepseek-v4-flash" {
		t.Errorf("extraction.model: expected deepseek-v4-flash, got %s", cfg.Extraction.Model)
	}
	if cfg.Extraction.NarrativeModel != "deepseek-v4-pro" {
		t.Errorf("extraction.narrative_model: expected deepseek-v4-pro, got %s", cfg.Extraction.NarrativeModel)
	}
}

// E2E Test 3: Codex-only path (provider=openai_compatible with OpenAI URL)
func TestE2E_CodexOnlyPath_GeneratesValidYAML(t *testing.T) {
	cfg := parseGeneratedConfig(t,
		"gpt-5.5-codex",
		true,
		"",
		"sk-openai-key",
		"https://api.openai.com/v1",
		"openai_compatible",
		"gnome-terminal",
		"gpt-5.5-codex",
		"gpt-5.5-codex",
		"gpt-5.5-codex",
	)

	// Verify all 4 extraction models are gpt-5.5-codex
	if cfg.Extraction.Model != "gpt-5.5-codex" {
		t.Errorf("extraction.model: expected gpt-5.5-codex, got %s", cfg.Extraction.Model)
	}
	if cfg.Extraction.NarrativeModel != "gpt-5.5-codex" {
		t.Errorf("extraction.narrative_model: expected gpt-5.5-codex, got %s", cfg.Extraction.NarrativeModel)
	}
	if cfg.Extraction.QualityModel != "gpt-5.5-codex" {
		t.Errorf("extraction.quality_model: expected gpt-5.5-codex, got %s", cfg.Extraction.QualityModel)
	}
	if cfg.Extraction.SummarizeModel != "gpt-5.5-codex" {
		t.Errorf("extraction.summarize_model: expected gpt-5.5-codex, got %s", cfg.Extraction.SummarizeModel)
	}

	// Verify openai URL
	if cfg.API.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Errorf("api.openai_base_url: expected https://api.openai.com/v1, got %s", cfg.API.OpenAIBaseURL)
	}
	if cfg.LLM.OpenAIBaseURL != "https://api.openai.com/v1" {
		t.Errorf("llm.openai_base_url: expected https://api.openai.com/v1, got %s", cfg.LLM.OpenAIBaseURL)
	}
}

// E2E Test 4: Claude-only path (provider=cli, narrativeModel=opus)
func TestE2E_ClaudeOnlyPath_GeneratesValidYAML(t *testing.T) {
	cfg := parseGeneratedConfig(t,
		"sonnet",
		true,
		"sk-ant-claude-key",
		"",
		"",
		"cli",
		"gnome-terminal",
		"opus",
		"sonnet",
		"haiku",
	)

	if cfg.LLM.Provider != "cli" {
		t.Errorf("llm.provider: expected cli, got %s", cfg.LLM.Provider)
	}
	if cfg.API.APIKey != "sk-ant-claude-key" {
		t.Errorf("api.api_key: expected sk-ant-claude-key, got %s", cfg.API.APIKey)
	}
	// No openai fields for cli
	if cfg.API.OpenAIAPIKey != "" {
		t.Errorf("cli path should not have openai_api_key")
	}
}

// E2E Test 5: Default models kick in when empty strings are passed
func TestE2E_DefaultModelsWhenEmpty(t *testing.T) {
	cfg := parseGeneratedConfig(t,
		"sonnet",
		true,
		"sk-test",
		"", "", "api", "xterm",
		"", "", "", // all model overrides empty
	)

	if cfg.Extraction.NarrativeModel != "opus" {
		t.Errorf("default narrative_model should be opus, got %s", cfg.Extraction.NarrativeModel)
	}
	if cfg.Extraction.QualityModel != "sonnet" {
		t.Errorf("default quality_model should be sonnet, got %s", cfg.Extraction.QualityModel)
	}
	if cfg.Extraction.SummarizeModel != "haiku" {
		t.Errorf("default summarize_model should be haiku, got %s", cfg.Extraction.SummarizeModel)
	}
}

// E2E Test 6: proxy.provider_targets is always empty (auto-discovery fills)
func TestE2E_ProviderTargetsAlwaysEmpty(t *testing.T) {
	for _, provider := range []string{"cli", "api", "openai_compatible", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			cfgStr := generateConfig("x", true, "k", "", "", provider, "xterm", "", "", "")
			if strings.Contains(cfgStr, "deepseek:") && strings.Contains(cfgStr, "https://api.deepseek.com") {
				t.Errorf("provider %s: config should NOT contain hardcoded deepseek URL", provider)
			}
			if !strings.Contains(cfgStr, "provider_targets: {}") {
				t.Errorf("provider %s: config should contain 'provider_targets: {}'", provider)
			}
		})
	}
}

// E2E Test 7: generated config has no unresolved placeholders when URL is provided
func TestE2E_NoUnresolvedPlaceholders(t *testing.T) {
	cfgStr := generateConfig("x", true, "sk-test", "sk-openai", "https://api.test.com/v1", "openai_compatible", "xterm", "", "", "")
	if strings.Contains(cfgStr, "${OPENAI_BASE_URL}") {
		t.Errorf("config should not contain ${OPENAI_BASE_URL} placeholder when URL is provided")
	}
	// But should contain placeholder when URL is empty
	cfgStr2 := generateConfig("x", true, "sk-test", "", "", "openai_compatible", "xterm", "", "", "")
	if !strings.Contains(cfgStr2, "${OPENAI_BASE_URL}") {
		t.Errorf("config should contain ${OPENAI_BASE_URL} placeholder when no URL provided (backward compat)")
	}
}

// E2E Test 8: Integration — write config to disk and re-read it
func TestE2E_ConfigFileWriteRead(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfgStr := generateConfig("deepseek-v4-flash", true, "sk-ant", "sk-ds", "https://api.deepseek.com", "openai_compatible", "gnome-terminal", "deepseek-v4-pro", "deepseek-v4-pro", "deepseek-v4-flash")

	if err := os.WriteFile(cfgPath, []byte(cfgStr), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Re-read and verify
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg configYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("re-parse config: %v", err)
	}
	if cfg.Extraction.Model != "deepseek-v4-flash" {
		t.Errorf("re-read extraction.model: expected deepseek-v4-flash, got %s", cfg.Extraction.Model)
	}
	if cfg.LLM.OpenAIBaseURL != "https://api.deepseek.com" {
		t.Errorf("re-read llm.openai_base_url: expected URL, got %s", cfg.LLM.OpenAIBaseURL)
	}
}

// E2E Test 9: OpenCode path produces /v1 URL, dynamic summarize_model, complete_provider
// Simulates what runOpenCodeSetup() passes to generateConfig after ensureV1 normalization.
func TestE2E_OpenCodePath_V1URL_DynamicSummarizeModel_CompleteProvider(t *testing.T) {
	// Simulate resolveOpenCodeProvider output after ensureV1:
	// baseURL="https://api.deepseek.com" → ensureV1 → "https://api.deepseek.com/v1"
	baseURL := ensureV1("https://api.deepseek.com")
	extractionModel := "deepseek-v4-flash"
	narrativeModel := "deepseek-v4-pro"
	qualityModel := narrativeModel
	summarizeModel := extractionModel // Bug B fix: was "haiku" hardcoded

	cfgStr := generateConfig(extractionModel, true, "", "sk-ds-key", baseURL,
		"openai_compatible", "gnome-terminal",
		narrativeModel, qualityModel, summarizeModel)

	// Write + re-read for fidelity
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgStr), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	var cfg configYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Bug A: URL must have /v1 suffix so daemon's normalizeOpenAIURL works
	if cfg.API.OpenAIBaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("api.openai_base_url: expected .../v1, got %s", cfg.API.OpenAIBaseURL)
	}
	if cfg.LLM.OpenAIBaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("llm.openai_base_url: expected .../v1, got %s", cfg.LLM.OpenAIBaseURL)
	}

	// Bug B: summarize_model should match extraction model, not hardcoded haiku
	if cfg.Extraction.SummarizeModel != "deepseek-v4-flash" {
		t.Errorf("summarize_model: expected deepseek-v4-flash, got %s", cfg.Extraction.SummarizeModel)
	}

	// Bug C: complete_provider must be set for openai_compatible
	if cfg.LLM.CompleteProvider != "opencode" {
		t.Errorf("complete_provider: expected opencode, got %s", cfg.LLM.CompleteProvider)
	}

	// Verify the extraction pipeline models are all DeepSeek, not Anthropic
	if cfg.Extraction.Model != "deepseek-v4-flash" {
		t.Errorf("extraction.model: expected deepseek-v4-flash, got %s", cfg.Extraction.Model)
	}
	if cfg.Extraction.NarrativeModel != "deepseek-v4-pro" {
		t.Errorf("narrative_model: expected deepseek-v4-pro, got %s", cfg.Extraction.NarrativeModel)
	}
	if cfg.Extraction.QualityModel != "deepseek-v4-pro" {
		t.Errorf("quality_model: expected deepseek-v4-pro, got %s", cfg.Extraction.QualityModel)
	}
}
