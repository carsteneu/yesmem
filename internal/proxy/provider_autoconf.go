package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// modelsJSONEntry represents one provider entry in OpenCode's models.json cache.
type modelsJSONEntry struct {
	ID     string                     `json:"id"`
	NPM    string                     `json:"npm"`
	API    string                     `json:"api"`
	Env    []string                   `json:"env"`
	Models map[string]modelsJSONModel `json:"models"`
}

type modelsJSONModel struct {
	ID string `json:"id"`
}

// opencodeProviderBlock represents a provider section in opencode.json.
type opencodeProviderBlock struct {
	APIKey  string                  `json:"apiKey"`
	Env     []string                `json:"env"`
	Options opencodeProviderOptions `json:"options"`
}

type opencodeProviderOptions struct {
	BaseURL string `json:"baseURL"`
}

// opencodeAuthEntry represents a credential entry in opencode's auth.json.
type opencodeAuthEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// autoDiscoveredProvider maps a model ID to its upstream API endpoint.
type autoDiscoveredProvider struct {
	ModelID        string // e.g. "big-pickle"
	UpstreamURL    string // e.g. "https://opencode.ai/zen"
	ProviderID     string // e.g. "opencode"
	IsOpenAICompat bool   // uses @ai-sdk/openai-compatible
}

// well-known first-party providers where models.json has no "api" field.
var firstPartyDefaults = map[string]string{
	"openai":    "https://api.openai.com",
	"anthropic": "https://api.anthropic.com",
	"google":    "https://generativelanguage.googleapis.com",
	"groq":      "https://api.groq.com",
	"mistral":   "https://api.mistral.ai",
}

// openaiCompatibleNPM is the npm package name for OpenAI-compatible providers.
const openaiCompatibleNPM = "@ai-sdk/openai-compatible"

// loadModelsJSON reads the OpenCode models cache and returns a map of providerID → entry.
// Returns nil and no error if the file doesn't exist (OpenCode not installed).
func loadModelsJSON(path string) (map[string]modelsJSONEntry, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".cache", "opencode", "models.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // OpenCode not installed — skip silently
		}
		return nil, fmt.Errorf("read models.json: %w", err)
	}
	var entries map[string]modelsJSONEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse models.json: %w", err)
	}
	return entries, nil
}

