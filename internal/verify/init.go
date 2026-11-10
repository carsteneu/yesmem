package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// InitDetection is one stack the manifests named, with the file that named it.
type InitDetection struct {
	Name     string
	Manifest string
	Language engine.Language
}

// InitProof is the one real run that decides whether a proposal is written.
//
// A proof that never started carries a Reason and no exit: the difference
// matters, because a sandbox that cannot start says nothing about the command.
type InitProof struct {
	Command   string
	Exit      int
	Sandboxed bool
	Output    string
	Reason    string
}

// InitResult is what one init did, in the order a reader needs it.
type InitResult struct {
	// Detected is every stack the manifests named, in detection order. Only the
	// first is proved.
	Detected []InitDetection
	// Proven is the run that decided the outcome, nil when no stack was found.
	Proven *InitProof
	// Written is the profile's path, empty when nothing was written.
	Written string
	// Notes are what a human has to know: what was skipped, why a proof failed,
	// and how to add a profile by hand.
	Notes []string
	// Example is a minimal file to copy when nothing was detected.
	Example string
}

// InitOptions is one init run.
type InitOptions struct {
	Repo string
	// TmpDir holds the throwaway checkout of the proof run.
	TmpDir string
	// Base is the built-in language data a proposal starts from. Nil means the
	// defaults.
	Base []engine.Language
	// Budget bounds the whole init; zero means InitBudget.
	Budget time.Duration
	// Prove is the proof run. Nil means the sandboxed one. It is a seam for the
	// tests: a unit test must not start a sandbox.
	Prove func(engine.Language) InitProof
}

const (
	// InitBudget bounds a whole init run.
	InitBudget = 15 * time.Minute
	// initProofTimeout bounds the one proof run. It is longer than the gate's
	// per-command limit because a first run in a cold checkout compiles
	// everything, and init is interactive: a human waits for it, the gate does
	// not.
	initProofTimeout = 10 * time.Minute
	// maxManifestBytes bounds a manifest this init reads. A file larger than
	// this is not a manifest, and reading it whole would be an unbounded read
	// out of repository content.
	maxManifestBytes = 1 << 20
)

// profileExample is the shape a human can write by hand when auto-detection
// found nothing. It is the same file the writer produces, for a Go project.
const profileExample = `languages:
  - name: go
    source_extensions: [".go"]
    test_file_patterns: ["_test\\.go$"]
    test_command: go test ./...
`

// ErrProfileExists is the init's refusal: the project already has a profile, and
// init does not replace one. It is a sentinel so a caller can tell a refusal
// (nothing to do) from a failure (nothing written).
var ErrProfileExists = errors.New("the project already has a verify profile")

