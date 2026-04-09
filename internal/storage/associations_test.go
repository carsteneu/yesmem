package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestAssociationRelationTypeField(t *testing.T) {
	s := mustOpen(t)

	assoc := &models.Association{
		SourceType:   "learning",
		SourceID:     "42",
		TargetType:   "learning",
		TargetID:     "99",
		Weight:       1.0,
		RelationType: "supports",
	}
	if err := s.UpsertAssociation(assoc); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	edges, err := s.GetAssociationsFrom("learning", "42")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].RelationType != "supports" {
		t.Errorf("expected relation_type='supports', got %q", edges[0].RelationType)
	}
}

func TestAssociationDefaultRelationType(t *testing.T) {
	s := mustOpen(t)

	assoc := &models.Association{
		SourceType: "session",
		SourceID:   "abc",
		TargetType: "file",
		TargetID:   "main.go",
		Weight:     1.0,
	}
	if err := s.UpsertAssociation(assoc); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	edges, err := s.GetAssociationsFrom("session", "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].RelationType != "related" {
		t.Errorf("expected relation_type='related', got %q", edges[0].RelationType)
	}
}

func TestInsertTypedAssociation(t *testing.T) {
	s := mustOpen(t)

	if err := s.InsertTypedAssociation(10, 20, "supports"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	edges, err := s.GetAssociationsByRelationType("learning", "10", "supports")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(edges) != 1 || edges[0].TargetID != "20" {
		t.Errorf("expected 1 edge to 20, got %v", edges)
	}
	if edges[0].RelationType != "supports" {
		t.Errorf("expected 'supports', got %q", edges[0].RelationType)
	}
}

func TestInsertTypedAssociation_UpdateRelationType(t *testing.T) {
	s := mustOpen(t)

	if err := s.InsertTypedAssociation(10, 20, "supports"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := s.InsertTypedAssociation(10, 20, "contradicts"); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	edges, err := s.GetAssociationsByRelationType("learning", "10", "contradicts")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("expected 1 edge with contradicts, got %d", len(edges))
	}

	old, _ := s.GetAssociationsByRelationType("learning", "10", "supports")
	if len(old) != 0 {
		t.Errorf("expected old 'supports' edge to be replaced, got %d edges", len(old))
	}
}

func TestGetAssociationsByRelationType_Empty(t *testing.T) {
	s := mustOpen(t)

	edges, err := s.GetAssociationsByRelationType("learning", "99", "supports")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

func TestGetLearningsWithEntityOverlap(t *testing.T) {
	s := mustOpen(t)

	l1 := &models.Learning{Category: "gotcha", Content: "proxy.go causes issues when X", Source: "llm_extracted", ModelUsed: "test", CreatedAt: time.Now(), Entities: []string{"proxy.go", "internal/proxy"}}
	id1, err := s.InsertLearning(l1)
	if err != nil {
		t.Fatalf("save l1: %v", err)
	}

	l2 := &models.Learning{Category: "decision", Content: "proxy.go was refactored for Y", Source: "llm_extracted", ModelUsed: "test", CreatedAt: time.Now(), Entities: []string{"proxy.go", "handler.go"}}
	id2, err := s.InsertLearning(l2)
	if err != nil {
		t.Fatalf("save l2: %v", err)
	}

	l3 := &models.Learning{Category: "pattern", Content: "unrelated learning", Source: "llm_extracted", ModelUsed: "test", CreatedAt: time.Now(), Entities: []string{"schema.go"}}
	_, err = s.InsertLearning(l3)
	if err != nil {
		t.Fatalf("save l3: %v", err)
	}

	matches, err := s.GetLearningsWithEntityOverlap([]string{"proxy.go", "internal/proxy"}, id1, 10)
	if err != nil {
		t.Fatalf("overlap: %v", err)
	}
	if len(matches) != 1 || matches[0] != id2 {
		t.Errorf("expected [%d], got %v", id2, matches)
	}
}

func TestGetLearningsWithEntityOverlap_Empty(t *testing.T) {
	s := mustOpen(t)

	matches, err := s.GetLearningsWithEntityOverlap([]string{}, 1, 10)
	if err != nil {
		t.Fatalf("overlap: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0, got %d", len(matches))
	}
}

func TestGetAssociationNeighbors(t *testing.T) {
	s := mustOpen(t)

	// Create learnings so the JOIN finds them
	l10 := &models.Learning{Content: "learning 10", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	id10, _ := s.InsertLearning(l10)
	l20 := &models.Learning{Content: "learning 20", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	id20, _ := s.InsertLearning(l20)
	l30 := &models.Learning{Content: "learning 30", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	id30, _ := s.InsertLearning(l30)
	l40 := &models.Learning{Content: "learning 40", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	id40, _ := s.InsertLearning(l40)
	l50 := &models.Learning{Content: "learning 50", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	id50, _ := s.InsertLearning(l50)

	if err := s.InsertTypedAssociation(id10, id20, "supports"); err != nil {
		t.Fatalf("insert 10→20: %v", err)
	}
	if err := s.InsertTypedAssociation(id10, id30, "depends_on"); err != nil {
		t.Fatalf("insert 10→30: %v", err)
	}
	if err := s.InsertTypedAssociation(id40, id50, "relates_to"); err != nil {
		t.Fatalf("insert 40→50: %v", err)
	}

	result, err := s.GetAssociationNeighbors([]string{fmt.Sprintf("%d", id10), fmt.Sprintf("%d", id40)}, 10)
	if err != nil {
		t.Fatalf("get neighbors: %v", err)
	}
	id10Str := fmt.Sprintf("%d", id10)
	id40Str := fmt.Sprintf("%d", id40)
	if len(result[id10Str]) != 2 {
		t.Errorf("expected 2 neighbors for %s, got %d", id10Str, len(result[id10Str]))
	}
	if len(result[id40Str]) != 1 {
		t.Errorf("expected 1 neighbor for %s, got %d", id40Str, len(result[id40Str]))
	}
}

func TestGetAssociationNeighbors_Empty(t *testing.T) {
	s := mustOpen(t)

	result, err := s.GetAssociationNeighbors([]string{}, 10)
	if err != nil {
		t.Fatalf("get neighbors: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestGetAssociationNeighbors_Limit(t *testing.T) {
	s := mustOpen(t)

	// Create source + 5 target learnings
	lSrc := &models.Learning{Content: "source", Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
	srcID, _ := s.InsertLearning(lSrc)
	for i := 0; i < 5; i++ {
		lt := &models.Learning{Content: fmt.Sprintf("target %d", i), Category: "gotcha", Source: "test", ModelUsed: "test", CreatedAt: time.Now()}
		tgtID, _ := s.InsertLearning(lt)
		if err := s.InsertTypedAssociation(srcID, tgtID, "relates_to"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	result, err := s.GetAssociationNeighbors([]string{fmt.Sprintf("%d", srcID)}, 3)
	if err != nil {
		t.Fatalf("get neighbors: %v", err)
	}
	srcStr := fmt.Sprintf("%d", srcID)
	if len(result[srcStr]) != 3 {
		t.Errorf("expected 3 neighbors (limit), got %d", len(result[srcStr]))
	}
}

func TestGetContradictingPairs(t *testing.T) {
	s := mustOpen(t)

	// Create contradicts edge: 10 contradicts 20
	if err := s.InsertTypedAssociation(10, 20, "contradicts"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Create non-contradicts edge: 10 supports 30
	if err := s.InsertTypedAssociation(10, 30, "supports"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pairs, err := s.GetContradictingPairs([]int64{10}, []int64{20, 30})
	if err != nil {
		t.Fatalf("get pairs: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
	if pairs[0][0] != 10 || pairs[0][1] != 20 {
		t.Errorf("expected [10,20], got %v", pairs[0])
	}
}

func TestGetContradictingPairs_NoPairs(t *testing.T) {
	s := mustOpen(t)

	if err := s.InsertTypedAssociation(10, 20, "supports"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pairs, err := s.GetContradictingPairs([]int64{10}, []int64{20})
	if err != nil {
		t.Fatalf("get pairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("expected 0 pairs, got %d", len(pairs))
	}
}

func TestGetContradictingPairs_Bidirectional(t *testing.T) {
	s := mustOpen(t)

	// Edge goes from 20→10, but we query new=[10] vs previous=[20]
	// Should still find it since contradiction is symmetric
	if err := s.InsertTypedAssociation(20, 10, "contradicts"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pairs, err := s.GetContradictingPairs([]int64{10}, []int64{20})
	if err != nil {
		t.Fatalf("get pairs: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair (bidirectional), got %d", len(pairs))
	}
}

func TestGetContradictingPairs_Empty(t *testing.T) {
	s := mustOpen(t)

	pairs, err := s.GetContradictingPairs([]int64{}, []int64{20})
	if err != nil {
		t.Fatalf("get pairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("expected 0, got %d", len(pairs))
	}
}
