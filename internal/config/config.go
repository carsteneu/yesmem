package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	Diagnostics           DiagnosticsConfig         `yaml:"diagnostics"`
	Update                UpdateConfig              `yaml:"update"`
	Agents                AgentsConfig              `yaml:"agents"`
	ForkedAgents          ForkedAgentsConfig        `yaml:"forked_agents"`
	DefaultSandboxProfile string                    `yaml:"default_sandbox_profile"`
	CapsDir               string                    `yaml:"caps_dir"`
	SecretsSanitization   SecretsSanitizationConfig `yaml:"secrets_sanitization"`
	RuleGuard             RuleGuardConfig           `yaml:"rule_guard"`
	DoneVerify            DoneVerifyConfig          `yaml:"done_verify"`
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
	Terminal               string `yaml:"terminal"`                 // Preferred terminal: ghostty, kitty, gnome-terminal, alacritty, wezterm, xterm. Empty = auto-detect.
	ViewerTerminal         string `yaml:"viewer_terminal"`          // Terminal for showing yesmem-agents session. Falls back to terminal if empty.
	DefaultBackend         string `yaml:"default_backend"`          // Default agent backend: "claude" or "opencode". Empty = "claude".
	MaxRuntime             string `yaml:"max_runtime"`              // Max runtime per agent (Go duration: "30m", "1h"). Empty = 48h default (getAgentMaxRuntime).
	MaxTurns               int    `yaml:"max_turns"`                // Max relay turns per agent. 0 = 30 default.
	MaxDepth               int    `yaml:"max_depth"`                // Max spawn depth (agent→sub-agent). 0 = 3 default.
	TokenBudget            int    `yaml:"token_budget"`             // Max output tokens per agent (0 = no limit). The unit is output tokens: input_tokens is cumulative and includes cache reads, so it cannot express work or cost. Overridable per spawn.
	PermissionKick         bool   `yaml:"permission_kick"`          // Kick agents stuck in permission dialogs via double-Enter. Default true.
	PermissionKickDelay    string `yaml:"permission_kick_delay"`    // Stream-inactivity before first kick (Go duration). Empty = 3m default.
	PermissionKickInterval string `yaml:"permission_kick_interval"` // Re-kick interval (Go duration). Empty = 5m default.

	// Themes maps an agent type (the section prefix, e.g. "yesloop") to the
	// opencode TUI theme for its spawned agents. An empty value disables the
	// type's theme; unset keys keep the built-in default.
	Themes map[string]string `yaml:"themes"`
}

// SecretsSanitizationConfig konfiguriert die SecretRedactor-Pipeline.
// Wenn Enabled=true, werden alle LLM-Inputs/Outputs und Bash-Job-Outputs
// durch den SecretRedactor geschickt bevor sie persistiert werden.
type SecretsSanitizationConfig struct {
	Enabled           bool     `yaml:"enabled"`
	AllowedExceptions []string `yaml:"allowed_exceptions"`
}

// RuleGuardConfig is the opencode plugin's pre-tool rule guard.
// plugins/opencode-yesmem-v2/rule_guard.ts reads rule_guard.model itself and
// falls back to its own default when the key is empty. The field exists so the
// key is part of the schema: `config set rule_guard.model` reaches the file and
// `config get` can read it back, instead of the section being a foreign key
// that only the plugin knows about.
type RuleGuardConfig struct {
	Model string `yaml:"model"`
}