// InitProfile makes a project's first profile: it detects the stack from the
// manifests, proves the proposal with one real run in the sandbox, and writes the
// file only if that run passed.
//
// Nothing here is guessed into a file. A stack the manifests do not name, a
// command that does not pass, a sandbox that does not start: each one leaves the
// repository without a profile and the reader with the reason.
func InitProfile(options InitOptions) (InitResult, error) {
	result := InitResult{Example: profileExample}
	if options.Repo == "" {
		return result, errors.New("no repository to initialise")
	}
	profilePath := filepath.Join(options.Repo, claimtypes.ProfilePath)
	switch _, err := os.Stat(profilePath); {
	case err == nil:
		return result, fmt.Errorf("%w: %s already exists; edit it or remove it and run init again",
			ErrProfileExists, claimtypes.ProfilePath)
	case !errors.Is(err, os.ErrNotExist):
		return result, fmt.Errorf("%s cannot be checked: %v", claimtypes.ProfilePath, err)
	}

	base := options.Base
	if len(base) == 0 {
		base = claimtypes.DefaultLanguages()
	}
	detections, notes := detectLanguages(options.Repo, base)
	result.Detected = detections
	result.Notes = append(result.Notes, notes...)
	if len(detections) == 0 {
		result.Notes = append(result.Notes, fmt.Sprintf(
			"nothing was detected: no go.mod, no composer.json with vendor/bin/phpunit, "+
				"no package.json with a test script, no pyproject.toml and no requirements.txt. "+
				"Write %s by hand, for example:", claimtypes.ProfilePath))
		return result, nil
	}
	if len(detections) > 1 {
		result.Notes = append(result.Notes, fmt.Sprintf(
			"%d stacks were detected; the proof run judges the first (%s). "+
				"Add the others to %s by hand once their commands are proven.",
			len(detections), detections[0].Name, claimtypes.ProfilePath))
	}

	prove := options.Prove
	if prove == nil {
		prove = func(language engine.Language) InitProof {
			return proveInSandbox(options.Repo, options.TmpDir, language, options.Budget)
		}
	}
	chosen := detections[0]
	proof := prove(chosen.Language)
	result.Proven = &proof
	switch {
	case proof.Reason != "":
		result.Notes = append(result.Notes, fmt.Sprintf(
			"the proof run of %q could not start: %s; nothing was written",
			chosen.Language.TestCommand, proof.Reason))
		return result, nil
	case proof.Exit != 0:
		result.Notes = append(result.Notes, fmt.Sprintf(
			"the proof run of %q exited %d; nothing was written. The run happens in a clean clone of HEAD, "+
				"so dependencies that are ignored or uncommitted (vendor/, node_modules/, .venv/) are not there. "+
				"Fix the command, or write %s by hand.",
			chosen.Language.TestCommand, proof.Exit, claimtypes.ProfilePath))
		return result, nil
	case !proof.Sandboxed:
		// The write is where the invariant is decided: a pass outside the sandbox
		// says nothing about the run the verifier will make, and a profile written
		// from it would be trusted as if it had been proven.
		result.Notes = append(result.Notes, fmt.Sprintf(
			"the proof run of %q exited 0 but did not run in the sandbox; "+
				"an unsandboxed pass proves nothing about the verifier's run, so nothing was written",
			chosen.Language.TestCommand))
		return result, nil
	}

	written, err := claimtypes.WriteProfileFile(options.Repo, []engine.Language{chosen.Language})
	if err != nil {
		return result, err
	}
	result.Written = written
	return result, nil
}

// detectLanguages derives the proposal from the manifests, in a fixed order, and
// says what it skipped and why.
//
// The built-in entry is the proposal wherever one exists: it is the entry this
// project's verifications already run with, so a profile written from it changes
// nothing that was already proven. Only node has no built-in entry, and its
// patterns are the ones its runner is named by.
func detectLanguages(repo string, base []engine.Language) (detections []InitDetection, notes []string) {
	if fileExists(filepath.Join(repo, "go.mod")) {
		if language, ok := builtInNamed(base, "go"); ok {
			detections = append(detections, InitDetection{Name: "go", Manifest: "go.mod", Language: language})
		}
	}

	if fileExists(filepath.Join(repo, "composer.json")) {
		switch {
		case !fileExists(filepath.Join(repo, "vendor", "bin", "phpunit")):
			notes = append(notes, "php (composer.json): vendor/bin/phpunit is missing in the checkout, "+
				"so the suite cannot run. Commit vendor/ (composer install) and run init again, "+
				"or write the command by hand.")
		default:
			if language, ok := builtInNamed(base, "php"); ok {
				detections = append(detections, InitDetection{Name: "php", Manifest: "composer.json", Language: language})
			}
		}
	}

	if fileExists(filepath.Join(repo, "package.json")) {
		script, problem := packageTestScript(filepath.Join(repo, "package.json"))
		switch {
		case problem != "":
			notes = append(notes, "node (package.json): "+problem+"; nothing is proposed for it")
		case script == "":
			notes = append(notes, "node (package.json): no scripts.test, so there is no suite to prove. "+
				"Add a test script, or write the command by hand.")
		case strings.Contains(script, "no test specified"):
			// npm writes this placeholder into every new package.json, and it
			// exits non-zero by design: proposing "npm test" for it would write
			// a profile that can only fail.
			notes = append(notes, "node (package.json): scripts.test is npm's placeholder "+
				"(\"no test specified\"), which is not a suite. Replace it, or write the command by hand.")
		default:
			detections = append(detections, InitDetection{Name: "node", Manifest: "package.json", Language: nodeLanguage()})
		}
	}

	if manifest := firstExisting(repo, "pyproject.toml", "requirements.txt", "setup.py"); manifest != "" {
		if language, ok := builtInNamed(base, "python"); ok {
			detections = append(detections, InitDetection{Name: "python", Manifest: manifest, Language: language})
		}
	}
	return detections, notes
}

