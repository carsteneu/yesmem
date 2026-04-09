package storage

import (
	"database/sql"
	"fmt"

	"github.com/carsteneu/yesmem/internal/models"
)

// ReplaceLearningClusters atomically replaces all clusters for a project.
// Deletes existing clusters in a transaction, then inserts the new ones.
func (s *Store) ReplaceLearningClusters(project string, clusters []models.LearningCluster) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM learning_clusters WHERE project = ?`, project); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete old clusters: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO learning_clusters
		(project, label, learning_count, avg_recency_days, avg_hit_count, confidence, learning_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, c := range clusters {
		if _, err := stmt.Exec(project, c.Label, c.LearningCount, c.AvgRecencyDays, c.AvgHitCount, c.Confidence, c.LearningIDs); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert cluster %q: %w", c.Label, err)
		}
	}

	return tx.Commit()
}

// GetLearningClusters returns all clusters for a project, ordered by confidence DESC.
func (s *Store) GetLearningClusters(project string) ([]models.LearningCluster, error) {
	rows, err := s.readerDB().Query(`SELECT id, project, label, learning_count, avg_recency_days,
		avg_hit_count, confidence, learning_ids
		FROM learning_clusters WHERE project = ?
		ORDER BY confidence DESC`, project)
	if err != nil {
		return nil, fmt.Errorf("get learning clusters: %w", err)
	}
	defer rows.Close()

	var clusters []models.LearningCluster
	for rows.Next() {
		var c models.LearningCluster
		if err := rows.Scan(&c.ID, &c.Project, &c.Label, &c.LearningCount,
			&c.AvgRecencyDays, &c.AvgHitCount, &c.Confidence, &c.LearningIDs); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// GetClusterHash returns the stored change-detection hash for a project's clustering.
func (s *Store) GetClusterHash(project string) (string, error) {
	key := "cluster_hash:" + project
	var hash string
	err := s.db.QueryRow(`SELECT value FROM proxy_state WHERE key = ?`, key).Scan(&hash)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return hash, nil
}

// SetClusterHash stores the change-detection hash for a project's clustering.
func (s *Store) SetClusterHash(project, hash string) error {
	key := "cluster_hash:" + project
	_, err := s.db.Exec(`INSERT INTO proxy_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`,
		key, hash)
	return err
}
