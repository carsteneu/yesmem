package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// docChunkVecCache caches doc chunk embeddings in memory for fast cosine search.
// Invalidated on SetDocChunkEmbedding calls.
type docChunkVecCache struct {
	mu      sync.RWMutex
	entries []cachedChunkVec
	valid   bool
}

type cachedChunkVec struct {
	id          int64
	sourceID    int64
	sourceName  string
	version     string
	sourceFile  string
	sourceHash  string
	headingPath string
	content     string
	contentHash string
	tokensApprox int
	metadata    map[string]string
	vec         []float32
}

var chunkVecCache = &docChunkVecCache{}

// DocSourceProjectMap returns a map of source name → project for all doc sources.
func (s *Store) DocSourceProjectMap() map[string]string {
	rows, err := s.readerDB().Query(`SELECT name, project FROM doc_sources`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var name, project string
		if rows.Scan(&name, &project) == nil {
			m[name] = project
		}
	}
	return m
}

type DocSource struct {
	ID           int64
	Name         string
	Version      string
	Path         string
	URL          string
	Project      string
	ChunkCount   int
	LastSync     time.Time
	CreatedAt    time.Time
	IsSkill      bool
	OriginalPath string
	FullContent  string // Skill full text (only for is_skill=true sources)
	TriggerExtensions string // comma-separated, e.g. ".go,.mod"
	DocType      string // "reference" (auto-inject) or "style" (explicit search only)
	ExampleQuery  string // example docs_search query for plan checkpoint hint
}

// SkillInfo holds minimal skill metadata for listing.
type SkillInfo struct {
	Name        string
	Description string
}

