package codescan

import (
	"testing"
)

func TestCodeGraph_AddNode(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{
		QualifiedName: "internal/proxy.CacheGet",
		Kind:          "function",
		File:          "internal/proxy/cache.go",
		Line:          42,
		Signature:     "func CacheGet(key string) ([]byte, bool)",
	})

	node := g.GetNode("internal/proxy.CacheGet")
	if node == nil {
		t.Fatal("node not found")
	}
	if node.Kind != "function" {
		t.Errorf("expected function, got %s", node.Kind)
	}
}

func TestCodeGraph_AddEdge(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "cmd/main", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "internal/proxy", Kind: "package"})
	g.AddEdge(CodeEdge{From: "cmd/main", To: "internal/proxy", Kind: "imports"})

	edges := g.EdgesFrom("cmd/main")
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].To != "internal/proxy" {
		t.Errorf("expected edge to internal/proxy, got %s", edges[0].To)
	}
}

func TestCodeGraph_EdgesTo(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "pkg_a", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "pkg_b", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "pkg_c", Kind: "package"})
	g.AddEdge(CodeEdge{From: "pkg_a", To: "pkg_c", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "pkg_b", To: "pkg_c", Kind: "imports"})

	edges := g.EdgesTo("pkg_c")
	if len(edges) != 2 {
		t.Errorf("expected 2 inbound edges, got %d", len(edges))
	}
}

func TestCodeGraph_SearchNodes_ByPattern(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "proxy.CacheGet", Kind: "function", Signature: "func CacheGet()"})
	g.AddNode(CodeNode{QualifiedName: "proxy.CacheSet", Kind: "function", Signature: "func CacheSet()"})
	g.AddNode(CodeNode{QualifiedName: "proxy.Forward", Kind: "function", Signature: "func Forward()"})
	g.AddNode(CodeNode{QualifiedName: "storage.Query", Kind: "function", Signature: "func Query()"})

	results := g.SearchNodes("Cache", "", "")
	if len(results) != 2 {
		t.Errorf("expected 2 matches for 'Cache', got %d", len(results))
	}
}

func TestCodeGraph_SearchNodes_ByKindAndFile(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "proxy.CacheGet", Kind: "function", File: "proxy/cache.go"})
	g.AddNode(CodeNode{QualifiedName: "proxy.CacheSet", Kind: "function", File: "proxy/cache.go"})
	g.AddNode(CodeNode{QualifiedName: "proxy.Forward", Kind: "function", File: "proxy/forward.go"})
	g.AddNode(CodeNode{QualifiedName: "proxy.Config", Kind: "type", File: "proxy/config.go"})

	// Substring match (no glob chars)
	results := g.SearchNodes("", "function", "proxy")
	if len(results) != 3 {
		t.Errorf("expected 3 functions in proxy, got %d", len(results))
	}
}

func TestCodeGraph_SearchNodes_GlobPattern(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "proxy.CacheGet", Kind: "function", File: "proxy/cache.go"})
	g.AddNode(CodeNode{QualifiedName: "proxy.Forward", Kind: "function", File: "proxy/forward.go"})
	g.AddNode(CodeNode{QualifiedName: "storage.Query", Kind: "function", File: "storage/db.go"})
	g.AddNode(CodeNode{QualifiedName: "storage.Migrate", Kind: "function", File: "storage/migrate.py"})

	// Glob: *.go should match Go files only
	results := g.SearchNodes("", "", "*.go")
	if len(results) != 3 {
		t.Errorf("expected 3 .go files, got %d", len(results))
	}

	// Glob: storage/* should match storage dir
	results = g.SearchNodes("", "", "storage/*")
	if len(results) != 2 {
		t.Errorf("expected 2 storage files, got %d", len(results))
	}
}

func TestCodeGraph_Traverse_Outbound(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "main", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "daemon", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "storage", Kind: "package"})
	g.AddEdge(CodeEdge{From: "main", To: "daemon", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "daemon", To: "storage", Kind: "imports"})

	paths := g.Traverse("main", "outbound", "imports", 3)
	if len(paths) == 0 {
		t.Fatal("expected traversal paths")
	}
	found := false
	for _, p := range paths {
		if len(p) == 3 && p[0] == "main" && p[1] == "daemon" && p[2] == "storage" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected path main→daemon→storage, got %v", paths)
	}
}

