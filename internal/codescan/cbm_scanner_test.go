package codescan

import (
	"path/filepath"
	"runtime"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func TestCBMScanner_Scan(t *testing.T) {
	if FindCBMBinary() == "" {
		t.Skip("codebase-memory-mcp not installed, skipping integration test")
	}

	scanner := NewCBMScanner()
	result, err := scanner.Scan(repoRoot(t))
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if len(result.Packages) == 0 {
		t.Fatal("expected packages, got 0")
	}
	if result.Stats.FileCount == 0 {
		t.Fatal("expected files, got 0")
	}

	t.Logf("Packages: %d, Files: %d, LOC: %d, Tier: %s",
		len(result.Packages), result.Stats.FileCount, result.Stats.TotalLOC, result.Tier)

	// Check signatures
	sigCount := 0
	for _, pkg := range result.Packages {
		for _, f := range pkg.Files {
			sigCount += len(f.Signatures)
		}
	}
	if sigCount == 0 {
		t.Fatal("expected signatures, got 0")
	}
	t.Logf("Total signatures: %d", sigCount)

	// Verify known package
	found := false
	for _, pkg := range result.Packages {
		if pkg.Name == "internal/proxy" {
			found = true
			if pkg.FileCount == 0 {
				t.Error("internal/proxy should have files")
			}
			t.Logf("internal/proxy: %d files, %d LOC", pkg.FileCount, pkg.TotalLOC)
			break
		}
	}
	if !found {
		t.Error("expected internal/proxy package")
	}
}

func TestCBMProjectName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/tmp/foo/yesmem", "tmp-foo-yesmem"},
		{"/home/user/project", "home-user-project"},
		{"/tmp/test", "tmp-test"},
		// Worktrees must get their own DB — never collapse to parent repo.
		{
			"/tmp/foo/yesmem/.worktrees/briefing-injection",
			"tmp-foo-yesmem-.worktrees-briefing-injection",
		},
	}
	for _, tt := range tests {
		got := cbmProjectName(tt.input)
		if got != tt.want {
			t.Errorf("cbmProjectName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseImportRows(t *testing.T) {
	rows := [][]interface{}{
		{"internal/proxy/proxy.go", "internal/storage"},
		{"internal/proxy/proxy.go", "internal/daemon"},
		{"internal/daemon/daemon.go", "internal/storage"},
	}
	result := parseImportRows(rows)
	if len(result["internal/proxy/proxy.go"]) != 2 {
		t.Errorf("expected 2 imports for proxy.go, got %d", len(result["internal/proxy/proxy.go"]))
	}
	if len(result["internal/daemon/daemon.go"]) != 1 {
		t.Errorf("expected 1 import for daemon.go, got %d", len(result["internal/daemon/daemon.go"]))
	}
}

func TestParseImportRows_Empty(t *testing.T) {
	result := parseImportRows(nil)
	if len(result) != 0 {
		t.Errorf("expected empty map for nil rows, got %d entries", len(result))
	}
}

func TestParseEntryPoints(t *testing.T) {
	rows := [][]interface{}{
		{"main", "main.go"},
		{"main", "cmd/dbstats/main.go"},
	}
	result := parseEntryPoints(rows)
	if len(result) != 2 {
		t.Errorf("expected 2 entry points, got %d", len(result))
	}
	if result[0] != "main.go" {
		t.Errorf("expected main.go first, got %q", result[0])
	}
}

func TestParseEntryPoints_SkipsEmpty(t *testing.T) {
	rows := [][]interface{}{
		{"main", ""},
		{"main", "main.go"},
	}
	result := parseEntryPoints(rows)
	if len(result) != 1 {
		t.Errorf("expected 1 entry point (empty filtered), got %d", len(result))
	}
}

func TestParseTestCoverage(t *testing.T) {
	rows := [][]interface{}{
		{"store_test.go", "store.go"},
		{"store_integration_test.go", "store.go"},
		{"daemon_test.go", "daemon.go"},
	}
	result := parseTestCoverage(rows)
	if result["store.go"] != 2 {
		t.Errorf("expected 2 test files for store.go, got %d", result["store.go"])
	}
	if result["daemon.go"] != 1 {
		t.Errorf("expected 1 test file for daemon.go, got %d", result["daemon.go"])
	}
}

func TestParseChangeCoupling(t *testing.T) {
	rows := [][]interface{}{
		{"proxy.go", "associate.go"},
		{"associate.go", "proxy.go"}, // duplicate in reverse
		{"daemon.go", "extract.go"},
		{"daemon.go", "daemon.go"}, // self-reference
	}
	result := parseChangeCoupling(rows)
	if len(result) != 2 {
		t.Errorf("expected 2 unique pairs (deduped + self-filtered), got %d", len(result))
	}
}

func TestParseKeyFiles(t *testing.T) {
	rows := [][]interface{}{
		{"internal/storage/store.go", "mustOpen", "15"},
		{"internal/storage/store.go", "Open", "10"},
		{"internal/storage/learnings.go", "Get", "5"},
		{"internal/proxy/proxy.go", "mustHandler", "25"},
	}
	result := parseKeyFiles(rows, 1)
	if len(result["internal/storage"]) != 1 {
		t.Errorf("expected 1 key file for storage (topN=1), got %d", len(result["internal/storage"]))
	}
	if result["internal/storage"][0] != "store.go" {
		t.Errorf("expected store.go as top file, got %q", result["internal/storage"][0])
	}
	if result["internal/proxy"][0] != "proxy.go" {
		t.Errorf("expected proxy.go as top file, got %q", result["internal/proxy"][0])
	}
}

func TestParseCount(t *testing.T) {
	tests := []struct {
		input interface{}
		want  int
	}{
		{"15", 15},
		{"0", 0},
		{"", 0},
		{float64(42), 42},
		{float64(0), 0},
		{nil, 0},
	}
	for _, tt := range tests {
		got := parseCount(tt.input)
		if got != tt.want {
			t.Errorf("parseCount(%v) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestDedup(t *testing.T) {
	input := []string{"main.go", "cmd/main.go", "main.go", "routes.go", "cmd/main.go"}
	result := dedup(input)
	if len(result) != 3 {
		t.Errorf("expected 3 unique entries, got %d: %v", len(result), result)
	}
	if result[0] != "main.go" || result[1] != "cmd/main.go" || result[2] != "routes.go" {
		t.Errorf("unexpected order: %v", result)
	}
}

func TestDedup_Empty(t *testing.T) {
	result := dedup(nil)
	if len(result) != 0 {
		t.Errorf("expected empty for nil, got %v", result)
	}
}

func TestParseGitActiveZones(t *testing.T) {
	gitOutput := `internal/proxy/sawtooth.go
internal/proxy/proxy.go
internal/proxy/collapse.go
internal/daemon/handler_state.go
internal/daemon/persona.go
internal/codescan/scanner.go
internal/codescan/render.go
internal/codescan/cbm_scanner.go
internal/codescan/cbm_scanner.go
main.go
`
	zones := parseGitActiveZones(gitOutput)
	if len(zones) == 0 {
		t.Fatal("expected zones, got none")
	}
	// proxy has 3 changes, codescan has 4 (cbm_scanner.go twice)
	if zones[0].Package != "internal/codescan" || zones[0].ChangeCount != 4 {
		t.Errorf("expected internal/codescan with 4 changes first, got %s with %d", zones[0].Package, zones[0].ChangeCount)
	}
	if zones[1].Package != "internal/proxy" || zones[1].ChangeCount != 3 {
		t.Errorf("expected internal/proxy with 3 changes second, got %s with %d", zones[1].Package, zones[1].ChangeCount)
	}
}

func TestParseGitActiveZones_Empty(t *testing.T) {
	zones := parseGitActiveZones("")
	if len(zones) != 0 {
		t.Errorf("expected empty for empty input, got %v", zones)
	}
}

func TestParseGitActiveZones_RootFiles(t *testing.T) {
	gitOutput := "main.go\ncmd_telegram.go\n"
	zones := parseGitActiveZones(gitOutput)
	if len(zones) != 1 || zones[0].Package != "." {
		t.Errorf("expected root package '.', got %v", zones)
	}
	if zones[0].ChangeCount != 2 {
		t.Errorf("expected 2 changes, got %d", zones[0].ChangeCount)
	}
}
