package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"gopkg.in/yaml.v3"
)

// Config holds all YesMem configuration.
type Config struct {
	Extraction            ExtractionConfig          `yaml:"extraction"`
	Evolution             EvolutionConfig           `yaml:"evolution"`
	Briefing              BriefingConfig            `yaml:"briefing"`
	API                   APIConfig                 `yaml:"api"`
	LLM                   LLMConfig                 `yaml:"llm"`
	Pricing               map[string]ModelPricing   `yaml:"pricing"`
	Paths                 PathsConfig               `yaml:"paths"`
	Embedding             embedding.EmbeddingConfig `yaml:"embedding"`
	Proxy                 ProxyConfig               `yaml:"proxy"`
	Signals               SignalsConfig             `yaml:"signals"`
	ClaudeMd              ClaudeMdConfig            `yaml:"claudemd"`
	HTTP                  HTTPConfig                `yaml:"http"`
	Update                UpdateConfig              `yaml:"update"`
	Agents                AgentsConfig              `yaml:"agents"`
	ForkedAgents          ForkedAgentsConfig        `yaml:"forked_agents"`
	DefaultSandboxProfile string                    `yaml:"default_sandbox_profile"`
	CapsDir               string                    `yaml:"caps_dir"`
	SecretsSanitization   SecretsSanitizationConfig `yaml:"secrets_sanitization"`
	// ExcludeProjects lists project directories whose sessions are never indexed.
	// Sessions with Project matching any of these paths are silently skipped.
	// Example: ["/home", "/tmp"] prevents home-directory and temp-directory sessions.
	ExcludeProjects []string `yaml:"exclude_projects"`
}

// ModelPricing holds per-million-token pricing for a model.
type ModelPricing struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// AgentsConfig controls agent spawning behavior.
type AgentsConfig struct {
	Terminal       string `yaml:"terminal"`         // Preferred terminal: ghostty, kitty, gnome-terminal, alacritty, wezterm, xterm. Empty = auto-detect.
	ViewerTerminal string `yaml:"viewer_terminal"`  // Terminal for showing yesmem-agents session. Falls back to terminal if empty.
	DefaultBackend string `yaml:"default_backend"`  // Default agent backend: "claude" or "opencode". Empty = "claude".
	MaxRuntime     string `yaml:"max_runtime"`      // Max runtime per agent (Go duration: "30m", "1h"). Empty = 30m default.
	MaxTurns       int    `yaml:"max_turns"`        // Max relay turns per agent. 0 = 30 default.
	MaxDepth       int    `yaml:"max_depth"`        // Max spawn depth (agent→sub-agent). 0 = 3 default.
	TokenBudget    int    `yaml:"token_budget"`     // Max tokens per agent (input+output combined). 0 = 500000 default. Overridable per spawn.
}

// SecretsSanitizationConfig konfiguriert die SecretRedactor-Pipeline.
// Wenn Enabled=true, werden alle LLM-Inputs/Outputs und Bash-Job-Outputs
// durch den SecretRedactor geschickt bevor sie persistiert werden.
type SecretsSanitizationConfig struct {
	Enabled           bool     `yaml:"enabled"`
	AllowedExceptions []string `yaml:"allowed_exceptions"`
}

// ForkedAgentsConfig controls the forked agent proxy feature.
type ForkedAgentsConfig struct {
	Enabled            bool    `yaml:"enabled"`
	Model              string  `yaml:"model"`
	TokenGrowthTrigger int     `yaml:"token_growth_trigger"`
	MaxForksPerSession int     `yaml:"max_forks_per_session"`
	MaxCostPerSession  float64 `yaml:"max_cost_per_session"`
	Debug              bool    `yaml:"debug"`
}

// UpdateConfig controls automatic version checking and updates.
type UpdateConfig struct {
	AutoUpdate    bool   `yaml:"auto_update"`
	CheckInterval string `yaml:"check_interval"`
	Channel       string `yaml:"channel"`
}

// ClaudeMdConfig controls per-project operative reference generation.
type ClaudeMdConfig struct {
	Enabled         bool           `yaml:"enabled"`
	MaxPerCategory  map[string]int `yaml:"max_per_category"`
	RefreshInterval string         `yaml:"refresh_interval"`
	MinSessions     int            `yaml:"min_sessions"`
	OutputFileName  string         `yaml:"output_file"`
	Model           string         `yaml:"model"`
}

