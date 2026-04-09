package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// UpsertSession inserts or updates a session.
// If a previously-extracted short session (≤5 messages) grows past the threshold,
// extracted_at is reset so the extraction pipeline re-processes it.
func (s *Store) UpsertSession(sess *models.Session) error {
	sourceAgent := models.NormalizeSourceAgent(sess.SourceAgent)

	_, err := s.db.Exec(`
		INSERT INTO sessions (id, project, project_short, git_branch, first_message,
			message_count, started_at, ended_at, jsonl_path, jsonl_size, indexed_at,
			parent_session_id, agent_type, source_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			message_count = excluded.message_count,
			ended_at = excluded.ended_at,
			jsonl_size = excluded.jsonl_size,
			indexed_at = excluded.indexed_at,
			parent_session_id = excluded.parent_session_id,
			agent_type = excluded.agent_type,
			source_agent = excluded.source_agent,
			extracted_at = CASE
				WHEN sessions.extracted_at IS NOT NULL AND sessions.message_count <= 5 AND excluded.message_count > 5
				THEN NULL ELSE sessions.extracted_at END`,
		sess.ID, sess.Project, sess.ProjectShort, sess.GitBranch, sess.FirstMessage,
		sess.MessageCount, fmtTime(sess.StartedAt), fmtTime(sess.EndedAt),
		sess.JSONLPath, sess.JSONLSize, fmtTime(sess.IndexedAt),
		nullString(sess.ParentSessionID), nullString(sess.AgentType), sourceAgent)
	if err != nil {
		return fmt.Errorf("upsert session %s: %w", sess.ID, err)
	}
	return nil
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(id string) (*models.Session, error) {
	row := s.readerDB().QueryRow(`SELECT id, project, project_short, git_branch, first_message,
		message_count, started_at, ended_at, jsonl_path, jsonl_size, indexed_at,
		COALESCE(parent_session_id, ''), COALESCE(agent_type, ''), COALESCE(source_agent, 'claude'),
		COALESCE(extracted_at, ''), COALESCE(narrative_at, '')
		FROM sessions WHERE id = ?`, id)

	sess := &models.Session{}
	var startedAt, endedAt, indexedAt, extractedAt, narrativeAt string
	err := row.Scan(&sess.ID, &sess.Project, &sess.ProjectShort, &sess.GitBranch,
		&sess.FirstMessage, &sess.MessageCount, &startedAt, &endedAt,
		&sess.JSONLPath, &sess.JSONLSize, &indexedAt,
		&sess.ParentSessionID, &sess.AgentType, &sess.SourceAgent, &extractedAt, &narrativeAt)
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", id, err)
	}
	sess.StartedAt = parseTime(startedAt)
	sess.EndedAt = parseTime(endedAt)
	sess.IndexedAt = parseTime(indexedAt)
	sess.ExtractedAt = parseTime(extractedAt)
	sess.NarrativeAt = parseTime(narrativeAt)
	return sess, nil
}

// ListSessions returns sessions ordered by started_at DESC.
// Subagent sessions are excluded — use ListAllSessions to include them.
func (s *Store) ListSessions(project string, limit int) ([]models.Session, error) {
	return s.listSessions(project, limit, false)
}

// ResolveProjectPath returns the filesystem path for a project_short name.
// E.g. "yesmem" → "/home/user/projects/yesmem"
func (s *Store) ResolveProjectPath(projectShort string) string {
	if projectShort == "" {
		return ""
	}
	var path string
	s.readerDB().QueryRow("SELECT project FROM sessions WHERE project_short = ? AND project LIKE '/%' ORDER BY started_at DESC LIMIT 1", projectShort).Scan(&path)
	return path
}

