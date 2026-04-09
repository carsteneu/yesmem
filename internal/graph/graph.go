package graph

import (
	"sync"

	"github.com/carsteneu/yesmem/internal/models"
)

// Node represents a node in the association graph.
type Node struct {
	Type string `json:"type"` // session, file, command, project, concept
	ID   string `json:"id"`
}

// Edge represents a directed edge with weight.
type Edge struct {
	Target Node
	Weight float64
}

// Graph is an in-memory adjacency list for association traversal.
type Graph struct {
	mu    sync.RWMutex
	edges map[Node][]Edge
}

// New creates an empty graph.
func New() *Graph {
	return &Graph{
		edges: make(map[Node][]Edge),
	}
}

// AddEdge adds a bidirectional edge between two nodes.
func (g *Graph) AddEdge(srcType, srcID, tgtType, tgtID string, weight float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	src := Node{Type: srcType, ID: srcID}
	tgt := Node{Type: tgtType, ID: tgtID}

	g.edges[src] = append(g.edges[src], Edge{Target: tgt, Weight: weight})
	g.edges[tgt] = append(g.edges[tgt], Edge{Target: src, Weight: weight})
}

// GetRelated traverses the graph from a starting node up to `depth` hops.
// Returns all reachable nodes excluding the start node.
func (g *Graph) GetRelated(nodeType, nodeID string, depth int) []Node {
	g.mu.RLock()
	defer g.mu.RUnlock()

	start := Node{Type: nodeType, ID: nodeID}
	visited := map[Node]bool{start: true}
	var result []Node

	current := []Node{start}
	for d := 0; d < depth; d++ {
		var next []Node
		for _, node := range current {
			for _, edge := range g.edges[node] {
				if !visited[edge.Target] {
					visited[edge.Target] = true
					result = append(result, edge.Target)
					next = append(next, edge.Target)
				}
			}
		}
		current = next
	}

	return result
}

// GetRelatedToFile returns session IDs that have worked with a file.
func (g *Graph) GetRelatedToFile(filePath string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	fileNode := Node{Type: "file", ID: filePath}
	var sessions []string

	for _, edge := range g.edges[fileNode] {
		if edge.Target.Type == "session" {
			sessions = append(sessions, edge.Target.ID)
		}
	}
	return sessions
}

// LoadFromAssociations loads edges from a slice of Association models.
func (g *Graph) LoadFromAssociations(assocs []models.Association) {
	for _, a := range assocs {
		g.AddEdge(a.SourceType, a.SourceID, a.TargetType, a.TargetID, a.Weight)
	}
}

// NodeCount returns the number of unique nodes.
func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.edges)
}

// EdgeCount returns the total number of directed edges (each bidirectional pair = 2).
func (g *Graph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	count := 0
	for _, edges := range g.edges {
		count += len(edges)
	}
	return count / 2 // bidirectional
}
