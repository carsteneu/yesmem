package mcp

import "os"

// callerCWD returns the working directory to use for _cwd injection.
//
// For Claude sessions, this is the parent process CWD (the TUI tracks the
// user's `cd` in real time). For OpenCode sessions, the parent/TUI CWD
// can be stale across restarts (TUI PID keeps a since-renamed directory),
// so prefer the MCP server's own CWD which tracks the actual workspace.
// Falls back to os.Getwd() if the primary source is unavailable.
func callerCWD(sourceAgent string) string {
	if sourceAgent == "opencode" {
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			return cwd
		}
	}
	if cwd, ok := processCWD(os.Getppid()); ok && cwd != "" {
		return cwd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}
