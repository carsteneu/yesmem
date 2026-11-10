package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The DONE gate's verification is on by default: it is the layer that stops a
// report from being believed, and a default-off switch would leave every loop
// exactly as trusting as before. A config file that does not mention the key
// must therefore keep it enabled.
func TestDoneVerifyIsEnabledByDefault(t *testing.T) {
	if !Default().DoneVerify.Enabled {
		t.Error("Default().DoneVerify.Enabled = false, want true")
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("data_dir: ~/.claude/yesmem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.DoneVerify.Enabled {
		t.Error("a config without the key loaded with DoneVerify disabled, want the default enabled")
	}
}

// An operator who switches the gate off says so explicitly, and that has to
// survive the load: an unreadable switch cannot be audited.
func TestDoneVerifyCanBeSwitchedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "data_dir: ~/.claude/yesmem\ndone_verify:\n  enabled: false\n  model: deepseek-chat\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DoneVerify.Enabled {
		t.Error("DoneVerify.Enabled = true, want the written false")
	}
	if loaded.DoneVerify.Model != "deepseek-chat" {
		t.Errorf("DoneVerify.Model = %q, want %q", loaded.DoneVerify.Model, "deepseek-chat")
	}
}
