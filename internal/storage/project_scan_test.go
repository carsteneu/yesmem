package storage

import (
	"testing"
)

func TestProjectScan_SaveAndLoad(t *testing.T) {
	s := newTestStore(t)

	scan := &ProjectScanRow{
		Project:  "yesmem",
		ScanJSON: `{"RootDir":"/tmp/yesmem","Tier":"large"}`,
		GitHead:  "abc123",
		CbmMtime: 1776338702,
	}

	if err := s.SaveProjectScan(scan); err != nil {
		t.Fatalf("SaveProjectScan: %v", err)
	}

	got, err := s.GetProjectScan("yesmem")
	if err != nil {
		t.Fatalf("GetProjectScan: %v", err)
	}
	if got == nil {
		t.Fatal("expected scan row, got nil")
	}
	if got.GitHead != "abc123" {
		t.Errorf("git_head = %q, want abc123", got.GitHead)
	}
	if got.ScanJSON != scan.ScanJSON {
		t.Errorf("scan_json mismatch: %q", got.ScanJSON)
	}
	if got.ScannedAt.IsZero() {
		t.Error("scanned_at should not be zero")
	}
}

func TestProjectScan_Upsert(t *testing.T) {
	s := newTestStore(t)

	scan1 := &ProjectScanRow{Project: "yesmem", ScanJSON: `{"v":1}`, GitHead: "aaa", CbmMtime: 100}
	scan2 := &ProjectScanRow{Project: "yesmem", ScanJSON: `{"v":2}`, GitHead: "bbb", CbmMtime: 200}

	s.SaveProjectScan(scan1)
	s.SaveProjectScan(scan2)

	got, _ := s.GetProjectScan("yesmem")
	if got.GitHead != "bbb" {
		t.Errorf("expected upsert to bbb, got %q", got.GitHead)
	}
	if got.ScanJSON != `{"v":2}` {
		t.Errorf("expected v2, got %q", got.ScanJSON)
	}
}

func TestProjectScan_NotFound(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetProjectScan("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent, got %+v", got)
	}
}