// DocChunk represents a structured section of documentation.
type DocChunk struct {
	ID           int64
	SourceID     int64
	SourceFile   string
	SourceHash   string
	HeadingPath  string
	SectionLevel int
	Content      string
	ContentHash  string
	TokensApprox int
	Metadata     map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// DocChunkResult is a search result with score.
type DocChunkResult struct {
	DocChunk
	Score      float64
	SourceName string
	Version    string
}

// UpsertDocSource inserts or replaces a doc source keyed on (name, project).
func (s *Store) UpsertDocSource(ds *DocSource) (int64, error) {
	// Check if exists first to preserve ID on update
	var existingID int64
	err := s.readerDB().QueryRow(`SELECT id FROM doc_sources WHERE name = ? AND project = ?`, ds.Name, ds.Project).Scan(&existingID)
	if err == nil {
		// Update existing:
		// - TriggerExtensions="" (absent param) → preserve existing value
		// - TriggerExtensions="-" (sentinel from empty array) → clear to ""
		// - TriggerExtensions=".go,.mod" (values) → overwrite
		// - DocType="" (absent param) → preserve existing value
		// - DocType="reference"/"style" → overwrite
		triggerVal := ds.TriggerExtensions
		if triggerVal == "-" {
			triggerVal = "" // sentinel → clear
		}
		_, err = s.db.Exec(`UPDATE doc_sources SET version = ?, path = ?, url = ?, chunk_count = ?, is_skill = ?, original_path = ?, full_content = ?, trigger_extensions = CASE WHEN ? = '' AND ? != '-' THEN trigger_extensions ELSE ? END, doc_type = CASE WHEN ? = '' THEN doc_type ELSE ? END, last_sync = CURRENT_TIMESTAMP WHERE id = ?`,
			ds.Version, ds.Path, ds.URL, ds.ChunkCount, ds.IsSkill, ds.OriginalPath, ds.FullContent, ds.TriggerExtensions, ds.TriggerExtensions, triggerVal, ds.DocType, ds.DocType, existingID)
		if err != nil {
			return 0, fmt.Errorf("update doc_source: %w", err)
		}
		return existingID, nil
	}
	// Insert new
	docType := ds.DocType
	if docType == "" {
		docType = "reference"
	}
	result, err := s.db.Exec(`INSERT INTO doc_sources (name, version, path, url, project, chunk_count, is_skill, original_path, full_content, trigger_extensions, doc_type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ds.Name, ds.Version, ds.Path, ds.URL, ds.Project, ds.ChunkCount, ds.IsSkill, ds.OriginalPath, ds.FullContent, ds.TriggerExtensions, docType)
	if err != nil {
		return 0, fmt.Errorf("insert doc_source: %w", err)
	}
	return result.LastInsertId()
}

// GetDocSource retrieves a single doc source by name and project.
func (s *Store) GetDocSource(name, project string) (*DocSource, error) {
	ds := &DocSource{}
	var lastSync, createdAt string
	var path, url, originalPath sql.NullString
	var isSkill sql.NullBool
	err := s.readerDB().QueryRow(`SELECT id, name, version, path, url, project, chunk_count, last_sync, created_at, COALESCE(is_skill, 0), COALESCE(original_path, ''), COALESCE(full_content, ''), COALESCE(trigger_extensions, ''), COALESCE(doc_type, 'reference') FROM doc_sources WHERE name = ? AND project = ?`,
		name, project).Scan(&ds.ID, &ds.Name, &ds.Version, &path, &url, &ds.Project, &ds.ChunkCount, &lastSync, &createdAt, &isSkill, &originalPath, &ds.FullContent, &ds.TriggerExtensions, &ds.DocType)
	if err != nil {
		return nil, fmt.Errorf("get doc_source %q/%q: %w", name, project, err)
	}
	if path.Valid {
		ds.Path = path.String
	}
	if url.Valid {
		ds.URL = url.String
	}
	if isSkill.Valid {
		ds.IsSkill = isSkill.Bool
	}
	if originalPath.Valid {
		ds.OriginalPath = originalPath.String
	}
	ds.LastSync = parseTime(lastSync)
	ds.CreatedAt = parseTime(createdAt)
	return ds, nil
}

// SetDocSourceOriginalPath stores the original skill path for restore on remove.
func (s *Store) SetDocSourceOriginalPath(sourceID int64, path string) {
	_, _ = s.db.Exec(`UPDATE doc_sources SET original_path = ? WHERE id = ?`, path, sourceID)
}

// GetSkillContent returns the full_content for a skill source.
func (s *Store) GetSkillContent(name, project string) (string, error) {
	var content string
	err := s.readerDB().QueryRow(`SELECT COALESCE(full_content, '') FROM doc_sources WHERE name = ? AND project = ? AND is_skill = 1`, name, project).Scan(&content)
	if err != nil {
		return "", fmt.Errorf("get skill content %q/%q: %w", name, project, err)
	}
	return content, nil
}

// GetRulesContent returns the condensed rules block for a project (for proxy re-injection).
func (s *Store) GetRulesContent(project string) string {
	var content string
	s.readerDB().QueryRow(`SELECT COALESCE(full_content, '') FROM doc_sources WHERE is_rules = 1 AND project = ? LIMIT 1`, project).Scan(&content)
	return content
}

// GetRulesHash returns the stored content hash for change detection.
func (s *Store) GetRulesHash(project string) string {
	var hash string
	s.readerDB().QueryRow(`SELECT COALESCE(version, '') FROM doc_sources WHERE is_rules = 1 AND project = ? LIMIT 1`, project).Scan(&hash)
	return hash
}

// ListRulesProjects returns all projects with rules (is_rules=1), with their source path.
func (s *Store) ListRulesProjects() ([]struct{ Project, Path string }, error) {
	rows, err := s.readerDB().Query(`SELECT project, path FROM doc_sources WHERE is_rules = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []struct{ Project, Path string }
	for rows.Next() {
		var r struct{ Project, Path string }
		if rows.Scan(&r.Project, &r.Path) == nil {
			result = append(result, r)
		}
	}
	return result, nil
}

// SaveRulesContent updates the condensed rules content and hash for an existing doc_source.
func (s *Store) SaveRulesContent(sourceID int64, condensed, hash string) error {
	_, err := s.db.Exec(`UPDATE doc_sources SET is_rules = 1, full_content = ?, version = ? WHERE id = ?`, condensed, hash, sourceID)
	return err
}

// ListSkillNames returns minimal info for all skills in a project.
func (s *Store) ListSkillNames(project string) ([]SkillInfo, error) {
	rows, err := s.readerDB().Query(`SELECT name, COALESCE(full_content, '') FROM doc_sources WHERE is_skill = 1 AND project = ? ORDER BY name`, project)
	if err != nil {
		return nil, fmt.Errorf("list skill names: %w", err)
	}
	defer rows.Close()

	var skills []SkillInfo
	for rows.Next() {
		var si SkillInfo
		var content string
		if err := rows.Scan(&si.Name, &content); err != nil {
			return nil, err
		}
		// Extract description from frontmatter if present
		si.Description = ExtractFrontmatterField(content, "description")
		skills = append(skills, si)
	}
	return skills, rows.Err()
}

// ExtractFrontmatterField extracts a field value from YAML frontmatter.
func ExtractFrontmatterField(content, field string) string {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return ""
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, field+":") {
			val := strings.TrimPrefix(line, field+":")
			return strings.Trim(strings.TrimSpace(val), "\"'")
		}
	}
	return ""
}

