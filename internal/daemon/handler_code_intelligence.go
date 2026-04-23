package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/codescan"
)

// codeGraphEntry holds a cached CodeGraph with the git HEAD it was built from.
type codeGraphEntry struct {
	graph   *codescan.CodeGraph
	head    string
	scanner *codescan.CachedScanner
}

// getCodeGraph returns the CodeGraph for a project, building it on first access.
// Thread-safe. Returns nil if project directory cannot be resolved.
// Cache is keyed by resolved project directory (or project name as fallback) so
// worktrees get separate graphs.
func (h *Handler) getCodeGraph(project string, params ...map[string]any) *codescan.CodeGraph {
	projectDir := h.resolveProjectDir(project, params...)
	cacheKey := projectDir
	if cacheKey == "" {
		cacheKey = project
	}

	h.codeGraphMu.RLock()
	if entry, ok := h.codeGraphs[cacheKey]; ok {
		h.codeGraphMu.RUnlock()
		return entry.graph
	}
	h.codeGraphMu.RUnlock()

	if projectDir == "" {
		return nil
	}

	h.codeGraphMu.Lock()
	defer h.codeGraphMu.Unlock()

	// Double-check after acquiring write lock
	if entry, ok := h.codeGraphs[cacheKey]; ok {
		return entry.graph
	}

	var inner codescan.Scanner
	if codescan.FindCBMBinary() != "" {
		inner = codescan.NewCBMScanner()
	} else {
		inner = &codescan.DirectoryScanner{}
	}
	scanner := codescan.NewCachedScanner(inner).WithStore(h.store)
	result, err := scanner.Scan(projectDir)
	if err != nil {
		return nil
	}

	graph := codescan.BuildCodeGraph(result)

	if h.codeGraphs == nil {
		h.codeGraphs = make(map[string]*codeGraphEntry)
	}
	h.codeGraphs[cacheKey] = &codeGraphEntry{
		graph:   graph,
		scanner: scanner,
	}
	return graph
}

// resolveProjectDir returns the filesystem path for a project name.
// Prefers explicit project_dir or _cwd from params (worktree-aware) over stored path.
func (h *Handler) resolveProjectDir(project string, params ...map[string]any) string {
	if len(params) > 0 && params[0] != nil {
		if dir, _ := params[0]["project_dir"].(string); dir != "" {
			return dir
		}
		if cwd, _ := params[0]["_cwd"].(string); cwd != "" {
			return cwd
		}
	}
	if project == "" {
		return ""
	}
	return h.store.ResolveProjectPath(project)
}

