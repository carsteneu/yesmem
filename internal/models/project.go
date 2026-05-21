package models

import (
	"path/filepath"
	"strings"
)

// ProjectMatches checks if two project paths refer to the same project.
// Supports exact match, suffix match (one path is a suffix of the other),
// and basename match.
func ProjectMatches(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a) {
		return true
	}
	return filepath.Base(a) == filepath.Base(b)
}

// CanonicalProject returns the canonical (parent) project basename for a CWD path.
// If CWD is inside a .worktrees/ directory, returns the parent directory basename.
// Otherwise returns filepath.Base(cwd).
func CanonicalProject(cwd string) string {
	if i := strings.Index(cwd, "/.worktrees/"); i >= 0 {
		return filepath.Base(cwd[:i])
	}
	return filepath.Base(cwd)
}
