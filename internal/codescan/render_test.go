package codescan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderCodeMap_Tiny_FullContent(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierTiny

	md := RenderCodeMap(result, nil)

	if !strings.Contains(md, content) {
		t.Error("Tiny tier should include full file content")
	}
	if !strings.Contains(md, "main.go") {
		t.Error("should include filename")
	}
}

func TestRenderCodeMap_Small_Signatures(t *testing.T) {
	dir := t.TempDir()
	content := "package api\n\nfunc HandleCreate() {}\nfunc HandleDelete() {}\n\ntype Request struct {\n\tBody string\n}\n"
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierSmall

	md := RenderCodeMap(result, nil)

	if !strings.Contains(md, "HandleCreate") {
		t.Error("Small tier should include function signatures")
	}
	if !strings.Contains(md, "Request struct") {
		t.Error("Small tier should include type signatures")
	}
	// Should NOT include full content
	if strings.Contains(md, "Body string") {
		t.Error("Small tier should not include struct fields")
	}
}

func TestRenderCodeMap_Medium_PackagesWithSignatures(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal", "proxy"), 0755)
	os.MkdirAll(filepath.Join(dir, "internal", "storage"), 0755)

	for i := 0; i < 15; i++ {
		os.WriteFile(filepath.Join(dir, "internal", "proxy", "file_"+string(rune('a'+i))+".go"),
			[]byte("package proxy\n\nfunc Handle"+string(rune('A'+i))+"() {}\n"), 0644)
	}
	for i := 0; i < 15; i++ {
		os.WriteFile(filepath.Join(dir, "internal", "storage", "file_"+string(rune('a'+i))+".go"),
			[]byte("package storage\n\nfunc Query"+string(rune('A'+i))+"() {}\n"), 0644)
	}
	// Extra files in root to push into medium territory
	for i := 0; i < 30; i++ {
		os.WriteFile(filepath.Join(dir, "cmd_"+string(rune('a'+i))+".go"),
			[]byte("package main\n\nfunc Cmd"+string(rune('A'+i))+"() {}\n"), 0644)
	}

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierMedium

	md := RenderCodeMap(result, nil)

	if !strings.Contains(md, "internal/proxy") {
		t.Error("Medium tier should show package names")
	}
	if !strings.Contains(md, "internal/storage") {
		t.Error("Medium tier should show package names")
	}
	// Should have signatures
	if !strings.Contains(md, "HandleA") {
		t.Error("Medium tier should include top signatures per package")
	}
}

