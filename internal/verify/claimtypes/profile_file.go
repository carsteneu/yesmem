package claimtypes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// ProfilePath is where a project states its own verify profile, relative to the
// repository root.
//
// The file is the project's word about its own stack. It is read at the
// consumer, next to the built-in entries it overrides, and it may only narrow or
// correct them: a field it names replaces the built-in one, and a field neither
// side fills stays empty, which the claim that needs it reports as an
// environment gap.
const ProfilePath = ".yesmem/verify.yaml"

// MaxProfileBytes bounds the profile file. It is read on the verifier's single
// slot before any claim runs, so an unbounded read would be an unbounded
// allocation out of repository content; the report has the same ceiling.
const MaxProfileBytes = 1 << 20

// profileFile is the file's document shape. The pointer fields are what makes
// "absent" and "empty" two different statements: absent keeps the built-in
// value, an explicit empty value removes it.
type profileFile struct {
	Languages []profileLanguage `yaml:"languages"`
}

type profileLanguage struct {
	Name              string             `yaml:"name"`
	SourceExtensions  *[]string          `yaml:"source_extensions"`
	TestFilePatterns  *[]string          `yaml:"test_file_patterns"`
	TestCommand       *string            `yaml:"test_command"`
	CoverageCommand   *string            `yaml:"coverage_command"`
	CoverageFormat    *string            `yaml:"coverage_format"`
	CoverageArtifact  *string            `yaml:"coverage_artifact"`
	BuildErrorPattern *[]string          `yaml:"build_error_patterns"`
	SandboxReadOnly   *[]string          `yaml:"sandbox_readonly"`
	SandboxEnv        *map[string]string `yaml:"sandbox_env"`
	Requires          *[]string          `yaml:"requires"`
	SandboxCache      *string            `yaml:"sandbox_cache"`
}

// ProfileCommands are the commands the project's own file states, split by the
// claim family that may run them. A file that names no command leaves the list
// empty, which widens nothing.
type ProfileCommands struct {
	Tests    []string
	Coverage []string
}

// CommittedProfile reads the commands from the profile the commits carry.
//
// The bytes come from the commit, never from the file beside it. A command right
// has to be bound to the content that is judged: a worktree file can be edited, or
// hidden from git (assume-unchanged, skip-worktree, a clean filter), between the
// moment it is read and the moment the command runs.
func CommittedProfile(repo string) (ProfileCommands, bool) {
	if repo == "" {
		return ProfileCommands{}, false
	}
	ctx := &engine.Ctx{Repo: repo}
	result := ctx.Git("cat-file", "blob", "HEAD:"+ProfilePath)
	// Stdout carries the last TailBytes of the stream, not the whole of it: a
	// blob at that size was truncated on the way in, and a fragment must never be
	// read as the document.
	if result.Code != 0 || len(result.Stdout) >= engine.TailBytes || len(result.Stdout) > MaxProfileBytes {
		return ProfileCommands{}, false
	}
	entries, err := parseProfile([]byte(result.Stdout))
	if err != nil {
		return ProfileCommands{}, false
	}
	return commandsOf(entries), true
}

// ResolvedProfile is the language data of one run together with the commands the
// project's own file names.
type ResolvedProfile struct {
	Languages []engine.Language
	Commands  ProfileCommands
}

// ResolveProfile reads the project's file once and returns both halves of what it
// says: the languages a quality claim works with, and the commands a test or
// coverage claim may run.
//
// An error means the file exists and cannot be used. It is returned, never
// swallowed: falling back to the built-in entry would run a suite the project
// explicitly said is not its own, and the verdict would be attributed to a
// profile nobody chose.
//
// Only the file's entries become commands. The built-in languages are the
// fallback the file overrides, and treating their commands as the project's word
// would widen a caller's narrower allowlist in a repository that has no profile.
func ResolveProfile(repo string, builtin []engine.Language) (ResolvedProfile, error) {
	file, found, err := loadProfileFile(repo)
	if err != nil {
		return ResolvedProfile{}, err
	}
	if !found {
		return ResolvedProfile{Languages: builtin}, nil
	}
	languages, err := mergeLanguages(builtin, file)
	if err != nil {
		return ResolvedProfile{}, err
	}
	return ResolvedProfile{Languages: languages, Commands: commandsOf(file)}, nil
}

