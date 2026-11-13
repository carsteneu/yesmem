package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CBM daemon has no unit today: an MCP client starts it on first use, it is
// reparented to systemd --user, and nobody knows its version or restarts it. Without
// a running daemon every scan pays the full cold start (measured 4.5 s vs 1.1 s on
// 2026-11-13), so yesmem has to own the unit.
func TestBuildCBMUnitRunsThePermanentDaemon(t *testing.T) {
	unit := buildCBMUnit("/home/x/.claude/yesmem/cli/codebase-memory-mcp")

	for _, want := range []struct{ text, why string }{
		{"--cbm-daemon-internal", "the daemon mode"},
		{"--cbm-daemon-permanent", "permanence: without it the process exits after the call"},
		{"Restart=on-failure", "a crash must come back"},
		{"WantedBy=default.target", "it must start with the session, without yesmem"},
	} {
		if !strings.Contains(unit, want.text) {
			t.Errorf("unit misses %s (%q):\n%s", want.why, want.text, unit)
		}
	}
	if strings.Contains(unit, "PartOf=") {
		t.Errorf("the unit is coupled to yesmem.service, so a yesmem restart kills it:\n%s", unit)
	}
}

func TestParseCBMVersion(t *testing.T) {
	cases := []struct{ out, want string }{
		{"codebase-memory-mcp 0.12.1\n", "0.12.1"},
		{"codebase-memory-mcp 0.10.8 (existing)\n", "0.10.8"},
		{"0.12.1", "0.12.1"},
		{"garbage", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseCBMVersion(c.out); got != c.want {
			t.Errorf("parseCBMVersion(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}

func TestCBMVersionOlder(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              bool
	}{
		{"0.10.8", "0.12.1", true},
		{"0.12.1", "0.12.1", false},
		{"0.12.1", "0.10.8", false},
		{"0.9.0", "0.10.0", true},
		{"1.0.0", "0.99.0", false},
		{"0.12", "0.12.1", true}, // a shorter version is older, not equal
		{"", "0.12.1", false},    // unknown install: never auto-upgrade blind
		{"0.10.8", "", false},    // no answer from the release feed
	}
	for _, c := range cases {
		if got := cbmVersionOlder(c.installed, c.latest); got != c.want {
			t.Errorf("cbmVersionOlder(%q, %q) = %v, want %v", c.installed, c.latest, got, c.want)
		}
	}
}

// The unit is what an existing installation is missing, so writing it has to be part
// of the migration that "yesmem update" already runs.
func TestEnsureCBMUnitWritesEnablesAndStarts(t *testing.T) {
	home := t.TempDir()
	binaryDir := t.TempDir()
	binary := filepath.Join(binaryDir, "codebase-memory-mcp")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	var calls []string
	previous := systemctlRun
	systemctlRun = func(ctx context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { systemctlRun = previous })

	changed, err := EnsureCBMUnit(home, binary)
	if err != nil {
		t.Fatalf("EnsureCBMUnit: %v", err)
	}
	if !changed {
		t.Error("changed = false on a missing unit")
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "yesmem-cbm.service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !strings.Contains(string(data), binary) {
		t.Errorf("unit does not name the CBM binary:\n%s", data)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"--user daemon-reload", "--user enable --now yesmem-cbm.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("systemctl was not called with %q; calls:\n%s", want, joined)
		}
	}

	// A second run changes nothing on disk.
	before, _ := os.ReadFile(unitPath)
	changed, err = EnsureCBMUnit(home, binary)
	if err != nil {
		t.Fatalf("second EnsureCBMUnit: %v", err)
	}
	if changed {
		t.Error("changed = true although the unit is already current")
	}
	if after, _ := os.ReadFile(unitPath); string(after) != string(before) {
		t.Error("the unit was rewritten although it was current")
	}
}

// An installation that predates the unit has no CBM binary at the expected place and
// still must not fail the update: the unit is written for the configured path and the
// reload error is reported, not fatal, so the rest of the migration runs.
func TestEnsureCBMUnitReportsAReloadFailureWithoutLosingTheUnit(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "codebase-memory-mcp")

	previous := systemctlRun
	systemctlRun = func(ctx context.Context, args ...string) error { return errors.New("no user manager") }
	t.Cleanup(func() { systemctlRun = previous })

	changed, err := EnsureCBMUnit(home, binary)
	if !changed {
		t.Error("the unit file was not written")
	}
	if err == nil {
		t.Fatal("a failed enable was swallowed; the caller cannot report it")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "yesmem-cbm.service")); statErr != nil {
		t.Errorf("the unit is missing although it was written before the failure: %v", statErr)
	}
}

// The upgrade path: an installed 0.10.8 against a 0.12.1 release has to run the
// shipped installer, and the daemon must be stopped first — it holds the binary, so
// the swap fails with "text file busy" otherwise (seen on 2026-11-13).
func TestEnsureCBMVersionUpgradesThroughTheInstaller(t *testing.T) {
	dataDir := t.TempDir()
	installScript := filepath.Join(dataDir, "cli", "install.sh")
	if err := os.MkdirAll(filepath.Dir(installScript), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(installScript, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write installer: %v", err)
	}

	var order []string
	previousRun, previousInstall, previousLatest := systemctlRun, runCBMInstaller, cbmLatestVersion
	systemctlRun = func(ctx context.Context, args ...string) error {
		order = append(order, "systemctl "+strings.Join(args, " "))
		return nil
	}
	runCBMInstaller = func(ctx context.Context, script string) error {
		order = append(order, "install "+filepath.Base(script))
		return nil
	}
	cbmLatestVersion = func(ctx context.Context) (string, error) { return "0.12.1", nil }
	t.Cleanup(func() {
		systemctlRun, runCBMInstaller, cbmLatestVersion = previousRun, previousInstall, previousLatest
	})

	changed, err := EnsureCBMVersion(dataDir, func(context.Context) (string, error) { return "0.10.8", nil })
	if err != nil {
		t.Fatalf("EnsureCBMVersion: %v", err)
	}
	if !changed {
		t.Fatal("changed = false although 0.10.8 is behind 0.12.1")
	}
	joined := strings.Join(order, "\n")
	for _, want := range []string{"systemctl --user stop yesmem-cbm.service", "install install.sh", "start yesmem-cbm.service"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the upgrade skipped %q; order:\n%s", want, joined)
		}
	}
	if strings.Index(joined, "stop") > strings.Index(joined, "install ") {
		t.Errorf("the installer ran before the daemon was stopped; order:\n%s", joined)
	}
}

func TestEnsureCBMVersionLeavesACurrentInstallAlone(t *testing.T) {
	dataDir := t.TempDir()
	previousLatest, previousInstall := cbmLatestVersion, runCBMInstaller
	cbmLatestVersion = func(ctx context.Context) (string, error) { return "0.12.1", nil }
	runCBMInstaller = func(ctx context.Context, script string) error {
		t.Error("the installer ran although the installation is current")
		return nil
	}
	t.Cleanup(func() { cbmLatestVersion, runCBMInstaller = previousLatest, previousInstall })

	changed, err := EnsureCBMVersion(dataDir, func(context.Context) (string, error) { return "0.12.1", nil })
	if err != nil {
		t.Fatalf("EnsureCBMVersion: %v", err)
	}
	if changed {
		t.Error("changed = true for a current installation")
	}
}
