package storage

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// UpsertAssociation inserts or updates an association edge.
func (s *Store) UpsertAssociation(a *models.Association) error {
	relType := a.RelationType
	if relType == "" {
		relType = "related"
	}
	_, err := s.db.Exec(`INSERT INTO associations (source_type, source_id, target_type, target_id, weight, relation_type)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_type, source_id, target_type, target_id)
		DO UPDATE SET weight = weight + excluded.weight`,
		a.SourceType, a.SourceID, a.TargetType, a.TargetID, a.Weight, relType)
	return err
}

// InsertAssociationBatch inserts multiple associations in a transaction.
func (s *Store) InsertAssociationBatch(assocs []models.Association) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO associations (source_type, source_id, target_type, target_id, weight, relation_type)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_type, source_id, target_type, target_id)
		DO UPDATE SET weight = weight + excluded.weight`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range assocs {
		relType := a.RelationType
		if relType == "" {
			relType = "related"
		}
		if _, err := stmt.Exec(a.SourceType, a.SourceID, a.TargetType, a.TargetID, a.Weight, relType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetAssociationsFrom returns all edges from a given node.
func (s *Store) GetAssociationsFrom(nodeType, nodeID string) ([]models.Association, error) {
	rows, err := s.readerDB().Query(`SELECT source_type, source_id, target_type, target_id, weight, relation_type
		FROM associations WHERE source_type = ? AND source_id = ? ORDER BY weight DESC`,
		nodeType, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get associations from %s/%s: %w", nodeType, nodeID, err)
	}
	defer rows.Close()

	var assocs []models.Association
	for rows.Next() {
		var a models.Association
		if err := rows.Scan(&a.SourceType, &a.SourceID, &a.TargetType, &a.TargetID, &a.Weight, &a.RelationType); err != nil {
			return nil, err
		}
		assocs = append(assocs, a)
	}
	return assocs, rows.Err()
}

// GetAssociationsTo returns all edges pointing to a given node.
func (s *Store) GetAssociationsTo(nodeType, nodeID string) ([]models.Association, error) {
	rows, err := s.readerDB().Query(`SELECT source_type, source_id, target_type, target_id, weight, relation_type
		FROM associations WHERE target_type = ? AND target_id = ? ORDER BY weight DESC`,
		nodeType, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get associations to %s/%s: %w", nodeType, nodeID, err)
	}
	defer rows.Close()

	var assocs []models.Association
	for rows.Next() {
		var a models.Association
		if err := rows.Scan(&a.SourceType, &a.SourceID, &a.TargetType, &a.TargetID, &a.Weight, &a.RelationType); err != nil {
			return nil, err
		}
		assocs = append(assocs, a)
	}
	return assocs, rows.Err()
}

// LoadAllAssociations returns all associations (for loading into in-memory graph).
func (s *Store) LoadAllAssociations() ([]models.Association, error) {
	rows, err := s.readerDB().Query(`SELECT source_type, source_id, target_type, target_id, weight, relation_type FROM associations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var assocs []models.Association
	for rows.Next() {
		var a models.Association
		if err := rows.Scan(&a.SourceType, &a.SourceID, &a.TargetType, &a.TargetID, &a.Weight, &a.RelationType); err != nil {
			return nil, err
		}
		assocs = append(assocs, a)
	}
	return assocs, rows.Err()
}

// InsertTypedAssociation writes a semantic edge between two learnings.
// If an edge already exists between the same pair, the relation_type is updated.
func (s *Store) InsertTypedAssociation(sourceID, targetID int64, relType string) error {
	src := fmt.Sprintf("%d", sourceID)
	tgt := fmt.Sprintf("%d", targetID)
	_, err := s.db.Exec(`
		INSERT INTO associations (source_type, source_id, target_type, target_id, weight, relation_type)
		VALUES ('learning', ?, 'learning', ?, 1.0, ?)
		ON CONFLICT(source_type, source_id, target_type, target_id)
		DO UPDATE SET relation_type = excluded.relation_type, weight = weight + 1.0`,
		src, tgt, relType)
	return err
}

// GetAssociationsByRelationType returns all edges from a node with a specific relation type.
func (s *Store) GetAssociationsByRelationType(sourceType, sourceID, relType string) ([]models.Association, error) {
	rows, err := s.readerDB().Query(`
		SELECT source_type, source_id, target_type, target_id, weight, relation_type
		FROM associations
		WHERE source_type = ? AND source_id = ? AND relation_type = ?
		ORDER BY weight DESC`,
		sourceType, sourceID, relType)
	if err != nil {
		return nil, fmt.Errorf("get associations by type %s/%s/%s: %w", sourceType, sourceID, relType, err)
	}
	defer rows.Close()

	var assocs []models.Association
	for rows.Next() {
		var a models.Association
		if err := rows.Scan(&a.SourceType, &a.SourceID, &a.TargetType, &a.TargetID, &a.Weight, &a.RelationType); err != nil {
			return nil, err
		}
		assocs = append(assocs, a)
	}
	return assocs, rows.Err()
}