// commandsOf collects the commands the file names, one list per claim family. An
// entry the file names as empty is no command: it removes the built-in value and
// names nothing that may run.
func commandsOf(file []profileLanguage) ProfileCommands {
	commands := ProfileCommands{}
	for _, entry := range file {
		if entry.TestCommand != nil && strings.TrimSpace(*entry.TestCommand) != "" {
			commands.Tests = append(commands.Tests, *entry.TestCommand)
		}
		if entry.CoverageCommand != nil && strings.TrimSpace(*entry.CoverageCommand) != "" {
			commands.Coverage = append(commands.Coverage, *entry.CoverageCommand)
		}
	}
	return commands
}

// ResolveLanguages is the language-only projection of ResolveProfile, for a
// caller that needs no commands. A verification run reads ResolveProfile, because
// the commands and the languages come from the same file and must come from the
// same read.
func ResolveLanguages(repo string, builtin []engine.Language) ([]engine.Language, error) {
	resolved, err := ResolveProfile(repo, builtin)
	if err != nil {
		return nil, err
	}
	return resolved.Languages, nil
}

// WriteProfileFile writes the profile an init proved. It refuses to replace an
// existing file: the profile is the project's decision, not a generated
// artifact, and a second init must not overwrite an edited one.
func WriteProfileFile(repo string, languages []engine.Language) (string, error) {
	path := filepath.Join(repo, ProfilePath)
	switch _, err := os.Stat(path); {
	case err == nil:
		return "", fmt.Errorf("%s already exists; edit it or remove it before running init again", ProfilePath)
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%s cannot be checked: %v", ProfilePath, err)
	}

	document := profileFile{}
	for _, language := range languages {
		document.Languages = append(document.Languages, profileLanguageOf(language))
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%s cannot be written: %v", ProfilePath, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("%s cannot be created: %v", ProfilePath, err)
	}
	// A committed .yesmem may be a symlink into any directory the operator can
	// write. Following it would create a file named verify.yaml outside the
	// repository, so the parent has to resolve inside it.
	if err := refuseSymlinkedParent(repo, dir); err != nil {
		return "", err
	}
	// O_EXCL refuses to follow a symlink at the final component and refuses to
	// replace a file that appeared after the Stat above; the parent directory is
	// checked by refuseSymlinkedParent.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("%s cannot be written: %v", ProfilePath, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		// A half-written profile would steer later runs and could never be
		// repaired by another init: leave no file rather than a partial one.
		_ = os.Remove(path)
		return "", fmt.Errorf("%s cannot be written: %v", ProfilePath, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("%s cannot be written: %v", ProfilePath, err)
	}
	return path, nil
}

// refuseSymlinkedParent rejects a profile directory that resolves outside the
// repository.
func refuseSymlinkedParent(repo, dir string) error {
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return fmt.Errorf("%s cannot be resolved: %v", repo, err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%s cannot be resolved: %v", ProfilePath, err)
	}
	if resolvedDir == resolvedRepo || strings.HasPrefix(resolvedDir, resolvedRepo+string(os.PathSeparator)) {
		return nil
	}
	return fmt.Errorf("%s resolves to %q, which is outside the repository; refusing to write there",
		ProfilePath, resolvedDir)
}

// describeFileMode names what the path is instead of a regular file, so the
// error says why it was refused.
func describeFileMode(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "a symbolic link"
	case mode.IsDir():
		return "a directory"
	case mode&os.ModeNamedPipe != 0:
		return "a named pipe"
	default:
		return "not a regular file"
	}
}

