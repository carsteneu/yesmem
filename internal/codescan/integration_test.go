package codescan

import (
	"os"
	"strings"
	"testing"
)

// TestIntegration_ScanOwnRepo scans the YesMem repository
// and validates that CBMScanner + CodeGraph + RenderCodeMap produce coherent results.
func TestIntegration_ScanOwnRepo(t *testing.T) {
	if FindCBMBinary() == "" {
		t.Skip("codebase-memory-mcp not installed, skipping integration test")
	}

	repoRoot := findRepoRoot(t)

	scanner := NewCBMScanner()
	result, err := scanner.Scan(repoRoot)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	if len(result.Packages) == 0 {
		t.Fatal("no packages found")
	}

	if len(result.Packages) < 1 {
		t.Error("expected at least 1 package")
	}
	t.Logf("Package names: %v", func() []string {
		var names []string
		for _, p := range result.Packages {
			names = append(names, p.Name)
		}
		return names
	}())

	// CBMScanner should extract real signatures
	totalSigs := 0
	totalImports := 0
	for _, pkg := range result.Packages {
		for _, f := range pkg.Files {
			totalSigs += len(f.Signatures)
			totalImports += len(f.Imports)
		}
	}
	if totalSigs < 5 {
		t.Errorf("expected 5+ signatures, got %d", totalSigs)
	}
	t.Logf("Scanned %d packages, %d signatures, %d imports", len(result.Packages), totalSigs, totalImports)

	// Build CodeGraph
	graph := BuildCodeGraph(result)
	if graph.NodeCount() < 5 {
		t.Errorf("expected 5+ graph nodes, got %d", graph.NodeCount())
	}
	t.Logf("CodeGraph: %d nodes", graph.NodeCount())

	// Search should work
	scanHits := graph.SearchNodes("Scan", "", "")
	if len(scanHits) == 0 {
		t.Error("expected to find Scan-related symbols")
	}

	// Render code map should produce output
	codeMap := RenderCodeMap(result, nil)
	if len(codeMap) < 50 {
		t.Errorf("code map too short: %d bytes", len(codeMap))
	}
	t.Logf("Code map: %d bytes", len(codeMap))

	// Cycle detection should not crash
	cycles := graph.DetectCycles()
	t.Logf("Import cycles detected: %d", len(cycles))
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		parent := dir[:strings.LastIndex(dir, "/")]
		if parent == dir || parent == "" {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
