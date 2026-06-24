package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeMinimalFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetectAgents_NoConfig(t *testing.T) {
	dir := t.TempDir()
	agents := detectAgents(dir)
	if len(agents) != 0 {
		t.Fatalf("expected no agents, got %v", agents)
	}
}

func TestDetectAgents_OpenCodeOnly(t *testing.T) {
	dir := t.TempDir()
	setupOpenCodeConfig(t, dir)
	agents := detectAgents(dir)
	if len(agents) != 1 || agents[0] != "opencode" {
		t.Fatalf("expected [opencode], got %v", agents)
	}
}

func TestDetectAgents_ClaudeOnly(t *testing.T) {
	dir := t.TempDir()
	setupClaudeConfig(t, dir)
	agents := detectAgents(dir)
	if len(agents) != 1 || agents[0] != "claude" {
		t.Fatalf("expected [claude], got %v", agents)
	}
}

func TestDetectAgents_CodexOnly(t *testing.T) {
	dir := t.TempDir()
	setupCodexConfig(t, dir)
	agents := detectAgents(dir)
	if len(agents) != 1 || agents[0] != "codex" {
		t.Fatalf("expected [codex], got %v", agents)
	}
}

func TestDetectAgents_AllThree_OpenCodePrecedence(t *testing.T) {
	dir := t.TempDir()
	setupOpenCodeConfig(t, dir)
	setupClaudeConfig(t, dir)
	setupCodexConfig(t, dir)
	agents := detectAgents(dir)
	// OpenCode should always be first when present
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %v", agents)
	}
	if agents[0] != "opencode" {
		t.Fatalf("expected opencode first (precedence), got order: %v", agents)
	}
}

func TestDetectAgents_ClaudeAndCodex(t *testing.T) {
	dir := t.TempDir()
	setupClaudeConfig(t, dir)
	setupCodexConfig(t, dir)
	agents := detectAgents(dir)
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %v", agents)
	}
}

func TestHasOpenCodeConfig_AllFilesPresent(t *testing.T) {
	dir := t.TempDir()
	setupOpenCodeConfig(t, dir)
	if !hasOpenCodeConfig(dir) {
		t.Fatalf("expected OpenCode config detected")
	}
}

func TestHasOpenCodeConfig_MissingModelsJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".config", "opencode", "opencode.json"), `{}`)
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"), `{}`)
	// models.json missing
	if hasOpenCodeConfig(dir) {
		t.Fatalf("expected OpenCode NOT detected (models.json missing)")
	}
}

func TestHasOpenCodeConfig_MissingAuthJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".config", "opencode", "opencode.json"), `{}`)
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"), `{}`)
	// auth.json missing
	if hasOpenCodeConfig(dir) {
		t.Fatalf("expected OpenCode NOT detected (auth.json missing)")
	}
}

func TestHasClaudeCodeConfig_WithPrimaryApiKey(t *testing.T) {
	dir := t.TempDir()
	setupClaudeConfig(t, dir)
	if !hasClaudeCodeConfig(dir) {
		t.Fatalf("expected Claude Code config detected")
	}
}

func TestHasClaudeCodeConfig_NoPrimaryApiKey(t *testing.T) {
	dir := t.TempDir()
	emptyClaudeJSON := map[string]any{"other": "value"}
	data, _ := json.Marshal(emptyClaudeJSON)
	writeMinimalFile(t, filepath.Join(dir, ".claude.json"), string(data))
	if hasClaudeCodeConfig(dir) {
		t.Fatalf("expected Claude NOT detected (no primaryApiKey)")
	}
}

func TestHasClaudeCodeConfig_WithClaudeConfigJSON(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".claude", "config.json"), `{"installMethod":"native"}`)
	if !hasClaudeCodeConfig(dir) {
		t.Fatalf("expected Claude detected via ~/.claude/config.json")
	}
}

func TestHasCodexConfig_DefaultLocation(t *testing.T) {
	dir := t.TempDir()
	setupCodexConfig(t, dir)
	if !hasCodexConfig(dir) {
		t.Fatalf("expected Codex config detected")
	}
}

func TestHasCodexConfig_LegacyLocation(t *testing.T) {
	dir := t.TempDir()
	writeMinimalFile(t, filepath.Join(dir, ".codex", "config.json"), `{}`)
	if !hasCodexConfig(dir) {
		t.Fatalf("expected Codex detected via ~/.codex/config.json")
	}
}

func TestHasCodexConfig_NoConfig(t *testing.T) {
	dir := t.TempDir()
	if hasCodexConfig(dir) {
		t.Fatalf("expected Codex NOT detected (no config file)")
	}
}

// Helpers to set up realistic config structures

func setupOpenCodeConfig(t *testing.T, dir string) {
	t.Helper()
	writeMinimalFile(t, filepath.Join(dir, ".config", "opencode", "opencode.json"), `{"$schema":"https://opencode.ai/config.json"}`)
	writeMinimalFile(t, filepath.Join(dir, ".local", "share", "opencode", "auth.json"), `{"deepseek":{"type":"api","key":"sk-test"}}`)
	writeMinimalFile(t, filepath.Join(dir, ".cache", "opencode", "models.json"), `{"deepseek":{"npm":"@ai-sdk/openai-compatible","api":"https://api.deepseek.com","models":{"deepseek-chat":{"id":"deepseek-chat"}}}}`)
}

func setupClaudeConfig(t *testing.T, dir string) {
	t.Helper()
	claudeJSON := map[string]any{
		"primaryApiKey": "sk-ant-test-key",
	}
	data, _ := json.Marshal(claudeJSON)
	writeMinimalFile(t, filepath.Join(dir, ".claude.json"), string(data))
}

func setupCodexConfig(t *testing.T, dir string) {
	t.Helper()
	writeMinimalFile(t, filepath.Join(dir, ".config", "codex", "config.json"), `{"model":"gpt-5.5-codex"}`)
}
