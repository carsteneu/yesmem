package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateConfig_AddsSkillEvalInject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n  prompt_ungate: true\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "skill_eval_inject") {
		t.Error("config should contain skill_eval_inject after migration")
	}
}

func TestMigrateConfig_AddsEffortFloor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "effort_floor") {
		t.Error("config should contain effort_floor after migration")
	}
}

func TestMigrateConfig_SkipsExistingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Config with all fields already present — MigrateConfig should add nothing
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  skill_eval_inject: "true"
  effort_floor: "high"
  auto_configure_providers: true
  openai_target: "https://api.openai.com"
  reset_cache: false
  cache_keepalive_min_messages: 10
  model_features:
    claude:
      skill_eval: true
      think_reminder_min_chars: 0

paths:
  opencode_db: /custom/opencode.db

agents:
  default_backend: claude

exclude_projects:
  - /home/testuser
  - /tmp

caps_dir: ""
default_sandbox_profile: ""
secrets_sanitization:
  enabled: false
http:
  enabled: false

forked_agents:
  max_forks_per_session: 50
  max_cost_per_session: 5

token_thresholds:
  deepseek: 600000
  glm-5.2: 500000

pricing:
  deepseek-v4-flash: {input: 0.14, output: 0.56}
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 fields added for fully migrated config, got %d", n)
	}
}

func TestMigrateConfig_AddsAutoConfigureProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "auto_configure_providers: true") {
		t.Error("config should contain auto_configure_providers after migration")
	}
}

func TestMigrateConfig_PreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "proxy:\n  enabled: true\n  prompt_ungate: true\n\nextraction:\n  model: sonnet\n"
	os.WriteFile(path, []byte(original), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "prompt_ungate: true") {
		t.Error("existing content should be preserved")
	}
	if !strings.Contains(string(data), "extraction:") {
		t.Error("other sections should be preserved")
	}
}

func TestMigrateConfig_NoProxySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("extraction:\n  model: sonnet\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// model_features + opencode_db are added, proxy migrations are skipped
	if n == 0 {
		t.Errorf("expected at least 1 field added (model_features/opencode_db), got %d", n)
	}
}

func TestMigrateConfig_FileNotFound(t *testing.T) {
	_, err := MigrateConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestMigrateConfig_InsertsInsideProxySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n\nextraction:\n  model: sonnet\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	content := string(data)
	proxyIdx := strings.Index(content, "proxy:")
	extractionIdx := strings.Index(content, "extraction:")
	skillEvalIdx := strings.Index(content, "skill_eval_inject")

	if skillEvalIdx < proxyIdx {
		t.Error("skill_eval_inject should appear after proxy:")
	}
	if skillEvalIdx > extractionIdx {
		t.Error("skill_eval_inject should appear before extraction: (inside proxy section)")
	}
}

func TestMigrateConfig_AddsOpencodeDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("extraction:\n  model: sonnet\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "opencode_db") {
		t.Error("config should contain opencode_db after migration")
	}
}

func TestMigrateConfig_AddsModelFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n\nextraction:\n  model: sonnet\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "model_features:") {
		t.Error("config should contain model_features after migration")
	}
	if !strings.Contains(string(data), "feature_defaults:") {
		t.Error("config should contain feature_defaults after migration")
	}
	// model_features should be inside proxy:
	proxyIdx := strings.Index(string(data), "proxy:")
	mfIdx := strings.Index(string(data), "model_features:")
	extractionIdx := strings.Index(string(data), "extraction:")
	if mfIdx < proxyIdx || mfIdx > extractionIdx {
		t.Error("model_features should be inside proxy: section")
	}
}

func TestMigrateConfig_IdempotentModelFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Config that already has all fields MigrateConfig checks for
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  skill_eval_inject: "silent"
  effort_floor: ""
  auto_configure_providers: true
  openai_target: "https://api.openai.com"
  reset_cache: false
  cache_keepalive_min_messages: 10
  model_features:
    claude:
      skill_eval: true
      think_reminder_min_chars: 0

paths:
  opencode_db: /custom/opencode.db

agents:
  default_backend: claude

exclude_projects:
  - /home/testuser
  - /tmp

caps_dir: ""
default_sandbox_profile: ""
secrets_sanitization:
  enabled: false
http:
  enabled: false

forked_agents:
  max_forks_per_session: 50
  max_cost_per_session: 5

token_thresholds:
  deepseek: 600000
  glm-5.2: 500000

pricing:
  deepseek-v4-flash: {input: 0.14, output: 0.56}
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 fields added for fully migrated config, got %d", n)
	}
}

func TestMigrateConfig_AddsAgentsDefaultBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agents:\n  terminal: kitty\n\nextraction:\n  model: sonnet\n"), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "default_backend: claude") {
		t.Error("config should contain default_backend: claude after migration")
	}
	dbIdx := strings.Index(content, "default_backend:")
	extIdx := strings.Index(content, "extraction:")
	if dbIdx > extIdx {
		t.Error("default_backend should be inside the agents: section, not after extraction:")
	}

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if c := strings.Count(string(data), "default_backend:"); c != 1 {
		t.Errorf("default_backend should appear exactly once after second run, got %d", c)
	}
}

func TestMigrateConfig_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "proxy:\n  enabled: true\nextraction:\n  model: sonnet\n"
	os.WriteFile(path, []byte(original), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}

	// Check that a timestamped backup was created in the same directory
	entries, _ := os.ReadDir(dir)
	var backups []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.yaml.bak.") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) == 0 {
		t.Error("expected backup file config.yaml.bak.<timestamp> after migration")
	} else if len(backups) > 1 {
		t.Errorf("expected 1 backup, got %d: %v", len(backups), backups)
	}

	// Verify backup content matches original
	if len(backups) > 0 {
		backupData, _ := os.ReadFile(filepath.Join(dir, backups[0]))
		content := strings.TrimSpace(string(backupData))
		if content != strings.TrimSpace(original) {
			t.Errorf("backup content mismatch:\n got:  %s\nwant: %s", content, original)
		}
	}
}