func TestRenderCodeMap_Large_SpecEbene1(t *testing.T) {
	// Build a scan result matching yesmem-like project
	result := &ScanResult{
		Tier:    TierLarge,
		RootDir: "/home/user/project/yesmem",
		Stats:   ProjectStats{FileCount: 247},
		Packages: []PackageInfo{
			{Name: "internal/proxy", FileCount: 65, Description: "HTTP proxy, context pipeline, cache management",
				Files: make([]FileInfo, 65)},
			{Name: "internal/storage", FileCount: 35,
				Files: make([]FileInfo, 35)},
			{Name: "internal/daemon", FileCount: 22, Description: "Background service, MCP handlers, extraction",
				Files: make([]FileInfo, 22)},
			{Name: "internal/extraction", FileCount: 22, Description: "LLM-based learning extraction pipeline",
				Files: make([]FileInfo, 22)},
			{Name: "internal/embedding", FileCount: 14, Description: "Vector store, SSE embeddings",
				Files: make([]FileInfo, 14)},
			{Name: "internal/httpapi", FileCount: 13,
				Files: make([]FileInfo, 13)},
			{Name: "internal/briefing", FileCount: 8,
				Files: make([]FileInfo, 8)},
			{Name: "internal/setup", FileCount: 8,
				Files: make([]FileInfo, 8)},
			{Name: "internal/hooks", FileCount: 5,
				Files: make([]FileInfo, 5)},
			{Name: "internal/bloom", FileCount: 2,
				Files: make([]FileInfo, 2)},
			{Name: "internal/archive", FileCount: 2,
				Files: make([]FileInfo, 2)},
			{Name: "internal/models", FileCount: 6,
				Files: make([]FileInfo, 6)},
		},
	}

	md := RenderCodeMap(result, nil)

	// Spec: must have Code Map header
	if !strings.Contains(md, "## Code Map") {
		t.Error("missing '## Code Map' header")
	}

	// Spec: project summary line with file count and package count
	if !strings.Contains(md, "247 files") {
		t.Errorf("missing file count in project header; got:\n%s", md[:min(len(md), 300)])
	}
	if !strings.Contains(md, "12 packages") {
		t.Errorf("missing package count in project header; got:\n%s", md[:min(len(md), 300)])
	}

	// Spec: table format
	if !strings.Contains(md, "| Package") {
		t.Errorf("should use table format with '| Package' header; got:\n%s", md[:min(len(md), 500)])
	}

	// Spec: descriptions in table rows
	if !strings.Contains(md, "HTTP proxy") {
		t.Error("should include package descriptions in table")
	}

	// Spec: sorted by file count — proxy (65) before bloom (2)
	idxProxy := strings.Index(md, "proxy")
	if idxProxy < 0 {
		t.Error("proxy should appear")
	}

	// bloom (2 files) is in the collapsed tail (+2 packages) — not individually listed.
	// verify the collapsed count line is present.
	if !strings.Contains(md, "+2 packages:") {
		t.Errorf("should collapse remaining 2 packages with name-drops; got:\n%s", md[:500])
	}

	// Spec: NO LOC field as a table column (was buggy, not in spec).
	// Prose mention of LOC in the briefing intro is allowed (describes packages.md content).
	if strings.Contains(md, "| LOC ") || strings.Contains(md, " LOC |") {
		t.Error("should not contain LOC table column (not in spec)")
	}

	t.Logf("Rendered code map:\n%s", md)
}

func TestRenderCodeMap_Large_SortByFileCount(t *testing.T) {
	dir := t.TempDir()
	// Create packages with different file counts
	pkgSizes := map[string]int{"big": 20, "medium": 10, "small": 3, "tiny": 1}
	for name, count := range pkgSizes {
		pkgDir := filepath.Join(dir, name)
		os.MkdirAll(pkgDir, 0755)
		for j := 0; j < count; j++ {
			os.WriteFile(filepath.Join(pkgDir, fmt.Sprintf("f%d.go", j)),
				[]byte(fmt.Sprintf("package %s\n\nfunc Fn%d() {}\n", name, j)), 0644)
		}
	}

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierLarge

	md := RenderCodeMap(result, nil)

	// Table format
	if !strings.Contains(md, "| Package") {
		t.Fatalf("should use table format, got:\n%s", md)
	}

	// big (20 files) should appear before small (3 files)
	idxBig := strings.Index(md, "big")
	idxSmall := strings.Index(md, "small")
	if idxBig < 0 || idxSmall < 0 {
		t.Fatalf("should contain both big and small packages, got:\n%s", md[:min(len(md), 500)])
	}
	if idxBig > idxSmall {
		t.Error("big (20 files) should appear before small (3 files)")
	}

	t.Logf("Output:\n%s", md)
}

