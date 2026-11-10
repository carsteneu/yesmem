package engine

import (
	"os/exec"
	"testing"
)

// skipWithoutBubblewrap keeps a sandbox test on a host that cannot sandbox.
// The behaviour under test stays correct there (the run is refused), but the
// test's subject is what the sandbox does, and this host has none.
func skipWithoutBubblewrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not in PATH; a sandboxed run cannot happen on this host")
	}
}
