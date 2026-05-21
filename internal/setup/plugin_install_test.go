package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallOpencodePluginSource(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")

	if err := installOpencodePluginSource(home); err != nil {
		t.Fatalf("installOpencodePluginSource: %v", err)
	}

	dstDir := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem")
	indexTS := filepath.Join(dstDir, "index.ts")

	data, err := os.ReadFile(indexTS)
	if err != nil {
		t.Fatalf("index.ts not installed: %v", err)
	}
	if len(data) == 0 {
		t.Error("index.ts is empty")
	}

	// Check other files
	for _, name := range []string{"rpc.ts", "types.ts", "package.json"} {
		if _, err := os.Stat(filepath.Join(dstDir, name)); os.IsNotExist(err) {
			t.Errorf("%s not installed", name)
		}
	}
}

func TestInstallOpencodePluginSource_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")

	if err := installOpencodePluginSource(home); err != nil {
		t.Fatal(err)
	}

	// Second call should succeed without error
	if err := installOpencodePluginSource(home); err != nil {
		t.Fatalf("second installOpencodePluginSource: %v", err)
	}
}

func TestInstallOpencodePluginSource_ResolveAfterInstall(t *testing.T) {
	tmpDir := t.TempDir()
	home := filepath.Join(tmpDir, "home")
	binaryPath := filepath.Join(home, ".local", "bin", "yesmem")
	os.MkdirAll(filepath.Dir(binaryPath), 0755)
	os.WriteFile(binaryPath, []byte("fake"), 0755)

	if err := installOpencodePluginSource(home); err != nil {
		t.Fatal(err)
	}

	pluginSource := resolvePluginSource(home, binaryPath)
	if pluginSource == "" {
		t.Fatal("resolvePluginSource returned empty after install")
	}

	expected := filepath.Join(home, ".local", "share", "yesmem", "plugins", "opencode-yesmem", "index.ts")
	if pluginSource != expected {
		t.Errorf("expected %s, got %s", expected, pluginSource)
	}

	if _, err := os.Stat(pluginSource); os.IsNotExist(err) {
		t.Error("resolved plugin source does not exist")
	}
}
