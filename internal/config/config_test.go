package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
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
		model      string
		wantInput  float64
		wantOutput float64
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

func TestSecretsSanitization_DefaultDisabled(t *testing.T) {
	cfg := Default()
	if cfg.SecretsSanitization.Enabled {
		t.Fatalf("expected SecretsSanitization disabled by default")
	}
}

func TestSecretsSanitization_AllowedExceptionsEmpty(t *testing.T) {
	cfg := Default()
	if len(cfg.SecretsSanitization.AllowedExceptions) != 0 {
		t.Fatalf("expected no allowed exceptions by default")
	}
}

func TestSecretsSanitization_FromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	yaml := []byte("secrets_sanitization:\n  enabled: true\n  allowed_exceptions:\n    - carsten@example.com\n    - support@example.com\n")
	if err := os.WriteFile(cfgFile, yaml, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SecretsSanitization.Enabled {
		t.Errorf("expected Enabled=true, got false")
	}
	if got := len(cfg.SecretsSanitization.AllowedExceptions); got != 2 {
		t.Fatalf("expected 2 allowed exceptions, got %d", got)
	}
	if cfg.SecretsSanitization.AllowedExceptions[0] != "carsten@example.com" {
		t.Errorf("expected carsten@example.com first, got %q", cfg.SecretsSanitization.AllowedExceptions[0])
	}
	if cfg.SecretsSanitization.AllowedExceptions[1] != "support@example.com" {
		t.Errorf("expected support@example.com second, got %q", cfg.SecretsSanitization.AllowedExceptions[1])
	}
}

func TestDefault_ProxyDirectiveFlags_DefaultTrue(t *testing.T) {
	cfg := Default()
	if !cfg.Proxy.PromptToolPrefs {
		t.Error("PromptToolPrefs should default to true")
	}
	if !cfg.Proxy.PromptOutputDiscipline {
		t.Error("PromptOutputDiscipline should default to true")
	}
	if !cfg.Proxy.PromptCodingDiscipline {
		t.Error("PromptCodingDiscipline should default to true")
	}
	if !cfg.Proxy.PromptBeweislast {
		t.Error("PromptBeweislast should default to true")
	}
	if !cfg.Proxy.PromptScopeDiscipline {
		t.Error("PromptScopeDiscipline should default to true")
	}
	if !cfg.Proxy.PromptDelegationContract {
		t.Error("PromptDelegationContract should default to true")
	}
	if !cfg.Proxy.PromptClarifyFirst {
		t.Error("PromptClarifyFirst should default to true")
	}
}

