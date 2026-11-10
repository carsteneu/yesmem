package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// sandboxTestRoot is a fixture root outside the trees the sandbox masks, with
// HOME moved into it.
//
// t.TempDir() lives under the host's /tmp, and /tmp is one of the masked roots:
// a cache fixture there is now a refused mount, so a test that used it would
// prove the refusal instead of the behaviour it is about. The worktree itself
// lives under the operator's home, so HOME has to move too: otherwise every
// fixture path would read as a cache under the home, which only a toolchain may
// name.
func sandboxTestRoot(t *testing.T) string {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", "..", ".yesmem", "tmp", "engine"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", parent, err)
	}
	root, err := os.MkdirTemp(parent, "root-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	return root
}