// profileLanguageOf is the document form of one entry. An empty field is left
// out rather than written as an empty value: the writer emits what the profile
// says, and a reader that later sees the file must not read a generated empty
// value as a statement.
func profileLanguageOf(language engine.Language) profileLanguage {
	entry := profileLanguage{Name: language.Name}
	if len(language.Extensions) > 0 {
		entry.SourceExtensions = &language.Extensions
	}
	if len(language.TestFilePatterns) > 0 {
		entry.TestFilePatterns = &language.TestFilePatterns
	}
	if language.TestCommand != "" {
		entry.TestCommand = &language.TestCommand
	}
	if language.CoverageCommand != "" {
		entry.CoverageCommand = &language.CoverageCommand
	}
	if language.CoverageFormat != "" {
		entry.CoverageFormat = &language.CoverageFormat
	}
	if language.CoverageArtifact != "" {
		entry.CoverageArtifact = &language.CoverageArtifact
	}
	if len(language.BuildErrorPatterns) > 0 {
		entry.BuildErrorPattern = &language.BuildErrorPatterns
	}
	if len(language.SandboxReadOnly) > 0 {
		entry.SandboxReadOnly = &language.SandboxReadOnly
	}
	if len(language.SandboxEnv) > 0 {
		entry.SandboxEnv = &language.SandboxEnv
	}
	if len(language.Requires) > 0 {
		entry.Requires = &language.Requires
	}
	if language.SandboxCache != "" {
		entry.SandboxCache = &language.SandboxCache
	}
	return entry
}

// loadProfileFile reads the project's file. found is false when there is none,
// which is the ordinary case and not an error.
func loadProfileFile(repo string) (entries []profileLanguage, found bool, err error) {
	if repo == "" {
		return nil, false, nil
	}
	path := filepath.Join(repo, ProfilePath)
	// A profile is a regular file. Following a symlink here would read a host
	// path (or block on a FIFO) from repository content, and a dangling link
	// would silently read as "no profile" and fall back to the built-ins.
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil, false, nil
	}
	if statErr != nil {
		return nil, false, fmt.Errorf("%s cannot be read: %v", ProfilePath, statErr)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is %s; a profile must be a regular file",
			ProfilePath, describeFileMode(info.Mode()))
	}
	file, openErr := os.Open(path)
	if openErr != nil {
		return nil, false, fmt.Errorf("%s cannot be read: %v", ProfilePath, openErr)
	}
	defer func() { _ = file.Close() }()

	data, readErr := io.ReadAll(io.LimitReader(file, MaxProfileBytes+1))
	if readErr != nil {
		return nil, false, fmt.Errorf("%s cannot be read: %v", ProfilePath, readErr)
	}
	if len(data) > MaxProfileBytes {
		return nil, false, fmt.Errorf("%s is larger than %d bytes; a profile is configuration, not data",
			ProfilePath, MaxProfileBytes)
	}
	entries, parseErr := parseProfile(data)
	if parseErr != nil {
		return nil, false, fmt.Errorf("%s: %v", ProfilePath, parseErr)
	}
	return entries, true, nil
}

// parseProfile turns the bytes into the file's document shape.
//
// The container is checked on the YAML node before the typed decode, because a
// type error from the decoder names a Go type, not the field the human wrote.
// Everything below the container is decoded strictly: an unknown key is a
// person's typo, and ignoring it would leave them with a setting that never
// applies.
func parseProfile(data []byte) ([]profileLanguage, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Kind == 0 || len(document.Content) == 0 {
		return nil, errors.New("the file declares no 'languages'; nothing in it would apply")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the file must be a mapping with a 'languages' key")
	}
	var list *yaml.Node
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == "languages" {
			list = root.Content[index+1]
		}
	}
	if list == nil {
		return nil, errors.New("the file declares no 'languages'; nothing in it would apply")
	}
	if list.Kind != yaml.SequenceNode {
		return nil, errors.New("'languages' must be a list of language entries")
	}
	if len(list.Content) == 0 {
		return nil, errors.New("'languages' is an empty list; the file would change nothing")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var parsed profileFile
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	for index, entry := range parsed.Languages {
		if strings.TrimSpace(entry.Name) == "" {
			return nil, fmt.Errorf("language %d has no 'name'", index+1)
		}
		if err := validateProfileLanguage(entry); err != nil {
			return nil, err
		}
	}
	return parsed.Languages, nil
}

