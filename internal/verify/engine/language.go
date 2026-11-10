package engine

// Language is the language-specific half of a quality claim: which files are
// production sources, which are test files, how the suite runs, how coverage is
// measured and what a failed build looks like.
//
// It is data only. No claim type may branch on Name: such a branch would have to
// grow one arm per language, and every language it did not know would silently
// degrade into a guess.
type Language struct {
	Name string
	// Extensions are the source suffixes this language covers, dot included.
	Extensions []string
	// TestFilePatterns match a base name. A test file is never mutated: a mutant
	// inside the suite tests the test, not the code.
	TestFilePatterns []string
	// TestCommand is the suite command a quality claim runs. It has to pass the
	// same allowlist and escape rules as a reported command.
	TestCommand string
	// CoverageCommand writes CoverageArtifact in CoverageFormat. Empty means the
	// language cannot supply coverage, which a coverage claim reports as an
	// environment gap instead of a confirmation.
	CoverageCommand  string
	CoverageFormat   string
	CoverageArtifact string
	// BuildErrorPatterns recognise a failed build in the run's output. Without
	// one, a build failure cannot be told from a failing test, and counting it as
	// a killed mutant would confirm a claim that was never proven.
	BuildErrorPatterns []string
	// SandboxReadOnly names the host directories a sandboxed run of this
	// language must be able to read but never write: the module cache, the
	// vendor tree of a compiled toolchain.
	//
	// A path that does not exist is not bound; the run then stays an
	// environment gap instead of guessing a fallback. A path that is the
	// operator's home, or any parent of it, is refused: the sandbox hides the
	// home behind an empty tmpfs, and putting it back would undo that shield.
	//
	// The value may carry {rundir}, the directory the run executes in, and
	// {toolenv:NAME}, the answer of the language's own program to
	// `env NAME`. Paths are host paths: a profile must never derive one from
	// repository content.
	SandboxReadOnly []string
	// SandboxEnv is the environment a sandboxed run of this language gets on top
	// of the minimal one. A key the profile names REPLACES the minimal entry of
	// the same name: the profile speaks for the toolchain it declares, and two
	// entries for one key would let the child pick. That is why a profile may
	// move TMPDIR (a deep temp path breaks real suites) but must never touch
	// HOME or PATH without knowing what it undoes.
	//
	// Same placeholders as SandboxReadOnly, plus {cache}: the root SandboxCache
	// declares, or the checkout's own build cache when it declares none. A value
	// that names a mounted cache root is rewritten to the root's resolved path,
	// so the variable and the mount agree even when the cache root is a symlink.
	//
	// A run outside the sandbox never gets these: a language's own build
	// configuration belongs to the confined run, not to the verifier.
	SandboxEnv map[string]string
	// Requires names the programs the suite needs beside the declared command:
	// a runtime, a package manager, a linker the build shells out to. Before a
	// sandboxed run starts, each name is looked up on the operator's PATH and
	// judged against the sandbox's masks. A tool that is absent, or that lives
	// below a masked tree the profile does not mount, makes the run an
	// environment gap: the confinement could not provide it, and the report is
	// not wrong about that.
	//
	// Names only, no paths: a path here would be a mount claim in disguise, and
	// the mount is SandboxReadOnly's business. An empty list declares nothing,
	// which leaves the run exactly as it was.
	Requires []string
	// SandboxCache is the persistent build cache root of this language: one
	// absolute host path, mounted writable, surviving the throwaway checkout so
	// the next run compiles warm. It is the one writable tree a profile may name,
	// so the rules are the strictest: never the operator's home or a parent of
	// it, never a top-level directory of the home, never a credentials
	// directory, never the verified repository, never a tree the sandbox masks.
	// A missing root is created; one that cannot be created stays an environment
	// gap.
	//
	// Same placeholders as SandboxReadOnly. Empty means no declaration, and the
	// run then keeps its build cache inside the checkout, which dies with it.
	SandboxCache string
}
