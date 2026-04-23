package codescan

import (
	"path/filepath"
	"strings"
)

// CodeNode represents a symbol in the code graph.
type CodeNode struct {
	QualifiedName string // e.g. "internal/proxy.CacheGet"
	Kind          string // "function", "class", "type", "method", "interface", "package"
	File          string // relative path
	Line          int
	Signature     string
}

// CodeEdge represents a directed relationship between two nodes.
type CodeEdge struct {
	From string // qualified_name
	To   string // qualified_name
	Kind string // "imports", "defines"
}

// CodeGraph is an in-memory directed graph of code symbols and relationships.
type CodeGraph struct {
	nodes    map[string]*CodeNode
	outEdges map[string][]CodeEdge
	inEdges  map[string][]CodeEdge
}

// NewCodeGraph creates an empty graph.
func NewCodeGraph() *CodeGraph {
	return &CodeGraph{
		nodes:    make(map[string]*CodeNode),
		outEdges: make(map[string][]CodeEdge),
		inEdges:  make(map[string][]CodeEdge),
	}
}

func (g *CodeGraph) AddNode(n CodeNode) {
	g.nodes[n.QualifiedName] = &n
}

func (g *CodeGraph) GetNode(qualifiedName string) *CodeNode {
	return g.nodes[qualifiedName]
}

func (g *CodeGraph) AddEdge(e CodeEdge) {
	// Deduplicate: skip if identical edge already exists
	for _, existing := range g.outEdges[e.From] {
		if existing.To == e.To && existing.Kind == e.Kind {
			return
		}
	}
	g.outEdges[e.From] = append(g.outEdges[e.From], e)
	g.inEdges[e.To] = append(g.inEdges[e.To], e)
}

func (g *CodeGraph) EdgesFrom(qualifiedName string) []CodeEdge {
	return g.outEdges[qualifiedName]
}

func (g *CodeGraph) EdgesTo(qualifiedName string) []CodeEdge {
	return g.inEdges[qualifiedName]
}

func (g *CodeGraph) NodeCount() int {
	return len(g.nodes)
}

// FindNodeByFile finds a non-package graph node matching the given file path.
// If multiple nodes exist in the same file, returns the first alphabetically
// by QualifiedName for deterministic results.
func (g *CodeGraph) FindNodeByFile(file string) *CodeNode {
	var best *CodeNode
	for _, node := range g.nodes {
		if node.File == file && node.Kind != "package" {
			if best == nil || node.QualifiedName < best.QualifiedName {
				best = node
			}
		}
	}
	return best
}

// SearchNodes finds nodes matching optional filters.
// pattern: case-insensitive substring match on QualifiedName and Signature.
// kind: exact match on node Kind.
// filePattern: glob match on File path (e.g. "*.go", "proxy/*"), or substring if no glob chars.
func (g *CodeGraph) SearchNodes(pattern, kind, filePattern string) []*CodeNode {
	var results []*CodeNode
	patternLower := strings.ToLower(pattern)
	isGlob := strings.ContainsAny(filePattern, "*?[")

	for _, node := range g.nodes {
		if kind != "" && node.Kind != kind {
			continue
		}
		if filePattern != "" {
			if isGlob {
				matchFile, _ := filepath.Match(filePattern, node.File)
				matchBase, _ := filepath.Match(filePattern, filepath.Base(node.File))
				if !matchFile && !matchBase {
					continue
				}
			} else {
				if !strings.Contains(node.File, filePattern) && !strings.Contains(node.QualifiedName, filePattern) {
					continue
				}
			}
		}
		if pattern != "" {
			nameLower := strings.ToLower(node.QualifiedName + " " + node.Signature)
			if !strings.Contains(nameLower, patternLower) {
				continue
			}
		}
		results = append(results, node)
	}
	return results
}

// Traverse performs BFS from a starting node, returning all discovered paths.
// direction: "outbound", "inbound", or "both".
// edgeKind: filter edges by kind (empty = all).
// maxDepth: maximum path length.
func (g *CodeGraph) Traverse(from, direction, edgeKind string, maxDepth int) [][]string {
	var allPaths [][]string
	visited := make(map[string]bool)

	type item struct {
		node string
		path []string
	}

	queue := []item{{node: from, path: []string{from}}}
	visited[from] = true

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if len(cur.path) > maxDepth {
			continue
		}

		var edges []CodeEdge
		switch direction {
		case "outbound":
			edges = g.outEdges[cur.node]
		case "inbound":
			edges = g.inEdges[cur.node]
		case "both":
			edges = append(append([]CodeEdge{}, g.outEdges[cur.node]...), g.inEdges[cur.node]...)
		}

		for _, edge := range edges {
			target := edge.To
			if direction == "inbound" {
				target = edge.From
			} else if direction == "both" && edge.From != cur.node {
				target = edge.From
			}

			if edgeKind != "" && edge.Kind != edgeKind {
				continue
			}
			if visited[target] {
				continue
			}

			newPath := make([]string, len(cur.path)+1)
			copy(newPath, cur.path)
			newPath[len(cur.path)] = target
			allPaths = append(allPaths, newPath)

			visited[target] = true
			queue = append(queue, item{node: target, path: newPath})
		}
	}

	return allPaths
}