// SignalsConfig controls the cognitive signal reflection calls.
type SignalsConfig struct {
	Enabled     bool   `yaml:"enabled"`       // enable signal reflection (default: true)
	Mode        string `yaml:"mode"`          // "reflection" = separate async API call (default)
	Model       string `yaml:"model"`         // haiku, sonnet, opus — or full model ID (default: haiku)
	EveryNTurns int    `yaml:"every_n_turns"` // reflection call every N end_turn responses (default: 1)
}

// ProxyConfig controls the infinite-thread proxy.
type ProxyConfig struct {
	Enabled                  bool           `yaml:"enabled"`                    // auto-start proxy with daemon
	Listen                   string         `yaml:"listen"`                     // listen address, e.g. ":9099"
	Target                   string         `yaml:"target"`                     // target API URL, e.g. "https://api.anthropic.com"
	OpenAITarget             string            `yaml:"openai_target"`              // upstream for OpenAI-format clients (default: "https://api.openai.com")
	ProviderTargets          map[string]string `yaml:"provider_targets"`           // per-provider upstream, e.g. deepseek: "https://api.deepseek.com"
	AutoConfigureProviders   bool              `yaml:"auto_configure_providers"`  // auto-discover provider targets from opencode config (default: true)
	TokenThreshold           int            `yaml:"token_threshold"`            // trigger stubbing above this token count (default: 180000)
	TokenMinimumThreshold    int            `yaml:"token_minimum_threshold"`    // stub down to this floor (default: 80000)
	KeepRecent               int            `yaml:"keep_recent"`                // messages to always keep unmodified
	SawtoothEnabled          bool           `yaml:"sawtooth_enabled"`           // use sawtooth cache optimization (default: true)
	CacheTTL                 string         `yaml:"cache_ttl"`                  // "ephemeral" (5m, default) or "1h" (extended, 2× write cost)
	UsageDeflationFactor     float64        `yaml:"usage_deflation_factor"`     // scale input_tokens reported to CC (0=off, 0.7=70%)
	TokenThresholds          map[string]int `yaml:"token_thresholds"`           // model-specific thresholds: {"opus": 180000, "haiku": 130000}
	PromptUngate             bool           `yaml:"prompt_ungate"`              // [deprecated] use claude_prompt.prompt_ungate
	PromptRewrite            bool           `yaml:"prompt_rewrite"`             // [deprecated] use claude_prompt.prompt_rewrite
	PromptEnhance            bool           `yaml:"prompt_enhance"`             // [deprecated] use claude_prompt.prompt_enhance
	PromptToolPrefs          bool           `yaml:"prompt_tool_prefs"`          // [deprecated] use claude_prompt.prompt_tool_prefs
	PromptOutputDiscipline   bool           `yaml:"prompt_output_discipline"`   // [deprecated] use claude_prompt.prompt_output_discipline
	PromptCodingDiscipline   bool           `yaml:"prompt_coding_discipline"`   // [deprecated] use claude_prompt.prompt_coding_discipline
	PromptBeweislast         bool           `yaml:"prompt_beweislast"`          // [deprecated] use claude_prompt.prompt_beweislast
	PromptFable              bool           `yaml:"prompt_fable"`               // [deprecated] use claude_prompt.prompt_fable
	PromptScopeDiscipline    bool           `yaml:"prompt_scope_discipline"`    // [deprecated] use claude_prompt.prompt_scope_discipline
	PromptDelegationContract bool           `yaml:"prompt_delegation_contract"` // [deprecated] use claude_prompt.prompt_delegation_contract
	PromptClarifyFirst       bool           `yaml:"prompt_clarify_first"`       // [deprecated] use claude_prompt.prompt_clarify_first
	PromptCodeToolsFirst     bool           `yaml:"prompt_code_tools_first"`    // [deprecated] use claude_prompt.prompt_code_tools_first
	PromptWikiFirst          bool           `yaml:"prompt_wiki_first"`          // [deprecated] use claude_prompt.prompt_wiki_first
	PromptPatternSuggest     bool           `yaml:"prompt_pattern_suggest"`     // [deprecated] use claude_prompt.prompt_pattern_suggest
	EffortFloor              string         `yaml:"effort_floor"`               // minimum effort level: "low", "medium", "high", "max" (default: "" = off)
	SkillEvalInject          string         `yaml:"skill_eval_inject"`          // "true" = verbose eval output, "silent" = internal eval only, "false" = disabled (default: "silent")
	CacheKeepaliveEnabled    bool                `yaml:"cache_keepalive_enabled"`
	CacheKeepaliveMode       string              `yaml:"cache_keepalive_mode"`       // "auto", "5m", "1h"
	CacheKeepalivePings5m    int                 `yaml:"cache_keepalive_pings_5m"`
	CacheKeepalivePings1h    int                 `yaml:"cache_keepalive_pings_1h"`
	CacheKeepaliveMinMessages int                `yaml:"cache_keepalive_min_messages"`
	ResetCache               bool                `yaml:"reset_cache"`
	CodeNavMode              string              `yaml:"code_nav_mode"`
	CodeNavDismissCount      int                 `yaml:"code_nav_dismiss_count"`
	SharedPrompt             *PromptFlags        `yaml:"shared_prompt"`
	ClaudePrompt             *PromptFlags        `yaml:"claude_prompt"`
	CodexPrompt              *PromptFlags        `yaml:"codex_prompt"`
	OpencodePrompt           *PromptFlags        `yaml:"opencode_prompt"`
	ModelFeatures            map[string]*FeatureGates `yaml:"model_features"`
	FeatureDefaults          *FeatureGates            `yaml:"feature_defaults"`
	CustomSystemPrompt       CustomSystemPromptConfig `yaml:"custom_system_prompt"`
}

