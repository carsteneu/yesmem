package daemon

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

// An empty orphan list is the normal case on a machine that never restarted. It
// must be a no-op: no resume calls, no log spam, no error.
func TestAutoResumeOrphans_EmptyListIsNoop(t *testing.T) {
	h, _ := mustHandler(t)

	attempted, cleared, gaveUp := h.autoResumeOrphans(nil)
	if attempted != 0 || cleared != 0 || gaveUp != 0 {
		t.Fatalf("expected 0/0/0, got %d/%d/%d", attempted, cleared, gaveUp)
	}
}

// The wrapper must reach the store: a marked row whose work continued after the
// stop gets its marker cleared, which proves the callback wiring and the
// storage-side decision are connected. The per-decision cases are covered in
// internal/storage/orphan_resume_test.go.
func TestAutoResumeOrphans_WiresStoreDecision(t *testing.T) {
	h, store := mustHandler(t)
	if err := store.ExecForTest(`INSERT INTO agents
		(id, project, section, session_id, status, progress, orphan_reattachable,
		 opencode_session_id, work_dir, restart_count, max_restarts,
		 stopped_at, last_activity_at)
		VALUES ('agent-w', '/p', 'sec', 'ses-w', 'stopped', 'orphaned-reattachable', 1,
		 'oc-w', '/p', 0, 3, '2026-11-26T09:05:03Z', '2026-11-26T09:10:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orphans, err := store.OrphanedAgents()
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	attempted, cleared, gaveUp := h.autoResumeOrphans(orphans)
	if attempted != 0 || cleared != 1 || gaveUp != 0 {
		t.Fatalf("expected 0/1/0, got %d/%d/%d", attempted, cleared, gaveUp)
	}

	var marker int
	if err := store.QueryRowForTest(
		`SELECT COALESCE(orphan_reattachable,0) FROM agents WHERE id='agent-w'`,
	).Scan(&marker); err != nil {
		t.Fatalf("read: %v", err)
	}
	if marker != 0 {
		t.Fatalf("expected marker cleared, got %d", marker)
	}
}

var _ = storage.OrphanResumeAttempt