func TestRenderCodeMap_TokenBudget(t *testing.T) {
	dir := t.TempDir()
	// Create a large project with lots of content
	for i := 0; i < 30; i++ {
		pkgDir := filepath.Join(dir, "pkg_"+string(rune('a'+i)))
		os.MkdirAll(pkgDir, 0755)
		for j := 0; j < 5; j++ {
			// Each file ~500 bytes of signatures
			var lines []string
			lines = append(lines, "package pkg")
			for k := 0; k < 20; k++ {
				lines = append(lines, "func Handler"+string(rune('A'+k))+"() {}")
			}
			os.WriteFile(filepath.Join(pkgDir, "file_"+string(rune('a'+j))+".go"),
				[]byte(strings.Join(lines, "\n")+"\n"), 0644)
		}
	}

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)

	md := RenderCodeMap(result, nil)

	// ~10K tokens ≈ ~40KB. Should not exceed 50KB as hard ceiling.
	if len(md) > 50000 {
		t.Errorf("code map too large: %d bytes (should be <50KB)", len(md))
	}
}

func TestRenderCodeMap_ExcludesTestsFromSignatures(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package api\n\nfunc Handle() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte("package api\n\nfunc TestHandle(t *testing.T) {}\n"), 0644)

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierSmall

	md := RenderCodeMap(result, nil)

	if !strings.Contains(md, "Handle") {
		t.Error("should include non-test signatures")
	}
	// Test files should be noted but not have their signatures listed
	if strings.Contains(md, "TestHandle") {
		t.Error("should not include test function signatures in code map")
	}
}

func TestRenderCodeMap_LearningAnnotations(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal", "proxy"), 0755)
	os.WriteFile(filepath.Join(dir, "internal", "proxy", "cache.go"), []byte("package proxy\n\nfunc CacheGet() {}\n"), 0644)

	s := &DirectoryScanner{}
	result, _ := s.Scan(dir)
	result.Tier = TierSmall

	// Annotate package with learning count
	for i := range result.Packages {
		if result.Packages[i].Name == "internal/proxy" {
			result.Packages[i].LearningCount = 12
			result.Packages[i].GotchaCount = 3
		}
	}

	md := RenderCodeMap(result, nil)

	if !strings.Contains(md, "12 learnings") {
		t.Errorf("should show learning count, got:\n%s", md)
	}
	if !strings.Contains(md, "3 gotchas") {
		t.Errorf("should show gotcha count, got:\n%s", md)
	}
}

func TestRenderAnnotation_Empty(t *testing.T) {
	pkg := PackageInfo{Name: "test", LearningCount: 0, GotchaCount: 0}
	if renderAnnotation(pkg) != "" {
		t.Error("empty annotation should return empty string")
	}
}

func TestRenderAnnotation_OnlyLearnings(t *testing.T) {
	pkg := PackageInfo{Name: "test", LearningCount: 5}
	result := renderAnnotation(pkg)
	if result != ", 5 learnings" {
		t.Errorf("expected ', 5 learnings', got %q", result)
	}
}

func TestRenderCodeMap_ShowsImports_MediumTier(t *testing.T) {
	result := &ScanResult{
		Tier: TierMedium,
		Packages: []PackageInfo{
			{Name: "internal/proxy", FileCount: 5, TotalLOC: 500, Files: []FileInfo{
				{Path: "internal/proxy/cache.go", Language: "go",
					Signatures: []string{"func CacheGet()", "func CacheSet()"},
					Imports:    []string{"fmt", "internal/storage", "net/http"}},
			}},
		},
	}

	md := RenderCodeMap(result, nil)
	if !strings.Contains(md, "imports:") {
		t.Errorf("medium tier should show imports line, got:\n%s", md)
	}
	if !strings.Contains(md, "internal/storage") {
		t.Errorf("should show internal/storage import, got:\n%s", md)
	}
}

func TestRenderImportLine_Empty(t *testing.T) {
	var b strings.Builder
	pkg := PackageInfo{Name: "test", Files: []FileInfo{
		{Path: "test/a.go", Signatures: []string{"func A()"}},
	}}
	renderImportLine(&b, pkg)
	if b.Len() != 0 {
		t.Errorf("should not render import line when no imports, got: %s", b.String())
	}
}