// CustomSystemPromptConfig controls which pipelines get the SYSTEM.md template injection.
type CustomSystemPromptConfig struct {
	EnabledOpenCode   bool              `yaml:"enabled_opencode"`
	EnabledClaudeCode bool              `yaml:"enabled_claude_code"`
	EnabledCodex      bool              `yaml:"enabled_codex"`
	TemplatePath      string            `yaml:"template_path"`
	ModelTemplates    map[string]string `yaml:"model_templates"` // per-model overrides: modelPattern → templatePath
}

// FeatureGates controls which yesmem behavioral features are active
// for a given model/provider. Default (zero value) = all disabled.
// This is separate from PromptFlags which control prompt text injection.
type FeatureGates struct {
	SkillEval              bool `yaml:"skill_eval"`                 // inject [skill-eval] instruction block
	Briefing               bool `yaml:"briefing"`                   // inject yesmem briefing at session start
	RulesReminder          bool `yaml:"rules_reminder"`             // inject rules/guidelines reminder
	PlanCheckpoint         bool `yaml:"plan_checkpoint"`            // inject plan checkpoint reminders
	ThinkReminder          bool `yaml:"think_reminder"`             // inject extended-thinking reminder
	ThinkReminderMinChars  int  `yaml:"think_reminder_min_chars"`   // min user text length to trigger think_reminder (0=always)
	Timestamps             bool `yaml:"timestamps"`                 // inject [HH:MM:SS] [msg:N] [+Δ] markers
	AssocContext           bool `yaml:"assoc_context"`              // inject [assoc-context] from hybrid_search (frozen per msg:N)
}

// PromptFlags holds prompt injection flags for a specific profile.
type PromptFlags struct {
	Ungate             bool `yaml:"prompt_ungate"`
	Rewrite            bool `yaml:"prompt_rewrite"`
	Enhance            bool `yaml:"prompt_enhance"`
	ToolPrefs          bool `yaml:"prompt_tool_prefs"`
	OutputDiscipline   bool `yaml:"prompt_output_discipline"`
	CodingDiscipline   bool `yaml:"prompt_coding_discipline"`
	Beweislast         bool `yaml:"prompt_beweislast"`
	Fable              bool `yaml:"prompt_fable"`
	ScopeDiscipline    bool `yaml:"prompt_scope_discipline"`
	DelegationContract bool `yaml:"prompt_delegation_contract"`
	ClarifyFirst       bool `yaml:"prompt_clarify_first"`
	CodeToolsFirst     bool `yaml:"prompt_code_tools_first"`
	WikiFirst          bool `yaml:"prompt_wiki_first"`
	PatternSuggest     bool `yaml:"prompt_pattern_suggest"`
}

// claudeLegacyFlags returns PromptFlags from the deprecated flat ProxyConfig fields.
func (p *ProxyConfig) claudeLegacyFlags() *PromptFlags {
	return &PromptFlags{
		Ungate:             p.PromptUngate,
		Rewrite:            p.PromptRewrite,
		Enhance:            p.PromptEnhance,
		ToolPrefs:          p.PromptToolPrefs,
		OutputDiscipline:   p.PromptOutputDiscipline,
		CodingDiscipline:   p.PromptCodingDiscipline,
		Beweislast:         p.PromptBeweislast,
		Fable:              p.PromptFable,
		ScopeDiscipline:    p.PromptScopeDiscipline,
		DelegationContract: p.PromptDelegationContract,
		ClarifyFirst:       p.PromptClarifyFirst,
		CodeToolsFirst:     p.PromptCodeToolsFirst,
		WikiFirst:          p.PromptWikiFirst,
		PatternSuggest:     p.PromptPatternSuggest,
	}
}

