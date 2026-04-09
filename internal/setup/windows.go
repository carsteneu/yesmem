//go:build windows

package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func setupWindows(binaryPath string) error {
	// Create scheduled task that runs at logon
	args := []string{
		"/Create",
		"/TN", "YesMem",
		"/TR", fmt.Sprintf(`"%s" daemon`, binaryPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F", // force overwrite if exists
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func removeWindows() {
	exec.Command("schtasks", "/Delete", "/TN", "YesMem", "/F").Run()
}

func ensureProxyEnvVarWindows(home string) error {
	// Add to PowerShell profile
	psDir := filepath.Join(home, "Documents", "PowerShell")
	os.MkdirAll(psDir, 0755)
	psProfile := filepath.Join(psDir, "Microsoft.PowerShell_profile.ps1")

	marker := "ANTHROPIC_BASE_URL"
	envLine := `$env:ANTHROPIC_BASE_URL = "http://localhost:9099"`

	data, _ := os.ReadFile(psProfile)
	if strings.Contains(string(data), marker) {
		return nil
	}

	f, err := os.OpenFile(psProfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "\n# YesMem proxy\n%s\n", envLine)
	return err
}
