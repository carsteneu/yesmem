// Package guardmodel resolves which model the rule guard asks and in which request format.
//
// Consumers: the guard core (internal/guard), which both the Claude Code hook and the daemon RPC run, and
// yesmem setup, which reports at the end of an installation what the guard will use. The opencode plugin
// has no resolution of its own any more: it asks the daemon, which resolves here.
package guardmodel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// AnthropicNPM is the provider module that speaks the Anthropic messages format.
	AnthropicNPM = "@ai-sdk/anthropic"
	// AnthropicBase is the first-party Anthropic endpoint.
	AnthropicBase = "https://api.anthropic.com"

	// FallbackModel is the one model name this package holds. It is reached only when nothing names a model
	// and neither the account's model list nor the model registry can be read: the first run of an offline
	// installation.
	FallbackModel = "claude-haiku-4-5-20251001"
	// DefaultOpenAIModel is the model of the OpenAI-compatible path when nothing names one. It stays: an
	// empty default would deactivate the guard on every installation that is not Anthropic.
	DefaultOpenAIModel = "deepseek-v4-flash"

	// APITypeAnthropic and APITypeOpenAI name the two request shapes the guard can speak. APITypeOpenAI keeps
	// its historical value: it says the credential came from the opencode configuration.
	APITypeAnthropic = "anthropic"
	APITypeOpenAI    = "opencode"

	// TransportAnthropic and TransportOpenAI are the answers of Transport.
	TransportAnthropic = "anthropic"
	TransportOpenAI    = "openai"

	// SourceEnv, SourceConfig, SourceAccount, SourceRegistry and SourceFallback name where a model came from,
	// so the setup report can say it instead of presenting every resolution as equal.
	SourceEnv        = "YESMEM_GUARD_MODEL"
	SourceConfig     = "rule_guard.model"
	SourceAccount    = "account model list"
	SourceRegistry   = "models.json"
	SourceExtraction = "extraction.model"
	SourceFallback   = "built-in fallback"

	anthropicVersion = "2023-06-01"
)

// CacheTTL is how long an account's model list is reused. A variable so a test can shorten it.
var CacheTTL = 24 * time.Hour

// modelListTimeout bounds the one request that fills the cache. Setup pays it too, at the end of an
// installation, so it is short on purpose.
var modelListTimeout = 3 * time.Second

// CachePath is the file both guards use for the account's model list.
func CachePath(home string) string {
	return filepath.Join(home, ".claude", "yesmem", "guard_models.json")
}

// Transport returns the request shape the resolved provider speaks. Derived from the provider entry itself:
// its npm module says it directly, and a provider without one still speaks Anthropic when it answers on the
// Anthropic host. A gateway in front of Anthropic reads as openai, which is correct: a gateway speaks the
// OpenAI format.
func Transport(npm, baseURL string) string {
	if npm == AnthropicNPM {
		return TransportAnthropic
	}
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host == "api.anthropic.com" {
		return TransportAnthropic
	}
	return TransportOpenAI
}

// Registry is opencode's models.json, as far as the guard needs it.
type Registry map[string]ProviderEntry

// ProviderEntry is one provider in the registry.
type ProviderEntry struct {
	API    string                `json:"api"`
	NPM    string                `json:"npm"`
	Models map[string]ModelEntry `json:"models"`
}

// ModelEntry carries the release date that makes a model the newest of its family.
type ModelEntry struct {
	ReleaseDate string `json:"release_date"`
}

// LoadRegistry reads models.json. A missing or unreadable file is an empty registry, not an error: the model
// chain has fallbacks for exactly that case.
func LoadRegistry(path string) Registry {
	data, err := os.ReadFile(path)
	if err != nil {
		return Registry{}
	}
	var registry Registry
	if json.Unmarshal(data, &registry) != nil {
		return Registry{}
	}
	return registry
}

type modelCache struct {
	FetchedAt string   `json:"fetched_at"`
	Models    []string `json:"models"`
}

// freshModels returns the cached list while it is younger than CacheTTL.
func freshModels(cache modelCache, now time.Time) ([]string, bool) {
	if len(cache.Models) == 0 && cache.FetchedAt == "" {
		return nil, false
	}
	fetched, err := time.Parse(time.RFC3339, cache.FetchedAt)
	if err != nil || now.Sub(fetched) >= CacheTTL {
		return nil, false
	}
	return cache.Models, true
}