// EffectivePromptFlags resolves the effective prompt flags for a profile.
// Merge order (last wins): hard defaults → SharedPrompt → profile-specific → legacy flat fields (claude only).
func (p *ProxyConfig) EffectivePromptFlags(profile models.PromptProfile) *PromptFlags {
	// Start with shared prompt as base (or zero-value if nil).
	merged := &PromptFlags{}
	if p.SharedPrompt != nil {
		*merged = *p.SharedPrompt
	}

	// Apply profile-specific overrides.
	var profileFlags *PromptFlags
	switch profile {
	case models.ProfileClaude:
		profileFlags = p.ClaudePrompt
	case models.ProfileCodex:
		profileFlags = p.CodexPrompt
	case models.ProfileOpencode:
		profileFlags = p.OpencodePrompt
	}
	if profileFlags != nil {
		mergeFlags(merged, profileFlags)
	}

	// For Claude: deprecated flat fields are the legacy default.
	// They serve as a base layer if ClaudePrompt is nil or doesn't set a field.
	if profile == models.ProfileClaude {
		legacy := p.claudeLegacyFlags()
		mergeFlags(merged, legacy)
	}

	return merged
}

// mergeFlags copies non-zero values from src into dst.
// For booleans, "non-zero" means the field is true — this implements
// the pattern where profile-specific config only sets the flags it enables,
// and zero (false) means "inherit from parent layer".
func mergeFlags(dst, src *PromptFlags) {
	if src == nil || dst == nil {
		return
	}
	// Copy each field only if src has it enabled.
	// A false in src means "no opinion" — merged keeps its existing value.
	if src.Ungate {
		dst.Ungate = true
	}
	if src.Rewrite {
		dst.Rewrite = true
	}
	if src.Enhance {
		dst.Enhance = true
	}
	if src.ToolPrefs {
		dst.ToolPrefs = true
	}
	if src.OutputDiscipline {
		dst.OutputDiscipline = true
	}
	if src.CodingDiscipline {
		dst.CodingDiscipline = true
	}
	if src.Beweislast {
		dst.Beweislast = true
	}
	if src.Fable {
		dst.Fable = true
	}
	if src.ScopeDiscipline {
		dst.ScopeDiscipline = true
	}
	if src.DelegationContract {
		dst.DelegationContract = true
	}
	if src.ClarifyFirst {
		dst.ClarifyFirst = true
	}
	if src.CodeToolsFirst {
		dst.CodeToolsFirst = true
	}
	if src.PatternSuggest {
		dst.PatternSuggest = true
	}
}

// HTTPConfig controls the OpenClaw HTTP API server.
type HTTPConfig struct {
	Enabled   bool   `yaml:"enabled"`    // start HTTP listener (default: false)
	Listen    string `yaml:"listen"`     // e.g. "127.0.0.1:9377" (default)
	AuthToken string `yaml:"auth_token"` // bearer token, auto-generated if empty
}

// ExtractionConfig controls LLM-based knowledge extraction.
type ExtractionConfig struct {
	Model          string `yaml:"model"`                 // Pass 2 extraction model (default: sonnet)
	SummarizeModel string `yaml:"summarize_model"`       // Pass 1 summarization model (default: haiku)
	QualityModel   string `yaml:"quality_model"`         // Pass 2 quality refinement (default: narrative_model)
	NarrativeModel string `yaml:"narrative_model"`       // narrative generation (default: opus)
	Mode           string `yaml:"mode"`                  // single (legacy), two-pass (default)
	ChunkSize      int    `yaml:"chunk_size"`            // tokens per chunk
	AutoExtract    bool   `yaml:"auto_extract"`          // run after each session?
	MaxAgeDays     int    `yaml:"max_age_days"`          // 0 = all sessions, N = only last N days
	MaxPerRun      int    `yaml:"max_per_run"`           // 0 = unlimited, N = max sessions to extract per daemon run
	MinSessionAgeH int    `yaml:"min_session_age_hours"` // skip sessions younger than N hours (default: 24)
}