// GetAssociationNeighbors returns all outgoing learning→learning edges for the given source IDs.
// Returns a map from source ID string to its association edges. Single DB round-trip.
func (s *Store) GetAssociationNeighbors(ids []string, limitPerSource int) (map[string][]models.Association, error) {
	if len(ids) == 0 {
		return map[string][]models.Association{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT a.source_type, a.source_id, a.target_type, a.target_id, a.weight, COALESCE(a.relation_type, 'related')
		FROM associations a
		INNER JOIN learnings l ON CAST(a.target_id AS INTEGER) = l.id
		WHERE a.source_type = 'learning' AND a.target_type = 'learning'
		AND a.source_id IN (%s)
		AND l.superseded_by IS NULL AND l.quarantined_at IS NULL
		ORDER BY a.weight DESC
	`, strings.Join(placeholders, ","))

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get association neighbors: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.Association)
	for rows.Next() {
		var a models.Association
		if err := rows.Scan(&a.SourceType, &a.SourceID, &a.TargetType, &a.TargetID, &a.Weight, &a.RelationType); err != nil {
			return nil, err
		}
		if len(result[a.SourceID]) < limitPerSource {
			result[a.SourceID] = append(result[a.SourceID], a)
		}
	}
	return result, rows.Err()
}

// GetContradictingPairs finds contradicts-edges between newIDs and previousIDs.
// Checks both directions (A contradicts B and B contradicts A) since contradiction is symmetric.
// Returns pairs as [][2]int64 where [0]=newID, [1]=previousID.
func (s *Store) GetContradictingPairs(newIDs, previousIDs []int64) ([][2]int64, error) {
	if len(newIDs) == 0 || len(previousIDs) == 0 {
		return nil, nil
	}

	newPlaceholders := make([]string, len(newIDs))
	prevPlaceholders := make([]string, len(previousIDs))
	var args []any

	for i, id := range newIDs {
		newPlaceholders[i] = "?"
		args = append(args, fmt.Sprintf("%d", id))
	}
	for i, id := range previousIDs {
		prevPlaceholders[i] = "?"
		args = append(args, fmt.Sprintf("%d", id))
	}

	// Duplicate args for the reverse direction
	var argsReverse []any
	for _, id := range previousIDs {
		argsReverse = append(argsReverse, fmt.Sprintf("%d", id))
	}
	for _, id := range newIDs {
		argsReverse = append(argsReverse, fmt.Sprintf("%d", id))
	}
	allArgs := append(args, argsReverse...)

	query := fmt.Sprintf(`
		SELECT source_id, target_id FROM associations
		WHERE source_type = 'learning' AND target_type = 'learning'
		AND relation_type = 'contradicts'
		AND source_id IN (%s) AND target_id IN (%s)
		UNION
		SELECT target_id, source_id FROM associations
		WHERE source_type = 'learning' AND target_type = 'learning'
		AND relation_type = 'contradicts'
		AND source_id IN (%s) AND target_id IN (%s)
	`, strings.Join(newPlaceholders, ","), strings.Join(prevPlaceholders, ","),
		strings.Join(prevPlaceholders, ","), strings.Join(newPlaceholders, ","))

	rows, err := s.readerDB().Query(query, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("get contradicting pairs: %w", err)
	}
	defer rows.Close()

	newSet := make(map[string]int64)
	for _, id := range newIDs {
		newSet[fmt.Sprintf("%d", id)] = id
	}

	var pairs [][2]int64
	seen := make(map[[2]int64]bool)
	for rows.Next() {
		var srcStr, tgtStr string
		if err := rows.Scan(&srcStr, &tgtStr); err != nil {
			return nil, err
		}
		var newID, prevID int64
		if nid, ok := newSet[srcStr]; ok {
			newID = nid
			fmt.Sscanf(tgtStr, "%d", &prevID)
		} else {
			fmt.Sscanf(srcStr, "%d", &prevID)
			newID = newSet[tgtStr]
		}
		pair := [2]int64{newID, prevID}
		if !seen[pair] {
			seen[pair] = true
			pairs = append(pairs, pair)
		}
	}
	return pairs, rows.Err()
}

// GetLearningsWithEntityOverlap returns IDs of active learnings that share
// at least one entity value with the given list. excludeID is excluded (self).
func (s *Store) GetLearningsWithEntityOverlap(entities []string, excludeID int64, limit int) ([]int64, error) {
	if len(entities) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(entities))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(entities)+1)
	for i, e := range entities {
		args[i] = e
	}
	args[len(entities)] = excludeID

	rows, err := s.readerDB().Query(fmt.Sprintf(`
		SELECT DISTINCT le.learning_id
		FROM learning_entities le
		INNER JOIN learnings l ON l.id = le.learning_id
		WHERE le.value IN (%s)
		AND le.learning_id != ?
		AND l.superseded_by IS NULL
		AND l.quarantined_at IS NULL
		LIMIT %d
	`, placeholders, limit), args...)
	if err != nil {
		return nil, fmt.Errorf("entity overlap: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
