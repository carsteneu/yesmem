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