// ListDocSources returns all doc sources, optionally filtered by project.
func (s *Store) ListDocSources(project string) ([]DocSource, error) {
	var rows *sql.Rows
	var err error
	const listCols = `SELECT id, name, version, path, url, project, chunk_count, last_sync, created_at, COALESCE(is_skill, 0), COALESCE(original_path, ''), COALESCE(full_content, ''), COALESCE(trigger_extensions, ''), COALESCE(doc_type, 'reference') FROM doc_sources`
	if project == "" {
		rows, err = s.readerDB().Query(listCols + ` ORDER BY name`)
	} else {
		rows, err = s.readerDB().Query(listCols+` WHERE project = ? ORDER BY name`, project)
	}
	if err != nil {
		return nil, fmt.Errorf("list doc_sources: %w", err)
	}
	defer rows.Close()

	var sources []DocSource
	for rows.Next() {
		var ds DocSource
		var lastSync, createdAt string
		var path, url, originalPath sql.NullString
		var isSkill sql.NullBool
		if err := rows.Scan(&ds.ID, &ds.Name, &ds.Version, &path, &url, &ds.Project, &ds.ChunkCount, &lastSync, &createdAt, &isSkill, &originalPath, &ds.FullContent, &ds.TriggerExtensions, &ds.DocType); err != nil {
			return nil, err
		}
		if path.Valid {
			ds.Path = path.String
		}
		if url.Valid {
			ds.URL = url.String
		}
		if isSkill.Valid {
			ds.IsSkill = isSkill.Bool
		}
		if originalPath.Valid {
			ds.OriginalPath = originalPath.String
		}
		ds.LastSync = parseTime(lastSync)
		ds.CreatedAt = parseTime(createdAt)
		sources = append(sources, ds)
	}
	return sources, rows.Err()
}

// GetDocSourcesByExtensions returns doc sources whose trigger_extensions
// contain any of the given file extensions.
// docType — when non-empty, filters to only sources with matching doc_type.
func (s *Store) GetDocSourcesByExtensions(exts []string, docType string) ([]DocSource, error) {
	if len(exts) == 0 {
		return nil, nil
	}

	query := `SELECT id, name, version, project, COALESCE(trigger_extensions, ''), chunk_count, COALESCE(doc_type, 'reference') FROM doc_sources WHERE trigger_extensions != ''`
	args := []any{}
	if docType != "" {
		query += ` AND COALESCE(doc_type, 'reference') = ?`
		args = append(args, docType)
	}
	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query doc_sources by extensions: %w", err)
	}
	defer rows.Close()

	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		extSet[e] = true
	}

	var results []DocSource
	for rows.Next() {
		var ds DocSource
		if err := rows.Scan(&ds.ID, &ds.Name, &ds.Version, &ds.Project, &ds.TriggerExtensions, &ds.ChunkCount, &ds.DocType); err != nil {
			continue
		}
		for _, trigger := range strings.Split(ds.TriggerExtensions, ",") {
			if extSet[strings.TrimSpace(trigger)] {
				results = append(results, ds)
				break
			}
		}
	}
	return results, rows.Err()
}

// ListTriggerExtensions returns all unique trigger_extensions values.
// Lightweight — only reads trigger_extensions column, no full content.
func (s *Store) ListTriggerExtensions(project string) ([]string, error) {
	query := `SELECT DISTINCT trigger_extensions FROM doc_sources WHERE trigger_extensions != ''`
	args := []any{}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list trigger extensions: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var exts []string
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		for _, ext := range strings.Split(raw, ",") {
			ext = strings.TrimSpace(ext)
			if ext != "" && !seen[ext] {
				seen[ext] = true
				exts = append(exts, ext)
			}
		}
	}
	return exts, rows.Err()
}

