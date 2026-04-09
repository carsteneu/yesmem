package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DetectTerminal identifies the current terminal emulator.
// Returns one of: "ghostty", "kitty", "gnome-terminal", "alacritty",
// "wezterm", "xterm", "Terminal", "iTerm2", "unknown".
func DetectTerminal() string {
	kittyPID := os.Getenv("KITTY_PID")
	alacrittySocket := os.Getenv("ALACRITTY_SOCKET")
	termProgram := os.Getenv("TERM_PROGRAM")

	if result := detectFromEnvVars(kittyPID, alacrittySocket, termProgram); result != "unknown" {
		return result
	}

	// Fallback: check parent process name
	ppid := os.Getppid()
	proc := readParentProcessName(ppid)
	return normalizeProcessName(proc)
}

// detectFromEnvVars checks environment variables in priority order.
// TERM_PROGRAM wins when set, then KITTY_PID, then ALACRITTY_SOCKET.
func detectFromEnvVars(kittyPID, alacrittySocket, termProgram string) string {
	if termProgram != "" {
		return detectFromTermProgram(termProgram)
	}
	if kittyPID != "" {
		return "kitty"
	}
	if alacrittySocket != "" {
		return "alacritty"
	}
	if os.Getenv("GNOME_TERMINAL_SCREEN") != "" {
		return "gnome-terminal"
	}
	return "unknown"
}

// detectFromTermProgram maps TERM_PROGRAM values to canonical terminal names.
func detectFromTermProgram(val string) string {
	lower := strings.ToLower(val)
	switch {
	case lower == "ghostty":
		return "ghostty"
	case strings.Contains(lower, "iterm"):
		return "iTerm2"
	case strings.Contains(lower, "apple_terminal") || strings.Contains(lower, "apple terminal"):
		return "Terminal"
	case strings.Contains(lower, "wezterm"):
		return "wezterm"
	default:
		return "unknown"
	}
}

// readParentProcessName fetches the process name of the given PID.
// Tries /proc/<pid>/comm on Linux first, falls back to `ps -o comm= -p <pid>`.
func readParentProcessName(ppid int) string {
	// Linux fast path
	commPath := "/proc/" + strconv.Itoa(ppid) + "/comm"
	if data, err := os.ReadFile(commPath); err == nil {
		return strings.TrimSpace(string(data))
	}

	// Cross-platform fallback via ps
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(ppid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SpawnMode describes how agent terminal windows are created.
type SpawnMode int

const (
	SpawnModeStandard SpawnMode = iota // separate terminal windows (current behavior)
	SpawnModeTmux                      // tmux split-window panes in dedicated yesmem-agents session
)

// isTmuxAvailable returns true when tmux is installed and available in PATH.
// Does NOT require running inside a tmux session — agents always use tmux when available,
// regardless of the calling terminal (ghostty, gnome-terminal, etc.).
func isTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// DetectSpawnMode returns the best available spawn mode.
// Uses tmux when installed (regardless of current terminal), otherwise standard.
func DetectSpawnMode() SpawnMode {
	if isTmuxAvailable() {
		return SpawnModeTmux
	}
	return SpawnModeStandard
}

// EnsureTmuxSession creates the named tmux session if it does not already exist.
// Idempotent: returns nil if the session already exists. Runs with a 5s timeout.
func EnsureTmuxSession(session string) error {
	// Fast-path: session already exists — no-op.
	if err := exec.Command("tmux", "has-session", "-t", session).Run(); err == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session).Run()
}

// TmuxSessionHasClients returns true when at least one terminal client is attached to the tmux session.
// Returns false if the session does not exist, has no clients, or tmux is unavailable.
func TmuxSessionHasClients(session string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "list-clients", "-t", session)
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func normalizeProcessName(proc string) string {
	lower := strings.ToLower(proc)
	switch {
	case lower == "ghostty":
		return "ghostty"
	case lower == "kitty":
		return "kitty"
	case strings.HasPrefix(lower, "gnome-terminal"):
		return "gnome-terminal"
	case lower == "alacritty":
		return "alacritty"
	case strings.HasPrefix(lower, "wezterm"):
		return "wezterm"
	case lower == "xterm":
		return "xterm"
	case lower == "terminal" || lower == "terminal.app":
		return "Terminal"
	case lower == "iterm2" || lower == "iterm.app":
		return "iTerm2"
	default:
		return "unknown"
	}
}