func TestCodeGraph_Traverse_Inbound(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "main", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "daemon", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "storage", Kind: "package"})
	g.AddEdge(CodeEdge{From: "main", To: "daemon", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "daemon", To: "storage", Kind: "imports"})

	paths := g.Traverse("storage", "inbound", "imports", 3)
	if len(paths) == 0 {
		t.Fatal("expected inbound traversal paths")
	}
}

func TestCodeGraph_DetectCycles(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "a", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "b", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "c", Kind: "package"})
	g.AddEdge(CodeEdge{From: "a", To: "b", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "b", To: "c", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "c", To: "a", Kind: "imports"})

	cycles := g.DetectCycles()
	if len(cycles) == 0 {
		t.Error("should detect cycle a→b→c→a")
	}
}

func TestCodeGraph_NoCycles(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "a", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "b", Kind: "package"})
	g.AddEdge(CodeEdge{From: "a", To: "b", Kind: "imports"})

	cycles := g.DetectCycles()
	if len(cycles) != 0 {
		t.Errorf("should not detect cycles, got %v", cycles)
	}
}

func TestCodeGraph_BuildFromScanResult(t *testing.T) {
	result := &ScanResult{
		RootDir: "/test",
		Packages: []PackageInfo{
			{Name: "internal/proxy", Files: []FileInfo{
				{Path: "internal/proxy/cache.go", Language: "go",
					Signatures: []string{"func CacheGet()", "func CacheSet()"},
					Imports:    []string{"fmt", "internal/storage"}},
			}},
			{Name: "internal/storage", Files: []FileInfo{
				{Path: "internal/storage/db.go", Language: "go",
					Signatures: []string{"func Query()"},
					Imports:    []string{"database/sql"}},
			}},
		},
	}

	g := BuildCodeGraph(result)

	if g.GetNode("internal/proxy") == nil {
		t.Error("missing package node internal/proxy")
	}
	if g.GetNode("internal/proxy.CacheGet") == nil {
		t.Error("missing function node CacheGet")
	}

	edges := g.EdgesFrom("internal/proxy")
	hasImport := false
	for _, e := range edges {
		if e.To == "internal/storage" && e.Kind == "imports" {
			hasImport = true
		}
	}
	if !hasImport {
		t.Error("missing import edge proxy→storage")
	}
}

func TestCodeGraph_NodeCount(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "a", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "b", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "a.Foo", Kind: "function"})

	if g.NodeCount() != 3 {
		t.Errorf("expected 3 nodes, got %d", g.NodeCount())
	}
}

func TestCodeGraph_BuildFromScanResult_ImportsWithPaths(t *testing.T) {
	result := &ScanResult{
		Packages: []PackageInfo{
			{Name: "internal/proxy", Files: []FileInfo{
				{Path: "internal/proxy/proxy.go", Imports: []string{"internal/storage", "internal/daemon"}},
			}},
			{Name: "internal/storage", Files: []FileInfo{
				{Path: "internal/storage/store.go"},
			}},
			{Name: "internal/daemon", Files: []FileInfo{
				{Path: "internal/daemon/daemon.go"},
			}},
		},
	}
	graph := BuildCodeGraph(result)
	edges := graph.EdgesFrom("internal/proxy")
	importCount := 0
	for _, e := range edges {
		if e.Kind == "imports" {
			importCount++
		}
	}
	if importCount != 2 {
		t.Errorf("expected 2 import edges from proxy, got %d", importCount)
	}
}

func TestCodeGraph_BuildFromScanResult_ImportsBaseName(t *testing.T) {
	// CBM CLI may return base-name-only imports (e.g. "storage") not full paths
	result := &ScanResult{
		Packages: []PackageInfo{
			{Name: "internal/proxy", Files: []FileInfo{
				{Path: "internal/proxy/proxy.go", Imports: []string{"storage", "daemon"}},
			}},
			{Name: "internal/storage", Files: []FileInfo{
				{Path: "internal/storage/store.go"},
			}},
			{Name: "internal/daemon", Files: []FileInfo{
				{Path: "internal/daemon/daemon.go"},
			}},
		},
	}
	graph := BuildCodeGraph(result)
	edges := graph.EdgesFrom("internal/proxy")
	importCount := 0
	for _, e := range edges {
		if e.Kind == "imports" {
			importCount++
		}
	}
	if importCount != 2 {
		t.Errorf("expected 2 import edges from base-name imports, got %d", importCount)
	}
}

