package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterMCPPermissions_Empty(t *testing.T) {
	settings := map[string]any{}
	registerMCPPermissions(settings)

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("permissions not set")
	}
	allow, ok := perms["allow"].([]any)
	if !ok {
		t.Fatal("permissions.allow not set")
	}
	if len(allow) != 1 || allow[0] != "mcp__yesmem__*" {
		t.Fatalf("expected [mcp__yesmem__*], got %v", allow)
	}
}

func TestRegisterMCPPermissions_ExistingPermissions(t *testing.T) {
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(git:*)", "WebSearch"},
		},
	}
	registerMCPPermissions(settings)

	perms := settings["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if len(allow) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(allow), allow)
	}
	found := false
	for _, v := range allow {
		if v == "mcp__yesmem__*" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mcp__yesmem__* not found in %v", allow)
	}
}

func TestRegisterMCPPermissions_AlreadyPresent(t *testing.T) {
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(git:*)", "mcp__yesmem__*"},
		},
	}
	registerMCPPermissions(settings)

	allow := settings["permissions"].(map[string]any)["allow"].([]any)
	count := 0
	for _, v := range allow {
		if v == "mcp__yesmem__*" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 mcp__yesmem__*, got %d in %v", count, allow)
	}
}

func TestAddPostToolUseResolveHook_HasIfCondition(t *testing.T) {
	hooks := map[string]any{}
	addPostToolUseResolveHook(hooks, "/usr/bin/yesmem")

	entries, ok := hooks["PostToolUse"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("expected 1 PostToolUse entry, got %v", hooks["PostToolUse"])
	}
	entry := entries[0].(map[string]any)
	ifVal, ok := entry["if"].(string)
	if !ok {
		t.Fatal("if field not set on PostToolUse resolve hook")
	}
	if ifVal != "Bash(git *)" {
		t.Fatalf("expected 'Bash(git *)', got %q", ifVal)
	}
}

func TestAddPostToolUseResolveHook_MigratesExistingWithoutIf(t *testing.T) {
	hooks := map[string]any{
		"PostToolUse": []any{
			map[string]any{
				"matcher": "Bash",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": "/usr/bin/yesmem hook-resolve",
					},
				},
			},
		},
	}
	addPostToolUseResolveHook(hooks, "/usr/bin/yesmem")

	entries := hooks["PostToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (no duplicate), got %d", len(entries))
	}
	entry := entries[0].(map[string]any)
	ifVal, ok := entry["if"].(string)
	if !ok {
		t.Fatal("if field not migrated on existing hook")
	}
	if ifVal != "Bash(git *)" {
		t.Fatalf("expected 'Bash(git *)', got %q", ifVal)
	}
}

func TestRegisterMCPPermissions_PermissionsWithoutAllow(t *testing.T) {
	settings := map[string]any{
		"permissions": map[string]any{
			"defaultMode": "default",
		},
	}
	registerMCPPermissions(settings)

	perms := settings["permissions"].(map[string]any)
	allow, ok := perms["allow"].([]any)
	if !ok {
		t.Fatal("permissions.allow not created")
	}
	if len(allow) != 1 || allow[0] != "mcp__yesmem__*" {
		t.Fatalf("expected [mcp__yesmem__*], got %v", allow)
	}
	// defaultMode should be preserved
	if perms["defaultMode"] != "default" {
		t.Fatal("defaultMode was overwritten")
	}
}

func TestAddSessionStartHook_UsesBriefingHookCommand(t *testing.T) {
	hooks := map[string]any{}
	addSessionStartHook(hooks, "/usr/local/bin/yesmem")

	entries, ok := hooks["SessionStart"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatal("SessionStart hook not registered")
	}
	hookList := entries[0].(map[string]any)["hooks"].([]any)
	cmd := hookList[0].(map[string]any)["command"].(string)
	want := "/usr/local/bin/yesmem briefing-hook"
	if cmd != want {
		t.Errorf("hook command = %q want %q", cmd, want)
	}
}

