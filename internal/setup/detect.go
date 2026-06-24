package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// detectAgents returns which LLM agents are configured in the user's home.
// Checks: Claude Code, Codex, OpenCode.
// Returns sorted with "opencode" first when present.
func detectAgents(home string) []string {
	var agents []string
	if hasOpenCodeConfig(home) {
		agents = append(agents, "opencode")
	}
	if hasClaudeCodeConfig(home) {
		agents = append(agents, "claude")
	}
	if hasCodexConfig(home) {
		agents = append(agents, "codex")
	}
	// Sort: opencode first, then alphabetical
	sort.Slice(agents, func(i, j int) bool {
		if agents[i] == "opencode" {
			return true
		}
		if agents[j] == "opencode" {
			return false
		}
		return agents[i] < agents[j]
	})
	return agents
}

// hasOpenCodeConfig returns true if OpenCode is installed.
// opencode.json is the required marker; auth.json and models.json are optional
// (auth.json only exists for paid providers, models.json is auto-downloaded on first run).
func hasOpenCodeConfig(home string) bool {
	ocPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(ocPath); err != nil {
		return false
	}
	return true
}

// hasClaudeCodeConfig returns true if Claude Code appears to be configured.
// Checks for ~/.claude.json with a primaryApiKey or ~/.claude/config.json.
func hasClaudeCodeConfig(home string) bool {
	// Check ~/.claude.json first
	claudeJSON := filepath.Join(home, ".claude.json")
	if data, err := os.ReadFile(claudeJSON); err == nil {
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err == nil {
			if k, ok := cfg["primaryApiKey"].(string); ok && k != "" {
				return true
			}
		}
	}
	// Check ~/.claude/config.json
	claudeDir := filepath.Join(home, ".claude")
	claudeConfig := filepath.Join(claudeDir, "config.json")
	if _, err := os.Stat(claudeConfig); err == nil {
		return true
	}
	return false
}

// hasCodexConfig returns true if Codex appears to be configured.
// Checks for ~/.config/codex/config.json or ~/.codex/config.json.
func hasCodexConfig(home string) bool {
	candidates := []string{
		filepath.Join(home, ".config", "codex", "config.json"),
		filepath.Join(home, ".codex", "config.json"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
