package claimtypes

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// profileRepo is a throwaway project root under the worktree's own temp space.
// Nothing here writes to /tmp.
func profileRepo(t *testing.T, name string) string {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "profiles"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	return root
}

// writeRawProfile writes the file verbatim: a schema test needs bytes the writer
// would never produce.
func writeRawProfile(t *testing.T, repo, body string) {
	t.Helper()
	path := filepath.Join(repo, ProfilePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

func languageNamed(t *testing.T, languages []engine.Language, name string) engine.Language {
	t.Helper()
	for _, language := range languages {
		if language.Name == name {
			return language
		}
	}
	t.Fatalf("no language %q in %+v", name, languages)
	return engine.Language{}
}

// The project file is the project's word about its own stack. A field it names
// replaces the built-in one; a field it does not name keeps the built-in value,
// because the built-in entry is the fallback the project is overriding, not a
// second source that has to be repeated in full.
func TestProjectProfileOverridesTheBuiltIn(t *testing.T) {
	repo := profileRepo(t, "override")
	writeRawProfile(t, repo, "languages:\n"+
		"  - name: go\n"+
		"    test_command: go test -run 'TestProject' ./...\n")

	resolved, err := ResolveLanguages(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	builtIn := languageNamed(t, DefaultLanguages(), "go")
	overridden := languageNamed(t, resolved, "go")

	if got, want := overridden.TestCommand, "go test -run 'TestProject' ./..."; got != want {
		t.Errorf("test command = %q, want the project's %q", got, want)
	}
	if got, want := overridden.CoverageCommand, builtIn.CoverageCommand; got != want {
		t.Errorf("coverage command = %q, want the built-in %q", got, want)
	}
	if got, want := overridden.Extensions, builtIn.Extensions; !reflect.DeepEqual(got, want) {
		t.Errorf("extensions = %v, want the built-in %v", got, want)
	}
	if got, want := overridden.SandboxEnv, builtIn.SandboxEnv; !reflect.DeepEqual(got, want) {
		t.Errorf("sandbox env = %v, want the built-in %v", got, want)
	}
}

// Without a file nothing changes: the built-in entries are what Go has run with
// so far, and a project that never wrote a profile must not notice this feature.
func TestWithoutAProfileFileTheBuiltInsStay(t *testing.T) {
	repo := profileRepo(t, "nofile")

	resolved, err := ResolveLanguages(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveLanguages without a file: %v", err)
	}
	if !reflect.DeepEqual(resolved, DefaultLanguages()) {
		t.Fatalf("resolved languages changed without a file:\n got %+v\nwant %+v",
			resolved, DefaultLanguages())
	}
}

// A field neither the file nor the built-in fills stays empty. An empty field is
// not a zero-value default: the claim that needs it reports an environment gap.
func TestAFieldInNeitherPlaceStaysEmpty(t *testing.T) {
	repo := profileRepo(t, "empty")
	writeRawProfile(t, repo, "languages:\n"+
		"  - name: ruby\n"+
		"    source_extensions: [\".rb\"]\n"+
		"    test_file_patterns: [\"_test\\\\.rb$\"]\n")

	resolved, err := ResolveLanguages(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	ruby := languageNamed(t, resolved, "ruby")
	if ruby.TestCommand != "" {
		t.Errorf("test command = %q, want empty", ruby.TestCommand)
	}
	if ruby.CoverageCommand != "" || ruby.CoverageFormat != "" || ruby.CoverageArtifact != "" {
		t.Errorf("coverage triple = (%q, %q, %q), want empty",
			ruby.CoverageCommand, ruby.CoverageFormat, ruby.CoverageArtifact)
	}
	if _, ok := compileLanguages(resolved).forFile("lib/app.rb"); !ok {
		t.Fatal("the project's own language does not cover its extension")
	}
}

// The consequence at the claim: a language the profile fills with nothing to
// measure is an environment gap, never a confirmation. This is the third arm of
// the precedence, checked where it decides a verdict.
func TestALanguageWithNothingToMeasureIsAnEnvironmentGap(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "profile-ruby",
		map[string]string{"lib/impl.rb": "limit=2\n", "run.sh": "#!/bin/sh\nexit 0\n"},
		map[string]string{"lib/impl.rb": "limit=1\n"},
	)
	writeRawProfile(t, repo, "languages:\n"+
		"  - name: ruby\n"+
		"    source_extensions: [\".rb\"]\n")

	resolved, err := ResolveLanguages(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base, resolved...))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// A broken file is loud. It names the file and the field, and the caller must be
// able to tell that from "no profile": a silent fallback to the built-in entry
// would hide the project's own word behind a default it never asked for.
func TestASchemaErrorIsLoudAndNamesTheFile(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown field",
			body: "languages:\n  - name: go\n    test_commnd: go test ./...\n",
			want: "test_commnd",
		},
		{
			name: "languages is not a list",
			body: "languages: go\n",
			want: "languages",
		},
		{
			name: "a language without a name",
			body: "languages:\n  - test_command: go test ./...\n",
			want: "name",
		},
		{
			name: "no language at all",
			body: "# nothing here\n",
			want: "language",
		},
		{
			name: "unreadable yaml",
			body: "languages: [\n",
			want: "yaml",
		},
		{
			// An explicitly empty list is not "nothing to say": it would make
			// every file a production line, test files included.
			name: "empty test file patterns",
			body: "languages:\n  - name: ruby\n    test_file_patterns: []\n",
			want: "test_file_patterns",
		},
		{
			name: "empty source extensions",
			body: "languages:\n  - name: ruby\n    source_extensions: []\n",
			want: "source_extensions",
		},
		{
			name: "an invalid pattern",
			body: "languages:\n  - name: ruby\n    build_error_patterns: [\"(\"]\n",
			want: "build_error_patterns",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repo := profileRepo(t, "schema")
			writeRawProfile(t, repo, test.body)

			_, err := ResolveLanguages(repo, DefaultLanguages())
			if err == nil {
				t.Fatal("a broken profile file resolved as if it were valid")
			}
			if !strings.Contains(err.Error(), ProfilePath) {
				t.Errorf("error does not name %s: %v", ProfilePath, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Errorf("error does not mention %q: %v", test.want, err)
			}
		})
	}
}

// A profile is read on the verifier's single slot, so its size is bounded like
// the report's: a committed file must not be able to make the daemon allocate
// without limit.
func TestAProfileFileOverTheLimitIsRefused(t *testing.T) {
	repo := profileRepo(t, "toobig")
	body := "languages:\n  - name: ruby\n    source_extensions: [\".rb\"]\n# " +
		strings.Repeat("x", MaxProfileBytes) + "\n"
	writeRawProfile(t, repo, body)

	_, err := ResolveLanguages(repo, DefaultLanguages())
	if err == nil {
		t.Fatal("an oversized profile file resolved")
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error does not name the size limit: %v", err)
	}
}

// A profile that is a symlink is refused like one that points out of the
// repository: following it would read a host path from repository content, and a
// dangling link would silently read as "no profile" and fall back to the
// built-ins.
func TestAProfileThatIsASymlinkIsRefused(t *testing.T) {
	repo := profileRepo(t, "link")
	target := filepath.Join(profileRepo(t, "link-target"), "profile.yaml")
	if err := os.WriteFile(target, []byte("languages:\n  - name: ruby\n    source_extensions: [\".rb\"]\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(repo, ProfilePath)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := ResolveLanguages(repo, DefaultLanguages())
	if err == nil {
		t.Fatal("a symlinked profile resolved as if it were a regular file")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("error = %v, want it to name the file type", err)
	}
}

// The write must not follow a committed symlink: .yesmem may be a link to any
// directory the operator can write, and a profile named verify.yaml would then
// be created outside the repository.
func TestWriteProfileFileRefusesASymlinkedDirectory(t *testing.T) {
	repo := profileRepo(t, "symlink")
	outside := profileRepo(t, "symlink-outside")
	if err := os.Symlink(outside, filepath.Join(repo, ".yesmem")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := WriteProfileFile(repo, DefaultLanguages()); err == nil {
		t.Fatal("WriteProfileFile wrote through a symlinked .yesmem")
	}
	if _, err := os.Stat(filepath.Join(outside, "verify.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the profile was created outside the repository")
	}
}

// Writing is the init's last step, and it never replaces a file the project
// already has: the profile is a decision, not a generated artifact.
func TestWriteProfileFileRefusesToOverwrite(t *testing.T) {
	repo := profileRepo(t, "overwrite")
	writeRawProfile(t, repo, "languages:\n  - name: go\n    test_command: go test ./...\n")

	if _, err := WriteProfileFile(repo, DefaultLanguages()); err == nil {
		t.Fatal("WriteProfileFile replaced an existing profile")
	}
}

// What the writer writes is what the reader reads: every field the built-ins
// carry survives the file, including the sandbox bridge and the patterns a
// quality claim needs.
func TestWriteProfileFileRoundTrips(t *testing.T) {
	repo := profileRepo(t, "roundtrip")
	written := engine.Language{
		Name:               "node",
		Extensions:         []string{".js", ".mjs"},
		TestFilePatterns:   []string{`\.test\.mjs$`},
		TestCommand:        "npm test",
		CoverageCommand:    "npm test -- --coverage",
		CoverageFormat:     "cobertura",
		CoverageArtifact:   ".yesmem-coverage.xml",
		BuildErrorPatterns: []string{`(?m)^SyntaxError:`},
		SandboxReadOnly:    []string{"/opt/node-cache"},
		SandboxEnv:         map[string]string{"NODE_OPTIONS": "--no-warnings"},
	}

	path, err := WriteProfileFile(repo, []engine.Language{written})
	if err != nil {
		t.Fatalf("WriteProfileFile: %v", err)
	}
	if want := filepath.Join(repo, ProfilePath); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	resolved, err := ResolveLanguages(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	read := languageNamed(t, resolved, "node")
	if !reflect.DeepEqual(read, written) {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", read, written)
	}
}