// loadOpenCodeConfig reads opencode.json and extracts provider configurations.
// Returns a map of providerID → provider block.
func loadOpenCodeConfig(path string) (map[string]opencodeProviderBlock, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".config", "opencode", "opencode.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No opencode config — skip silently
		}
		return nil, fmt.Errorf("read opencode.json: %w", err)
	}
	// opencode.json has a top-level "provider" key
	var cfg struct {
		Provider map[string]opencodeProviderBlock `json:"provider"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse opencode.json: %w", err)
	}
	return cfg.Provider, nil
}

// loadOpenCodeAuth reads opencode's auth.json and returns a map of providerID → apiKey.
// Returns nil and no error if the file doesn't exist.
func loadOpenCodeAuth(path string) (map[string]string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".local", "share", "opencode", "auth.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read auth.json: %w", err)
	}
	var entries map[string]opencodeAuthEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse auth.json: %w", err)
	}
	result := make(map[string]string, len(entries))
	for providerID, entry := range entries {
		if entry.Key != "" {
			result[providerID] = entry.Key
		}
	}
	return result, nil
}

// hasProviderCredentials checks if a provider has valid credentials configured.
// Checks opencode.json fields first, then env vars from models.json, then auth.json.
func hasProviderCredentials(providerID string, block opencodeProviderBlock, models map[string]modelsJSONEntry, auth map[string]string) bool {
	if block.APIKey != "" {
		return true
	}
	for _, envVar := range block.Env {
		if os.Getenv(envVar) != "" {
			return true
		}
	}
	// Check models.json for env var hints (used by default)
	if entry, ok := models[providerID]; ok {
		for _, envVar := range entry.Env {
			if val := os.Getenv(envVar); val != "" {
				return true
			}
		}
	}
	// Check auth.json
	if _, ok := auth[providerID]; ok {
		return true
	}
	return false
}

// discoverOpenAICompatibleProviders finds all OpenAI-compatible providers from models.json
// that are active in opencode.json. Returns model→upstream mappings.
// Also returns a list of inactive (non-OpenAI-compatible) providers for YAML comment generation.
func discoverOpenAICompatibleProviders(
	models map[string]modelsJSONEntry,
	opencode map[string]opencodeProviderBlock,
	auth map[string]string,
) (active []autoDiscoveredProvider, inactive []autoDiscoveredProvider) {
	for providerID, entry := range models {
		block, isConfigured := opencode[providerID]
		if !isConfigured || !hasProviderCredentials(providerID, block, models, auth) {
			continue // not used by this user
		}

		upstreamURL := entry.API
		if upstreamURL == "" {
			// First-party provider — look up hardcoded default
			var ok bool
			upstreamURL, ok = firstPartyDefaults[providerID]
			if !ok {
				continue // unknown provider, skip
			}
		}
		upstreamURL = strings.TrimRight(upstreamURL, "/")

		isOpenAICompat := entry.NPM == openaiCompatibleNPM ||
			entry.NPM == "@ai-sdk/openai" ||
			entry.NPM == "@ai-sdk/mistral" ||
			(entry.NPM == "" && providerID == "openai")

		for modelID := range entry.Models {
			p := autoDiscoveredProvider{
				ModelID:        modelID,
				UpstreamURL:    upstreamURL,
				ProviderID:     providerID,
				IsOpenAICompat: isOpenAICompat,
			}
			if isOpenAICompat {
				active = append(active, p)
			} else {
				inactive = append(inactive, p)
			}
		}
	}
	return active, inactive
}

// buildAutoProviderTargets converts discovered providers to a modelID→URL map.
// For duplicate model names across providers, last one wins (non-deterministic, but
// user should use explicit provider_targets for ambiguous cases).
func buildAutoProviderTargets(providers []autoDiscoveredProvider) map[string]string {
	m := make(map[string]string, len(providers))
	for _, p := range providers {
		m[strings.ToLower(p.ModelID)] = p.UpstreamURL
		// Also add providerID/modelID so zai-coding-plan/glm-5.2 resolves correctly.
		// autoProviderTargets is only checked after explicit provider_targets,
		// so the fallback order is correct.
		m[strings.ToLower(p.ProviderID+"/"+p.ModelID)] = p.UpstreamURL
	}
	return m
}

// maybePatchOpenCodeBaseURL sets baseURL to localhost:9099/v1 for active OpenAI-compatible
// providers that don't already have a baseURL configured. Returns true if the file was modified.
func maybePatchOpenCodeBaseURL(path string, active []autoDiscoveredProvider) (bool, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, err
		}
		path = filepath.Join(home, ".config", "opencode", "opencode.json")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read opencode.json: %w", err)
	}

	// Deduplicate: one provider may have many models
	seenProviders := make(map[string]bool)
	var providersToPatch []string
	for _, p := range active {
		if seenProviders[p.ProviderID] {
			continue
		}
		seenProviders[p.ProviderID] = true
		providersToPatch = append(providersToPatch, p.ProviderID)
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("parse opencode.json: %w", err)
	}

	providerSection, ok := cfg["provider"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	modified := false
	for _, providerID := range providersToPatch {
		pBlock, ok := providerSection[providerID].(map[string]interface{})
		if !ok {
			continue
		}
		options, ok := pBlock["options"].(map[string]interface{})
		if !ok {
			// No options block yet — create one
			options = map[string]interface{}{}
			pBlock["options"] = options
		}
		if _, hasBaseURL := options["baseURL"]; hasBaseURL {
			continue // user already configured baseURL
		}
		options["baseURL"] = "http://localhost:9099/v1"
		modified = true
	}

	if !modified {
		return false, nil
	}

	// Write back with indentation
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal opencode.json: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0644); err != nil {
		return false, fmt.Errorf("write opencode.json: %w", err)
	}
	return true, nil
}

// runAutoDiscovery loads models.json and opencode.json, discovers active providers,
// builds autoProviderTargets, and optionally patches opencode.json baseURLs.
// Returns the auto-discovered modelID→URL map (nil on any non-fatal error).
func runAutoDiscovery(logger *log.Logger) map[string]string {
	models, err := loadModelsJSON("")
	if err != nil {
		logger.Printf("[autoconf] WARNING: failed to load models.json: %v", err)
		return nil
	}
	if models == nil {
		logger.Printf("[autoconf] models.json not found — skipping auto-discovery (OpenCode not installed?)")
		return nil
	}

	oc, err := loadOpenCodeConfig("")
	if err != nil {
		logger.Printf("[autoconf] WARNING: failed to load opencode.json: %v", err)
		return nil
	}
	if oc == nil {
		logger.Printf("[autoconf] opencode.json not found — skipping auto-discovery")
		return nil
	}

	auth, err := loadOpenCodeAuth("")
	if err != nil {
		logger.Printf("[autoconf] WARNING: failed to load auth.json: %v", err)
		// Continue — auth.json is optional; some providers use env vars
	}

	active, inactive := discoverOpenAICompatibleProviders(models, oc, auth)

	if len(active) == 0 {
		logger.Printf("[autoconf] no active OpenAI-compatible providers found")
		return nil
	}

	// Log discovered providers
	seen := make(map[string]bool)
	var names []string
	for _, p := range active {
		if !seen[p.ProviderID] {
			seen[p.ProviderID] = true
			names = append(names, p.ProviderID)
		}
	}
	logger.Printf("[autoconf] discovered %d providers (%s) with %d total models",
		len(names), strings.Join(names, ", "), len(active))

	// Log inactive providers (for future reference)
	if len(inactive) > 0 {
		seen = make(map[string]bool)
		names = names[:0]
		for _, p := range inactive {
			if !seen[p.ProviderID] {
				seen[p.ProviderID] = true
				names = append(names, p.ProviderID)
			}
		}
		logger.Printf("[autoconf] %d non-OpenAI-compatible providers skipped (%s)",
			len(names), strings.Join(names, ", "))
	}

	// Patch opencode.json
	patchedCount := len(seen)
	if modified, err := maybePatchOpenCodeBaseURL("", active); err != nil {
		logger.Printf("[autoconf] WARNING: failed to patch opencode.json: %v", err)
		// Continue anyway — routing still works via in-memory map
	} else if modified {
		logger.Printf("[autoconf] patched opencode.json: set baseURL for %d providers", patchedCount)
	}

	return buildAutoProviderTargets(active)
}

// BuildModelProviderMap returns a map of lowercase bare modelID → providerID,
// for resolving bare model names (e.g. "glm-5.2") to "providerID/modelID" strings
// (e.g. "zai-coding-plan/glm-5.2"). Used by the daemon when spawning agents to avoid
// hardcoding provider prefixes.
//
// All discovered providers are included. If multiple providers carry the same
// modelID, the alphabetically-first ProviderID wins (deterministic across
// restarts). User must resolve ambiguities explicitly via "provider/model".
//
// Returns nil on any non-fatal error (no models.json, no opencode.json, etc.).
// The map is built fresh on each call; callers should cache the result.
func BuildModelProviderMap() map[string]string {
	models, err := loadModelsJSON("")
	if err != nil || models == nil {
		return nil
	}
	oc, err := loadOpenCodeConfig("")
	if err != nil || oc == nil {
		return nil
	}
	auth, _ := loadOpenCodeAuth("")
	active, _ := discoverOpenAICompatibleProviders(models, oc, auth)
	if len(active) == 0 {
		return nil
	}
	return buildModelProviderMap(active)
}

// buildModelProviderMap converts discovered providers to a lowercase
// modelID → providerID map. All providers are included; when multiple
// providers carry the same modelID, the alphabetically-first ProviderID wins
// (deterministic across daemon restarts; discovery order itself is
// non-deterministic because it iterates maps). Users must resolve ambiguous
// models explicitly via "provider/model".
func buildModelProviderMap(providers []autoDiscoveredProvider) map[string]string {
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].ProviderID < providers[j].ProviderID
	})
	m := make(map[string]string, len(providers))
	for _, p := range providers {
		key := strings.ToLower(p.ModelID)
		if _, exists := m[key]; !exists {
			m[key] = p.ProviderID
		}
	}
	return m
}
