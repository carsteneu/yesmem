package storage

import (
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// GetIndexState returns the index state for a JSONL path, or nil if not indexed.
func (s *Store) GetIndexState(jsonlPath string) (*models.IndexState, error) {
	row := s.readerDB().QueryRow(`SELECT jsonl_path, file_size, file_mtime, indexed_at
		FROM index_state WHERE jsonl_path = ?`, jsonlPath)

	st := &models.IndexState{}
	var mtime, indexedAt string
	err := row.Scan(&st.JSONLPath, &st.FileSize, &mtime, &indexedAt)
	if err != nil {
		return nil, err
	}
	st.FileMtime = parseTime(mtime)
	st.IndexedAt = parseTime(indexedAt)
	return st, nil
}

// UpsertIndexState records that a JSONL file has been indexed.
func (s *Store) UpsertIndexState(st *models.IndexState) error {
	_, err := s.db.Exec(`INSERT INTO index_state (jsonl_path, file_size, file_mtime, indexed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(jsonl_path) DO UPDATE SET
			file_size = excluded.file_size,
			file_mtime = excluded.file_mtime,
			indexed_at = excluded.indexed_at`,
		st.JSONLPath, st.FileSize, fmtTime(st.FileMtime), fmtTime(st.IndexedAt))
	return err
}

// NeedsReindex checks if a file needs re-indexing by comparing size and mtime.
// Truncates mtime to seconds because RFC3339 storage loses nanoseconds.
func (s *Store) NeedsReindex(jsonlPath string, size int64, mtime time.Time) bool {
	st, err := s.GetIndexState(jsonlPath)
	if err != nil {
		return true // not indexed yet
	}
	return st.FileSize != size || !st.FileMtime.Truncate(time.Second).Equal(mtime.Truncate(time.Second))
}
