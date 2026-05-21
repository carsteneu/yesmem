package storage

import (
	"testing"
)

func TestActivateCap_InsertsRow(t *testing.T) {
	s := mustOpen(t)
	if err := s.ActivateCap("thread-1", "git_log_since"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	caps, err := s.GetSessionCaps("thread-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("expected 1 cap, got %d", len(caps))
	}
	if caps[0].CapName != "git_log_since" {
		t.Errorf("expected name=git_log_since, got %q", caps[0].CapName)
	}
	if caps[0].ThreadID != "thread-1" {
		t.Errorf("expected thread-1, got %q", caps[0].ThreadID)
	}
	if caps[0].ActivatedAt == 0 {
		t.Errorf("activated_at should be set")
	}
	if caps[0].LastUsedAt != nil {
		t.Errorf("last_used_at should be nil before first use")
	}
}

func TestActivateCap_Idempotent(t *testing.T) {
	s := mustOpen(t)
	if err := s.ActivateCap("t", "c1"); err != nil {
		t.Fatalf("first activate: %v", err)
	}
	if err := s.ActivateCap("t", "c1"); err != nil {
		t.Fatalf("second activate should succeed: %v", err)
	}
	caps, _ := s.GetSessionCaps("t")
	if len(caps) != 1 {
		t.Errorf("expected 1 cap after duplicate activate, got %d", len(caps))
	}
}

func TestDeactivateCap_ExistingRow(t *testing.T) {
	s := mustOpen(t)
	_ = s.ActivateCap("t", "c1")
	deleted, err := s.DeactivateCap("t", "c1")
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if !deleted {
		t.Errorf("expected deleted=true for existing row")
	}
	caps, _ := s.GetSessionCaps("t")
	if len(caps) != 0 {
		t.Errorf("expected 0 caps after deactivate, got %d", len(caps))
	}
}

func TestDeactivateCap_Nonexistent(t *testing.T) {
	s := mustOpen(t)
	deleted, err := s.DeactivateCap("t", "nonexistent")
	if err != nil {
		t.Fatalf("deactivate nonexistent: %v", err)
	}
	if deleted {
		t.Errorf("expected deleted=false for nonexistent row")
	}
}

func TestGetSessionCaps_Empty(t *testing.T) {
	s := mustOpen(t)
	caps, err := s.GetSessionCaps("no-such-thread")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(caps) != 0 {
		t.Errorf("expected empty slice, got %d items", len(caps))
	}
}

func TestGetSessionCaps_ThreadIsolation(t *testing.T) {
	s := mustOpen(t)
	_ = s.ActivateCap("t1", "c1")
	_ = s.ActivateCap("t1", "c2")
	_ = s.ActivateCap("t1", "c3")
	_ = s.ActivateCap("t2", "c1")

	caps1, _ := s.GetSessionCaps("t1")
	if len(caps1) != 3 {
		t.Errorf("thread t1: expected 3 caps, got %d", len(caps1))
	}
	caps2, _ := s.GetSessionCaps("t2")
	if len(caps2) != 1 {
		t.Errorf("thread t2: expected 1 cap, got %d", len(caps2))
	}
}

func TestTouchCap_SetsLastUsedAt(t *testing.T) {
	s := mustOpen(t)
	_ = s.ActivateCap("t", "c1")

	before, _ := s.GetSessionCaps("t")
	if before[0].LastUsedAt != nil {
		t.Fatalf("precondition: last_used_at should be nil before touch")
	}

	if err := s.TouchCap("t", "c1"); err != nil {
		t.Fatalf("touch: %v", err)
	}

	after, _ := s.GetSessionCaps("t")
	if after[0].LastUsedAt == nil {
		t.Errorf("expected last_used_at to be set after touch")
	} else if *after[0].LastUsedAt == 0 {
		t.Errorf("expected last_used_at > 0 after touch")
	}
}

func TestTouchCap_NonexistentIsNoOp(t *testing.T) {
	s := mustOpen(t)
	if err := s.TouchCap("t", "nonexistent"); err != nil {
		t.Errorf("touch on nonexistent should not error, got %v", err)
	}
}

func TestActivateCap_UsesCapsDBWhenAvailable(t *testing.T) {
	s := mustOpenCapStore(t)
	if err := s.ActivateCap("thread-caps", "test_cap"); err != nil {
		t.Fatalf("activate via caps.db: %v", err)
	}
	caps, err := s.GetSessionCaps("thread-caps")
	if err != nil {
		t.Fatalf("get via caps.db: %v", err)
	}
	if len(caps) != 1 || caps[0].CapName != "test_cap" {
		t.Fatalf("expected 1 cap 'test_cap', got %d (%v)", len(caps), caps)
	}
	// Verify NOT in yesmem.db
	capsFromMain, err := s.GetSessionCaps("thread-caps")
	if err != nil {
		t.Fatalf("get from main db: %v", err)
	}
	if len(capsFromMain) != 1 {
		t.Fatalf("caps.db write should be readable via capDB()")
	}
	// Deactivate via caps.db path
	deleted, err := s.DeactivateCap("thread-caps", "test_cap")
	if err != nil {
		t.Fatalf("deactivate via caps.db: %v", err)
	}
	if !deleted {
		t.Errorf("expected deleted=true")
	}
	after, _ := s.GetSessionCaps("thread-caps")
	if len(after) != 0 {
		t.Errorf("expected 0 caps after deactivate, got %d", len(after))
	}
}
