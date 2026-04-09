package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Extraction.Model != "sonnet" {
		t.Errorf("default model: got %q, want 'sonnet'", cfg.Extraction.Model)
	}
	if cfg.Extraction.Mode != "prefiltered" {
		t.Errorf("default mode: got %q, want 'prefiltered'", cfg.Extraction.Mode)
	}
	if cfg.Extraction.ChunkSize != 25000 {
		t.Errorf("default chunk_size: got %d, want 25000", cfg.Extraction.ChunkSize)
	}
	if !cfg.Extraction.AutoExtract {
		t.Error("auto_extract should default to true")
	}
	if cfg.Briefing.DetailedSessions != 3 {
		t.Errorf("default detailed_sessions: got %d, want 3", cfg.Briefing.DetailedSessions)
	}
	if cfg.Briefing.DedupThreshold != 0.4 {
		t.Errorf("default dedup_threshold: got %f, want 0.4", cfg.Briefing.DedupThreshold)
	}
	if cfg.Briefing.MaxPerCategory != 5 {
		t.Errorf("default max_per_category: got %d, want 5", cfg.Briefing.MaxPerCategory)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(`
extraction:
  model: opus
  mode: full
  chunk_size: 30000
briefing:
  detailed_sessions: 20
`), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Extraction.Model != "opus" {
		t.Errorf("model: got %q, want 'opus'", cfg.Extraction.Model)
	}
	if cfg.Extraction.ChunkSize != 30000 {
		t.Errorf("chunk_size: got %d, want 30000", cfg.Extraction.ChunkSize)
	}
	if cfg.Briefing.DetailedSessions != 20 {
		t.Errorf("detailed_sessions: got %d, want 20", cfg.Briefing.DetailedSessions)
	}
	// Defaults preserved for unset fields
	if !cfg.Extraction.AutoExtract {
		t.Error("auto_extract should still be true (default)")
	}
}

func TestLoadMissing(t *testing.T) {
	cfg, err := Load("/nonexistent/config.yaml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.Extraction.Model != "sonnet" {
		t.Error("missing file should use defaults")
	}
}

