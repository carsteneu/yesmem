package storage

// tableTokenUsage tracks cumulative token usage per thread (proxy reports after each API call).
const tableTokenUsage = `CREATE TABLE IF NOT EXISTS token_usage (
	thread_id           TEXT PRIMARY KEY,
	input_tokens        INTEGER DEFAULT 0,
	output_tokens       INTEGER DEFAULT 0,
	cache_read_tokens   INTEGER DEFAULT 0,
	cache_write_tokens  INTEGER DEFAULT 0,
	request_count       INTEGER DEFAULT 0,
	updated_at          TEXT DEFAULT (datetime('now'))
)`

// TrackTokenUsage atomically adds token counts for a thread.
func (s *Store) TrackTokenUsage(threadID string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) error {
	_, err := s.db.Exec(`INSERT INTO token_usage (thread_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_count, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, datetime('now'))
		ON CONFLICT(thread_id) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
			cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
			request_count = request_count + 1,
			updated_at = datetime('now')`,
		threadID, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
	return err
}

// GetTokenUsage returns cumulative token usage for a thread. Returns 0,0 if not found.
func (s *Store) GetTokenUsage(threadID string) (inputTokens, outputTokens int, err error) {
	err = s.readerDB().QueryRow("SELECT input_tokens, output_tokens FROM token_usage WHERE thread_id = ?", threadID).Scan(&inputTokens, &outputTokens)
	if err != nil {
		return 0, 0, nil // not found = no usage
	}
	return
}

// TrackForkTokenUsage atomically adds fork-specific token counts for a thread.
func (s *Store) TrackForkTokenUsage(threadID string, inputTokens, outputTokens int) error {
	_, err := s.db.Exec(`INSERT INTO token_usage (thread_id, fork_input_tokens, fork_output_tokens, fork_request_count, updated_at)
		VALUES (?, ?, ?, 1, datetime('now'))
		ON CONFLICT(thread_id) DO UPDATE SET
			fork_input_tokens = fork_input_tokens + excluded.fork_input_tokens,
			fork_output_tokens = fork_output_tokens + excluded.fork_output_tokens,
			fork_request_count = fork_request_count + 1,
			updated_at = datetime('now')`,
		threadID, inputTokens, outputTokens)
	return err
}

// GetFullTokenUsage returns main + fork token usage for a thread.
func (s *Store) GetFullTokenUsage(threadID string) (inputTokens, outputTokens, forkIn, forkOut, forkReqs int, err error) {
	err = s.readerDB().QueryRow(`SELECT input_tokens, output_tokens,
		COALESCE(fork_input_tokens, 0), COALESCE(fork_output_tokens, 0), COALESCE(fork_request_count, 0)
		FROM token_usage WHERE thread_id = ?`, threadID).Scan(&inputTokens, &outputTokens, &forkIn, &forkOut, &forkReqs)
	if err != nil {
		return 0, 0, 0, 0, 0, nil
	}
	return
}
