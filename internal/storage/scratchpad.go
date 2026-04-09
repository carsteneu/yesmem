package storage

import "database/sql"

// tableScratchpadEntries defines the scratchpad_entries table schema.
const tableScratchpadEntries = `CREATE TABLE IF NOT EXISTS scratchpad_entries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project    TEXT NOT NULL,
	section    TEXT NOT NULL,
	content    TEXT NOT NULL DEFAULT '',
	owner      TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(project, section)
)`

// ScratchpadSection represents a single section entry in the scratchpad.
type ScratchpadSection struct {
	Section   string
	Content   string
	Owner     string
	Size      int
	UpdatedAt string
}

// ScratchpadProject summarises scratchpad presence for a project.
type ScratchpadProject struct {
	Project      string
	SectionCount int
	LastUpdated  string
	Sections     []ScratchpadSection
}

// ScratchpadWrite upserts a section for the given project.
// If the (project, section) pair already exists the content and owner are updated.
func (s *Store) ScratchpadWrite(project, section, content, owner string) error {
	_, err := s.db.Exec(`
		INSERT INTO scratchpad_entries (project, section, content, owner, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project, section) DO UPDATE SET
			content    = excluded.content,
			owner      = excluded.owner,
			updated_at = CURRENT_TIMESTAMP`,
		project, section, content, owner)
	return err
}

// ScratchpadRead returns sections for a project.
// If section is non-empty only that section is returned; otherwise all sections for the project are returned.
func (s *Store) ScratchpadRead(project, section string) ([]ScratchpadSection, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if section != "" {
		rows, err = s.readerDB().Query(`
			SELECT section, content, owner, length(content), updated_at
			FROM scratchpad_entries
			WHERE project = ? AND section = ?
			ORDER BY section ASC`,
			project, section)
	} else {
		rows, err = s.readerDB().Query(`
			SELECT section, content, owner, length(content), updated_at
			FROM scratchpad_entries
			WHERE project = ?
			ORDER BY section ASC`,
			project)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ScratchpadSection
	for rows.Next() {
		var e ScratchpadSection
		if err := rows.Scan(&e.Section, &e.Content, &e.Owner, &e.Size, &e.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// ScratchpadList returns a summary of projects that have scratchpad entries.
// If project is non-empty only that project's summary is returned; otherwise all projects are returned.
func (s *Store) ScratchpadList(project string) ([]ScratchpadProject, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if project != "" {
		rows, err = s.readerDB().Query(`
			SELECT project, section, owner, length(content), updated_at
			FROM scratchpad_entries
			WHERE project = ?
			ORDER BY project ASC, section ASC`,
			project)
	} else {
		rows, err = s.readerDB().Query(`
			SELECT project, section, owner, length(content), updated_at
			FROM scratchpad_entries
			ORDER BY project ASC, section ASC`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projectMap := make(map[string]*ScratchpadProject)
	var order []string
	for rows.Next() {
		var proj, sec, owner, updatedAt string
		var size int
		if err := rows.Scan(&proj, &sec, &owner, &size, &updatedAt); err != nil {
			return nil, err
		}
		p, exists := projectMap[proj]
		if !exists {
			p = &ScratchpadProject{Project: proj}
			projectMap[proj] = p
			order = append(order, proj)
		}
		p.Sections = append(p.Sections, ScratchpadSection{Section: sec, Owner: owner, Size: size, UpdatedAt: updatedAt})
		p.SectionCount = len(p.Sections)
		if updatedAt > p.LastUpdated {
			p.LastUpdated = updatedAt
		}
	}
	var result []ScratchpadProject
	for _, k := range order {
		result = append(result, *projectMap[k])
	}
	return result, rows.Err()
}

// ScratchpadDelete removes scratchpad entries.
// If section is non-empty only that section is deleted; otherwise the entire project is deleted.
// Returns the number of rows deleted.
func (s *Store) ScratchpadDelete(project, section string) (int64, error) {
	var (
		res sql.Result
		err error
	)
	if section != "" {
		res, err = s.db.Exec(`DELETE FROM scratchpad_entries WHERE project = ? AND section = ?`, project, section)
	} else {
		res, err = s.db.Exec(`DELETE FROM scratchpad_entries WHERE project = ?`, project)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
