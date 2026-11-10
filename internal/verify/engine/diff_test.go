package engine

import (
	"strings"
	"testing"
)

// A quality claim is scoped to the lines a diff adds or changes, so the parser
// has to read them off the unified diff instead of guessing from the file list.
// The cases below are the shapes git emits: counted hunk, count omitted, pure
// deletion, deleted file, rename without a hunk, and a path with a space.
func TestParseChangedLines(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/impl.go b/impl.go",
		"--- a/impl.go",
		"+++ b/impl.go",
		"@@ -5,0 +6,2 @@ func A() {",
		"+one",
		"+two",
		"@@ -12 +14 @@",
		"-\told",
		"+\tnew",
		"\\ No newline at end of file",
		"diff --git a/del.txt b/del.txt",
		"--- a/del.txt",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-a",
		"-b",
		"diff --git a/old.go b/new.go",
		"similarity index 100%",
		"rename from old.go",
		"rename to new.go",
		"diff --git a/dir with space/x.go b/dir with space/x.go",
		"--- a/dir with space/x.go",
		"+++ b/dir with space/x.go",
		"@@ -1 +1 @@",
		"-a",
		"+b",
	}, "\n")

	got := ParseChangedLines(diff)
	want := map[string][]int{
		"impl.go":             {6, 7, 14},
		"dir with space/x.go": {1},
	}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for name, lines := range want {
		gotLines, ok := got[name]
		if !ok {
			t.Fatalf("%s: missing, got %v", name, got)
		}
		if len(gotLines) != len(lines) {
			t.Fatalf("%s: lines = %v, want %v", name, gotLines, lines)
		}
		for index := range lines {
			if gotLines[index] != lines[index] {
				t.Errorf("%s: line[%d] = %d, want %d", name, index, gotLines[index], lines[index])
			}
		}
	}
}

// A file header that is not a file header: an added line whose content starts
// with "+++" must not be read as a path.
func TestParseChangedLinesIgnoresAddedLinesLookingLikeHeaders(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/doc.md b/doc.md",
		"--- a/doc.md",
		"+++ b/doc.md",
		"@@ -1,1 +1,2 @@",
		" text",
		"+++ b/not-a-header.md",
	}, "\n")

	got := ParseChangedLines(diff)
	lines := got["doc.md"]
	if len(lines) != 1 || lines[0] != 2 {
		t.Fatalf("lines = %v, want [2]", got)
	}
	if _, ok := got["not-a-header.md"]; ok {
		t.Errorf("an added line was read as a file header: %v", got)
	}
}

func TestParseChangedLinesEmptyInput(t *testing.T) {
	if got := ParseChangedLines(""); len(got) != 0 {
		t.Fatalf("lines = %v, want none", got)
	}
}
