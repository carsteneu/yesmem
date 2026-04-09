package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps an SQLite database connection.
type Store struct {
	db        *sql.DB
	runtimeDB *sql.DB
	readDB    *sql.DB

	messagesDB     *sql.DB
	messagesReadDB *sql.DB

	ftsLastSyncID atomic.Int64 // tracks highest learning ID synced to FTS5
}

// Open creates or opens an SQLite database at the given path.
// Use ":memory:" for an in-memory database (testing).
func Open(path string) (*Store, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}

	s := &Store{db: db, runtimeDB: db, readDB: db, messagesDB: db, messagesReadDB: db}

	if path != ":memory:" {
		runtimePath := filepath.Join(filepath.Dir(path), "runtime.db")
		runtimeDB, err := openSQLite(runtimePath)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("open runtime db: %w", err)
		}
		s.runtimeDB = runtimeDB

		messagesPath := filepath.Join(filepath.Dir(path), "messages.db")
		messagesDB, err := openSQLite(messagesPath)
		if err != nil {
			runtimeDB.Close()
			db.Close()
			return nil, fmt.Errorf("open messages db: %w", err)
		}
		s.messagesDB = messagesDB
	}

	if err := s.createSchema(); err != nil {
		s.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	if err := s.createRuntimeSchema(); err != nil {
		s.Close()
		return nil, fmt.Errorf("create runtime schema: %w", err)
	}

	if err := s.createMessagesSchema(); err != nil {
		s.Close()
		return nil, fmt.Errorf("create messages schema: %w", err)
	}

	if path != ":memory:" {
		readDB, err := openSQLite(path)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("open read db: %w", err)
		}
		readDB.SetMaxOpenConns(8) // WAL mode allows concurrent reads
		s.readDB = readDB

		messagesReadDB, err := openSQLite(filepath.Join(filepath.Dir(path), "messages.db"))
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("open messages read db: %w", err)
		}
		messagesReadDB.SetMaxOpenConns(8)
		// Prevent reads from blocking on writes during heavy indexing
		messagesReadDB.Exec("PRAGMA read_uncommitted=true")
		s.messagesReadDB = messagesReadDB
	}

	if err := s.migrateProxyStateToRuntime(); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate proxy state: %w", err)
	}

	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.messagesReadDB != nil && s.messagesReadDB != s.db && s.messagesReadDB != s.messagesDB {
		s.messagesReadDB.Close()
	}
	if s.messagesDB != nil && s.messagesDB != s.db {
		s.messagesDB.Close()
	}
	if s.runtimeDB != nil && s.runtimeDB != s.db {
		if err := s.runtimeDB.Close(); err != nil {
			s.db.Close()
			return err
		}
	}
	if s.readDB != nil && s.readDB != s.db && s.readDB != s.runtimeDB {
		if err := s.readDB.Close(); err != nil {
			s.db.Close()
			return err
		}
	}
	return s.db.Close()
}

// DB returns the underlying sql.DB for advanced queries.
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) proxyStateDB() *sql.DB {
	if s.runtimeDB != nil {
		return s.runtimeDB
	}
	return s.db
}

func (s *Store) readerDB() *sql.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

// ReaderDB returns the read-optimized DB connection (exported for daemon use).
func (s *Store) ReaderDB() *sql.DB {
	return s.readerDB()
}

func (s *Store) messagesWriteDB() *sql.DB {
	if s.messagesDB != nil {
		return s.messagesDB
	}
	return s.db
}

func (s *Store) messagesReaderDB() *sql.DB {
	if s.messagesReadDB != nil {
		return s.messagesReadDB
	}
	return s.messagesWriteDB()
}

// MessagesDB returns the messages database connection (for direct queries).
func (s *Store) MessagesDB() *sql.DB {
	return s.messagesReaderDB()
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set synchronous: %w", err)
	}

	return db, nil
}

// StartFTSSync runs a background goroutine that periodically syncs new learnings
// into the FTS5 index. Replaces the old trigger-based approach which caused
// write contention blocking BM25 reads for 5-60s during extraction/evolution.
func (s *Store) StartFTSSync(ctx context.Context, interval time.Duration) {
	// Initialize last sync ID from current FTS5 state
	var maxID int64
	s.db.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM learnings_fts`).Scan(&maxID)
	s.ftsLastSyncID.Store(maxID)
	log.Printf("[fts-sync] started (interval=%v, last_id=%d)", interval, maxID)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.syncFTS()
			}
		}
	}()
}

// syncFTS inserts new learnings into FTS5 in a single batch transaction.
func (s *Store) syncFTS() {
	lastID := s.ftsLastSyncID.Load()

	// Find new learnings since last sync
	rows, err := s.readerDB().Query(`SELECT id, content || ' ' || COALESCE(trigger_rule, '') FROM learnings WHERE id > ? ORDER BY id`, lastID)
	if err != nil {
		log.Printf("[fts-sync] read error: %v", err)
		return
	}
	defer rows.Close()

	type pending struct {
		id      int64
		content string
	}
	var items []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.content) == nil {
			items = append(items, p)
		}
	}
	if len(items) == 0 {
		return
	}

	// Batch insert into FTS5 — single transaction, single COMMIT
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[fts-sync] begin tx: %v", err)
		return
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO learnings_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		log.Printf("[fts-sync] prepare: %v", err)
		return
	}
	defer stmt.Close()

	for _, item := range items {
		stmt.Exec(item.id, item.content)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[fts-sync] commit: %v", err)
		return
	}

	maxNew := items[len(items)-1].id
	s.ftsLastSyncID.Store(maxNew)
	log.Printf("[fts-sync] synced %d learnings (id %d→%d)", len(items), lastID, maxNew)
}

// SyncFTSNow runs a synchronous FTS5 sync. Intended for tests where no
// background sync goroutine is running (in-memory SQLite).
func (s *Store) SyncFTSNow() {
	s.syncFTS()
}
