package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestLogCapUsage_RoundTrip(t *testing.T) {
	s := testStore(t)

	start := time.Date(2026, 11, 26, 6, 30, 0, 0, time.Local)
	if err := s.LogCapUsage(CapUsageEntry{
		CapName: "code_map", Fn: "search", SessionID: "ses_abc",
		StartedAt: start, Duration: 42 * time.Millisecond,
		OK: true, ArgsHash: "deadbeef", ArgsLen: 17,
	}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := s.LogCapUsage(CapUsageEntry{
		CapName: "nonexistent", Fn: "", SessionID: "ses_abc",
		StartedAt: start, Duration: 1 * time.Millisecond,
		OK: false, Error: "cap not found", ArgsHash: "cafe", ArgsLen: 2,
	}); err != nil {
		t.Fatalf("log: %v", err)
	}

	ok, failed, err := s.CapUsageCounts()
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("expected 1 ok / 1 failed, got %d / %d", ok, failed)
	}
}

func TestLogCapUsage_ErrorTruncated(t *testing.T) {
	s := testStore(t)
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	if err := s.LogCapUsage(CapUsageEntry{
		CapName: "c", StartedAt: time.Now(), OK: false, Error: string(long),
	}); err != nil {
		t.Fatalf("log: %v", err)
	}
	var stored string
	if err := s.db.QueryRow("SELECT error FROM cap_usage LIMIT 1").Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(stored) > capUsageErrorMax {
		t.Fatalf("error not truncated: %d chars", len(stored))
	}
}
