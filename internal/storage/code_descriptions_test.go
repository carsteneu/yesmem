package storage

import (
	"testing"
)

func TestUpsertAndGetCodeDescriptions(t *testing.T) {
	s := mustOpen(t)

	// Upsert two package descriptions
	err := s.UpsertCodeDescription("yesmem", "proxy", "Intercepts API requests for context management.", "", "abc123", 100)
	if err != nil {
		t.Fatalf("upsert proxy: %v", err)
	}
	err = s.UpsertCodeDescription("yesmem", "storage", "SQLite persistence layer for learnings and sessions.", "", "abc123", 100)
	if err != nil {
		t.Fatalf("upsert storage: %v", err)
	}

	// Get all descriptions for project
	descs, err := s.GetCodeDescriptions("yesmem")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(descs) != 2 {
		t.Fatalf("expected 2 descriptions, got %d", len(descs))
	}
	if descs["proxy"].Description != "Intercepts API requests for context management." {
		t.Errorf("proxy desc: %q", descs["proxy"].Description)
	}

	// Upsert same package — should update, not duplicate
	err = s.UpsertCodeDescription("yesmem", "proxy", "HTTP proxy for context injection.", "", "def456", 105)
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	descs, err = s.GetCodeDescriptions("yesmem")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if len(descs) != 2 {
		t.Errorf("expected 2 after upsert, got %d", len(descs))
	}
	if descs["proxy"].Description != "HTTP proxy for context injection." {
		t.Errorf("proxy desc after update: %q", descs["proxy"].Description)
	}

	// Different project — should be empty
	descs2, err := s.GetCodeDescriptions("other")
	if err != nil {
		t.Fatalf("get other: %v", err)
	}
	if len(descs2) != 0 {
		t.Errorf("expected empty for other project, got %d", len(descs2))
	}
}

func TestCodeDescriptionStaleness(t *testing.T) {
	s := mustOpen(t)

	// No descriptions — always stale
	if !s.IsCodeDescriptionStale("yesmem", "abc123", 100) {
		t.Error("expected stale when no descriptions exist")
	}

	// Insert description
	s.UpsertCodeDescription("yesmem", "proxy", "desc", "", "abc123", 100)

	// Same HEAD, same learning count — NOT stale
	if s.IsCodeDescriptionStale("yesmem", "abc123", 100) {
		t.Error("expected not stale with same HEAD and learning count")
	}

	// Different HEAD — stale
	if !s.IsCodeDescriptionStale("yesmem", "def456", 100) {
		t.Error("expected stale with different HEAD")
	}

	// Same HEAD, learning count delta >= 5 — stale
	if !s.IsCodeDescriptionStale("yesmem", "abc123", 105) {
		t.Error("expected stale with 5+ new learnings")
	}

	// Same HEAD, learning count delta < 5 — NOT stale
	if s.IsCodeDescriptionStale("yesmem", "abc123", 104) {
		t.Error("expected not stale with <5 new learnings")
	}
}

func TestCodeDescriptionAntiPatterns(t *testing.T) {
	s := mustOpen(t)

	antiPatterns := "→ New injection = new *_inject.go file\n→ Never modify pipeline order"
	err := s.UpsertCodeDescription("yesmem", "proxy", "Proxy desc", antiPatterns, "abc123", 100)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	descs, _ := s.GetCodeDescriptions("yesmem")
	if descs["proxy"].AntiPatterns != antiPatterns {
		t.Errorf("anti_patterns: %q", descs["proxy"].AntiPatterns)
	}
}