// GetDocChunksBySourceIDs returns top chunks from given source IDs,
// ordered by tokens_approx descending (prefer substantial chunks).
func (s *Store) GetDocChunksBySourceIDs(sourceIDs []int64, limit int) ([]DocChunk, error) {
	if len(sourceIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(sourceIDs))
	args := make([]any, len(sourceIDs))
	for i, id := range sourceIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	args = append(args, limit)

	// Prefer mid-size chunks (100-400 tokens) that are self-contained.
	// ORDER: chunks in sweet spot first (by proximity to 250), then others by size descending.
	query := fmt.Sprintf(`SELECT id, source_id, source_file, heading_path, content, tokens_approx FROM doc_chunks WHERE source_id IN (%s) ORDER BY CASE WHEN tokens_approx BETWEEN 100 AND 400 THEN 0 ELSE 1 END, ABS(tokens_approx - 250) ASC LIMIT ?`, strings.Join(placeholders, ","))

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get doc chunks by source IDs: %w", err)
	}
	defer rows.Close()

	var chunks []DocChunk
	for rows.Next() {
		var c DocChunk
		if err := rows.Scan(&c.ID, &c.SourceID, &c.SourceFile, &c.HeadingPath, &c.Content, &c.TokensApprox); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// DeleteDocSource deletes a source and all its chunks + FTS entries.
// DeleteDocSourceResult holds info about a deleted source for post-delete actions.
type DeleteDocSourceResult struct {
	IsSkill            bool
	OriginalPath       string
	SourcePath         string
	DeletedLearningIDs []int64
}

func (s *Store) DeleteDocSource(name, project string) (*DeleteDocSourceResult, error) {
	var sourceID int64
	var isSkill sql.NullBool
	var originalPath, sourcePath sql.NullString
	err := s.readerDB().QueryRow(`SELECT id, COALESCE(is_skill, 0), COALESCE(original_path, ''), COALESCE(path, '') FROM doc_sources WHERE name = ? AND project = ?`, name, project).Scan(&sourceID, &isSkill, &originalPath, &sourcePath)
	if err != nil {
		return nil, fmt.Errorf("find doc_source for delete: %w", err)
	}

	result := &DeleteDocSourceResult{}
	if isSkill.Valid {
		result.IsSkill = isSkill.Bool
	}
	if originalPath.Valid {
		result.OriginalPath = originalPath.String
	}
	if sourcePath.Valid {
		result.SourcePath = sourcePath.String
	}

	// Hard delete docs_extracted learnings that reference chunks from this source
	// Also collect their IDs for embedding cleanup
	var learningIDs []int64
	rows, err := s.readerDB().Query(`SELECT id FROM learnings WHERE source = 'docs_extracted' AND doc_chunk_ref IN (SELECT id FROM doc_chunks WHERE source_id = ?)`, sourceID)
	if err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				learningIDs = append(learningIDs, id)
			}
		}
		rows.Close()
	}

	// Delete learnings (hard delete, not supersede)
	if _, err := s.db.Exec(`DELETE FROM learnings WHERE source = 'docs_extracted' AND doc_chunk_ref IN (SELECT id FROM doc_chunks WHERE source_id = ?)`, sourceID); err != nil {
		log.Printf("warn: delete docs_extracted learnings for source %d: %v", sourceID, err)
	}

	// Delete FTS entries for learnings
	for _, lid := range learningIDs {
		s.db.Exec(`DELETE FROM learnings_fts WHERE rowid = ?`, lid)
	}

	// Delete FTS entries for chunks (content= table requires manual sync)
	if _, err := s.db.Exec(`DELETE FROM doc_chunks_fts WHERE rowid IN (SELECT id FROM doc_chunks WHERE source_id = ?)`, sourceID); err != nil {
		return nil, fmt.Errorf("delete doc_chunks_fts: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM doc_chunks WHERE source_id = ?`, sourceID); err != nil {
		return nil, fmt.Errorf("delete doc_chunks: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM doc_sources WHERE id = ?`, sourceID); err != nil {
		return nil, fmt.Errorf("delete doc_source: %w", err)
	}

	result.DeletedLearningIDs = learningIDs
	return result, nil
}

// InsertDocChunk inserts a chunk and syncs FTS. Metadata is stored as JSON.
func (s *Store) InsertDocChunk(chunk *DocChunk) (int64, error) {
	metaJSON := "{}"
	if len(chunk.Metadata) > 0 {
		b, err := json.Marshal(chunk.Metadata)
		if err != nil {
			return 0, fmt.Errorf("marshal metadata: %w", err)
		}
		metaJSON = string(b)
	}

	result, err := s.db.Exec(`INSERT INTO doc_chunks (source_id, source_file, source_hash, heading_path, section_level, content, content_hash, tokens_approx, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chunk.SourceID, chunk.SourceFile, chunk.SourceHash, chunk.HeadingPath, chunk.SectionLevel, chunk.Content, chunk.ContentHash, chunk.TokensApprox, metaJSON)
	if err != nil {
		return 0, fmt.Errorf("insert doc_chunk: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	// Manual FTS sync (content= table has no automatic triggers)
	if _, err := s.db.Exec(`INSERT INTO doc_chunks_fts(rowid, content, heading_path, metadata) VALUES (?, ?, ?, ?)`,
		id, chunk.Content, chunk.HeadingPath, metaJSON); err != nil {
		return 0, fmt.Errorf("insert doc_chunks_fts: %w", err)
	}

	return id, nil
}

// GetDocChunksBySource returns all chunks for a source.
func (s *Store) GetDocChunksBySource(sourceID int64) ([]DocChunk, error) {
	rows, err := s.readerDB().Query(`SELECT id, source_id, source_file, source_hash, heading_path, section_level, content, content_hash, tokens_approx, metadata, created_at, updated_at FROM doc_chunks WHERE source_id = ? ORDER BY id`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("get doc_chunks by source: %w", err)
	}
	defer rows.Close()
	return scanDocChunks(rows)
}

// GetDocChunksByFile returns all chunks for a specific file.
func (s *Store) GetDocChunksByFile(sourceFile string) ([]DocChunk, error) {
	rows, err := s.readerDB().Query(`SELECT id, source_id, source_file, source_hash, heading_path, section_level, content, content_hash, tokens_approx, metadata, created_at, updated_at FROM doc_chunks WHERE source_file = ? ORDER BY id`, sourceFile)
	if err != nil {
		return nil, fmt.Errorf("get doc_chunks by file: %w", err)
	}
	defer rows.Close()
	return scanDocChunks(rows)
}

// DeleteDocChunksBySource deletes all chunks and FTS entries for a source.
func (s *Store) DeleteDocChunksBySource(sourceID int64) error {
	// FTS first, then content table
	if _, err := s.db.Exec(`DELETE FROM doc_chunks_fts WHERE rowid IN (SELECT id FROM doc_chunks WHERE source_id = ?)`, sourceID); err != nil {
		return fmt.Errorf("delete doc_chunks_fts by source: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM doc_chunks WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete doc_chunks by source: %w", err)
	}
	return nil
}

// DeleteDocChunksByFile deletes all chunks and FTS entries for a specific file.
func (s *Store) DeleteDocChunksByFile(sourceFile string) error {
	// FTS first, then content table
	if _, err := s.db.Exec(`DELETE FROM doc_chunks_fts WHERE rowid IN (SELECT id FROM doc_chunks WHERE source_file = ?)`, sourceFile); err != nil {
		return fmt.Errorf("delete doc_chunks_fts by file: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM doc_chunks WHERE source_file = ?`, sourceFile); err != nil {
		return fmt.Errorf("delete doc_chunks by file: %w", err)
	}
	return nil
}

// DeleteDocChunksByIDs deletes specific chunks and their FTS entries.
func (s *Store) DeleteDocChunksByIDs(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := s.db.Exec(`DELETE FROM doc_chunks_fts WHERE rowid IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("delete doc_chunks_fts by ids: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM doc_chunks WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("delete doc_chunks by ids: %w", err)
	}
	return nil
}

// UpdateDocSourceStats updates chunk_count and last_sync for a source.
func (s *Store) UpdateDocSourceStats(sourceID int64) error {
	_, err := s.db.Exec(`UPDATE doc_sources SET chunk_count = (SELECT COUNT(*) FROM doc_chunks WHERE source_id = ?), last_sync = CURRENT_TIMESTAMP WHERE id = ?`, sourceID, sourceID)
	if err != nil {
		return fmt.Errorf("update doc_source stats: %w", err)
	}
	return nil
}

// UpdateDocChunksSourceHashByFile refreshes the file-level source hash on all chunks of a file.
func (s *Store) UpdateDocChunksSourceHashByFile(sourceFile, sourceHash string) error {
	_, err := s.db.Exec(`UPDATE doc_chunks SET source_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE source_file = ?`, sourceHash, sourceFile)
	if err != nil {
		return fmt.Errorf("update doc_chunks source_hash by file: %w", err)
	}
	return nil
}

// SearchDocChunksFTS performs FTS5 search with BM25 ranking and AND-matching.
// source filters by doc_sources.name, section filters by heading_path LIKE.
// sourceIDs — when non-empty, restricts results to those source IDs.
func (s *Store) SearchDocChunksFTS(query string, source string, section string, since string, before string, limit int, sourceIDs []int64) ([]DocChunkResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if query == "" {
		return nil, nil
	}

	// Tokenize and quote for FTS5
	tokens := strings.Fields(query)
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, `"`, `""`)
		quoted = append(quoted, `"`+tok+`"`)
	}
	if len(quoted) == 0 {
		return nil, nil
	}

	// Build base WHERE clause (excluding MATCH)
	baseWhere := ""
	baseArgs := []any{}
	if source != "" {
		baseWhere += ` AND ds.name = ?`
		baseArgs = append(baseArgs, source)
	}
	if section != "" {
		baseWhere += ` AND dc.heading_path LIKE ?`
		baseArgs = append(baseArgs, "%"+section+"%")
	}
	if since != "" {
		baseWhere += ` AND dc.created_at >= ?`
		baseArgs = append(baseArgs, since)
	}
	if before != "" {
		baseWhere += ` AND dc.created_at < ?`
		baseArgs = append(baseArgs, before)
	}
	if len(sourceIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(sourceIDs)), ",")
		baseWhere += ` AND dc.source_id IN (` + placeholders + `)`
		for _, id := range sourceIDs {
			baseArgs = append(baseArgs, id)
		}
	}

	runFTS := func(terms []string) ([]DocChunkResult, error) {
		ftsQuery := strings.Join(terms, " AND ")
		q := `SELECT dc.id, dc.source_id, dc.source_file, dc.source_hash, dc.heading_path, dc.section_level, dc.content, dc.content_hash, dc.tokens_approx, dc.metadata, dc.created_at, dc.updated_at, bm25(doc_chunks_fts) AS score, ds.name, ds.version
			FROM doc_chunks_fts fts
			JOIN doc_chunks dc ON dc.id = fts.rowid
			JOIN doc_sources ds ON ds.id = dc.source_id
			WHERE doc_chunks_fts MATCH ?` + baseWhere + ` ORDER BY score LIMIT ?`
		args := append([]any{ftsQuery}, baseArgs...)
		args = append(args, limit)

		rows, err := s.readerDB().Query(q, args...)
		if err != nil {
			return nil, fmt.Errorf("search doc_chunks fts: %w", err)
		}
		defer rows.Close()

		var results []DocChunkResult
		for rows.Next() {
			var r DocChunkResult
			var metaJSON, createdAt, updatedAt string
			if err := rows.Scan(&r.ID, &r.SourceID, &r.SourceFile, &r.SourceHash, &r.HeadingPath, &r.SectionLevel, &r.Content, &r.ContentHash, &r.TokensApprox, &metaJSON, &createdAt, &updatedAt, &r.Score, &r.SourceName, &r.Version); err != nil {
				return nil, err
			}
			r.CreatedAt = parseTime(createdAt)
			r.UpdatedAt = parseTime(updatedAt)
			r.Metadata = make(map[string]string)
			if metaJSON != "" && metaJSON != "{}" {
				json.Unmarshal([]byte(metaJSON), &r.Metadata)
			}
			results = append(results, r)
		}
		return results, rows.Err()
	}

	// Short queries (1-2 terms): skip existence filter, search directly
	if len(quoted) <= 2 {
		return runFTS(quoted)
	}

	// Term-existence filter: check each term against corpus, keep only terms that exist.
	// Sorts surviving terms by document frequency ascending (rarest first = most specific).
	type termHit struct {
		quoted string
		count  int
	}
	var alive []termHit
	db := s.readerDB()
	for _, q := range quoted {
		var cnt int
		err := db.QueryRow(`SELECT COUNT(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH ?`, q).Scan(&cnt)
		if err != nil || cnt == 0 {
			continue
		}
		alive = append(alive, termHit{q, cnt})
	}
	if len(alive) < 2 {
		// Fallback: if only 1 term survives, search with that single term
		if len(alive) == 1 {
			return runFTS([]string{alive[0].quoted})
		}
		return nil, nil
	}

	// Sort by frequency ascending — rarest terms first (highest IDF proxy).
	// When we drop terms for lower tiers, we lose the most common (least specific) ones.
	sort.Slice(alive, func(i, j int) bool {
		return alive[i].count < alive[j].count
	})

	// Cap at 6 terms (research: optimal 3-6 terms for BM25)
	if len(alive) > 6 {
		alive = alive[:6]
	}

	filtered := make([]string, len(alive))
	for i, a := range alive {
		filtered[i] = a.quoted
	}

	// Tier fallback on filtered terms: all → all-1 → ... → 2
	for n := len(filtered); n >= 2; n-- {
		results, err := runFTS(filtered[:n])
		if err != nil {
			return nil, err
		}
		if len(results) > 0 {
			return results, nil
		}
	}

	return nil, nil
}