// DoneVerifyConfig is the DONE gate's verification of a completion report.
//
// Enabled is the global switch and defaults to on: the gate is the layer that
// stops a report from being believed, so shipping it off would leave every loop
// as trusting as before. Budget is the default run budget of a direct
// verify_claims call, which is the only place a verification runs; MaxWait is how
// long such a call waits for the single verification slot before it reports that
// another run is still working.
//
// Model is part of the schema for symmetry with rule_guard and so
// `config set done_verify.model` reaches the file instead of creating a key
// only one reader knows. The verifier itself is deterministic and calls no
// model; an operator setting the key changes nothing about a run.
type DoneVerifyConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`
	Budget  string `yaml:"budget"`
	MaxWait string `yaml:"max_wait"`
}

// UnmarshalYAML keeps a hand-edited rule_guard section loadable. Before the key
// entered the schema the loader ignored every shape of it; now a scalar would
// reject the whole file and Load would fall back to defaults, losing the API
// keys and every path. A scalar is the model name written without its key, a
// mapping is the documented shape, and a shape that carries no model is ignored
// rather than fatal.
func (r *RuleGuardConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		r.Model = strings.TrimSpace(value.Value)
	case yaml.MappingNode:
		type plain RuleGuardConfig
		var decoded plain
		if err := value.Decode(&decoded); err != nil {
			return nil
		}
		*r = RuleGuardConfig(decoded)
	}
	return nil
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

// SignalsConfig is the signals surface of config.yaml. Only Enabled is read: it
// gates the inline reflection (proxy.Config.InlineReflectionEnabled). Mode, Model
// and EveryNTurns configured the async reflection call, which no longer exists —
// they stay accepted so existing config.yaml files keep parsing.
type SignalsConfig struct {
	Enabled     bool   `yaml:"enabled"`       // gate for the inline reflection (default: true)
	Mode        string `yaml:"mode"`          // inert: configured the removed reflection call
	Model       string `yaml:"model"`         // inert: configured the removed reflection call
	EveryNTurns int    `yaml:"every_n_turns"` // inert: configured the removed reflection call
}

// ProxyConfig controls the infinite-thread proxy.
type ProxyConfig struct {
	Enabled                   bool                     `yaml:"enabled"`                    // auto-start proxy with daemon
	Listen                    string                   `yaml:"listen"`                     // listen address, e.g. ":9099"
	Target                    string                   `yaml:"target"`                     // target API URL, e.g. "https://api.anthropic.com"
	OpenAITarget              string                   `yaml:"openai_target"`              // upstream for OpenAI-format clients (default: "https://api.openai.com")
	ProviderTargets           map[string]string        `yaml:"provider_targets"`           // per-provider upstream, e.g. deepseek: "https://api.deepseek.com"
	AutoConfigureProviders    bool                     `yaml:"auto_configure_providers"`   // auto-discover provider targets from opencode config (default: true)
	TokenThreshold            int                      `yaml:"token_threshold"`            // trigger stubbing above this token count (default: 180000)
	TokenMinimumThreshold     int                      `yaml:"token_minimum_threshold"`    // stub down to this floor (default: 80000)
	KeepRecent                int                      `yaml:"keep_recent"`                // messages to always keep unmodified
	SawtoothEnabled           bool                     `yaml:"sawtooth_enabled"`           // use sawtooth cache optimization (default: true)
	CacheTTL                  string                   `yaml:"cache_ttl"`                  // "ephemeral" (5m, default) or "1h" (extended, 2× write cost)
	UsageDeflationFactor      float64                  `yaml:"usage_deflation_factor"`     // scale input_tokens reported to CC (0=off, 0.7=70%)
	TokenThresholds           map[string]int           `yaml:"token_thresholds"`           // model-specific thresholds: {"opus": 180000, "haiku": 130000}
	PromptUngate              bool                     `yaml:"prompt_ungate"`              // [deprecated] use claude_prompt.prompt_ungate
	PromptRewrite             bool                     `yaml:"prompt_rewrite"`             // [deprecated] use claude_prompt.prompt_rewrite
	PromptEnhance             bool                     `yaml:"prompt_enhance"`             // [deprecated] use claude_prompt.prompt_enhance
	PromptToolPrefs           bool                     `yaml:"prompt_tool_prefs"`          // [deprecated] use claude_prompt.prompt_tool_prefs
	PromptOutputDiscipline    bool                     `yaml:"prompt_output_discipline"`   // [deprecated] use claude_prompt.prompt_output_discipline
	PromptCodingDiscipline    bool                     `yaml:"prompt_coding_discipline"`   // [deprecated] use claude_prompt.prompt_coding_discipline
	PromptBeweislast          bool                     `yaml:"prompt_beweislast"`          // [deprecated] use claude_prompt.prompt_beweislast
	PromptFable               bool                     `yaml:"prompt_fable"`               // [deprecated] use claude_prompt.prompt_fable
	PromptScopeDiscipline     bool                     `yaml:"prompt_scope_discipline"`    // [deprecated] use claude_prompt.prompt_scope_discipline
	PromptDelegationContract  bool                     `yaml:"prompt_delegation_contract"` // [deprecated] use claude_prompt.prompt_delegation_contract
	PromptClarifyFirst        bool                     `yaml:"prompt_clarify_first"`       // [deprecated] use claude_prompt.prompt_clarify_first
	PromptCodeToolsFirst      bool                     `yaml:"prompt_code_tools_first"`    // [deprecated] use claude_prompt.prompt_code_tools_first
	PromptWikiFirst           bool                     `yaml:"prompt_wiki_first"`          // [deprecated] use claude_prompt.prompt_wiki_first
	PromptPatternSuggest      bool                     `yaml:"prompt_pattern_suggest"`     // [deprecated] use claude_prompt.prompt_pattern_suggest
	EffortFloor               string                   `yaml:"effort_floor"`               // minimum effort level: "low", "medium", "high", "max" (default: "" = off)
	SkillEvalInject           string                   `yaml:"skill_eval_inject"`          // "true" = verbose eval output, "silent" = internal eval only, "false" = disabled (default: "silent")
	CacheKeepaliveEnabled     bool                     `yaml:"cache_keepalive_enabled"`
	CacheKeepaliveMode        string                   `yaml:"cache_keepalive_mode"` // "auto", "5m", "1h"
	CacheKeepalivePings5m     int                      `yaml:"cache_keepalive_pings_5m"`
	CacheKeepalivePings1h     int                      `yaml:"cache_keepalive_pings_1h"`
	CacheKeepaliveMinMessages int                      `yaml:"cache_keepalive_min_messages"`
	EagerStubEnabled          bool                     `yaml:"eager_stub_enabled"` // eager-stub closed-turn tool_results; default false (upstream 400s + cache-miss cascades)
	ResetCache                bool                     `yaml:"reset_cache"`
	CodeNavMode               string                   `yaml:"code_nav_mode"`
	CodeNavDismissCount       int                      `yaml:"code_nav_dismiss_count"` // retired and unread: the threshold is fixed since YM-425, the key stays for existing configs (#106662)
	SharedPrompt              *PromptFlags             `yaml:"shared_prompt"`
	ClaudePrompt              *PromptFlags             `yaml:"claude_prompt"`
	CodexPrompt               *PromptFlags             `yaml:"codex_prompt"`
	OpencodePrompt            *PromptFlags             `yaml:"opencode_prompt"`
	ModelFeatures             map[string]*FeatureGates `yaml:"model_features"`
	FeatureDefaults           *FeatureGates            `yaml:"feature_defaults"`
	CustomSystemPrompt        CustomSystemPromptConfig `yaml:"custom_system_prompt"`
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
	SkillEval             bool `yaml:"skill_eval"`               // inject [skill-eval] instruction block
	Briefing              bool `yaml:"briefing"`                 // inject yesmem briefing at session start
	RulesReminder         bool `yaml:"rules_reminder"`           // inject rules/guidelines reminder
	PlanCheckpoint        bool `yaml:"plan_checkpoint"`          // inject plan checkpoint reminders
	ThinkReminder         bool `yaml:"think_reminder"`           // inject extended-thinking reminder
	ThinkReminderMinChars int  `yaml:"think_reminder_min_chars"` // min user text length for think_reminder; 0 = inherit from feature_defaults (0 there = always)
	Timestamps            bool `yaml:"timestamps"`               // inject [HH:MM:SS] [msg:N] [+Δ] markers
	AssocContext          bool `yaml:"assoc_context"`            // inject [assoc-context] from hybrid_search (frozen per msg:N)
	LoopWarning           bool `yaml:"loop_warning"`             // inject [loop-warning] when loop detection fires (frozen per msg:N)

	// named records which keys this block actually wrote. A bool cannot tell a
	// written false from "not set", and that difference decides whether the
	// block or feature_defaults wins (YM-455). A block built in code leaves it
	// nil: a struct literal has no unset fields, so it states all of them.
	named map[string]bool
	// extra holds keys this build does not know (a newer version's gate, or one
	// the user added by hand). A rewrite of config.yaml goes through
	// MarshalYAML, and dropping them there would silently delete a setting the
	// user made (YM-302).
	extra map[string]any
}

// featureGatesPlain shadows FeatureGates for decoding without recursing into
// UnmarshalYAML.
type featureGatesPlain FeatureGates

// UnmarshalYAML keeps the set of keys the block names, alongside their values.
// A gate key counts as named when the typed decoder accepts its value: the raw
// type is not enough, because YAML 1.1 words (yes/no/on/off) arrive as strings in
// an untyped decode but are written into bool fields, and a written "no" has to
// outrank feature_defaults like any other false. A key of a merged block (<<)
// counts as named too, because the decoder applies it. A gate value neither the
// bool nor the int decoder accepts stays "no opinion" here; note that a written
// gate value of the wrong type already fails the struct decode above, which makes
// Load fall back to the defaults (pre-existing, not decided here).
func (g *FeatureGates) UnmarshalYAML(value *yaml.Node) error {
	if err := value.Decode((*featureGatesPlain)(g)); err != nil {
		return err
	}
	named := make(map[string]bool, len(value.Content)/2)
	extra := make(map[string]any)
	collectStatedGates(value, named, extra)
	g.named = named
	if len(extra) > 0 {
		g.extra = extra
	} else {
		g.extra = nil
	}
	return nil
}

// collectStatedGates walks a gate mapping and records which gate keys it states,
// plus the keys this build does not know. It recurses into a merge key (<<), so a
// block that inherits its gates from an anchor states them just like a block that
// spells them out.
func collectStatedGates(node *yaml.Node, named map[string]bool, extra map[string]any) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	fields := (&FeatureGates{}).boolFields()
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, field := node.Content[i].Value, node.Content[i+1]
		if key == "<<" {
			switch field.Kind {
			case yaml.AliasNode:
				collectStatedGates(field.Alias, named, extra)
			case yaml.SequenceNode:
				for _, item := range field.Content {
					if item.Kind == yaml.AliasNode {
						collectStatedGates(item.Alias, named, extra)
						continue
					}
					collectStatedGates(item, named, extra)
				}
			case yaml.MappingNode:
				collectStatedGates(field, named, extra)
			}
			continue
		}
		if _, known := fields[key]; !known && key != "think_reminder_min_chars" {
			var v any
			if field.Decode(&v) == nil {
				extra[key] = v
			}
			continue
		}
		if field.Tag == "!!null" {
			continue
		}
		var asBool bool
		if field.Decode(&asBool) == nil {
			named[key] = true
			continue
		}
		var asInt int
		if field.Decode(&asInt) == nil {
			named[key] = true
		}
	}
}

// boolFields maps the yaml key of every bool gate to its field.
func (g *FeatureGates) boolFields() map[string]*bool {
	return map[string]*bool{
		"skill_eval":      &g.SkillEval,
		"briefing":        &g.Briefing,
		"rules_reminder":  &g.RulesReminder,
		"plan_checkpoint": &g.PlanCheckpoint,
		"think_reminder":  &g.ThinkReminder,
		"timestamps":      &g.Timestamps,
		"assoc_context":   &g.AssocContext,
		"loop_warning":    &g.LoopWarning,
	}
}

// MarshalYAML writes only the keys the block names, plus the keys this build
// does not know. A full dump would turn every unnamed key into a written false,
// which outranks feature_defaults and would silently undo the inheritance on any
// config round trip (MergeDefaults, Save). A block built in code carries no key
// set, and such a block states every field, so all of them are written.
func (g FeatureGates) MarshalYAML() (any, error) {
	fields := g.boolFields()
	out := make(map[string]any, len(fields)+1+len(g.extra))
	if g.named == nil {
		for key, field := range fields {
			out[key] = *field
		}
		out["think_reminder_min_chars"] = g.ThinkReminderMinChars
	} else {
		for key := range g.named {
			if field, ok := fields[key]; ok {
				out[key] = *field
				continue
			}
			if key == "think_reminder_min_chars" {
				out[key] = g.ThinkReminderMinChars
			}
		}
	}
	for key, value := range g.extra {
		out[key] = value
	}
	return out, nil
}

// Names reports whether the block states the gate, whether as true or as false.
// A block without a key set was built in code and states every field, so every
// key counts as named: Default() and programmatic configs keep their behaviour.
func (g *FeatureGates) Names(key string) bool {
	if g == nil {
		return false
	}
	if g.named == nil {
		return true
	}
	return g.named[key]
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

	// named records which keys this layer actually wrote. A prompt layer is an
	// overlay, so a written false must be able to switch off a flag the parent
	// layer enabled; a plain bool cannot tell that apart from "not set".
	// Programmatic layers leave it nil and fall back to "only true counts".
	named map[string]bool
	// extra holds keys this build does not know (a newer version's flag, or one
	// the user added by hand). A rewrite of config.yaml goes through
	// MarshalYAML, and dropping them there would silently delete a setting the
	// user made (YM-302).
	extra map[string]any
}

// promptFlagsPlain shadows PromptFlags for decoding without recursing into
// UnmarshalYAML.
type promptFlagsPlain PromptFlags

// UnmarshalYAML keeps the set of keys the layer names, alongside their values.
// A key counts as named when the typed decoder accepts its value: the raw type
// is not enough, because YAML 1.1 words (yes/no/on/off/y/n) arrive as strings in
// an untyped decode but are written into bool fields, and a written "no" has to
// switch off an inherited flag like any other false (YM-465). An entry without a
// value, or one no bool decoder accepts, stays "no opinion" here; note that a
// written value of the wrong type already fails the struct decode above, which
// makes Load fall back to the defaults (pre-existing, not decided here). The
// struct decode runs first on purpose: it is what rejects a self-referential
// merge, and a node graph the decoder refuses must never reach the walk. For the
// same reason the walk never trusts that rejection: it gates merges on the tag
// the decoder uses (a quoted "<<" is an ordinary key the decoder does not even
// look at) and refuses to visit a node twice.
func (f *PromptFlags) UnmarshalYAML(value *yaml.Node) error {
	if err := value.Decode((*promptFlagsPlain)(f)); err != nil {
		return err
	}
	named := make(map[string]bool)
	extra := collectStatedFlags(value, (&PromptFlags{}).flagFields(), named, make(map[*yaml.Node]bool))
	f.named = named
	if len(extra) > 0 {
		f.extra = extra
	} else {
		f.extra = nil
	}
	return nil
}

// collectStatedFlags walks a prompt layer and records which flag keys it states,
// plus the keys this build does not know, and returns the latter. It recurses
// into a merge key, because the struct decoder applies the flags of an anchored
// block just like keys the layer spells out.
//
// Merge detection is tag-gated like yaml.v3's own isMerge: a quoted "<<" is a
// plain key, and the decoder skips an unknown key's value without traversing it,
// so its aliases never reach the decoder's cycle check. Following such a key
// would leave the walk to terminate on its own, which it cannot. seen does that
// job, so the walk never depends on the decoder having validated the graph.
//
// The unknown keys follow the decoder's precedence: a key the layer spells out
// wins over one it inherits through a merge, and the first merge wins over a
// later one.
func collectStatedFlags(node *yaml.Node, known map[string]*bool, named map[string]bool, seen map[*yaml.Node]bool) map[string]any {
	if node == nil || node.Kind != yaml.MappingNode || seen[node] {
		return nil
	}
	seen[node] = true
	stated := make(map[string]any)
	merged := make(map[string]any)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, field := node.Content[i], node.Content[i+1]
		key := keyNode.Value
		if keyNode.Tag == "!!merge" {
			targets := []*yaml.Node{field}
			if field.Kind == yaml.SequenceNode {
				targets = field.Content
			}
			for _, target := range targets {
				block := target
				if target.Kind == yaml.AliasNode {
					block = target.Alias
				}
				for k, v := range collectStatedFlags(block, known, named, seen) {
					if _, taken := merged[k]; !taken {
						merged[k] = v
					}
				}
			}
			continue
		}
		if _, isFlag := known[key]; !isFlag {
			// A key this build does not read states nothing about the flags, but
			// it is the user's setting and has to survive a rewrite.
			var v any
			if err := field.Decode(&v); err != nil {
				log.Printf("warn: prompt layer: unknown key %q has a value this build cannot read; it is dropped on a rewrite", key)
				continue
			}
			stated[key] = v
			continue
		}
		if field.Tag == "!!null" {
			continue
		}
		var asBool bool
		if field.Decode(&asBool) == nil {
			named[key] = true
		}
	}
	for k, v := range stated {
		merged[k] = v
	}
	return merged
}

// MarshalYAML writes only the keys the layer names. A full dump would turn
// every unnamed key into a written false, and under the overlay semantics that
// means "switch off" — a config round-trip would silently disable injections.
// A layer built in code carries no key set; for it, true is the only statement
// (the pre-overlay rule), so only its true keys are written.
func (f PromptFlags) MarshalYAML() (any, error) {
	fields := (&f).flagFields()
	out := make(map[string]any, len(f.named)+len(f.extra))
	if f.named == nil {
		for key, field := range fields {
			if *field {
				out[key] = true
			}
		}
	} else {
		for key := range f.named {
			if field, ok := fields[key]; ok {
				out[key] = *field
			}
		}
	}
	// Keys this build does not know are written back as they came in: they say
	// nothing about the flags, but dropping them would delete a user setting.
	for key, v := range f.extra {
		if _, isFlag := fields[key]; isFlag {
			continue // a known key is written from its field, never from the raw value
		}
		out[key] = v
	}
	return out, nil
}

// flagFields maps every YAML key to its field. Merging and marshalling both go
// through this table, so a newly added flag cannot be forgotten by one of them.
func (f *PromptFlags) flagFields() map[string]*bool {
	return map[string]*bool{
		"prompt_ungate":              &f.Ungate,
		"prompt_rewrite":             &f.Rewrite,
		"prompt_enhance":             &f.Enhance,
		"prompt_tool_prefs":          &f.ToolPrefs,
		"prompt_output_discipline":   &f.OutputDiscipline,
		"prompt_coding_discipline":   &f.CodingDiscipline,
		"prompt_beweislast":          &f.Beweislast,
		"prompt_fable":               &f.Fable,
		"prompt_scope_discipline":    &f.ScopeDiscipline,
		"prompt_delegation_contract": &f.DelegationContract,
		"prompt_clarify_first":       &f.ClarifyFirst,
		"prompt_code_tools_first":    &f.CodeToolsFirst,
		"prompt_wiki_first":          &f.WikiFirst,
		"prompt_pattern_suggest":     &f.PatternSuggest,
	}
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
		// merged is a resolved value, not a layer: keeping the shared key set
		// would let a later merge treat unnamed keys as deliberately off.
		merged.named = nil
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
	// They serve as a base layer if ClaudePrompt is nil or doesn't set a field,
	// but never overrule a flag claude_prompt names explicitly.
	if profile == models.ProfileClaude {
		legacy := p.claudeLegacyFlags()
		var claimed map[string]bool
		if profileFlags != nil {
			claimed = profileFlags.named
		}
		mergeFlagsExcept(merged, legacy, claimed)
	}

	return merged
}

// mergeFlags copies the flags a layer names into dst.
// A named false switches off what a previous layer enabled; a key the layer
// does not mention inherits. Layers built in code (no YAML decode) keep the
// older "only true is a statement" rule.
func mergeFlags(dst, src *PromptFlags) {
	mergeFlagsExcept(dst, src, nil)
}

// mergeFlagsExcept leaves the keys in claimed untouched. The deprecated flat
// fields merge through it: they are a compatibility default for Claude, not a
// statement, so they must not re-enable what claude_prompt switched off.
func mergeFlagsExcept(dst, src *PromptFlags, claimed map[string]bool) {
	if src == nil || dst == nil {
		return
	}
	dstFields := dst.flagFields()
	for key, srcField := range src.flagFields() {
		if claimed[key] {
			continue
		}
		if src.named != nil {
			if _, ok := src.named[key]; !ok {
				continue
			}
		} else if !*srcField {
			continue
		}
		*dstFields[key] = *srcField
	}
	// A key this build does not know rides along with the layer that named it, so
	// a rewrite of the resolved value cannot delete the user's setting either.
	for key, v := range src.extra {
		if _, isFlag := dstFields[key]; isFlag {
			continue
		}
		if dst.extra == nil {
			dst.extra = make(map[string]any)
		}
		dst.extra[key] = v
	}
}

// HTTPConfig controls the OpenClaw HTTP API server.
type HTTPConfig struct {
	Enabled   bool   `yaml:"enabled"`    // start HTTP listener (default: false)
	Listen    string `yaml:"listen"`     // e.g. "127.0.0.1:9377" (default)
	AuthToken string `yaml:"auth_token"` // bearer token, auto-generated if empty
}

// DiagnosticsConfig controls optional diagnostic endpoints on the daemon.
// Everything here is off by default.
type DiagnosticsConfig struct {
	Pprof       bool   `yaml:"pprof"`        // start the localhost pprof endpoint (default: false)
	PprofListen string `yaml:"pprof_listen"` // loopback listen address (default: DefaultPprofListen)
}

// DefaultPprofListen is the loopback address used when diagnostics.pprof_listen
// is empty.
const DefaultPprofListen = "127.0.0.1:7071"

// DefaultProxyListen is the proxy's listen address when proxy.listen is empty.
// Shared with the daemon so the default lives in one place.
const DefaultProxyListen = ":9099"

// PprofEnabled reports whether the pprof endpoint should start. The YESMEM_PPROF
// environment variable overrides the config value (1/true/yes/on forces it on,
// 0/false/no/off forces it off) so diagnostics can be toggled on hosts without
// access to config.yaml.
func (d DiagnosticsConfig) PprofEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("YESMEM_PPROF"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return d.Pprof
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
	DB string `yaml:"db"`
	// BleveIndex is accepted but read by nothing: search moved to SQLite FTS5 and
	// the migration deletes the old directory. The field stays because checkKeys
	// derives its schema from this struct, so removing it would report the key as
	// unknown in every config.yaml the setup template ever shipped.
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
			Listen:                   DefaultProxyListen,
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
				CodeToolsFirst:   true,
			},
			SkillEvalInject: "silent",
			// The glm block mirrors the fresh template: the template has carried it
			// while these defaults did not, so a glm session resolved to
			// feature_defaults instead of the documented block (YM-349).
			ModelFeatures: map[string]*FeatureGates{
				"claude":   {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: true, ThinkReminder: true, Timestamps: false, AssocContext: true, LoopWarning: true},
				"deepseek": {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: false, ThinkReminder: true, Timestamps: true, AssocContext: true, LoopWarning: true},
				"glm":      {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: false, ThinkReminder: true, Timestamps: true, AssocContext: true, LoopWarning: true},
				"gpt":      {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: false, ThinkReminder: false, Timestamps: false, AssocContext: true, LoopWarning: true},
				"openai":   {SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: false, ThinkReminder: false, Timestamps: false, AssocContext: true, LoopWarning: true},
			},
			// assoc_context belongs in the fallback: a model that matches no
			// model_features prefix otherwise loses the associative context that
			// every listed model has (YM-302). It was left out when the gate was
			// introduced, so unlisted models (glm, kimi, ...) ran without it.
			FeatureDefaults: &FeatureGates{
				SkillEval: true, Briefing: true, RulesReminder: true, PlanCheckpoint: true, ThinkReminder: true, Timestamps: true, AssocContext: true, LoopWarning: true,
			},
			CacheKeepaliveEnabled:     true,
			CacheKeepaliveMode:        "5m",
			CacheKeepalivePings5m:     5,
			CacheKeepalivePings1h:     1,
			CacheKeepaliveMinMessages: 10,
			CodeNavMode:               "block",
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
		Diagnostics: DiagnosticsConfig{
			Pprof:       false,
			PprofListen: DefaultPprofListen,
		},
		Update: UpdateConfig{
			AutoUpdate:    true,
			CheckInterval: "6h",
			Channel:       "stable",
		},
		Agents: AgentsConfig{
			DefaultBackend: "claude",
			PermissionKick: true,
		},
		ForkedAgents: ForkedAgentsConfig{
			Enabled:            false,
			Model:              "sonnet",
			TokenGrowthTrigger: 20000,
			MaxForksPerSession: 50,
			MaxCostPerSession:  5.0,
		},
		DoneVerify: DoneVerifyConfig{
			Enabled: true,
			Budget:  "5m",
			MaxWait: "10m",
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

	if issues := schemaIssues(data); len(issues) > 0 {
		warnSchemaIssues(path, issues)
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

	// The installer's template ships every path in the "~/" form
	// (setup.go paths block). Unexpanded, "~" is a literal directory name
	// relative to the process's working directory, so each value is anchored at
	// the home directory before any consumer sees it. A value that cannot be
	// anchored stays as it is, including the empty one: for paths.db an empty
	// value means "not configured".
	home, _ := os.UserHomeDir()
	cfg.Paths.DB = expandHomePath(cfg.Paths.DB, home)
	cfg.Paths.BleveIndex = expandHomePath(cfg.Paths.BleveIndex, home)
	cfg.Paths.Archive = expandHomePath(cfg.Paths.Archive, home)
	cfg.Paths.ClaudeProjects = expandHomePath(cfg.Paths.ClaudeProjects, home)
	cfg.Paths.OpencodeDB = expandHomePath(cfg.Paths.OpencodeDB, home)

	return cfg, nil
}

// expandHomePath resolves a leading "~" against home. It is the one expansion
// every configured path goes through; Go has no portable form for "~user", so a
// value carrying one is left untouched. Surrounding whitespace is trimmed first,
// because a value copied into config.yaml by hand often carries it.
func expandHomePath(value, home string) string {
	value = strings.TrimSpace(value)
	if value == "" || home == "" {
		return value
	}
	switch {
	case value == "~":
		return home
	case strings.HasPrefix(value, "~/"):
		return filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value
}

// ClaudeProjectsDir is the directory Claude Code names its project folders in.
//
// The value comes from config.yaml, which the setup template ships as an
// unexpanded "~/.claude/projects" and nothing normalizes; an empty or still
// relative value would turn a directory scan into a walk of the process's
// working directory, silently and without a line naming the root. Both cases
// fall back to the home default, which is what Default() yields.
func (c *Config) ClaudeProjectsDir() string {
	home, _ := os.UserHomeDir()
	dir := expandHomePath(c.Paths.ClaudeProjects, home)
	if dir == "" || !filepath.IsAbs(dir) {
		// Without HOME there is no default to fall back on, and a relative one
		// would turn the scan into a walk of the working directory: return empty
		// instead, so a caller sees that no directory is configured.
		if home != "" {
			return filepath.Join(home, ".claude", "projects")
		}
		return ""
	}
	return dir
}

// ArchiveDir is the one derivation of the archive directory: paths.archive when
// it is configured, else "archive" inside the data dir. Until YM-381 the knob had
// no reader at all: the daemon always built the archive from its own data dir,
// so the setup template promised a configurable location that nothing honoured.
//
// The loader's default does not count as configured. Default() anchors
// paths.archive at ~/.claude/yesmem whatever data directory the caller runs
// with, so a daemon started against a different one would write its archive into
// the live installation instead of beside its own database. Comparing against
// that default keeps such a call on the data-dir fallback, while an explicitly
// configured path wins.
func (c *Config) ArchiveDir(dataDir string) string {
	fallback := ""
	if dataDir != "" {
		fallback = filepath.Join(dataDir, "archive")
	}
	if c == nil {
		return fallback
	}
	if dir := c.Paths.Archive; dir != "" && dir != Default().Paths.Archive {
		// A relative value has no anchor: Load expands "~" only when a home
		// directory exists, and an unanchored base would put the archive under
		// whatever directory the daemon happens to run in. Fail closed to the
		// data-dir path instead.
		if filepath.IsAbs(dir) {
			return dir
		}
	}
	return fallback
}

// CapsDirPath is the one derivation of the CAP.md directory: the configured
// caps_dir, else "caps" beside the data dir. Four call sites used to derive it
// themselves (daemon start, save_cap, the sandbox mask, the installer), and the
// installer ignored the configuration entirely: it always wrote into
// ~/.claude/caps, so with a configured caps_dir it installed the bundled caps
// where the daemon does not import them. The data dir moves the default with it,
// which is why it is an input here rather than a constant: the daemon, the export
// path and yesmem update all run against a configured data dir. Setup fixes its
// data dir to ~/.claude/yesmem, so on that path the default never moves, and the
// derivation is shared there to keep the two sides from drifting apart (YM-353).
//
// The result is absolute whenever it can be anchored at all, and it is anchored
// the same way in every process. A leading "~" is expanded, and a relative value is
// resolved against the home directory. Resolving it against the working directory
// would restore the split this derivation exists to end: the daemon and yesmem
// update do not share a working directory. Only the "~/" form is expanded, because
// Go has no portable expansion for "~user".
//
// Without a home directory there is nothing to anchor against, and the derivation
// yields no directory at all. That is the fail-closed answer: the watcher imports
// nothing, the sandbox masks nothing and the installer refuses, instead of every
// consumer resolving the same relative value against whatever directory it happens
// to run in.
func CapsDirPath(configured, dataDir string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		if dataDir == "" {
			return ""
		}
		configured = filepath.Join(filepath.Dir(dataDir), "caps")
	}
	return absoluteCapsDir(configured)
}

// absoluteCapsDir expands a leading "~" and anchors a relative path at the home
// directory, the one directory that does not depend on which process is asking. A
// value that cannot be anchored yields "".
func absoluteCapsDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch {
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		case !filepath.IsAbs(dir):
			dir = filepath.Join(home, dir)
		}
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return ""
}

// schemaIssues decodes the document a second time to walk it against the
// schema: Load decodes into the typed struct, which silently drops everything
// the struct does not know.
func schemaIssues(data []byte) []Issue {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return checkKeys(raw)
}

var (
	warnedSchemaMu    sync.Mutex
	warnedSchemaPaths = map[string]string{}
)

// warnSchemaIssues logs once per file and issue set within this process. Load
// runs repeatedly (daemon startup, the config-reading hooks), so a per-process
// dedupe keeps one broken config from repeating the same line there. Each hook
// invocation is its own process and therefore still reports once.
func warnSchemaIssues(path string, issues []Issue) {
	var sig strings.Builder
	for _, issue := range issues {
		sig.WriteString(issue.String())
		sig.WriteString("; ")
	}
	message := sig.String()

	warnedSchemaMu.Lock()
	previous, seen := warnedSchemaPaths[path]
	warnedSchemaPaths[path] = message
	warnedSchemaMu.Unlock()

	if seen && previous == message {
		return
	}
	log.Printf("warn: %s: %d key(s) the schema does not read: %srun 'yesmem migrate' to move misplaced proxy keys", filepath.Base(path), len(issues), message)
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
	if err := writeConfigFile(path, data); err != nil {
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
	if err := writeConfigFile(path, data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// writeConfigFile writes config.yaml atomically with mode 0600: it carries the
// API keys. The data goes into a temporary file beside the target first, so a
// crash leaves either the previous file or the complete new one, never a
// truncated one. WriteFile alone kept the inode and the mode of an existing
// file, so it could neither replace the content in one step nor tighten a file
// that had been created world readable.
//
// The rename replaces the inode: a config.yaml that is a symlink becomes a
// regular file after the first write. The previous target keeps its content and
// its mode, so the operator is told instead of silently ending up with two
// copies.
func writeConfigFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		log.Printf("warn: %s is a symlink; the write replaces the link with a regular file, the old target stays untouched", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), configTempPrefix+"*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Cleanup for every error path. After a successful rename the path is gone
	// and the remove fails harmlessly.
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Sync before the rename: without it the rename can land before the data,
	// and a crash then leaves a correctly named empty file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	// Sync the directory: the rename is durable only once the entry itself is
	// on disk, otherwise a crash can leave the old name pointing at nothing.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		if syncErr := dir.Sync(); syncErr != nil {
			log.Printf("warn: sync config dir: %v", syncErr)
		}
		dir.Close()
	}
	return nil
}

// configBackupKeep bounds how many timestamped copies of config.yaml survive.
// Every copy carries the API keys, and a backup was written on every change
// without ever removing one: after months of edits the directory held a pile of
// key files. 10 covers a week of daily edits.
const configBackupKeep = 10

const (
	// configTempPrefix marks the temporary file an atomic write creates next to
	// its target.
	configTempPrefix = ".config.yaml.tmp-"
	// configTempMaxAge is how long a temp file may sit before it counts as
	// garbage. A crash leaves one behind carrying the API keys; a writer that is
	// still running owns its file, and no write takes that long.
	configTempMaxAge = time.Hour
)

// configWriteLockSuffix names the lock file the incremental config writers take.
// It lives next to config.yaml, so every writer that takes it contends on one
// inode; the installer's full rewrite deliberately does not.
const configWriteLockSuffix = ".lock"

// configWriteLockWait bounds the wait for that lock. A write is a read, a
// marshal and an atomic replace, so milliseconds are normal. A lock held longer
// belongs to a stuck process: the wait then gives up and the write proceeds
// unprotected, which is the behavior before the lock existed, and says so.
const configWriteLockWait = 3 * time.Second

// lockConfigWrite serializes the read-modify-write cycles on one config.yaml.
// The CLI ("yesmem config set") and the daemon (set_config) both read the whole
// document, change one key and write it back; without the lock the second write
// carries the first one's state and drops its change (YM-392).
//
// The caller holds the lock until the returned function runs. An unusable lock
// (path is a directory, no flock support, foreign owner) must not disable config
// writes: that is the pre-lock behavior, so it is kept.
func lockConfigWrite(path string) func() {
	noop := func() {}
	// O_NOFOLLOW: a link planted at the lock name would otherwise be followed and
	// some other file locked. The open then fails, and the pre-lock behavior
	// (write without a lock) is what the caller gets.
	f, err := os.OpenFile(path+configWriteLockSuffix, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		log.Printf("warn: config write lock %s: %v", path, err)
		return noop
	}
	deadline := time.Now().Add(configWriteLockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			log.Printf("warn: config write lock %s unusable: %v", path, err)
			f.Close()
			return noop
		}
		if time.Now().After(deadline) {
			log.Printf("warn: config write lock %s still held after %s, writing without it", path, configWriteLockWait)
			f.Close()
			return noop
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// NextBackupPath returns a free name for the next timestamped copy of path:
// path+".bak."+YYYYMMDDTHHMMSS, plus "-<n>" when a copy of this second already
// exists. The installer keeps its copies in its own directory and needs the same
// uniqueness rule as backupFile, so the choice is exported instead of written
// twice (the second copy of a second would otherwise overwrite the first).
//
// A Stat error that is not "does not exist" is returned: it means the name cannot
// be checked (parent is a file, permission denied, symlink loop), and treating it
// as free either overwrites a copy or loops forever.
func NextBackupPath(path string) (string, error) {
	base := path + ".bak." + time.Now().Format("20060102T150405")
	backupPath := base
	for n := 2; ; n++ {
		_, err := os.Stat(backupPath)
		if os.IsNotExist(err) {
			return backupPath, nil
		}
		if err != nil {
			return "", fmt.Errorf("check backup name %s: %w", backupPath, err)
		}
		backupPath = fmt.Sprintf("%s-%d", base, n)
	}
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
	// Timestamps have second resolution, and a migration writes twice
	// (MigrateConfig, then the placement move). Reusing the name would leave
	// only the post-first-write state, so the pre-migration file survives under
	// a counter suffix instead of being overwritten.
	backupPath, err := NextBackupPath(path)
	if err != nil {
		return err
	}
	// config.yaml carries API keys, so a copy of it is not world readable, and
	// it goes through the same atomic write: an os.WriteFile here would follow a
	// pre-planted symlink at the backup name and truncate whatever it points at.
	if err := writeConfigFile(backupPath, data); err != nil {
		return err
	}
	pruneConfigBackups(path)
	return nil
}

// pruneConfigBackups deletes the oldest copies beyond configBackupKeep. Failing
// to remove a copy does not fail the write it follows: the new state is already
// on disk, and the leftover is cleaned up by the next backup.
func pruneConfigBackups(path string) {
	PruneBackups(filepath.Dir(path), filepath.Base(path))
}

// backupEntry is one timestamped copy: its name carries the ordering signal.
type backupEntry struct {
	name    string
	modTime time.Time
}

// backupOrderLess reports whether a is older than b, that is, whether a is
// pruned first. The modification time orders copies across seconds. Within one
// second the counter suffix is the only difference, and "-10" sorts before "-2"
// lexicographically, so the suffix is compared as a number.
func backupOrderLess(a, b backupEntry) bool {
	if !a.modTime.Equal(b.modTime) {
		return a.modTime.Before(b.modTime)
	}
	if ca, cb := backupCounter(a.name), backupCounter(b.name); ca != cb {
		return ca < cb
	}
	return a.name < b.name
}

// backupCounter reads the counter suffix a backup name carries: the unsuffixed
// first copy of a second is 1, "<timestamp>-2" is 2 and so on.
func backupCounter(name string) int {
	dash := strings.LastIndex(name, "-")
	if dash < 0 {
		return 1
	}
	n, err := strconv.Atoi(name[dash+1:])
	if err != nil {
		return 1
	}
	return n
}

// isTimestampedBackup reports whether name is a copy this writer created:
// base+".bak." followed by YYYYMMDDTHHMMSS and an optional "-<counter>". The
// rotation must not touch an operator's own copy, which often carries a
// descriptive suffix ("before-upgrade") and is exactly what a rollback needs.
func isTimestampedBackup(name, base string) bool {
	rest, ok := strings.CutPrefix(name, base+".bak.")
	if !ok || len(rest) < len("20060102T150405") || rest[8] != 'T' {
		return false
	}
	if _, err := time.Parse("20060102T150405", rest[:len("20060102T150405")]); err != nil {
		return false
	}
	rest = rest[len("20060102T150405"):]
	if rest == "" {
		return true
	}
	if !strings.HasPrefix(rest, "-") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(rest, "-"))
	return err == nil && n > 0
}

// PruneBackups keeps the newest configBackupKeep copies in dir whose name is a
// timestamped backup of base, and removes stale temporary files. The installer
// keeps its copies in a directory of its own, so the pruning is exported instead
// of living twice.
func PruneBackups(dir, base string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A directory without backups is the normal case, not an error.
		if !os.IsNotExist(err) {
			log.Printf("warn: config backup pruning: %v", err)
		}
		return
	}
	var found []backupEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), configTempPrefix) {
			// A crash between create and rename leaves a 0600 file with the API
			// keys. Age, not presence, decides: a fresh one belongs to a writer
			// that is still running.
			info, err := entry.Info()
			if err != nil || time.Since(info.ModTime()) < configTempMaxAge {
				continue
			}
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				log.Printf("warn: remove stale config temp file %s: %v", entry.Name(), err)
			}
			continue
		}
		if !isTimestampedBackup(entry.Name(), base) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		found = append(found, backupEntry{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(found) <= configBackupKeep {
		return
	}
	sort.Slice(found, func(i, j int) bool { return backupOrderLess(found[i], found[j]) })
	for _, stale := range found[:len(found)-configBackupKeep] {
		if err := os.Remove(filepath.Join(dir, stale.name)); err != nil && !os.IsNotExist(err) {
			log.Printf("warn: remove config backup %s: %v", stale.name, err)
		}
	}
}

// WriteFile writes a config.yaml-shaped file the way every writer must do it:
// atomically, with mode 0600. The installer uses it for the template and for
// the copy it keeps, so the hardening does not depend on which code path
// creates the file.
func WriteFile(path string, data []byte) error {
	return writeConfigFile(path, data)
}

// ProxyStateKey maps a config path onto the proxy_state key the running proxy
// and get_config read first. Only proxy keys have a runtime slot: the proxy
// reads them as config_override:<the key below proxy:>, and a per-model
// threshold as config_override:token_threshold:<model>. The second return value
// reports whether the path has a runtime slot at all.
func ProxyStateKey(dotPath string) (string, bool) {
	normKey, err := NormalizeSetPath(dotPath)
	if err != nil {
		return "", false
	}
	if model, ok := strings.CutPrefix(normKey, "proxy.token_thresholds."); ok {
		return "config_override:token_threshold:" + model, true
	}
	rest, ok := strings.CutPrefix(normKey, "proxy.")
	if !ok {
		return "", false
	}
	return "config_override:" + rest, true
}

// SessionOverrideStateKey is the proxy_state key prefix for one session's runtime
// override (a token threshold). It is a family of its own, deliberately not a
// suffix of config_override: that namespace carries the per-model slots, and their
// suffix is a model name the caller chooses, so a session id and a model name
// cannot be told apart there. The daemon writes this key (set_config with a
// session_id), the proxy reads it (sessionOverrideKeys, internal/proxy).
const SessionOverrideStateKey = "session_override:token_threshold"

// ProxyRuntimeStateKey maps a config path onto the runtime slot the RUNNING
// proxy re-reads by itself (refreshConfigOverrides): only the token-threshold
// family. Every other proxy key is applied at proxy start, so a slot written for
// it would never be read. A caller that mirrors must use this function, not
// ProxyStateKey, or it reports an effect that does not exist.
func ProxyRuntimeStateKey(dotPath string) (string, bool) {
	stateKey, ok := ProxyStateKey(dotPath)
	if !ok {
		return "", false
	}
	if stateKey == "config_override:token_threshold" || strings.HasPrefix(stateKey, "config_override:token_threshold:") {
		return stateKey, true
	}
	return "", false
}

// LoadProxyTokenThresholds reads only proxy.token_thresholds from config.yaml
// and reports a read or parse error. Load cannot serve a caller that must tell
// "the file names no model" from "the file is broken": Load swallows both into
// a fallback to defaults, which for the proxy would mean dropping every
// configured threshold because of a typo.
//
// The result carries the built-in defaults overlaid by the file, exactly what
// Load produces. A file without the block is the normal case (any config.yaml
// created by `config set`), and a caller that took only the file would drop
// every default threshold.
func LoadProxyTokenThresholds(path string) (map[string]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	thresholds := make(map[string]int, len(Default().Proxy.TokenThresholds))
	for model, threshold := range Default().Proxy.TokenThresholds {
		thresholds[model] = threshold
	}
	raw := struct {
		Proxy struct {
			TokenThresholds map[string]int `yaml:"token_thresholds"`
		} `yaml:"proxy"`
	}{}
	raw.Proxy.TokenThresholds = thresholds
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return raw.Proxy.TokenThresholds, nil
}

// GetValue reads a specific value from config.yaml by dot-path.
// e.g. "extraction.model" returns the extraction model.
// Returns nil if the path does not exist (no error).
func GetValue(path, dotPath string) (any, error) {
	// Read the same place SetValue writes: a bare proxy key lives under proxy:.
	// An unknown path stays a miss instead of an error, which is GetValue's
	// contract.
	if normalized, normalizeErr := NormalizeSetPath(dotPath); normalizeErr == nil {
		dotPath = normalized
	}
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

	// A map key may contain a dot, so the segments come from the schema, not
	// from a plain split.
	segments, err := pathSegments(dotPath)
	if err != nil {
		return nil, nil // unknown path = no value
	}
	current := any(raw)
	for _, segment := range segments {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, nil // path not found
		}
		current, ok = m[segment]
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
// The path is normalized onto its schema location first: a known proxy key
// given without the "proxy." prefix is written under proxy:, and a key the
// schema does not know is rejected (see NormalizeSetPath). The value must fit
// the field's type, because a value that does not makes Load reject the whole
// file and fall back to defaults.
func SetValue(path, dotPath string, value any) error {
	dotPath, err := NormalizeSetPath(dotPath)
	if err != nil {
		return err
	}
	value, err = CoerceValueForPath(dotPath, value)
	if err != nil {
		return err
	}
	segments, err := pathSegments(dotPath)
	if err != nil {
		return err
	}
	// The whole read-modify-write runs under the write lock: two writers that
	// read the same document would otherwise each write back their own view, and
	// the second one drops the first change.
	unlock := lockConfigWrite(path)
	defer unlock()

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
	current := raw
	for i, segment := range segments {
		if i == len(segments)-1 {
			current[segment] = value
			break
		}
		next, ok := current[segment]
		if !ok {
			nextMap := map[string]any{}
			current[segment] = nextMap
			current = nextMap
		} else if m, ok := next.(map[string]any); ok {
			current = m
		} else {
			// Overwrite scalar with map to continue path
			nextMap := map[string]any{}
			current[segment] = nextMap
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
	return writeConfigFile(path, out)
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
		// Config-wins-over-env: an explicit openai_api_key in config.yaml
		// (e.g. a DeepSeek key paired with api.deepseek.com) must NOT be
		// silently overridden by OPENAI_API_KEY from .bashrc. Env is only a
		// fallback when config has no key set.
		if k := strings.TrimSpace(c.API.OpenAIAPIKey); k != "" {
			return k
		}
		return os.Getenv("OPENAI_API_KEY")
	}
	if k := strings.TrimSpace(c.API.APIKey); k != "" {
		return k
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// ResolvedOpenAIBaseURL returns the configured OpenAI-compatible base URL.
func (c *Config) ResolvedOpenAIBaseURL() string {
	if c == nil {
		return ""
	}
	if k := strings.TrimSpace(c.API.OpenAIBaseURL); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
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
