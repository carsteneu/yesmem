package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func defaultOpencodeSettings() map[string]any {
	return map[string]any{
		"model":       "deepseek/deepseek-reasoner",
		"small_model": "deepseek/deepseek-chat",
		"provider": map[string]any{
			"deepseek": map[string]any{
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
				},
				"models": map[string]any{
					"deepseek-chat": map[string]any{
						"name":  "DeepSeek V4 Flash",
						"limit": map[string]any{"context": 1000000, "output": 8192},
					},
					"deepseek-reasoner": map[string]any{
						"name":  "DeepSeek V4 Pro",
						"limit": map[string]any{"context": 1000000, "output": 65536},
						"interleaved": map[string]any{
							"field": "reasoning_content",
						},
					},
				},
			},
			"openai": map[string]any{
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
				},
				"models": map[string]any{
					"gpt-5.5": map[string]any{
						"name":  "GPT-5.5",
						"limit": map[string]any{"context": 400000, "output": 128000},
					},
				},
			},
		},
		"mcp": map[string]any{
			"yesmem": map[string]any{
				"type":    "local",
				"command": []any{"yesmem", "mcp"},
				"enabled": true,
				"timeout": 30000,
				"environment": map[string]any{
					"YESMEM_SOURCE_AGENT": "opencode",
				},
			},
		},
		"compaction": map[string]any{
			"auto":  false,
			"prune": false,
		},
	}
}

func opencodeConfigPath(home string) string {
	return filepath.Join(home, ".config", "opencode", "opencode.json")
}

func mergeOpencodeSettings(home string) error {
	return mergeOpencodeSettingsWith(home, "", "")
}

func mergeOpencodeSettingsWith(home, model, smallModel string) error {
	cfgPath := opencodeConfigPath(home)

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var cfg map[string]any
	data, err := os.ReadFile(cfgPath)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			cfg = nil
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	defaults := defaultOpencodeSettings()
	if model != "" {
		defaults["model"] = model
	}
	if smallModel != "" {
		defaults["small_model"] = smallModel
	}
	deepMergeJSON(cfg, defaults)

	upgradeOpencodeTimeout(cfg)

	cfg["$schema"] = "https://opencode.ai/config.json"

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, append(out, '\n'), 0644)
}

func upgradeOpencodeTimeout(cfg map[string]any) {
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		return
	}
	yesmem, ok := mcp["yesmem"].(map[string]any)
	if !ok {
		return
	}
	timeout, ok := yesmem["timeout"].(float64)
	if ok && timeout == 10000 {
		yesmem["timeout"] = float64(30000)
	}
}

func removeOpencodeSettings(home string) error {
	cfgPath := opencodeConfigPath(home)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}

	removeOpencodeProviders(cfg)
	removeOpencodeMCP(cfg)
	removeOpencodeCompaction(cfg)
	removeOpencodePluginEntry(cfg)

	if len(cfg) == 0 || (len(cfg) == 1 && cfg["$schema"] != nil) {
		if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, append(out, '\n'), 0644)
}

func removeOpencodeProviders(cfg map[string]any) {
	provider, ok := cfg["provider"].(map[string]any)
	if !ok {
		return
	}
	delete(provider, "deepseek")
	delete(provider, "openai")
	if len(provider) == 0 {
		delete(cfg, "provider")
	}
}

func removeOpencodeMCP(cfg map[string]any) {
	mcp, ok := cfg["mcp"].(map[string]any)
	if !ok {
		return
	}
	delete(mcp, "yesmem")
	if len(mcp) == 0 {
		delete(cfg, "mcp")
	}
}

func removeOpencodeCompaction(cfg map[string]any) {
	compaction, ok := cfg["compaction"].(map[string]any)
	if !ok {
		return
	}
	delete(compaction, "auto")
	delete(compaction, "prune")
	if len(compaction) == 0 {
		delete(cfg, "compaction")
	}
}

func removeOpencodePlugin(home string) error {
	pluginsDir := filepath.Join(home, ".config", "opencode", "plugins")
	os.RemoveAll(pluginsDir)

	pluginSourceDir := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem")
	os.RemoveAll(pluginSourceDir)

	return removeOpencodeSettings(home)
}

func removeOpencodePluginEntry(cfg map[string]any) {
	plugins, ok := cfg["plugin"].([]any)
	if !ok || len(plugins) == 0 {
		return
	}
	home, _ := os.UserHomeDir()
	yesmemPlugin := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem", "index.ts")

	var filtered []any
	for _, p := range plugins {
		if s, ok := p.(string); ok {
			if s == yesmemPlugin || filepath.Base(s) == "index.ts" && contains(s, "yesmem") {
				continue
			}
		}
		filtered = append(filtered, p)
	}
	if len(filtered) == 0 {
		delete(cfg, "plugin")
	} else {
		cfg["plugin"] = filtered
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type opencodeConfigState struct {
	ConfigPresent        bool
	PluginConfigured     bool
	ProviderConfigured   bool
	MCPConfigured        bool
	CompactionConfigured bool
}

func readOpencodeConfigState(home string) opencodeConfigState {
	state := opencodeConfigState{}

	data, err := os.ReadFile(opencodeConfigPath(home))
	if err != nil {
		return state
	}

	state.ConfigPresent = true

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return state
	}

	plugins, ok := cfg["plugin"].([]any)
	if ok {
		for _, p := range plugins {
			if s, ok := p.(string); ok && contains(s, "yesmem") {
				state.PluginConfigured = true
				break
			}
		}
	}

	if provider, ok := cfg["provider"].(map[string]any); ok {
		if provider["deepseek"] != nil || provider["openai"] != nil {
			state.ProviderConfigured = true
		}
	}

	if mcp, ok := cfg["mcp"].(map[string]any); ok {
		if mcp["yesmem"] != nil {
			state.MCPConfigured = true
		}
	}

	if compaction, ok := cfg["compaction"].(map[string]any); ok {
		if auto, ok := compaction["auto"].(bool); ok && !auto {
			state.CompactionConfigured = true
		}
	}

	return state
}