// LLMConfig controls the LLM backend provider.
type LLMConfig struct {
	Provider              string  `yaml:"provider"`                 // auto, api/anthropic, openai, openai_compatible, cli
	ClaudeBinary          string  `yaml:"claude_binary"`            // optional path to claude binary
	DailyBudgetExtractUSD float64 `yaml:"daily_budget_extract_usd"` // extraction (Haiku/Sonnet), 0 = unlimited
	DailyBudgetQualityUSD float64 `yaml:"daily_budget_quality_usd"` // narratives/persona (Opus), 0 = unlimited
	MaxBudgetPerCallUSD   float64 `yaml:"max_budget_per_call_usd"`  // per-call safety net (CLI: --max-budget-usd), 0 = no limit
	OpenAIBaseURL         string  `yaml:"openai_base_url"`          // override for llm_complete RPC (e.g. proxy), empty = use api.openai_base_url
	CompleteProvider      string  `yaml:"complete_provider"`        // override provider for llm_complete RPC (e.g. opencode), empty = use provider
}

// EvolutionConfig controls knowledge evolution behavior.
type EvolutionConfig struct {
	AutoResolve   bool `yaml:"auto_resolve"`
	UnfinishedTTL int  `yaml:"unfinished_ttl_days"`
}

// BriefingConfig controls briefing generation.
type BriefingConfig struct {
	DetailedSessions  int      `yaml:"detailed_sessions"`
	OtherProjectsDays int      `yaml:"other_projects_days"`
	MaxTokens         int      `yaml:"max_tokens"`
	DedupThreshold    float64  `yaml:"dedup_threshold"`
	MaxPerCategory    int      `yaml:"max_per_category"`
	Languages         []string `yaml:"languages"`
	RemindOpenWork    bool     `yaml:"remind_open_work"` // inject instruction to mention open work at session start (default: true)
	UserProfile       bool     `yaml:"user_profile"`     // include synthesized user profile in briefing (default: true)
}

// APIConfig holds API credentials.
type APIConfig struct {
	APIKey        string `yaml:"api_key"`
	OpenAIAPIKey  string `yaml:"openai_api_key"`
	OpenAIBaseURL string `yaml:"openai_base_url"`
}

// PathsConfig holds file system paths.
type PathsConfig struct {
	DB             string `yaml:"db"`
	BleveIndex     string `yaml:"bleve_index"`
	Archive        string `yaml:"archive"`
	ClaudeProjects string `yaml:"claude_projects"`
	OpencodeDB     string `yaml:"opencode_db"`
}

