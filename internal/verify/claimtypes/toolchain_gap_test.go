package claimtypes

import (
	"strings"
	"testing"
)

// A failing run is not automatically a refutation. When the toolchain itself
// could not run (no module cache, no network), the output says so, and the
// claim must stay an environment gap. The classifier is the seam where that
// decision lives, so it is tested here as a table: the end-to-end tests in
// internal/verify cannot produce every message (which one appears depends on the
// host's go version and module cache).
func TestTheToolchainGapIsRecognisedInAFailingRun(t *testing.T) {
	cases := []struct {
		name   string
		argv   []string
		output string
		want   bool
	}{
		{
			name:   "the toolchain switch needs the network",
			argv:   []string{"go", "test", "./..."},
			output: "go: downloading go1.25.0 (linux/amd64)\ngo: download go1.25.0: dial tcp: lookup proxy.golang.org: connection refused\n",
			want:   true,
		},
		{
			name:   "no module cache entry",
			argv:   []string{"go", "test", "./..."},
			output: "go: github.com/x/y@v1.2.3: missing go.sum entry for module providing package github.com/x/y\n",
			want:   true,
		},
		{
			name:   "the runner cannot be fetched",
			argv:   []string{"npm", "test"},
			output: "npm ERR! failed to download the package\n",
			want:   true,
		},
		{
			name:   "a failing test is not a toolchain gap",
			argv:   []string{"go", "test", "./..."},
			output: "--- FAIL: TestThing (0.00s)\n    thing_test.go:9: expected 1, got 2\nFAIL\n",
			want:   false,
		},
		{
			name:   "a program that is no toolchain cannot claim the gap",
			argv:   []string{"sh", "-c", "go test ./..."},
			output: "go: downloading go1.25.0 (linux/amd64)\n",
			want:   false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := toolchainGap(testCase.argv, testCase.output); got != testCase.want {
				t.Fatalf("toolchainGap = %v, want %v (output=%q)", got, testCase.want,
					strings.TrimSpace(testCase.output))
			}
		})
	}
}

// A JavaScript run stops at its imports when the checkout has no node_modules:
// the directory is ignored by git, the checkout is a fresh clone, and the
// sandbox has no network. That run executed no test, so it cannot refute a green
// claim. The output below is what node 24.12.0 prints for a file whose first
// require cannot be resolved.
func TestAMissingNodeModuleIsAnEnvironmentGap(t *testing.T) {
	missingTypescript := `node:internal/modules/cjs/loader:1424
  throw err;
  ^

Error: Cannot find module 'typescript'
Require stack:
- /checkout/tests/model/minimum-fit-abc.test.js
    at Module._resolveFilename (node:internal/modules/cjs/loader:1421:15)
  code: 'MODULE_NOT_FOUND',
  requireStack: [ '/checkout/tests/model/minimum-fit-abc.test.js' ]
}

Node.js v24.12.0
✖ tests/model/minimum-fit-abc.test.js (106.849815ms)
ℹ tests 1
ℹ suites 0
ℹ pass 0
ℹ fail 1
`
	cases := []struct {
		name   string
		argv   []string
		output string
		want   bool
	}{
		{
			name:   "the checkout has no node_modules",
			argv:   []string{"node", "--test", "tests/*/*.test.js"},
			output: missingTypescript,
			want:   true,
		},
		{
			name:   "the same failure inside a package manager",
			argv:   []string{"npm", "test"},
			output: missingTypescript,
			want:   true,
		},
		{
			name:   "a failing suite is not a missing module",
			argv:   []string{"node", "--test", "tests/*/*.test.js"},
			output: "✔ geometry clamps the pane (2.1ms)\n✖ minimum fit rounds down (1.3ms)\nℹ tests 2\nℹ pass 1\nℹ fail 1\n",
			want:   false,
		},
		{
			name:   "a run that passed tests got past its imports",
			argv:   []string{"node", "--test", "tests/*/*.test.js"},
			output: missingTypescript + "✔ geometry clamps the pane (2.1ms)\nℹ pass 1\n",
			want:   false,
		},
		{
			name:   "a program that is no JavaScript runner cannot claim the gap",
			argv:   []string{"go", "test", "./..."},
			output: missingTypescript,
			want:   false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := missingNodeModules(testCase.argv, testCase.output); got != testCase.want {
				t.Fatalf("missingNodeModules = %v, want %v (output=%q)", got, testCase.want,
					strings.TrimSpace(testCase.output))
			}
		})
	}
}