// ResolveProjectShort finds the best matching project_short for a directory path.
// Checks filepath.Base and subdirectories, returns the one with the most recent session.
func (s *Store) ResolveProjectShort(projectDir string) string {
	if projectDir == "" {
		return ""
	}

	// Fast path: if input is already a known project_short, return it directly.
	// This prevents LIKE '%name%' from matching worktree paths that contain the name.
	if len(projectDir) < 60 && projectDir[0] != '/' {
		var count int
		err := s.readerDB().QueryRow("SELECT COUNT(*) FROM sessions WHERE project_short = ?", projectDir).Scan(&count)
		if err == nil && count > 0 {
			return projectDir
		}
	}

	type candidate struct {
		name   string
		latest string
	}
	var best candidate

	check := func(name string) {
		var latest string
		err := s.readerDB().QueryRow("SELECT MAX(started_at) FROM sessions WHERE project_short = ?", name).Scan(&latest)
		if err == nil && latest != "" && latest > best.latest {
			best = candidate{name, latest}
		}
	}

	// Check base name
	check(filepath.Base(projectDir))

	// Check subdirectory names (covers renamed projects like memory/ containing yesmem/)
	// Use %name% for short names without slashes, prefix match for full paths
	likePattern := projectDir + "%"
	if projectDir[0] != '/' {
		likePattern = "%" + projectDir + "%"
	}
	rows, err := s.readerDB().Query("SELECT DISTINCT project_short FROM sessions WHERE project LIKE ?", likePattern)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ps string
			if rows.Scan(&ps) == nil {
				check(ps)
			}
		}
	}

	if best.name != "" {
		return best.name
	}
	return filepath.Base(projectDir)
}

// ListAllSessions returns all sessions including subagent sessions.
// Used by extraction pipeline to extract learnings from subagent work.
func (s *Store) ListAllSessions(project string, limit int) ([]models.Session, error) {
	return s.listSessions(project, limit, true)
}