// Default returns a config with sensible defaults.
func Default() *Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".claude", "yesmem")

	return &Config{
		Extraction: ExtractionConfig{
			Model:          "sonnet",
			SummarizeModel: "haiku",
			NarrativeModel: "opus",
			QualityModel:   "sonnet",
			Mode:           "prefiltered",
			ChunkSize:      25000,
			AutoExtract:    true,
			MaxAgeDays:     0,
			MaxPerRun:      30,
			MinSessionAgeH: 24,
		},
		LLM: LLMConfig{
			Provider:              "auto",
			DailyBudgetExtractUSD: 20.0,
			DailyBudgetQualityUSD: 10.0,
			MaxBudgetPerCallUSD:   1.0,
		},
		Pricing: DefaultPricing(),
		Evolution: EvolutionConfig{
			AutoResolve:   true,
			UnfinishedTTL: 30,
		},
		Briefing: BriefingConfig{
			DetailedSessions:  3,
			OtherProjectsDays: 90,
			MaxTokens:         5000,
			DedupThreshold:    0.4,
			MaxPerCategory:    5,
			Languages:         []string{"de", "en"},
			RemindOpenWork:    true,
			UserProfile:       true,
		},
		API: APIConfig{
			APIKey:        os.Getenv("ANTHROPIC_API_KEY"),
			OpenAIAPIKey:  os.Getenv("OPENAI_API_KEY"),
			OpenAIBaseURL: os.Getenv("OPENAI_BASE_URL"),
		},
		Paths: PathsConfig{
			DB:             filepath.Join(dataDir, "yesmem.db"),
			BleveIndex:     filepath.Join(dataDir, "bleve-index"),
			Archive:        filepath.Join(dataDir, "archive"),
			ClaudeProjects: filepath.Join(home, ".claude", "projects"),
			OpencodeDB:     filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
		},
		Embedding: embedding.DefaultEmbeddingConfig(),
		Proxy: ProxyConfig{
			Enabled:                  true,
			Listen:                   ":9099",
			Target:                   "https://api.anthropic.com",
			OpenAITarget:             "https://api.openai.com",
			AutoConfigureProviders:   true,
			TokenThreshold:           250000,
			TokenMinimumThreshold:    100000,
			KeepRecent:               10,
			SawtoothEnabled:          true,
			CacheTTL:                 "ephemeral",
			UsageDeflationFactor:     0.7, // experimental: report 70% of actual tokens to CC
			PromptUngate:             true,
			PromptToolPrefs:          true,
			PromptOutputDiscipline:   true,
			PromptCodingDiscipline:   true,
			PromptBeweislast:         true,
			PromptFable:              true,
			PromptScopeDiscipline:    true,
			PromptDelegationContract: true,
			PromptClarifyFirst:       true,
			PromptCodeToolsFirst:     true,
			PromptWikiFirst:          true,
			PromptPatternSuggest:     true,
			// Profile-aware defaults: shared layer enables agent-neutral injectors for all profiles.
			// Claude-specific flags (ToolPrefs, CodeToolsFirst, DelegationContract) are only in
			// the legacy flat fields (mapped to ClaudePrompt by claudeLegacyFlags).
			SharedPrompt: &PromptFlags{
				OutputDiscipline: true,
				CodingDiscipline: true,
				Beweislast:       true,
				Fable:            true,
				ScopeDiscipline:  true,
				ClarifyFirst:     true,
				CodeToolsFirst: true,
			},
			SkillEvalInject:          "silent",
		ModelFeatures: map[string]*FeatureGates{
		"claude":   {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: true, ThinkReminder: true, Timestamps: false, AssocContext: true},
		"deepseek": {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: false, ThinkReminder: true, Timestamps: true, AssocContext: true},
		"gpt":      {SkillEval: true, Briefing: true, RulesReminder: true, ThinkReminder: false, Timestamps: false, AssocContext: true},
		"openai":   {SkillEval: true, Briefing: true, RulesReminder: true, ThinkReminder: false, Timestamps: false, AssocContext: true},
		},
		FeatureDefaults: &FeatureGates{
			SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: true, ThinkReminder: true, Timestamps: true,
		},
			CacheKeepaliveEnabled:     true,
			CacheKeepaliveMode:        "5m",
			CacheKeepalivePings5m:     5,
			CacheKeepalivePings1h:     1,
			CacheKeepaliveMinMessages: 10,
			CodeNavMode:              "block",
			CodeNavDismissCount:      5,
			TokenThresholds: map[string]int{
				"opus":     180000,
				"sonnet":   180000,
				"haiku":    130000,
				"gpt-5":    180000,
				"codex":    180000,
				"deepseek": 600000,
				"glm-5.2":  500000,
			},
			CustomSystemPrompt: CustomSystemPromptConfig{
				EnabledOpenCode:   true,
				EnabledClaudeCode: false,
				EnabledCodex:      false,
				TemplatePath:      "~/.claude/yesmem/SYSTEM.md",
			},
		},
		Signals: SignalsConfig{
			Enabled:     true,
			Mode:        "reflection",
			Model:       "haiku",
			EveryNTurns: 1,
		},
		ClaudeMd: ClaudeMdConfig{
			Enabled: true,
			MaxPerCategory: map[string]int{
				"gotcha":            15,
				"pattern":           10,
				"decision":          10,
				"explicit_teaching": 5,
				"pivot_moment":      5,
			},
			RefreshInterval: "2h",
			MinSessions:     3,
			OutputFileName:  "yesmem-ops.md",
			Model:           "",
		},
		HTTP: HTTPConfig{
			Enabled: false,
			Listen:  "127.0.0.1:9377",
		},
		Update: UpdateConfig{
			AutoUpdate:    true,
			CheckInterval: "6h",
			Channel:       "stable",
		},
		Agents: AgentsConfig{
			DefaultBackend: "claude",
		},
		ForkedAgents: ForkedAgentsConfig{
			Enabled:            false,
			Model:              "sonnet",
			TokenGrowthTrigger: 20000,
			MaxForksPerSession: 50,
			MaxCostPerSession:  5.0,
		},
	}
}

// Load reads config from a YAML file, falling back to defaults for missing fields.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // No config file = use defaults
		}
		log.Printf("warn: config read error: %v (using defaults)", err)
		return cfg, nil
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Printf("warn: config parse error: %v (using defaults)", err)
		return Default(), nil
	}

	if cfg.API.APIKey == "" {
		cfg.API.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	} else {
		cfg.API.APIKey = expandEnvRef(cfg.API.APIKey)
	}
	if cfg.API.OpenAIAPIKey == "" {
		cfg.API.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	} else {
		cfg.API.OpenAIAPIKey = expandEnvRef(cfg.API.OpenAIAPIKey)
	}
	if cfg.API.OpenAIBaseURL == "" {
		cfg.API.OpenAIBaseURL = os.Getenv("OPENAI_BASE_URL")
	} else {
		cfg.API.OpenAIBaseURL = expandEnvRef(cfg.API.OpenAIBaseURL)
	}

	return cfg, nil
}

