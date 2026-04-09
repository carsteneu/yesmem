package storage

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/models"
)

// UpsertFileCoverage updates coverage for a file in a project.
func (s *Store) UpsertFileCoverage(c *models.FileCoverage) error {
	_, err := s.db.Exec(`INSERT INTO file_coverage (project, file_path, directory, session_count, last_touched, operation_types)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, file_path) DO UPDATE SET
			session_count = session_count + 1,
			last_touched = excluded.last_touched,
			operation_types = CASE
				WHEN operation_types LIKE '%' || excluded.operation_types || '%' THEN operation_types
				ELSE operation_types || ',' || excluded.operation_types
			END`,
		c.Project, c.FilePath, c.Directory, c.SessionCount, c.LastTouched, c.OperationTypes)
	return err
}

// GetCoverageByProject returns file coverage grouped by directory.
func (s *Store) GetCoverageByProject(project string) ([]models.FileCoverage, error) {
	rows, err := s.readerDB().Query(`SELECT project, file_path, directory, session_count, last_touched, operation_types
		FROM file_coverage WHERE project = ? ORDER BY directory, file_path`, project)
	if err != nil {
		return nil, fmt.Errorf("get coverage for %s: %w", project, err)
	}
	defer rows.Close()

	var cov []models.FileCoverage
	for rows.Next() {
		var c models.FileCoverage
		if err := rows.Scan(&c.Project, &c.FilePath, &c.Directory, &c.SessionCount, &c.LastTouched, &c.OperationTypes); err != nil {
			return nil, err
		}
		cov = append(cov, c)
	}
	return cov, rows.Err()
}
