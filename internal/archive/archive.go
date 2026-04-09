package archive

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Archiver copies JSONL session files to a permanent archive directory.
// Protects against Claude Code's 30-day cleanup (cleanupPeriodDays).
type Archiver struct {
	baseDir string // e.g. ~/.claude/yesmem/archive/
}

// New creates an archiver targeting the given base directory.
func New(baseDir string) *Archiver {
	return &Archiver{baseDir: baseDir}
}

// ArchiveFile copies a JSONL file into the archive under the given project subdirectory.
// Uses the original filename (session UUID) for deduplication.
func (a *Archiver) ArchiveFile(srcPath, project string) error {
	// Verify source exists
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", srcPath, err)
	}

	// Create project subdirectory
	destDir := filepath.Join(a.baseDir, project)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	destPath := filepath.Join(destDir, filepath.Base(srcPath))

	// Check if archive already has same-size copy (skip if identical)
	if destInfo, err := os.Stat(destPath); err == nil {
		if destInfo.Size() == srcInfo.Size() {
			return nil // already archived, same size
		}
	}

	// Copy file
	return copyFile(srcPath, destPath)
}

// BaseDir returns the archive base directory.
func (a *Archiver) BaseDir() string {
	return a.baseDir
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