func TestDefault_ProxyDirectiveFlags_YAMLOverride(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgFile, []byte(`
proxy:
  prompt_tool_prefs: false
  prompt_output_discipline: false
  prompt_coding_discipline: false
  prompt_beweislast: false
  prompt_scope_discipline: false
  prompt_delegation_contract: false
  prompt_clarify_first: false
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Proxy.PromptToolPrefs {
		t.Error("PromptToolPrefs should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptOutputDiscipline {
		t.Error("PromptOutputDiscipline should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptCodingDiscipline {
		t.Error("PromptCodingDiscipline should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptBeweislast {
		t.Error("PromptBeweislast should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptScopeDiscipline {
		t.Error("PromptScopeDiscipline should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptDelegationContract {
		t.Error("PromptDelegationContract should be overridable to false via YAML")
	}
	if cfg.Proxy.PromptClarifyFirst {
		t.Error("PromptClarifyFirst should be overridable to false via YAML")
	}
}

func TestEffectivePromptFlags_LegacyFlatMapping(t *testing.T) {
	cfg := Default()
	// Legacy flat fields map to claude profile
	cfg.Proxy.PromptToolPrefs = true
	cfg.Proxy.PromptOutputDiscipline = true
	cfg.Proxy.PromptBeweislast = true
	cfg.Proxy.PromptScopeDiscipline = true
	cfg.Proxy.PromptDelegationContract = true

	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileClaude)
	if !flags.ToolPrefs {
		t.Error("claude: ToolPrefs should be true from legacy flat field")
	}
	if !flags.OutputDiscipline {
		t.Error("claude: OutputDiscipline should be true from legacy flat field")
	}
	if !flags.Beweislast {
		t.Error("claude: Beweislast should be true from legacy flat field")
	}
	if !flags.ScopeDiscipline {
		t.Error("claude: ScopeDiscipline should be true from legacy flat field")
	}
	if !flags.DelegationContract {
		t.Error("claude: DelegationContract should be true from legacy flat field")
	}
}

func TestEffectivePromptFlags_ClaudeProfileTakesPrecedence(t *testing.T) {
	cfg := Default()
	// Legacy flat says false, claude_prompt says true
	cfg.Proxy.PromptToolPrefs = false
	cfg.Proxy.ClaudePrompt = &PromptFlags{ToolPrefs: true}

	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileClaude)
	if !flags.ToolPrefs {
		t.Error("claude: ToolPrefs should be true from claude_prompt override")
	}
}

func TestEffectivePromptFlags_CodexProfileIgnoresLegacy(t *testing.T) {
	cfg := Default()
	// Override Default's SharedPrompt to nil for this test — we want to verify
	// that legacy flat fields (claude-only) are NOT inherited by codex.
	cfg.Proxy.SharedPrompt = nil
	cfg.Proxy.PromptToolPrefs = true
	cfg.Proxy.PromptBeweislast = true

	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileCodex)
	if flags.ToolPrefs {
		t.Error("codex: ToolPrefs should be false (legacy claude flags not inherited)")
	}
	if flags.Beweislast {
		t.Error("codex: Beweislast should be false (legacy claude flags not inherited)")
	}
}

func TestEffectivePromptFlags_SharedPromptMerges(t *testing.T) {
	cfg := Default()
	cfg.Proxy.SharedPrompt = &PromptFlags{ToolPrefs: true, OutputDiscipline: true}
	cfg.Proxy.CodexPrompt = &PromptFlags{OutputDiscipline: true} // codex overrides

	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileCodex)
	if !flags.ToolPrefs {
		t.Error("codex: ToolPrefs should be true from shared_prompt")
	}
	if !flags.OutputDiscipline {
		t.Error("codex: OutputDiscipline should be true from codex_prompt overlay")
	}
	if flags.Beweislast {
		t.Error("codex: Beweislast should be false (not in shared or codex)")
	}
}

func TestEffectivePromptFlags_OpencodeProfileBaseline(t *testing.T) {
	cfg := Default()
	// Override Default's SharedPrompt to nil for baseline test.
	cfg.Proxy.SharedPrompt = nil
	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileOpencode)
	if flags.ToolPrefs || flags.Beweislast || flags.ScopeDiscipline {
		t.Error("opencode: all flags should be false when nothing configured")
	}

	// With shared_prompt, opencode inherits.
	cfg.Proxy.SharedPrompt = &PromptFlags{OutputDiscipline: true}
	flags = cfg.Proxy.EffectivePromptFlags(models.ProfileOpencode)
	if !flags.OutputDiscipline {
		t.Error("opencode: OutputDiscipline should be true from shared_prompt")
	}

	// With Default's SharedPrompt (Beweislast, ScopeDiscipline etc.), opencode inherits them.
	cfg2 := Default()
	flags2 := cfg2.Proxy.EffectivePromptFlags(models.ProfileOpencode)
	if !flags2.Beweislast {
		t.Error("opencode: Beweislast should be true from Default's SharedPrompt")
	}
	if !flags2.OutputDiscipline {
		t.Error("opencode: OutputDiscipline should be true from Default's SharedPrompt")
	}
}

func TestEffectivePromptFlags_GenericFallback(t *testing.T) {
	cfg := Default()
	cfg.Proxy.SharedPrompt = &PromptFlags{CodingDiscipline: true}

	flags := cfg.Proxy.EffectivePromptFlags(models.ProfileGeneric)
	if !flags.CodingDiscipline {
		t.Error("generic: CodingDiscipline should be true from shared_prompt")
	}
	// Generic profile has no legacy or profile-specific layer
	if flags.Beweislast {
		t.Error("generic: Beweislast should be false")
	}
}

func TestDefaultPathsOpencodeDB(t *testing.T) {
	cfg := Default()
	if cfg.Paths.OpencodeDB == "" {
		t.Fatal("Paths.OpencodeDB should not be empty")
	}
	if !strings.HasSuffix(cfg.Paths.OpencodeDB, "opencode.db") {
		t.Errorf("Paths.OpencodeDB should end with opencode.db, got %q", cfg.Paths.OpencodeDB)
	}
}

func TestLoadPathsOpencodeDBFromYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(`
paths:
  opencode_db: /custom/path/opencode.db
`), 0644)

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Paths.OpencodeDB != "/custom/path/opencode.db" {
		t.Errorf("Paths.OpencodeDB = %q, want /custom/path/opencode.db", cfg.Paths.OpencodeDB)
	}
}
