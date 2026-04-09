package storage

import "fmt"

// SpendAdapter wraps Store to satisfy extraction.SpendPersister interface.
type SpendAdapter struct {
	Store *Store
}

func (a *SpendAdapter) TrackSpend(day, bucket string, costUSD float64) error {
	return a.Store.TrackSpend(day, bucket, costUSD)
}

func (a *SpendAdapter) GetDailySpend(day, bucket string) (float64, int, error) {
	ds, err := a.Store.GetDailySpend(day, bucket)
	return ds.SpentUSD, ds.Calls, err
}

// TrackSpend records or increments daily spend for a bucket (extract/quality).
func (s *Store) TrackSpend(day, bucket string, costUSD float64) error {
	_, err := s.db.Exec(`
		INSERT INTO daily_spend (day, bucket, spent_usd, calls, updated_at)
		VALUES (?, ?, ?, 1, datetime('now'))
		ON CONFLICT(day, bucket) DO UPDATE SET
			spent_usd = spent_usd + ?,
			calls = calls + 1,
			updated_at = datetime('now')
	`, day, bucket, costUSD, costUSD)
	return err
}

// TrackProxyUsage records proxy token usage in daily_spend with proxy-specific buckets.
// Separates input/output/cache tokens into distinct buckets for cost analysis.
func (s *Store) TrackProxyUsage(day string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) error {
	type entry struct {
		bucket string
		tokens int
	}
	entries := []entry{
		{"proxy_input", inputTokens},
		{"proxy_output", outputTokens},
		{"proxy_cache_read", cacheReadTokens},
		{"proxy_cache_write", cacheCreationTokens},
	}
	for _, e := range entries {
		if e.tokens <= 0 {
			continue
		}
		// Store token count as "spent_usd" (actually tokens — rename later or add column)
		// For now: calls = number of requests, spent_usd = total tokens
		_, err := s.db.Exec(`
			INSERT INTO daily_spend (day, bucket, spent_usd, calls, updated_at)
			VALUES (?, ?, ?, 1, datetime('now'))
			ON CONFLICT(day, bucket) DO UPDATE SET
				spent_usd = spent_usd + ?,
				calls = calls + 1,
				updated_at = datetime('now')
		`, day, e.bucket, float64(e.tokens), float64(e.tokens))
		if err != nil {
			return err
		}
	}
	return nil
}

// TrackForkUsage records fork agent token usage in daily_spend with fork-specific buckets.
func (s *Store) TrackForkUsage(day string, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int) error {
	type entry struct {
		bucket string
		tokens int
	}
	entries := []entry{
		{"fork_input", inputTokens},
		{"fork_output", outputTokens},
		{"fork_cache_read", cacheReadTokens},
		{"fork_cache_write", cacheCreationTokens},
	}
	for _, e := range entries {
		if e.tokens <= 0 {
			continue
		}
		_, err := s.db.Exec(`
			INSERT INTO daily_spend (day, bucket, spent_usd, calls, updated_at)
			VALUES (?, ?, ?, 1, datetime('now'))
			ON CONFLICT(day, bucket) DO UPDATE SET
				spent_usd = spent_usd + ?,
				calls = calls + 1,
				updated_at = datetime('now')
		`, day, e.bucket, float64(e.tokens), float64(e.tokens))
		if err != nil {
			return err
		}
	}
	return nil
}

// DailySpend represents spend for one bucket on one day.
type DailySpend struct {
	Day      string
	Bucket   string
	SpentUSD float64
	Calls    int
}

// GetDailySpend returns spend for a specific day and bucket.
func (s *Store) GetDailySpend(day, bucket string) (DailySpend, error) {
	var ds DailySpend
	err := s.readerDB().QueryRow(`
		SELECT day, bucket, spent_usd, calls FROM daily_spend
		WHERE day = ? AND bucket = ?
	`, day, bucket).Scan(&ds.Day, &ds.Bucket, &ds.SpentUSD, &ds.Calls)
	if err != nil {
		return DailySpend{Day: day, Bucket: bucket}, nil // not found = zero
	}
	return ds, nil
}

// GetAllDailySpend returns all spend entries for a specific day.
func (s *Store) GetAllDailySpend(day string) ([]DailySpend, error) {
	rows, err := s.readerDB().Query(`
		SELECT day, bucket, spent_usd, calls FROM daily_spend
		WHERE day = ? ORDER BY bucket
	`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailySpend
	for rows.Next() {
		var ds DailySpend
		if err := rows.Scan(&ds.Day, &ds.Bucket, &ds.SpentUSD, &ds.Calls); err != nil {
			return nil, err
		}
		result = append(result, ds)
	}
	return result, rows.Err()
}

// GetSpendHistory returns spend per day for the last N days.
func (s *Store) GetSpendHistory(days int) ([]DailySpend, error) {
	rows, err := s.readerDB().Query(`
		SELECT day, bucket, spent_usd, calls FROM daily_spend
		WHERE day >= date('now', ?)
		ORDER BY day DESC, bucket
	`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailySpend
	for rows.Next() {
		var ds DailySpend
		if err := rows.Scan(&ds.Day, &ds.Bucket, &ds.SpentUSD, &ds.Calls); err != nil {
			return nil, err
		}
		result = append(result, ds)
	}
	return result, rows.Err()
}
