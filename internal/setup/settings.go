package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readSettingsJSON reads ~/.claude/settings.json as a map.
func readSettingsJSON(home string) (map[string]any, error) {
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}
	return settings, nil
}

// writeSettingsJSON writes the settings map back to ~/.claude/settings.json.
func writeSettingsJSON(home string, settings map[string]any) error {
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// savePreInstallState persists settings values that install will overwrite,
// so uninstall can restore them. Saved to dataDir/install-state.json.
func savePreInstallState(dataDir string, settings map[string]any, home string) {
	state := map[string]any{}
	if val, ok := settings["autoCompactEnabled"]; ok {
		state["autoCompactEnabled"] = val
	} else {
		state["autoCompactEnabled"] = nil // was absent → remove on uninstall
	}
	// Preserve existing ANTHROPIC_API_KEY from env block
	if env, ok := settings["env"].(map[string]any); ok {
		if val, ok := env["ANTHROPIC_API_KEY"]; ok {
			state["envAPIKey"] = val
		} else {
			state["envAPIKey"] = nil
		}
	} else {
		state["envAPIKey"] = nil
	}
	// Preserve existing primaryApiKey from ~/.claude.json
	if home != "" {
		if data, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
			var cfg map[string]any
			if json.Unmarshal(data, &cfg) == nil {
				if k, ok := cfg["primaryApiKey"].(string); ok {
					state["primaryApiKey"] = k
				} else {
					state["primaryApiKey"] = nil
				}
				if oa, ok := cfg["oauthAccount"]; ok {
					state["oauthAccount"] = oa
				}
			}
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(dataDir, "install-state.json"), data, 0644)
}

// loadPreInstallState reads the saved pre-install state.
func loadPreInstallState(dataDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(dataDir, "install-state.json"))
	if err != nil {
		return nil
	}
	var state map[string]any
	if json.Unmarshal(data, &state) != nil {
		return nil
	}
	return state
}

// setCleanupPeriod sets cleanupPeriodDays in settings.json.
func setCleanupPeriod(settings map[string]any) {
	settings["cleanupPeriodDays"] = 99999
}

// disableAutoCompact disables Claude Code's built-in context compaction.
// Sawtooth cache optimization handles context management via the proxy instead.
func disableAutoCompact(settings map[string]any) {
	settings["autoCompactEnabled"] = false
}

// setProxyEnvVar sets ANTHROPIC_BASE_URL in the settings.json env block.
// This is the official, documented way for Claude Code to read environment variables —
// shell profiles (.bashrc) have an interactive guard and are not sourced by non-interactive contexts.
func setProxyEnvVar(settings map[string]any) {
	env, ok := settings["env"].(map[string]any)
	if !ok {
		env = map[string]any{}
	}
	env["ANTHROPIC_BASE_URL"] = "http://localhost:9099"
	settings["env"] = env
}

// removeProxyEnvVar removes ANTHROPIC_BASE_URL from the settings.json env block.
func removeProxyEnvVar(settings map[string]any) {
	env, ok := settings["env"].(map[string]any)
	if !ok {
		return
	}
	delete(env, "ANTHROPIC_BASE_URL")
	if len(env) == 0 {
		delete(settings, "env")
	}
}

// registerMCPPermissions adds mcp__yesmem__* to permissions.allow so all
// yesmem MCP tools are auto-allowed without permission dialog.
func registerMCPPermissions(settings map[string]any) {
	const rule = "mcp__yesmem__*"

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		perms = map[string]any{}
	}

	allow, ok := perms["allow"].([]any)
	if !ok {
		allow = []any{}
	}

	for _, v := range allow {
		if v == rule {
			return
		}
	}

	allow = append(allow, rule)
	perms["allow"] = allow
	settings["permissions"] = perms
}

// registerMCPServer adds yesmem to mcpServers in settings.json.
func registerMCPInSettings(settings map[string]any, binaryPath string) {
	mcpServers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = map[string]any{}
	}
	mcpServers["yesmem"] = map[string]any{
		"command": binaryPath,
		"args":    []string{"mcp"},
	}
	settings["mcpServers"] = mcpServers
}

// clearClaudeJSONAuth removes primaryApiKey and oauthAccount from ~/.claude.json
// so that Claude Code uses ANTHROPIC_API_KEY from settings.json env without conflict.
// Skips silently if the file doesn't exist (fresh install without prior Claude Code usage).
func clearClaudeJSONAuth(home string) error {
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse claude.json: %w", err)
	}

	delete(cfg, "primaryApiKey")
	delete(cfg, "oauthAccount")

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0600)
}

