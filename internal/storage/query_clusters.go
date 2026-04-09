package storage

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// QueryCluster represents a cluster of semantically similar queries.
type QueryCluster struct {
	ID             int64
	Project        string
	CentroidVector []float32
	Label          string
	QueryCount     int
}

// ClusterScore tracks how a learning performs within a specific query cluster.
type ClusterScore struct {
	LearningID     int64
	ClusterID      int64
	InjectCount    int
	UseCount       int
	NoiseCount     int
}

// GetUnclusteredQueries returns query_log entries without a cluster assignment.
func (s *Store) GetUnclusteredQueries(limit int) ([]QueryLogEntry, error) {
	rows, err := s.readerDB().Query("SELECT id, project, query_text, query_vector, COALESCE(injected_learning_ids, '') FROM query_log WHERE cluster_id IS NULL AND query_vector IS NOT NULL ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []QueryLogEntry
	for rows.Next() {
		var e QueryLogEntry
		var vecBlob []byte
		if err := rows.Scan(&e.ID, &e.Project, &e.QueryText, &vecBlob, &e.InjectedLearningIDs); err != nil {
			continue
		}
		e.QueryVector = blobToFloat32(vecBlob)
		entries = append(entries, e)
	}
	return entries, nil
}

// QueryLogEntry represents a single query_log row with its vector.
type QueryLogEntry struct {
	ID                  int64
	Project             string
	QueryText           string
	QueryVector         []float32
	InjectedLearningIDs string
}

// GetQueryClusters returns all query clusters, optionally filtered by project.
func (s *Store) GetQueryClusters(project string) ([]QueryCluster, error) {
	q := "SELECT id, project, centroid_vector, label, query_count FROM query_clusters"
	var args []any
	if project != "" {
		q += " WHERE project = ?"
		args = append(args, project)
	}
	rows, err := s.readerDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var clusters []QueryCluster
	for rows.Next() {
		var c QueryCluster
		var vecBlob []byte
		if err := rows.Scan(&c.ID, &c.Project, &vecBlob, &c.Label, &c.QueryCount); err != nil {
			continue
		}
		c.CentroidVector = blobToFloat32(vecBlob)
		clusters = append(clusters, c)
	}
	return clusters, nil
}

// SaveQueryCluster inserts or updates a query cluster and returns its ID.
func (s *Store) SaveQueryCluster(c QueryCluster) (int64, error) {
	vecBlob := float32ToBlob(c.CentroidVector)
	if c.ID > 0 {
		_, err := s.db.Exec("UPDATE query_clusters SET centroid_vector = ?, label = ?, query_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			vecBlob, c.Label, c.QueryCount, c.ID)
		return c.ID, err
	}
	res, err := s.db.Exec("INSERT INTO query_clusters (project, centroid_vector, label, query_count) VALUES (?, ?, ?, ?)",
		c.Project, vecBlob, c.Label, c.QueryCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AssignQueryToCluster sets the cluster_id on a query_log entry.
func (s *Store) AssignQueryToCluster(queryLogID, clusterID int64) error {
	_, err := s.db.Exec("UPDATE query_log SET cluster_id = ? WHERE id = ?", clusterID, queryLogID)
	return err
}

// IncrementClusterScore bumps inject/use/noise count for a learning×cluster pair.
func (s *Store) IncrementClusterScore(learningID, clusterID int64, field string) error {
	col := "inject_count"
	switch field {
	case "use":
		col = "use_count"
	case "noise":
		col = "noise_count"
	default:
		col = "inject_count"
	}
	_, err := s.db.Exec(`INSERT INTO learning_cluster_scores (learning_id, cluster_id, `+col+`, last_injected_at)
		VALUES (?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(learning_id, cluster_id) DO UPDATE SET `+col+` = `+col+` + 1, last_injected_at = CURRENT_TIMESTAMP`,
		learningID, clusterID)
	return err
}

// GetClusterScoresForLearnings returns cluster scores for a set of learning IDs.
func (s *Store) GetClusterScoresForLearnings(learningIDs []string) (map[string][]ClusterScore, error) {
	if len(learningIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(learningIDs))
	args := make([]any, len(learningIDs))
	for i, id := range learningIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.readerDB().Query("SELECT learning_id, cluster_id, inject_count, use_count, noise_count FROM learning_cluster_scores WHERE learning_id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]ClusterScore)
	for rows.Next() {
		var cs ClusterScore
		if err := rows.Scan(&cs.LearningID, &cs.ClusterID, &cs.InjectCount, &cs.UseCount, &cs.NoiseCount); err != nil {
			continue
		}
		k := fmt.Sprintf("%d", cs.LearningID)
		result[k] = append(result[k], cs)
	}
	return result, nil
}

// FindNearestCluster returns the cluster ID with highest cosine similarity to the query vector.
// Returns 0 if no cluster exceeds minSimilarity.
func FindNearestCluster(queryVec []float32, clusters []QueryCluster, minSimilarity float64) int64 {
	bestID := int64(0)
	bestSim := minSimilarity
	for _, c := range clusters {
		sim := cosineSimilarity32(queryVec, c.CentroidVector)
		if sim > bestSim {
			bestSim = sim
			bestID = c.ID
		}
	}
	return bestID
}

func cosineSimilarity32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// blobToFloat32 converts a little-endian byte slice to float32 slice.
func blobToFloat32(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// GetRecentClusterForLearnings returns the cluster_id from the most recent
// query_log entry that injected each learning ID. Returns map[learningID]clusterID.
func (s *Store) GetRecentClusterForLearnings(learningIDs []int64) map[int64]int64 {
	result := make(map[int64]int64)
	if len(learningIDs) == 0 {
		return result
	}
	for _, lid := range learningIDs {
		idStr := fmt.Sprintf("%d", lid)
		var clusterID int64
		err := s.readerDB().QueryRow(
			"SELECT cluster_id FROM query_log WHERE cluster_id IS NOT NULL AND injected_learning_ids LIKE ? ORDER BY created_at DESC LIMIT 1",
			"%"+idStr+"%").Scan(&clusterID)
		if err == nil && clusterID > 0 {
			result[lid] = clusterID
		}
	}
	return result
}

// QuarantineSession marks all learnings from a session as quarantined.
// Quarantined learnings are excluded from vector search and BM25 results.
func (s *Store) QuarantineSession(sessionID string) (int64, error) {
	res, err := s.db.Exec("UPDATE learnings SET quarantined_at = CURRENT_TIMESTAMP WHERE session_id = ? AND quarantined_at IS NULL", sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UnquarantineSession removes quarantine from all learnings of a session.
func (s *Store) UnquarantineSession(sessionID string) (int64, error) {
	res, err := s.db.Exec("UPDATE learnings SET quarantined_at = NULL WHERE session_id = ? AND quarantined_at IS NOT NULL", sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SkipExtraction marks a session to be skipped by the extraction pipeline.
func (s *Store) SkipExtraction(sessionID string) error {
	_, err := s.db.Exec("UPDATE sessions SET skip_extraction = 1 WHERE id = ?", sessionID)
	return err
}

// IsExtractionSkipped checks if a session is marked for extraction skip.
func (s *Store) IsExtractionSkipped(sessionID string) bool {
	var skip int
	err := s.readerDB().QueryRow("SELECT COALESCE(skip_extraction, 0) FROM sessions WHERE id = ?", sessionID).Scan(&skip)
	return err == nil && skip == 1
}

// PurgeOldQueryLogs deletes clustered query_log entries older than days.
// Only deletes rows that have already been assigned to a cluster to preserve unprocessed data.
func (s *Store) PurgeOldQueryLogs(days int) (int64, error) {
	res, err := s.db.Exec("DELETE FROM query_log WHERE cluster_id IS NOT NULL AND created_at < datetime('now', '-' || ? || ' days')", days)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
