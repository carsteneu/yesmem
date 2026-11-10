package guard

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// deletionStateFile is the runtime state file the hook already keeps its suggestion cooldowns in
// (internal/hooks): one installation, one file, one WAL.
const deletionStateFile = "guard_state.db"

// deletionStateTable is the counter the deletion gate needs across processes. The Claude Code hook is a fresh
// process for every tool call, so "this session has already been refused once" cannot live in memory.
const deletionStateTable = `CREATE TABLE IF NOT EXISTS deletion_refusal (
	session_id TEXT PRIMARY KEY,
	refused_at INTEGER NOT NULL
)`

// openDeletionState opens the runtime state database, creating it and the table on first use. WAL and a busy
// timeout keep parallel hook processes from tripping over each other; the file is created owner-only before
// SQLite touches it, and the sidecars are narrowed the same way.
func openDeletionState(dataDir string) (*sql.DB, error) {
	if dataDir == "" {
		home, err := homeDir()
		if err != nil {
			return nil, err
		}
		dataDir = defaultDataDir(home)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, deletionStateFile)
	// Create the file with its final mode: sql.Open would leave it 0644 for the moment before the chmod below.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	file.Close()
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(deletionStateTable); err != nil {
		db.Close()
		return nil, err
	}
	// Best effort, and the sidecars belong to the file: a row says a session was refused a deletion, which is
	// as private as the rest of the data directory (YM-338).
	for _, file := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		_ = os.Chmod(file, 0o600)
	}
	return db, nil
}

// recordDeletionRefusal marks this session as having been refused a deletion. The row is what a repeated
// attempt needs; without it the gate keeps answering at the first stage.
//
// A session the caller could not name is never remembered; the gate checks that case itself and refuses
// without asking for an approval that could never be honoured.
func recordDeletionRefusal(dataDir, session string) error {
	if session == "" {
		return nil
	}
	db, err := openDeletionState(dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO deletion_refusal (session_id, refused_at) VALUES (?, ?)
		ON CONFLICT(session_id) DO NOTHING`, session, time.Now().Unix())
	return err
}

// recordDeletionApproval answers whether the approval word may pass: it is true when this session has already
// been refused one deletion. A session that has not been refused yet is refused now, in the same connection, so
// the round trip the word was supposed to force cannot be skipped by two calls in a row.
func recordDeletionApproval(dataDir, session string) (bool, error) {
	if session == "" {
		return false, nil
	}
	db, err := openDeletionState(dataDir)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM deletion_refusal WHERE session_id = ?`, session).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if _, err := db.Exec(`INSERT INTO deletion_refusal (session_id, refused_at) VALUES (?, ?)
		ON CONFLICT(session_id) DO NOTHING`, session, time.Now().Unix()); err != nil {
		return false, err
	}
	return false, nil
}
