package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// macOS makes /tmp and /var/tmp symlinks into /private. The paths reaching the
// mount check are resolved, so a comparison against the literal roots missed
// them and a profile could re-expose a masked tree.
func TestMaskedHostPathSeesThroughASymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "masked")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(dir, "tmp")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	// The resolved form of the link must still count as masked when the link
	// itself is one of the roots we mask.
	saved := maskedHostDirs
	maskedHostDirs = []string{link}
	t.Cleanup(func() { maskedHostDirs = saved })

	if !maskedHostPath(filepath.Join(target, "cache")) {
		t.Fatalf("resolved path under %q was not seen as masked", link)
	}
	if !maskedHostPath(link) {
		t.Fatalf("the root %q itself was not seen as masked", link)
	}
	if maskedHostPath(filepath.Join(dir, "elsewhere")) {
		t.Fatalf("a path outside the roots was treated as masked")
	}
}
