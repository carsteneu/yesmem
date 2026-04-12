package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanSettingsJSON_RemovesMCPPermissions(t *testing.T) {
	// Simulate settings with yesmem MCP tool permissions
	allow := []any{"Bash(git:*)", "WebSearch"}
	for _, name := range mcpToolNames {
		allow = append(allow, "mcp__yesmem__"+name)
	}
	settings := map[string]any{
		"permissions": map[string]any{
			"allow":       allow,
			"defaultMode": "acceptEdits",
		},
	}

	cleanMCPPermissions(settings)

	perms := settings["permissions"].(map[string]any)
	cleaned := perms["allow"].([]any)

	// Only non-yesmem entries should remain
	for _, v := range cleaned {
		s := v.(string)
		if strings.HasPrefix(s, "mcp__yesmem__") {
			t.Errorf("yesmem permission not removed: %s", s)
		}
	}

	// Existing non-yesmem entries preserved
	if len(cleaned) != 2 {
		t.Fatalf("expected 2 non-yesmem entries, got %d: %v", len(cleaned), cleaned)
	}
	if cleaned[0] != "Bash(git:*)" || cleaned[1] != "WebSearch" {
		t.Fatalf("non-yesmem entries not preserved: %v", cleaned)
	}
}

func TestCleanSettingsJSON_RemovesWildcard(t *testing.T) {
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"mcp__yesmem__*", "mcp__yesmem__search", "WebSearch"},
		},
	}

	cleanMCPPermissions(settings)

	cleaned := settings["permissions"].(map[string]any)["allow"].([]any)
	for _, v := range cleaned {
		s := v.(string)
		if strings.HasPrefix(s, "mcp__yesmem__") {
			t.Errorf("yesmem entry not removed: %s", s)
		}
	}
	if len(cleaned) != 1 || cleaned[0] != "WebSearch" {
		t.Fatalf("expected [WebSearch], got %v", cleaned)
	}
}

func TestCleanSettingsJSON_NoPermissions(t *testing.T) {
	settings := map[string]any{}

	// Must not panic
	cleanMCPPermissions(settings)
}

func TestCleanSettingsJSON_AllYesmemPermissions_EmptyArray(t *testing.T) {
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"mcp__yesmem__search", "mcp__yesmem__remember"},
		},
	}

	cleanMCPPermissions(settings)

	perms := settings["permissions"].(map[string]any)
	// Must serialize to [] not null — Claude Code rejects null
	out, _ := json.Marshal(perms["allow"])
	if string(out) != "[]" {
		t.Fatalf("expected empty array [], got %s", string(out))
	}
}

func TestCleanSettingsJSON_RemovesStatusLine(t *testing.T) {
	settings := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": "yesmem statusline",
		},
	}

	cleanStatusLine(settings)

	if _, ok := settings["statusLine"]; ok {
		t.Fatal("statusLine not removed")
	}
}

func TestCleanSettingsJSON_PreservesNonYesmemStatusLine(t *testing.T) {
	settings := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": "some-other-tool statusline",
		},
	}

	cleanStatusLine(settings)

	if _, ok := settings["statusLine"]; !ok {
		t.Fatal("non-yesmem statusLine should be preserved")
	}
}

func TestCleanSettingsJSON_RemovesSessionEndHook(t *testing.T) {
	hooks := map[string]any{
		"SessionEnd": []any{
			map[string]any{
				"matcher": "clear|compact",
				"hooks": []any{
					map[string]any{"type": "command", "command": "/usr/local/bin/yesmem session-end"},
				},
			},
		},
		"SessionStart": []any{
			map[string]any{
				"matcher": "startup|resume|clear|compact",
				"hooks": []any{
					map[string]any{"type": "command", "command": "/usr/local/bin/yesmem briefing-hook"},
				},
			},
		},
	}

	removeYesmemHook(hooks, "SessionEnd")

	if _, ok := hooks["SessionEnd"]; ok {
		t.Fatal("SessionEnd hook not removed")
	}
	// SessionStart untouched
	if _, ok := hooks["SessionStart"]; !ok {
		t.Fatal("SessionStart should not be touched")
	}
}

func TestRestoreAPIKeyFromState_RestoresPreviousKey(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{"envAPIKey": "sk-ant-original"}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "install-state.json"), data, 0644)

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY": "sk-ant-installed-by-yesmem",
		},
	}
	restoreAPIKeyFromState(dir, settings)

	env := settings["env"].(map[string]any)
	if env["ANTHROPIC_API_KEY"] != "sk-ant-original" {
		t.Fatalf("expected original key restored, got %v", env["ANTHROPIC_API_KEY"])
	}
}