func TestFormatSignature(t *testing.T) {
	tests := []struct {
		label    string
		name     string
		expected string
	}{
		{"Function", "CacheGet", "func CacheGet"},
		{"Method", "SearchNodes", "func SearchNodes"},
		{"Interface", "Handler", "interface Handler"},
		{"Class", "Server", "type Server"},
		{"Unknown", "Foo", "func Foo"},
	}
	for _, tt := range tests {
		got := formatSignature(tt.label, tt.name, "")
		if got != tt.expected {
			t.Errorf("formatSignature(%q, %q) = %q, want %q", tt.label, tt.name, got, tt.expected)
		}
	}
}

func TestExtractNameFromSig(t *testing.T) {
	tests := []struct {
		sig      string
		expected string
	}{
		{"func CacheGet()", "CacheGet"},
		{"func SearchNodes", "SearchNodes"},
		{"method SearchNodes", "SearchNodes"},
		{"func (g *CodeGraph) SearchNodes(pattern, kind, filePattern string) []*CodeNode", "SearchNodes"},
		{"type CodeGraph struct", "CodeGraph"},
		{"func New(dataDir string) (*Server, error)", "New"},
	}
	for _, tt := range tests {
		got := extractNameFromSig(tt.sig)
		if got != tt.expected {
			t.Errorf("extractNameFromSig(%q) = %q, want %q", tt.sig, got, tt.expected)
		}
	}
}

func TestBuildFromScanResult_WithMethods(t *testing.T) {
	// Regression test: Methods must appear in the code graph.
	// Bug: formatSignature returned "method X" which extractNameFromSig couldn't parse.
	result := &ScanResult{
		RootDir: "/test",
		Packages: []PackageInfo{
			{Name: "internal/codescan", Files: []FileInfo{
				{Path: "internal/codescan/graph.go", Language: "go",
					Signatures: []string{
						"func NewCodeGraph",
						"func SearchNodes",
						"func AddNode",
						"type CodeGraph",
					},
				},
			}},
		},
	}

	g := BuildCodeGraph(result)

	// All symbols should be findable
	expected := []string{
		"internal/codescan.NewCodeGraph",
		"internal/codescan.SearchNodes",
		"internal/codescan.AddNode",
		"internal/codescan.CodeGraph",
	}
	for _, qn := range expected {
		if g.GetNode(qn) == nil {
			t.Errorf("missing node: %s", qn)
		}
	}

	// SearchNodes should find Methods
	results := g.SearchNodes("SearchNodes", "", "")
	if len(results) != 1 {
		t.Errorf("SearchNodes for 'SearchNodes': expected 1, got %d", len(results))
	}
	results = g.SearchNodes("AddNode", "", "")
	if len(results) != 1 {
		t.Errorf("SearchNodes for 'AddNode': expected 1, got %d", len(results))
	}
}

func TestCodeGraph_EdgeDedup(t *testing.T) {
	g := NewCodeGraph()
	g.AddNode(CodeNode{QualifiedName: "a", Kind: "package"})
	g.AddNode(CodeNode{QualifiedName: "b", Kind: "package"})

	// Add same edge twice
	g.AddEdge(CodeEdge{From: "a", To: "b", Kind: "imports"})
	g.AddEdge(CodeEdge{From: "a", To: "b", Kind: "imports"})

	edges := g.EdgesFrom("a")
	if len(edges) != 1 {
		t.Errorf("expected 1 edge after dedup, got %d", len(edges))
	}

	inEdges := g.EdgesTo("b")
	if len(inEdges) != 1 {
		t.Errorf("expected 1 inbound edge after dedup, got %d", len(inEdges))
	}

	// Different kind = separate edge
	g.AddEdge(CodeEdge{From: "a", To: "b", Kind: "calls"})
	edges = g.EdgesFrom("a")
	if len(edges) != 2 {
		t.Errorf("expected 2 edges (imports + calls), got %d", len(edges))
	}
}
