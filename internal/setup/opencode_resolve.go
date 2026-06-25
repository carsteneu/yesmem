package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// modelsEntry represents one provider entry in OpenCode's models.json cache.
// Duplicated from proxy to avoid circular import.
type modelsEntry struct {
	NPM    string                 `json:"npm"`
	API    string                 `json:"api"`
	Models map[string]modelsModel `json:"models"`
}

type modelsModel struct {
	ID string `json:"id"`
}

// filteredProvider represents a provider option shown to the user.
type filteredProvider struct {
	ID      string
	Label   string
	BaseURL string
}

// resolveOpenCodeProvider interactively guides the user through selecting an
// OpenAI-compatible provider from their OpenCode configuration.
// Returns the resolved baseURL, apiKey, extraction model, and narrative model.
func resolveOpenCodeProvider(home string) (baseURL, apiKey, extractionModel, narrativeModel string, err error) {
	// 1. Load models.json
	models, err := loadModels(home)
	if err != nil {
		return "", "", "", "", fmt.Errorf("load models.json: %w", err)
	}

	// 2. Load auth.json (optional — free-tier users have no auth.json)
	auth, err := loadAuth(home)
	if err != nil {
		return "", "", "", "", fmt.Errorf("load auth.json: %w", err)
	}

	// 3. Filter: opencode free-tier provider + openai-compatible providers with API key
	// Free-tier users (no auth.json) see only the 'opencode' provider (opencode.ai/zen).
	// Paid users see 'opencode' + any provider they have a key for.
	var providers []filteredProvider
	for id, entry := range models {
		baseURL := entry.API
		key := auth[id]

		// opencode provider is the free-tier default — always available
		if id == "opencode" {
			if baseURL == "" {
				baseURL = "https://opencode.ai/zen/v1"
			}
			if entry.NPM != "@ai-sdk/openai-compatible" {
				continue
			}
			providers = append(providers, filteredProvider{
				ID:      id,
				Label:   fmt.Sprintf("%s (free tier, %s)", id, baseURL),
				BaseURL: baseURL,
			})
			continue
		}

		// Other providers require an API key
		if key == "" {
			continue
		}
		if entry.NPM != "@ai-sdk/openai-compatible" {
			continue
		}
		if baseURL == "" {
			switch id {
			case "openai":
				baseURL = "https://api.openai.com/v1"
			default:
				continue
			}
		}
		if !strings.HasPrefix(baseURL, "http") {
			continue
		}
		providers = append(providers, filteredProvider{
			ID:      id,
			Label:   fmt.Sprintf("%s (%s)", id, baseURL),
			BaseURL: baseURL,
		})
	}

	if len(providers) == 0 {
		return "", "", "", "", fmt.Errorf("no OpenCode providers found (free tier or configured API keys)")
	}

	// Sort alphabetically
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].ID < providers[j].ID
	})

	// 4. Show provider list
	fmt.Println("  Detected OpenAI-compatible providers:")
	fmt.Println()
	options := make([]string, len(providers))
	for i, p := range providers {
		options[i] = p.Label
	}
	fmt.Println("  Which provider should YesMem use for extraction?")
	idx := promptChoice(options, 0)
	selected := providers[idx]
	fmt.Println()

	// 5. Resolve API key
	apiKey = auth[selected.ID]

	// 6. Get model list for the selected provider
	entry := models[selected.ID]
	modelIDs := make([]string, 0, len(entry.Models))
	for modelID := range entry.Models {
		// For opencode free-tier provider: only show actual free models.
		// models.json lists 71 models under 'opencode' but most are paid models
		// (claude-*, gpt-*, gemini-*) accessible via opencode account — these need
		// OAuth/session, not usable by the daemon's direct HTTP client.
		if selected.ID == "opencode" && !isFreeTierModel(modelID) {
			continue
		}
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)

	if len(modelIDs) == 0 {
		return "", "", "", "", fmt.Errorf("no models found for provider %q", selected.ID)
	}

	// 7. Interactive model selection
	if selected.ID == "deepseek" {
		// DeepSeek: prompt for two models (pro for narrative, flash for extraction)
		fmt.Println("  DeepSeek supports two-tier model selection:")
		fmt.Println("    - A stronger model for narrative/quality (e.g., deepseek-v4-pro)")
		fmt.Println("    - A lighter model for extraction (e.g., deepseek-v4-flash)")
		fmt.Println()

		fmt.Println("  Which model for narrative generation (quality & depth)?")
		narrativeIdx := promptChoice(modelIDs, 0)
		fmt.Println()

		// Remove chosen narrative model from options for extraction
		extractionOpts := make([]string, 0, len(modelIDs))
		for _, m := range modelIDs {
			if m != modelIDs[narrativeIdx] {
				extractionOpts = append(extractionOpts, m)
			}
		}
		if len(extractionOpts) == 0 {
			extractionOpts = modelIDs
		}

		fmt.Println("  Which model for extraction (speed & cost)?")
		extractionIdx := promptChoice(extractionOpts, 0)
		fmt.Println()

		narrativeModel = modelIDs[narrativeIdx]
		extractionModel = extractionOpts[extractionIdx]
	} else {
		// Other providers: one model for everything
		fmt.Println("  Which model should YesMem use?")
		modelIdx := promptChoice(modelIDs, 0)
		fmt.Println()

		modelName := modelIDs[modelIdx]
		narrativeModel = modelName
		extractionModel = modelName
	}

	// ensureV1: normalize URL to end with /v1 so daemon's normalizeOpenAIURL
	// (which appends /chat/completions only when URL ends in /v1) works.
	baseURL = ensureV1(selected.BaseURL)
	return baseURL, apiKey, extractionModel, narrativeModel, nil
}

// ensureV1 appends /v1 to an OpenAI-compatible base URL if not already present.
// DeepSeek's models.json lists "https://api.deepseek.com" (no /v1), but the
// daemon's LLM client needs /v1 to construct the /chat/completions endpoint.
// Idempotent: returns input unchanged if it already ends with /v1.
func ensureV1(url string) string {
	url = strings.TrimRight(url, "/")
	if strings.HasSuffix(url, "/v1") {
		return url
	}
	return url + "/v1"
}

// isFreeTierModel returns true if the model ID is an actual free-tier model
// on opencode.ai/zen (not a paid model rebranded under the opencode provider).
// Free-tier markers: "-free" suffix, or known free model name patterns.
func isFreeTierModel(modelID string) bool {
	id := strings.ToLower(modelID)
	if strings.HasSuffix(id, "-free") {
		return true
	}
	// Known free-tier model name patterns (without -free suffix)
	freePatterns := []string{
		"big-pickle", "ring-", "mimo-", "nemotron-", "hy3-",
		"ling-", "minimax-m", "north-", "trinity-", "kimi-k2.5",
	}
	for _, p := range freePatterns {
		if strings.Contains(id, p) {
			return true
		}
	}
	return false
}

// loadModels reads opencode's models.json cache.
func loadModels(home string) (map[string]modelsEntry, error) {
	path := filepath.Join(home, ".cache", "opencode", "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("models.json not found at %s", path)
		}
		return nil, err
	}
	var entries map[string]modelsEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// loadAuth reads opencode's auth.json and returns providerID → apiKey map.
// Returns empty map (no error) if auth.json doesn't exist — free-tier users
// (opencode.ai/zen) have no auth.json.
func loadAuth(home string) (map[string]string, error) {
	path := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var entries map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for providerID, entry := range entries {
		if entry.Key != "" {
			result[providerID] = entry.Key
		}
	}
	return result, nil
}
