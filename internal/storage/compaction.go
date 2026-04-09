package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

// CompactedBlock represents a group of stage-3 stubs compressed into a summary.
type CompactedBlock struct {
	ID        int64  `json:"id"`
	ThreadID  string `json:"thread_id"`
	StartIdx  int    `json:"start_idx"`
	EndIdx    int    `json:"end_idx"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
}

// SaveCompactedBlock stores a new compacted block (upsert by thread_id + start_idx).
func (s *Store) SaveCompactedBlock(threadID string, startIdx, endIdx int, content string) error {
	_, err := s.db.Exec(`
		INSERT INTO compacted_blocks (thread_id, start_idx, end_idx, content)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(thread_id, start_idx) DO UPDATE SET
			end_idx = excluded.end_idx,
			content = excluded.content`,
		threadID, startIdx, endIdx, content,
	)
	if err != nil {
		return fmt.Errorf("save compacted block: %w", err)
	}
	return nil
}

// GetCompactedBlocks returns all compacted blocks for a thread, ordered by start_idx.
func (s *Store) GetCompactedBlocks(threadID string) ([]*CompactedBlock, error) {
	rows, err := s.readerDB().Query(`
		SELECT id, thread_id, start_idx, end_idx, content, created_at
		FROM compacted_blocks
		WHERE thread_id = ?
		ORDER BY start_idx ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("get compacted blocks: %w", err)
	}
	defer rows.Close()

	return scanCompactedBlocks(rows)
}

// GetCompactedBlocksInRange returns blocks that overlap with the given message index range.
func (s *Store) GetCompactedBlocksInRange(threadID string, fromIdx, toIdx int) ([]*CompactedBlock, error) {
	rows, err := s.readerDB().Query(`
		SELECT id, thread_id, start_idx, end_idx, content, created_at
		FROM compacted_blocks
		WHERE thread_id = ? AND start_idx < ? AND end_idx > ?
		ORDER BY start_idx ASC`, threadID, toIdx, fromIdx)
	if err != nil {
		return nil, fmt.Errorf("get compacted blocks in range: %w", err)
	}
	defer rows.Close()

	return scanCompactedBlocks(rows)
}

// DeleteCompactedBlocks removes all compacted blocks for a thread.
func (s *Store) DeleteCompactedBlocks(threadID string) error {
	_, err := s.db.Exec(`DELETE FROM compacted_blocks WHERE thread_id = ?`, threadID)
	return err
}

func scanCompactedBlocks(rows interface {
	Next() bool
	Scan(...any) error
}) ([]*CompactedBlock, error) {
	var blocks []*CompactedBlock
	for rows.Next() {
		b := &CompactedBlock{}
		if err := rows.Scan(&b.ID, &b.ThreadID, &b.StartIdx, &b.EndIdx, &b.Content, &b.CreatedAt); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// SetProxyState stores a key-value pair in proxy_state (upsert).
func (s *Store) SetProxyState(key, value string) error {
	_, err := s.proxyStateDB().Exec(`INSERT INTO proxy_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, key, value)
	return err
}

// GetProxyState retrieves a value from proxy_state. Returns "" if not found.
func (s *Store) GetProxyState(key string) (string, error) {
	var val string
	err := s.proxyStateDB().QueryRow(`SELECT value FROM proxy_state WHERE key = ?`, key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// DeleteProxyStatePrefix deletes all proxy_state entries whose key starts with prefix.
// Returns the number of rows deleted.
func (s *Store) DeleteProxyStatePrefix(prefix string) (int, error) {
	result, err := s.proxyStateDB().Exec(`DELETE FROM proxy_state WHERE key LIKE ? || '%'`, prefix)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}
