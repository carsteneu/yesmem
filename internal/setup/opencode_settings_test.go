package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultOpencodeSettings_HasRequiredKeys(t *testing.T) {
	s := defaultOpencodeSettings()

	if s["provider"] == nil {
		t.Error("missing provider")
	}
	if s["mcp"] == nil {
		t.Error("missing mcp")
	}
	if s["compaction"] == nil {
		t.Error("missing compaction")
	}

	provider := s["provider"].(map[string]any)
	if provider["deepseek"] == nil {
		t.Error("missing provider.deepseek")
	}
	if provider["openai"] == nil {
		t.Error("missing provider.openai")
	}

	mcp := s["mcp"].(map[string]any)
	yesmem, ok := mcp["yesmem"].(map[string]any)
	if !ok {
		t.Fatal("missing mcp.yesmem")
	}
	if yesmem["type"] != "local" {
		t.Errorf("wrong mcp type: %v", yesmem["type"])
	}
	cmd, ok := yesmem["command"].([]any)
	if !ok || len(cmd) != 2 || cmd[0] != "yesmem" || cmd[1] != "mcp" {
		t.Errorf("wrong mcp command: %v", yesmem["command"])
	}
	env, ok := yesmem["environment"].(map[string]any)
	if !ok || env["YESMEM_SOURCE_AGENT"] != "opencode" {
		t.Errorf("mcp.yesmem.environment.YESMEM_SOURCE_AGENT should be opencode, got %v", yesmem["environment"])
	}
		if yesmem["timeout"] != 30000 {
		t.Errorf("wrong mcp timeout: got %v, want 30000", yesmem["timeout"])
	}

	compaction := s["compaction"].(map[string]any)
	if compaction["auto"] != false {
		t.Errorf("compaction.auto should be false: %v", compaction["auto"])
	}
	if compaction["prune"] != false {
		t.Errorf("compaction.prune should be false: %v", compaction["prune"])
	}
}

func TestRemoveOpencodeProviders(t *testing.T) {
	cfg := map[string]any{
		"provider": map[string]any{
			"deepseek": map[string]any{"options": map[string]any{"baseURL": "x"}},
			"openai":   map[string]any{"options": map[string]any{"baseURL": "x"}},
			"custom":   map[string]any{"options": map[string]any{"baseURL": "y"}},
		},
	}

	removeOpencodeProviders(cfg)

	provider := cfg["provider"].(map[string]any)
	if provider["deepseek"] != nil {
		t.Error("deepseek provider not removed")
	}
	if provider["openai"] != nil {
		t.Error("openai provider not removed")
	}
	if provider["custom"] == nil {
		t.Error("custom provider was incorrectly removed")
	}
}

func TestRemoveOpencodeProviders_AllYesMem(t *testing.T) {
	cfg := map[string]any{
		"provider": map[string]any{
			"deepseek": map[string]any{"options": nil},
			"openai":   map[string]any{"options": nil},
		},
	}

	removeOpencodeProviders(cfg)

	if cfg["provider"] != nil {
		t.Error("provider key should be removed when empty")
	}
}

func TestRemoveOpencodeMCP(t *testing.T) {
	cfg := map[string]any{
		"mcp": map[string]any{
			"yesmem": map[string]any{"type": "local"},
			"custom": map[string]any{"type": "remote"},
		},
	}

	removeOpencodeMCP(cfg)

	mcp := cfg["mcp"].(map[string]any)
	if mcp["yesmem"] != nil {
		t.Error("yesmem MCP not removed")
	}
	if mcp["custom"] == nil {
		t.Error("custom MCP was incorrectly removed")
	}
}

func TestRemoveOpencodeMCP_OnlyYesMem(t *testing.T) {
	cfg := map[string]any{
		"mcp": map[string]any{
			"yesmem": map[string]any{"type": "local"},
		},
	}

	removeOpencodeMCP(cfg)

	if cfg["mcp"] != nil {
		t.Error("mcp key should be removed when empty")
	}
}

func TestRemoveOpencodeCompaction(t *testing.T) {
	cfg := map[string]any{
		"compaction": map[string]any{
			"auto":      false,
			"threshold": 100000,
		},
	}

	removeOpencodeCompaction(cfg)

	compaction := cfg["compaction"].(map[string]any)
	if compaction["auto"] != nil {
		t.Error("compaction.auto not removed")
	}
	if compaction["threshold"] != 100000 {
		t.Error("compaction.threshold was incorrectly removed")
	}
}

func TestRemoveOpencodePluginEntry_YesMemPluginRemoved(t *testing.T) {
	cfg := map[string]any{
		"plugin": []any{"/home/testuser/.local/share/yesmem/plugins/opencode-yesmem/index.ts", "/other/plugin.ts"},
	}

	removeOpencodePluginEntryWithHome(cfg, "/home/testuser")

	plugins, ok := cfg["plugin"].([]any)
	if !ok || len(plugins) != 1 || plugins[0] != "/other/plugin.ts" {
		t.Errorf("yesmem plugin not removed: %v", cfg["plugin"])
	}
}

