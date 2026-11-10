package claimtypes

import (
	"strings"
	"testing"
)

// Exiting 0 is not a green claim: these are the positive lines that make one.
func TestShowsTestEvidence(t *testing.T) {
	positive := []string{
		"Ran 1 test in 0.001s",
		"Ran 162 tests in 4.2s",
		"3 passed in 0.4s",
		"2 failing",
		"ok  \tgithub.com/carsteneu/yesmem/internal/verify\t0.5s",
		"--- PASS: TestParseReport (0.00s)",
		"OK (2 tests, 3 assertions)",
		"##teamcity[testFinished name='test_x']",
		"pass 4",
		"Tests:       12 passed, 2 skipped",
	}
	for _, output := range positive {
		if !showsTestEvidence(output) {
			t.Errorf("%q should count as test evidence", output)
		}
	}

	negative := []string{
		"",
		"Ran 0 tests in 0.000s",
		"no test files",
		"0 passing (12ms)",
		"BUILD SUCCESSFUL",
		"Linting 3 files",
	}
	for _, output := range negative {
		if showsTestEvidence(output) {
			t.Errorf("%q must not count as test evidence", output)
		}
	}
}

// A wrapper whose output shows nothing must not launder a green claim, and an
// empty or skipped run is its own signal.
func TestClaimsNoTests(t *testing.T) {
	positive := []string{
		"Ran 0 tests in 0.000s",
		"no test files",
		"no tests ran",
		"no tests found",
		"0 tests",
	}
	for _, output := range positive {
		if !claimsNoTests(output) {
			t.Errorf("%q should read as 'no tests ran'", output)
		}
	}
	if claimsNoTests("Ran 3 tests in 0.1s") {
		t.Error("a real run must not read as empty")
	}
}

// The skip counters ride along in the verdict text: "3 passed, 2 skipped" must
// not look like "3 passed".
func TestEvidenceSummaryCarriesCounters(t *testing.T) {
	summary := evidenceSummary("3 passed, 2 skipped in 0.4s\n")
	if !strings.Contains(summary, "evidence: 3 passed") {
		t.Errorf("summary = %q, want the evidence line", summary)
	}
	if !strings.Contains(summary, "2 skipped") {
		t.Errorf("summary = %q, want the skip counter", summary)
	}
}

// A PHP-family runner without vendor/ is an environment gap, not a test result.
func TestMissingDependency(t *testing.T) {
	output := "PHP Warning:  require(vendor/autoload.php): Failed to open stream: No such file or directory"
	if !missingDependency([]string{"vendor/bin/phpunit"}, output) {
		t.Error("a missing vendor/ should be recognised")
	}
	if missingDependency([]string{"vendor/bin/phpunit"}, output+"\nOK (2 tests, 3 assertions)") {
		t.Error("test evidence must win over a dependency phrase")
	}
	if missingDependency([]string{"python3", "-m", "unittest"}, output) {
		t.Error("a Python runner must not be judged by PHP phrases")
	}
}
