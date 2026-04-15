package indexer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/archive"
	"github.com/carsteneu/yesmem/internal/bloom"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/parser"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Indexer orchestrates the full indexing pipeline.
type Indexer struct {
	store    *storage.Store
	bloom    *bloom.Manager
	archiver *archive.Archiver
}

// New creates an indexer with all dependencies.
func New(store *storage.Store, bloomMgr *bloom.Manager, arch *archive.Archiver) *Indexer {
	return &Indexer{
		store:    store,
		bloom:    bloomMgr,
		archiver: arch,
	}
}

// IndexSession runs the full pipeline for a single session JSONL file.
func (idx *Indexer) IndexSession(jsonlPath string) error {
	// 1. Check if reindex needed
	info, err := os.Stat(jsonlPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", jsonlPath, err)
	}
	if !idx.store.NeedsReindex(jsonlPath, info.Size(), info.ModTime()) {
		return nil // already up to date
	}

	// 2. Parse JSONL
	messages, meta, err := parser.ParseAuto(jsonlPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", jsonlPath, err)
	}
	if meta.SessionID == "" {
		return nil // empty or unparseable session
	}
	meta.SourceAgent = models.NormalizeSourceAgent(meta.SourceAgent)
	meta.SessionID = models.NormalizeSessionID(meta.SourceAgent, meta.SessionID)
	for i := range messages {
		messages[i].SessionID = models.NormalizeSessionID(meta.SourceAgent, messages[i].SessionID)
		messages[i].SourceAgent = meta.SourceAgent
	}

	// 3. Build session record
	projectShort := models.ProjectShortFromPath(meta.Project)
	sess := &models.Session{
		ID:           meta.SessionID,
		Project:      meta.Project,
		ProjectShort: projectShort,
		GitBranch:    meta.GitBranch,
		FirstMessage: meta.FirstUserMessage,
		MessageCount: len(messages),
		StartedAt:    meta.StartedAt,
		EndedAt:      meta.EndedAt,
		JSONLPath:    jsonlPath,
		JSONLSize:    info.Size(),
		IndexedAt:    time.Now(),
		SourceAgent:  meta.SourceAgent,
	}

	// For subagent sessions: use agentId as session ID, sessionId as parent
	if meta.AgentID != "" {
		sess.ID = models.NormalizeSessionID(meta.SourceAgent, meta.AgentID)
		sess.ParentSessionID = meta.SessionID
		sess.AgentType = guessAgentType(meta.FirstUserMessage)
	}

	// 4. Store session + messages in SQLite
	if err := idx.store.UpsertSession(sess); err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	idx.store.DeleteMessagesBySession(sess.ID) // clean re-index
	if len(messages) > 0 {
		// Fix session ID on messages for subagents
		if meta.AgentID != "" {
			for i := range messages {
				messages[i].SessionID = sess.ID
				messages[i].SourceAgent = meta.SourceAgent
			}
		}
		if err := idx.store.InsertMessages(messages); err != nil {
			return fmt.Errorf("store messages: %w", err)
		}
	}

	// 4b. Store pulse learnings from away_summary events
	for _, msg := range messages {
		if msg.MessageType != "pulse" {
			continue
		}
		exists, err := idx.store.HasPulseForSession(sess.ID)
		if err != nil {
			log.Printf("warn: pulse dedup check for %s: %v", sess.ID, err)
			continue
		}
		if exists {
			continue
		}
		_, err = idx.store.InsertLearning(&models.Learning{
			SessionID:  sess.ID,
			Category:   "pulse",
			Content:    msg.Content,
			Project:    projectShort,
			Confidence: 1.0,
			Source:     "system_captured",
			ModelUsed:  "cc_recap",
			CreatedAt:  msg.Timestamp,
		})
		if err != nil {
			log.Printf("warn: pulse learning for %s: %v", sess.ID, err)
		}
	}

	// 5. FTS5 indexing happens automatically via InsertMessages() — no separate step needed

	// 6. Update bloom filter (use sess.ID, not meta.SessionID — for subagents these differ)
	terms := extractTerms(messages)
	idx.bloom.AddSession(sess.ID, terms)

	// 7. Build associations
	assocs := buildAssociations(sess.ID, projectShort, messages)
	if len(assocs) > 0 {
		if err := idx.store.InsertAssociationBatch(assocs); err != nil {
			log.Printf("warn: associations for %s: %v", sess.ID, err)
		}
	}

	// 8. Update file coverage
	idx.updateCoverage(projectShort, messages)

	// 9. Archive JSONL
	if idx.archiver != nil {
		project := projectDirName(jsonlPath)
		if err := idx.archiver.ArchiveFile(jsonlPath, project); err != nil {
			log.Printf("warn: archive %s: %v", jsonlPath, err)
		}
	}

	// 10. Update index state
	if err := idx.store.UpsertIndexState(&models.IndexState{
		JSONLPath: jsonlPath,
		FileSize:  info.Size(),
		FileMtime: info.ModTime(),
		IndexedAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("update index state: %w", err)
	}

	return nil
}

// ProgressFunc is called during IndexAll to report progress.
// total=-1 means total is not yet known.
type ProgressFunc func(indexed, skipped, total int)

// IndexAll scans a Claude Code projects directory and indexes all sessions.
func (idx *Indexer) IndexAll(projectsDir string) (indexed, skipped int, err error) {
	return idx.IndexAllWithProgress(projectsDir, nil)
}

// IndexAllWithProgress scans and indexes all sessions, calling progressFn after each file.
func (idx *Indexer) IndexAllWithProgress(projectsDir string, progressFn ProgressFunc) (indexed, skipped int, err error) {
	jsonlFiles, err := idx.collectSessionFiles(projectsDir)
	if err != nil {
		return 0, 0, err
	}

	total := len(jsonlFiles)
	for _, jsonlPath := range jsonlFiles {
		did, indexErr := idx.tryIndexFile(jsonlPath)
		if indexErr != nil {
			log.Printf("warn: index %s: %v", jsonlPath, indexErr)
		}
		if did {
			indexed++
		} else {
			skipped++
		}
		if progressFn != nil {
			progressFn(total, indexed+skipped, skipped)
		}
	}

	return indexed, skipped, nil
}

func (idx *Indexer) collectSessionFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat sessions dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sessions path is not a directory: %s", root)
	}

	var jsonlFiles []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "archive" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		jsonlFiles = append(jsonlFiles, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan sessions dir: %w", err)
	}
	sort.Strings(jsonlFiles)
	return jsonlFiles, nil
}

