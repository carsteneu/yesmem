package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The codebase-memory-mcp daemon has no unit of its own: whichever client touches it
// first starts it, systemd --user adopts the process, and nothing knows its version,
// restarts it, or notices it is gone. Every scan that arrives without it pays the cold
// start — measured on 2026-11-13 as 4.5 s against 1.1 s with the daemon up. yesmem
// installs the binary, so yesmem owns the unit and the version.

// CBMBinaryPath is where yesmem keeps the CBM binary.
func CBMBinaryPath(dataDir string) string {
	return filepath.Join(dataDir, "cli", "codebase-memory-mcp")
}

// CBMUnitName is the systemd user unit that keeps the CBM daemon up.
const CBMUnitName = "yesmem-cbm.service"

// buildCBMUnit renders the unit. No PartOf: the daemon must survive a yesmem restart,
// which is exactly what a child process would not.
func buildCBMUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=codebase-memory-mcp daemon — code graph for the yesmem code tools
After=default.target

[Service]
Type=simple
ExecStart=%s --cbm-daemon-internal --cbm-daemon-permanent
Restart=on-failure
RestartSec=5
TimeoutStartSec=120

[Install]
WantedBy=default.target
`, binaryPath)
}

// EnsureCBMUnit writes the unit when it is missing or names another binary, then
// reloads and enables it. changed reports whether the file was rewritten; the reload
// error is returned but the file stays, so a host without a user manager still ends up
// with a correct unit on disk.
func EnsureCBMUnit(home, binaryPath string) (bool, error) {
	unitPath := filepath.Join(home, ".config", "systemd", "user", CBMUnitName)
	want := buildCBMUnit(binaryPath)

	changed := false
	if current, err := os.ReadFile(unitPath); err != nil || string(current) != want {
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
			return false, fmt.Errorf("create systemd user dir: %w", err)
		}
		if err := os.WriteFile(unitPath, []byte(want), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", unitPath, err)
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, enableCBMUnit()
}

// enableCBMUnit loads the rewritten unit and starts it. enable --now is idempotent:
// on an already running daemon it only re-asserts the enablement.
func enableCBMUnit() error {
	ctx, cancel := context.WithTimeout(context.Background(), systemdReloadTimeout)
	defer cancel()
	if err := systemctlRun(ctx, "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if err := systemctlRun(ctx, "--user", "enable", "--now", CBMUnitName); err != nil {
		return fmt.Errorf("systemctl --user enable --now %s: %w", CBMUnitName, err)
	}
	return nil
}

// parseCBMVersion reads "0.12.1" out of `codebase-memory-mcp --version`. The output
// says "codebase-memory-mcp 0.10.8 (existing)" from the setup path as well.
func parseCBMVersion(out string) string {
	for _, field := range strings.Fields(out) {
		if strings.Count(field, ".") >= 1 && field[0] >= '0' && field[0] <= '9' {
			return field
		}
	}
	return ""
}

// cbmVersionOlder reports whether installed is behind latest. An unknown installed
// version is never "older": a blind upgrade would replace a binary we could not even
// read, and an empty answer from the release feed means nothing to compare against.
func cbmVersionOlder(installed, latest string) bool {
	if installed == "" || latest == "" {
		return false
	}
	left, right := strings.Split(installed, "."), strings.Split(latest, ".")
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r int
		if i < len(left) {
			l, _ = strconv.Atoi(strings.TrimFunc(left[i], func(c rune) bool { return c < '0' || c > '9' }))
		}
		if i < len(right) {
			r, _ = strconv.Atoi(strings.TrimFunc(right[i], func(c rune) bool { return c < '0' || c > '9' }))
		}
		if l != r {
			return l < r
		}
	}
	return false
}

// cbmReleaseAPI is the release feed the version check reads.
const cbmReleaseAPI = "https://api.github.com/repos/" // + cbmRepo + "/releases/latest"

// cbmLatestVersion asks GitHub for the newest release tag. Seamed for tests.
var cbmLatestVersion = func(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cbmReleaseAPI+cbmRepo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release feed: http %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return strings.TrimPrefix(body.TagName, "v"), nil
}

// runCBMInstaller runs the installer the release ships. It downloads the platform
// archive, verifies its checksum and replaces the binary in place.
var runCBMInstaller = func(ctx context.Context, script string) error {
	return exec.CommandContext(ctx, "bash", script).Run()
}

// EnsureCBMVersion upgrades an installation that is behind the release feed, then
// restarts the daemon. The daemon is stopped first: it holds the binary, so the swap
// fails with "text file busy" while it runs (2026-11-13).
//
// installed reports the binary in use. A failed release lookup is not an error — the
// update must not fail because GitHub is unreachable.
func EnsureCBMVersion(dataDir string, installed func(context.Context) (string, error)) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	have, _ := installed(ctx)
	if have == "" {
		return false, nil
	}
	latest, err := cbmLatestVersion(ctx)
	if err != nil {
		return false, nil
	}
	if !cbmVersionOlder(have, latest) {
		return false, nil
	}

	script := filepath.Join(dataDir, "cli", "install.sh")
	if _, statErr := os.Stat(script); statErr != nil {
		return false, fmt.Errorf("CBM is at %s but the release is %s and %s is missing; reinstall the CLI bundle",
			have, latest, script)
	}

	if err := systemctlRun(ctx, "--user", "stop", CBMUnitName); err != nil {
		// No unit yet is the normal case on an installation that predates it.
		_ = err
	}
	if err := runCBMInstaller(ctx, script); err != nil {
		return false, fmt.Errorf("CBM installer: %w", err)
	}
	if err := systemctlRun(ctx, "--user", "start", CBMUnitName); err != nil {
		return true, fmt.Errorf("systemctl --user start %s: %w", CBMUnitName, err)
	}
	return true, nil
}

// InstalledCBMVersion reads the version of the binary at dataDir. Output carries the
// "(existing)" suffix when the binary was already there.
func InstalledCBMVersion(dataDir string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		out, err := exec.CommandContext(ctx, CBMBinaryPath(dataDir), "--version").Output()
		if err != nil {
			return "", err
		}
		return parseCBMVersion(string(out)), nil
	}
}