func TestRenderImportLine_Deduplicates(t *testing.T) {
	var b strings.Builder
	pkg := PackageInfo{Name: "test", Files: []FileInfo{
		{Path: "test/a.go", Imports: []string{"fmt", "os"}},
		{Path: "test/b.go", Imports: []string{"fmt", "net/http"}},
	}}
	renderImportLine(&b, pkg)
	out := b.String()
	if strings.Count(out, "fmt") != 1 {
		t.Errorf("should deduplicate fmt, got: %s", out)
	}
}

func TestRenderDescription_Empty(t *testing.T) {
	pkg := PackageInfo{Name: "test"}
	if renderDescription(pkg) != "" {
		t.Error("empty description should return empty string")
	}
}

func TestRenderDescription_WithContent(t *testing.T) {
	pkg := PackageInfo{
		Name:         "proxy",
		Description:  "HTTP proxy that intercepts API requests for context injection.",
		AntiPatterns: "→ New injection = new *_inject.go file\n→ Never modify pipeline order",
	}
	out := renderDescription(pkg)
	if !strings.Contains(out, "HTTP proxy that intercepts") {
		t.Errorf("should contain description, got: %s", out)
	}
	if !strings.Contains(out, "→ New injection") {
		t.Errorf("should contain anti-pattern hint, got: %s", out)
	}
	if !strings.Contains(out, "→ Never modify") {
		t.Errorf("should contain second anti-pattern, got: %s", out)
	}
}

func TestRenderCodeMap_ShowsDescription_SmallTier(t *testing.T) {
	result := &ScanResult{
		Tier: TierSmall,
		Packages: []PackageInfo{
			{Name: "proxy", FileCount: 3, TotalLOC: 200,
				Description: "Context injection proxy.",
				Files: []FileInfo{
					{Path: "proxy/cache.go", Language: "go", Signatures: []string{"func CacheGet()"}},
				}},
		},
	}
	out := RenderCodeMap(result, nil)
	if !strings.Contains(out, "Context injection proxy.") {
		t.Errorf("small tier should show description, got:\n%s", out)
	}
}

func TestBuildImportedByMap(t *testing.T) {
	result := &ScanResult{
		Packages: []PackageInfo{
			{Name: "daemon", Files: []FileInfo{
				{Path: "daemon/extract.go", Imports: []string{"internal/storage", "internal/extraction", "internal/codescan"}},
				{Path: "daemon/handler.go", Imports: []string{"internal/storage"}},
			}},
			{Name: "proxy", Files: []FileInfo{
				{Path: "proxy/inject.go", Imports: []string{"internal/storage", "net/http"}},
			}},
			{Name: "storage", Files: []FileInfo{
				{Path: "storage/store.go", Imports: []string{"database/sql"}},
			}},
			{Name: "extraction", Files: []FileInfo{
				{Path: "extraction/llm.go", Imports: []string{"encoding/json"}},
			}},
			{Name: "codescan", Files: []FileInfo{
				{Path: "codescan/scanner.go", Imports: []string{"os"}},
			}},
		},
	}

	importedBy := buildImportedByMap(result)

	// storage is imported by daemon (2 files) and proxy (1 file) — but unique package names
	if refs, ok := importedBy["storage"]; !ok {
		t.Error("storage should have importers")
	} else {
		if len(refs) != 2 {
			t.Errorf("storage should have 2 importers (daemon, proxy), got %d: %v", len(refs), refs)
		}
	}

	// extraction imported only by daemon
	if refs, ok := importedBy["extraction"]; !ok {
		t.Error("extraction should have importers")
	} else if len(refs) != 1 || refs[0] != "daemon" {
		t.Errorf("extraction importers: %v", refs)
	}

	// stdlib imports should be excluded
	if _, ok := importedBy["net/http"]; ok {
		t.Error("stdlib imports should be excluded")
	}
}