func expandEnvRef(value string) string {
	if value == "" || value[0] != '$' {
		return value
	}
	envName := value[1:]
	envName = strings.TrimPrefix(envName, "{")
	envName = strings.TrimSuffix(envName, "}")
	return os.Getenv(envName)
}

// MergeDefaults loads config.yaml, fills in defaults for any missing fields,
// and writes it back. Preserves existing user values.
func MergeDefaults(path string) error {
	cfg, err := Load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := backupFile(path); err != nil {
		return fmt.Errorf("backup config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Save writes config to a YAML file, creating a timestamped backup of any existing file.
func Save(path string, cfg *Config) error {
	if err := backupFile(path); err != nil {
		return fmt.Errorf("backup config: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// backupFile creates a timestamped backup copy of path if the file exists.
func backupFile(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // no existing file to back up
	} else if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	backupPath := path + ".bak." + time.Now().Format("20060102T150405")
	return os.WriteFile(backupPath, data, 0644)
}

// GetValue reads a specific value from config.yaml by dot-path.
// e.g. "extraction.model" returns the extraction model.
// Returns nil if the path does not exist (no error).
func GetValue(path, dotPath string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no config file = no value
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	parts := strings.Split(dotPath, ".")
	current := any(raw)
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, nil // path not found
		}
		current, ok = m[part]
		if !ok {
			return nil, nil // path not found
		}
	}
	return current, nil
}

// SetValue sets a specific value in config.yaml by dot-path, creates a backup,
// and writes the modified config back. Creates the file with defaults if it
// does not exist.
// e.g. SetValue("config.yaml", "extraction.model", "opus")
// Deeply nested paths create intermediate maps as needed.
func SetValue(path, dotPath string, value any) error {
	// Read existing config or start fresh
	var raw map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read config: %w", err)
		}
		raw = map[string]any{} // start fresh
	} else {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}

	// Navigate/create path and set value
	parts := strings.Split(dotPath, ".")
	current := raw
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			break
		}
		next, ok := current[part]
		if !ok {
			nextMap := map[string]any{}
			current[part] = nextMap
			current = nextMap
		} else if m, ok := next.(map[string]any); ok {
			current = m
		} else {
			// Overwrite scalar with map to continue path
			nextMap := map[string]any{}
			current[part] = nextMap
			current = nextMap
		}
	}

	// Backup and write
	if err := backupFile(path); err != nil {
		return fmt.Errorf("backup config: %w", err)
	}
	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, out, 0644)
}

// CoerceValue attempts to parse a string as the most specific type.
// Order: int, float, bool, then string fallback. This ensures YAML
// round-trips produce correct types (ints not quoted strings, etc.).
func CoerceValue(s string) any {
	if s == "" {
		return s
	}
	// Try int first (most specific)
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	// Try float
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	// Try bool
	if v, err := strconv.ParseBool(s); err == nil {
		return v
	}
	return s
}

// NormalizeLLMProvider maps legacy aliases to canonical provider names.
func NormalizeLLMProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "auto":
		return "auto"
	case "anthropic":
		return "api"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// IsOpenAIProvider reports whether the configured provider uses an OpenAI-compatible API.
func IsOpenAIProvider(provider string) bool {
	switch NormalizeLLMProvider(provider) {
	case "openai", "openai_compatible":
		return true
	default:
		return false
	}
}

// ResolveModelIDForProvider maps tier shortnames to provider-specific model IDs.
// Unknown non-empty names are passed through unchanged so explicit model IDs work.
func ResolveModelIDForProvider(provider, name, anthropicFallback, openAIFallback string) string {
	raw := strings.TrimSpace(name)
	key := strings.ToLower(raw)
	if key == "" {
		if IsOpenAIProvider(provider) {
			return openAIFallback
		}
		return anthropicFallback
	}

	if IsOpenAIProvider(provider) {
		switch key {
		case "haiku":
			return "gpt-5-mini"
		case "sonnet":
			return "gpt-5.2"
		case "opus":
			return "gpt-5.4"
		default:
			return raw
		}
	}

	switch key {
	case "opus":
		return "claude-opus-4-6"
	case "sonnet":
		return "claude-sonnet-4-6"
	case "haiku":
		return "claude-haiku-4-5-20251001"
	default:
		return raw
	}
}

// ResolvedAPIKey returns the provider-appropriate API key from env/config.
func (c *Config) ResolvedAPIKey() string {
	if c == nil {
		return ""
	}
	if IsOpenAIProvider(c.LLM.Provider) {
		if k := os.Getenv("OPENAI_API_KEY"); k != "" {
			return k
		}
		return c.API.OpenAIAPIKey
	}
	if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
		return k
	}
	return c.API.APIKey
}

