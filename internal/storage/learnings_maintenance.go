package storage

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/textutil"
)

// DeleteOldNarratives deletes narratives for a project, keeping the N most recent.
// Narratives don't accumulate value — only the latest 2-3 matter for briefing/profiles.
func (s *Store) DeleteOldNarratives(project string, keep int) (int64, error) {
	// Clean up AQ-FTS entries for learnings about to be deleted
	s.db.Exec(`DELETE FROM anticipated_queries_fts WHERE learning_id IN (
		SELECT id FROM learnings WHERE category = 'narrative' AND project = ?
		AND id NOT IN (SELECT id FROM learnings WHERE category = 'narrative' AND project = ? ORDER BY created_at DESC LIMIT ?)
	)`, project, project, keep)

	result, err := s.db.Exec(`
		DELETE FROM learnings
		WHERE category = 'narrative'
		AND project = ?
		AND id NOT IN (
			SELECT id FROM learnings
			WHERE category = 'narrative' AND project = ?
			ORDER BY created_at DESC
			LIMIT ?
		)`, project, project, keep)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// SupersedeNarrativesBySession marks all active narratives for a session as superseded.
// Returns the IDs of superseded learnings for vector store cleanup.
func (s *Store) SupersedeNarrativesBySession(sessionID, reason string) ([]int64, error) {
	ids, err := s.collectIDs(`SELECT id FROM learnings
		WHERE category = 'narrative' AND session_id = ? AND superseded_by IS NULL`, sessionID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = -1, supersede_reason = ?, valid_until = datetime('now')
		WHERE category = 'narrative' AND session_id = ? AND superseded_by IS NULL`,
		reason, sessionID)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// SupersedeShortNarratives marks narratives from sessions with fewer than minMessages as superseded.
// Returns the IDs of superseded learnings for vector store cleanup.
func (s *Store) SupersedeShortNarratives(minMessages int) ([]int64, error) {
	ids, err := s.collectIDs(`SELECT id FROM learnings
		WHERE category = 'narrative' AND superseded_by IS NULL
		AND session_id IN (SELECT id FROM sessions WHERE message_count < ?)`, minMessages)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = -1, supersede_reason = 'session too short', valid_until = datetime('now')
		WHERE category = 'narrative' AND superseded_by IS NULL
		AND session_id IN (SELECT id FROM sessions WHERE message_count < ?)`, minMessages)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// SupersedeShortSessionLearnings marks ALL learnings from sessions with fewer than minMessages as superseded.
// Returns the IDs of superseded learnings for vector store cleanup.
func (s *Store) SupersedeShortSessionLearnings(minMessages int) ([]int64, error) {
	ids, err := s.collectIDs(`SELECT id FROM learnings
		WHERE superseded_by IS NULL
		AND session_id IN (SELECT id FROM sessions WHERE message_count < ?)`, minMessages)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = -1, supersede_reason = 'session too short for extraction', valid_until = datetime('now')
		WHERE superseded_by IS NULL
		AND session_id IN (SELECT id FROM sessions WHERE message_count < ?)`, minMessages)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// CleanupJunkLearnings marks garbage learnings as superseded.
// Targets: too short (<10 chars), JSON fragments, conversation snippets, overly long unfinished items.
// Returns the IDs of superseded learnings for vector store cleanup.
func (s *Store) CleanupJunkLearnings() ([]int64, error) {
	whereClause := "superseded_by IS NULL AND (" +
		"length(content) < 10 " +
		"OR content LIKE '{\"_%' " +
		"OR content LIKE '[%tool_use%' " +
		"OR content LIKE 'Soll ich%' " +
		"OR content LIKE 'Moment,%' " +
		"OR content LIKE 'No skills needed%' " +
		"OR content LIKE '%```json%' " +
		"OR content LIKE '%```yaml%' " +
		"OR (category = 'unfinished' AND length(content) > 500))" +
		" OR content LIKE '%GEFIXT:%'" +
		" OR content LIKE '%RESOLVED:%'" +
		" OR content LIKE '%FIXED:%'"
	ids, err := s.collectIDs("SELECT id FROM learnings WHERE " + whereClause)
	if err != nil {
		return nil, fmt.Errorf("cleanup junk collect: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec("UPDATE learnings SET superseded_by = -1, supersede_reason = 'junk cleanup', valid_until = datetime('now') WHERE " + whereClause)
	if err != nil {
		return nil, fmt.Errorf("cleanup junk: %w", err)
	}
	return ids, nil
}

// SupersedeByIDs marks specific learnings as superseded by a given learning.
func (s *Store) SupersedeByIDs(ids []int64, supersededByID int64, reason string) error {
	return s.SupersedeLearningBatch(ids, supersededByID, reason)
}

// SupersedeSessionLearnings marks all auto-extracted learnings for a session as superseded.
// Preserves user-stated and user-override learnings.
// Returns the IDs of superseded learnings for vector store cleanup.
func (s *Store) SupersedeSessionLearnings(sessionID string) ([]int64, error) {
	ids, err := s.collectIDs(`SELECT id FROM learnings
		WHERE session_id = ? AND superseded_by IS NULL
		AND (source IS NULL OR source NOT IN ('user_stated', 'user_override'))`, sessionID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = ?, supersede_reason = 'reextract', valid_until = datetime('now')
		WHERE session_id = ?
		AND superseded_by IS NULL
		AND (source IS NULL OR source NOT IN ('user_stated', 'user_override'))`,
		SupersededByReextract, sessionID)
	if err != nil {
		return nil, fmt.Errorf("supersede session learnings %s: %w", sessionID, err)
	}
	return ids, nil
}

// MigrateHitCounts copies hit_count, last_hit_at, match_count, inject_count, use_count,
// and save_count from superseded (reextract) learnings to their new active replacements,
// matched by session_id + category.
// Returns the number of updated learnings.
func (s *Store) MigrateHitCounts() (int64, error) {
	// For each (session_id, category) pair, sum counts from superseded learnings
	// and distribute to the new active ones. Uses MAX(last_hit_at) for the timestamp.
	result, err := s.db.Exec(`
		UPDATE learnings SET
			hit_count = hit_count + COALESCE((
				SELECT SUM(COALESCE(old.hit_count, 0))
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
				  AND old.hit_count > 0
			), 0),
			last_hit_at = COALESCE(learnings.last_hit_at, (
				SELECT MAX(old.last_hit_at)
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
				  AND old.last_hit_at IS NOT NULL
			)),
			match_count = match_count + COALESCE((
				SELECT SUM(COALESCE(old.match_count, 0))
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
			), 0),
			inject_count = inject_count + COALESCE((
				SELECT SUM(COALESCE(old.inject_count, 0))
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
			), 0),
			use_count = use_count + COALESCE((
				SELECT SUM(COALESCE(old.use_count, 0))
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
			), 0),
			save_count = save_count + COALESCE((
				SELECT SUM(COALESCE(old.save_count, 0))
				FROM learnings old
				WHERE old.session_id = learnings.session_id
				  AND old.category = learnings.category
				  AND old.superseded_by = ?
			), 0)
		WHERE learnings.superseded_by IS NULL
		  AND EXISTS (
			SELECT 1 FROM learnings old
			WHERE old.session_id = learnings.session_id
			  AND old.category = learnings.category
			  AND old.superseded_by = ?
		)`, SupersededByReextract, SupersededByReextract,
		SupersededByReextract, SupersededByReextract,
		SupersededByReextract, SupersededByReextract,
		SupersededByReextract)
	if err != nil {
		return 0, fmt.Errorf("migrate hit counts: %w", err)
	}
	return result.RowsAffected()
}

// BackfillContentHashes fills empty content_hash fields on existing learnings.
func (s *Store) BackfillContentHashes(hashFn func(string) string) (int64, error) {
	var count int64
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE content_hash = '' OR content_hash IS NULL`).Scan(&count)
	if count == 0 {
		return 0, nil
	}

	type pendingHash struct {
		id      int64
		content string
	}

	rows, err := s.readerDB().Query(`SELECT id, content FROM learnings WHERE content_hash = '' OR content_hash IS NULL`)
	if err != nil {
		return 0, err
	}

	pending := make([]pendingHash, 0, count)
	for rows.Next() {
		var item pendingHash
		if err := rows.Scan(&item.id, &item.content); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET content_hash = ? WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	var updated int64
	for _, item := range pending {
		hash := hashFn(item.content)
		if _, err := stmt.Exec(hash, item.id); err == nil {
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

// BackfillProjects fills empty project fields from sessions table.
func (s *Store) BackfillProjects() (int64, error) {
	result, err := s.db.Exec(`UPDATE learnings SET project = (
		SELECT s.project_short FROM sessions s WHERE s.id = learnings.session_id
	) WHERE (project IS NULL OR project = '') AND session_id != ''`)
	if err != nil {
		return 0, fmt.Errorf("backfill projects: %w", err)
	}
	return result.RowsAffected()
}

// UpdateLearningContent updates the content of an active (non-superseded) learning
// and clears embedding_text to force re-embedding on next access.
//
// modernc/sqlite has a bug where the FTS5 'delete' command inside an AFTER UPDATE
// trigger always fails with "SQL logic error (1)" on a non-external-content FTS5
// table (content_rowid= without content=). Workaround: drop the trigger, perform
// the learnings UPDATE and the FTS sync manually, then recreate the trigger — all
// inside a single transaction so the schema change is invisible to other connections.
func (s *Store) UpdateLearningContent(id int64, newContent string) error {
	// Only proceed if the learning is active.
	var count int
	if err := s.readerDB().QueryRow(
		`SELECT COUNT(*) FROM learnings WHERE id = ? AND superseded_by IS NULL`, id,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil // superseded or not found — no-op
	}
	learning, err := s.GetLearning(id)
	if err != nil {
		return err
	}
	learning.Content = newContent
	learning.EmbeddingText = ""
	_, embeddingContentHash := prepareEmbeddingTracking(learning)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Drop the FTS update trigger so it does not fire during our manual UPDATE.
	if _, err = tx.Exec(`DROP TRIGGER IF EXISTS learnings_fts_update`); err != nil {
		return err
	}
	// Update the base table.
	if _, err = tx.Exec(
		`UPDATE learnings
			 SET content = ?, content_hash = ?, embedding_text = '', embedding_status = 'pending', embedding_content_hash = ?, embedded_at = NULL
			 WHERE id = ? AND superseded_by IS NULL`,
		newContent, textutil.ContentHash(newContent), embeddingContentHash, id); err != nil {
		return err
	}
	// Sync FTS manually: remove old entry, insert new one.
	if _, err = tx.Exec(`DELETE FROM learnings_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO learnings_fts(rowid, content) VALUES (?, ?)`, id, newContent); err != nil {
		return err
	}
	// Recreate the trigger identical to the original schema definition.
	if _, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS learnings_fts_update
	AFTER UPDATE OF content ON learnings BEGIN
		INSERT INTO learnings_fts(learnings_fts, rowid, content) VALUES ('delete', old.id, old.content);
		INSERT INTO learnings_fts(rowid, content) VALUES (new.id, new.content);
	END`); err != nil {
		return err
	}
	return tx.Commit()
}
