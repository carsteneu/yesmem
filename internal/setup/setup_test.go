package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateConfigAnthropicProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-ant-test", "", "", "api", "ghostty", "", "", "")
	if !strings.Contains(cfg, "provider: api") {
		t.Fatalf("config missing anthropic provider: %s", cfg)
	}
	if !strings.Contains(cfg, "api_key: sk-ant-test") {
		t.Fatalf("config missing anthropic api_key: %s", cfg)
	}
}

func TestGenerateConfigOpenAIProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-anthropic-key", "sk-openai-key", "https://api.openai.com/v1", "openai", "ghostty", "", "", "")
	if !strings.Contains(cfg, "provider: openai") {
		t.Fatalf("config missing openai provider: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_api_key: sk-openai-key") {
		t.Fatalf("config missing openai_api_key: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_base_url: https://api.openai.com/v1") {
		t.Fatalf("config missing resolved openai_base_url: %s", cfg)
	}
}

func TestGenerateConfigOpenAICompatibleProvider(t *testing.T) {
	cfg := generateConfig("deepseek-v4-flash", true, "sk-ant-key", "sk-deepseek-key", "https://api.deepseek.com/v1", "openai_compatible", "ghostty", "", "", "")
	if !strings.Contains(cfg, "provider: openai_compatible") {
		t.Fatalf("config missing openai_compatible provider: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_api_key: sk-deepseek-key") {
		t.Fatalf("config missing openai_api_key: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_base_url: https://api.deepseek.com/v1") {
		t.Fatalf("config missing openai_base_url: %s", cfg)
	}
}

func TestGenerateConfigContainsActiveFields(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-test", "", "", "api", "ghostty", "", "", "")
	checks := []string{
		"opencode_db:",
		"openai_target:",
		"max_budget_per_call_usd:",
		"remind_open_work:",
		"max_runtime:",
		"max_turns:",
		"max_depth:",
		"viewer_terminal:",
		"token_budget:",
	}
	for _, check := range checks {
		if !strings.Contains(cfg, check) {
			t.Errorf("config missing active field %q", check)
		}
	}
}

func TestGenerateConfigCLIProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "", "", "", "cli", "ghostty", "", "", "")
	if !strings.Contains(cfg, "provider: cli") {
		t.Fatalf("config missing cli provider: %s", cfg)
	}
	// CLI provider with empty apiKey should use env var placeholder
	if !strings.Contains(cfg, "api_key: ${ANTHROPIC_API_KEY}") {
		t.Fatalf("CLI config should have env var placeholder: %s", cfg)
	}
	// Should not contain openai fields
	if strings.Contains(cfg, "openai_api_key:") {
		t.Fatalf("CLI config should not contain openai_api_key: %s", cfg)
	}
}

func TestGenerateConfigOpencodeProvider(t *testing.T) {
	cfg := generateConfig("deepseek-v4-pro", true, "", "sk-deepseek", "https://api.deepseek.com", "opencode", "ghostty", "opus-4-7", "sonnet", "haiku")
	if !strings.Contains(cfg, "provider: opencode") {
		t.Fatalf("config missing opencode provider: %s", cfg)
	}
	if !strings.Contains(cfg, "provider_targets: {}") {
		t.Fatalf("config should have empty provider_targets: %s", cfg)
	}
	if !strings.Contains(cfg, "auto_configure_providers: true") {
		t.Fatalf("config missing auto_configure_providers: %s", cfg)
	}
	// Opencode has no API key field
	if strings.Contains(cfg, "openai_api_key:") {
		t.Fatalf("opencode config should not contain openai_api_key: %s", cfg)
	}
}

func TestGenerateConfigDynamicModels(t *testing.T) {
	cfg := generateConfig("flash-model", true, "", "sk-key", "https://api.test.com/v1", "openai_compatible", "ghostty", "narrative-model", "quality-model", "summarize-model")
	if !strings.Contains(cfg, "summarize_model: summarize-model") {
		t.Fatalf("config missing dynamic summarize_model: %s", cfg)
	}
	if !strings.Contains(cfg, "model: flash-model") {
		t.Fatalf("config missing dynamic extraction model: %s", cfg)
	}
	if !strings.Contains(cfg, "narrative_model: narrative-model") {
		t.Fatalf("config missing dynamic narrative_model: %s", cfg)
	}
	if !strings.Contains(cfg, "quality_model: quality-model") {
		t.Fatalf("config missing dynamic quality_model: %s", cfg)
	}
}

func TestGenerateConfigLLMOpenAIBaseURL(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-ant-key", "sk-oai-key", "https://api.test.com/v1", "openai_compatible", "ghostty", "", "", "")
	if !strings.Contains(cfg, "llm:") {
		t.Fatalf("config missing llm section: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_base_url: https://api.test.com/v1") {
		t.Fatalf("config missing llm.openai_base_url: %s", cfg)
	}
}

func TestMergeOpencodeJSON_AddsPlugin(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "opencode.json")
	os.WriteFile(cfgPath, []byte(`{"$schema": "https://opencode.ai/config.json"}`), 0644)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	err := mergeOpencodeJSON(dir, "/test/plugin/index.ts", "", "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "/test/plugin/index.ts") {
		t.Errorf("plugin entry missing: %s", string(data))
	}
}

func TestMergeOpencodeJSON_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	cfgPath := filepath.Join(cfgDir, "opencode.json")
	os.WriteFile(cfgPath, []byte(`{"$schema":"https://opencode.ai/config.json","plugin":["/test/plugin/index.ts"]}`), 0644)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	err := mergeOpencodeJSON(dir, "/test/plugin/index.ts", "", "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	count := strings.Count(string(data), "/test/plugin/index.ts")
	if count != 1 {
		t.Errorf("expected 1 plugin entry, got %d: %s", count, string(data))
	}
}

// Regression: step 7d (installOpencodePlugin) used to call mergeOpencodeSettings
// WITHOUT the user-chosen model, which wrote the hardcoded deepseek-reasoner
// default. Step 7d2 (mergeOpencodeSettingsWith) was then unable to overwrite it
// because deepMergeJSON preserves existing scalars. Result: users who chose
// big-pickle (or any non-DeepSeek model) at install time still ended up with
// deepseek/deepseek-reasoner in opencode.json.
//
// Fix: thread primaryModel/smallModel through installOpencodePlugin and
// mergeOpencodeJSON so the user's choice is written in a single pass.
func TestInstallOpencodePlugin_PreservesChosenModel(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	// Fresh opencode install: no opencode.json present, so opencodeConfigPath
	// resolves to opencode.jsonc (opencode's preferred format). Writes must
	// land there, not in the legacy opencode.json.
	cfgPath := filepath.Join(cfgDir, "opencode.jsonc")

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	// Simulate step 7d with the user-chosen model threaded through.
	if err := installOpencodePlugin(dir, "/test/yesmem", "opencode/big-pickle", "opencode/big-pickle"); err != nil {
		t.Fatalf("installOpencodePlugin: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read opencode.jsonc: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m := cfg["model"]; m != "opencode/big-pickle" {
		t.Errorf("model = %v, want opencode/big-pickle (user-chosen model must win over hardcoded deepseek default)", m)
	}
	if m := cfg["small_model"]; m != "opencode/big-pickle" {
		t.Errorf("small_model = %v, want opencode/big-pickle", m)
	}
}

// TestVerifyLLMConnection_UsesActualModel verifies the model parameter threads
// through to the resolver instead of being silently replaced by the hardcoded
// "haiku" tier shortname. For arbitrary model IDs (e.g. opencode free-tier
// "big-pickle") the value must pass through unchanged so the opencode CLI
// subprocess receives a model that actually exists in the user's config.
func TestVerifyLLMConnection_UsesActualModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		// Arbitrary model IDs must pass through unchanged for every provider.
		{"opencode big-pickle", "opencode", "big-pickle", "big-pickle"},
		{"api custom model", "api", "claude-custom-test", "claude-custom-test"},
		{"openai_compatible custom", "openai_compatible", "deepseek-v4-pro", "deepseek-v4-pro"},
		// Tier shortnames still resolve to provider-specific IDs.
		{"api haiku tier", "api", "haiku", "claude-haiku-4-5-20251001"},
		{"openai haiku tier", "openai", "haiku", "gpt-5-mini"},
		{"api sonnet tier", "api", "sonnet", "claude-sonnet-4-6"},
		// Empty model falls back to haiku tier semantics.
		{"empty model api", "api", "", "claude-haiku-4-5-20251001"},
		{"empty model openai", "openai", "", "gpt-5-mini"},
		{"whitespace model api", "api", "   ", "claude-haiku-4-5-20251001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveLLMModelIDForVerify(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("resolveLLMModelIDForVerify(%q, %q) = %q, want %q",
					tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
