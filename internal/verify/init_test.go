package verify

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// initRepo is a throwaway project root under the worktree's own temp space.
func initRepo(t *testing.T, name string) string {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "inits"))
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

func writeManifest(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func notesContain(result InitResult, want string) bool {
	for _, note := range result.Notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}

func detectionNamed(t *testing.T, detections []InitDetection, name string) InitDetection {
	t.Helper()
	for _, detection := range detections {
		if detection.Name == name {
			return detection
		}
	}
	t.Fatalf("no detection %q in %+v", name, detections)
	return InitDetection{}
}

// A go.mod is the whole statement needed here: the built-in Go entry is the
// proposal, because it is the entry this repository's own verifications already
// run with, sandbox bridge included.
func TestInitDetectsGoFromItsManifest(t *testing.T) {
	repo := initRepo(t, "detect-go")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")

	detections, notes := detectLanguages(repo, claimtypes.DefaultLanguages())
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	detection := detectionNamed(t, detections, "go")
	if detection.Manifest != "go.mod" {
		t.Errorf("manifest = %q, want go.mod", detection.Manifest)
	}
	builtIn := engine.Language{}
	for _, language := range claimtypes.DefaultLanguages() {
		if language.Name == "go" {
			builtIn = language
		}
	}
	if !reflect.DeepEqual(detection.Language, builtIn) {
		t.Errorf("proposal = %+v, want the built-in go entry %+v", detection.Language, builtIn)
	}
}

// A composer.json without the vendor tree cannot be proved: proposing phpunit
// would produce a profile whose very first run fails. The note names the fix.
func TestInitDetectsPHPOnlyWithItsVendorTree(t *testing.T) {
	repo := initRepo(t, "detect-php")
	writeManifest(t, repo, "composer.json", `{"require-dev":{"phpunit/phpunit":"^10.0"}}`)

	detections, notes := detectLanguages(repo, claimtypes.DefaultLanguages())
	if len(detections) != 0 {
		t.Fatalf("detections = %+v, want none without vendor/bin/phpunit", detections)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "vendor/bin/phpunit") {
		t.Fatalf("notes = %v, want one naming vendor/bin/phpunit", notes)
	}
}

func TestInitDetectsPHPWithItsVendorTree(t *testing.T) {
	repo := initRepo(t, "detect-php-vendor")
	writeManifest(t, repo, "composer.json", `{"require-dev":{"phpunit/phpunit":"^10.0"}}`)
	writeManifest(t, repo, "vendor/bin/phpunit", "#!/bin/sh\nexit 0\n")

	detections, notes := detectLanguages(repo, claimtypes.DefaultLanguages())
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	detection := detectionNamed(t, detections, "php")
	if detection.Language.TestCommand != "vendor/bin/phpunit" {
		t.Errorf("test command = %q, want vendor/bin/phpunit", detection.Language.TestCommand)
	}
}

// A test script is the node statement: npm's own default placeholder is not one,
// and proposing "npm test" for it would write a profile that fails by design.
func TestInitDetectsNodeUsesItsTestScript(t *testing.T) {
	repo := initRepo(t, "detect-node")
	writeManifest(t, repo, "package.json", `{"scripts":{"test":"node --test"}}`)

	detections, notes := detectLanguages(repo, claimtypes.DefaultLanguages())
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
	detection := detectionNamed(t, detections, "node")
	if detection.Language.TestCommand != "npm test" {
		t.Errorf("test command = %q, want npm test", detection.Language.TestCommand)
	}
	if len(detection.Language.TestFilePatterns) == 0 || len(detection.Language.BuildErrorPatterns) == 0 {
		t.Errorf("the node proposal carries no test-file or build-error pattern: %+v", detection.Language)
	}
}

func TestInitDoesNotProposeTheNpmPlaceholderScript(t *testing.T) {
	repo := initRepo(t, "detect-node-placeholder")
	writeManifest(t, repo, "package.json",
		`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`)

	detections, notes := detectLanguages(repo, claimtypes.DefaultLanguages())
	if len(detections) != 0 {
		t.Fatalf("detections = %+v, want none for npm's placeholder script", detections)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "scripts.test") {
		t.Fatalf("notes = %v, want one naming scripts.test", notes)
	}
}

func TestInitDetectsPython(t *testing.T) {
	repo := initRepo(t, "detect-python")
	writeManifest(t, repo, "requirements.txt", "pytest==8.0.0\n")

	detections, _ := detectLanguages(repo, claimtypes.DefaultLanguages())
	detection := detectionNamed(t, detections, "python")
	if detection.Language.TestCommand != "python3 -m pytest -q" {
		t.Errorf("test command = %q", detection.Language.TestCommand)
	}
}

// Nothing recognised is an answer, not a silent success: the note names the
// manifests that were read and the file a human can write instead.
func TestInitReportsAnUnrecognizedStack(t *testing.T) {
	repo := initRepo(t, "detect-nothing")

	result, err := InitProfile(InitOptions{Repo: repo, Prove: func(engine.Language) InitProof {
		t.Fatal("a proof ran for an unrecognised stack")
		return InitProof{}
	}})
	if err != nil {
		t.Fatalf("InitProfile: %v", err)
	}
	if result.Written != "" {
		t.Fatalf("written = %q, want nothing", result.Written)
	}
	if len(result.Detected) != 0 {
		t.Fatalf("detected = %+v, want none", result.Detected)
	}
	for _, want := range []string{"go.mod", "composer.json", "package.json", "pyproject.toml"} {
		if !notesContain(result, want) {
			t.Errorf("notes do not name %q: %v", want, result.Notes)
		}
	}
	if result.Example == "" {
		t.Error("no example of the file a human would write")
	}
	if _, err := os.Stat(filepath.Join(repo, claimtypes.ProfilePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a profile was written for an unrecognised stack")
	}
}

// The proof is the gate: one real run, and only a passing one writes the file.
func TestInitWritesOnlyAfterAPassingProof(t *testing.T) {
	repo := initRepo(t, "proof-pass")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")

	var proofs int
	result, err := InitProfile(InitOptions{
		Repo:   repo,
		TmpDir: filepath.Join(repo, ".yesmem", "tmp"),
		Prove: func(language engine.Language) InitProof {
			proofs++
			return InitProof{Command: language.TestCommand, Exit: 0, Sandboxed: true, Output: "ok  example.com/project"}
		},
	})
	if err != nil {
		t.Fatalf("InitProfile: %v", err)
	}
	if result.Written == "" {
		t.Fatalf("nothing written after a passing proof: %v", result.Notes)
	}
	if proofs != 1 {
		t.Fatalf("the proof ran %d times, want exactly one run", proofs)
	}
	if result.Proven == nil || result.Proven.Exit != 0 || !result.Proven.Sandboxed {
		t.Fatalf("proven = %+v, want the passing sandboxed run", result.Proven)
	}
	if _, err := os.Stat(result.Written); err != nil {
		t.Fatalf("written file does not exist: %v", err)
	}

	resolved, err := claimtypes.ResolveLanguages(repo, claimtypes.DefaultLanguages())
	if err != nil {
		t.Fatalf("the written file does not read back: %v", err)
	}
	for _, language := range resolved {
		if language.Name == "go" && language.TestCommand != "go test ./..." {
			t.Errorf("test command = %q, want the proved go test ./...", language.TestCommand)
		}
	}
}

func TestInitWritesNothingWhenTheProofFails(t *testing.T) {
	repo := initRepo(t, "proof-fail")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")

	result, err := InitProfile(InitOptions{Repo: repo, Prove: func(language engine.Language) InitProof {
		return InitProof{Command: language.TestCommand, Exit: 1, Sandboxed: true, Output: "FAIL"}
	}})
	if err != nil {
		t.Fatalf("InitProfile: %v", err)
	}
	if result.Written != "" {
		t.Fatalf("written = %q, want nothing after a red proof", result.Written)
	}
	if !notesContain(result, "exited 1") {
		t.Errorf("notes do not name the exit code: %v", result.Notes)
	}
	// The proof runs in a clean clone of HEAD: a dependency that is ignored or
	// uncommitted (vendor/, node_modules/, .venv/) is not there, and the reader
	// has to know that before hunting for a broken command.
	if !notesContain(result, "clean clone") {
		t.Errorf("notes do not explain that the proof ran in a clean clone: %v", result.Notes)
	}
	if _, err := os.Stat(filepath.Join(repo, claimtypes.ProfilePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a profile was written although the proof failed")
	}
}

// A sandbox that cannot start is an environment gap, and the file must stay
// unwritten: a profile whose proof never ran is a guess.
func TestInitWritesNothingWhenTheProofCannotStart(t *testing.T) {
	repo := initRepo(t, "proof-nostart")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")

	result, err := InitProfile(InitOptions{Repo: repo, Prove: func(engine.Language) InitProof {
		return InitProof{Reason: "sandbox required but bubblewrap is not available in PATH"}
	}})
	if err != nil {
		t.Fatalf("InitProfile: %v", err)
	}
	if result.Written != "" {
		t.Fatalf("written = %q, want nothing when the proof could not start", result.Written)
	}
	if !notesContain(result, "bubblewrap") {
		t.Errorf("notes do not name the reason: %v", result.Notes)
	}
}

func TestInitRefusesToOverwriteAnExistingProfile(t *testing.T) {
	repo := initRepo(t, "exists")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")
	writeManifest(t, repo, claimtypes.ProfilePath, "languages:\n  - name: go\n    test_command: go test -run T ./...\n")

	_, err := InitProfile(InitOptions{Repo: repo, Prove: func(engine.Language) InitProof {
		t.Fatal("a proof ran although the project already has a profile")
		return InitProof{}
	}})
	if err == nil {
		t.Fatal("InitProfile overwrote an existing profile")
	}
	if !errors.Is(err, ErrProfileExists) {
		t.Fatalf("error = %v, want ErrProfileExists so a caller can tell a refusal from a failure", err)
	}
}

// A pass that did not run in the sandbox proves nothing about the command under
// the verifier: the write is the point where that invariant is decided, not the
// caller's good faith.
func TestInitRefusesAProofThatDidNotRunSandboxed(t *testing.T) {
	repo := initRepo(t, "proof-nosandbox")
	writeManifest(t, repo, "go.mod", "module example.com/project\n\ngo 1.22\n")

	result, err := InitProfile(InitOptions{Repo: repo, Prove: func(language engine.Language) InitProof {
		return InitProof{Command: language.TestCommand, Exit: 0, Sandboxed: false}
	}})
	if err != nil {
		t.Fatalf("InitProfile: %v", err)
	}
	if result.Written != "" {
		t.Fatalf("written = %q, want nothing for an unsandboxed pass", result.Written)
	}
	if !notesContain(result, "sandbox") {
		t.Errorf("notes do not name the missing sandbox: %v", result.Notes)
	}
}