func TestRenderEntryPoints(t *testing.T) {
	result := renderEntryPoints([]string{"main.go", "cmd/dbstats/main.go"})
	if !strings.Contains(result, "main.go") {
		t.Error("expected main.go in output")
	}
	if !strings.Contains(result, "Entry") {
		t.Error("expected 'Entry' header")
	}
	if !strings.Contains(result, "cmd/dbstats/main.go") {
		t.Error("expected cmd/dbstats/main.go in output")
	}
}

func TestRenderEntryPoints_Empty(t *testing.T) {
	result := renderEntryPoints(nil)
	if result != "" {
		t.Errorf("expected empty for nil entry points, got %q", result)
	}
}

func TestRenderTestCoverageStats(t *testing.T) {
	pkgs := []PackageInfo{
		{Name: "internal/storage", Files: []FileInfo{
			{Path: "store.go", TestCount: 2},
			{Path: "learnings.go", TestCount: 1},
			{Path: "schema.go", TestCount: 0},
			{Path: "store_test.go", IsTest: true},
		}},
		{Name: "internal/proxy", Files: []FileInfo{
			{Path: "proxy.go", TestCount: 1},
			{Path: "cache.go", TestCount: 0},
		}},
	}
	result := renderTestCoverageStats(pkgs)
	if !strings.Contains(result, "3/5") {
		t.Errorf("expected '3/5' test coverage (3 tested, 5 source), got %q", result)
	}
}

func TestRenderTestCoverageStats_AllTested(t *testing.T) {
	pkgs := []PackageInfo{
		{Name: "pkg", Files: []FileInfo{
			{Path: "a.go", TestCount: 1},
			{Path: "b.go", TestCount: 2},
		}},
	}
	result := renderTestCoverageStats(pkgs)
	if !strings.Contains(result, "2/2") {
		t.Errorf("expected '2/2' for all tested, got %q", result)
	}
}

func TestRenderTestCoverageStats_NoFiles(t *testing.T) {
	result := renderTestCoverageStats(nil)
	if result != "" {
		t.Errorf("expected empty for nil packages, got %q", result)
	}
}

func TestRenderKeyFiles(t *testing.T) {
	keyFiles := map[string][]string{
		"internal/proxy":   {"proxy.go", "associate.go"},
		"internal/storage": {"store.go"},
	}
	result := renderKeyFiles(keyFiles)
	if !strings.Contains(result, "proxy.go") {
		t.Error("expected proxy.go in key files output")
	}
	if !strings.Contains(result, "store.go") {
		t.Error("expected store.go in key files output")
	}
	if !strings.Contains(result, "Key") {
		t.Error("expected 'Key' header")
	}
}

func TestRenderKeyFiles_Empty(t *testing.T) {
	result := renderKeyFiles(nil)
	if result != "" {
		t.Errorf("expected empty for nil key files, got %q", result)
	}
}

func TestRenderChangeCoupling(t *testing.T) {
	pairs := []ChangePair{
		{FileA: "proxy.go", FileB: "associate.go"},
		{FileA: "daemon.go", FileB: "extract.go"},
	}
	result := renderChangeCoupling(pairs, 10)
	if !strings.Contains(result, "proxy.go") {
		t.Error("expected proxy.go in output")
	}
	if !strings.Contains(result, "Change Coupling") {
		t.Error("expected 'Change Coupling' header")
	}
}

func TestRenderChangeCoupling_Limit(t *testing.T) {
	pairs := []ChangePair{
		{FileA: "a.go", FileB: "b.go"},
		{FileA: "c.go", FileB: "d.go"},
		{FileA: "e.go", FileB: "f.go"},
	}
	result := renderChangeCoupling(pairs, 2)
	if !strings.Contains(result, "+ 1 more") {
		t.Errorf("expected overflow message, got %q", result)
	}
}

func TestRenderChangeCoupling_Empty(t *testing.T) {
	result := renderChangeCoupling(nil, 10)
	if result != "" {
		t.Errorf("expected empty for nil pairs, got %q", result)
	}
}

