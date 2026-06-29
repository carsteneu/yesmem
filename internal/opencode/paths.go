// Package opencode holds helpers shared across internal/setup and
// internal/proxy for resolving opencode's config file location.
//
// It exists to avoid an import cycle: internal/setup imports daemon+httpapi,
// internal/proxy imports daemon, and both need to know which opencode config
// file opencode actually reads. Putting the helper in either of those
// packages would force the other into a cycle.
package opencode

import (
	"os"
	"path/filepath"
)

// ConfigPath returns the path to the opencode config file opencode actually
// reads, mirroring opencode's own globalConfigFile() priority:
//
//	opencode.jsonc → opencode.json → config.json
//
// Falls back to opencode.jsonc (opencode's preferred format for fresh
// installs) when none exist. yesmem MUST write to the same file opencode
// reads, otherwise yesmem's provider blocks, MCP config, and model defaults
// are silently ignored. This was the root cause of the "LLM call failed:
// opencode error: exit status 1" bug on fresh opencode installs where only
// opencode.jsonc existed.
//
// The return value is deterministic given the same filesystem state — no
// time randomness, no env vars beyond os.Stat. The function does NOT create
// the file or any directories; callers handle creation.
func ConfigPath(home string) string {
	dir := filepath.Join(home, ".config", "opencode")
	for _, name := range []string{"opencode.jsonc", "opencode.json", "config.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return filepath.Join(dir, name)
		}
	}
	return filepath.Join(dir, "opencode.jsonc")
}
