package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateSystemdUnits_RemovesNetworkOnline(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	units := map[string]string{
		"yesmem.service": `[Unit]
Description=YesMem — Long-term memory for coding agents
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/yesmem daemon --replace
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`,
		"yesmem-proxy.service": `[Unit]
Description=YesMem Proxy — Infinite-thread context for coding agents
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/yesmem proxy
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`,
	}
	for name, content := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	changed, err := MigrateSystemdUnits(home)
	if err != nil {
		t.Fatalf("MigrateSystemdUnits: %v", err)
	}
	if changed != 2 {
		t.Fatalf("expected 2 changed units, got %d", changed)
	}

	for name, ref := range map[string]string{
		"yesmem.service":      "ExecStart=/usr/local/bin/yesmem daemon --replace",
		"yesmem-proxy.service": "ExecStart=/usr/local/bin/yesmem proxy",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if strings.Contains(content, "network-online.target") {
			t.Errorf("%s still references network-online.target:\n%s", name, content)
		}
		if strings.Contains(content, "After=") || strings.Contains(content, "Wants=") {
			t.Errorf("%s still has ordering/wants dependency:\n%s", name, content)
		}
		if !strings.Contains(content, ref) || !strings.Contains(content, "WantedBy=default.target") {
			t.Errorf("%s lost required lines:\n%s", name, content)
		}
	}
}

func TestMigrateSystemdUnits_Idempotent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	clean := `[Unit]
Description=YesMem — Long-term memory for coding agents

[Service]
Type=simple
ExecStart=/usr/local/bin/yesmem daemon --replace
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`
	if err := os.WriteFile(filepath.Join(dir, "yesmem.service"), []byte(clean), 0644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateSystemdUnits(home)
	if err != nil {
		t.Fatalf("MigrateSystemdUnits: %v", err)
	}
	if changed != 0 {
		t.Fatalf("expected 0 changed (idempotent), got %d", changed)
	}
}

func TestMigrateSystemdUnits_MissingDirIsNoop(t *testing.T) {
	home := t.TempDir() // no .config/systemd/user created
	changed, err := MigrateSystemdUnits(home)
	if err != nil {
		t.Fatalf("MigrateSystemdUnits: %v", err)
	}
	if changed != 0 {
		t.Fatalf("expected 0 changed for missing dir, got %d", changed)
	}
}