func (s *Store) listSessions(project string, limit int, includeSubagents bool) ([]models.Session, error) {
	query := `SELECT id, project, project_short, git_branch, first_message,
		message_count, started_at, ended_at, jsonl_path, jsonl_size, indexed_at,
		COALESCE(parent_session_id, ''), COALESCE(agent_type, ''), COALESCE(source_agent, 'claude'),
		COALESCE(extracted_at, ''), COALESCE(narrative_at, '')
		FROM sessions`
	args := []any{}

	if !includeSubagents {
		query += ` WHERE parent_session_id IS NULL`
	} else {
		query += ` WHERE 1=1`
	}

	if project != "" {
		query += ` AND (project_short = ? OR project = ?)`
		args = append(args, project, project)
	}
	query += ` ORDER BY started_at DESC`
	if limit > 0 {
		query += fmt.Sprintf(` LIMIT %d`, limit)
	}

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var sess models.Session
		var startedAt, endedAt, indexedAt, extractedAt, narrativeAt string
		if err := rows.Scan(&sess.ID, &sess.Project, &sess.ProjectShort, &sess.GitBranch,
			&sess.FirstMessage, &sess.MessageCount, &startedAt, &endedAt,
			&sess.JSONLPath, &sess.JSONLSize, &indexedAt,
			&sess.ParentSessionID, &sess.AgentType, &sess.SourceAgent, &extractedAt, &narrativeAt); err != nil {
			return nil, err
		}
		sess.StartedAt = parseTime(startedAt)
		sess.EndedAt = parseTime(endedAt)
		sess.IndexedAt = parseTime(indexedAt)
		sess.ExtractedAt = parseTime(extractedAt)
		sess.NarrativeAt = parseTime(narrativeAt)
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// ListProjects returns distinct projects with session counts.
func (s *Store) ListProjects() ([]ProjectSummary, error) {
	rows, err := s.readerDB().Query(`
		SELECT project_short, project, COUNT(*) as session_count,
			MAX(started_at) as last_active
		FROM sessions
		WHERE parent_session_id IS NULL
		GROUP BY project_short, project
		ORDER BY last_active DESC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []ProjectSummary
	for rows.Next() {
		var p ProjectSummary
		if err := rows.Scan(&p.ProjectShort, &p.Project, &p.SessionCount, &p.LastActive); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ProjectSummary holds project-level aggregation data.
type ProjectSummary struct {
	ProjectShort string `json:"project_short"`
	Project      string `json:"project"`
	SessionCount int    `json:"session_count"`
	LastActive   string `json:"last_active"`
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// nullString returns nil for empty strings so SQLite stores NULL.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SubagentSummary holds aggregated subagent info for a parent session.
type SubagentSummary struct {
	ParentSessionID string `json:"parent_session_id"`
	AgentType       string `json:"agent_type"`
	Count           int    `json:"count"`
	TotalMessages   int    `json:"total_messages"`
}

// GetSubagentSummary returns aggregated subagent stats for a parent session.
func (s *Store) GetSubagentSummary(parentID string) ([]SubagentSummary, error) {
	rows, err := s.readerDB().Query(`
		SELECT parent_session_id, COALESCE(agent_type, 'unknown'), COUNT(*), COALESCE(SUM(message_count), 0)
		FROM sessions
		WHERE parent_session_id = ?
		GROUP BY agent_type`, parentID)
	if err != nil {
		return nil, fmt.Errorf("get subagent summary for %s: %w", parentID, err)
	}
	defer rows.Close()

	var summaries []SubagentSummary
	for rows.Next() {
		var sum SubagentSummary
		if err := rows.Scan(&sum.ParentSessionID, &sum.AgentType, &sum.Count, &sum.TotalMessages); err != nil {
			return nil, err
		}
		summaries = append(summaries, sum)
	}
	return summaries, rows.Err()
}

// GetSubagentCounts returns a map of parent_session_id → subagent count.
// Efficient bulk query for briefing rendering.
func (s *Store) GetSubagentCounts(parentIDs []string) (map[string]int, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	// Build placeholders
	placeholders := make([]string, len(parentIDs))
	args := make([]any, len(parentIDs))
	for i, id := range parentIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT parent_session_id, COUNT(*)
		FROM sessions
		WHERE parent_session_id IN (` + strings.Join(placeholders, ",") + `)
		GROUP BY parent_session_id`

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get subagent counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var pid string
		var count int
		if err := rows.Scan(&pid, &count); err != nil {
			return nil, err
		}
		counts[pid] = count
	}
	return counts, rows.Err()
}

// SessionMeta holds lightweight session metadata for search result annotation.
type SessionMeta struct {
	ParentSessionID string
	AgentType       string
	Project         string
	SourceAgent     string
}

// GetSessionMetaBulk returns agent_type, parent_session_id, project and source_agent for a list of session IDs.
func (s *Store) GetSessionMetaBulk(ids []string) (map[string]SessionMeta, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `SELECT id, COALESCE(parent_session_id, ''), COALESCE(agent_type, ''),
		COALESCE(project, ''), COALESCE(source_agent, 'claude')
		FROM sessions
		WHERE id IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := s.readerDB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get session meta bulk: %w", err)
	}
	defer rows.Close()

	meta := make(map[string]SessionMeta)
	for rows.Next() {
		var id, parent, agentType, project, sourceAgent string
		if err := rows.Scan(&id, &parent, &agentType, &project, &sourceAgent); err != nil {
			return nil, err
		}
		meta[id] = SessionMeta{
			ParentSessionID: parent,
			AgentType:       agentType,
			Project:         project,
			SourceAgent:     sourceAgent,
		}
	}
	return meta, rows.Err()
}

// MarkSessionExtracted sets extracted_at to now, marking the session as already processed.
func (s *Store) MarkSessionExtracted(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET extracted_at = ? WHERE id = ?`,
		time.Now().Format(time.RFC3339), sessionID)
	return err
}

// ResetSessionExtracted clears extracted_at so the session will be re-extracted on next run.
func (s *Store) ResetSessionExtracted(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET extracted_at = NULL WHERE id = ?`, sessionID)
	return err
}

// MarkShortSessionsExtracted marks sessions with ≤maxMessages as extracted,
// so they don't show as pending. Returns the number of sessions marked.
func (s *Store) MarkShortSessionsExtracted(maxMessages int) (int64, error) {
	res, err := s.db.Exec(`UPDATE sessions SET extracted_at = ? WHERE extracted_at IS NULL AND message_count <= ?`,
		time.Now().Format(time.RFC3339), maxMessages)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountPendingExtractions returns the number of sessions that need extraction
// (not yet extracted, more than minMessages messages).
func (s *Store) CountPendingExtractions(minMessages int) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE extracted_at IS NULL AND message_count > ?`, minMessages).Scan(&count)
	return count, err
}

// MarkSessionNarrative sets narrative_at to now, marking the session as having a narrative.
func (s *Store) MarkSessionNarrative(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET narrative_at = ? WHERE id = ?`,
		time.Now().Format(time.RFC3339), sessionID)
	return err
}