// handleSearchCodeIndex searches the code graph for symbols by pattern.
func (h *Handler) handleSearchCodeIndex(params map[string]any) Response {
	pattern, _ := params["pattern"].(string)
	kind, _ := params["kind"].(string)
	filePattern, _ := params["file_pattern"].(string)
	project, _ := params["project"].(string)
	limit := intParamCI(params, "limit", 20)

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	results := graph.SearchNodes(pattern, kind, filePattern)
	if len(results) > limit {
		results = results[:limit]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d matches\n\n", len(results)))
	for _, node := range results {
		b.WriteString(fmt.Sprintf("**%s** (%s)", node.QualifiedName, node.Kind))
		if node.File != "" {
			b.WriteString(fmt.Sprintf(" in `%s`", node.File))
		}
		b.WriteString("\n")
		if node.Signature != "" {
			b.WriteString(fmt.Sprintf("  %s\n", node.Signature))
		}
	}

	if len(results) == 0 {
		return textResponse("No matches found for pattern: " + pattern)
	}
	return textResponse(b.String())
}

// handleSearchCode greps source files for a pattern, then enriches matches with graph context.
// Unlike search_code_index which searches only graph metadata (names, signatures),
// this searches actual file content and shows containing functions + caller counts.
func (h *Handler) handleSearchCode(params map[string]any) Response {
	pattern, _ := params["pattern"].(string)
	project, _ := params["project"].(string)
	filePattern, _ := params["file_pattern"].(string)
	limit := intParamCI(params, "limit", 20)

	if pattern == "" {
		return errorResponse("pattern is required")
	}

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	projectDir := h.resolveProjectDir(project, params)
	if projectDir == "" {
		// Fall back to graph-only search if no directory available
		return h.searchCodeGraphOnly(graph, pattern, filePattern, limit)
	}

	// Grep source files for the pattern
	type grepMatch struct {
		file    string
		line    int
		content string
	}
	var matches []grepMatch
	patternLower := strings.ToLower(pattern)

	filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() && codescan.ShouldSkipDir(path, info, projectDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !codescan.IsSourceFile(path) {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)

		if filePattern != "" {
			isGlob := strings.ContainsAny(filePattern, "*?[")
			if isGlob {
				matchFile, _ := filepath.Match(filePattern, rel)
				matchBase, _ := filepath.Match(filePattern, filepath.Base(rel))
				if !matchFile && !matchBase {
					return nil
				}
			} else if !strings.Contains(rel, filePattern) {
				return nil
			}
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), patternLower) {
				matches = append(matches, grepMatch{file: rel, line: i + 1, content: strings.TrimSpace(line)})
				if len(matches) >= limit*3 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	// Deduplicate into containing graph nodes
	type enrichedMatch struct {
		node        *codescan.CodeNode
		matchLines  []string
		callerCount int
	}
	seen := make(map[string]*enrichedMatch)
	var ordered []string

	for _, m := range matches {
		// Find containing graph node for this file
		node := graph.FindNodeByFile(m.file)
		key := m.file
		if node != nil {
			key = node.QualifiedName
		}

		em, ok := seen[key]
		if !ok {
			em = &enrichedMatch{node: node}
			if node != nil {
				inbound := graph.EdgesTo(node.QualifiedName)
				for _, e := range inbound {
					if e.Kind != "defines" {
						em.callerCount++
					}
				}
			}
			seen[key] = em
			ordered = append(ordered, key)
		}
		if len(em.matchLines) < 3 {
			em.matchLines = append(em.matchLines, fmt.Sprintf("  L%d: %s", m.line, m.content))
		}
	}

	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d matches in %d locations for \"%s\"\n\n", len(matches), len(ordered), pattern))

	for _, key := range ordered {
		em := seen[key]
		if em.node != nil {
			b.WriteString(fmt.Sprintf("**%s** (%s) in `%s`\n", em.node.QualifiedName, em.node.Kind, em.node.File))
			if em.node.Signature != "" {
				b.WriteString(fmt.Sprintf("  `%s`\n", em.node.Signature))
			}
			if em.callerCount > 0 {
				b.WriteString(fmt.Sprintf("  %d callers/references\n", em.callerCount))
			}
		} else {
			b.WriteString(fmt.Sprintf("**%s** (unresolved)\n", key))
		}
		for _, ml := range em.matchLines {
			b.WriteString(ml + "\n")
		}
		b.WriteString("\n")
	}

	if len(matches) == 0 {
		return textResponse("No matches found for: " + pattern)
	}
	return textResponse(b.String())
}

// searchCodeGraphOnly is the fallback when project dir is not available.
func (h *Handler) searchCodeGraphOnly(graph *codescan.CodeGraph, pattern, filePattern string, limit int) Response {
	results := graph.SearchNodes(pattern, "", filePattern)
	if len(results) > limit {
		results = results[:limit]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Found %d results for \"%s\" (graph-only, no file access)\n\n", len(results), pattern))

	for _, node := range results {
		b.WriteString(fmt.Sprintf("**%s** (%s) in `%s`\n", node.QualifiedName, node.Kind, node.File))
		if node.Signature != "" {
			b.WriteString(fmt.Sprintf("  `%s`\n", node.Signature))
		}
		inbound := graph.EdgesTo(node.QualifiedName)
		callerCount := 0
		for _, e := range inbound {
			if e.Kind != "defines" {
				callerCount++
			}
		}
		if callerCount > 0 {
			b.WriteString(fmt.Sprintf("  %d callers/references\n", callerCount))
		}
		b.WriteString("\n")
	}

	if len(results) == 0 {
		return textResponse("No matches found for: " + pattern)
	}
	return textResponse(b.String())
}

