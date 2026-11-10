package daemon

import (
	"os/exec"
	"testing"
)

// skipWithoutBubblewrap keeps a sandboxed verify test on a host that cannot
// sandbox. The operation's refusal is correct there, but the tests assert what
// happens after the sandbox ran, and this host has none.
func skipWithoutBubblewrap(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not in PATH; a sandboxed claim cannot run on this host")
	}
}
