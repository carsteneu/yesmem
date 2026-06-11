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
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n  skill_eval_inject: \"true\"\n  effort_floor: \"high\"\n  auto_configure_providers: true\n  model_features:\n    claude:\n      skill_eval: true\n\npaths:\n  opencode_db: /custom/opencode.db\n\nexclude_projects:\n  - /home/testuser\n  - /tmp\n"), 0644)

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
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n  skill_eval_inject: \"silent\"\n  effort_floor: \"\"\n  auto_configure_providers: true\n  model_features:\n    claude:\n      skill_eval: true\n\npaths:\n  opencode_db: /custom/opencode.db\n\nexclude_projects:\n  - /home/testuser\n  - /tmp\n"), 0644)

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