func TestRestoreAPIKeyFromState_RemovesWhenNoPrior(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{"envAPIKey": nil}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "install-state.json"), data, 0644)

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY":  "sk-ant-installed",
			"ANTHROPIC_BASE_URL": "http://localhost:9099",
		},
	}
	restoreAPIKeyFromState(dir, settings)

	env := settings["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatal("API key should be removed when no prior key existed")
	}
	if env["ANTHROPIC_BASE_URL"] != "http://localhost:9099" {
		t.Fatal("other env vars should be preserved")
	}
}

func TestRestoreAPIKeyFromState_NoStateFile(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY": "sk-ant-something",
		},
	}
	// Must not panic, removes key as safe default
	restoreAPIKeyFromState(dir, settings)

	env := settings["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatal("API key should be removed when no state file exists")
	}
}

func TestRestoreAPIKeyFromState_CreatesEnvBlockWhenRestoring(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{"envAPIKey": "sk-ant-original"}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "install-state.json"), data, 0644)

	// env block was deleted by removeProxyEnvVar
	settings := map[string]any{}
	restoreAPIKeyFromState(dir, settings)

	env, ok := settings["env"].(map[string]any)
	if !ok {
		t.Fatal("env block should be created when restoring a key")
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-original" {
		t.Fatalf("expected sk-ant-original, got %v", env["ANTHROPIC_API_KEY"])
	}
}

func TestRestoreAPIKeyFromState_NoEnvNoState(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]any{}
	// Must not panic
	restoreAPIKeyFromState(dir, settings)
	if _, ok := settings["env"]; ok {
		t.Fatal("env block should not be created when no state file exists")
	}
}

func TestRestorePrimaryApiKeyFromState_RestoresOAuthAccount(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	// Pre-install state had an OAuth account
	state := map[string]any{
		"primaryApiKey": "sk-original",
		"oauthAccount":  map[string]any{"emailAddress": "test@test.com", "displayName": "Test"},
	}
	stateData, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dataDir, "install-state.json"), stateData, 0600)

	// claude.json currently has our API key, no OAuth
	claudeJSON := `{"primaryApiKey":"sk-yesmem-set","numStartups":5}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0600)

	restorePrimaryApiKeyFromState(dataDir, home)

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if cfg["primaryApiKey"] != "sk-original" {
		t.Fatalf("expected sk-original, got %v", cfg["primaryApiKey"])
	}
	oa, ok := cfg["oauthAccount"].(map[string]any)
	if !ok {
		t.Fatal("oauthAccount not restored")
	}
	if oa["emailAddress"] != "test@test.com" {
		t.Fatalf("expected test@test.com, got %v", oa["emailAddress"])
	}
}

func TestRemoveProxyEnvVar_SetsRealAPIForKeyUsers(t *testing.T) {
	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY":  "sk-ant-key",
			"ANTHROPIC_BASE_URL": "http://localhost:9099",
		},
	}
	removeProxyEnvVar(settings)
	env := settings["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://api.anthropic.com" {
		t.Fatalf("expected https://api.anthropic.com, got %v", env["ANTHROPIC_BASE_URL"])
	}
}

func TestRemoveProxyEnvVar_RemovesForNonKeyUsers(t *testing.T) {
	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "http://localhost:9099",
		},
	}
	removeProxyEnvVar(settings)
	if _, ok := settings["env"]; ok {
		t.Fatal("env block should be removed when no API key and no other vars")
	}
}

func TestUninstallFlow_RestoreKeyThenSetBaseURL(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{"envAPIKey": "sk-ant-original"}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "install-state.json"), data, 0644)

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY":  "sk-ant-installed",
			"ANTHROPIC_BASE_URL": "http://localhost:9099",
		},
	}
	restoreAPIKeyFromState(dir, settings)
	removeProxyEnvVar(settings)

	env := settings["env"].(map[string]any)
	if env["ANTHROPIC_API_KEY"] != "sk-ant-original" {
		t.Fatalf("expected sk-ant-original, got %v", env["ANTHROPIC_API_KEY"])
	}
	if env["ANTHROPIC_BASE_URL"] != "https://api.anthropic.com" {
		t.Fatalf("expected https://api.anthropic.com, got %v", env["ANTHROPIC_BASE_URL"])
	}
}

func TestUninstallFlow_NoKeyRemovesBaseURL(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{"envAPIKey": nil}
	data, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "install-state.json"), data, 0644)

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY":  "sk-ant-installed",
			"ANTHROPIC_BASE_URL": "http://localhost:9099",
		},
	}
	restoreAPIKeyFromState(dir, settings)
	removeProxyEnvVar(settings)

	env, ok := settings["env"].(map[string]any)
	if !ok {
		return // env block removed entirely, fine
	}
	if _, hasKey := env["ANTHROPIC_API_KEY"]; hasKey {
		t.Fatal("API key should be removed")
	}
	if _, hasURL := env["ANTHROPIC_BASE_URL"]; hasURL {
		t.Fatal("BASE_URL should be removed when no API key")
	}
}

func TestCleanProjectLocalSettings_RemovesYesmemPermissions(t *testing.T) {
	home := t.TempDir()

	// Create a project directory with settings.local.json containing yesmem permissions
	projDir := filepath.Join(home, "myproject", ".claude")
	os.MkdirAll(projDir, 0755)
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []any{
				"WebSearch",
				"mcp__yesmem__search",
				"mcp__yesmem__remember",
				"Bash(git *)",
			},
		},
	}
	data, _ := json.Marshal(settings)
	os.WriteFile(filepath.Join(projDir, "settings.local.json"), data, 0644)

	// Create .claude.json with project entry
	claudeJSON := map[string]any{
		"projects": map[string]any{
			filepath.Join(home, "myproject"): map[string]any{
				"hasTrustDialogAccepted": true,
			},
		},
	}
	cdata, _ := json.Marshal(claudeJSON)
	os.WriteFile(filepath.Join(home, ".claude.json"), cdata, 0600)

	cleanProjectLocalSettings(home)

	result, _ := os.ReadFile(filepath.Join(projDir, "settings.local.json"))
	var got map[string]any
	json.Unmarshal(result, &got)
	perms := got["permissions"].(map[string]any)["allow"].([]any)
	for _, p := range perms {
		if strings.Contains(p.(string), "yesmem") {
			t.Fatalf("yesmem permission should be removed: %v", p)
		}
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 remaining permissions, got %d: %v", len(perms), perms)
	}
}

func TestCleanProjectLocalSettings_NoProjectsNoPanic(t *testing.T) {
	home := t.TempDir()
	// No .claude.json — must not panic
	cleanProjectLocalSettings(home)
}

func TestCleanClaudeJSON_RemovesYesmemSkillUsage(t *testing.T) {
	home := t.TempDir()
	claudeJSON := `{"mcpServers":{},"skillUsage":{"yesmem-sessions":{"usageCount":3},"yesmem-recall":{"usageCount":1},"commit":{"usageCount":5}},"numStartups":10}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0600)

	cleanClaudeJSON(home)

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	su, ok := cfg["skillUsage"].(map[string]any)
	if !ok {
		t.Fatal("skillUsage should still exist (has non-yesmem entries)")
	}
	if _, has := su["yesmem-sessions"]; has {
		t.Fatal("yesmem-sessions should be removed")
	}
	if _, has := su["yesmem-recall"]; has {
		t.Fatal("yesmem-recall should be removed")
	}
	if _, has := su["commit"]; !has {
		t.Fatal("non-yesmem skill should be preserved")
	}
	if cfg["numStartups"] != float64(10) {
		t.Fatal("other fields should be preserved")
	}
}

