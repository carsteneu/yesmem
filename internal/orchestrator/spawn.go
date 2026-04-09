package orchestrator

import (
	"fmt"
	"runtime"
	"strings"
)

// BuildSpawnCommand returns the binary and args to spawn a new terminal running innerCmd with optional args.
// title sets the terminal window title via ANSI escape (empty = no title).
// On Linux, commands are wrapped in bash -ic to inherit the user's .bashrc settings.
func BuildSpawnCommand(terminal, innerCmd, title string, innerArgs ...string) (bin string, args []string) {
	shellCmd := buildShellCmd(innerCmd, innerArgs, title)

	switch terminal {
	case "ghostty":
		return "ghostty", []string{"-e", "bash", "-ic", shellCmd}
	case "kitty":
		return "kitty", []string{"bash", "-ic", shellCmd}
	case "gnome-terminal":
		return "gnome-terminal", []string{"--wait", "--", "bash", "-ic", shellCmd}
	case "alacritty":
		return "alacritty", []string{"-e", "bash", "-ic", shellCmd}
	case "wezterm":
		return "wezterm", []string{"start", "--", "bash", "-ic", shellCmd}
	case "xterm":
		return "xterm", []string{"-e", "bash", "-ic", shellCmd}
	case "iTerm2":
		return "open", append([]string{"-na", "iTerm", "--args", innerCmd}, innerArgs...)
	case "Terminal":
		return "open", append([]string{"-na", "Terminal", "--args", innerCmd}, innerArgs...)
	case "tmux":
		// Always targets a dedicated yesmem-agents session — works from any terminal,
		// even when the caller is not running inside tmux. Creates session if it doesn't exist.
		ensure := "tmux new-session -d -s yesmem-agents 2>/dev/null"
		hook := "tmux set-hook -t yesmem-agents pane-exited 'select-layout tiled'"
		split := "tmux split-window -t yesmem-agents -d " + shellQuote("bash -ic "+shellCmd)
		layout := "tmux select-layout -t yesmem-agents tiled"
		return "sh", []string{"-c", ensure + " ; " + hook + " ; " + split + " ; " + layout}
	default:
		return unknownFallback(innerCmd, innerArgs...)
	}
}

// buildShellCmd constructs a properly quoted shell command string for bash -ic.
// If title is non-empty, sets the terminal window title via ANSI escape sequence.
func buildShellCmd(cmd string, args []string, title string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, shellQuote(cmd))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	titleEsc := ""
	if title != "" {
		titleEsc = fmt.Sprintf("echo -ne '\\033]0;%s\\007'; ", title)
	}
	return ". ~/.bashrc 2>/dev/null; " + titleEsc + "exec " + strings.Join(parts, " ")
}

// shellQuote wraps a string in single quotes, escaping embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// unknownFallback returns a platform-appropriate terminal spawn command.
func unknownFallback(innerCmd string, innerArgs ...string) (bin string, args []string) {
	if runtime.GOOS == "darwin" {
		return "open", append([]string{"-na", "Terminal", "--args", innerCmd}, innerArgs...)
	}
	shellCmd := buildShellCmd(innerCmd, innerArgs, "")
	return "x-terminal-emulator", []string{"-e", "bash", "-ic", shellCmd}
}

// BuildAttachCommand returns the command to open a terminal window attached to a tmux session.
// Used to make the yesmem-agents session visible when no client is already attached.
func BuildAttachCommand(terminal, session string) (string, []string) {
	attach := []string{"tmux", "attach", "-t", session}
	switch terminal {
	case "ghostty":
		return "ghostty", append([]string{"--fullscreen=true", "-e"}, attach...)
	case "kitty":
		return "kitty", attach
	case "alacritty":
		return "alacritty", append([]string{"-e"}, attach...)
	case "wezterm":
		return "wezterm", append([]string{"start", "--"}, attach...)
	case "gnome-terminal":
		return "gnome-terminal", append([]string{"--"}, attach...)
	default:
		return "sh", []string{"-c", "tmux attach -t " + shellQuote(session) + " &"}
	}
}
