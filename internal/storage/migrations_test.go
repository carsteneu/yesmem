package storage

import (
	"database/sql"
	"strings"
	"testing"
)

// mustOpen → createSchema → runMigrations records the full real migration set
// (ids 1..N) into schema_migrations. Every test below isolates runMigrations by
// clearing that table first so it controls the "already applied" set.
func clearMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}
}

// TestRunMigrations_SkipsAlreadyApplied asserts that a migration whose id is
// already in schema_migrations is never re-executed — even if its SQL would
// fail. This is the idempotency guarantee that stops every startup from
// re-running the full ALTER TABLE sequence.
func TestRunMigrations_SkipsAlreadyApplied(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	// Pre-record id=1 as applied. id=1's SQL below is deliberately broken; if
	// the runner respects tracking, it must be skipped (no error).
	if _, err := db.Exec(`INSERT INTO schema_migrations (id) VALUES (1)`); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}

	broken := []string{`THIS IS NOT VALID SQL`}
	if err := runMigrations(db, broken); err != nil {
		t.Errorf("recorded migration must be skipped, not executed; got: %v", err)
	}
}

// TestRunMigrations_SurfacesBrokenSQL is the core bug regression: the old
// runner swallowed ALL errors (s.db.Exec(mig) // Ignore errors), so a typo in a
// new migration silently no-op'd. After the fix, only benign idempotency errors
// are tolerated; everything else surfaces — with the offending SQL attached.
//
// Note: we use a SYNTAX error here, not a typo'd table name. A typo'd table
// yields "no such table", which is deliberately tolerated (fresh-DB semantics —
// migrations run before CREATE TABLE). A syntax error is unambiguous: no code
// path tolerates it, so it must surface.
func TestRunMigrations_SurfacesBrokenSQL(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	// id=1 valid (adds a throwaway column) → succeeds.
	// id=2 syntactic garbage → MUST surface.
	bad := []string{
		`ALTER TABLE learnings ADD COLUMN _migration_test_ok TEXT`,
		`THIS IS NOT VALID SQL`,
	}
	err := runMigrations(db, bad)
	if err == nil {
		t.Fatal("broken migration SQL must surface an error instead of being swallowed")
	}
	if !strings.Contains(err.Error(), "migration #2") {
		t.Errorf("error should identify which migration failed, got: %v", err)
	}
}

// TestRunMigrations_DuplicateColumnRecorded asserts the benign-idempotency
// path: when ALTER TABLE ADD COLUMN hits "duplicate column name" (the column
// was already added by CREATE TABLE on a fresh DB), the runner tolerates it
// AND records the migration as applied so it is skipped next time.
func TestRunMigrations_DuplicateColumnRecorded(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	// "source" exists on learnings from createSchema → duplicate-column error.
	mig := []string{`ALTER TABLE learnings ADD COLUMN source TEXT DEFAULT 'x'`}
	if err := runMigrations(db, mig); err != nil {
		t.Fatalf("duplicate column must be tolerated, got: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("idempotent failure should be recorded as applied; got count=%d", n)
	}
}

// TestRunMigrations_NoSuchTableToleratedNotRecorded pins the fresh-DB semantics:
// migrations run BEFORE CREATE TABLE IF NOT EXISTS, so on a fresh DB every
// ALTER TABLE fails with "no such table". That must be tolerated (otherwise no
// fresh install could open) but NOT recorded — CREATE TABLE builds the real
// schema, and a genuine missing-table on an existing DB is a real problem we
// want surfaced on retry.
func TestRunMigrations_NoSuchTableToleratedNotRecorded(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	mig := []string{`ALTER TABLE nonexistent_tbl_xyz ADD COLUMN x TEXT`}
	if err := runMigrations(db, mig); err != nil {
		t.Fatalf("no-such-table must be tolerated on fresh DB, got: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("no-such-table failures must NOT be recorded (CREATE TABLE handles them); got count=%d", n)
	}
}

// TestRunMigrations_RenameColumnIdempotent pins the migration #99 case: a
// RENAME COLUMN whose source is already renamed fails "no such column". That is
// an idempotent state (the rename's effect is present), so it must be tolerated
// and recorded — otherwise every restart on a fresh-DB-originated store errors
// out on the one legacy rename migration.
func TestRunMigrations_RenameColumnIdempotent(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	// session_active_caps.cap_name already exists (fresh schema); renaming the
	// old name → "no such column: capability_name".
	mig := []string{
		`ALTER TABLE session_active_caps RENAME COLUMN capability_name TO cap_name`,
	}
	if err := runMigrations(db, mig); err != nil {
		t.Fatalf("already-applied RENAME must be tolerated, got: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("idempotent RENAME failure should be recorded; got count=%d", n)
	}
}

// TestRunMigrations_NoSuchColumnOnDML_Surfaces is the regression guard for the
// "no such column" tolerance narrowing. An UPDATE (or any DML) referencing a
// missing column means a prerequisite ALTER did not land; the runner MUST
// surface it. The old classification tolerated any "no such column" and
// recorded it as applied — which would silently drop a data backfill the
// moment a future UPDATE migration's column is missing.
func TestRunMigrations_NoSuchColumnOnDML_Surfaces(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	mig := []string{`UPDATE learnings SET nonexistent_col_xyz = 1`}
	err := runMigrations(db, mig)
	if err == nil {
		t.Fatal("UPDATE on a missing column must surface, not be tolerated as idempotent")
	}
	if !strings.Contains(err.Error(), "migration #1") {
		t.Errorf("error should identify which migration failed, got: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("surfaced DML failure must not be recorded; got count=%d", n)
	}
}

// TestRunMigrations_DataBackfillRecorded pins that a successful data-backfill
// (UPDATE) migration is recorded as applied. The all-current-apply-clean test
// would pass even if UPDATEs were never recorded (idempotent re-runs are
// harmless), so this is the only check that catches a recording regression on
// the DML path.
func TestRunMigrations_DataBackfillRecorded(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()
	clearMigrations(t, db)

	mig := []string{`UPDATE learnings SET project = project`}
	if err := runMigrations(db, mig); err != nil {
		t.Fatalf("data-backfill UPDATE should succeed on populated table: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("successful UPDATE must be recorded so it is not re-run; got count=%d", n)
	}
}

// TestRunMigrations_AllCurrentApplyClean verifies the real migration slice
// re-runs without surfacing on a populated store — every migration is either
// already applied (skipped) or hits a tolerated idempotency error. This catches
// regressions where a migration has a typo against the actual schema.
func TestRunMigrations_AllCurrentApplyClean(t *testing.T) {
	s := mustOpen(t)
	db := s.DB()

	// createSchema already recorded all ids; re-running must be a clean skip.
	if err := runMigrations(db, migrations); err != nil {
		t.Fatalf("re-run migrations on existing schema: %v", err)
	}
}