// GetLearningsBySourceFile returns active learnings linked to a source file.
func (s *Store) GetLearningsBySourceFile(sourceFile string) ([]models.Learning, error) {
	rows, err := s.readerDB().Query(`SELECT id, session_id, category, content, project, confidence,
		superseded_by, supersede_reason, created_at, expires_at, model_used, source,
		COALESCE(hit_count, 0), COALESCE(emotional_intensity, 0.0), last_hit_at, COALESCE(session_flavor, ''), valid_until, supersedes, COALESCE(importance, 3), supersede_status, COALESCE(noise_count, 0), COALESCE(fail_count, 0),
		COALESCE(match_count, 0), COALESCE(inject_count, 0), COALESCE(use_count, 0), COALESCE(save_count, 0), COALESCE(stability, 30.0),
		COALESCE(context, ''), COALESCE(domain, 'code'), COALESCE(trigger_rule, ''), COALESCE(embedding_text, ''),
		COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(doc_chunk_ref, 0), COALESCE(task_type, ''), COALESCE(turns_at_creation, 0), COALESCE(source_msg_from, -1), COALESCE(source_msg_to, -1)
		FROM learnings WHERE source_file = ? AND superseded_by IS NULL`, sourceFile)
	if err != nil {
		return nil, fmt.Errorf("get learnings by source_file %q: %w", sourceFile, err)
	}
	defer rows.Close()
	return scanLearnings(rows)
}