// ResolvedOpenAIBaseURL returns the configured OpenAI-compatible base URL.
func (c *Config) ResolvedOpenAIBaseURL() string {
	if c == nil {
		return ""
	}
	if k := os.Getenv("OPENAI_BASE_URL"); k != "" {
		return strings.TrimSpace(k)
	}
	return strings.TrimSpace(c.API.OpenAIBaseURL)
}

// DefaultPricing returns hardcoded per-million-token pricing as fallback defaults.
func DefaultPricing() map[string]ModelPricing {
	return map[string]ModelPricing{
		"haiku":             {Input: 1.0, Output: 5.0},
		"sonnet":            {Input: 3.0, Output: 15.0},
		"opus":              {Input: 5.0, Output: 25.0},
		"gpt-5-mini":        {Input: 0.25, Output: 2.0},
		"gpt-5.2":           {Input: 1.75, Output: 14.0},
		"gpt-5.4":           {Input: 2.5, Output: 15.0},
		"deepseek-v4-flash": {Input: 0.14, Output: 0.56},
		"deepseek-v4-pro":   {Input: 0.28, Output: 1.12},
	}
}

// PricingForModel returns input/output per-million-token pricing for the given model.
// Checks config pricing map first (exact match, then substring), falls back to defaults.
func (c *Config) PricingForModel(model string) (inputPerM, outputPerM float64) {
	pricing := c.Pricing
	if len(pricing) == 0 {
		pricing = DefaultPricing()
	}

	// Exact match first
	if p, ok := pricing[model]; ok {
		return p.Input, p.Output
	}

	// Substring match (e.g. "claude-sonnet-4-6" contains "sonnet")
	for key, p := range pricing {
		if strings.Contains(model, key) {
			return p.Input, p.Output
		}
	}

	// Fallback to sonnet pricing
	if p, ok := pricing["sonnet"]; ok {
		return p.Input, p.Output
	}
	return 3.0, 15.0
}

// ModelID returns the full provider-specific model ID for the configured extraction model.
func (c *Config) ModelID() string {
	return ResolveModelIDForProvider(c.LLM.Provider, c.Extraction.Model, "claude-sonnet-4-6", "gpt-5.2")
}

// NarrativeModelID returns the full provider-specific model ID for narrative generation.
func (c *Config) NarrativeModelID() string {
	m := c.Extraction.NarrativeModel
	if m == "" {
		m = "opus"
	}
	return ResolveModelIDForProvider(c.LLM.Provider, m, "claude-opus-4-6", "gpt-5.4")
}

// QualityModelID returns the full provider-specific model ID for Pass 2 extraction.
// Falls back to NarrativeModelID if not set.
func (c *Config) QualityModelID() string {
	m := c.Extraction.QualityModel
	if m == "" {
		return c.NarrativeModelID()
	}
	return ResolveModelIDForProvider(c.LLM.Provider, m, "claude-opus-4-6", "gpt-5.4")
}

// SummarizeModelID returns the full provider-specific model ID for Pass 1 summarization.
// Falls back to haiku if not set (summarization is a compression task, not creative).
func (c *Config) SummarizeModelID() string {
	m := c.Extraction.SummarizeModel
	if m == "" {
		return ResolveModelIDForProvider(c.LLM.Provider, "", "claude-haiku-4-5-20251001", "gpt-5-mini")
	}
	return ResolveModelIDForProvider(c.LLM.Provider, m, "claude-haiku-4-5-20251001", "gpt-5-mini")
}

// SignalsModelID returns the full provider-specific model ID for signal reflection calls.
// Falls back to haiku if not set (reflection is a classification task, not creative).
func (c *Config) SignalsModelID() string {
	m := c.Signals.Model
	if m == "" {
		return ResolveModelIDForProvider(c.LLM.Provider, "", "claude-haiku-4-5-20251001", "gpt-5-mini")
	}
	return ResolveModelIDForProvider(c.LLM.Provider, m, "claude-haiku-4-5-20251001", "gpt-5-mini")
}

// ForkedAgentsModelID returns the full provider-specific model ID for forked agent calls.
// Empty string = use the same model as the main thread (model from the original request).
func (c *Config) ForkedAgentsModelID() string {
	m := c.ForkedAgents.Model
	if m == "" {
		return "" // empty → buildForkRequest keeps original request model
	}
	return ResolveModelIDForProvider(c.LLM.Provider, m, "claude-sonnet-4-6", "gpt-5.2")
}
