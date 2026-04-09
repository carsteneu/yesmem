package graph

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestAddAndTraverse(t *testing.T) {
	g := New()

	g.AddEdge("session", "s1", "file", "/etc/nginx/conf", 1)
	g.AddEdge("session", "s1", "project", "ccm19", 1)
	g.AddEdge("session", "s2", "file", "/etc/nginx/conf", 1)
	g.AddEdge("session", "s2", "project", "ccm19", 1)
	g.AddEdge("session", "s3", "project", "green", 1)

	// From s1, depth 1: should find file and project nodes
	related := g.GetRelated("session", "s1", 1)
	if len(related) != 2 { // file + project
		t.Errorf("depth 1 from s1: expected 2, got %d: %v", len(related), related)
	}

	// From s1, depth 2: should also find s2 (via shared file /etc/nginx/conf)
	related2 := g.GetRelated("session", "s1", 2)
	found := false
	for _, n := range related2 {
		if n.Type == "session" && n.ID == "s2" {
			found = true
		}
	}
	if !found {
		t.Error("depth 2 from s1 should find s2 via shared file")
	}
}

func TestGetRelatedToFile(t *testing.T) {
	g := New()
	g.AddEdge("session", "s1", "file", "/var/www/index.php", 1)
	g.AddEdge("session", "s2", "file", "/var/www/index.php", 1)
	g.AddEdge("session", "s3", "file", "/var/www/other.php", 1)

	sessions := g.GetRelatedToFile("/var/www/index.php")
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions for index.php, got %d", len(sessions))
	}
}

func TestLoadFromAssociations(t *testing.T) {
	g := New()

	assocs := []models.Association{
		{SourceType: "session", SourceID: "s1", TargetType: "file", TargetID: "/a.txt", Weight: 1},
		{SourceType: "session", SourceID: "s1", TargetType: "command", TargetID: "docker", Weight: 2},
		{SourceType: "session", SourceID: "s2", TargetType: "file", TargetID: "/a.txt", Weight: 1},
	}
	g.LoadFromAssociations(assocs)

	// s1, s2, /a.txt, docker = 4 unique nodes
	if g.NodeCount() != 4 {
		t.Errorf("expected 4 nodes, got %d", g.NodeCount())
	}
	if g.EdgeCount() != 3 {
		t.Errorf("expected 3 edges, got %d", g.EdgeCount())
	}
}

func TestEmptyGraph(t *testing.T) {
	g := New()
	related := g.GetRelated("session", "nonexistent", 2)
	if len(related) != 0 {
		t.Error("empty graph should return no results")
	}
	files := g.GetRelatedToFile("/nonexistent")
	if len(files) != 0 {
		t.Error("empty graph should return no file results")
	}
}
