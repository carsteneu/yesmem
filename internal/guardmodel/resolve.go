package guardmodel

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Resolution is where the guard sends its judgement call.
type Resolution struct {
	Model string
	// Fallbacks is one step down the account's haiku list. The plugin steps down on 404/403; the Go guard
	// keeps its call path unchanged and reports the list only.
	Fallbacks []string
	BaseURL   string
	APIKey    string
	APIType   string // APITypeAnthropic or APITypeOpenAI
	NPM       string // the provider's module, which decides the optional request fields
	Source    string // where Model came from
	Host      string // the endpoint host, for the log line and the setup report
}

// RequestURL is the endpoint the guard posts to, in the format APIType names.
func (r *Resolution) RequestURL() string {
	if r.APIType == APITypeAnthropic {
		return strings.TrimRight(r.BaseURL, "/") + "/v1/messages"
	}
	return strings.TrimRight(r.BaseURL, "/") + "/v1/chat/completions"
}

// ConfigLite is the part of config.yaml the guard reads.
type ConfigLite struct {
	Extraction struct {
		Model string `yaml:"model"`
	} `yaml:"extraction"`
	RuleGuard struct {
		Model string `yaml:"model"`
	} `yaml:"rule_guard"`
	Proxy struct {
		// ProviderTargets routes a model to a custom upstream. A gateway model is usually not in models.json,
		// so this table is the only place its endpoint exists.
		ProviderTargets map[string]string `yaml:"provider_targets"`
	} `yaml:"proxy"`
}

type authEntry struct {
	Key string `json:"key"`
}

// firstPartyDefaults maps provider IDs to their default API endpoints.
var firstPartyDefaults = map[string]string{
	"openai":    "https://api.openai.com/v1",
	"deepseek":  "https://api.deepseek.com",
	"mistral":   "https://api.mistral.ai/v1",
	"anthropic": AnthropicBase,
	"google":    "https://generativelanguage.googleapis.com",
}

// Resolve finds the credential, the request format and the model for one guard call.
//
// The order is the plugin's: the opencode providers first, because an opencode installation has its key
// there, then an Anthropic credential for the installations that have no provider holding the guard's model.
func Resolve(home, dataDir string) (*Resolution, error) {
	registry := LoadRegistry(filepath.Join(home, ".cache", "opencode", "models.json"))
	auth := loadAuth(filepath.Join(home, ".local", "share", "opencode", "auth.json"))
	cfg := loadConfig(filepath.Join(dataDir, "config.yaml"))

	// The plugin's override, and the reason it exists: an explicit endpoint, key and model win over anything
	// the installation happens to hold.
	if override := strings.TrimSpace(os.Getenv("YESMEM_GUARD_URL")); override != "" {
		baseURL := strings.TrimRight(override, "/")
		apiKey := os.Getenv("YESMEM_GUARD_KEY")
		npm := os.Getenv("YESMEM_GUARD_NPM")
		resolution := &Resolution{
			BaseURL: baseURL,
			APIKey:  apiKey,
			APIType: APITypeOpenAI,
			NPM:     npm,
			Host:    hostOf(baseURL),
		}
		transport := Transport(npm, baseURL)
		if transport == TransportAnthropic {
			resolution.APIType = APITypeAnthropic
		}
		if envModel := strings.TrimSpace(os.Getenv("YESMEM_GUARD_MODEL")); envModel != "" {
			resolution.Model, resolution.Source = envModel, SourceEnv
		} else if resolution.APIType == APITypeAnthropic {
			resolution.Model, resolution.Fallbacks, resolution.Source = ResolveModel("", apiKey, baseURL, registry, home)
		} else {
			resolution.Model, resolution.Source = DefaultOpenAIModel, SourceFallback
		}
		return resolution, nil
	}

	configured := strings.TrimSpace(cfg.RuleGuard.Model)
	// The provider search is keyed on the model the guard was told to use. The candidates are tried in
	// order: a newer key that no provider hosts must not hide the one that is hosted, which is how an
	// installation kept its model before rule_guard.model existed at all.
	searchModels := make([]string, 0, 3)
	for _, candidate := range []string{strings.TrimSpace(os.Getenv("YESMEM_GUARD_MODEL")), configured, cfg.Extraction.Model} {
		if candidate != "" {
			searchModels = append(searchModels, candidate)
		}
	}
	if len(searchModels) == 0 {
		searchModels = append(searchModels, DefaultOpenAIModel)
	}

	baseURL, apiKey, npm, searchModel := "", "", "", ""
	matchedConfigured := false
	for _, candidate := range searchModels {
		matched, endpoint, key, providerNPM := providerWithKey(registry, auth, candidate)
		if !matched {
			continue
		}
		baseURL, apiKey, npm, searchModel = endpoint, key, providerNPM, candidate
		// An explicit rule_guard.model is a pin for a provider that knows the model. Outside that match it
		// says nothing about this credential and must not decide the Anthropic request.
		matchedConfigured = configured != "" && candidate == configured
		break
	}

	// A gateway model is served by a custom upstream from config.yaml and authenticated with the opencode
	// "gateway" key. The plugin reads the same table; without it, a gateway installation loses its guard.
	if apiKey == "" {
		for _, candidate := range searchModels {
			if target := providerTarget(cfg.Proxy.ProviderTargets, candidate); target != "" {
				baseURL, apiKey, npm, searchModel = strings.TrimRight(target, "/"), auth["gateway"].Key, "@ai-sdk/openai-compatible", candidate
				matchedConfigured = configured != "" && candidate == configured
				break
			}
		}
	}

	if apiKey == "" {
		if credential, ok := anthropicCredential(home, registry, auth); ok {
			baseURL, apiKey, npm = credential.baseURL, credential.apiKey, AnthropicNPM
		}
	}
	if apiKey == "" || baseURL == "" {
		return nil, errors.New("no guard config found (tried OpenCode + Anthropic)")
	}

	apiType := Transport(npm, baseURL)
	resolution := &Resolution{
		BaseURL: baseURL,
		APIKey:  apiKey,
		APIType: APITypeOpenAI,
		NPM:     npm,
		Host:    hostOf(baseURL),
	}
	if apiType == TransportAnthropic {
		resolution.APIType = APITypeAnthropic
	}

	if apiType == TransportAnthropic {
		pinned := ""
		if matchedConfigured {
			pinned = configured
		}
		resolution.Model, resolution.Fallbacks, resolution.Source = ResolveModel(pinned, apiKey, baseURL, registry, home)
		return resolution, nil
	}

	// The model is the one the provider search resolved, not a re-derivation: those two disagreed once
	// already and the guard asked a model its provider never listed.
	resolution.Model = searchModel
	switch searchModel {
	case strings.TrimSpace(os.Getenv("YESMEM_GUARD_MODEL")):
		resolution.Source = SourceEnv
	case configured:
		resolution.Source = SourceConfig
	case cfg.Extraction.Model:
		resolution.Source = SourceExtraction
	default:
		resolution.Source = SourceFallback
	}
	return resolution, nil
}

