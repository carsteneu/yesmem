package engine

import (
	"strings"
	"testing"
)

// The project's own profile names the command its stack runs. That command needs
// no built-in entry: a PHP project whose suite is `./vendor/bin/phpunit` with a
// custom bootstrap, or a Makefile target no built-in knows, is the project's word
// about itself. The guard is the exact entry, never a family of commands.
func TestPrepareProfileCommandAdmitsExactlyTheProjectCommand(t *testing.T) {
	allowlist := []string{"go test"}
	profile := []string{"make integration"}

	argv, _, verdict := PrepareProfileCommand("make integration", allowlist, profile)
	if verdict != nil {
		t.Fatalf("the project's own command was refused: %s", verdict.Reason)
	}
	if strings.Join(argv, " ") != "make integration" {
		t.Errorf("argv = %v, want the project command's tokens", argv)
	}

	// A command that merely starts with the project's entry is not that entry:
	// an extra argument changes what runs, and only the project's own words are
	// admitted.
	for _, command := range []string{
		"make integration --all",
		"make integration-fast",
		"make integration2",
		"make",
		"./make integration",
	} {
		if _, _, refused := PrepareProfileCommand(command, allowlist, profile); refused == nil {
			t.Errorf("%q was admitted, want a refusal: it is not the profile's exact command", command)
		}
	}
}

// The refusal has to say what the profile would have accepted, or the agent
// retries the same shape and spends another run.
func TestPrepareProfileCommandNamesTheProfileEntry(t *testing.T) {
	_, _, verdict := PrepareProfileCommand("make integration --all", []string{"go test"},
		[]string{"make integration"})
	if verdict == nil {
		t.Fatal("a command extending the profile entry was admitted")
	}
	if !strings.Contains(verdict.Reason, "make integration") {
		t.Errorf("reason = %q, want it to name the profile's command", verdict.Reason)
	}
	if verdict.Class != ClassEnvironment {
		t.Errorf("class = %q, want the allowlist gate's class", verdict.Class)
	}
}

// The built-in allowlist keeps its own behaviour: the profile entry is an
// addition for one claim family, not a replacement.
func TestPrepareProfileCommandKeepsTheAllowlistBehaviour(t *testing.T) {
	allowlist := []string{"go test"}
	profile := []string{"make integration"}

	if _, _, verdict := PrepareProfileCommand("go test -run TestOne ./internal/...", allowlist, profile); verdict != nil {
		t.Errorf("an allowlisted command was refused: %s", verdict.Reason)
	}
	if _, _, verdict := PrepareProfileCommand("curl http://example.invalid", allowlist, profile); verdict == nil {
		t.Error("a command outside both lists was admitted")
	}
}

// A repository without a profile is exactly the old verifier: the empty list
// widens nothing, including for a caller-supplied narrower allowlist.
func TestPrepareProfileCommandWithoutAProfileIsTheOldGate(t *testing.T) {
	allowlist := []string{"go test"}

	for _, command := range []string{"vendor/bin/phpunit", "make integration", "go test ./..."} {
		_, _, withProfile := PrepareProfileCommand(command, allowlist, nil)
		_, _, plain := PrepareCommand(command, allowlist)
		if (withProfile == nil) != (plain == nil) {
			t.Errorf("%q: profile gate and plain gate disagree (profile=%v, plain=%v)",
				command, withProfile, plain)
		}
	}
}
