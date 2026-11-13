package codescan

import (
	"os"
	"testing"
)

// The three queries the scanner runs must parse against the CBM version the
// installation actually has. This test exists because 0.12.1 changed the table format
// and broke all three at once (2026-11-13: 8 "unexpected data line" errors in one
// afternoon, none before), and no unit test with a hand-written sample would have
// caught a format the sample did not model.
//
// Opt-in: it needs the binary and an indexed project.
//
//	CBM_INTEGRATION=1 CBM_TEST_PROJECT=yesmem go test ./internal/codescan -run TestRealCBMTables -v
func TestRealCBMTablesParse(t *testing.T) {
	if os.Getenv("CBM_INTEGRATION") != "1" {
		t.Skip("set CBM_INTEGRATION=1 with an indexed project to run the real-binary check")
	}
	project := os.Getenv("CBM_TEST_PROJECT")
	if project == "" {
		project = "yesmem"
	}
	bin := FindCBMBinary()
	if bin == "" {
		t.Skip("no codebase-memory-mcp binary found")
	}

	for name, cypher := range map[string]string{
		"imports":         `MATCH (a)-[:IMPORTS]->(b) RETURN a.file_path, b.name ORDER BY a.file_path`,
		"test coverage":   `MATCH (t)-[:TESTS_FILE]->(f) RETURN t.name, f.file_path`,
		"key files":       `MATCH (n)-[:CALLS]->(f) WHERE f.label = 'Function' RETURN f.file_path, f.name, count(n) ORDER BY count(n) DESC`,
		"single column":   `MATCH (f:File) RETURN f.path`,
		"no match at all": `MATCH (x:NoSuchLabel) RETURN x.name`,
	} {
		rows, err := cbmQuery(bin, project, cypher)
		if err != nil {
			t.Errorf("%s query: %v", name, err)
			continue
		}
		for _, row := range rows {
			for _, cell := range row {
				if s, ok := cell.(string); ok && len(s) > 0 && s[0] == '@' {
					t.Errorf("%s query: unresolved reference %q in %v", name, s, row)
				}
			}
		}
		t.Logf("%s query: %d rows", name, len(rows))
	}
}
