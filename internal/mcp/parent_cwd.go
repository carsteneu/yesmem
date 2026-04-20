package mcp

import "os"

// callerCWD returns the current working directory of the parent process
// (the Claude Code CLI that spawned this MCP server). Claude Code updates
// its process CWD when the user `cd`s inside a running session, but the
// MCP server's own CWD stays fixed at spawn time. Reading the parent's
// /proc (Linux) or lsof (macOS) view gives the user's actual location.
// Falls back to os.Getwd() if the parent CWD cannot be determined.
func callerCWD() string {
	if cwd, ok := processCWD(os.Getppid()); ok && cwd != "" {
		return cwd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}