// handleGetCodeContext reads source and graph context for a specific symbol.
func (h *Handler) handleGetCodeContext(params map[string]any) Response {
	qualifiedName, _ := params["qualified_name"].(string)
	project, _ := params["project"].(string)
	includeNeighbors := boolParam(params, "include_neighbors", false)

	if qualifiedName == "" {
		return errorResponse("qualified_name is required")
	}

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	node := graph.GetNode(qualifiedName)
	if node == nil {
		// Try fuzzy search
		results := graph.SearchNodes(qualifiedName, "", "")
		if len(results) == 0 {
			return errorResponse("symbol not found: " + qualifiedName)
		}
		node = results[0]
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s** (%s)\n", node.QualifiedName, node.Kind))
	if node.File != "" {
		b.WriteString(fmt.Sprintf("File: `%s`\n", node.File))
	}
	if node.Signature != "" {
		b.WriteString(fmt.Sprintf("Signature: `%s`\n", node.Signature))
	}

	if includeNeighbors {
		inbound := graph.EdgesTo(node.QualifiedName)
		if len(inbound) > 0 {
			b.WriteString("\n**Referenced by:**\n")
			for _, e := range inbound {
				if e.Kind != "defines" {
					b.WriteString(fmt.Sprintf("- %s (%s)\n", e.From, e.Kind))
				}
			}
		}

		outbound := graph.EdgesFrom(node.QualifiedName)
		if len(outbound) > 0 {
			b.WriteString("\n**References:**\n")
			for _, e := range outbound {
				b.WriteString(fmt.Sprintf("- %s (%s)\n", e.To, e.Kind))
			}
		}
	}

	return textResponse(b.String())
}

// handleGetDependencyMap returns the import graph for a package.
func (h *Handler) handleGetDependencyMap(params map[string]any) Response {
	pkg, _ := params["package"].(string)
	project, _ := params["project"].(string)
	depth := intParamCI(params, "depth", 2)

	if pkg == "" {
		return errorResponse("package is required")
	}

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	node := graph.GetNode(pkg)
	if node == nil {
		return errorResponse("package not found: " + pkg)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s** dependencies (depth %d):\n\n", pkg, depth))

	paths := graph.Traverse(pkg, "outbound", "imports", depth)
	if len(paths) == 0 {
		b.WriteString("No dependencies found.\n")
	} else {
		for _, path := range paths {
			indent := strings.Repeat("  ", len(path)-2)
			target := path[len(path)-1]
			b.WriteString(fmt.Sprintf("%s→ %s\n", indent, target))
		}
	}

	// Reverse: who depends on this package?
	b.WriteString(fmt.Sprintf("\n**Dependents** (who imports %s):\n", pkg))
	revPaths := graph.Traverse(pkg, "inbound", "imports", depth)
	if len(revPaths) == 0 {
		b.WriteString("No dependents found.\n")
	} else {
		for _, path := range revPaths {
			b.WriteString(fmt.Sprintf("← %s\n", path[len(path)-1]))
		}
	}

	// Cycle detection
	cycles := graph.DetectCycles()
	relevantCycles := [][]string{}
	for _, cycle := range cycles {
		for _, node := range cycle {
			if node == pkg {
				relevantCycles = append(relevantCycles, cycle)
				break
			}
		}
	}
	if len(relevantCycles) > 0 {
		b.WriteString("\n**Cycles detected:**\n")
		for _, cycle := range relevantCycles {
			b.WriteString("  " + strings.Join(cycle, " → ") + "\n")
		}
	} else {
		b.WriteString("\n**Cycles:** none\n")
	}

	return textResponse(b.String())
}

// handleGraphTraverse performs BFS/DFS traversal on the code graph.
func (h *Handler) handleGraphTraverse(params map[string]any) Response {
	from, _ := params["from"].(string)
	direction, _ := params["direction"].(string)
	edgeType, _ := params["edge_type"].(string)
	project, _ := params["project"].(string)
	depth := intParamCI(params, "depth", 3)

	if from == "" {
		return errorResponse("from is required")
	}
	if direction == "" {
		direction = "outbound"
	}

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	paths := graph.Traverse(from, direction, edgeType, depth)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Traversal from **%s** (%s, edge=%s, depth=%d):\n\n", from, direction, edgeType, depth))

	if len(paths) == 0 {
		b.WriteString("No paths found.\n")
	} else {
		for _, path := range paths {
			b.WriteString(strings.Join(path, " → ") + "\n")
		}
		b.WriteString(fmt.Sprintf("\n%d paths found.\n", len(paths)))
	}

	return textResponse(b.String())
}

// boolParam extracts a boolean parameter with a default.
func boolParam(params map[string]any, key string, defaultVal bool) bool {
	v, ok := params[key].(bool)
	if !ok {
		return defaultVal
	}
	return v
}

// intParam extracts an integer parameter with a default.
// Handles both float64 (JSON numbers) and int.
// handleGetFileIndex lists source files in a directory with learning annotations.
// Provides on-demand Ebene 3 detail: file list + knowledge density per file.
func (h *Handler) handleGetCodeSnippet(params map[string]any) Response {
	qualifiedName, _ := params["qualified_name"].(string)
	project, _ := params["project"].(string)
	file, _ := params["file"].(string)
	startLine, _ := params["start_line"].(float64)
	endLine, _ := params["end_line"].(float64)

	// Range mode: file + start_line + end_line
	if file != "" && startLine > 0 && endLine > 0 {
		projectDir := h.resolveProjectDir(project, params)
		if projectDir == "" {
			return errorResponse("cannot resolve project directory for: " + project)
		}
		filePath := filepath.Join(projectDir, file)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return errorResponse(fmt.Sprintf("cannot read file %s: %v", file, err))
		}
		result := codescan.ExtractRange(string(data), int(startLine), int(endLine))
		if result == "" {
			return errorResponse(fmt.Sprintf("line range %d-%d out of bounds in %s", int(startLine), int(endLine), file))
		}
		return textResponse(fmt.Sprintf("**%s** lines %d-%d\n\n```go\n%s\n```\n", file, int(startLine), int(endLine), result))
	}

	// Symbol mode: qualified_name
	if qualifiedName == "" {
		return errorResponse("qualified_name or (file + start_line + end_line) required")
	}

	graph := h.getCodeGraph(project, params)
	if graph == nil {
		return errorResponse("no code index available for project: " + project)
	}

	node := graph.GetNode(qualifiedName)
	if node == nil {
		results := graph.SearchNodes(qualifiedName, "", "")
		if len(results) == 0 {
			return errorResponse("symbol not found: " + qualifiedName)
		}
		node = results[0]
	}

	if node.File == "" {
		return errorResponse("no file path for symbol: " + qualifiedName)
	}

	projectDir := h.resolveProjectDir(project, params)
	if projectDir == "" {
		return errorResponse("cannot resolve project directory for: " + project)
	}

	filePath := filepath.Join(projectDir, node.File)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResponse(fmt.Sprintf("cannot read file %s: %v", node.File, err))
	}

	shortName := node.QualifiedName
	if idx := strings.LastIndex(shortName, "."); idx >= 0 {
		shortName = shortName[idx+1:]
	}

	body := codescan.ExtractSymbol(string(data), shortName)
	if body == "" {
		return errorResponse(fmt.Sprintf("symbol %s not found in %s", shortName, node.File))
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s** in `%s`\n\n```go\n%s\n```\n", node.QualifiedName, node.File, body))
	return textResponse(b.String())
}

func (h *Handler) handleGetFileIndex(params map[string]any) Response {
	dir, _ := params["dir"].(string)
	project, _ := params["project"].(string)

	if project == "" {
		return errorResponse("project is required")
	}

	projectDir := h.resolveProjectDir(project, params)
	if projectDir == "" {
		return errorResponse("no project directory found for: " + project)
	}

	targetDir := projectDir
	if dir != "" {
		targetDir = filepath.Join(projectDir, dir)
	}

	// Collect source files
	type fileEntry struct {
		relPath string
		size    int64
	}
	var files []fileEntry

	filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if codescan.ShouldSkipDir(path, info, targetDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !codescan.IsSourceFile(path) {
			return nil
		}
		rel, _ := filepath.Rel(targetDir, path)
		files = append(files, fileEntry{relPath: rel, size: info.Size()})
		return nil
	})

	// Get learning counts for annotations
	counts, _ := h.store.GetLearningCountsByEntity(project)

	var b strings.Builder
	if dir != "" {
		b.WriteString(fmt.Sprintf("File index for `%s` in project %s (%d files)\n\n", dir, project, len(files)))
	} else {
		b.WriteString(fmt.Sprintf("File index for project %s (%d files)\n\n", project, len(files)))
	}

	for _, f := range files {
		base := filepath.Base(f.relPath)
		annotation := ""

		// Check learning counts for this file
		ec := counts[base]
		if pathEC, ok := counts[f.relPath]; ok {
			ec.Total += pathEC.Total
			ec.Gotchas += pathEC.Gotchas
		}

		if ec.Total > 0 {
			if ec.Gotchas > 0 {
				annotation = fmt.Sprintf(" (%d learnings, %d gotchas)", ec.Total, ec.Gotchas)
			} else {
				annotation = fmt.Sprintf(" (%d learnings)", ec.Total)
			}
		}

		b.WriteString(fmt.Sprintf("  %s%s\n", f.relPath, annotation))
	}

	if len(files) == 0 {
		return textResponse("No source files found in: " + dir)
	}
	return textResponse(b.String())
}