// registerMCPInClaudeJSON adds yesmem to ~/.claude.json (user-scope, all projects).
func registerMCPInClaudeJSON(home, binaryPath string) error {
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No claude.json yet — skip
		}
		return err
	}

	// Backup before modifying (only if no backup exists yet)
	backupPath := path + ".bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		if err := os.WriteFile(backupPath, data, 0600); err != nil {
			return fmt.Errorf("backup claude.json: %w", err)
		}
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse claude.json: %w", err)
	}

	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		mcpServers = map[string]any{}
	}

	mcpServers["yesmem"] = map[string]any{
		"type":    "stdio",
		"command": binaryPath,
		"args":    []string{"mcp"},
		"env":     map[string]any{},
	}
	config["mcpServers"] = mcpServers

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0600)
}

// registerHooks adds all yesmem hooks (SessionStart briefing, PreToolUse, PostToolUse, etc.).
// Preserves existing hooks — appends yesmem commands without overwriting.
func registerHooks(settings map[string]any, binaryPath string) {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}

	// SessionStart hook — briefing injection
	addSessionStartHook(hooks, binaryPath)

	// Think hook removed — reminder injection now handled by the proxy (think.go).
	// Clean up any existing hook-think entries from previous setups.
	removeThinkHook(hooks)

	// PreToolUse — warn about known gotchas before Bash execution
	addPreToolUseHook(hooks, binaryPath)

	// PostToolUseFailure — unified hook (learn + assist combined)
	addPostToolUseFailureCombinedHook(hooks, binaryPath)

	// PostToolUse — auto-resolve unfinished items on git commit
	addPostToolUseResolveHook(hooks, binaryPath)

	settings["hooks"] = hooks
}

func addSessionStartHook(hooks map[string]any, binaryPath string) {
	existing, ok := hooks["SessionStart"].([]any)
	if !ok {
		existing = []any{}
	}

	// Check if yesmem hook already registered — fix matcher if missing
	for i, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "yesmem") {
					// Ensure matcher is set (fix for hooks registered without matcher)
					if _, hasMatcher := m["matcher"]; !hasMatcher {
						m["matcher"] = "startup|resume|clear|compact"
						existing[i] = m
						hooks["SessionStart"] = existing
					}
					// Update command path if binary moved
					h["command"] = binaryPath + " briefing-hook"
					return
				}
			}
		}
	}

	// Add new SessionStart hook entry
	existing = append(existing, map[string]any{
		"matcher": "startup|resume|clear|compact",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": binaryPath + " briefing-hook",
			},
		},
	})
	hooks["SessionStart"] = existing
}

func addIdleTickHook(hooks map[string]any, binaryPath string) {
	idleCmd := binaryPath + " idle-tick"
	oldCmd := "micro-reminder"

	existing, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		// No existing hook — create one
		hooks["UserPromptSubmit"] = []any{
			map[string]any{
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": idleCmd,
					},
				},
			},
		}
		return
	}

	// Check if already registered (idle-tick) or needs migration (micro-reminder → idle-tick)
	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok {
					if strings.Contains(cmd, "idle-tick") {
						return // Already registered
					}
					// Migrate: replace micro-reminder with idle-tick
					if strings.Contains(cmd, oldCmd) {
						h["command"] = strings.Replace(cmd, binaryPath+" "+oldCmd, idleCmd, 1)
						m["hooks"] = toAnySlice(toHookSlice(m["hooks"]))
						hooks["UserPromptSubmit"] = existing
						return
					}
				}
			}
		}
	}

	// Append to first existing hook's command chain
	if len(existing) > 0 {
		if first, ok := existing[0].(map[string]any); ok {
			hooksList := toHookSlice(first["hooks"])
			if len(hooksList) > 0 {
				if cmd, ok := hooksList[0]["command"].(string); ok {
					hooksList[0]["command"] = cmd + "; " + idleCmd
					first["hooks"] = toAnySlice(hooksList)
					existing[0] = first
					hooks["UserPromptSubmit"] = existing
					return
				}
			}
		}
	}

	// Fallback: add as new entry
	existing = append(existing, map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": idleCmd,
			},
		},
	})
	hooks["UserPromptSubmit"] = existing
}

// removeThinkHook removes the deprecated hook-think entry from UserPromptSubmit.
// Think reminder injection is now handled by the proxy (think.go).
func removeThinkHook(hooks map[string]any) {
	existing, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		return
	}

	var cleaned []any
	for _, entry := range existing {
		m, ok := entry.(map[string]any)
		if !ok {
			cleaned = append(cleaned, entry)
			continue
		}
		isThink := false
		for _, h := range toHookSlice(m["hooks"]) {
			if cmd, ok := h["command"].(string); ok {
				if strings.Contains(cmd, "hook-think") {
					isThink = true
					break
				}
			}
		}
		if !isThink {
			cleaned = append(cleaned, entry)
		}
	}
	hooks["UserPromptSubmit"] = cleaned
}

