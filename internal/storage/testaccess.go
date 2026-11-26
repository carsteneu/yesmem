package storage

import "database/sql"

// ExecForTest runs a statement against the store's database. It exists so tests
// in other packages can seed rows without exporting the *sql.DB itself.
func (s *Store) ExecForTest(query string, args ...any) error {
	_, err := s.db.Exec(query, args...)
	return err
}

// QueryRowForTest returns a single row for assertions. See ExecForTest.
func (s *Store) QueryRowForTest(query string, args ...any) *sql.Row {
	return s.db.QueryRow(query, args...)
}
