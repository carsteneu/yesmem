package claimtypes

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// DefaultLanguages is the language data the quality claims work with.
//
// A profile may replace it. Every entry has to name its test file pattern and its
// build-error patterns: without them a test file would be mutated and a build
// failure would be counted as a killed mutant, which is the one error a quality
// claim must never make.
//
// SandboxReadOnly and SandboxEnv are the toolchain bridge: a compiled language
// needs the module cache its host already downloaded, because the sandbox has
// no network and hides the operator's home. Only the Go entry fills them, and
// it names its paths through the toolchain itself (engine.ToolEnv) instead of
// hardcoding a home layout. The PHP and Python entries stay empty on purpose: a
// composer cache or a pip cache path cannot be resolved without that toolchain
// being installed, and a guessed path would be bound as if it were true. With an
// empty profile the run keeps the plain sandbox, so its outcome stays an
// environment gap, which is the correct answer there.
func DefaultLanguages() []engine.Language {
	return []engine.Language{
		{
			Name:             "go",
			Extensions:       []string{".go"},
			TestFilePatterns: []string{`_test\.go$`},
			TestCommand:      "go test ./...",
			CoverageCommand:  "go test -coverprofile=.yesmem-cover.out ./...",
			CoverageFormat:   "go-cover",
			CoverageArtifact: ".yesmem-cover.out",
			BuildErrorPatterns: []string{
				// The package header go prints before a compile error, and the
				// "[build failed]" marker of the test summary. A plain
				// "file.go:12:" line is NOT enough: a failing test prints its own
				// file:line, and reading that as a build failure would drop a
				// killed mutant.
				`(?m)^# `,
				`(?m)^\./[^\s:]*\.go:\d+(?::\d+)?: `,
				`\[build failed\]`,
			},
			// The sandbox hides the operator's home and has no network, so the
			// module cache the host already has is the only thing that lets a
			// suite compile at all. It is bound read-only: a verdict must not be
			// able to write into it. The build cache is written instead, and
			// {cache} is that decision in one place: the persistent root a
			// project profile declares, or the checkout's own cache when it
			// declares none.
			//
			// TMPDIR moves to the sandbox's private tmpfs. The default (a
			// subdirectory of the checkout) is as deep as the caller's temp
			// root, and a suite that binds a unix socket or derives a file name
			// from its temp path fails there for a reason the code never had.
			SandboxReadOnly: []string{engine.ToolEnv("GOMODCACHE")},
			SandboxEnv: map[string]string{
				"GOMODCACHE":  engine.ToolEnv("GOMODCACHE"),
				"GOCACHE":     engine.CacheDirPlaceholder,
				"TMPDIR":      engine.SandboxTmpDir,
				"GOFLAGS":     "-mod=mod",
				"CGO_ENABLED": "0",
			},
			// The compiler the suite runs is a tool of the machine, not of the
			// checkout: a host that keeps go outside the system paths (a
			// version manager, a per-account install) would otherwise see the
			// suite fail for a reason the code never had.
			Requires: []string{"go"},
		},
		{
			Name:             "php",
			Extensions:       []string{".php"},
			TestFilePatterns: []string{`Test\.php$`},
			TestCommand:      "vendor/bin/phpunit",
			CoverageCommand:  "vendor/bin/phpunit --coverage-cobertura .yesmem-coverage.xml",
			CoverageFormat:   "cobertura",
			CoverageArtifact: ".yesmem-coverage.xml",
			BuildErrorPatterns: []string{
				`(?i)PHP Parse error`,
				`(?i)PHP Fatal error:  Uncaught (?:Error|TypeError|ParseError)`,
				`(?i)Cannot declare .* because the name is already in use`,
			},
		},
		{
			Name:             "python",
			Extensions:       []string{".py"},
			TestFilePatterns: []string{`^test_.*\.py$`, `_test\.py$`},
			TestCommand:      "python3 -m pytest -q",
			CoverageCommand:  "python3 -m pytest -q --cov-report=xml:.yesmem-coverage.xml",
			CoverageFormat:   "cobertura",
			CoverageArtifact: ".yesmem-coverage.xml",
			BuildErrorPatterns: []string{
				`(?i)\bSyntaxError\b`,
				`(?i)\bIndentationError\b`,
			},
		},
	}
}

// languages is the language data a quality claim works with: the profile's, or
// the defaults when the caller supplied none.
func languages(ctx *engine.Ctx) []engine.Language {
	if len(ctx.Languages) > 0 {
		return ctx.Languages
	}
	return DefaultLanguages()
}

// languageRules is one language with its patterns compiled: the claims run once
// per report, the patterns are compiled once per claim.
type languageRules struct {
	// index is the position in the profile: it is the identity of a language,
	// because two entries may carry the same name.
	index       int
	language    engine.Language
	testFile    []*regexp.Regexp
	buildErrors []*regexp.Regexp
}

func compileLanguages(languages []engine.Language) languageRulesSet {
	compiled := make(languageRulesSet, 0, len(languages))
	for index, language := range languages {
		rules := languageRules{index: index, language: language}
		for _, pattern := range language.TestFilePatterns {
			rules.testFile = append(rules.testFile, regexp.MustCompile(pattern))
		}
		for _, pattern := range language.BuildErrorPatterns {
			rules.buildErrors = append(rules.buildErrors, regexp.MustCompile(pattern))
		}
		compiled = append(compiled, rules)
	}
	return compiled
}

// forFile picks the language that covers a path by its extension.
//
// No fallback: a file no profile covers is reported by the claim, never guessed
// into one language or another.
func (set languageRulesSet) forFile(name string) (languageRules, bool) {
	extension := strings.ToLower(filepath.Ext(name))
	for _, rules := range set {
		for _, candidate := range rules.language.Extensions {
			if extension == strings.ToLower(candidate) {
				return rules, true
			}
		}
	}
	return languageRules{}, false
}

type languageRulesSet []languageRules

// isTestFile reports whether a path names a test file of this language.
func (r languageRules) isTestFile(name string) bool {
	base := filepath.Base(name)
	for _, pattern := range r.testFile {
		if pattern.MatchString(base) {
			return true
		}
	}
	return false
}

// showsBuildFailure reports whether an output is a failed build rather than a
// failing test.
func (r languageRules) showsBuildFailure(output string) bool {
	for _, pattern := range r.buildErrors {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}
