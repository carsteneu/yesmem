package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/setup"
)

// The standard rules live in the installation, and the guard enforces from that copy. Setup writes it,
// but a machine that only ever runs "yesmem update" never re-runs setup — so it kept whatever it had,
// or nothing at all, and a release could never deliver a standard rule. The migration path installs
// it: absent means written, a provably untouched copy is refreshed, an operator-edited one is kept.
//
// runMigrateCmd itself is not testable in-process (it opens the live DB and calls systemctl), so the
// test binds the call site and checks the installer against a temp home — the same approach the
// opencode-commands test takes.
func TestUpdateInstallsTheMasterRules(t *testing.T) {
	source, err := os.ReadFile("cmd_update.go")
	if err != nil {
		t.Fatalf("read cmd_update.go: %v", err)
	}
	if !strings.Contains(string(source), "setup.InstallRULESmd(") {
		t.Error("runMigrateCmd does not call setup.InstallRULESmd: a machine that only updates would never receive the standard rules")
	}

	dataDir := filepath.Join(t.TempDir(), ".claude", "yesmem")
	path, err := setup.InstallRULESmd(dataDir)
	if err != nil {
		t.Fatalf("install into an empty data dir: %v", err)
	}
	if path == "" {
		t.Fatal("an absent master was not written")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the installed master: %v", err)
	}
	// The Decision Policy is not decoration: the guard parses it to narrow the enforceable block set,
	// and without it a fresh installation falls back to the literal table in code.
	if !strings.Contains(string(content), "## Decision Policy") {
		t.Errorf("the installed master carries no Decision Policy:\n%s", content)
	}

	// The second half of the contract: an operator's own file is never replaced.
	own := "1. A rule this operator wrote.\n"
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatalf("write an operator master: %v", err)
	}
	if again, err := setup.InstallRULESmd(dataDir); err != nil || again != "" {
		t.Fatalf("InstallRULESmd = (%q, %v) on an edited master, want it kept", again, err)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back the operator master: %v", err)
	}
	if string(kept) != own {
		t.Errorf("the operator's master was overwritten:\n%s", kept)
	}
}