// providerTarget is the routing entry for a model: an exact key first, then a case-insensitive match.
func providerTarget(targets map[string]string, model string) string {
	if len(targets) == 0 || model == "" {
		return ""
	}
	if target := targets[model]; target != "" {
		return target
	}
	for key, target := range targets {
		if strings.EqualFold(key, model) {
			return target
		}
	}
	return ""
}

// providerWithKey finds the provider entry that hosts the model and carries a key for it.
func providerWithKey(registry Registry, auth map[string]authEntry, model string) (bool, string, string, string) {
	for id, entry := range registry {
		if _, ok := entry.Models[model]; !ok {
			continue
		}
		key := auth[id].Key
		if key == "" {
			continue
		}
		endpoint := entry.API
		if endpoint == "" {
			endpoint = firstPartyDefaults[id]
		}
		return true, endpoint, key, entry.NPM
	}
	return false, "", "", ""
}

// anthropicCredential finds an Anthropic API key for an installation whose providers carry none for the
// guard's model. An OAuth subscription is not one of these: its auth.json entry has no key field, so a Claude
// subscription stays without a guard instead of the guard borrowing a token that is not for API calls.
func anthropicCredential(home string, registry Registry, auth map[string]authEntry) (struct{ baseURL, apiKey string }, bool) {
	var credential struct{ baseURL, apiKey string }
	credential.baseURL = AnthropicBase
	if entry, ok := registry["anthropic"]; ok && entry.API != "" {
		credential.baseURL = entry.API
	}
	if key := auth["anthropic"].Key; key != "" {
		credential.apiKey = key
		return credential, true
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		credential.apiKey = key
		return credential, true
	}
	for _, path := range []string{
		filepath.Join(home, ".claude", "config.json"),
		filepath.Join(home, ".claude.json"),
	} {
		var file struct {
			PrimaryAPIKey string `json:"primaryApiKey"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &file) != nil {
			continue
		}
		if file.PrimaryAPIKey != "" {
			credential.apiKey = file.PrimaryAPIKey
			return credential, true
		}
	}
	return credential, false
}

func loadAuth(path string) map[string]authEntry {
	auth := make(map[string]authEntry)
	data, err := os.ReadFile(path)
	if err != nil {
		return auth
	}
	if json.Unmarshal(data, &auth) != nil {
		return make(map[string]authEntry)
	}
	return auth
}

func loadConfig(path string) ConfigLite {
	var cfg ConfigLite
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return ConfigLite{}
	}
	return cfg
}

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "(unparseable endpoint)"
	}
	return parsed.Host
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
