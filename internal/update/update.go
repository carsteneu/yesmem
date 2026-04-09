package update

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// BinaryPath returns the path to the currently running yesmem binary.
func BinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		home, _ := os.UserHomeDir()
		return home + "/.local/bin/yesmem"
	}
	return exe
}

// RunUpdate checks for an update, downloads it, and replaces the binary.
// Returns the new version string if updated, or empty string if already current.
func RunUpdate(currentVersion string, logger *log.Logger) (string, error) {
	if logger == nil {
		logger = log.Default()
	}

	logger.Println("[update] checking for updates...")
	info, err := CheckForUpdate(currentVersion)
	if err != nil {
		return "", fmt.Errorf("check failed: %w", err)
	}
	if !info.Available {
		logger.Println("[update] already up to date")
		return "", nil
	}

	logger.Printf("[update] new version available: %s (current: %s)", info.Version, currentVersion)

	if info.BinaryURL == "" {
		return "", fmt.Errorf("no binary for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, info.Version)
	}
	if info.ChecksumURL == "" {
		return "", fmt.Errorf("no checksums.txt in release %s", info.Version)
	}

	dest := BinaryPath()
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	logger.Printf("[update] downloading %s → %s", asset, dest)

	if err := DownloadAndReplace(info.BinaryURL, info.ChecksumURL, asset, dest); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	logger.Printf("[update] installed %s successfully", info.Version)

	logger.Println("[update] running post-update migration...")
	if err := runMigrate(dest); err != nil {
		logger.Printf("[update] migration warning: %v", err)
	}

	return info.Version, nil
}

func runMigrate(binaryPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "migrate")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func restartArgs() []string {
	return []string{"restart"}
}

// RunRestart executes "yesmem restart" to restart daemon and proxy.
func RunRestart(logger *log.Logger) error {
	if logger == nil {
		logger = log.Default()
	}
	logger.Println("[update] restarting daemon + proxy...")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, BinaryPath(), restartArgs()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ParseCheckInterval parses a duration string like "6h", "12h".
// Returns 6h as default on empty or invalid input.
func ParseCheckInterval(s string) time.Duration {
	if s == "" {
		return 6 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 1*time.Hour {
		return 6 * time.Hour
	}
	return d
}