// nodeLanguage is the proposal for a package.json project. The test command is
// npm's own door so the script under test is the project's, not one this init
// picked. Coverage stays empty: which coverage tool a node project uses is not in
// the manifest, and a guessed command would be a claim nobody made.
func nodeLanguage() engine.Language {
	return engine.Language{
		Name:             "node",
		Extensions:       []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx"},
		TestFilePatterns: []string{`\.(?:test|spec)\.[cm]?[jt]sx?$`},
		TestCommand:      "npm test",
		BuildErrorPatterns: []string{
			`(?m)^SyntaxError:`,
			`(?i)\berror TS\d+:`,
		},
	}
}

// packageTestScript reads scripts.test. A file this init cannot read is a
// problem for the reader, not a reason to propose a command the file never named.
func packageTestScript(path string) (script, problem string) {
	file, err := os.Open(path)
	if err != nil {
		return "", "the file cannot be read: " + err.Error()
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return "", "the file cannot be read: " + err.Error()
	}
	if len(data) > maxManifestBytes {
		return "", fmt.Sprintf("the file is larger than %d bytes, which is not a manifest", maxManifestBytes)
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "the file is not valid JSON: " + err.Error()
	}
	return strings.TrimSpace(manifest.Scripts["test"]), ""
}

func builtInNamed(languages []engine.Language, name string) (engine.Language, bool) {
	for _, language := range languages {
		if language.Name == name {
			return language, true
		}
	}
	return engine.Language{}, false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstExisting(repo string, names ...string) string {
	for _, name := range names {
		if fileExists(filepath.Join(repo, name)) {
			return name
		}
	}
	return ""
}

// proveInSandbox runs the proposal's test command once, in a throwaway checkout,
// under the same sandbox and the same allowlist the verification uses. The run is
// the whole point: a profile whose command was never run is a guess.
func proveInSandbox(repo, tmpDir string, language engine.Language, budget time.Duration) InitProof {
	proof := InitProof{Command: language.TestCommand}
	if tmpDir == "" {
		proof.Reason = "no working directory for the proof run"
		return proof
	}
	if budget <= 0 {
		budget = InitBudget
	}
	head := engine.Head(repo)
	if head == "" {
		proof.Reason = "the repository has no commit to run: the verifier inspects a checkout, " +
			"so init needs a git repository"
		return proof
	}
	ctx := &engine.Ctx{
		Repo:      repo,
		TmpDir:    tmpDir,
		Sandbox:   engine.SandboxRequire,
		Allowlist: engine.DefaultCommandAllowlist,
		Deadline:  time.Now().Add(budget),
		Languages: []engine.Language{language},
	}
	argv, _, problem := engine.PrepareCommand(language.TestCommand, ctx.Allowlist)
	if problem != nil {
		proof.Reason = problem.Reason
		return proof
	}
	checkout, failure := ctx.NewCheckout("init", head)
	if failure != nil {
		proof.Reason = failure.Reason
		return proof
	}
	defer checkout.Close()

	outcome, problem := ctx.RunInDirWithin(checkout.Dir, language.TestCommand, argv, initProofTimeout)
	if problem != nil {
		proof.Reason = problem.Reason
		return proof
	}
	proof.Exit = outcome.Code
	proof.Sandboxed = outcome.Sandboxed
	// The raw tail, not the five-line display an ordinary verdict carries: a
	// failed proof has to say which package failed, and the last five lines of a
	// suite run are the summary, not the cause.
	proof.Output = outcome.RawOutput
	return proof
}