func writeModelCache(home string, models []string) {
	if err := os.MkdirAll(filepath.Dir(CachePath(home)), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(modelCache{FetchedAt: time.Now().UTC().Format(time.RFC3339), Models: models})
	if err != nil {
		return
	}
	_ = os.WriteFile(CachePath(home), data, 0o600)
}

// modelListURL is the endpoint that answers "which models does this account have". The environment override
// exists so a test can point it at a local server: no test may reach api.anthropic.com.
func modelListURL(baseURL string) string {
	if override := os.Getenv("YESMEM_GUARD_MODELS_URL"); override != "" {
		return override
	}
	return strings.TrimRight(baseURL, "/") + "/v1/models?limit=100"
}

type modelListEntry struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// Haikus returns the haiku ids the credential's account offers, newest first.
//
// At most one request per CacheTTL: the answer is cached on disk, and a failed request is cached as empty for
// the same window. Without that, an endpoint that is down would cost a timeout on every tool call.
func Haikus(apiKey, baseURL, home string) []string {
	if cached, ok := freshModels(readModelCache(home), time.Now()); ok {
		return cached
	}
	models := []string{}
	if apiKey != "" {
		models = fetchHaikus(apiKey, baseURL)
	}
	writeModelCache(home, models)
	return models
}

func readModelCache(home string) modelCache {
	var cache modelCache
	data, err := os.ReadFile(CachePath(home))
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(data, &cache)
	return cache
}

func fetchHaikus(apiKey, baseURL string) []string {
	client := &http.Client{
		Timeout: modelListTimeout,
		// The endpoint may redirect; following one would replay the credential at whatever host it names. The
		// plugin sets redirect: "error" for the same reason.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest("GET", modelListURL(baseURL), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	var payload struct {
		Data []modelListEntry `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&payload) != nil {
		return nil
	}
	type dated struct {
		id      string
		created time.Time
	}
	haikus := make([]dated, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if !strings.Contains(strings.ToLower(entry.ID), "haiku") {
			continue
		}
		created, _ := time.Parse(time.RFC3339, entry.CreatedAt)
		haikus = append(haikus, dated{id: entry.ID, created: created})
	}
	// Newest first. The endpoint returns them in an order it does not promise, and the guard asks for the
	// newest haiku, not whichever one happened to come first.
	for i := 1; i < len(haikus); i++ {
		for j := i; j > 0 && haikus[j].created.After(haikus[j-1].created); j-- {
			haikus[j], haikus[j-1] = haikus[j-1], haikus[j]
		}
	}
	ids := make([]string, 0, len(haikus))
	for _, entry := range haikus {
		ids = append(ids, entry.id)
	}
	return ids
}

// newestHaikuInRegistry is the fallback when the account's own list is out of reach.
func newestHaikuInRegistry(registry Registry) string {
	best := ""
	bestDate := ""
	for _, entry := range registry {
		for modelID, model := range entry.Models {
			if !strings.Contains(strings.ToLower(modelID), "haiku") {
				continue
			}
			if best == "" || model.ReleaseDate > bestDate {
				best = modelID
				bestDate = model.ReleaseDate
			}
		}
	}
	return best
}

// ResolveModel walks the model chain: the environment, the configured model, the account's list, the model
// registry, and finally the one constant. Only the Anthropic path uses it; the OpenAI path keeps the model
// the provider search was keyed on.
func ResolveModel(configured, apiKey, baseURL string, registry Registry, home string) (model string, fallbacks []string, source string) {
	if env := strings.TrimSpace(os.Getenv("YESMEM_GUARD_MODEL")); env != "" {
		return env, nil, SourceEnv
	}
	if configured != "" {
		return configured, nil, SourceConfig
	}
	if haikus := Haikus(apiKey, baseURL, home); len(haikus) > 0 {
		// One step down the list, no more: a model the endpoint refuses is worth one retry, not a search.
		if len(haikus) > 1 {
			fallbacks = []string{haikus[1]}
		}
		return haikus[0], fallbacks, SourceAccount
	}
	if id := newestHaikuInRegistry(registry); id != "" {
		return id, nil, SourceRegistry
	}
	return FallbackModel, nil, SourceFallback
}