// SupersedeBySourceFile marks all active learnings for a source file as superseded.
// Returns the IDs of affected learnings.
func (s *Store) SupersedeBySourceFile(sourceFile, reason string) ([]int64, error) {
	ids, err := s.collectIDs(`SELECT id FROM learnings WHERE source_file = ? AND superseded_by IS NULL`, sourceFile)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = -1, supersede_reason = ?, valid_until = datetime('now') WHERE source_file = ? AND superseded_by IS NULL`,
		reason, sourceFile)
	if err != nil {
		return nil, fmt.Errorf("supersede by source_file %q: %w", sourceFile, err)
	}
	return ids, nil
}

// SupersedeByDocChunkRefs marks active learnings for the given chunk refs as superseded.
func (s *Store) SupersedeByDocChunkRefs(chunkRefs []int64, reason string) ([]int64, error) {
	if len(chunkRefs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(chunkRefs)), ",")
	args := make([]any, 0, len(chunkRefs)+1)
	for _, id := range chunkRefs {
		args = append(args, id)
	}

	ids, err := s.collectIDs(`SELECT id FROM learnings WHERE doc_chunk_ref IN (`+placeholders+`) AND superseded_by IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	updateArgs := make([]any, 0, len(chunkRefs)+1)
	updateArgs = append(updateArgs, reason)
	updateArgs = append(updateArgs, args...)
	_, err = s.db.Exec(`UPDATE learnings SET superseded_by = -1, supersede_reason = ?, valid_until = datetime('now') WHERE doc_chunk_ref IN (`+placeholders+`) AND superseded_by IS NULL`, updateArgs...)
	if err != nil {
		return nil, fmt.Errorf("supersede by doc_chunk_ref: %w", err)
	}
	return ids, nil
}

