package storage

import (
	"database/sql"
	"time"
)

// ProjectScanRow holds a cached scan result for a project.
type ProjectScanRow struct {
	Project   string
	ScanJSON  string
	GitHead   string
	CbmMtime  int64
	ScannedAt time.Time
}

// SaveProjectScan upserts a project scan result.
func (s *Store) SaveProjectScan(row *ProjectScanRow) error {
	_, err := s.db.Exec(`INSERT INTO project_scan (project, scan_json, git_head, cbm_mtime, scanned_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project) DO UPDATE SET scan_json=excluded.scan_json, git_head=excluded.git_head, cbm_mtime=excluded.cbm_mtime, scanned_at=CURRENT_TIMESTAMP`,
		row.Project, row.ScanJSON, row.GitHead, row.CbmMtime)
	return err
}

// LoadScan implements codescan.ScanStore — returns cached scan JSON, git head, and CBM index mtime.
func (s *Store) LoadScan(project string) (scanJSON, gitHead string, cbmMtime int64, err error) {
	row, err := s.GetProjectScan(project)
	if err != nil || row == nil {
		return "", "", 0, err
	}
	return row.ScanJSON, row.GitHead, row.CbmMtime, nil
}

// PersistScan implements codescan.ScanStore — saves scan result to SQLite.
func (s *Store) PersistScan(project, scanJSON, gitHead string, cbmMtime int64) error {
	return s.SaveProjectScan(&ProjectScanRow{
		Project:  project,
		ScanJSON: scanJSON,
		GitHead:  gitHead,
		CbmMtime: cbmMtime,
	})
}

// GetProjectScan returns the cached scan for a project, or nil if not found.
func (s *Store) GetProjectScan(project string) (*ProjectScanRow, error) {
	row := s.db.QueryRow(`SELECT project, scan_json, git_head, COALESCE(cbm_mtime, 0), scanned_at FROM project_scan WHERE project = ?`, project)
	var r ProjectScanRow
	var scannedAt string
	err := row.Scan(&r.Project, &r.ScanJSON, &r.GitHead, &r.CbmMtime, &scannedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.ScannedAt, _ = time.Parse(time.RFC3339, scannedAt)
	if r.ScannedAt.IsZero() {
		r.ScannedAt, _ = time.Parse("2006-01-02 15:04:05", scannedAt)
	}
	return &r, nil
}