func TestModelID(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"opus", "claude-opus-4-6"},
		{"sonnet", "claude-sonnet-4-6"},
		{"haiku", "claude-haiku-4-5-20251001"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		cfg := Default()
		cfg.Extraction.Model = tt.model
		if got := cfg.ModelID(); got != tt.want {
			t.Errorf("ModelID(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestNormalizeLLMProvider(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "auto"},
		{" auto ", "auto"},
		{"anthropic", "api"},
		{"API", "api"},
		{"openai", "openai"},
		{"OPENAI_COMPATIBLE", "openai_compatible"},
	}

	for _, tt := range tests {
		if got := NormalizeLLMProvider(tt.in); got != tt.want {
			t.Errorf("NormalizeLLMProvider(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsOpenAIProvider(t *testing.T) {
	if !IsOpenAIProvider("openai") {
		t.Fatal("openai should be recognized as OpenAI provider")
	}
	if !IsOpenAIProvider("openai_compatible") {
		t.Fatal("openai_compatible should be recognized as OpenAI provider")
	}
	if IsOpenAIProvider("api") {
		t.Fatal("api should not be recognized as OpenAI provider")
	}
}

func TestResolveModelIDForProviderOpenAI(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", "gpt-5.2"},
		{"haiku", "gpt-5-mini"},
		{"sonnet", "gpt-5.2"},
		{"opus", "gpt-5.4"},
		{"gpt-5.2-codex", "gpt-5.2-codex"},
	}

	for _, tt := range tests {
		got := ResolveModelIDForProvider("openai", tt.name, "claude-sonnet-4-6", "gpt-5.2")
		if got != tt.want {
			t.Errorf("ResolveModelIDForProvider(openai, %q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestResolvedAPIKeyAnthropicAndOpenAI(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")

		cfg := Default()
		cfg.LLM.Provider = "api"
		cfg.API.APIKey = "cfg-anthropic"
		cfg.API.OpenAIAPIKey = "cfg-openai"

		if got := cfg.ResolvedAPIKey(); got != "cfg-anthropic" {
			t.Fatalf("ResolvedAPIKey() = %q, want anthropic key", got)
		}

		t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
		if got := cfg.ResolvedAPIKey(); got != "env-anthropic" {
			t.Fatalf("ResolvedAPIKey() with env = %q, want env-anthropic", got)
		}
	})

	t.Run("openai", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
		t.Setenv("OPENAI_API_KEY", "")

		cfg := Default()
		cfg.LLM.Provider = "openai"
		cfg.API.APIKey = "cfg-anthropic"
		cfg.API.OpenAIAPIKey = "cfg-openai"

		if got := cfg.ResolvedAPIKey(); got != "cfg-openai" {
			t.Fatalf("ResolvedAPIKey() = %q, want cfg-openai", got)
		}

		t.Setenv("OPENAI_API_KEY", "env-openai")
		if got := cfg.ResolvedAPIKey(); got != "env-openai" {
			t.Fatalf("ResolvedAPIKey() with env = %q, want env-openai", got)
		}
	})
}

func TestResolvedOpenAIBaseURL(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "")

	cfg := Default()
	cfg.API.OpenAIBaseURL = " https://example.test/openai "
	if got := cfg.ResolvedOpenAIBaseURL(); got != "https://example.test/openai" {
		t.Fatalf("ResolvedOpenAIBaseURL() = %q", got)
	}

	t.Setenv("OPENAI_BASE_URL", "https://env.example/v1")
	if got := cfg.ResolvedOpenAIBaseURL(); got != "https://env.example/v1" {
		t.Fatalf("ResolvedOpenAIBaseURL() with env = %q", got)
	}
}

func TestLoadExpandsProviderEnvRefs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-anthropic")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	t.Setenv("OPENAI_BASE_URL", "https://openai.example/v1")

	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte(`
llm:
  provider: openai_compatible
api:
  api_key: ${ANTHROPIC_API_KEY}
  openai_api_key: ${OPENAI_API_KEY}
  openai_base_url: ${OPENAI_BASE_URL}
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.API.APIKey != "env-anthropic" {
		t.Fatalf("API.APIKey = %q, want env-anthropic", cfg.API.APIKey)
	}
	if cfg.API.OpenAIAPIKey != "env-openai" {
		t.Fatalf("API.OpenAIAPIKey = %q, want env-openai", cfg.API.OpenAIAPIKey)
	}
	if cfg.API.OpenAIBaseURL != "https://openai.example/v1" {
		t.Fatalf("API.OpenAIBaseURL = %q, want https://openai.example/v1", cfg.API.OpenAIBaseURL)
	}
}

func TestProviderSpecificModelIDs(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "openai"
	cfg.Extraction.Model = "sonnet"
	cfg.Extraction.SummarizeModel = "haiku"
	cfg.Extraction.QualityModel = "opus"
	cfg.Extraction.NarrativeModel = "opus"
	cfg.Signals.Model = "haiku"

	if got := cfg.ModelID(); got != "gpt-5.2" {
		t.Fatalf("ModelID() = %q, want gpt-5.2", got)
	}
	if got := cfg.SummarizeModelID(); got != "gpt-5-mini" {
		t.Fatalf("SummarizeModelID() = %q, want gpt-5-mini", got)
	}
	if got := cfg.QualityModelID(); got != "gpt-5.4" {
		t.Fatalf("QualityModelID() = %q, want gpt-5.4", got)
	}
	if got := cfg.NarrativeModelID(); got != "gpt-5.4" {
		t.Fatalf("NarrativeModelID() = %q, want gpt-5.4", got)
	}
	if got := cfg.SignalsModelID(); got != "gpt-5-mini" {
		t.Fatalf("SignalsModelID() = %q, want gpt-5-mini", got)
	}
}

func TestDefaultSignalsConfig(t *testing.T) {
	cfg := Default()
	if !cfg.Signals.Enabled {
		t.Error("signals should be enabled by default")
	}
	if cfg.Signals.Mode != "reflection" {
		t.Errorf("expected mode=reflection, got %s", cfg.Signals.Mode)
	}
	if cfg.Signals.EveryNTurns != 1 {
		t.Errorf("expected every_n_turns=1, got %d", cfg.Signals.EveryNTurns)
	}
}

func TestSignalsConfigFromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(`
signals:
  enabled: false
  mode: reflection
  every_n_turns: 3
`), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Signals.Enabled {
		t.Error("expected signals disabled")
	}
	if cfg.Signals.EveryNTurns != 3 {
		t.Errorf("expected every_n_turns=3, got %d", cfg.Signals.EveryNTurns)
	}
	// Defaults preserved for unset fields
	if cfg.Signals.Mode != "reflection" {
		t.Errorf("expected mode=reflection, got %s", cfg.Signals.Mode)
	}
}

func TestSignalsModelID(t *testing.T) {
	cfg := Default()
	// Default should be haiku (reflection is classification, not creative)
	if got := cfg.SignalsModelID(); got != "claude-haiku-4-5-20251001" {
		t.Errorf("SignalsModelID() default = %q, want claude-haiku-4-5-20251001", got)
	}
	cfg.Signals.Model = "sonnet"
	if got := cfg.SignalsModelID(); got != "claude-sonnet-4-6" {
		t.Errorf("SignalsModelID() = %q, want claude-sonnet-4-6", got)
	}
}

func TestLoadHTTPConfig(t *testing.T) {
	yaml := `
http:
  enabled: true
  listen: "127.0.0.1:9377"
`
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0644)
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HTTP.Enabled {
		t.Error("HTTP should be enabled")
	}
	if cfg.HTTP.Listen != "127.0.0.1:9377" {
		t.Errorf("listen = %q, want 127.0.0.1:9377", cfg.HTTP.Listen)
	}
	if cfg.HTTP.AuthToken != "" {
		t.Error("auth token should be empty in config")
	}
}

func TestDefaultHTTPConfig(t *testing.T) {
	cfg := Default()
	if cfg.HTTP.Enabled {
		t.Error("HTTP should be disabled by default")
	}
	if cfg.HTTP.Listen != "127.0.0.1:9377" {
		t.Errorf("default listen = %q, want 127.0.0.1:9377", cfg.HTTP.Listen)
	}
}

func TestDefaultConfigHasUpdateSection(t *testing.T) {
	cfg := Default()
	if !cfg.Update.AutoUpdate {
		t.Error("auto_update should default to true")
	}
	if cfg.Update.CheckInterval != "6h" {
		t.Errorf("check_interval = %q, want 6h", cfg.Update.CheckInterval)
	}
	if cfg.Update.Channel != "stable" {
		t.Errorf("channel = %q, want stable", cfg.Update.Channel)
	}
}

func TestPricingForModelDefaults(t *testing.T) {
	cfg := Default()
	tests := []struct {
		model         string
		wantInput     float64
		wantOutput    float64
	}{
		{"claude-haiku-4-5-20251001", 1.0, 5.0},
		{"claude-sonnet-4-6", 3.0, 15.0},
		{"claude-opus-4-6", 5.0, 25.0},
		{"gpt-5-mini", 0.25, 2.0},
		{"gpt-5.2-codex", 1.75, 14.0},
		{"gpt-5.4", 2.5, 15.0},
		{"unknown-model", 3.0, 15.0}, // fallback to sonnet
	}
	for _, tt := range tests {
		in, out := cfg.PricingForModel(tt.model)
		if in != tt.wantInput || out != tt.wantOutput {
			t.Errorf("PricingForModel(%q) = (%v, %v), want (%v, %v)", tt.model, in, out, tt.wantInput, tt.wantOutput)
		}
	}
}

func TestPricingForModelConfigOverride(t *testing.T) {
	cfg := Default()
	cfg.Pricing["haiku"] = ModelPricing{Input: 0.5, Output: 2.5}
	cfg.Pricing["my-custom"] = ModelPricing{Input: 10.0, Output: 50.0}

	in, out := cfg.PricingForModel("claude-haiku-4-5-20251001")
	if in != 0.5 || out != 2.5 {
		t.Errorf("overridden haiku = (%v, %v), want (0.5, 2.5)", in, out)
	}

	in, out = cfg.PricingForModel("my-custom")
	if in != 10.0 || out != 50.0 {
		t.Errorf("custom model = (%v, %v), want (10.0, 50.0)", in, out)
	}
}

func TestForkedAgentsConfig(t *testing.T) {
	cfg := Default()
	if cfg.ForkedAgents.Enabled {
		t.Error("forked agents should be disabled by default")
	}
	if cfg.ForkedAgents.TokenGrowthTrigger != 20000 {
		t.Errorf("expected token_growth_trigger=20000, got %d", cfg.ForkedAgents.TokenGrowthTrigger)
	}
	if cfg.ForkedAgents.MaxForksPerSession != 50 {
		t.Errorf("expected max_forks_per_session=50, got %d", cfg.ForkedAgents.MaxForksPerSession)
	}
	if cfg.ForkedAgents.Model != "sonnet" {
		t.Errorf("expected model=sonnet, got %s", cfg.ForkedAgents.Model)
	}
	if cfg.ForkedAgents.MaxCostPerSession != 5.0 {
		t.Errorf("expected max_cost_per_session=5.0, got %f", cfg.ForkedAgents.MaxCostPerSession)
	}
}

func TestForkedAgentsConfigFromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(`
forked_agents:
  enabled: true
  model: sonnet
  token_growth_trigger: 30000
  max_forks_per_session: 20
`), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !cfg.ForkedAgents.Enabled {
		t.Error("expected forked agents enabled")
	}
	if cfg.ForkedAgents.TokenGrowthTrigger != 30000 {
		t.Errorf("expected 30000, got %d", cfg.ForkedAgents.TokenGrowthTrigger)
	}
	if cfg.ForkedAgents.MaxForksPerSession != 20 {
		t.Errorf("expected 20, got %d", cfg.ForkedAgents.MaxForksPerSession)
	}
}

func TestForkedAgentsModelID(t *testing.T) {
	cfg := Default()
	cfg.ForkedAgents.Model = "haiku"
	if got := cfg.ForkedAgentsModelID(); got != "claude-haiku-4-5-20251001" {
		t.Errorf("ForkedAgentsModelID() = %q, want claude-haiku-4-5-20251001", got)
	}
	cfg.ForkedAgents.Model = "sonnet"
	if got := cfg.ForkedAgentsModelID(); got != "claude-sonnet-4-6" {
		t.Errorf("ForkedAgentsModelID() = %q, want claude-sonnet-4-6", got)
	}
}

func TestPricingForModelEmptyConfig(t *testing.T) {
	cfg := Default()
	cfg.Pricing = nil // simulate missing pricing section

	in, out := cfg.PricingForModel("claude-sonnet-4-6")
	if in != 3.0 || out != 15.0 {
		t.Errorf("nil pricing sonnet = (%v, %v), want (3.0, 15.0)", in, out)
	}
}