func TestRenderActiveZones(t *testing.T) {
	zones := []ActiveZone{
		{Package: "internal/proxy", ChangeCount: 12},
		{Package: "internal/daemon", ChangeCount: 5},
		{Package: ".", ChangeCount: 2},
	}
	result := renderActiveZones(zones, 10)
	if !strings.Contains(result, "Active Zones") {
		t.Error("expected 'Active Zones' header")
	}
	if !strings.Contains(result, "proxy") {
		t.Error("expected proxy in output")
	}
	if !strings.Contains(result, "12") {
		t.Error("expected change count 12")
	}
}

func TestRenderActiveZones_Limit(t *testing.T) {
	zones := []ActiveZone{
		{Package: "a", ChangeCount: 10},
		{Package: "b", ChangeCount: 5},
		{Package: "c", ChangeCount: 3},
	}
	result := renderActiveZones(zones, 2)
	if !strings.Contains(result, "+ 1 more") {
		t.Errorf("expected overflow, got %q", result)
	}
}

func TestRenderActiveZones_Empty(t *testing.T) {
	result := renderActiveZones(nil, 10)
	if result != "" {
		t.Errorf("expected empty for nil, got %q", result)
	}
}

// ── Codemap Shrink Tests ────────────────────────────────────────────

func TestRenderLarge_SortsByActivityScore(t *testing.T) {
	result := &ScanResult{
		RootDir: "/tmp/proj",
		Stats:   ProjectStats{FileCount: 100},
		Packages: []PackageInfo{
			{Name: "internal/cold", FileCount: 50},
			{Name: "internal/hot", FileCount: 5},
			{Name: "internal/medium", FileCount: 20},
		},
		ActiveZones: []ActiveZone{
			{Package: "internal/hot", ChangeCount: 100},
			{Package: "internal/medium", ChangeCount: 30},
			{Package: "internal/cold", ChangeCount: 0},
		},
	}
	out := renderLarge(result, nil, nil)
	hotIdx := strings.Index(out, "internal/hot")
	coldIdx := strings.Index(out, "internal/cold")
	if hotIdx == -1 || coldIdx == -1 {
		t.Fatalf("packages missing in output:\n%s", out)
	}
	if hotIdx > coldIdx {
		t.Errorf("expected internal/hot before internal/cold, got hot=%d cold=%d", hotIdx, coldIdx)
	}
}

func TestRenderLarge_CollapsedTableTail(t *testing.T) {
	pkgs := make([]PackageInfo, 20)
	for i := range pkgs {
		pkgs[i] = PackageInfo{Name: fmt.Sprintf("pkg%02d", i), FileCount: 20 - i}
	}
	result := &ScanResult{
		RootDir:  "/tmp/proj",
		Stats:    ProjectStats{FileCount: 100},
		Packages: pkgs,
	}
	out := renderLarge(result, nil, nil)
	rowCount := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "| pkg") {
			rowCount++
		}
	}
	if rowCount > 10 {
		t.Errorf("expected at most 10 data rows, got %d", rowCount)
	}
	if rowCount < 1 {
		t.Errorf("expected at least 1 data row, got 0")
	}
	if !strings.Contains(out, "+10 packages") {
		t.Errorf("expected '+10 packages' collapsed footer:\n%s", out)
	}
}

func TestRenderLarge_NoKeyFilesNoChangeCouplingNoActiveZones(t *testing.T) {
	result := &ScanResult{
		RootDir: "/tmp/proj",
		Stats:   ProjectStats{FileCount: 100},
		Packages: []PackageInfo{
			{Name: "internal/x", FileCount: 10},
		},
		KeyFiles: map[string][]string{
			"internal/x": {"a.go", "b.go"},
		},
		ChangeCoupling: []ChangePair{
			{FileA: "internal/x/a.go", FileB: "internal/y/b.go"},
		},
		ActiveZones: []ActiveZone{
			{Package: "internal/x", ChangeCount: 10},
		},
	}
	out := renderLarge(result, nil, nil)
	for _, banned := range []string{"### Key Files", "### Change Coupling", "### Active Zones"} {
		if strings.Contains(out, banned) {
			t.Errorf("renderLarge must NOT contain %q:\n%s", banned, out)
		}
	}
}

