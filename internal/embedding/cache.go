package embedding

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// CachedProvider wraps any Provider with a persistent SQLite query cache.
// Cache entries are keyed by SHA256(query_text) and invalidated on model change.
type CachedProvider struct {
	inner Provider
	db    *sql.DB
	model string // e.g. "static"
}

// NewCachedProvider wraps inner with a persistent cache backed by db.
// model identifies the current embedding model — cache entries from other models are ignored.
func NewCachedProvider(inner Provider, db *sql.DB, model string) *CachedProvider {
	return &CachedProvider{inner: inner, db: db, model: model}
}

func (c *CachedProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	var misses []int

	for i, text := range texts {
		hash := hashQuery(text)
		vec, err := c.loadFromCache(hash)
		if err == nil && vec != nil {
			results[i] = vec
		} else {
			misses = append(misses, i)
		}
	}

	if len(misses) == 0 {
		return results, nil
	}

	// Batch-embed all cache misses
	missTexts := make([]string, len(misses))
	for j, idx := range misses {
		missTexts[j] = texts[idx]
	}

	vecs, err := c.inner.Embed(ctx, missTexts)
	if err != nil {
		return nil, err
	}

	for j, idx := range misses {
		results[idx] = vecs[j]
		c.saveToCache(hashQuery(texts[idx]), texts[idx], vecs[j])
	}

	return results, nil
}

func (c *CachedProvider) Dimensions() int { return c.inner.Dimensions() }
func (c *CachedProvider) Enabled() bool   { return c.inner.Enabled() }
func (c *CachedProvider) Close() error    { return c.inner.Close() }

// InvalidateAll clears the entire cache (call on model change).
func (c *CachedProvider) InvalidateAll() error {
	_, err := c.db.Exec("DELETE FROM embedding_cache")
	return err
}

func hashQuery(text string) string {
	h := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", h)
}

func (c *CachedProvider) loadFromCache(hash string) ([]float32, error) {
	var blob []byte
	var model string
	err := c.db.QueryRow(
		"SELECT vector, model FROM embedding_cache WHERE query_hash = ?", hash,
	).Scan(&blob, &model)
	if err != nil {
		return nil, err
	}
	if model != c.model {
		c.db.Exec("DELETE FROM embedding_cache WHERE query_hash = ?", hash)
		return nil, fmt.Errorf("model mismatch")
	}
	return deserializeVector(blob), nil
}

func (c *CachedProvider) saveToCache(hash, text string, vec []float32) {
	blob := serializeVector(vec)
	c.db.Exec(
		"INSERT OR REPLACE INTO embedding_cache (query_hash, query_text, vector, model, created_at) VALUES (?, ?, ?, ?, ?)",
		hash, text, blob, c.model, time.Now().UTC().Format(time.RFC3339),
	)
}

func serializeVector(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func deserializeVector(buf []byte) []float32 {
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec
}