// validateProfileLanguage rejects the settings that would be silently dead:
// an extension without its dot matches no file, and a pattern the claim compiles
// with regexp.MustCompile would turn a typo into a panic during a run.
func validateProfileLanguage(entry profileLanguage) error {
	if entry.SourceExtensions != nil {
		if len(*entry.SourceExtensions) == 0 {
			return fmt.Errorf("language %q: source_extensions is empty; "+
				"a language with no extension covers no file", entry.Name)
		}
		for _, extension := range *entry.SourceExtensions {
			if !strings.HasPrefix(extension, ".") {
				return fmt.Errorf("language %q: source_extension %q must start with a dot",
					entry.Name, extension)
			}
		}
	}
	if entry.TestFilePatterns != nil && len(*entry.TestFilePatterns) == 0 {
		// Without this list every file is a production line, test files
		// included, and a mutant inside a test would count as a kill.
		return fmt.Errorf("language %q: test_file_patterns is empty; "+
			"a quality claim would then mutate test files", entry.Name)
	}
	for _, field := range []struct {
		name     string
		patterns *[]string
	}{
		{"test_file_patterns", entry.TestFilePatterns},
		{"build_error_patterns", entry.BuildErrorPattern},
	} {
		if field.patterns == nil {
			continue
		}
		for _, pattern := range *field.patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("language %q: %s: %v", entry.Name, field.name, err)
			}
		}
	}
	if entry.Requires != nil {
		for _, tool := range *entry.Requires {
			if !programNameShape(tool) {
				// A path here is a mount claim in disguise, and the mount is
				// sandbox_readonly's business: a requirement is a program the
				// verifier looks up, never a place it binds.
				return fmt.Errorf("language %q: requires %q must be a program name, not a path",
					entry.Name, tool)
			}
		}
	}
	return nil
}

// programNameShape reports whether a string names a program the verifier can
// look up: one bare name, no directory part and no traversal.
func programNameShape(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		return false
	}
	return filepath.Base(name) == name
}

// mergeLanguages applies the file to the built-in entries.
//
// A field the file names replaces the built-in one, a field it does not name
// keeps it, and a language the built-ins do not know is appended. The built-in
// set is never reduced: a mixed repository keeps its other languages.
func mergeLanguages(builtin []engine.Language, file []profileLanguage) ([]engine.Language, error) {
	merged := make([]engine.Language, len(builtin))
	copy(merged, builtin)

	for _, entry := range file {
		index := -1
		for candidate := range merged {
			if merged[candidate].Name == entry.Name {
				index = candidate
				break
			}
		}
		if index < 0 {
			merged = append(merged, engine.Language{})
			index = len(merged) - 1
		}
		language := &merged[index]
		language.Name = entry.Name
		if entry.SourceExtensions != nil {
			language.Extensions = *entry.SourceExtensions
		}
		if entry.TestFilePatterns != nil {
			language.TestFilePatterns = *entry.TestFilePatterns
		}
		if entry.TestCommand != nil {
			language.TestCommand = *entry.TestCommand
		}
		if entry.CoverageCommand != nil {
			language.CoverageCommand = *entry.CoverageCommand
		}
		if entry.CoverageFormat != nil {
			language.CoverageFormat = *entry.CoverageFormat
		}
		if entry.CoverageArtifact != nil {
			language.CoverageArtifact = *entry.CoverageArtifact
		}
		if entry.BuildErrorPattern != nil {
			language.BuildErrorPatterns = *entry.BuildErrorPattern
		}
		if entry.SandboxReadOnly != nil {
			language.SandboxReadOnly = *entry.SandboxReadOnly
		}
		if entry.SandboxEnv != nil {
			language.SandboxEnv = *entry.SandboxEnv
		}
		if entry.Requires != nil {
			language.Requires = *entry.Requires
		}
		if entry.SandboxCache != nil {
			language.SandboxCache = *entry.SandboxCache
		}
	}
	return merged, nil
}