func TestCleanClaudeJSON_RemovesEmptySkillUsage(t *testing.T) {
	home := t.TempDir()
	claudeJSON := `{"skillUsage":{"yesmem-sessions":{"usageCount":3}},"numStartups":5}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0600)

	cleanClaudeJSON(home)

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if _, ok := cfg["skillUsage"]; ok {
		t.Fatal("skillUsage should be removed when empty after cleanup")
	}
}

func TestCleanClaudeJSON_NoSkillUsageNoPanic(t *testing.T) {
	home := t.TempDir()
	claudeJSON := `{"numStartups":5}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0600)

	err := cleanClaudeJSON(home)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestorePrimaryApiKeyFromState_RestoresKey(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()

	// Pre-install state had a key
	state := map[string]any{"primaryApiKey": "sk-original"}
	stateData, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dataDir, "install-state.json"), stateData, 0600)

	// claude.json currently has our key
	claudeJSON := `{"primaryApiKey":"sk-yesmem-set","numStartups":5}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0600)

	restorePrimaryApiKeyFromState(dataDir, home)

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if cfg["primaryApiKey"] != "sk-original" {
		t.Fatalf("expected sk-original restored, got %v", cfg["primaryApiKey"])
	}
}

func TestSavePreInstallState_SavesPrimaryApiKey(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	existing := `{"primaryApiKey":"sk-original","numStartups":5}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(existing), 0600)

	settings := map[string]any{}
	savePreInstallState(dataDir, settings, home)

	state := loadPreInstallState(dataDir)
	if state == nil {
		t.Fatal("state not saved")
	}
	if state["primaryApiKey"] != "sk-original" {
		t.Fatalf("expected sk-original, got %v", state["primaryApiKey"])
	}
}