func addPreToolUseHook(hooks map[string]any, binaryPath string) {
	hookCmd := binaryPath + " hook-check"
	existing, ok := hooks["PreToolUse"].([]any)
	if !ok {
		existing = []any{}
	}

	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "yesmem") {
					return
				}
			}
		}
	}

	existing = append(existing, map[string]any{
		"matcher": "Bash|Edit|Write",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCmd,
			},
		},
	})
	hooks["PreToolUse"] = existing
}

func addPostToolUseFailureHook(hooks map[string]any, binaryPath string) {
	hookCmd := binaryPath + " hook-learn"
	existing, ok := hooks["PostToolUseFailure"].([]any)
	if !ok {
		existing = []any{}
	}

	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "yesmem") {
					return
				}
			}
		}
	}

	existing = append(existing, map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCmd,
			},
		},
	})
	hooks["PostToolUseFailure"] = existing
}

// addPostToolUseFailureCombinedHook registers the unified hook-failure command,
// migrating from the old hook-learn + hook-assist pair.
func addPostToolUseFailureCombinedHook(hooks map[string]any, binaryPath string) {
	hookCmd := binaryPath + " hook-failure"
	existing, ok := hooks["PostToolUseFailure"].([]any)
	if !ok {
		existing = []any{}
	}

	// Check if already registered
	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "hook-failure") {
					return
				}
			}
		}
	}

	// Remove old hook-learn and hook-assist entries, replace with hook-failure
	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			matcher, _ := m["matcher"].(string)
			if matcher == "Bash" || matcher == "Bash|WebFetch|WebSearch" || matcher == "" {
				// Filter out old hooks, add new one
				var kept []map[string]any
				for _, h := range toHookSlice(m["hooks"]) {
					if cmd, ok := h["command"].(string); ok {
						if strings.Contains(cmd, "hook-learn") || strings.Contains(cmd, "hook-assist") {
							continue // Remove old
						}
					}
					kept = append(kept, h)
				}
				kept = append(kept, map[string]any{
					"type":    "command",
					"command": hookCmd,
				})
				m["hooks"] = toAnySlice(kept)
				hooks["PostToolUseFailure"] = existing
				return
			}
		}
	}

	// Fallback: add as new entry
	existing = append(existing, map[string]any{
		"matcher": "Bash|WebFetch|WebSearch",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCmd,
			},
		},
	})
	hooks["PostToolUseFailure"] = existing
}

func addPostToolUseResolveHook(hooks map[string]any, binaryPath string) {
	hookCmd := binaryPath + " hook-resolve"
	existing, ok := hooks["PostToolUse"].([]any)
	if !ok {
		existing = []any{}
	}

	// Check if already registered
	for _, entry := range existing {
		if m, ok := entry.(map[string]any); ok {
			for _, h := range toHookSlice(m["hooks"]) {
				if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "hook-resolve") {
					// Migrate: add if-condition for older installations without it
					if _, hasIf := m["if"]; !hasIf {
						m["if"] = "Bash(git *)"
					}
					return
				}
			}
		}
	}

	existing = append(existing, map[string]any{
		"matcher": "Bash",
		"if":      "Bash(git *)",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCmd,
			},
		},
	})
	hooks["PostToolUse"] = existing
}

func toHookSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var result []map[string]any
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

// EnsureHooks reads settings.json, registers/updates all yesmem hooks, and writes back.
// Safe to call repeatedly — preserves existing hooks, only adds/updates yesmem entries.
func EnsureHooks() error {
	home, _ := os.UserHomeDir()
	binaryPath, _ := os.Executable()

	settings, err := readSettingsJSON(home)
	if err != nil {
		return fmt.Errorf("read settings.json: %w", err)
	}

	registerHooks(settings, binaryPath)
	registerMCPPermissions(settings)
	registerStatusLine(settings, binaryPath)

	if err := writeSettingsJSON(home, settings); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	return nil
}

// registerStatusLine sets the custom statusline command in settings.json.
func registerStatusLine(settings map[string]any, binaryPath string) {
	settings["statusLine"] = map[string]any{
		"type":            "command",
		"command":         binaryPath + " statusline",
		"refreshInterval": 2,
	}
}

func toAnySlice(hooks []map[string]any) []any {
	result := make([]any, len(hooks))
	for i, h := range hooks {
		result[i] = h
	}
	return result
}