func TestRenderLarge_IncludesWikiLink(t *testing.T) {
	result := &ScanResult{
		RootDir: "/tmp/yesmem",
		Stats:   ProjectStats{FileCount: 700},
		Packages: []PackageInfo{
			{Name: "internal/proxy", FileCount: 130},
			{Name: "internal/daemon", FileCount: 70},
		},
	}
	out := renderLarge(result, nil, nil)
	for _, expect := range []string{
		"Full code map:",
		"~/.claude/yesmem/wiki/yesmem/",
		"BEFORE",
		"check its wiki page",
		"search_code_index",
		"`/tmp/yesmem`",
	} {
		if !strings.Contains(out, expect) {
			t.Errorf("missing %q in renderLarge output:\n%s", expect, out)
		}
	}
}

func TestRenderMedium_IncludesWikiLink(t *testing.T) {
	result := &ScanResult{
		Tier:    TierMedium,
		RootDir: "/tmp/yesmem",
		Stats:   ProjectStats{FileCount: 150},
		Packages: []PackageInfo{
			{Name: "internal/proxy", FileCount: 60, Description: "HTTP proxy",
				Files: make([]FileInfo, 60)},
			{Name: "internal/daemon", FileCount: 40, Description: "Daemon",
				Files: make([]FileInfo, 40)},
		},
	}
	out := RenderCodeMap(result, nil)
	for _, expect := range []string{
		"Full code map:",
		"~/.claude/yesmem/wiki/yesmem/",
		"BEFORE",
		"check its wiki page",
	} {
		if !strings.Contains(out, expect) {
			t.Errorf("missing %q in renderMedium output:\n%s", expect, out)
		}
	}
}

func TestRenderSmall_IncludesWikiLink(t *testing.T) {
	result := &ScanResult{
		Tier:    TierSmall,
		RootDir: "/tmp/yessmall",
		Stats:   ProjectStats{FileCount: 30},
		Packages: []PackageInfo{
			{Name: "internal/app", FileCount: 15, Description: "App",
				Files: make([]FileInfo, 15)},
			{Name: "internal/util", FileCount: 10, Description: "Utils",
				Files: make([]FileInfo, 10)},
		},
	}
	out := RenderCodeMap(result, nil)
	for _, expect := range []string{
		"Full code map:",
		"~/.claude/yesmem/wiki/yessmall/",
		"BEFORE",
	} {
		if !strings.Contains(out, expect) {
			t.Errorf("missing %q in renderSmall output:\n%s", expect, out)
		}
	}
}

func TestRenderTiny_IncludesWikiLink(t *testing.T) {
	result := &ScanResult{
		Tier:    TierTiny,
		RootDir: "/tmp/yestiny",
		Stats:   ProjectStats{FileCount: 3},
		Packages: []PackageInfo{
			{Name: "yestiny", FileCount: 1, Description: "Tiny project",
				Files: []FileInfo{
					{Path: "main.go", Language: "go", LOC: 50, Content: "package main\nfunc main() {}"},
				}},
		},
		Files: []FileInfo{
			{Path: "main.go", Language: "go", LOC: 50, Content: "package main\nfunc main() {}"},
		},
	}
	out := RenderCodeMap(result, nil)
	for _, expect := range []string{
		"Full code map:",
		"~/.claude/yesmem/wiki/yestiny/",
		"BEFORE",
	} {
		if !strings.Contains(out, expect) {
			t.Errorf("missing %q in renderTiny output:\n%s", expect, out)
		}
	}
}