func extractTerms(msgs []models.Message) []string {
	var terms []string
	for _, m := range msgs {
		if m.Content != "" {
			terms = append(terms, m.Content)
		}
		if m.ToolName != "" {
			terms = append(terms, m.ToolName)
		}
		if m.FilePath != "" {
			terms = append(terms, m.FilePath)
		}
	}
	return terms
}

func buildAssociations(sessionID, project string, msgs []models.Message) []models.Association {
	var assocs []models.Association

	// session → project
	if project != "" {
		assocs = append(assocs, models.Association{
			SourceType: "session", SourceID: sessionID,
			TargetType: "project", TargetID: project, Weight: 1,
		})
	}

	seen := map[string]bool{}
	for _, m := range msgs {
		// session → file
		if m.FilePath != "" {
			key := "file:" + m.FilePath
			if !seen[key] {
				assocs = append(assocs, models.Association{
					SourceType: "session", SourceID: sessionID,
					TargetType: "file", TargetID: m.FilePath, Weight: 1,
				})
				seen[key] = true
			}
		}
		// session → command (normalized: first word of Bash commands)
		if m.ToolName == "Bash" && m.Content != "" {
			cmd := normalizeCommand(m.Content)
			key := "command:" + cmd
			if !seen[key] && cmd != "" {
				assocs = append(assocs, models.Association{
					SourceType: "session", SourceID: sessionID,
					TargetType: "command", TargetID: cmd, Weight: 1,
				})
				seen[key] = true
			}
		}
	}
	return assocs
}

func normalizeCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	// Return first word (e.g. "docker" from "docker-compose up -d")
	return strings.ToLower(parts[0])
}

func (idx *Indexer) updateCoverage(project string, msgs []models.Message) {
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.FilePath == "" || seen[m.FilePath] {
			continue
		}
		seen[m.FilePath] = true

		op := "read"
		if m.ToolName == "Edit" || m.ToolName == "Write" {
			op = "write"
		} else if m.ToolName == "Grep" {
			op = "grep"
		} else if m.ToolName == "Bash" {
			op = "bash"
		}

		idx.store.UpsertFileCoverage(&models.FileCoverage{
			Project:        project,
			FilePath:       m.FilePath,
			Directory:      filepath.Dir(m.FilePath),
			SessionCount:   1,
			LastTouched:    time.Now().Format(time.RFC3339),
			OperationTypes: op,
		})
	}
}

func projectDirName(jsonlPath string) string {
	if strings.Contains(filepath.Clean(jsonlPath), string(os.PathSeparator)+".codex"+string(os.PathSeparator)+"sessions"+string(os.PathSeparator)) {
		return "codex"
	}
	// Regular:  .../projects/-var-www-html-ccm19/session.jsonl → -var-www-html-ccm19
	// Subagent: .../projects/-var-www-html-ccm19/<session>/subagents/agent.jsonl → -var-www-html-ccm19
	dir := filepath.Dir(jsonlPath)
	if filepath.Base(dir) == "subagents" {
		// Walk up past subagents/ and session-id/
		return filepath.Base(filepath.Dir(filepath.Dir(dir)))
	}
	return filepath.Base(dir)
}

// tryIndexFile checks whether a JSONL file needs reindexing and, if so,
// runs the full indexing pipeline. It returns (true, nil) when the file was
// indexed, (false, nil) when it was skipped (already current or unreadable),
// and (false, err) when IndexSession failed.
func (idx *Indexer) tryIndexFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, nil // unreadable — caller counts as skipped
	}
	if !idx.store.NeedsReindex(path, info.Size(), info.ModTime()) {
		return false, nil // already up to date
	}
	if err := idx.IndexSession(path); err != nil {
		return false, err
	}
	return true, nil
}

// guessAgentType infers the agent type from the first user message.
// Uses kebab-case consistently, matching Claude Code's subagent_type values.
func guessAgentType(firstMessage string) string {
	lower := strings.ToLower(firstMessage)
	switch {
	case strings.Contains(lower, "explore") || strings.Contains(lower, "find files") || strings.Contains(lower, "search code"):
		return "explore"
	case strings.Contains(lower, "write a plan") || strings.Contains(lower, "design a plan") || strings.Contains(lower, "implementation plan"):
		return "plan"
	case strings.Contains(lower, "review") || strings.Contains(lower, "code-review"):
		return "code-reviewer"
	default:
		return "general-purpose"
	}
}