// DetectCycles finds import cycles using DFS coloring.
func (g *CodeGraph) DetectCycles() [][]string {
	var cycles [][]string
	white := make(map[string]bool)
	gray := make(map[string]bool)
	pathIdx := make(map[string]int)
	var currentPath []string

	for name := range g.nodes {
		white[name] = true
	}

	var dfs func(node string)
	dfs = func(node string) {
		delete(white, node)
		gray[node] = true
		pathIdx[node] = len(currentPath)
		currentPath = append(currentPath, node)

		for _, edge := range g.outEdges[node] {
			if edge.Kind != "imports" {
				continue
			}
			if gray[edge.To] {
				cycleStart := pathIdx[edge.To]
				cycle := make([]string, len(currentPath)-cycleStart)
				copy(cycle, currentPath[cycleStart:])
				cycle = append(cycle, edge.To)
				cycles = append(cycles, cycle)
			} else if white[edge.To] {
				dfs(edge.To)
			}
		}

		delete(gray, node)
		delete(pathIdx, node)
		currentPath = currentPath[:len(currentPath)-1]
	}

	for name := range g.nodes {
		if white[name] {
			dfs(name)
		}
	}

	return cycles
}

// BuildCodeGraph constructs a CodeGraph from a ScanResult.
func BuildCodeGraph(result *ScanResult) *CodeGraph {
	g := NewCodeGraph()

	pkgNames := make(map[string]bool)
	for _, pkg := range result.Packages {
		pkgNames[pkg.Name] = true
	}

	for _, pkg := range result.Packages {
		g.AddNode(CodeNode{
			QualifiedName: pkg.Name,
			Kind:          "package",
		})

		for _, f := range pkg.Files {
			if f.IsTest {
				continue
			}
			for _, sig := range f.Signatures {
				name := extractNameFromSig(sig)
				qn := pkg.Name + "." + name
				g.AddNode(CodeNode{
					QualifiedName: qn,
					Kind:          classifySignature(sig),
					File:          f.Path,
					Signature:     sig,
				})
				g.AddEdge(CodeEdge{From: pkg.Name, To: qn, Kind: "defines"})
			}

			for _, imp := range f.Imports {
				target := imp
				if !pkgNames[target] {
					// Try matching base name against package base names
					base := filepath.Base(imp)
					for pn := range pkgNames {
						if filepath.Base(pn) == base {
							target = pn
							break
						}
					}
				}
				if pkgNames[target] && target != pkg.Name {
					g.AddEdge(CodeEdge{From: pkg.Name, To: target, Kind: "imports"})
				}
			}
		}
	}

	return g
}

// extractNameFromSig extracts the symbol name from a signature string.
func extractNameFromSig(sig string) string {
	for _, prefix := range []string{"func ", "type ", "class ", "struct ", "def ", "interface ", "trait ", "pub fn ", "fn "} {
		if strings.HasPrefix(sig, prefix) {
			rest := sig[len(prefix):]
			if strings.HasPrefix(rest, "(") {
				if idx := strings.Index(rest, ") "); idx >= 0 {
					rest = rest[idx+2:]
				}
			}
			end := strings.IndexAny(rest, "( {<:")
			if end > 0 {
				return rest[:end]
			}
			end = strings.IndexAny(rest, " \n")
			if end > 0 {
				return rest[:end]
			}
			return rest
		}
	}
	if idx := strings.IndexAny(sig, " ({\n"); idx > 0 {
		return sig[:idx]
	}
	return sig
}

// classifySignature guesses the kind from a signature string.
func classifySignature(sig string) string {
	lower := strings.ToLower(sig)
	switch {
	case strings.HasPrefix(lower, "func ("):
		return "method"
	case strings.HasPrefix(lower, "func "):
		return "function"
	case strings.Contains(lower, "struct"):
		return "type"
	case strings.Contains(lower, "interface"):
		return "interface"
	case strings.Contains(lower, "class"):
		return "class"
	case strings.Contains(lower, "trait"):
		return "trait"
	case strings.HasPrefix(lower, "const "):
		return "constant"
	case strings.HasPrefix(lower, "var "):
		return "variable"
	case strings.HasPrefix(lower, "def "):
		return "function"
	default:
		return "function"
	}
}
