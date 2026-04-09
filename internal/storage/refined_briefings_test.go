package storage

import (
	"testing"
)

func TestSaveAndGetRefinedBriefing(t *testing.T) {
	s := newTestStore(t)

	err := s.SaveRefinedBriefing("testproj", "hash1", "Refined prose text", "opus")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetRefinedBriefing("testproj")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "Refined prose text" {
		t.Errorf("expected 'Refined prose text', got %q", got)
	}
}

func TestGetRefinedBriefing_NotFound(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetRefinedBriefing("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string for missing project, got %q", got)
	}
}

func TestSaveRefinedBriefing_Upsert(t *testing.T) {
	s := newTestStore(t)

	s.SaveRefinedBriefing("proj", "hash1", "version 1", "haiku")
	s.SaveRefinedBriefing("proj", "hash2", "version 2", "opus")

	got, _ := s.GetRefinedBriefing("proj")
	if got != "version 2" {
		t.Errorf("upsert should update to 'version 2', got %q", got)
	}
}

func TestGetRefinedBriefingHash(t *testing.T) {
	s := newTestStore(t)

	s.SaveRefinedBriefing("proj", "abc123hash", "text", "opus")

	got, err := s.GetRefinedBriefingHash("proj")
	if err != nil {
		t.Fatalf("get hash: %v", err)
	}
	if got != "abc123hash" {
		t.Errorf("expected 'abc123hash', got %q", got)
	}
}

func TestGetRefinedBriefingHash_NotFound(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetRefinedBriefingHash("missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty hash for missing project, got %q", got)
	}
}

func TestProjectChangeFingerprint_Stable(t *testing.T) {
	s := newTestStore(t)

	fp1 := s.ProjectChangeFingerprint("empty")
	fp2 := s.ProjectChangeFingerprint("empty")
	if fp1 != fp2 {
		t.Errorf("fingerprint should be stable: %q != %q", fp1, fp2)
	}
}

func TestProjectChangeFingerprint_ChangesWithData(t *testing.T) {
	s := newTestStore(t)

	fp1 := s.ProjectChangeFingerprint("testproj")

	// Insert a session — project_short must match the fingerprint query
	s.db.Exec(`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path) VALUES ('s1', '/test', 'testproj', datetime('now'), datetime('now'), '/s1.jsonl')`)

	fp2 := s.ProjectChangeFingerprint("testproj")
	if fp1 == fp2 {
		t.Error("fingerprint should change after inserting a session")
	}
}

func TestProjectChangeFingerprint_ChangesWithPersonaDirective(t *testing.T) {
	s := newTestStore(t)

	fp1 := s.ProjectChangeFingerprint("testproj")

	// Insert a persona directive — fingerprint must change
	s.db.Exec(`INSERT INTO persona_directives (user_id, directive, traits_hash, generated_at, model_used) VALUES ('user_profile', 'test profile', 'hash1', datetime('now'), 'opus')`)

	fp2 := s.ProjectChangeFingerprint("testproj")
	if fp1 == fp2 {
		t.Error("fingerprint should change after inserting a persona directive")
	}

	// Update directive — fingerprint must change again
	s.db.Exec(`INSERT INTO persona_directives (user_id, directive, traits_hash, generated_at, model_used) VALUES ('default', 'new directive', 'hash2', datetime('now', '+1 second'), 'opus')`)

	fp3 := s.ProjectChangeFingerprint("testproj")
	if fp2 == fp3 {
		t.Error("fingerprint should change after updating persona directive")
	}
}

func TestProjectChangeFingerprint_DifferentProjects(t *testing.T) {
	s := newTestStore(t)

	s.db.Exec(`INSERT INTO sessions (id, project, project_short, started_at, indexed_at, jsonl_path) VALUES ('s1', '/a', 'projA', datetime('now'), datetime('now'), '/s1.jsonl')`)

	fpA := s.ProjectChangeFingerprint("projA")
	fpB := s.ProjectChangeFingerprint("projB")
	if fpA == fpB {
		t.Error("different projects should have different fingerprints")
	}
}
