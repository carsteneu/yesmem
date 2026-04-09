package storage

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/textutil"
)

func isEmbeddableLearning(category string) bool {
	// Narratives are briefing-only context — not useful for semantic search.
	// They're long session summaries that match everything and add noise.
	return category != "narrative"
}

func effectiveEmbeddingText(l *models.Learning) string {
	if strings.TrimSpace(l.EmbeddingText) != "" {
		return l.EmbeddingText
	}
	return l.BuildEmbeddingText()
}

func prepareEmbeddingTracking(l *models.Learning) (status, hash string) {
	if !isEmbeddableLearning(l.Category) {
		return "done", ""
	}
	return "pending", textutil.ContentHash(effectiveEmbeddingText(l))
}

// GetPendingLearningsForEmbedding returns active learnings that need embedding work.
func (s *Store) GetPendingLearningsForEmbedding(limit int) ([]models.Learning, error) {
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings
		WHERE (expires_at IS NULL OR expires_at > ?)
		AND embedding_vector IS NULL
		ORDER BY created_at ASC`
	args := []any{fmtTime(time.Now())}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get pending learnings for embedding: %w", err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// MarkEmbeddingsDone marks the given learnings as embedded successfully.
func (s *Store) MarkEmbeddingsDone(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, fmtTime(time.Now()))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `UPDATE learnings
		SET embedding_status = 'done', embedded_at = ?
		WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	_, err := s.db.Exec(query, args...)
	return err
}

// EmbeddingResult holds an embedding vector for a learning.
type EmbeddingResult struct {
	ID     int64
	Vector []float32
}

// SaveEmbeddingVectors saves embedding vectors to SQLite and marks as done.
func (s *Store) SaveEmbeddingVectors(results []EmbeddingResult) error {
	if len(results) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET embedding_vector = ?, embedding_status = 'done', embedded_at = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	now := fmtTime(time.Now())
	for _, r := range results {
		blob := float32ToBytes(r.Vector)
		if _, err := stmt.Exec(blob, now, r.ID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadEmbeddingVectors returns all active learnings with stored vectors.
// Used by the daemon to populate the in-memory VectorStore from SQLite.
func (s *Store) LoadEmbeddingVectors() ([]EmbeddingResult, error) {
	rows, err := s.readerDB().Query(`SELECT id, embedding_vector FROM learnings WHERE embedding_vector IS NOT NULL AND superseded_by IS NULL AND quarantined_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []EmbeddingResult
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		results = append(results, EmbeddingResult{ID: id, Vector: bytesToFloat32(blob)})
	}
	return results, rows.Err()
}

// float32ToBytes converts a float32 slice to bytes (little-endian).
func float32ToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

// bytesToFloat32 converts bytes back to float32 slice (little-endian).
func bytesToFloat32(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		v[i] = math.Float32frombits(bits)
	}
	return v
}

// GetAllLearningsForEmbedding returns all learnings including superseded ones.
// Used for embedding backfill — superseded learnings serve as alias search paths.
func (s *Store) GetAllLearningsForEmbedding() ([]models.Learning, error) {
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings
		ORDER BY category, created_at DESC`

	rows, err := s.readerDB().Query(query)
	if err != nil {
		return nil, fmt.Errorf("get all learnings for embedding: %w", err)
	}
	defer rows.Close()

	return scanLearnings(rows)
}
