package setup

import (
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

	err := mergeOpencodeJSON(dir, "/test/plugin/index.ts")
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

	err := mergeOpencodeJSON(dir, "/test/plugin/index.ts")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	count := strings.Count(string(data), "/test/plugin/index.ts")
	if count != 1 {
		t.Errorf("expected 1 plugin entry, got %d: %s", count, string(data))
	}
}
