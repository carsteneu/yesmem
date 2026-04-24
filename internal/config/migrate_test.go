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
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n  skill_eval_inject: \"true\"\n  effort_floor: \"high\"\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 fields added for existing config, got %d", n)
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
	if n != 0 {
		t.Errorf("expected 0 fields when proxy section missing, got %d", n)
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
