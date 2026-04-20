package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/models"
)

// newTestCodeGraph creates a small graph for handler tests.
func newTestCodeGraph() *codescan.CodeGraph {
	g := codescan.NewCodeGraph()
	g.AddNode(codescan.CodeNode{QualifiedName: "proxy", Kind: "package"})
	g.AddNode(codescan.CodeNode{QualifiedName: "storage", Kind: "package"})
	g.AddNode(codescan.CodeNode{QualifiedName: "proxy.CacheGet", Kind: "function", File: "proxy/cache.go", Signature: "func CacheGet(key string) ([]byte, bool)"})
	g.AddNode(codescan.CodeNode{QualifiedName: "proxy.CacheSet", Kind: "function", File: "proxy/cache.go", Signature: "func CacheSet(key string, val []byte)"})
	g.AddNode(codescan.CodeNode{QualifiedName: "storage.Query", Kind: "function", File: "storage/db.go", Signature: "func Query(sql string) ([]Row, error)"})
	g.AddEdge(codescan.CodeEdge{From: "proxy", To: "storage", Kind: "imports"})
	g.AddEdge(codescan.CodeEdge{From: "proxy", To: "proxy.CacheGet", Kind: "defines"})
	g.AddEdge(codescan.CodeEdge{From: "proxy", To: "proxy.CacheSet", Kind: "defines"})
	g.AddEdge(codescan.CodeEdge{From: "storage", To: "storage.Query", Kind: "defines"})
	// Non-defines edge for caller-count enrichment testing (S9)
	g.AddEdge(codescan.CodeEdge{From: "storage.Query", To: "proxy.CacheGet", Kind: "calls"})
	return g
}

func TestHandleSearchCodeIndex_NoProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSearchCodeIndex(map[string]any{"pattern": "Cache"})
	if resp.Error == "" {
		t.Error("expected error when no project specified")
	}
}

func TestHandleSearchCode_MissingPattern(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSearchCode(map[string]any{"project": "test"})
	if resp.Error == "" {
		t.Error("expected error when pattern is missing")
	}
}

func TestHandleGetCodeContext_MissingQualifiedName(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetCodeContext(map[string]any{"project": "test"})
	if resp.Error == "" {
		t.Error("expected error when qualified_name is missing")
	}
}

func TestHandleGetDependencyMap_MissingPackage(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetDependencyMap(map[string]any{"project": "test"})
	if resp.Error == "" {
		t.Error("expected error when package is missing")
	}
}

func TestHandleGraphTraverse_MissingFrom(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGraphTraverse(map[string]any{"project": "test"})
	if resp.Error == "" {
		t.Error("expected error when from is missing")
	}
}

func TestBoolParam(t *testing.T) {
	params := map[string]any{"flag": true, "off": false}
	if !boolParam(params, "flag", false) {
		t.Error("expected true")
	}
	if boolParam(params, "off", true) {
		t.Error("expected false")
	}
	if !boolParam(params, "missing", true) {
		t.Error("expected default true")
	}
}

func TestIntParamCI(t *testing.T) {
	params := map[string]any{"n": float64(42), "m": 7}
	if intParamCI(params, "n", 0) != 42 {
		t.Error("expected 42 from float64")
	}
	if intParamCI(params, "m", 0) != 7 {
		t.Error("expected 7 from int")
	}
	if intParamCI(params, "missing", 99) != 99 {
		t.Error("expected default 99")
	}
}

