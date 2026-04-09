package setup

import (
	"strings"
	"testing"
)

func TestGenerateConfigAnthropicProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-ant-test", "api", "ghostty")
	if !strings.Contains(cfg, "provider: api") {
		t.Fatalf("config missing anthropic provider: %s", cfg)
	}
	if !strings.Contains(cfg, "api_key: sk-ant-test") {
		t.Fatalf("config missing anthropic api_key: %s", cfg)
	}
}

func TestGenerateConfigOpenAIProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-openai-test", "openai", "ghostty")
	if !strings.Contains(cfg, "provider: openai") {
		t.Fatalf("config missing openai provider: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_api_key: sk-openai-test") {
		t.Fatalf("config missing openai_api_key: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_base_url: ${OPENAI_BASE_URL}") {
		t.Fatalf("config missing openai_base_url placeholder: %s", cfg)
	}
}

func TestGenerateConfigOpenAICompatibleProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-compat-test", "openai_compatible", "ghostty")
	if !strings.Contains(cfg, "provider: openai_compatible") {
		t.Fatalf("config missing openai_compatible provider: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_api_key: sk-compat-test") {
		t.Fatalf("config missing openai_api_key: %s", cfg)
	}
	if !strings.Contains(cfg, "openai_base_url:") {
		t.Fatalf("config missing openai_base_url: %s", cfg)
	}
}

func TestGenerateConfigContainsCommentedFields(t *testing.T) {
	cfg := generateConfig("sonnet", true, "sk-test", "api", "ghostty")
	checks := []string{
		"# openai_target:",
		"# max_budget_per_call_usd:",
		"# remind_open_work:",
		"# max_runtime:",
		"# max_turns:",
		"# max_depth:",
		"# token_budget:",
	}
	for _, check := range checks {
		if !strings.Contains(cfg, check) {
			t.Errorf("config missing commented field %q", check)
		}
	}
}

func TestGenerateConfigCLIProvider(t *testing.T) {
	cfg := generateConfig("sonnet", true, "", "cli", "ghostty")
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