func TestAddSessionStartHook_UpdatesExistingCommandToBriefingHook(t *testing.T) {
	hooks := map[string]any{
		"SessionStart": []any{
			map[string]any{
				"matcher": "startup|resume|clear|compact",
				"hooks": []any{
					map[string]any{"type": "command", "command": "/home/user/.claude/hooks/yesmem-briefing.sh"},
				},
			},
		},
	}
	addSessionStartHook(hooks, "/usr/local/bin/yesmem")

	entries := hooks["SessionStart"].([]any)
	hookList := entries[0].(map[string]any)["hooks"].([]any)
	cmd := hookList[0].(map[string]any)["command"].(string)
	want := "/usr/local/bin/yesmem briefing-hook"
	if cmd != want {
		t.Errorf("existing hook not updated: got %q want %q", cmd, want)
	}
}

func TestSetPrimaryApiKeyInClaudeJSON_RemovesOAuthAccount(t *testing.T) {
	home := t.TempDir()
	existing := `{"numStartups":5,"primaryApiKey":"sk-old","oauthAccount":{"emailAddress":"test@test.com","displayName":"Test"}}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(existing), 0600)

	err := clearClaudeJSONAuth(home)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if _, ok := cfg["primaryApiKey"]; ok {
		t.Fatal("primaryApiKey should be removed")
	}
	if _, ok := cfg["oauthAccount"]; ok {
		t.Fatal("oauthAccount should be removed")
	}
	if cfg["numStartups"] != float64(5) {
		t.Fatal("other fields should be preserved")
	}
}

func TestSavePreInstallState_SavesOAuthAccount(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	existing := `{"primaryApiKey":"sk-old","oauthAccount":{"emailAddress":"test@test.com"}}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(existing), 0600)

	settings := map[string]any{}
	savePreInstallState(dataDir, settings, home)

	state := loadPreInstallState(dataDir)
	if state == nil {
		t.Fatal("state not saved")
	}
	oa, ok := state["oauthAccount"].(map[string]any)
	if !ok {
		t.Fatal("oauthAccount not saved in pre-install state")
	}
	if oa["emailAddress"] != "test@test.com" {
		t.Fatalf("expected test@test.com, got %v", oa["emailAddress"])
	}
}

func TestClearClaudeJSONAuth_RemovesBothKeys(t *testing.T) {
	home := t.TempDir()
	existing := `{"numStartups":5,"primaryApiKey":"sk-old-key","mcpServers":{}}`
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(existing), 0600)

	err := clearClaudeJSONAuth(home)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if _, ok := cfg["primaryApiKey"]; ok {
		t.Fatal("primaryApiKey should be removed")
	}
	if cfg["numStartups"] != float64(5) {
		t.Fatal("other fields should be preserved")
	}
}

func TestClearClaudeJSONAuth_NoFileNoError(t *testing.T) {
	home := t.TempDir()
	err := clearClaudeJSONAuth(home)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
}

func TestRegisterStatusLine_SetsRefreshInterval(t *testing.T) {
	settings := map[string]any{}
	registerStatusLine(settings, "/usr/bin/yesmem")

	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		t.Fatal("statusLine not set")
	}
	if sl["type"] != "command" {
		t.Fatalf("type = %v, want command", sl["type"])
	}
	if sl["command"] != "/usr/bin/yesmem statusline" {
		t.Fatalf("command = %v, want '/usr/bin/yesmem statusline'", sl["command"])
	}
	ri, ok := sl["refreshInterval"].(int)
	if !ok {
		t.Fatal("refreshInterval not set")
	}
	if ri != 2 {
		t.Fatalf("refreshInterval = %d, want 2", ri)
	}
}

func TestSavePreInstallState_SavesAPIKey(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY": "sk-ant-existing",
		},
	}
	savePreInstallState(dir, settings, "")

	state := loadPreInstallState(dir)
	if state == nil {
		t.Fatal("state not saved")
	}
	if state["envAPIKey"] != "sk-ant-existing" {
		t.Fatalf("expected sk-ant-existing, got %v", state["envAPIKey"])
	}
}

func TestSavePreInstallState_SavesAbsentAPIKey(t *testing.T) {
	dir := t.TempDir()
	settings := map[string]any{}
	savePreInstallState(dir, settings, "")

	state := loadPreInstallState(dir)
	if state == nil {
		t.Fatal("state not saved")
	}
	if state["envAPIKey"] != nil {
		t.Fatalf("absent key should be nil, got %v", state["envAPIKey"])
	}
}
