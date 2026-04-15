package storage

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// InsertLearning inserts a learning and returns its ID.
func (s *Store) InsertLearning(l *models.Learning) (int64, error) {
	ids, err := s.InsertLearningBatch([]*models.Learning{l})
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("insert learning: no id returned")
	}
	return ids[0], nil
}

// HasPulseForSession checks if a pulse learning already exists for the given session.
func (s *Store) HasPulseForSession(sessionID string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM learnings WHERE session_id = ? AND category = 'pulse'", sessionID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check pulse for %s: %w", sessionID, err)
	}
	return count > 0, nil
}

// GetPulseLearningsSince returns pulse learnings for a project created after since.
func (s *Store) GetPulseLearningsSince(project string, since time.Time, limit int) ([]models.Learning, error) {
	rows, err := s.readerDB().Query(`SELECT id, session_id, content, created_at FROM learnings
		WHERE category = 'pulse' AND (project = ? OR project IS NULL OR project = '')
		AND created_at > ? AND superseded_by IS NULL
		ORDER BY created_at ASC LIMIT ?`,
		project, since.Format(time.RFC3339), limit)
	if err != nil {
		return nil, fmt.Errorf("get pulse learnings: %w", err)
	}
	defer rows.Close()

	var result []models.Learning
	for rows.Next() {
		var l models.Learning
		var createdAt string
		if err := rows.Scan(&l.ID, &l.SessionID, &l.Content, &createdAt); err != nil {
			return nil, fmt.Errorf("scan pulse: %w", err)
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		l.Category = "pulse"
		result = append(result, l)
	}
	return result, nil
}

// InsertLearningBatch inserts multiple learnings in a single transaction.
// Returns the IDs of all inserted learnings. One COMMIT instead of N reduces
// FTS5 write contention that blocks concurrent BM25 reads.
func (s *Store) InsertLearningBatch(learnings []*models.Learning) ([]int64, error) {
	if len(learnings) == 0 {
		return nil, nil
	}

	// Bulk-fetch current turn counts BEFORE opening transaction (avoids deadlock with MaxOpenConns=1)
	projects := make(map[string]bool)
	hasGlobal := false
	for _, l := range learnings {
		if l.Project != "" {
			projects[l.Project] = true
		} else {
			hasGlobal = true
		}
	}
	projectList := make([]string, 0, len(projects)+1)
	for p := range projects {
		projectList = append(projectList, p)
	}
	if hasGlobal {
		projectList = append(projectList, "__global__")
	}
	turnCounts, _ := s.GetTurnCountsBulk(projectList)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO learnings
		(session_id, category, content, project, confidence, created_at, expires_at, model_used, source, emotional_intensity, session_flavor, supersedes, importance, context, domain, trigger_rule, embedding_text, embedding_status, embedding_content_hash, embedded_at, source_file, source_hash, doc_chunk_ref, content_hash, task_type, turns_at_creation, source_msg_from, source_msg_to)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	// Keep learnings_fts read-after-write consistent for interactive search/resolve
	// paths without relying on the periodic background sync.
	ftsStmt, err := tx.Prepare(`INSERT OR REPLACE INTO learnings_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare learnings fts: %w", err)
	}
	defer ftsStmt.Close()

	var ids []int64
	for _, l := range learnings {
		var expiresAt *string
		if l.ExpiresAt != nil {
			t := fmtTime(*l.ExpiresAt)
			expiresAt = &t
		}

		source := l.Source
		if source == "" {
			source = "llm_extracted"
		}
		if l.ContentHash == "" {
			l.ContentHash = textutil.ContentHash(l.Content)
		}
		embeddingStatus, embeddingContentHash := prepareEmbeddingTracking(l)

		importance := l.Importance
		if importance == 0 {
			switch source {
			case "user_stated", "agreed_upon":
				importance = 4
			case "claude_suggested":
				importance = 2
			default:
				importance = 3
			}
		}

		turnKey := l.Project
		if turnKey == "" {
			turnKey = "__global__"
		}

		// Lineage sentinel: Go zero-value (0) means "not set" — normalize to -1
		// so we can distinguish "no lineage" from "extracted from message 0".
		sourceMsgFrom, sourceMsgTo := l.SourceMsgFrom, l.SourceMsgTo
		if sourceMsgFrom == 0 && sourceMsgTo == 0 {
			sourceMsgFrom, sourceMsgTo = -1, -1
		}

		result, err := stmt.Exec(
			l.SessionID, l.Category, l.Content, l.Project, l.Confidence,
			fmtTime(l.CreatedAt), expiresAt, l.ModelUsed, source, l.EmotionalIntensity, l.SessionFlavor, l.Supersedes, importance,
			l.Context, l.Domain, l.TriggerRule, l.EmbeddingText, embeddingStatus, embeddingContentHash, l.SourceFile, l.SourceHash, l.DocChunkRef, l.ContentHash, l.TaskType, turnCounts[turnKey], sourceMsgFrom, sourceMsgTo)
		if err != nil {
			return ids, fmt.Errorf("insert learning: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)

		searchableText := BuildSearchableText(l.Content, l.Keywords, l.AnticipatedQueries, l.Entities)
		if _, err := ftsStmt.Exec(id, searchableText); err != nil {
			return ids, fmt.Errorf("insert learning fts: %w", err)
		}

		// Junction tables within same transaction
		for _, e := range l.Entities {
			tx.Exec("INSERT INTO learning_entities (learning_id, value) VALUES (?, ?)", id, e)
		}
		for _, a := range l.Actions {
			tx.Exec("INSERT INTO learning_actions (learning_id, value) VALUES (?, ?)", id, a)
		}
		for _, k := range l.Keywords {
			tx.Exec("INSERT INTO learning_keywords (learning_id, value) VALUES (?, ?)", id, k)
		}
		for _, q := range l.AnticipatedQueries {
			tx.Exec("INSERT INTO learning_anticipated_queries (learning_id, value) VALUES (?, ?)", id, q)
			tx.Exec("INSERT INTO anticipated_queries_fts (value, learning_id) VALUES (?, ?)", q, id)
		}

		// Expand FTS5 index to include anticipated queries for BM25 discovery.
		// The trigger already inserted with content only — replace with expanded version.
		if len(l.AnticipatedQueries) > 0 {
			expanded := l.Content + " " + strings.Join(l.AnticipatedQueries, " ")
			tx.Exec("INSERT INTO learnings_fts(learnings_fts, rowid, content) VALUES ('delete', ?, ?)", id, l.Content)
			tx.Exec("INSERT INTO learnings_fts(rowid, content) VALUES (?, ?)", id, expanded)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit batch: %w", err)
	}
	return ids, nil
}

// RebuildFTSEnriched rebuilds the FTS index with enriched content (content + keywords + anticipated_queries + entities).
// Safe alternative to trigger-based enrichment — avoids FTS5 delete bug with modernc/sqlite [ID:37853].
// Call once after batch insertion, not per-learning.
func (s *Store) RebuildFTSEnriched() (int, error) {
	rows, err := s.db.Query(`SELECT l.id, l.content FROM learnings l WHERE l.superseded_by IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("query learnings: %w", err)
	}
	defer rows.Close()

	type entry struct {
		id      int64
		content string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.content); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	// Load ALL junction data BEFORE starting transaction (avoids deadlock with MaxOpenConns=1)
	type enrichedEntry struct {
		id       int64
		enriched string
	}
	var enriched []enrichedEntry
	for _, e := range entries {
		var keywords, queries, entities []string
		kRows, _ := s.readerDB().Query("SELECT value FROM learning_keywords WHERE learning_id = ?", e.id)
		if kRows != nil {
			for kRows.Next() {
				var v string
				kRows.Scan(&v)
				keywords = append(keywords, v)
			}
			kRows.Close()
		}
		qRows, _ := s.readerDB().Query("SELECT value FROM learning_anticipated_queries WHERE learning_id = ?", e.id)
		if qRows != nil {
			for qRows.Next() {
				var v string
				qRows.Scan(&v)
				queries = append(queries, v)
			}
			qRows.Close()
		}
		eRows, _ := s.readerDB().Query("SELECT value FROM learning_entities WHERE learning_id = ?", e.id)
		if eRows != nil {
			for eRows.Next() {
				var v string
				eRows.Scan(&v)
				entities = append(entities, v)
			}
			eRows.Close()
		}
		enriched = append(enriched, enrichedEntry{id: e.id, enriched: BuildSearchableText(e.content, keywords, queries, entities)})
	}

	// Rebuild FTS: clear + populate with enriched content (all reads done, only writes below)
	if _, err := s.db.Exec(`DELETE FROM learnings_fts`); err != nil {
		return 0, fmt.Errorf("clear fts: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT INTO learnings_fts(rowid, content) VALUES (?, ?)`)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for _, e := range enriched {
		if _, err := stmt.Exec(e.id, e.enriched); err != nil {
			log.Printf("  [fts] skip %d: %v", e.id, err)
			continue
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit fts rebuild: %w", err)
	}
	return count, nil
}

// BuildSearchableText combines content with keywords, anticipated_queries, and entities.
// This makes metadata searchable via BM25 without changing the stored content.
func BuildSearchableText(content string, keywords, anticipatedQueries, entities []string) string {
	if len(keywords) == 0 && len(anticipatedQueries) == 0 && len(entities) == 0 {
		return content
	}
	var sb strings.Builder
	sb.WriteString(content)
	for _, v := range keywords {
		sb.WriteString(" ")
		sb.WriteString(v)
	}
	for _, v := range anticipatedQueries {
		sb.WriteString(" ")
		sb.WriteString(v)
	}
	for _, v := range entities {
		sb.WriteString(" ")
		sb.WriteString(v)
	}
	return sb.String()
}

// GetLearningsByCategory returns active learnings of a specific category.
func (s *Store) GetLearningsByCategory(category, project string, limit int) ([]models.Learning, error) {
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings WHERE superseded_by IS NULL
		AND category = ?
		AND (project = ? OR project IS NULL OR project = '')
		ORDER BY created_at DESC
		LIMIT ?`
	rows, err := s.readerDB().Query(query, category, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// GetLearningsSince returns active learnings created after the given time.
// Filters to decision, pattern, gotcha, pivot_moment categories (most useful for metamemory).
func (s *Store) GetLearningsSince(project string, since time.Time, limit int) ([]models.Learning, error) {
	short := filepath.Base(project)
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings WHERE superseded_by IS NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND created_at > ?
		AND category IN ('decision', 'pattern', 'gotcha', 'pivot_moment', 'unfinished', 'explicit_teaching')
		AND (project = ? OR project = ? OR project IS NULL)
		ORDER BY created_at DESC
		LIMIT ?`

	rows, err := s.readerDB().Query(query, fmtTime(time.Now()), fmtTime(since), project, short, limit)
	if err != nil {
		return nil, fmt.Errorf("get learnings since: %w", err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// GetSessionFlavorsSince returns distinct session flavors grouped by session_id since the given time.
func (s *Store) GetSessionFlavorsSince(project string, since time.Time, limit int) ([]map[string]any, error) {
	// Learnings store short project names (e.g. "memory"), but proxy passes full paths
	// (e.g. "/home/user/projects/myproject"). Match both formats.
	short := filepath.Base(project)
	query := `SELECT session_flavor, MIN(created_at) as first_seen, session_id
		FROM learnings
		WHERE superseded_by IS NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND created_at > ?
		AND length(session_flavor) > 0
		AND (project = ? OR project = ? OR project IS NULL)
		GROUP BY session_id
		ORDER BY first_seen ASC
		LIMIT ?`

	rows, err := s.readerDB().Query(query, fmtTime(time.Now()), fmtTime(since), project, short, limit)
	if err != nil {
		return nil, fmt.Errorf("get session flavors since: %w", err)
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var flavor, sessionID, createdAt string
		if err := rows.Scan(&flavor, &createdAt, &sessionID); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"flavor":     flavor,
			"created_at": createdAt,
			"session_id": sessionID,
		})
	}
	return results, nil
}

// GetAgentRolesBulk returns agent_role for a list of learning IDs.
func (s *Store) GetAgentRolesBulk(ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf("SELECT id, COALESCE(agent_role, '') FROM learnings WHERE id IN (%s)", strings.Join(placeholders, ","))
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string, len(ids))
	for rows.Next() {
		var id int64
		var role string
		if rows.Scan(&id, &role) == nil && role != "" {
			result[fmt.Sprintf("%d", id)] = role
		}
	}
	return result, nil
}

// GetActiveCategories returns all distinct categories that have active (non-superseded) learnings.
func (s *Store) GetActiveCategories() ([]string, error) {
	rows, err := s.readerDB().Query(`SELECT DISTINCT category FROM learnings
		WHERE superseded_by IS NULL ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			cats = append(cats, c)
		}
	}
	return cats, rows.Err()
}

// GetActiveLearnings returns all non-superseded, non-expired learnings.
// Filter by category and/or project (empty string = no filter).
func (s *Store) GetActiveLearnings(category, project, since, before string) ([]models.Learning, error) {
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings WHERE superseded_by IS NULL
		AND (expires_at IS NULL OR expires_at > ?)`
	args := []any{fmtTime(time.Now())}

	if category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	if project != "" {
		query += ` AND (project = ? OR project IS NULL)`
		args = append(args, project)
	}
	if since != "" {
		query += ` AND created_at >= ?`
		args = append(args, since)
	}
	if before != "" {
		query += ` AND created_at < ?`
		args = append(args, before)
	}
	query += ` ORDER BY category, created_at DESC`

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get active learnings: %w", err)
	}
	defer rows.Close()

	return scanLearnings(rows)
}

// GetActiveLearningsBySessionIDs returns active learnings for the given session IDs.
func (s *Store) GetActiveLearningsBySessionIDs(sessionIDs []string) ([]models.Learning, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(sessionIDs))
	args := make([]any, 0, len(sessionIDs)+1)
	args = append(args, fmtTime(time.Now()))
	for i, sessionID := range sessionIDs {
		placeholders[i] = "?"
		args = append(args, sessionID)
	}

	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings
		WHERE superseded_by IS NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND session_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY created_at DESC`

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get active learnings by session IDs: %w", err)
	}
	defer rows.Close()

	return scanLearnings(rows)
}

// GetLearningsForClaudeMd returns active learnings for a project with per-category caps.
func (s *Store) GetLearningsForClaudeMd(project string, maxPerCategory map[string]int) ([]models.Learning, error) {
	all, err := s.GetActiveLearnings("", project, "", "")
	if err != nil {
		return nil, err
	}
	// Score-sort so per-category cap keeps highest-value learnings
	models.ScoreAndSort(all)
	if len(maxPerCategory) == 0 {
		return all, nil
	}
	counts := map[string]int{}
	result := make([]models.Learning, 0, len(all))
	for _, l := range all {
		cap, ok := maxPerCategory[l.Category]
		if !ok {
			cap = 10
		}
		if counts[l.Category] < cap {
			result = append(result, l)
			counts[l.Category]++
		}
	}
	return result, nil
}

// GetLearning returns a single learning by ID.
func (s *Store) GetLearning(id int64) (*models.Learning, error) {
	row := s.readerDB().QueryRow(`SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings WHERE id = ?`, id)

	l := &models.Learning{}
	var createdAt string
	var expiresAt, sessionID, project, supersedeReason, source, lastHitAt, validUntil, supersedeStatus *string
	var supersededBy, supersedes *int64
	err := row.Scan(&l.ID, &sessionID, &l.Category, &l.Content, &project, &l.Confidence,
		&supersededBy, &supersedeReason, &createdAt, &expiresAt, &l.ModelUsed, &source, &l.HitCount, &l.EmotionalIntensity, &lastHitAt, &l.SessionFlavor, &validUntil, &supersedes, &l.Importance, &supersedeStatus, &l.NoiseCount, &l.FailCount,
		&l.MatchCount, &l.InjectCount, &l.UseCount, &l.SaveCount, &l.Stability,
		&l.Context, &l.Domain, &l.TriggerRule, &l.EmbeddingText,
		&l.SourceFile, &l.SourceHash, &l.DocChunkRef, &l.TaskType, &l.TurnsAtCreation, &l.SourceMsgFrom, &l.SourceMsgTo)
	if err != nil {
		return nil, fmt.Errorf("get learning %d: %w", id, err)
	}
	l.CreatedAt = parseTime(createdAt)
	if source != nil {
		l.Source = *source
	}
	if expiresAt != nil {
		t := parseTime(*expiresAt)
		l.ExpiresAt = &t
	}
	if lastHitAt != nil {
		t := parseTime(*lastHitAt)
		l.LastHitAt = &t
	}
	if sessionID != nil {
		l.SessionID = *sessionID
	}
	if project != nil {
		l.Project = *project
	}
	if supersededBy != nil {
		l.SupersededBy = supersededBy
	}
	if supersedeReason != nil {
		l.SupersedeReason = *supersedeReason
	}
	if validUntil != nil {
		t := parseTime(*validUntil)
		l.ValidUntil = &t
	}
	if supersedes != nil {
		l.Supersedes = supersedes
	}
	if supersedeStatus != nil {
		l.SupersedeStatus = *supersedeStatus
	}
	return l, nil
}

const (
	SupersededByResolved  int64 = -2
	SupersededByReextract int64 = -3
)

// ResolveLearning marks an unfinished learning as resolved (superseded_by = -2).
// Returns error if learning doesn't exist or is already superseded.
func (s *Store) ResolveLearning(id int64, reason string) error {
	result, err := s.db.Exec(`UPDATE learnings SET superseded_by = ?, supersede_reason = ?, valid_until = datetime('now')
		WHERE id = ? AND superseded_by IS NULL`,
		SupersededByResolved, reason, id)
	if err != nil {
		return fmt.Errorf("resolve learning %d: %w", id, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("learning %d not found or already superseded", id)
	}
	return nil
}

// GetTriggeredLearnings returns active learnings with non-empty trigger_rule
// that haven't been triggered recently (12h cooldown via last_hit_at).
func (s *Store) GetTriggeredLearnings(project string) ([]models.Learning, error) {
	now := time.Now()
	cooldown := now.Add(-12 * time.Hour)
	query := `SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings
		WHERE superseded_by IS NULL
		AND trigger_rule != '' AND trigger_rule IS NOT NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND (last_hit_at IS NULL OR last_hit_at < ?)
		AND (project = ? OR project IS NULL OR project = '')
		LIMIT 10`
	rows, err := s.readerDB().Query(query, fmtTime(now), fmtTime(cooldown), project)
	if err != nil {
		return nil, fmt.Errorf("get triggered learnings: %w", err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// splitWords splits a string into lowercase words.
func splitWords(s string) []string {
	var words []string
	for _, w := range strings.Fields(s) {
		w = strings.ToLower(w)
		if len(w) >= 2 {
			words = append(words, w)
		}
	}
	return words
}

// ResolveBatch marks multiple learnings as resolved in a single transaction.
func (s *Store) ResolveBatch(ids []int64, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE learnings SET superseded_by = ?, supersede_reason = ?, valid_until = datetime('now')
		WHERE id = ? AND superseded_by IS NULL`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(SupersededByResolved, reason, id); err != nil {
			tx.Rollback()
			return fmt.Errorf("resolve %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// isInSupersededChain checks if targetID appears in the supersede chain starting from startID.
// Used to prevent cycles when setting superseded_by.
func (s *Store) isInSupersededChain(startID, targetID int64) bool {
	current := startID
	for i := 0; i < 10; i++ {
		var next sql.NullInt64
		err := s.readerDB().QueryRow("SELECT superseded_by FROM learnings WHERE id = ?", current).Scan(&next)
		if err != nil || !next.Valid || next.Int64 <= 0 {
			return false
		}
		if next.Int64 == targetID {
			return true
		}
		current = next.Int64
	}
	return false
}

// SupersedeLearning marks a learning as superseded by another.
func (s *Store) SupersedeLearning(id, supersededByID int64, reason string) error {
	return s.SupersedeLearningBatch([]int64{id}, supersededByID, reason)
}

// SupersedeLearningBatch marks multiple learnings as superseded in a single transaction.
// Two UPDATEs per learning (mark + backlink) × N → 1 COMMIT instead of 2N.
func (s *Store) SupersedeLearningBatch(ids []int64, supersededByID int64, reason string) error {
	if len(ids) == 0 {
		return nil
	}

	// Cycle detection: skip any ID where the winner is already in the loser's chain.
	if supersededByID > 0 {
		filtered := make([]int64, 0, len(ids))
		for _, id := range ids {
			if s.isInSupersededChain(supersededByID, id) {
				log.Printf("warn: supersede cycle detected: %d -> %d -> ... -> %d, skipping", id, supersededByID, id)
				continue
			}
			filtered = append(filtered, id)
		}
		ids = filtered
		if len(ids) == 0 {
			return nil
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	markStmt, err := tx.Prepare(`UPDATE learnings SET superseded_by = ?, supersede_reason = ?, valid_until = datetime('now')
		WHERE id = ? AND superseded_by IS NULL`)
	if err != nil {
		return fmt.Errorf("prepare mark: %w", err)
	}
	defer markStmt.Close()

	linkStmt, err := tx.Prepare(`UPDATE learnings SET supersedes = ? WHERE id = ? AND supersedes IS NULL`)
	if err != nil {
		return fmt.Errorf("prepare link: %w", err)
	}
	defer linkStmt.Close()

	for _, id := range ids {
		markStmt.Exec(supersededByID, reason, id)
		if supersededByID > 0 {
			linkStmt.Exec(id, supersededByID)
		}
		// Clean up AQ-FTS for superseded learning
		tx.Exec("DELETE FROM anticipated_queries_fts WHERE learning_id = ?", id)
	}

	return tx.Commit()
}

// ResolveSupersededIDs takes a list of learning IDs and returns a map from
// superseded ID → active successor. Follows chains (A→B→C) up to 10 hops.
// IDs that are active (superseded_by IS NULL) or resolved (superseded_by < 0)
// are not included in the map — they need no redirect.
func (s *Store) ResolveSupersededIDs(ids []int64) (map[int64]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	// Single recursive CTE replaces N+1 per-hop queries.
	// Walks each supersede chain up to 10 hops, then picks the deepest
	// (= final) target per original_id.
	// Also captures negative superseded_by (junk/resolved) so the caller can drop them.
	query := `WITH RECURSIVE chain(original_id, current_id, depth) AS (
		SELECT id, CAST(superseded_by AS INTEGER), 1
		FROM learnings
		WHERE id IN (` + strings.Join(placeholders, ",") + `)
		AND superseded_by IS NOT NULL
		UNION ALL
		SELECT c.original_id, CAST(l.superseded_by AS INTEGER), c.depth + 1
		FROM chain c
		JOIN learnings l ON l.id = c.current_id
		WHERE l.superseded_by IS NOT NULL AND l.superseded_by > 0
		AND c.depth < 10
	)
	SELECT original_id, current_id FROM chain
	WHERE NOT EXISTS (
		SELECT 1 FROM chain c2
		WHERE c2.original_id = chain.original_id
		AND c2.depth > chain.depth
	)`

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve superseded: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]int64)
	for rows.Next() {
		var original, target int64
		if err := rows.Scan(&original, &target); err == nil {
			result[original] = target
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// GetRecentNarratives returns the N newest narrative learnings for a project.
// Only includes narratives from sessions with at least minMessages messages.
func (s *Store) GetRecentNarratives(project string, limit int) ([]models.Learning, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := s.readerDB().Query(`SELECT l.id, l.session_id, l.category, l.content, l.project, l.confidence,
		l.superseded_by, l.supersede_reason, l.created_at, l.expires_at, l.model_used, l.source,
		COALESCE(l.hit_count, 0), COALESCE(l.emotional_intensity, 0.0), l.last_hit_at, COALESCE(l.session_flavor, ''), l.valid_until, l.supersedes, COALESCE(l.importance, 3), l.supersede_status, COALESCE(l.noise_count, 0), COALESCE(l.fail_count, 0),
		COALESCE(l.match_count, 0), COALESCE(l.inject_count, 0), COALESCE(l.use_count, 0), COALESCE(l.save_count, 0), COALESCE(l.stability, 30.0),
		COALESCE(l.context, ''), COALESCE(l.domain, 'code'), COALESCE(l.trigger_rule, ''), COALESCE(l.embedding_text, ''),
		COALESCE(l.source_file, ''), COALESCE(l.source_hash, ''), COALESCE(l.doc_chunk_ref, 0), COALESCE(l.task_type, ''), COALESCE(l.turns_at_creation, 0), COALESCE(l.source_msg_from, -1), COALESCE(l.source_msg_to, -1)
		FROM learnings l
		LEFT JOIN sessions s ON l.session_id = s.id
		WHERE l.category = 'narrative' AND l.project = ? AND l.superseded_by IS NULL
		AND (s.message_count IS NULL OR s.message_count = 0 OR s.message_count >= 10)
		ORDER BY l.created_at DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, fmt.Errorf("get recent narratives: %w", err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

func scanLearnings(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]models.Learning, error) {
	var learnings []models.Learning
	for rows.Next() {
		var l models.Learning
		var createdAt string
		var expiresAt, sessionID, project, supersedeReason, source, lastHitAt, validUntil, supersedeStatus *string
		var supersededBy, supersedes *int64
		if err := rows.Scan(&l.ID, &sessionID, &l.Category, &l.Content, &project, &l.Confidence,
			&supersededBy, &supersedeReason, &createdAt, &expiresAt, &l.ModelUsed, &source, &l.HitCount, &l.EmotionalIntensity, &lastHitAt, &l.SessionFlavor, &validUntil, &supersedes, &l.Importance, &supersedeStatus, &l.NoiseCount, &l.FailCount,
			&l.MatchCount, &l.InjectCount, &l.UseCount, &l.SaveCount, &l.Stability,
			&l.Context, &l.Domain, &l.TriggerRule, &l.EmbeddingText,
			&l.SourceFile, &l.SourceHash, &l.DocChunkRef, &l.TaskType, &l.TurnsAtCreation, &l.SourceMsgFrom, &l.SourceMsgTo); err != nil {
			return nil, err
		}
		l.CreatedAt = parseTime(createdAt)
		if expiresAt != nil {
			t := parseTime(*expiresAt)
			l.ExpiresAt = &t
		}
		if lastHitAt != nil {
			t := parseTime(*lastHitAt)
			l.LastHitAt = &t
		}
		if sessionID != nil {
			l.SessionID = *sessionID
		}
		if project != nil {
			l.Project = *project
		}
		if supersededBy != nil {
			l.SupersededBy = supersededBy
		}
		if supersedeReason != nil {
			l.SupersedeReason = *supersedeReason
		}
		if validUntil != nil {
			t := parseTime(*validUntil)
			l.ValidUntil = &t
		}
		if supersedes != nil {
			l.Supersedes = supersedes
		}
		if source != nil {
			l.Source = *source
		}
		if supersedeStatus != nil {
			l.SupersedeStatus = *supersedeStatus
		}
		learnings = append(learnings, l)
	}
	return learnings, rows.Err()
}

// LoadJunctionData loads entities, actions, keywords, anticipated_queries for a learning.
func (s *Store) LoadJunctionData(l *models.Learning) error {
	return s.loadJunctionData(l)
}

func (s *Store) loadJunctionData(l *models.Learning) error {
	rows, err := s.readerDB().Query("SELECT value FROM learning_entities WHERE learning_id = ?", l.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		l.Entities = append(l.Entities, v)
	}

	rows2, err := s.readerDB().Query("SELECT value FROM learning_actions WHERE learning_id = ?", l.ID)
	if err != nil {
		return err
	}
	defer rows2.Close()
	for rows2.Next() {
		var v string
		if err := rows2.Scan(&v); err != nil {
			return err
		}
		l.Actions = append(l.Actions, v)
	}

	rows3, err := s.readerDB().Query("SELECT value FROM learning_keywords WHERE learning_id = ?", l.ID)
	if err != nil {
		return err
	}
	defer rows3.Close()
	for rows3.Next() {
		var v string
		if err := rows3.Scan(&v); err != nil {
			return err
		}
		l.Keywords = append(l.Keywords, v)
	}

	rows4, err := s.readerDB().Query("SELECT value FROM learning_anticipated_queries WHERE learning_id = ?", l.ID)
	if err != nil {
		return err
	}
	defer rows4.Close()
	for rows4.Next() {
		var v string
		if err := rows4.Scan(&v); err != nil {
			return err
		}
		l.AnticipatedQueries = append(l.AnticipatedQueries, v)
	}
	return nil
}

// BatchLoadEntities loads entities for multiple learning IDs at once.
func (s *Store) BatchLoadEntities(ids []int64) map[int64][]string {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT learning_id, value FROM learning_entities WHERE learning_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var v string
		if rows.Scan(&id, &v) == nil {
			result[id] = append(result[id], v)
		}
	}
	return result
}

// BatchLoadActions loads actions for multiple learning IDs at once.
func (s *Store) BatchLoadActions(ids []int64) map[int64][]string {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT learning_id, value FROM learning_actions WHERE learning_id IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var v string
		if rows.Scan(&id, &v) == nil {
			result[id] = append(result[id], v)
		}
	}
	return result
}

// collectIDs runs a SELECT query and returns all matching integer IDs.
func (s *Store) collectIDs(query string, args ...any) ([]int64, error) {
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// InsertContradiction records a contradiction flagged by Claude.
func (s *Store) InsertContradiction(learningIDs string, description, project, threadID string) error {
	_, err := s.db.Exec(`
		INSERT INTO contradictions (learning_ids, description, project, thread_id)
		VALUES (?, ?, ?, ?)
	`, learningIDs, description, project, threadID)
	return err
}

// GetLearningCounts returns category → active learning count for a project.
// Excludes: unfinished (has own section), preference/relationship (in Persona section), narrative (not knowledge).
func (s *Store) GetLearningCounts(project string) (map[string]int, error) {
	projectFilter := "1=1"
	args := []any{fmtTime(time.Now())}
	if project != "" {
		projectFilter = "(project = ? OR project IS NULL OR project = '')"
		args = append(args, project)
	}
	query := `SELECT category, COUNT(*) FROM learnings
		WHERE superseded_by IS NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND category NOT IN ('unfinished', 'preference', 'relationship', 'narrative')
		AND ` + projectFilter + `
		GROUP BY category`
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get learning counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		counts[cat] = count
	}
	return counts, rows.Err()
}

// GetLearningByContentHash finds an active learning with the given content hash.
func (s *Store) GetLearningByContentHash(hash string) (*models.Learning, error) {
	if hash == "" {
		return nil, nil
	}
	row := s.readerDB().QueryRow(`SELECT id, content, category, project, COALESCE(match_count, 0)
		FROM learnings WHERE content_hash = ? AND superseded_by IS NULL LIMIT 1`, hash)
	var l models.Learning
	err := row.Scan(&l.ID, &l.Content, &l.Category, &l.Project, &l.MatchCount)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// CountLearningSince returns the number of learnings created after the given timestamp.
func (s *Store) CountLearningSince(since time.Time) int {
	var count int
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE created_at > ?`, fmtTime(since)).Scan(&count)
	return count
}

// GetRecentLearningIDs returns the N most recently created learning IDs.
// Used by hybrid_search skip_recent to prevent echo injection of just-extracted learnings.
func (s *Store) GetRecentLearningIDs(n int) ([]int64, error) {
	rows, err := s.readerDB().Query(`SELECT id FROM learnings ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// GetLatestSessionID returns the most recent session ID for a project.
func (s *Store) GetLatestSessionID(project string) (string, error) {
	var sid string
	var err error
	if project != "" {
		err = s.readerDB().QueryRow(`SELECT id FROM sessions WHERE project_short = ? ORDER BY started_at DESC LIMIT 1`, project).Scan(&sid)
	} else {
		err = s.readerDB().QueryRow(`SELECT id FROM sessions ORDER BY started_at DESC LIMIT 1`).Scan(&sid)
	}
	return sid, err
}

// GetCategoriesBulk returns a map of id → category for the given IDs.
func (s *Store) GetCategoriesBulk(ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.readerDB().Query(
		`SELECT CAST(id AS TEXT), category FROM learnings WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string, len(ids))
	for rows.Next() {
		var id, cat string
		if rows.Scan(&id, &cat) == nil {
			result[id] = cat
		}
	}
	return result, rows.Err()
}

// GetSupersededChain returns the supersede history for a learning (newest first).
// Follows the supersedes backlink up to maxDepth levels deep.
func (s *Store) GetSupersededChain(id int64, maxDepth int) ([]models.Learning, error) {
	if maxDepth <= 0 {
		maxDepth = 10
	}
	var chain []models.Learning
	currentID := id
	for i := 0; i < maxDepth; i++ {
		l, err := s.GetLearning(currentID)
		if err != nil {
			break
		}
		chain = append(chain, *l)
		if l.Supersedes == nil {
			break
		}
		currentID = *l.Supersedes
	}
	return chain, nil
}

// GetVolatility returns how often a topic has been superseded (chain length - 1).
func (s *Store) GetVolatility(id int64) (int, error) {
	chain, err := s.GetSupersededChain(id, 10)
	if err != nil {
		return 0, err
	}
	if len(chain) <= 1 {
		return 0, nil
	}
	return len(chain) - 1, nil
}

// SetSupersedeStatus updates the supersede_status of a learning.
func (s *Store) SetSupersedeStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE learnings SET supersede_status = ? WHERE id = ?`, status, id)
	return err
}

// ForkLearning represents a learning extracted by a fork in the current session.
type ForkLearning struct {
	ID       int64
	Content  string
	Category string
	Source   string
}

// UpdateImpactScore updates the running average impact score for a learning.
func (s *Store) UpdateImpactScore(learningID int64, score float64) error {
	_, err := s.db.Exec(`UPDATE learnings SET impact_score = (COALESCE(impact_score, 0.0) * COALESCE(impact_count, 0) + ?) / (COALESCE(impact_count, 0) + 1), impact_count = COALESCE(impact_count, 0) + 1 WHERE id = ?`, score, learningID)
	return err
}

// GetForkLearnings returns all fork-extracted learnings for a session.
func (s *Store) GetForkLearnings(sessionID string) ([]ForkLearning, error) {
	rows, err := s.db.Query(`SELECT id, content, category, source FROM learnings WHERE session_id = ? AND source = 'fork' AND superseded_by IS NULL ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ForkLearning
	for rows.Next() {
		var fl ForkLearning
		if err := rows.Scan(&fl.ID, &fl.Content, &fl.Category, &fl.Source); err != nil {
			return nil, err
		}
		result = append(result, fl)
	}
	return result, nil
}

// InsertForkCoverage records that a fork has processed a message range.
func (s *Store) InsertForkCoverage(sessionID string, fromIdx, toIdx, forkIndex int) error {
	_, err := s.db.Exec(`INSERT INTO fork_coverage (session_id, from_msg_idx, to_msg_idx, fork_index) VALUES (?, ?, ?, ?)`, sessionID, fromIdx, toIdx, forkIndex)
	return err
}

// IsCoveredByFork checks if a message range is fully covered by a previous fork.
func (s *Store) IsCoveredByFork(sessionID string, fromIdx, toIdx int) bool {
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM fork_coverage WHERE session_id = ? AND from_msg_idx <= ? AND to_msg_idx >= ?`, sessionID, fromIdx, toIdx).Scan(&count)
	return count > 0
}