func intParamCI(params map[string]any, key string, defaultVal int) int {
	if v, ok := params[key].(float64); ok {
		return int(v)
	}
	if v, ok := params[key].(int); ok {
		return v
	}
	return defaultVal
}

func (h *Handler) handleGetFileSymbols(params map[string]any) Response {
	file, _ := params["file"].(string)
	project, _ := params["project"].(string)

	if file == "" {
		return errorResponse("file is required")
	}

	projectDir := h.resolveProjectDir(project, params)
	if projectDir == "" {
		return errorResponse("cannot resolve project directory for: " + project)
	}

	filePath := filepath.Join(projectDir, file)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResponse(fmt.Sprintf("cannot read file %s: %v", file, err))
	}

	symbols := codescan.ParseFileSymbols(string(data))
	if len(symbols) == 0 {
		return textResponse(fmt.Sprintf("No symbols found in `%s`\n", file))
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**%s** — %d symbols\n\n", file, len(symbols)))
	for _, s := range symbols {
		b.WriteString(fmt.Sprintf("L%-4d  %-8s %s\n", s.Line, s.Kind, s.Signature))
	}
	return textResponse(b.String())
}

// textResponse wraps a text string as a JSON-encoded Response.
func textResponse(text string) Response {
	return jsonResponse(map[string]string{"text": text})
}
