package claimtypes

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The sandbox bridge is configuration, and a wrong value in it is invisible
// until a run fails: the Go profile must name the module cache through the
// toolchain, keep the build cache inside the run, and give the run a short temp
// directory. A language the entry knows no path for declares nothing, because a
// guessed path would be bound as if it were true.
func TestTheGoProfileDeclaresTheSandboxBridge(t *testing.T) {
	var goProfile *engine.Language
	for _, language := range DefaultLanguages() {
		if language.Name != "go" {
			continue
		}
		found := language
		goProfile = &found
		break
	}
	if goProfile == nil {
		t.Fatal("no go profile")
	}
	if want := []string{engine.ToolEnv("GOMODCACHE")}; len(goProfile.SandboxReadOnly) != 1 ||
		goProfile.SandboxReadOnly[0] != want[0] {
		t.Fatalf("read-only roots = %v, want %v", goProfile.SandboxReadOnly, want)
	}
	for key, want := range map[string]string{
		"GOMODCACHE":  engine.ToolEnv("GOMODCACHE"),
		"GOCACHE":     engine.CacheDirPlaceholder,
		"TMPDIR":      engine.SandboxTmpDir,
		"GOFLAGS":     "-mod=mod",
		"CGO_ENABLED": "0",
	} {
		if got := goProfile.SandboxEnv[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The compiler is the one tool the built-in entry can name for every
	// project: the suite runs it, and a host that keeps it outside the system
	// paths would otherwise see an honest report refuted.
	if len(goProfile.Requires) != 1 || goProfile.Requires[0] != "go" {
		t.Errorf("requires = %v, want [go]", goProfile.Requires)
	}
	if goProfile.SandboxCache != "" {
		t.Errorf("sandbox_cache = %q, want no built-in cache root: a machine-dependent path cannot be built in",
			goProfile.SandboxCache)
	}
	for _, language := range DefaultLanguages() {
		if language.Name == "go" {
			continue
		}
		if len(language.SandboxReadOnly) != 0 || len(language.SandboxEnv) != 0 {
			t.Errorf("%s declares a sandbox path without a toolchain to resolve it: %v %v",
				language.Name, language.SandboxReadOnly, language.SandboxEnv)
		}
	}
}

// The language data is the only place a language is known. Every claim of quality
// reads it, so a profile that misses a field would silently degrade a claim into
// a guess: the fields below are the ones the claim logic depends on.
func TestDefaultLanguagesAreComplete(t *testing.T) {
	languages := DefaultLanguages()
	if len(languages) == 0 {
		t.Fatal("no default language")
	}
	for _, language := range languages {
		if language.Name == "" {
			t.Errorf("%+v: no name", language)
		}
		if len(language.Extensions) == 0 {
			t.Errorf("%s: no source extension", language.Name)
		}
		for _, extension := range language.Extensions {
			if extension == "" || extension[0] != '.' {
				t.Errorf("%s: extension %q does not start with a dot", language.Name, extension)
			}
		}
		if len(language.TestFilePatterns) == 0 {
			t.Errorf("%s: no test-file pattern", language.Name)
		}
		if language.TestCommand == "" {
			t.Errorf("%s: no test command", language.Name)
		}
		if len(language.BuildErrorPatterns) == 0 {
			// Without it a build failure cannot be told from a failing test,
			// and a build failure counted as a kill is a false confirmation.
			t.Errorf("%s: no build-error pattern", language.Name)
		}
		if language.CoverageCommand != "" {
			if language.CoverageFormat == "" || language.CoverageArtifact == "" {
				t.Errorf("%s: a coverage command without format or artifact proves nothing",
					language.Name)
			}
		}
	}
}

// Selection is by extension, and a test file is not a production line.
func TestLanguageForFile(t *testing.T) {
	rules := compileLanguages(DefaultLanguages())

	cases := []struct {
		name     string
		wantName string
		wantTest bool
	}{
		{name: "internal/guard/guard.go", wantName: "go"},
		{name: "internal/guard/guard_test.go", wantName: "go", wantTest: true},
		{name: "src/Service/ThingTest.php", wantName: "php", wantTest: true},
		{name: "src/Service/Thing.php", wantName: "php"},
		{name: "pkg/test_thing.py", wantName: "python", wantTest: true},
		{name: "pkg/thing_test.py", wantName: "python", wantTest: true},
		{name: "pkg/thing.py", wantName: "python"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rulesFor, ok := rules.forFile(test.name)
			if !ok {
				t.Fatalf("no language for %s", test.name)
			}
			if rulesFor.language.Name != test.wantName {
				t.Fatalf("language = %q, want %q", rulesFor.language.Name, test.wantName)
			}
			if got := rulesFor.isTestFile(test.name); got != test.wantTest {
				t.Errorf("isTestFile = %v, want %v", got, test.wantTest)
			}
		})
	}
}

// A file no profile covers is not silently a production line: the claim reports
// it instead of guessing a language for it.
func TestLanguageForFileUnknown(t *testing.T) {
	rules := compileLanguages(DefaultLanguages())
	for _, name := range []string{"README.md", "Makefile", "src/main.rb", "a.go.txt"} {
		if _, ok := rules.forFile(name); ok {
			t.Errorf("%q resolved to a language", name)
		}
	}
}
