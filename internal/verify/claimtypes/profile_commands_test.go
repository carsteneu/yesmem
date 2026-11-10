package claimtypes

import (
	"reflect"
	"strings"
	"testing"
)

// Only the file's own commands are the project's word. A built-in entry is the
// fallback the file overrides, not a licence: admitting it would widen a
// caller's narrower allowlist in a repository that has no profile at all.
func TestResolveProfileReturnsOnlyTheFilesCommands(t *testing.T) {
	repo := profileRepo(t, "commandsonly")
	writeRawProfile(t, repo, "languages:\n"+
		"  - name: go\n    test_command: make integration\n    coverage_command: make cover\n")

	resolved, err := ResolveProfile(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if !reflect.DeepEqual(resolved.Commands.Tests, []string{"make integration"}) {
		t.Errorf("test commands = %v, want only the file's entry", resolved.Commands.Tests)
	}
	if !reflect.DeepEqual(resolved.Commands.Coverage, []string{"make cover"}) {
		t.Errorf("coverage commands = %v, want only the file's entry", resolved.Commands.Coverage)
	}

	// The merge itself is unchanged: a language the file does not mention keeps
	// its built-in entry, and that entry is still not a profile command.
	php := languageNamed(t, resolved.Languages, "php")
	if php.TestCommand == "" {
		t.Fatal("the built-in php entry lost its test command")
	}
	for _, command := range resolved.Commands.Tests {
		if command == php.TestCommand {
			t.Errorf("the built-in command %q was reported as a profile command", command)
		}
	}
	if got := languageNamed(t, resolved.Languages, "go").TestCommand; got != "make integration" {
		t.Errorf("go test command = %q, want the file's override", got)
	}
}

// A repository without a profile resolves to the built-ins and admits no extra
// command: that is the unchanged verifier.
func TestResolveProfileWithoutAFileHasNoCommands(t *testing.T) {
	repo := profileRepo(t, "nocommands")

	resolved, err := ResolveProfile(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(resolved.Commands.Tests) != 0 || len(resolved.Commands.Coverage) != 0 {
		t.Errorf("commands = %+v, want none without a profile file", resolved.Commands)
	}
	if !reflect.DeepEqual(resolved.Languages, DefaultLanguages()) {
		t.Errorf("languages = %+v, want the built-ins", resolved.Languages)
	}
}

// Every language the file names contributes its commands: a mixed repository
// keeps both runners, and neither replaces the other.
func TestResolveProfileCollectsEveryNamedLanguage(t *testing.T) {
	repo := profileRepo(t, "twolanguages")
	writeRawProfile(t, repo, "languages:\n"+
		"  - name: php\n    test_command: ddev phpunit\n"+
		"  - name: ruby\n    test_command: bundle exec rspec\n    coverage_command: bundle exec rspec --coverage\n")

	resolved, err := ResolveProfile(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	want := []string{"ddev phpunit", "bundle exec rspec"}
	if !reflect.DeepEqual(resolved.Commands.Tests, want) {
		t.Errorf("test commands = %v, want %v", resolved.Commands.Tests, want)
	}
	if !reflect.DeepEqual(resolved.Commands.Coverage, []string{"bundle exec rspec --coverage"}) {
		t.Errorf("coverage commands = %v, want the ruby entry", resolved.Commands.Coverage)
	}
}

// The git read returns the tail of the stream, not the whole of it. A profile at
// that size was truncated on the way in, and reading a fragment of a document as
// the document is exactly the shape that must fail closed.
func TestCommittedProfileRefusesAFragment(t *testing.T) {
	valid := "languages:\n  - name: go\n    test_command: make integration\n"
	small, _ := makeClaimRepo(t, "smallcommitted", map[string]string{ProfilePath: valid})
	commands, ok := CommittedProfile(small)
	if !ok || len(commands.Tests) != 1 || commands.Tests[0] != "make integration" {
		t.Fatalf("committed commands = %v (ok=%v), want the committed entry", commands.Tests, ok)
	}

	// The truncated tail has to be a readable document, or the test would pass for
	// the wrong reason: a YAML fragment that fails to parse is refused anyway.
	// Padding lines and the document are aligned to the read buffer so the cut
	// falls on a line start.
	padding := "#" + strings.Repeat("p", 62) + "\n" // 64 bytes, a YAML comment
	document := valid + " " + strings.Repeat(" ", 63) // trailing space is ignored
	for len(document)%64 != 0 {
		document += " "
	}
	huge := strings.Repeat(padding, 1024+2) + document
	if len(huge) <= 64<<10 {
		t.Fatalf("the fixture is %d bytes, it must exceed the read buffer", len(huge))
	}

	big, _ := makeClaimRepo(t, "hugecommitted", map[string]string{ProfilePath: huge})
	if commands, ok := CommittedProfile(big); ok || len(commands.Tests) > 0 {
		t.Fatalf("ok=%v commands=%v, want a refusal: the blob exceeds the read buffer",
			ok, commands.Tests)
	}
}
