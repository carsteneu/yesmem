package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var validTerminals = map[string]bool{
	"ghostty":        true,
	"kitty":          true,
	"gnome-terminal": true,
	"alacritty":      true,
	"wezterm":        true,
	"xterm":          true,
	"Terminal":       true,
	"iTerm2":         true,
	"unknown":        true,
}

func TestDetectTerminal_ReturnsNonEmpty(t *testing.T) {
	result := DetectTerminal()
	if result == "" {
		t.Error("DetectTerminal() returned empty string")
	}
}

func TestDetectTerminal_ReturnsValidValue(t *testing.T) {
	result := DetectTerminal()
	if !validTerminals[result] {
		t.Errorf("DetectTerminal() returned unexpected value %q", result)
	}
}

func TestDetectTerminalFromEnv_TermProgram(t *testing.T) {
	tests := []struct {
		envVal   string
		expected string
	}{
		{"ghostty", "ghostty"},
		{"iTerm.app", "iTerm2"},
		{"Apple_Terminal", "Terminal"},
		{"WezTerm", "wezterm"},
		{"unknown-term", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.envVal, func(t *testing.T) {
			result := detectFromTermProgram(tc.envVal)
			if result != tc.expected {
				t.Errorf("detectFromTermProgram(%q) = %q, want %q", tc.envVal, result, tc.expected)
			}
		})
	}
}

func TestDetectTerminalFromEnv_KittyPID(t *testing.T) {
	result := detectFromEnvVars("12345", "", "")
	if result != "kitty" {
		t.Errorf("expected kitty when KITTY_PID set, got %q", result)
	}
}

func TestDetectTerminalFromEnv_AlacrittySocket(t *testing.T) {
	result := detectFromEnvVars("", "/tmp/alacritty.sock", "")
	if result != "alacritty" {
		t.Errorf("expected alacritty when ALACRITTY_SOCKET set, got %q", result)
	}
}

func TestDetectTerminalFromEnv_TermProgram_Priority(t *testing.T) {
	// TERM_PROGRAM should win when set alongside KITTY_PID
	result := detectFromEnvVars("12345", "", "ghostty")
	if result != "ghostty" {
		t.Errorf("expected ghostty (TERM_PROGRAM wins), got %q", result)
	}
}

func TestIsTmuxAvailable(t *testing.T) {
	t.Run("tmux in PATH", func(t *testing.T) {
		dir := t.TempDir()
		fake := filepath.Join(dir, "tmux")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		if !isTmuxAvailable() {
			t.Error("expected isTmuxAvailable() = true when tmux binary is in PATH")
		}
	})

	t.Run("tmux not in PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // empty dir — no tmux binary
		if isTmuxAvailable() {
			t.Error("expected isTmuxAvailable() = false when tmux binary is not in PATH")
		}
	})
}

func TestDetectSpawnMode(t *testing.T) {
	t.Run("tmux installed", func(t *testing.T) {
		dir := t.TempDir()
		fake := filepath.Join(dir, "tmux")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		if got := DetectSpawnMode(); got != SpawnModeTmux {
			t.Errorf("DetectSpawnMode() = %v, want SpawnModeTmux", got)
		}
	})

	t.Run("tmux not installed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if got := DetectSpawnMode(); got != SpawnModeStandard {
			t.Errorf("DetectSpawnMode() = %v, want SpawnModeStandard", got)
		}
	})
}

func TestTmuxSessionHasClients(t *testing.T) {
	t.Run("non-existent session returns false", func(t *testing.T) {
		if TmuxSessionHasClients("definitely-does-not-exist-xyz-99999") {
			t.Error("expected false for non-existent tmux session")
		}
	})
}

func TestNormalizeProcessName(t *testing.T) {
	tests := []struct {
		proc     string
		expected string
	}{
		{"gnome-terminal-", "gnome-terminal"},
		{"gnome-terminal-server", "gnome-terminal"},
		{"kitty", "kitty"},
		{"alacritty", "alacritty"},
		{"ghostty", "ghostty"},
		{"wezterm-gui", "wezterm"},
		{"xterm", "xterm"},
		{"bash", "unknown"},
		{"", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.proc, func(t *testing.T) {
			result := normalizeProcessName(tc.proc)
			if result != tc.expected {
				t.Errorf("normalizeProcessName(%q) = %q, want %q", tc.proc, result, tc.expected)
			}
		})
	}
}

func TestEnsureTmuxSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	session := fmt.Sprintf("yesmem-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", session).Run() })

	if err := EnsureTmuxSession(session); err != nil {
		t.Fatalf("EnsureTmuxSession(%q) error: %v", session, err)
	}
	if err := exec.Command("tmux", "has-session", "-t", session).Run(); err != nil {
		t.Errorf("expected session %q to exist after EnsureTmuxSession", session)
	}
	// calling again on existing session must be idempotent
	if err := EnsureTmuxSession(session); err != nil {
		t.Errorf("EnsureTmuxSession on existing session must not error: %v", err)
	}
}
