package setup

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestAllLiveConfigKeysPresent verifies that every top-level key present in
// the user's live config.yaml has a matching entry (active OR commented) in
// the generated setup template. This protects against drift between what
// the user sees in their config and what setup documents.
func TestAllLiveConfigKeysPresent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires reading ~/.claude/yesmem/config.yaml")
	}
	home, _ := os.UserHomeDir()
	livePath := home + "/.claude/yesmem/config.yaml"
	liveData, err := os.ReadFile(livePath)
	if err != nil {
		t.Skipf("live config not readable: %v", err)
	}

	// Extract all top-level keys (column 0, optional '# ', ends with ':')
	// Must be at start of line with no leading whitespace.
	keyRe := regexp.MustCompile(`(?m)^#?\s*([a-z_]+):`)
	liveKeys := map[string]bool{}
	for _, m := range keyRe.FindAllStringSubmatch(string(liveData), -1) {
		// Filter to top-level only: comment char or letter at column 0
		line := strings.Split(string(liveData)[strings.Index(string(liveData), m[0]):], "\n")[0]
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, m[1]+":") {
			continue
		}
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "#\t") {
			// Commented with leading space — might be deeper. Check column.
			continue
		}
		liveKeys[m[1]] = true
	}

	// Generate template for each path
	paths := []struct {
		name, model, apiKey, openaiKey, openaiBaseURL, provider, narrativeModel string
	}{
		{"opencode", "deepseek-v4-flash", "sk-ant", "sk-ds", "https://api.deepseek.com", "openai_compatible", "deepseek-v4-pro"},
		{"cli", "sonnet", "sk-ant-key", "", "", "cli", "opus-4-7"},
		{"codex", "gpt-5.5-codex", "", "sk-openai", "https://api.openai.com/v1", "openai_compatible", "gpt-5.5-codex"},
	}

	missingAcrossPaths := map[string]bool{}
	for _, p := range paths {
		cfg := generateConfig(p.model, true, p.apiKey, p.openaiKey, p.openaiBaseURL, p.provider, "ghostty", p.narrativeModel, p.narrativeModel, p.model)
		templateKeys := map[string]bool{}
		for _, m := range keyRe.FindAllStringSubmatch(cfg, -1) {
			templateKeys[m[1]] = true
		}

		var missing []string
		for k := range liveKeys {
			if !templateKeys[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("path %s: template missing keys present in live config: %s", p.name, strings.Join(missing, ", "))
			for _, m := range missing {
				missingAcrossPaths[m] = true
			}
		}
	}

	if len(missingAcrossPaths) > 0 {
		var allMissing []string
		for k := range missingAcrossPaths {
			allMissing = append(allMissing, k)
		}
		sort.Strings(allMissing)
		t.Logf("keys missing across all paths: %s", strings.Join(allMissing, ", "))
	}
}
