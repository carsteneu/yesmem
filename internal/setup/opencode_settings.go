package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	opencodepkg "github.com/carsteneu/yesmem/internal/opencode"
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
			"anthropic": map[string]any{
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
				},
			},
			"opencode": map[string]any{
				"npm": "@ai-sdk/openai-compatible",
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
					"headers": map[string]any{
						"x-yesmem-allow-mcp": "1",
					},
				},
				"models": map[string]any{
					"big-pickle": map[string]any{
						"name":  "Big Pickle",
						"limit": map[string]any{"context": 200000, "output": 8192},
					},
				},
			},
		},
		"mcp": map[string]any{
			"yesmem": map[string]any{
				"type":    "local",
				"command": []any{"yesmem", "mcp"},
				"enabled": true,
				"timeout": 60000,
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

// opencodeConfigPath returns the path to the opencode config file opencode
// actually reads. Mirrors opencode's own globalConfigFile() priority
// (opencode.jsonc > opencode.json > config.json); falls back to opencode.jsonc
// when none exist.
//
// Delegates to the shared internal/opencode package so internal/proxy can
// use the same resolution without importing internal/setup (which would
// create a cycle via daemon/httpapi).
func opencodeConfigPath(home string) string {
	return opencodepkg.ConfigPath(home)
}

// migrateOpencodeJsonToJsonc copies yesmem-managed content from an existing
// opencode.json to opencode.jsonc when the jsonc file is empty or contains
// only $schema. The user's manual jsonc edits are preserved via deepMergeJSON
// (existing keys in jsonc win; only missing yesmem-managed keys are filled in
// from opencode.json). Idempotent: no-op if jsonc already has yesmem provider
// blocks or MCP config.
//
// Triggered once during yesmem setup when:
//   - opencode.json exists with yesmem-managed providers (deepseek/openai/anthropic/opencode), and
//   - opencode.jsonc is missing OR contains only $schema.
//
// After migration, opencode.json is left in place as a backup (not deleted).
// yesmem writes future config updates to opencode.jsonc via opencodeConfigPath.
func migrateOpencodeJsonToJsonc(home string) error {
	dir := filepath.Join(home, ".config", "opencode")
	jsonPath := filepath.Join(dir, "opencode.json")
	jsoncPath := filepath.Join(dir, "opencode.jsonc")

	jsonData, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read opencode.json: %w", err)
	}
	var jsonCfg map[string]any
	if err := json.Unmarshal(jsonData, &jsonCfg); err != nil {
		return nil
	}
	if !hasYesmemManagedContent(jsonCfg) {
		return nil
	}

	jsoncData, err := os.ReadFile(jsoncPath)
	if err != nil && !os.IsNotExist(err) {
		return nil
	}
	var jsoncCfg map[string]any
	if len(jsoncData) > 0 {
		if uerr := json.Unmarshal(jsoncData, &jsoncCfg); uerr != nil {
			return nil
		}
	}
	if hasYesmemManagedContent(jsoncCfg) {
		return nil
	}
	if hasUserContentBeyondSchema(jsoncCfg) {
		return nil
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	target := jsoncCfg
	if target == nil {
		target = map[string]any{}
	}
	if _, ok := target["$schema"]; !ok {
		if schema, ok := jsonCfg["$schema"]; ok {
			target["$schema"] = schema
		} else {
			target["$schema"] = "https://opencode.ai/config.json"
		}
	}

	yesmemContent := extractYesmemManaged(jsonCfg)
	deepMergeJSON(target, yesmemContent)

	out, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(jsoncPath, append(out, '\n'), 0644)
}

// hasYesmemManagedContent returns true if cfg contains any provider, MCP, or
// compaction block managed by yesmem (deepseek/openai/anthropic/opencode
// providers, mcp.yesmem, or compaction.auto/prune).
func hasYesmemManagedContent(cfg map[string]any) bool {
	if cfg == nil {
		return false
	}
	if prov, ok := cfg["provider"].(map[string]any); ok {
		for _, name := range []string{"deepseek", "openai", "anthropic", "opencode"} {
			if _, ok := prov[name]; ok {
				return true
			}
		}
	}
	if mcp, ok := cfg["mcp"].(map[string]any); ok {
		if _, ok := mcp["yesmem"]; ok {
			return true
		}
	}
	if cmp, ok := cfg["compaction"].(map[string]any); ok {
		if _, ok := cmp["auto"]; ok {
			return true
		}
	}
	return false
}

// extractYesmemManaged returns a new map containing only the yesmem-managed
// keys from src (provider subset, mcp.yesmem, compaction.auto/prune, model,
// small_model, plugin). Used by migration to copy yesmem defaults into jsonc
// without dragging along unrelated user content from opencode.json.
func extractYesmemManaged(src map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := src["model"]; ok {
		out["model"] = v
	}
	if v, ok := src["small_model"]; ok {
		out["small_model"] = v
	}
	if prov, ok := src["provider"].(map[string]any); ok {
		yesmemProv := map[string]any{}
		for _, name := range []string{"deepseek", "openai", "anthropic", "opencode"} {
			if v, ok := prov[name]; ok {
				yesmemProv[name] = v
			}
		}
		if len(yesmemProv) > 0 {
			out["provider"] = yesmemProv
		}
	}
	if mcp, ok := src["mcp"].(map[string]any); ok {
		if ymcp, ok := mcp["yesmem"]; ok {
			out["mcp"] = map[string]any{"yesmem": ymcp}
		}
	}
	if cmp, ok := src["compaction"].(map[string]any); ok {
		yesmemCmp := map[string]any{}
		if v, ok := cmp["auto"]; ok {
			yesmemCmp["auto"] = v
		}
		if v, ok := cmp["prune"]; ok {
			yesmemCmp["prune"] = v
		}
		if len(yesmemCmp) > 0 {
			out["compaction"] = yesmemCmp
		}
	}
	if plugins, ok := src["plugin"].([]any); ok && len(plugins) > 0 {
		out["plugin"] = plugins
	}
	return out
}

// hasUserContentBeyondSchema returns true if cfg has any key other than
// $schema. Used to gate migration: if the user has manually edited jsonc
// beyond the fresh-install $schema stub, we don't risk clobbering.
func hasUserContentBeyondSchema(cfg map[string]any) bool {
	if cfg == nil {
		return false
	}
	for k := range cfg {
		if k != "$schema" {
			return true
		}
	}
	return false
}

func mergeOpencodeSettings(home string) error {
	return mergeOpencodeSettingsWith(home, "", "", "")
}

func mergeOpencodeSettingsWith(home, model, smallModel, binaryPath string) error {
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
	// Use full binary path for the MCP command so opencode finds yesmem
	// even when ~/.local/bin is not yet in the PATH.
	if binaryPath != "" {
		if mcp, ok := defaults["mcp"].(map[string]any); ok {
			if yesmem, ok := mcp["yesmem"].(map[string]any); ok {
				yesmem["command"] = []any{binaryPath, "mcp"}
			}
		}
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
		yesmem["timeout"] = float64(60000)
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
	delete(provider, "anthropic")
	delete(provider, "opencode")
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
