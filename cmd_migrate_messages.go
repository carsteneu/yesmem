package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func runMigrateMessages() {
	dataDir := yesmemDataDir()
	mainDB := filepath.Join(dataDir, "yesmem.db")
	messagesDB := filepath.Join(dataDir, "messages.db")

	fmt.Printf("Migrating messages from %s to %s\n", mainDB, messagesDB)

	// Open source DB
	src, err := sql.Open("sqlite", mainDB)
	if err != nil {
		log.Fatalf("open source: %v", err)
	}
	defer src.Close()
	src.Exec("PRAGMA journal_mode=WAL")
	src.Exec("PRAGMA busy_timeout=10000")

	// Check if messages exist in source
	var count int64
	src.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	if count == 0 {
		fmt.Println("No messages in source DB — nothing to migrate.")
		return
	}
	fmt.Printf("Found %d messages to migrate\n", count)

	// Open/create target DB
	dst, err := sql.Open("sqlite", messagesDB)
	if err != nil {
		log.Fatalf("open target: %v", err)
	}
	defer dst.Close()
	dst.Exec("PRAGMA journal_mode=WAL")
	dst.Exec("PRAGMA busy_timeout=10000")
	dst.Exec("PRAGMA synchronous=NORMAL")

	// Create schema in target
	dst.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id      TEXT NOT NULL,
		role            TEXT NOT NULL,
		message_type    TEXT NOT NULL,
		content         TEXT,
		content_blob    BLOB,
		tool_name       TEXT,
		file_path       TEXT,
		timestamp       TEXT NOT NULL,
		sequence        INTEGER NOT NULL
	)`)
	dst.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id)`)
	dst.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_type ON messages(message_type)`)
	dst.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_file ON messages(file_path) WHERE file_path IS NOT NULL`)
	dst.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(content, tokenize='unicode61', content_rowid=id)`)

	// Check if already migrated (idempotent)
	var existing int64
	dst.QueryRow("SELECT COUNT(*) FROM messages").Scan(&existing)
	if existing > 0 {
		fmt.Printf("Target already has %d messages — skipping copy (idempotent).\n", existing)
		fmt.Println("To re-migrate, delete messages.db first.")
	} else {
		// Batch copy
		start := time.Now()
		batchSize := 1000
		offset := 0
		copied := int64(0)

		for {
			rows, err := src.Query(`SELECT session_id, role, message_type, content, content_blob, tool_name, file_path, timestamp, sequence FROM messages ORDER BY id LIMIT ? OFFSET ?`, batchSize, offset)
			if err != nil {
				log.Fatalf("query source: %v", err)
			}

			tx, err := dst.Begin()
			if err != nil {
				rows.Close()
				log.Fatalf("begin tx: %v", err)
			}

			stmt, _ := tx.Prepare(`INSERT INTO messages (session_id, role, message_type, content, content_blob, tool_name, file_path, timestamp, sequence) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
			ftsStmt, _ := tx.Prepare(`INSERT INTO messages_fts(rowid, content) VALUES (?, ?)`)

			batchCount := 0
			for rows.Next() {
				var sessionID, role, msgType, timestamp string
				var content sql.NullString
				var contentBlob []byte
				var toolName, filePath sql.NullString
				var sequence int

				if err := rows.Scan(&sessionID, &role, &msgType, &content, &contentBlob, &toolName, &filePath, &timestamp, &sequence); err != nil {
					rows.Close()
					tx.Rollback()
					log.Fatalf("scan: %v", err)
				}

				res, err := stmt.Exec(sessionID, role, msgType, content, contentBlob, toolName, filePath, timestamp, sequence)
				if err != nil {
					rows.Close()
					tx.Rollback()
					log.Fatalf("insert: %v", err)
				}

				if content.Valid && content.String != "" {
					id, _ := res.LastInsertId()
					ftsStmt.Exec(id, content.String)
				}

				batchCount++
				copied++
			}
			rows.Close()
			stmt.Close()
			ftsStmt.Close()

			if err := tx.Commit(); err != nil {
				log.Fatalf("commit: %v", err)
			}

			if copied%10000 == 0 || batchCount < batchSize {
				fmt.Printf("  %d / %d messages copied (%.1fs)\n", copied, count, time.Since(start).Seconds())
			}

			if batchCount < batchSize {
				break
			}
			offset += batchSize
		}

		fmt.Printf("Migration complete: %d messages in %.1fs\n", copied, time.Since(start).Seconds())
	}

	// Delete messages from source DB
	fmt.Print("Deleting messages from source DB... ")
	if _, err := src.Exec("DELETE FROM messages"); err != nil {
		log.Fatalf("delete from source: %v", err)
	}
	fmt.Println("done.")

	// VACUUM source
	fmt.Print("VACUUM source DB... ")
	if _, err := src.Exec("VACUUM"); err != nil {
		log.Printf("warn: vacuum failed: %v", err)
	} else {
		fmt.Println("done.")
	}

	// Remove Bleve index if it exists
	bleveDir := filepath.Join(dataDir, "bleve-index")
	if info, err := os.Stat(bleveDir); err == nil && info.IsDir() {
		fmt.Printf("Removing Bleve index (%s)... ", bleveDir)
		if err := os.RemoveAll(bleveDir); err != nil {
			log.Printf("warn: remove bleve: %v", err)
		} else {
			fmt.Println("done.")
		}
	}

	// Report sizes
	srcInfo, _ := os.Stat(mainDB)
	dstInfo, _ := os.Stat(messagesDB)
	if srcInfo != nil && dstInfo != nil {
		fmt.Printf("\nResult:\n  %s: %d MB\n  %s: %d MB\n",
			filepath.Base(mainDB), srcInfo.Size()/(1024*1024),
			filepath.Base(messagesDB), dstInfo.Size()/(1024*1024))
	}
}
