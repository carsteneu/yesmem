package wikirender

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ── loaders ──────────────────────────────────────────────────────────

func (s *renderState) loadLearnings(ctx context.Context) error {
	rows, err := s.cfg.Store.DB().QueryContext(ctx, `
		SELECT id, content, COALESCE(category,''), COALESCE(source,''), created_at,
		       COALESCE(use_count,0), COALESCE(quarantined_at,''),
		       COALESCE(importance,0.0), COALESCE(stability,0.0), COALESCE(confidence,0.0),
		       COALESCE(trigger_rule,''), COALESCE(context,''),
		       COALESCE(domain,''), COALESCE(task_type,''),
		       COALESCE(model_used,''), COALESCE(origin_tool,''), COALESCE(agent_role,''),
		       COALESCE(session_id,''), COALESCE(source_msg_from,0), COALESCE(source_msg_to,0),
		       COALESCE(dialog_id,''),
		       COALESCE(supersedes,0), COALESCE(supersede_reason,''),
		       COALESCE(hit_count,0), COALESCE(inject_count,0), COALESCE(save_count,0),
		       COALESCE(last_hit_at,''),
		       COALESCE(embedding_status,''), COALESCE(turns_at_creation,0)
		FROM learnings
		WHERE project = ? AND superseded_by IS NULL
		ORDER BY id
	`, s.cfg.Project)
	if err != nil {
		return fmt.Errorf("loadLearnings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var l Learning
		if err := rows.Scan(
			&l.ID, &l.Content, &l.Category, &l.Source, &l.CreatedAt,
			&l.UseCount, &l.QuarantinedAt,
			&l.Importance, &l.Stability, &l.Confidence,
			&l.TriggerRule, &l.Context,
			&l.Domain, &l.TaskType,
			&l.ModelUsed, &l.OriginTool, &l.AgentRole,
			&l.SessionID, &l.SourceMsgFrom, &l.SourceMsgTo,
			&l.DialogID,
			&l.Supersedes, &l.SupersedeReason,
			&l.HitCount, &l.InjectCount, &l.SaveCount,
			&l.LastHitAt,
			&l.EmbeddingStatus, &l.TurnsAtCreation,
		); err != nil {
			return err
		}
		l.Project = s.cfg.Project
		s.learnings[l.ID] = l
	}
	return rows.Err()
}

func (s *renderState) loadEntities(ctx context.Context) error {
	return s.loadAttribute(ctx, "learning_entities", func(lid int64, val string) {
		s.learningsMu.Lock()
		s.entities[val] = append(s.entities[val], lid)
		s.byLearning[lid] = append(s.byLearning[lid], val)
		l := s.learnings[lid]
		l.Entities = append(l.Entities, val)
		s.learnings[lid] = l
		s.learningsMu.Unlock()
	})
}

func (s *renderState) loadActions(ctx context.Context) error {
	return s.loadAttribute(ctx, "learning_actions", func(lid int64, val string) {
		s.learningsMu.Lock()
		l := s.learnings[lid]
		l.Actions = append(l.Actions, val)
		s.learnings[lid] = l
		s.learningsMu.Unlock()
	})
}

func (s *renderState) loadKeywords(ctx context.Context) error {
	return s.loadAttribute(ctx, "learning_keywords", func(lid int64, val string) {
		s.learningsMu.Lock()
		l := s.learnings[lid]
		l.Keywords = append(l.Keywords, val)
		s.learnings[lid] = l
		s.learningsMu.Unlock()
	})
}

func (s *renderState) loadAttribute(ctx context.Context, tbl string, sink func(int64, string)) error {
	q := fmt.Sprintf(`
		SELECT le.learning_id, le.value
		FROM %s le
		JOIN learnings l ON l.id = le.learning_id
		WHERE l.project = ? AND l.superseded_by IS NULL
	`, tbl)
	rows, err := s.cfg.Store.DB().QueryContext(ctx, q, s.cfg.Project)
	if err != nil {
		return fmt.Errorf("load %s: %w", tbl, err)
	}
	defer rows.Close()
	for rows.Next() {
		var lid int64
		var val string
		if err := rows.Scan(&lid, &val); err != nil {
			return err
		}
		sink(lid, val)
	}
	return rows.Err()
}

func (s *renderState) loadSupersedesContentForIDs(ctx context.Context, supIDs []int64) (map[int64]string, error) {
	if len(supIDs) == 0 {
		return nil, nil
	}

	args := make([]any, len(supIDs))
	placeholders := make([]string, len(supIDs))
	for i, id := range supIDs {
		args[i] = id
		placeholders[i] = "?"
	}

	q := fmt.Sprintf(`SELECT id, content FROM learnings WHERE id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.cfg.Store.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("loadSupersedesContent: %w", err)
	}
	defer rows.Close()

	supersedes := map[int64]string{}
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return nil, err
		}
		supersedes[id] = content
	}
	return supersedes, rows.Err()
}

func (s *renderState) loadFileCoverage(ctx context.Context) error {
	rows, err := s.cfg.Store.DB().QueryContext(ctx, `
		SELECT file_path, COALESCE(directory,''), COALESCE(session_count,0),
		       COALESCE(last_touched,''), COALESCE(operation_types,'')
		FROM file_coverage
		WHERE project = ?
		ORDER BY session_count DESC
	`, s.cfg.Project)
	if err != nil {
		return fmt.Errorf("loadFileCoverage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fp FilePage
		if err := rows.Scan(&fp.Path, &fp.Directory, &fp.SessionCount, &fp.LastTouched, &fp.OperationTypes); err != nil {
			return err
		}
		if !s.shouldIncludeFile(fp.Path) {
			continue
		}
		// Normalize absolute paths under the project root to relative.
		fp.Path = s.normalizePath(fp.Path)
		fp.Directory = s.normalizePath(fp.Directory)
		if fp.Directory == "" {
			fp.Directory = filepath.Dir(fp.Path)
		}
		s.files[fp.Path] = &fp
	}
	return rows.Err()
}

// mergeScanFiles adds files from the CBM scan that aren't already in
// the file index (no session data, but have code intel).
func (s *renderState) mergeScanFiles() {
	for _, f := range s.scanFiles {
		path := s.normalizePath(f.Path)
		if _, ok := s.files[path]; ok {
			continue
		}
		if !s.shouldIncludeFile(path) {
			continue
		}
		s.files[path] = &FilePage{
			Path:      path,
			Directory: filepath.Dir(path),
		}
	}
}

func (s *renderState) normalizePath(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	dir := s.projectPath
	if dir != "" && strings.HasPrefix(path, dir) {
		rel, err := filepath.Rel(dir, path)
		if err == nil {
			return rel
		}
	}
	return path
}

// shouldIncludeFile filters out paths that don't belong in the current
// project's wiki: other worktrees, dot-files/dirs, absolute paths.
func (s *renderState) shouldIncludeFile(path string) bool {
	dir := s.projectPath

	// Exclude absolute paths not under the project root.
	if filepath.IsAbs(path) && dir != "" && !strings.HasPrefix(path, dir) {
		return false
	}

	// Exclude other worktrees.
	isWorktree := strings.Contains(path, "/.claude/worktrees/") || strings.Contains(path, "/.worktrees/")
	if isWorktree {
		marker := "/worktrees/" + s.cfg.Project + "/"
		if !strings.Contains(path, marker) {
			return false
		}
	}

	// Exclude dot-files and dot-directories at any path level.
	// Skip for worktree paths — they contain .claude which is expected.
	if !isWorktree {
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, ".") && seg != ".." && seg != "." {
				return false
			}
		}
	}

	// Exclude non-code paths: vendored deps, PDFs, docs subdirectories.
	if strings.Contains(path, "/pkg/mod/") ||
		strings.Contains(path, "/vendor/") ||
		strings.HasSuffix(path, ".pdf") ||
		strings.Contains(path, "/pdfs/") ||
		strings.HasSuffix(path, "/erledigt") ||
		strings.Contains(path, "/erledigt/") ||
		strings.Contains(path, "go-path") || strings.Contains(path, "gopath") {
		return false
	}

	if s.isGitignored(path) {
		return false
	}
	return true
}

// isGitignored checks whether a path matches .gitignore patterns.
func (s *renderState) isGitignored(path string) bool {
	patterns := s.gitignorePatterns()
	for _, p := range patterns {
		if p.negate {
			continue
		}
		if matchGitignore(p.pattern, path) {
			return true
		}
	}
	return false
}

type gitignorePattern struct {
	pattern string
	negate  bool
}

// gitignorePatterns loads .gitignore from the project root and returns
// parsed patterns. Results are cached in the renderState after first load.
func (s *renderState) gitignorePatterns() []gitignorePattern {
	if s.gitignore != nil {
		return s.gitignore
	}
	s.gitignore = []gitignorePattern{}
	dir := s.projectPath
	if dir == "" {
		return s.gitignore
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return s.gitignore
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		s.gitignore = append(s.gitignore, gitignorePattern{pattern: line, negate: negate})
	}
	return s.gitignore
}

// matchGitignore checks whether path matches a gitignore-style pattern.
// Handles: *, ?, **, leading /, trailing /.
func matchGitignore(pattern, path string) bool {
	// Trailing / means only match directories.
	if strings.HasSuffix(pattern, "/") {
		pattern = pattern[:len(pattern)-1]
		if !strings.Contains(path, "/") {
			return false
		}
	}
	// Leading / anchors to root.
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
	}
	// ** matches any number of directories.
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		rest := path
		for i, part := range parts {
			if part == "" {
				continue
			}
			if i == 0 {
				if !strings.HasPrefix(rest, part) {
					idx := strings.Index(rest, "/"+part)
					if idx < 0 {
						return false
					}
					rest = rest[idx+1:]
				} else {
					rest = rest[len(part):]
				}
			} else {
				idx := strings.LastIndex(rest, part)
				if idx < 0 {
					return false
				}
				rest = rest[:idx]
			}
		}
		return true
	}
	// Simple glob: * matches anything except /.
	return matchSimpleGlob(pattern, path)
}

func matchSimpleGlob(pattern, path string) bool {
	pi, si := 0, 0
	for pi < len(pattern) && si < len(path) {
		switch pattern[pi] {
		case '*':
			if pi+1 >= len(pattern) {
				return !strings.Contains(path[si:], "/")
			}
			for si < len(path) && path[si] != '/' {
				if matchSimpleGlob(pattern[pi+1:], path[si:]) {
					return true
				}
				si++
			}
			return matchSimpleGlob(pattern[pi+1:], path[si:])
		case '?':
			if path[si] == '/' {
				return false
			}
			pi++
			si++
		default:
			if pattern[pi] != path[si] {
				return false
			}
			pi++
			si++
		}
	}
	return pi == len(pattern) && si == len(path)
}

func (s *renderState) loadContradictions(ctx context.Context) error {
	rows, err := s.cfg.Store.DB().QueryContext(ctx, `
		SELECT id, COALESCE(learning_ids,'[]'), COALESCE(description,''), created_at
		FROM contradictions
		WHERE resolved = 0 AND (project = ? OR project IS NULL OR project = '')
		ORDER BY id DESC LIMIT 20
	`, s.cfg.Project)
	if err != nil {
		return fmt.Errorf("loadContradictions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Contradiction
		var ids string
		if err := rows.Scan(&c.ID, &ids, &c.Description, &c.CreatedAt); err != nil {
			return err
		}
		var arr []int64
		if err := json.Unmarshal([]byte(ids), &arr); err != nil {
			arr = nil
		}
		c.LearningIDs = arr
		s.contradictions = append(s.contradictions, c)
	}
	return rows.Err()
}

func (s *renderState) loadSessionsForIDs(ctx context.Context, sids map[string]bool) error {
	if len(sids) == 0 {
		return nil
	}
	ids := make([]any, 0, len(sids))
	placeholders := make([]string, 0, len(sids))
	for id := range sids {
		ids = append(ids, id)
		placeholders = append(placeholders, "?")
	}
	q := fmt.Sprintf(`SELECT id, COALESCE(started_at,''), COALESCE(ended_at,''), COALESCE(message_count,0)
		FROM sessions WHERE id IN (%s)
		ORDER BY started_at DESC LIMIT 200`, strings.Join(placeholders, ","))
	rows, err := s.cfg.Store.DB().QueryContext(ctx, q, ids...)
	if err != nil {
		return fmt.Errorf("loadSessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.StartedAt, &sess.EndedAt, &sess.MessageCount); err != nil {
			return err
		}
		if len(sess.ID) >= 8 {
			sess.ShortID = sess.ID[:8]
		} else {
			sess.ShortID = sess.ID
		}
		s.sessions[sess.ID] = sess
	}
	return rows.Err()
}

func (s *renderState) attachLearningsToSessions() {
	s.learningsMu.Lock()
	defer s.learningsMu.Unlock()
	for _, l := range s.learnings {
		if l.SessionID == "" {
			continue
		}
		if sess, ok := s.sessions[l.SessionID]; ok {
			sess.Learnings = append(sess.Learnings, l)
			s.sessions[l.SessionID] = sess
		}
	}
}

func (s *renderState) loadFileSessions(ctx context.Context) error {
	rows, err := s.cfg.Store.DB().QueryContext(ctx, `
		SELECT path, json_group_array(json_object('id',sid,'started',started,'msgs',msgs)) AS sessions_json
		FROM (
			SELECT DISTINCT le.value AS path, substr(s.id,1,8) AS sid, s.started_at AS started, s.message_count AS msgs
			FROM learning_entities le
			JOIN learnings l ON l.id = le.learning_id
			JOIN sessions s ON s.id = l.session_id
			WHERE l.project = ? AND l.superseded_by IS NULL
		) GROUP BY path
	`, s.cfg.Project)
	if err != nil {
		return nil // optional — session section omitted if query fails
	}
	defer rows.Close()
	for rows.Next() {
		var path, raw string
		if err := rows.Scan(&path, &raw); err != nil {
			return err
		}
		if !isPathLikeEntity(path) {
			continue
		}
		var refs []SessionRef
		if err := json.Unmarshal([]byte(raw), &refs); err != nil {
			continue
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].StartedAt > refs[j].StartedAt })
		if len(refs) > 10 {
			refs = refs[:10]
		}
		if fp, ok := s.files[path]; ok {
			fp.Sessions = refs
		} else {
			for fpPath, fp := range s.files {
				if strings.HasSuffix(path, fpPath) {
					fp.Sessions = refs
					break
				}
			}
		}
	}
	return rows.Err()
}

// ── in-memory post-processing ────────────────────────────────────────

func isPathLikeEntity(s string) bool {
	if strings.Contains(s, "/") {
		return true
	}
	for _, ext := range []string{".go", ".md", ".ts", ".js", ".py", ".sh", ".yaml", ".json", ".tsx", ".jsx", ".html", ".css", ".sql"} {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

func (s *renderState) linkLearningsToFiles() {
	for entityVal, lids := range s.entities {
		if !isPathLikeEntity(entityVal) {
			continue
		}
		if fp, ok := s.files[entityVal]; ok {
			for _, lid := range lids {
				fp.Learnings = append(fp.Learnings, s.learnings[lid])
			}
			continue
		}
		for fpPath, fp := range s.files {
			if strings.HasSuffix(entityVal, fpPath) {
				for _, lid := range lids {
					fp.Learnings = append(fp.Learnings, s.learnings[lid])
				}
				break
			}
		}
	}
}

func (s *renderState) computeCoOccurrence() {
	type pair struct{ a, b string }
	counts := make(map[pair]int, 5000)

	for _, names := range s.byLearning {
		for i := 0; i < len(names); i++ {
			for j := i + 1; j < len(names); j++ {
				a, b := names[i], names[j]
				if a > b {
					a, b = b, a
				}
				counts[pair{a, b}]++
			}
		}
	}

	for p, n := range counts {
		if n < 2 {
			continue
		}
		s.cooc[p.a] = append(s.cooc[p.a], CoTopic{Name: p.b, Shared: n})
		s.cooc[p.b] = append(s.cooc[p.b], CoTopic{Name: p.a, Shared: n})
	}

	for k := range s.cooc {
		sort.Slice(s.cooc[k], func(i, j int) bool { return s.cooc[k][i].Shared > s.cooc[k][j].Shared })
		if len(s.cooc[k]) > 5 {
			s.cooc[k] = s.cooc[k][:5]
		}
	}
}

func (s *renderState) computeRelatedLearnings() {
	for id, l := range s.learnings {
		if len(l.Entities) == 0 {
			continue
		}
		overlap := map[int64]int{}
		for _, e := range l.Entities {
			for _, otherID := range s.entities[e] {
				if otherID == id {
					continue
				}
				overlap[otherID]++
			}
		}
		type pair struct {
			id int64
			n  int
		}
		var pairs []pair
		for oid, n := range overlap {
			pairs = append(pairs, pair{oid, n})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].n > pairs[j].n })
		if len(pairs) > 5 {
			pairs = pairs[:5]
		}
		for _, p := range pairs {
			other := s.learnings[p.id]
			s.related[id] = append(s.related[id], RelatedLearning{
				ID:       p.id,
				Snippet:  snippet(other.Content, 100),
				Source:   other.Source,
				Category: other.Category,
				Overlap:  p.n,
			})
		}
	}
}
