package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

const tableSchemaMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
	id INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

// runMigrations applies ordered SQL migrations to db, tracking each applied
// migration by its 1-based position in migs inside schema_migrations.
//
// This replaces the old loop that swallowed every error
// (`s.db.Exec(mig) // Ignore errors`), which silently no-op'd typo'd or
// malformed migrations — the worst possible failure mode for a migration
// runner, since the schema looked "migrated" while the change never landed.
//
// Error classification:
//   - already applied (id present)       → skipped, never executed
//   - "no such table"                    → tolerated, NOT recorded (fresh-DB)
//   - idempotent DDL errors (see below)  → tolerated AND recorded
//   - any other error                    → surfaced with migration index + SQL
//
// The migration list mixes DDL (ALTER TABLE, CREATE INDEX) with DML (UPDATE
// data backfills). The two need different idempotency rules:
//
//   - "duplicate column name" / "already exists" are DDL-only errors — they can
//     only come from ALTER TABLE ADD COLUMN / CREATE TABLE|INDEX|VIEW|TRIGGER,
//     never from DML. Safe to treat as "already applied" unconditionally.
//   - "no such column" is ambiguous: on an ALTER TABLE RENAME/DROP COLUMN it
//     means the rename/drop already landed (idempotent); on an UPDATE it means
//     a prerequisite ALTER failed and the column is genuinely missing.
//     Recording that as applied would silently drop a data backfill — the same
//     failure mode the old swallow-all runner had. So we tolerate "no such
//     column" ONLY when the migration is an ALTER; for any DML it surfaces.
//
// Why "no such table" is tolerated-but-not-recorded: createSchema runs
// migrations BEFORE the CREATE TABLE IF NOT EXISTS block. On a fresh DB every
// ALTER therefore fails with "no such table" the first time; CREATE TABLE then
// builds the real schema. Recording those would mask a genuine missing-table
// on an existing DB — so we leave it un-recorded and let the next run re-try.
func runMigrations(db *sql.DB, migs []string) error {
	if _, err := db.Exec(tableSchemaMigrations); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return err
	}

	for i, mig := range migs {
		id := int64(i + 1)
		if applied[id] {
			continue
		}

		if _, err := db.Exec(mig); err != nil {
			switch {
			case isNoSuchTableError(err):
				// Fresh-DB transient; CREATE TABLE IF NOT EXISTS handles it.
				// Deliberately not recorded — see godoc.
				continue
			case isIdempotentError(err, mig):
				// Already applied (duplicate column / already exists / "no such
				// column" on an ALTER RENAME|DROP). Fall through to record.
				// DML "no such column" is NOT tolerated — it means a prereq
				// ALTER never landed and the backfill would be silently lost.
			default:
				return fmt.Errorf("migration #%d failed: %w\n  SQL: %s", id, err, mig)
			}
		}

		if _, err := db.Exec(
			`INSERT OR IGNORE INTO schema_migrations (id) VALUES (?)`, id,
		); err != nil {
			return fmt.Errorf("record migration #%d: %w", id, err)
		}
	}
	return nil
}

func loadAppliedMigrations(db *sql.DB) (map[int64]bool, error) {
	rows, err := db.Query(`SELECT id FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func isNoSuchTableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// isIdempotentError reports whether err reflects a migration whose effect is
// already present, given the migration SQL for context.
//
// "duplicate column name" and "already exists" are always idempotent — they can
// only come from DDL. "no such column" is idempotent only on an ALTER TABLE
// (RENAME/DROP COLUMN already applied); on DML (UPDATE/INSERT/DELETE) it signals
// a genuinely missing column from a failed prerequisite ALTER, which must
// surface rather than be recorded as applied.
func isIdempotentError(err error, mig string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "duplicate column name") || strings.Contains(msg, "already exists") {
		return true
	}
	if strings.Contains(msg, "no such column") {
		return startsWithAlter(mig)
	}
	return false
}

// startsWithAlter reports whether mig is an ALTER TABLE statement, so "no such
// column" can be tolerated as a re-applied RENAME/DROP but not mis-attributed to
// an UPDATE. Statement kind is decided by the leading keyword, after trimming
// SQL comments and whitespace — a preceding "--" comment line must not mask it.
func startsWithAlter(mig string) bool {
	s := stripLineComments(mig)
	for len(s) > 0 {
		c := s[0]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			s = s[1:]
			continue
		}
		break
	}
	return strings.HasPrefix(strings.ToUpper(s), "ALTER TABLE")
}

// stripLineComments removes "-- ..." runs to end-of-line so the statement's
// leading keyword can be inspected without a comment line hiding it.
func stripLineComments(mig string) string {
	var b strings.Builder
	for _, line := range strings.Split(mig, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
