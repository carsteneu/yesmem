package verify

import (
	"strings"
	"testing"
)

// kinds returns the claim kinds in report order, for compact assertions.
func kinds(claims []Claim) []string {
	out := make([]string, 0, len(claims))
	for _, claim := range claims {
		out = append(out, claim.Kind)
	}
	return out
}

func TestParseCoreMarkers(t *testing.T) {
	report := "### Phase 6: FINISH\n" +
		"**send_to payload:** `[DONE] [DEPLOY: no] [COMMIT: 10cb019d9d3ee68328c862c7954bac63ed75c481] " +
		"[BRANCH: main] [MERGE: no] config wiring`\n"

	claims := ParseReport(report)
	want := []string{"deploy", "commit_exists", "branch_pushed", "merge"}
	if got := kinds(claims); !equalStrings(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	byKind := indexByKind(claims)
	if got := byKind["commit_exists"].Commit; got != "10cb019d9d3ee68328c862c7954bac63ed75c481" {
		t.Errorf("commit = %q", got)
	}
	if got := byKind["branch_pushed"].Branch; got != "main" {
		t.Errorf("branch = %q", got)
	}
	if got := byKind["merge"].Merge; got != "no" {
		t.Errorf("merge value = %q", got)
	}
	if got := byKind["deploy"].Deploy; got != "no" {
		t.Errorf("deploy value = %q", got)
	}
	if got := byKind["commit_exists"].Line; got != 2 {
		t.Errorf("line = %d, want 2", got)
	}
}

func indexByKind(claims []Claim) map[string]*Claim {
	out := make(map[string]*Claim, len(claims))
	for index := range claims {
		out[claims[index].Kind] = &claims[index]
	}
	return out
}

// A template names nothing: the marker templates of a briefing sit next to the
// real value and must not become claims.
func TestParseSkipsPlaceholders(t *testing.T) {
	report := "**send_to payload:** `[DONE] [DEPLOY: <status>] [COMMIT: <hash>] [BRANCH: <name>] [MERGE: <branch>]`\n" +
		"[COMMIT: e5b68dd1\u2026]\n" +
		"[COMMIT: TODO]\n" +
		"[COMPUTE: <cmd> -> <sha256>]\n" +
		"Tests run: <cmd> -> exit 0\n"

	if got := kinds(ParseReport(report)); len(got) != 0 {
		t.Fatalf("kinds = %v, want none", got)
	}
}

// A path pattern ends in an ASCII ellipsis and is not a template: a real
// command must stay a claim.
func TestParseKeepsPathEllipsisInCommand(t *testing.T) {
	report := "Tests run: go test ./... -> exit 0\n"

	claims := ParseReport(report)
	if got := kinds(claims); !equalStrings(got, []string{"tests_green"}) {
		t.Fatalf("kinds = %v, want [tests_green]", got)
	}
	if claims[0].Command != "go test ./..." {
		t.Errorf("command = %q", claims[0].Command)
	}
}

func TestParseTestsRunHonestFailure(t *testing.T) {
	report := "Tests run: python3 -m unittest test_bad -> exit 1\n"

	claims := ParseReport(report)
	if got := kinds(claims); !equalStrings(got, []string{"tests_exit"}) {
		t.Fatalf("kinds = %v, want [tests_exit]", got)
	}
	if !claims[0].HasExit || claims[0].ClaimedExit != 1 {
		t.Errorf("exit = %d/%v, want 1/true", claims[0].ClaimedExit, claims[0].HasExit)
	}
	if claims[0].Command != "python3 -m unittest test_bad" {
		t.Errorf("command = %q", claims[0].Command)
	}
}

// The bold form is what a yesloop report carries.
func TestParseFilesInScope(t *testing.T) {
	report := "**Files in scope:** good.txt, `test_ok.py`\n"

	claims := ParseReport(report)
	if got := kinds(claims); !equalStrings(got, []string{"diff_scope"}) {
		t.Fatalf("kinds = %v, want [diff_scope]", got)
	}
	if got := claims[0].Files; !equalStrings(got, []string{"good.txt", "test_ok.py"}) {
		t.Errorf("files = %v", got)
	}
}

func TestParseSkipsPlaceholderFileList(t *testing.T) {
	report := "**Files in scope:** proofboy/model.py, <pfad2>\n"

	if got := kinds(ParseReport(report)); len(got) != 0 {
		t.Fatalf("kinds = %v, want none", got)
	}
}

func TestParseUnknownMarkerIsADefect(t *testing.T) {
	report := "[FROB: 1]\n"

	claims := ParseReport(report)
	if got := kinds(claims); !equalStrings(got, []string{"unknown_marker"}) {
		t.Fatalf("kinds = %v, want [unknown_marker]", got)
	}
	if claims[0].Token != "FROB" {
		t.Errorf("token = %q", claims[0].Token)
	}
}

func TestParseUnknownMarkerTemplateAssertsNothing(t *testing.T) {
	report := "[FROB: <wert>]\n"

	if got := kinds(ParseReport(report)); len(got) != 0 {
		t.Fatalf("kinds = %v, want none", got)
	}
}

// The platform mandates [ID: <n>] citations in reports: a citation is not a
// claim. Only that exact shape is exempt.
func TestParseCitationIsNotAClaim(t *testing.T) {
	report := "Belegt durch [ID: 97352], nicht durch [ID: foo].\n"

	claims := ParseReport(report)
	if got := kinds(claims); !equalStrings(got, []string{"unknown_marker"}) {
		t.Fatalf("kinds = %v, want [unknown_marker]", got)
	}
	if claims[0].Token != "ID" {
		t.Errorf("token = %q", claims[0].Token)
	}
}

// A hostile line must not be scanned: the worst case per line stays bounded.
func TestParseSkipsOverlongLine(t *testing.T) {
	report := "[COMMIT: ffffffffffffffffffffffffffffffffffffffff] " + strings.Repeat("x", 9000) + "\n"

	if got := kinds(ParseReport(report)); len(got) != 0 {
		t.Fatalf("kinds = %v, want none", got)
	}
}

// Dependent claims bind to the report's commit only when exactly one
// hash-shaped commit is named: with two, the binding would be a guess.
func TestParseBindsSingleCommit(t *testing.T) {
	one := "**send_to payload:** `[COMMIT: 10cb019d9d3ee68328c862c7954bac63ed75c481] [BRANCH: main] [MERGE: topic]`\n" +
		"Tests run: python3 -m unittest test_ok -> exit 0\n"
	claims := ParseReport(one)
	for _, claim := range claims {
		if claim.Kind == "commit_exists" {
			continue
		}
		if claim.Commit != "10cb019d9d3ee68328c862c7954bac63ed75c481" {
			t.Errorf("%s commit = %q, want the report commit", claim.Kind, claim.Commit)
		}
	}

	two := "**send_to payload:** `[COMMIT: 10cb019d9d3ee68328c862c7954bac63ed75c481] " +
		"[COMMIT: 0e1d5fb324f1d087b53ca8f174cb178572d9dc0e] [BRANCH: main] [MERGE: topic]`\n" +
		"Tests run: python3 -m unittest test_ok -> exit 0\n"
	for _, claim := range ParseReport(two) {
		if claim.Kind == "commit_exists" {
			continue
		}
		if claim.Commit != "" {
			t.Errorf("%s commit = %q, want no binding with two candidates", claim.Kind, claim.Commit)
		}
	}
}

// A non-hash value is not a binding candidate, however many markers name it.
func TestParseBindsOnlyHashShapedCommits(t *testing.T) {
	report := "`[COMMIT: zz-not-hex] [COMMIT: 10cb019d9d3ee68328c862c7954bac63ed75c481] [BRANCH: main]`\n"

	claims := ParseReport(report)
	var branch *Claim
	for index := range claims {
		if claims[index].Kind == "branch_pushed" {
			branch = &claims[index]
		}
	}
	if branch == nil {
		t.Fatal("no branch_pushed claim")
	}
	if branch.Commit != "10cb019d9d3ee68328c862c7954bac63ed75c481" {
		t.Errorf("bound commit = %q", branch.Commit)
	}
}

// A report with no marker at all yields no claim: the profile closes that hole,
// the parser does not invent one.
func TestParseReportWithoutClaims(t *testing.T) {
	report := "### Phase 6: FINISH\n**Status:** COMPLETE\nAlle Phasen sind abgeschlossen.\n"

	if got := kinds(ParseReport(report)); len(got) != 0 {
		t.Fatalf("kinds = %v, want none", got)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
