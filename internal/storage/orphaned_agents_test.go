package storage

import (
	"testing"
)

// OrphanedAgents must return exactly the rows the recovery sweep marked as
// reattachable, with enough identifying data that an operator can act on them
// without opening SQLite.
func TestOrphanedAgents_ReturnsMarkedRows(t *testing.T) {
	s := testStore(t)

	if _, err := s.db.Exec(`INSERT INTO agents
		(id, project, section, session_id, status, progress, orphan_reattachable,
		 opencode_session_id, work_dir)
		VALUES
		('agent-a', '/p/one', 'sec-a', 'ses-a', 'stopped', 'orphaned-reattachable', 1,
		 'opencode-sess-a', '/p/one'),
		('agent-b', '/p/two', 'sec-b', 'ses-b', 'stopped', 'stopped', 0,
		 'opencode-sess-b', '/p/two'),
		('agent-c', '/p/three', 'sec-c', 'ses-c', 'stopped', 'orphaned-reattachable', 1,
		 'opencode-sess-c', '/p/three')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orphans, err := s.OrphanedAgents()
	if err != nil {
		t.Fatalf("OrphanedAgents: %v", err)
	}
	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphans, got %d: %+v", len(orphans), orphans)
	}
	got := map[string]string{}
	for _, o := range orphans {
		got[o.ID] = o.OpenCodeSessionID
	}
	if got["agent-a"] != "opencode-sess-a" || got["agent-c"] != "opencode-sess-c" {
		t.Fatalf("unexpected orphan set: %+v", got)
	}
	if _, ok := got["agent-b"]; ok {
		t.Fatal("agent-b was not marked reattachable and must not be listed")
	}
}

// An empty result is the normal case on a machine that never restarted. It must
// not be an error, or every startup would log a failure.
func TestOrphanedAgents_EmptyIsNotAnError(t *testing.T) {
	s := testStore(t)

	orphans, err := s.OrphanedAgents()
	if err != nil {
		t.Fatalf("OrphanedAgents on empty table: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("expected none, got %d", len(orphans))
	}
}
