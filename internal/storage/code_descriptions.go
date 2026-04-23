package storage

// CodeDescription holds a cached LLM-generated package description.
type CodeDescription struct {
	Description  string
	AntiPatterns string
	GitHead      string
	LearningCount int
}

// UpsertCodeDescription inserts or updates an LLM-generated package description.
func (s *Store) UpsertCodeDescription(project, packageName, description, antiPatterns, gitHead string, learningCount int) error {
	_, err := s.db.Exec(`
		INSERT INTO code_descriptions (project, package_name, description, anti_patterns, git_head, learning_count_at_creation)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, package_name) DO UPDATE SET
			description = excluded.description,
			anti_patterns = excluded.anti_patterns,
			git_head = excluded.git_head,
			learning_count_at_creation = excluded.learning_count_at_creation,
			created_at = datetime('now')`,
		project, packageName, description, antiPatterns, gitHead, learningCount)
	return err
}

// GetCodeDescriptions returns all cached descriptions for a project, keyed by package name.
func (s *Store) GetCodeDescriptions(project string) (map[string]CodeDescription, error) {
	rows, err := s.readerDB().Query(`
		SELECT package_name, description, anti_patterns, git_head, learning_count_at_creation
		FROM code_descriptions WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	descs := make(map[string]CodeDescription)
	for rows.Next() {
		var name string
		var d CodeDescription
		if err := rows.Scan(&name, &d.Description, &d.AntiPatterns, &d.GitHead, &d.LearningCount); err != nil {
			continue
		}
		descs[name] = d
	}
	return descs, nil
}

// IsCodeDescriptionStale checks if cached descriptions need regeneration.
// Stale when: no descriptions exist, any row has a different git HEAD, or 5+ new learnings since last generation.
func (s *Store) IsCodeDescriptionStale(project, currentHead string, currentLearningCount int) bool {
	var matchCount, totalCount, minStoredCount int
	err := s.readerDB().QueryRow(`
		SELECT COUNT(*), SUM(CASE WHEN git_head = ? THEN 1 ELSE 0 END), MIN(learning_count_at_creation)
		FROM code_descriptions WHERE project = ?`, currentHead, project).Scan(&totalCount, &matchCount, &minStoredCount)
	if err != nil || totalCount == 0 {
		return true
	}
	if matchCount != totalCount {
		return true
	}
	return currentLearningCount-minStoredCount >= 5
}
