package daemon

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed SYSTEM.md
var systemPromptTemplate []byte

// EnsureSystemPromptTemplate writes the bundled SYSTEM.md to DataDir if no file exists there.
// Returns true if a new file was created, false if it already existed.
func EnsureSystemPromptTemplate(dataDir string) (bool, error) {
	dst := filepath.Join(dataDir, "SYSTEM.md")
	if _, err := os.Stat(dst); err == nil {
		return false, nil // already exists, don't overwrite
	}
	if err := os.WriteFile(dst, systemPromptTemplate, 0644); err != nil {
		return false, err
	}
	return true, nil
}
