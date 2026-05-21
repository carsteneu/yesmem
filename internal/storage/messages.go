package storage

import (
	"fmt"
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// InsertMessages inserts a batch of messages in a single transaction.
// Writes to messages.db and syncs FTS5 index.
func (s *Store) InsertMessages(msgs []models.Message) error {
	db := s.messagesWriteDB()
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO messages
		(session_id, source_agent, role, message_type, content, content_blob, tool_name, file_path, timestamp, sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	ftsStmt, err := tx.Prepare(`INSERT INTO messages_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare fts: %w", err)
	}
	defer ftsStmt.Close()

	for _, m := range msgs {
		sourceAgent := models.NormalizeSourceAgent(m.SourceAgent)
		// Copy thinking blob to content for FTS5 indexing
		content := m.Content
		if content == "" && len(m.ContentBlob) > 0 {
			content = string(m.ContentBlob)
		}
		res, err := stmt.Exec(m.SessionID, sourceAgent, m.Role, m.MessageType, content,
			m.ContentBlob, m.ToolName, m.FilePath, fmtTime(m.Timestamp), m.Sequence)
		if err != nil {
			return fmt.Errorf("insert message seq=%d: %w", m.Sequence, err)
		}
		if content != "" {
			id, _ := res.LastInsertId()
			if _, err := ftsStmt.Exec(id, content); err != nil {
				log.Printf("warn: messages_fts insert failed for id=%d: %v", id, err)
			}
		}
	}

	return tx.Commit()
}

// GetMessagesBySession returns all messages for a session, ordered by sequence.
func (s *Store) GetMessagesBySession(sessionID string) ([]models.Message, error) {
	rows, err := s.messagesReaderDB().Query(`SELECT id, session_id, COALESCE(source_agent, 'claude'), role, message_type, content,
		content_blob, tool_name, file_path, timestamp, sequence
		FROM messages WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages for %s: %w", sessionID, err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// GetMessagesBySessionAndType returns messages filtered by type, with optional limit.
func (s *Store) GetMessagesBySessionAndType(sessionID, msgType string, limit ...int) ([]models.Message, error) {
	q := `SELECT id, session_id, COALESCE(source_agent, 'claude'), role, message_type, content,
		content_blob, tool_name, file_path, timestamp, sequence
		FROM messages WHERE session_id = ? AND message_type = ? ORDER BY sequence`
	var args []any
	args = append(args, sessionID, msgType)
	if len(limit) > 0 && limit[0] > 0 {
		q += ` LIMIT ?`
		args = append(args, limit[0])
	}
	rows, err := s.messagesReaderDB().Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("get messages for %s type %s: %w", sessionID, msgType, err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// GetMessagesBySessionRange returns messages in a sequence range for a session.
func (s *Store) GetMessagesBySessionRange(sessionID string, fromSeq, toSeq int) ([]models.Message, error) {
	rows, err := s.messagesReaderDB().Query(`SELECT id, session_id, COALESCE(source_agent, 'claude'), role, message_type, content,
		content_blob, tool_name, file_path, timestamp, sequence
		FROM messages WHERE session_id = ? AND sequence >= ? AND sequence <= ? ORDER BY sequence`, sessionID, fromSeq, toSeq)
	if err != nil {
		return nil, fmt.Errorf("get messages range %s [%d-%d]: %w", sessionID, fromSeq, toSeq, err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// DeleteMessagesBySession removes all messages for a session (for re-indexing).
func (s *Store) DeleteMessagesBySession(sessionID string) error {
	db := s.messagesWriteDB()
	// Delete FTS5 entries first
	db.Exec(`DELETE FROM messages_fts WHERE rowid IN (SELECT id FROM messages WHERE session_id = ?)`, sessionID)
	_, err := db.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID)
	return err
}

// MessageSearchResult holds a single FTS5 search hit.
type MessageSearchResult struct {
	ID          int64
	SessionID   string
	SourceAgent string
	Content     string
	MessageType string
	Timestamp   string
	Sequence    int
	Rank        float64
}

// MessageSearchMeta is a lightweight search result without full content body.
type MessageSearchMeta struct {
	ID          int64
	SessionID   string
	SourceAgent string
	MessageType string
	Timestamp   string
	Sequence    int
	Rank        float64
}

// sanitizeFTS5Query quotes each term to prevent FTS5 from interpreting
// hyphens as column operators (e.g. "Multi-Agent" → "Multi" NOT column "Agent").
func sanitizeFTS5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	var quoted []string
	for _, w := range words {
		w = strings.ReplaceAll(w, "\"", "")
		w = strings.ReplaceAll(w, "'", "")
		w = strings.TrimSpace(w)
		if w == "" || w == "OR" || w == "AND" || w == "NOT" || w == "NEAR" {
			continue
		}
		quoted = append(quoted, "\""+w+"\"")
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, " OR ")
}

// SearchMessages performs FTS5 full-text search over messages.
// Replaces Bleve-based search.
func (s *Store) SearchMessages(query string, limit int) ([]MessageSearchResult, error) {
	return s.SearchMessagesCtx(query, "", "", limit)
}

// SearchMessagesCtx is SearchMessages with optional inclusive lower bound (since)
// and exclusive upper bound (before). Empty strings disable the respective bound.
// ISO-8601 timestamps compare lexicographically, so "2026-04-28" matches everything
// from that day onward (inclusive) and "2026-04-29" excludes that day onward.
func (s *Store) SearchMessagesCtx(query, since, before string, limit int) ([]MessageSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	ftsQuery := sanitizeFTS5Query(query)
	if ftsQuery == "" {
		return nil, nil
	}
	sql := `
		SELECT m.id, m.session_id, COALESCE(m.source_agent, 'claude'), m.content, m.message_type, m.timestamp, COALESCE(m.sequence, 0), bm25(messages_fts) as rank
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ?`
	args := []any{ftsQuery}
	if since != "" {
		sql += ` AND m.timestamp >= ?`
		args = append(args, since)
	}
	if before != "" {
		sql += ` AND m.timestamp < ?`
		args = append(args, before)
	}
	sql += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := s.messagesReaderDB().Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var results []MessageSearchResult
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.ID, &r.SessionID, &r.SourceAgent, &r.Content, &r.MessageType, &r.Timestamp, &r.Sequence, &r.Rank); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchMessagesDeep performs FTS5 search with optional type filtering for deep_search.
// Type filter is applied in SQL via IN-clause so narrow date windows dominated by
// excluded types (e.g. tool_use) cannot collapse the result set.
func (s *Store) SearchMessagesDeep(query string, includeThinking, includeCommands bool, limit int) ([]MessageSearchResult, error) {
	return s.SearchMessagesDeepCtx(query, includeThinking, includeCommands, "", "", limit)
}

// deepSearchGlobalBM25Threshold is the match-count at which deep_search switches from
// global BM25 ranking to the recency-biased subselect path. Calibrated for the 30s
// MCP deadline on a 3GB messages.db: BM25 over ≤5000 matches fits comfortably; beyond
// that it risks timeout.
const deepSearchGlobalBM25Threshold = 5000

// SearchMessagesDeepCtx is SearchMessagesDeep with optional inclusive lower bound (since)
// and exclusive upper bound (before). Empty strings disable the respective bound.
//
// Dispatches between two ranking strategies based on a cheap match-count precheck:
// small match-set → global BM25 (best relevance, fits in deadline); large match-set
// → recency-biased subselect (BM25 only over K most recent matches).
func (s *Store) SearchMessagesDeepCtx(query string, includeThinking, includeCommands bool, since, before string, limit int) ([]MessageSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	ftsQuery := sanitizeFTS5Query(query)
	if ftsQuery == "" {
		return nil, nil
	}

	var matchCount int
	if err := s.messagesReaderDB().QueryRow(
		`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?`,
		ftsQuery,
	).Scan(&matchCount); err != nil {
		return nil, fmt.Errorf("deep search count: %w", err)
	}

	if matchCount <= deepSearchGlobalBM25Threshold {
		return s.searchMessagesDeepGlobal(ftsQuery, includeThinking, includeCommands, since, before, limit)
	}
	return s.searchMessagesDeepRecency(ftsQuery, includeThinking, includeCommands, since, before, limit)
}

func (s *Store) searchMessagesDeepGlobal(ftsQuery string, includeThinking, includeCommands bool, since, before string, limit int) ([]MessageSearchResult, error) {
	allowed := deepSearchAllowedTypes(includeThinking, includeCommands)
	placeholders := strings.Repeat("?,", len(allowed))
	placeholders = placeholders[:len(placeholders)-1]

	sql := `
		SELECT m.id, m.session_id, COALESCE(m.source_agent, 'claude'), m.message_type, m.timestamp, COALESCE(m.sequence, 0), bm25(messages_fts) as rank
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ?
		  AND m.message_type IN (` + placeholders + `)`
	args := []any{ftsQuery}
	for _, t := range allowed {
		args = append(args, t)
	}
	if since != "" {
		sql += ` AND m.timestamp >= ?`
		args = append(args, since)
	}
	if before != "" {
		sql += ` AND m.timestamp < ?`
		args = append(args, before)
	}
	sql += ` ORDER BY rank LIMIT ?`
	args = append(args, limit*20)

	return s.runDeepSearchScan(sql, args, limit)
}

func (s *Store) searchMessagesDeepRecency(ftsQuery string, includeThinking, includeCommands bool, since, before string, limit int) ([]MessageSearchResult, error) {
	allowed := deepSearchAllowedTypes(includeThinking, includeCommands)
	placeholders := strings.Repeat("?,", len(allowed))
	placeholders = placeholders[:len(placeholders)-1]

	sql := `
		SELECT m.id, m.session_id, COALESCE(m.source_agent, 'claude'), m.message_type, m.timestamp, COALESCE(m.sequence, 0), fts.rnk as rank
		FROM messages m
		JOIN (
			SELECT rowid, bm25(messages_fts) as rnk
			FROM messages_fts
			WHERE messages_fts MATCH ?
			ORDER BY rowid DESC
			LIMIT ?
		) fts ON fts.rowid = m.id
		WHERE m.message_type IN (` + placeholders + `)`
	args := []any{ftsQuery, limit * 20}
	for _, t := range allowed {
		args = append(args, t)
	}
	if since != "" {
		sql += ` AND m.timestamp >= ?`
		args = append(args, since)
	}
	if before != "" {
		sql += ` AND m.timestamp < ?`
		args = append(args, before)
	}
	sql += ` ORDER BY rank LIMIT ?`
	args = append(args, limit*20)

	return s.runDeepSearchScan(sql, args, limit)
}

func deepSearchAllowedTypes(includeThinking, includeCommands bool) []string {
	allowed := []string{"text", "assistant", "user"}
	if includeThinking {
		allowed = append(allowed, "thinking")
	}
	if includeCommands {
		allowed = append(allowed, "tool_use", "tool_result", "bash_output")
	}
	return allowed
}

// runDeepSearchScan runs query and scans rows into MessageSearchResult, stopping at
// limit. SQL may over-fetch (the two callers over-fetch by 20× to reserve headroom
// for handler-side post-filtering); the Go-side break enforces the final cap.
func (s *Store) runDeepSearchScan(query string, args []any, limit int) ([]MessageSearchResult, error) {
	rows, err := s.messagesReaderDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("deep search messages: %w", err)
	}
	defer rows.Close()

	var results []MessageSearchResult
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.ID, &r.SessionID, &r.SourceAgent, &r.MessageType, &r.Timestamp, &r.Sequence, &r.Rank); err != nil {
			return nil, err
		}
		results = append(results, r)
		if len(results) >= limit {
			break
		}
	}
	return results, rows.Err()
}

// MessageCount returns total message count in messages.db.
func (s *Store) MessageCount() int64 {
	var count int64
	s.messagesReaderDB().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count)
	return count
}

// GetMessageContents loads m.content for the given ids in a single batch query.
// Returns a map id→content. Missing ids are simply absent from the map.
// Used as the second stage of deep_search: FTS5 ranking returns ids without
// content; the caller then fetches content only for the final survivors.
func (s *Store) GetMessageContents(ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.messagesReaderDB().Query(
		`SELECT id, content FROM messages WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return nil, err
		}
		out[id] = content
	}
	return out, rows.Err()
}

func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}

func scanMessages(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]models.Message, error) {
	var msgs []models.Message
	for rows.Next() {
		var m models.Message
		var ts string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.SourceAgent, &m.Role, &m.MessageType, &m.Content,
			&m.ContentBlob, &m.ToolName, &m.FilePath, &ts, &m.Sequence); err != nil {
			return nil, err
		}
		m.Timestamp = parseTime(ts)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