func TestTextResponse(t *testing.T) {
	resp := textResponse("hello world")
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var data map[string]string
	if err := json.Unmarshal(resp.Result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data["text"] != "hello world" {
		t.Errorf("expected 'hello world', got %q", data["text"])
	}
}

func TestHandleSearchCodeIndex_WithGraph(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	resp := h.handleSearchCodeIndex(map[string]any{
		"pattern": "Cache",
		"project": "testproject",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "CacheGet") {
		t.Errorf("expected CacheGet in results, got: %s", data["text"])
	}
}

func TestHandleGetCodeContext_WithGraph(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	resp := h.handleGetCodeContext(map[string]any{
		"qualified_name":    "proxy.CacheGet",
		"project":           "testproject",
		"include_neighbors": true,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "proxy.CacheGet") {
		t.Errorf("expected proxy.CacheGet in output, got: %s", data["text"])
	}
}

func TestHandleGetDependencyMap_WithGraph(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	resp := h.handleGetDependencyMap(map[string]any{
		"package": "proxy",
		"project": "testproject",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "storage") {
		t.Errorf("expected storage dependency, got: %s", data["text"])
	}
}

func TestHandleGraphTraverse_WithGraph(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	resp := h.handleGraphTraverse(map[string]any{
		"from":      "proxy",
		"direction": "outbound",
		"edge_type": "imports",
		"project":   "testproject",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "storage") {
		t.Errorf("expected storage in traversal, got: %s", data["text"])
	}
}

func TestHandleSearchCode_WithGraph(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	resp := h.handleSearchCode(map[string]any{
		"pattern": "Cache",
		"project": "testproject",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "CacheGet") {
		t.Errorf("expected CacheGet in results, got: %s", data["text"])
	}
	// Verify graph enrichment: CacheGet has 1 caller (storage.Query -> proxy.CacheGet calls edge)
	if !strings.Contains(data["text"], "callers/references") {
		t.Errorf("expected caller count enrichment, got: %s", data["text"])
	}
}

func TestHandleSearchCode_FilePatternFilter(t *testing.T) {
	h, _ := mustHandler(t)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	graph := newTestCodeGraph()
	h.codeGraphs["testproject"] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	// Filter to storage files only — should NOT match proxy functions
	resp := h.handleSearchCode(map[string]any{
		"pattern":      "Query",
		"project":      "testproject",
		"file_pattern": "storage",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)
	if !strings.Contains(data["text"], "storage.Query") {
		t.Errorf("expected storage.Query, got: %s", data["text"])
	}
	if strings.Contains(data["text"], "proxy.CacheGet") {
		t.Error("file_pattern filter should exclude proxy functions")
	}
}

func TestHandleSearchCode_RealGrep(t *testing.T) {
	h, s := mustHandler(t)

	// Create temp dir with source files
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "handler.go"), []byte("package pkg\n\nfunc HandleRequest() {\n\tprintln(\"processing request\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "utils.go"), []byte("package pkg\n\nfunc FormatRequest(r string) string {\n\treturn r\n}\n"), 0644)

	// Register project in DB so ResolveProjectPath works
	_, err2 := s.DB().Exec("INSERT INTO sessions (id, project, project_short, started_at, message_count, jsonl_path, indexed_at) VALUES (?, ?, ?, datetime('now'), 1, '', datetime('now'))", "test-sess", dir, "greptest")
	if err2 != nil {
		t.Fatalf("insert session: %v", err2)
	}

	// Build graph from the temp dir using DirectoryScanner (no external dependency)
	scanner := &codescan.DirectoryScanner{}
	result, err := scanner.Scan(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	graph := codescan.BuildCodeGraph(result)

	h.codeGraphMu.Lock()
	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	h.codeGraphs[dir] = &codeGraphEntry{graph: graph}
	h.codeGraphMu.Unlock()

	// Search for "request" — should grep actual files
	resp := h.handleSearchCode(map[string]any{
		"pattern": "request",
		"project": "greptest",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)

	// Should find matches in actual file content (grep, not just graph names)
	if !strings.Contains(data["text"], "processing request") {
		t.Errorf("should grep file content and find 'processing request', got:\n%s", data["text"])
	}
	// Must NOT be the graph-only fallback
	if strings.Contains(data["text"], "graph-only") {
		t.Errorf("should use real grep path, not graph-only fallback, got:\n%s", data["text"])
	}
	// Should show line numbers
	if !strings.Contains(data["text"], "L") {
		t.Errorf("should show line numbers, got:\n%s", data["text"])
	}
}

func TestHandleGetFileIndex(t *testing.T) {
	h, s := mustHandler(t)

	// Create temp dir with source files
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "handler.go"), []byte("package pkg\n\nfunc Handle() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "utils.go"), []byte("package pkg\n\nfunc Util() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644)

	// Register project in DB
	_, err := s.DB().Exec("INSERT INTO sessions (id, project, project_short, started_at, message_count, jsonl_path, indexed_at) VALUES (?, ?, ?, datetime('now'), 1, '', datetime('now'))", "test-sess", dir, "filetest")
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Add a learning with entity matching a file
	l := &models.Learning{Category: "gotcha", Content: "handler bug", Project: "filetest", Entities: []string{"handler.go"}, CreatedAt: time.Now(), Source: "test", ModelUsed: "test"}
	s.InsertLearning(l)

	resp := h.handleGetFileIndex(map[string]any{
		"dir":     "pkg",
		"project": "filetest",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	var data map[string]string
	json.Unmarshal(resp.Result, &data)

	// Should list Go source files
	if !strings.Contains(data["text"], "handler.go") {
		t.Errorf("should list handler.go, got:\n%s", data["text"])
	}
	if !strings.Contains(data["text"], "utils.go") {
		t.Errorf("should list utils.go, got:\n%s", data["text"])
	}
	// Should show learning annotation for handler.go
	if !strings.Contains(data["text"], "gotcha") {
		t.Errorf("should show gotcha annotation for handler.go, got:\n%s", data["text"])
	}
}
