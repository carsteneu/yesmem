package storage

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestFloat32BlobRoundtrip(t *testing.T) {
	input := []float32{0.1, 0.2, 0.3, -0.5, 1.0, 0.0}
	blob := float32ToBlob(input)
	if len(blob) != len(input)*4 {
		t.Fatalf("expected %d bytes, got %d", len(input)*4, len(blob))
	}
	output := blobToFloat32(blob)
	if len(output) != len(input) {
		t.Fatalf("expected %d floats, got %d", len(input), len(output))
	}
	for i := range input {
		if output[i] != input[i] {
			t.Errorf("index %d: expected %f, got %f", i, input[i], output[i])
		}
	}
}

func TestBlobToFloat32_Empty(t *testing.T) {
	if got := blobToFloat32(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := blobToFloat32([]byte{1, 2}); got != nil {
		t.Errorf("expected nil for short blob, got %v", got)
	}
}

func TestFindNearestCluster(t *testing.T) {
	clusters := []QueryCluster{
		{ID: 1, CentroidVector: []float32{1, 0, 0}},
		{ID: 2, CentroidVector: []float32{0, 1, 0}},
		{ID: 3, CentroidVector: []float32{0, 0, 1}},
	}

	// Exact match to cluster 2
	got := FindNearestCluster([]float32{0, 1, 0}, clusters, 0.80)
	if got != 2 {
		t.Errorf("expected cluster 2, got %d", got)
	}

	// Close to cluster 1
	got = FindNearestCluster([]float32{0.9, 0.1, 0}, clusters, 0.80)
	if got != 1 {
		t.Errorf("expected cluster 1, got %d", got)
	}

	// Nothing above threshold
	got = FindNearestCluster([]float32{0.5, 0.5, 0.5}, clusters, 0.95)
	if got != 0 {
		t.Errorf("expected 0 (no match), got %d", got)
	}

	// Empty clusters
	got = FindNearestCluster([]float32{1, 0, 0}, nil, 0.80)
	if got != 0 {
		t.Errorf("expected 0 for empty clusters, got %d", got)
	}
}

func TestCosineSimilarity32(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0},
		{"empty", nil, nil, 0.0},
		{"length_mismatch", []float32{1}, []float32{1, 2}, 0.0},
		{"zero_vector", []float32{0, 0, 0}, []float32{1, 0, 0}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity32(tt.a, tt.b)
			if abs(got-tt.want) > 0.001 {
				t.Errorf("got %f, want %f", got, tt.want)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestQueryClusterCRUD(t *testing.T) {
	s := mustOpen(t)

	// Insert
	id, err := s.SaveQueryCluster(QueryCluster{
		Project:        "yesmem",
		CentroidVector: []float32{0.5, 0.5, 0.0},
		Label:          "test cluster",
		QueryCount:     10,
	})
	if err != nil {
		t.Fatalf("save cluster: %v", err)
	}
	if id <= 0 {
		t.Fatal("expected positive id")
	}

	// Get
	clusters, err := s.GetQueryClusters("yesmem")
	if err != nil {
		t.Fatalf("get clusters: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	c := clusters[0]
	if c.Label != "test cluster" {
		t.Errorf("expected label 'test cluster', got '%s'", c.Label)
	}
	if c.QueryCount != 10 {
		t.Errorf("expected query_count 10, got %d", c.QueryCount)
	}
	if len(c.CentroidVector) != 3 {
		t.Errorf("expected 3-dim centroid, got %d", len(c.CentroidVector))
	}

	// Update
	c.QueryCount = 20
	c.Label = "updated"
	_, err = s.SaveQueryCluster(c)
	if err != nil {
		t.Fatalf("update cluster: %v", err)
	}
	clusters, _ = s.GetQueryClusters("yesmem")
	if clusters[0].QueryCount != 20 {
		t.Errorf("expected updated query_count 20, got %d", clusters[0].QueryCount)
	}

	// Filter by project
	empty, _ := s.GetQueryClusters("nonexistent")
	if len(empty) != 0 {
		t.Errorf("expected 0 clusters for nonexistent project, got %d", len(empty))
	}
}

func TestQueryLogAndClustering(t *testing.T) {
	s := mustOpen(t)

	// Insert query logs
	s.InsertQueryLog("yesmem", "test query 1", []float32{1, 0, 0}, []string{"101", "102"})
	s.InsertQueryLog("yesmem", "test query 2", []float32{0, 1, 0}, []string{"103"})
	s.InsertQueryLog("yesmem", "test query 3", []float32{0.9, 0.1, 0}, []string{"101"})

	// All should be unclustered
	unclustered, err := s.GetUnclusteredQueries(100)
	if err != nil {
		t.Fatalf("get unclustered: %v", err)
	}
	if len(unclustered) != 3 {
		t.Fatalf("expected 3 unclustered, got %d", len(unclustered))
	}

	// Check InjectedLearningIDs is loaded
	found := false
	for _, q := range unclustered {
		if q.QueryText == "test query 1" && q.InjectedLearningIDs == "101,102" {
			found = true
		}
	}
	if !found {
		t.Error("expected injected_learning_ids to be loaded")
	}

	// Assign to cluster
	clusterID, _ := s.SaveQueryCluster(QueryCluster{
		Project:        "yesmem",
		CentroidVector: []float32{1, 0, 0},
		Label:          "cluster A",
		QueryCount:     1,
	})
	s.AssignQueryToCluster(unclustered[0].ID, clusterID)

	// Now only 2 unclustered
	unclustered, _ = s.GetUnclusteredQueries(100)
	if len(unclustered) != 2 {
		t.Errorf("expected 2 unclustered after assignment, got %d", len(unclustered))
	}
}

func TestClusterScores(t *testing.T) {
	s := mustOpen(t)

	// Create a cluster first (FK constraint)
	clusterID, _ := s.SaveQueryCluster(QueryCluster{
		Project:        "yesmem",
		CentroidVector: []float32{1, 0, 0},
		Label:          "test",
		QueryCount:     1,
	})

	// Increment inject
	if err := s.IncrementClusterScore(101, clusterID, "inject"); err != nil {
		t.Fatalf("increment inject: %v", err)
	}
	if err := s.IncrementClusterScore(101, clusterID, "inject"); err != nil {
		t.Fatalf("increment inject 2: %v", err)
	}
	if err := s.IncrementClusterScore(101, clusterID, "use"); err != nil {
		t.Fatalf("increment use: %v", err)
	}
	if err := s.IncrementClusterScore(101, clusterID, "noise"); err != nil {
		t.Fatalf("increment noise: %v", err)
	}

	// Get scores
	scores, err := s.GetClusterScoresForLearnings([]string{"101"})
	if err != nil {
		t.Fatalf("get scores: %v", err)
	}
	if len(scores["101"]) != 1 {
		t.Fatalf("expected 1 score entry, got %d", len(scores["101"]))
	}
	cs := scores["101"][0]
	if cs.InjectCount != 2 {
		t.Errorf("expected inject_count=2, got %d", cs.InjectCount)
	}
	if cs.UseCount != 1 {
		t.Errorf("expected use_count=1, got %d", cs.UseCount)
	}
	if cs.NoiseCount != 1 {
		t.Errorf("expected noise_count=1, got %d", cs.NoiseCount)
	}

	// Empty query
	empty, _ := s.GetClusterScoresForLearnings(nil)
	if empty != nil {
		t.Errorf("expected nil for empty IDs, got %v", empty)
	}
}

func TestPurgeOldQueryLogs(t *testing.T) {
	s := mustOpen(t)

	// Insert a query and cluster it
	s.InsertQueryLog("yesmem", "old query", []float32{1, 0, 0}, nil)
	clusterID, _ := s.SaveQueryCluster(QueryCluster{
		Project: "yesmem", CentroidVector: []float32{1, 0, 0}, QueryCount: 1,
	})

	unclustered, _ := s.GetUnclusteredQueries(100)
	if len(unclustered) != 1 {
		t.Fatalf("expected 1, got %d", len(unclustered))
	}
	s.AssignQueryToCluster(unclustered[0].ID, clusterID)

	// Backdate the clustered row to 60 days ago
	s.DB().Exec("UPDATE query_log SET created_at = datetime('now', '-60 days') WHERE cluster_id IS NOT NULL")

	// Insert an unclustered query
	s.InsertQueryLog("yesmem", "new unclustered", []float32{0, 1, 0}, nil)

	// Purge with 30 days should delete old clustered, keep unclustered
	purged, err := s.PurgeOldQueryLogs(30)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Errorf("expected 1 purged (clustered), got %d", purged)
	}

	// Unclustered should survive
	remaining, _ := s.GetUnclusteredQueries(100)
	if len(remaining) != 1 {
		t.Errorf("expected 1 surviving unclustered, got %d", len(remaining))
	}
}

func TestGetRecentClusterForLearnings(t *testing.T) {
	s := mustOpen(t)

	// Setup: query log with cluster assignment
	s.InsertQueryLog("yesmem", "test query", []float32{1, 0, 0}, []string{"101", "102"})
	clusterID, _ := s.SaveQueryCluster(QueryCluster{
		Project: "yesmem", CentroidVector: []float32{1, 0, 0}, QueryCount: 1,
	})
	unclustered, _ := s.GetUnclusteredQueries(100)
	s.AssignQueryToCluster(unclustered[0].ID, clusterID)

	// Should find cluster for learning 101
	result := s.GetRecentClusterForLearnings([]int64{101, 999})
	if result[101] != clusterID {
		t.Errorf("expected cluster %d for learning 101, got %d", clusterID, result[101])
	}
	if _, ok := result[999]; ok {
		t.Error("should not find cluster for learning 999")
	}

	// Empty input
	empty := s.GetRecentClusterForLearnings(nil)
	if len(empty) != 0 {
		t.Errorf("expected empty map, got %v", empty)
	}
}

func TestQuarantineSession(t *testing.T) {
	s := mustOpen(t)

	// Insert a learning with session_id
	l := &models.Learning{Content: "quarantine test", Category: "gotcha", SessionID: "sess-123"}
	id, _ := s.InsertLearning(l)

	// Quarantine the session
	affected, err := s.QuarantineSession("sess-123")
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if affected != 1 {
		t.Errorf("expected 1 affected, got %d", affected)
	}

	// Double quarantine should affect 0
	affected, _ = s.QuarantineSession("sess-123")
	if affected != 0 {
		t.Errorf("expected 0 on double quarantine, got %d", affected)
	}

	// Unquarantine
	affected, err = s.UnquarantineSession("sess-123")
	if err != nil {
		t.Fatalf("unquarantine: %v", err)
	}
	if affected != 1 {
		t.Errorf("expected 1 unquarantined, got %d", affected)
	}
	_ = id
}

func TestSkipExtraction(t *testing.T) {
	s := mustOpen(t)

	// Insert a session
	s.DB().Exec("INSERT INTO sessions (id, project, project_short, started_at, jsonl_path, indexed_at) VALUES ('sess-skip', 'test', 'test', '2026-01-01', '/tmp/x', '2026-01-01')")

	// Not skipped by default
	if s.IsExtractionSkipped("sess-skip") {
		t.Error("should not be skipped by default")
	}

	// Skip it
	if err := s.SkipExtraction("sess-skip"); err != nil {
		t.Fatalf("skip: %v", err)
	}

	if !s.IsExtractionSkipped("sess-skip") {
		t.Error("should be skipped after SkipExtraction")
	}

	// Nonexistent session
	if s.IsExtractionSkipped("nonexistent") {
		t.Error("nonexistent session should not be skipped")
	}
}

func TestSetImportanceAndStability(t *testing.T) {
	s := mustOpen(t)

	l := &models.Learning{
		Content:    "test learning",
		Category:   "gotcha",
		Importance: 2,
		Confidence: 1.0,
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// SetImportance
	if err := s.SetImportance(id, 5); err != nil {
		t.Fatalf("set importance: %v", err)
	}
	got, err := s.GetLearning(id)
	if err != nil {
		t.Fatalf("get learning: %v", err)
	}
	if got.Importance != 5 {
		t.Errorf("expected importance=5, got %d", got.Importance)
	}

	// SetStability
	if err = s.SetStability(id, 45.0); err != nil {
		t.Fatalf("set stability: %v", err)
	}
}
