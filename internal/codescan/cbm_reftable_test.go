package codescan

import "testing"

// CBM 0.12.1 compresses repeated prefixes into a reference table before the rows:
//
//	rows_refs: 14  (cols: id prefix)
//	  0 internal/daemon/
//	  1 internal/extraction/
//	rows_ref_rule: @N+suffix=prefix+suffix
//	rows: 200  (cols: t.name f.file_path)
//	  agentic_test.go @1+agentic.go        ← means internal/extraction/agentic.go
//
// The data lines of the reference table arrive before any "rows: " header, so the old
// parser rejected the first of them with "unexpected data line" — 8 times on
// 2026-11-13 for the imports, test-coverage and key-files queries.
func TestParseQueryTable_ResolvesReferences(t *testing.T) {
	text := `rows_refs: 3  (cols: id prefix)
  0 internal/daemon/
  1 internal/codescan/
  2 internal/ingest/
rows_ref_rule: @N+suffix=prefix+suffix
rows: 4  (cols: t.name f.file_path)
  cmd_docs_test.go cmd_docs.go
  agentic_test.go @1+agentic.go
  ingest_test.go @2+ingest.go
  daemon_test.go @0+daemon.go
`

	rows, err := parseQueryTable(text)
	if err != nil {
		t.Fatalf("parseQueryTable: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %v", len(rows), rows)
	}
	want := [][2]string{
		{"cmd_docs_test.go", "cmd_docs.go"},
		{"agentic_test.go", "internal/codescan/agentic.go"},
		{"ingest_test.go", "internal/ingest/ingest.go"},
		{"daemon_test.go", "internal/daemon/daemon.go"},
	}
	for i, w := range want {
		if rows[i][0] != w[0] || rows[i][1] != w[1] {
			t.Errorf("row %d = %v, want %v", i, rows[i], w)
		}
	}
}

// A bare reference (no suffix) resolves to the prefix itself.
func TestParseQueryTable_ResolvesABareReference(t *testing.T) {
	text := `rows_refs: 1  (cols: id prefix)
  0 internal/daemon/
rows_ref_rule: @N+suffix=prefix+suffix
rows: 1  (cols: path)
  @0
`

	rows, err := parseQueryTable(text)
	if err != nil {
		t.Fatalf("parseQueryTable: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "internal/daemon/" {
		t.Fatalf("rows = %v, want [[internal/daemon/]]", rows)
	}
}

// An unknown reference must fail loudly: a silently dropped cell turns a wrong path into
// a plausible one, and the consumer has no way to notice.
func TestParseQueryTable_UnknownReferenceIsAnError(t *testing.T) {
	text := `rows_refs: 1  (cols: id prefix)
  0 internal/daemon/
rows_ref_rule: @N+suffix=prefix+suffix
rows: 1  (cols: a b)
  x @7+y
`

	if _, err := parseQueryTable(text); err == nil {
		t.Fatal("a reference to an id outside the table was accepted")
	}
}

// 0.10.x wrote no reference table, and those outputs must keep parsing: an installation
// that upgrades must not need a matching downgrade of its parser.
func TestParseQueryTable_OldFormatStaysUnchanged(t *testing.T) {
	text := `rows: 2  (cols: name path)
  build Makefile
  test Makefile
total: 2
`

	rows, err := parseQueryTable(text)
	if err != nil {
		t.Fatalf("parseQueryTable: %v", err)
	}
	if len(rows) != 2 || rows[0][1] != "Makefile" {
		t.Fatalf("rows = %v, want the old format unchanged", rows)
	}
}