// scanDocChunks scans rows into DocChunk slices.
func scanDocChunks(rows *sql.Rows) ([]DocChunk, error) {
	var chunks []DocChunk
	for rows.Next() {
		var c DocChunk
		var metaJSON, createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.SourceID, &c.SourceFile, &c.SourceHash, &c.HeadingPath, &c.SectionLevel, &c.Content, &c.ContentHash, &c.TokensApprox, &metaJSON, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(createdAt)
		c.UpdatedAt = parseTime(updatedAt)
		c.Metadata = make(map[string]string)
		if metaJSON != "" && metaJSON != "{}" {
			json.Unmarshal([]byte(metaJSON), &c.Metadata)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// escapeFTS5 escapes special characters for FTS5 MATCH queries.
// Wraps each token in double quotes to treat them as literals.
func escapeFTS5(query string) string {
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return query
	}
	escaped := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		// Replace double quotes within the token
		tok = strings.ReplaceAll(tok, `"`, `""`)
		escaped = append(escaped, `"`+tok+`"`)
	}
	return strings.Join(escaped, " ")
}

// SetDocChunkEmbedding stores an embedding vector for a doc chunk.
func (s *Store) SetDocChunkEmbedding(chunkID int64, vector []float32, contentHash string) error {
	blob := float32ToBytes(vector)
	_, err := s.db.Exec(`UPDATE doc_chunks SET embedding_vector = ?, embedding_hash = ? WHERE id = ?`, blob, contentHash, chunkID)
	if err == nil {
		chunkVecCache.mu.Lock()
		chunkVecCache.valid = false
		chunkVecCache.mu.Unlock()
	}
	return err
}

// DocChunksWithoutEmbedding returns chunks that need embedding (no vector or hash mismatch).
func (s *Store) DocChunksWithoutEmbedding() ([]DocChunk, error) {
	rows, err := s.readerDB().Query(`SELECT id, source_id, source_file, source_hash, heading_path, section_level, content, content_hash, tokens_approx, metadata, created_at, updated_at FROM doc_chunks WHERE embedding_vector IS NULL OR embedding_hash = '' OR embedding_hash != content_hash ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("doc chunks without embedding: %w", err)
	}
	defer rows.Close()
	return scanDocChunks(rows)
}

// SearchDocChunksSemantic performs vector similarity search on doc chunks.
// When source is non-empty, only chunks from that doc source are considered.
func (s *Store) SearchDocChunksSemantic(queryVec []float32, limit int, source string) ([]DocChunkResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Load cache if invalid
	entries := s.loadChunkVecCache()

	type scored struct {
		result DocChunkResult
		sim    float64
	}
	var candidates []scored

	for _, e := range entries {
		if source != "" && e.sourceName != source {
			continue
		}
		sim := cosineSimilarity32(queryVec, e.vec)
		if sim < 0.3 {
			continue
		}
		r := DocChunkResult{
			DocChunk: DocChunk{
				ID:           e.id,
				SourceID:     e.sourceID,
				SourceFile:   e.sourceFile,
				SourceHash:   e.sourceHash,
				HeadingPath:  e.headingPath,
				Content:      e.content,
				ContentHash:  e.contentHash,
				TokensApprox: e.tokensApprox,
				Metadata:     e.metadata,
			},
			Score:      sim,
			SourceName: e.sourceName,
			Version:    e.version,
		}
		candidates = append(candidates, scored{r, sim})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].sim > candidates[j].sim
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]DocChunkResult, len(candidates))
	for i, c := range candidates {
		results[i] = c.result
	}
	return results, nil
}

// loadChunkVecCache returns cached vectors, loading from DB if cache is invalid.
func (s *Store) loadChunkVecCache() []cachedChunkVec {
	chunkVecCache.mu.RLock()
	if chunkVecCache.valid {
		entries := chunkVecCache.entries
		chunkVecCache.mu.RUnlock()
		return entries
	}
	chunkVecCache.mu.RUnlock()

	// Upgrade to write lock and reload
	chunkVecCache.mu.Lock()
	defer chunkVecCache.mu.Unlock()

	// Double-check after acquiring write lock
	if chunkVecCache.valid {
		return chunkVecCache.entries
	}

	rows, err := s.readerDB().Query(`SELECT dc.id, dc.source_id, dc.source_file, dc.source_hash, dc.heading_path, dc.content, dc.content_hash, dc.tokens_approx, dc.metadata, dc.embedding_vector, ds.name, ds.version
		FROM doc_chunks dc
		JOIN doc_sources ds ON ds.id = dc.source_id
		WHERE dc.embedding_vector IS NOT NULL`)
	if err != nil {
		log.Printf("loadChunkVecCache: %v", err)
		return nil
	}
	defer rows.Close()

	var entries []cachedChunkVec
	for rows.Next() {
		var e cachedChunkVec
		var metaJSON string
		var vecBlob []byte
		if err := rows.Scan(&e.id, &e.sourceID, &e.sourceFile, &e.sourceHash, &e.headingPath, &e.content, &e.contentHash, &e.tokensApprox, &metaJSON, &vecBlob, &e.sourceName, &e.version); err != nil {
			log.Printf("loadChunkVecCache scan: %v", err)
			continue
		}
		if len(vecBlob) == 0 {
			continue
		}
		e.vec = bytesToFloat32(vecBlob)
		e.metadata = make(map[string]string)
		if metaJSON != "" && metaJSON != "{}" {
			json.Unmarshal([]byte(metaJSON), &e.metadata)
		}
		entries = append(entries, e)
	}

	chunkVecCache.entries = entries
	chunkVecCache.valid = true
	log.Printf("loadChunkVecCache: loaded %d vectors", len(entries))
	return entries
}

// GetReferenceSources returns all doc sources with doc_type='reference'.
// Used by plan checkpoint to build docs-available reminder.
func (s *Store) GetReferenceSources() ([]DocSource, error) {
	rows, err := s.readerDB().Query(`SELECT name, version, COALESCE(example_query, '') FROM doc_sources WHERE doc_type = 'reference' AND is_skill = 0 AND is_rules = 0 GROUP BY name ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("get reference sources: %w", err)
	}
	defer rows.Close()

	var sources []DocSource
	for rows.Next() {
		var ds DocSource
		if err := rows.Scan(&ds.Name, &ds.Version, &ds.ExampleQuery); err != nil {
			return nil, fmt.Errorf("scan reference source: %w", err)
		}
		sources = append(sources, ds)
	}
	return sources, rows.Err()
}