func TestRemoveOpencodePluginEntry_OnlyYesMem(t *testing.T) {
	cfg := map[string]any{
		"plugin": []any{"/home/testuser/.local/share/yesmem/plugins/opencode-yesmem/index.ts"},
	}

	removeOpencodePluginEntryWithHome(cfg, "/home/testuser")

	if cfg["plugin"] != nil {
		t.Error("plugin key should be removed when empty")
	}
}

func TestDefaultOpencodeSettings_MergePreservesExisting(t *testing.T) {
	original := `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "deepseek": {
      "options": {
        "baseURL": "https://custom.example.com/v1",
        "apiKey": "sk-custom-deepseek-key"
      }
    }
  },
  "mcp": {
    "custom_mcp": {
      "command": ["custom", "mcp"],
      "enabled": true
    }
  }
}`

	var cfg map[string]any
	if err := json.Unmarshal([]byte(original), &cfg); err != nil {
		t.Fatal(err)
	}

	defaults := defaultOpencodeSettings()
	deepMergeJSON(cfg, defaults)

	// deepseek baseURL and apiKey must NOT be overwritten
	ds := cfg["provider"].(map[string]any)["deepseek"].(map[string]any)
	dsOpts := ds["options"].(map[string]any)
	if dsOpts["baseURL"] != "https://custom.example.com/v1" {
		t.Errorf("existing baseURL was overwritten: %v", dsOpts["baseURL"])
	}
	if dsOpts["apiKey"] != "sk-custom-deepseek-key" {
		t.Errorf("existing apiKey was overwritten: %v", dsOpts["apiKey"])
	}

	// models must have been added
	if ds["models"] == nil {
		t.Error("deepseek.models not added")
	}

	// openai provider must be added
	prov := cfg["provider"].(map[string]any)
	if prov["openai"] == nil {
		t.Error("openai provider not added")
	}

	// yesmem MCP must be added
	mcp := cfg["mcp"].(map[string]any)
	if mcp["yesmem"] == nil {
		t.Error("yesmem MCP not added")
	}
	// custom MCP must be preserved
	if mcp["custom_mcp"] == nil {
		t.Error("custom_mcp was lost")
	}

	// compaction must be added
	if cfg["compaction"] == nil {
		t.Error("compaction not added")
	}
}

func TestOpencodeSettingsEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	configDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(configDir, 0755)

	cfgPath := filepath.Join(configDir, "opencode.json")

	existingConfig := `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "deepseek": {
      "options": {
        "baseURL": "https://my-proxy.com/v1"
      }
    }
  }
}`
	if err := os.WriteFile(cfgPath, []byte(existingConfig), 0644); err != nil {
		t.Fatal(err)
	}

	if err := mergeOpencodeSettings(home); err != nil {
		t.Fatalf("mergeOpencodeSettings: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}

	// Custom baseURL preserved
	dsOpts := cfg["provider"].(map[string]any)["deepseek"].(map[string]any)["options"].(map[string]any)
	if dsOpts["baseURL"] != "https://my-proxy.com/v1" {
		t.Errorf("custom baseURL was overwritten: %v", dsOpts["baseURL"])
	}

	// Models added
	ds := cfg["provider"].(map[string]any)["deepseek"].(map[string]any)
	if ds["models"] == nil {
		t.Error("models not added")
	}

	// openai provider added
	if cfg["provider"].(map[string]any)["openai"] == nil {
		t.Error("openai provider not added")
	}

	// yesmem MCP added
	if cfg["mcp"].(map[string]any)["yesmem"] == nil {
		t.Error("yesmem MCP not added")
	}

	// compaction added
	if cfg["compaction"] == nil {
		t.Error("compaction not added")
	}

	// Schema preserved
	if cfg["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("schema lost: %v", cfg["$schema"])
	}
}

func TestOpencodeSettingsRemoveEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	configDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(configDir, 0755)

	cfgPath := filepath.Join(configDir, "opencode.json")

	config := `{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["/other/plugin.ts"],
  "provider": {
    "deepseek": {
      "options": {
        "baseURL": "http://localhost:9099/v1"
      },
      "models": {
        "deepseek-chat": {"name": "DeepSeek V4 Flash"}
      }
    },
    "openai": {
      "options": {
        "baseURL": "http://localhost:9099/v1"
      }
    },
    "custom_provider": {
      "options": {"baseURL": "https://custom.example.com"}
    }
  },
  "mcp": {
    "yesmem": {
      "type": "local",
      "command": ["yesmem", "mcp"],
      "enabled": true
    },
    "custom_mcp": {
      "command": ["custom"],
      "enabled": true
    }
  },
  "compaction": {
    "auto": false,
    "custom_setting": "keep"
  }
}`
	if err := os.WriteFile(cfgPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	if err := removeOpencodeSettings(home); err != nil {
		t.Fatalf("removeOpencodeSettings: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "yesmem") {
		t.Errorf("yesmem entries not fully removed:\n%s", string(data))
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}

	// provider.deepseek and provider.openai should be removed
	prov := cfg["provider"].(map[string]any)
	if prov["deepseek"] != nil {
		t.Error("deepseek provider not removed")
	}
	if prov["openai"] != nil {
		t.Error("openai provider not removed")
	}
	// custom_provider should remain
	if prov["custom_provider"] == nil {
		t.Error("custom_provider was incorrectly removed")
	}

	// mcp.yesmem should be removed
	mcp := cfg["mcp"].(map[string]any)
	if mcp["yesmem"] != nil {
		t.Error("yesmem MCP not removed")
	}
	if mcp["custom_mcp"] == nil {
		t.Error("custom_mcp was incorrectly removed")
	}

	// compaction.auto and compaction.prune should be removed
	compaction := cfg["compaction"].(map[string]any)
	if compaction["auto"] != nil {
		t.Error("compaction.auto not removed")
	}
	if compaction["prune"] != nil {
		t.Error("compaction.prune not removed")
	}
	if compaction["custom_setting"] != "keep" {
		t.Error("compaction.custom_setting was incorrectly removed")
	}
}

func TestOpencodeSettingsRemove_EmptyConfigDeleted(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	configDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(configDir, 0755)

	cfgPath := filepath.Join(configDir, "opencode.json")

	config := `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "deepseek": {"options": {"baseURL": "http://localhost:9099/v1"}},
    "openai": {"options": {"baseURL": "http://localhost:9099/v1"}}
  },
  "mcp": {
    "yesmem": {"type": "local", "command": ["yesmem", "mcp"]}
  },
  "compaction": {
    "auto": false
  }
}`
	if err := os.WriteFile(cfgPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	if err := removeOpencodeSettings(home); err != nil {
		t.Fatalf("removeOpencodeSettings: %v", err)
	}

	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Error("config file should be deleted when only yesmem entries + schema remain")
	}
}

func TestOpencodeSettingsRemove_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")

	if err := removeOpencodeSettings(home); err != nil {
		t.Fatalf("removeOpencodeSettings should not error on missing file: %v", err)
	}
}

func removeOpencodePluginEntryWithHome(cfg map[string]any, home string) {
	plugins, ok := cfg["plugin"].([]any)
	if !ok || len(plugins) == 0 {
		return
	}
	yesmemPlugin := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem", "index.ts")

	var filtered []any
	for _, p := range plugins {
		if s, ok := p.(string); ok {
			if s == yesmemPlugin || (filepath.Base(s) == "index.ts" && searchString(s, "yesmem")) {
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

func TestUpgradeOpencodeTimeout_OldDefault(t *testing.T) {
	cfg := map[string]any{
		"mcp": map[string]any{
			"yesmem": map[string]any{
				"timeout": float64(10000),
			},
		},
	}

	upgradeOpencodeTimeout(cfg)

	yesmem := cfg["mcp"].(map[string]any)["yesmem"].(map[string]any)
	if yesmem["timeout"] != float64(30000) {
		t.Errorf("old timeout not upgraded: got %v, want 30000", yesmem["timeout"])
	}
}

func TestUpgradeOpencodeTimeout_CustomPreserved(t *testing.T) {
	cfg := map[string]any{
		"mcp": map[string]any{
			"yesmem": map[string]any{
				"timeout": float64(60000),
			},
		},
	}

	upgradeOpencodeTimeout(cfg)

	yesmem := cfg["mcp"].(map[string]any)["yesmem"].(map[string]any)
	if yesmem["timeout"] != float64(60000) {
		t.Errorf("custom timeout was changed: got %v, want 60000", yesmem["timeout"])
	}
}

func TestUpgradeOpencodeTimeout_NoMCP(t *testing.T) {
	cfg := map[string]any{}
	upgradeOpencodeTimeout(cfg) // must not panic
}

func TestMergeOpencodeSettings_UpgradesOldTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	configDir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(configDir, 0755)

	cfgPath := filepath.Join(configDir, "opencode.json")

	existingConfig := `{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "yesmem": {
      "type": "local",
      "command": ["yesmem", "mcp"],
      "enabled": true,
      "timeout": 10000
    }
  }
}`
	if err := os.WriteFile(cfgPath, []byte(existingConfig), 0644); err != nil {
		t.Fatal(err)
	}

	if err := mergeOpencodeSettings(home); err != nil {
		t.Fatalf("mergeOpencodeSettings: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}

	yesmem := cfg["mcp"].(map[string]any)["yesmem"].(map[string]any)
	if yesmem["timeout"] != float64(30000) {
		t.Errorf("old timeout not upgraded in merge: got %v, want 30000", yesmem["timeout"])
	}
}
