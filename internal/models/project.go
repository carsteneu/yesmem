package models

import (
	"path/filepath"
	"strings"
)

// ProjectMatches reports whether two project identifiers refer to the same project.
// Since project identifiers are now full absolute paths (see ProjectShortFromPath),
// a simple equality check is both necessary and sufficient. Earlier fuzzy matching
// (suffix/basename) caused collisions between distinct repos sharing a folder name.
func ProjectMatches(a, b string) bool {
	return a == b
}

// CanonicalProject returns the canonical (parent) project basename for a CWD path.
// If CWD is inside a .worktrees/ directory, returns the parent directory basename.
// Otherwise returns filepath.Base(cwd). Used for worktree→main grouping, NOT path
// disambiguation — deliberately kept separate from ProjectShortFromPath.
func CanonicalProject(cwd string) string {
	if i := strings.Index(cwd, "/.worktrees/"); i >= 0 {
		return filepath.Base(cwd[:i])
	}
	return filepath.Base(cwd)
}

// DisplayName returns a short human-readable label for a project path, consisting
// of the last two path segments (e.g. "ccm19/cookie-consent-management"). Used for
// briefing headers and list_projects output where the full path would be too verbose.
func DisplayName(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	parent := filepath.Dir(cleaned)
	base := filepath.Base(cleaned)
	parentBase := filepath.Base(parent)
	if parentBase == "/" || parentBase == "." || parentBase == base {
		return base
	}
	return parentBase + "/" + base
}

// DisplayNameOfShort returns a display label from any project identifier, accepting
// both full paths and legacy short names. Short names are returned unchanged.
func DisplayNameOfShort(short string) string {
	if short == "" {
		return ""
	}
	if short[0] == '/' {
		return DisplayName(short)
	}
	return short
}
