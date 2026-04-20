package storage

// EntityCounts holds learning counts for a single entity value.
type EntityCounts struct {
	Total   int
	Gotchas int
}

// GetLearningCountsByEntity returns learning counts per entity value for a project.
// Only counts active learnings (superseded_by IS NULL).
func (s *Store) GetLearningCountsByEntity(project string) (map[string]EntityCounts, error) {
	rows, err := s.readerDB().Query(`
		SELECT le.value, l.category, COUNT(DISTINCT l.id)
		FROM learning_entities le
		JOIN learnings l ON l.id = le.learning_id
		WHERE l.project = ? AND l.superseded_by IS NULL
		GROUP BY le.value, l.category`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]EntityCounts)
	for rows.Next() {
		var entity, category string
		var count int
		if err := rows.Scan(&entity, &category, &count); err != nil {
			continue
		}
		ec := counts[entity]
		ec.Total += count
		if category == "gotcha" {
			ec.Gotchas += count
		}
		counts[entity] = ec
	}
	return counts, nil
}
