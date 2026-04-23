package storage

import (
	"fmt"
	"os"

	"github.com/carsteneu/yesmem/internal/textutil"
)

// createSchema creates all tables if they don't exist.
func (s *Store) createSchema() error {
	// Migrations FIRST — fix schema before CREATE TABLE IF NOT EXISTS skips
	for _, mig := range migrations {
		s.db.Exec(mig) // Ignore errors (column may already exist)
	}

	tables := []string{
		tableSessions,
		tableMessages,
		tableLearnings,
		tableAssociations,
		tableIndexState,
		tableProjectProfiles,
		tableStrategicContext,
		tableFileCoverage,
		tableSelfFeedback,
		tablePersonaTraits,
		tablePersonaDirectives,
		tableSessionTracking,
		tableClaudeMdState,
		tableRefinedBriefings,
		tableCompactedBlocks,
		tableProxyState,
		tableLearningClusters,
		tableKnowledgeGaps,
		tableContradictions,
		tableDailySpend,
		tableLearningEntities,
		tableLearningActions,
		tableLearningKeywords,
		tableLearningAnticipatedQueries,
		tableQueryLog,
		tableQueryClusters,
		tableLearningClusterScores,
		tableEmbeddingCache,
		tablePinnedLearnings,
		tableAgentDialogs,
		tableAgentMessages,
		tableAgentBroadcasts,
		tablePlans,
		tableTurnCounters,
		tableScratchpadEntries,
		tableAgents,
		tableTokenUsage,
		tableCodeDescriptions,
		tableProjectScan,
	}
	for _, ddl := range tables {
		if _, err := s.db.Exec(ddl); err != nil {
			return err
		}
	}

	for _, idx := range indices {
		if _, err := s.db.Exec(idx); err != nil {
			return err
		}
	}

	// FTS5 for learnings — table only, NO triggers (background sync instead)
	if _, err := s.db.Exec(tableLearningsFTS); err != nil {
		return err
	}

	// Drop FTS5 triggers — replaced by background sync to avoid write contention
	// that blocked BM25 reads for 5-60s during extraction/evolution.
	for _, trigger := range []string{"learnings_fts_insert", "learnings_fts_delete", "learnings_fts_update"} {
		s.db.Exec("DROP TRIGGER IF EXISTS " + trigger)
	}

	// Backfill FTS5 index for existing data (idempotent)
	var ftsCount int
	s.db.QueryRow(`SELECT COUNT(*) FROM learnings_fts`).Scan(&ftsCount)
	if ftsCount == 0 {
		s.db.Exec(`INSERT INTO learnings_fts(rowid, content) SELECT id, content || ' ' || COALESCE(trigger_rule, '') FROM learnings`)
	}

	// Backfill content hashes for existing learnings (v0.22)
	s.backfillContentHashes()

	return nil
}

func (s *Store) createRuntimeSchema() error {
	db := s.proxyStateDB()
	if db == nil {
		return nil
	}
	if _, err := db.Exec(tableProxyState); err != nil {
		return err
	}
	if _, err := db.Exec(tablePinnedLearnings); err != nil {
		return err
	}
	return nil
}

// createMessagesSchema creates the messages table + FTS5 index in the separate messages.db.
func (s *Store) createMessagesSchema() error {
	db := s.messagesWriteDB()

	if _, err := db.Exec(tableMessages); err != nil {
		return fmt.Errorf("create messages table: %w", err)
	}

	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_type ON messages(message_type)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_file ON messages(file_path) WHERE file_path IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session_type ON messages(session_id, message_type, sequence)`,
	} {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("create messages index: %w", err)
		}
	}

	// FTS5 for full-text search over messages
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts
		USING fts5(content, tokenize='unicode61', content_rowid=id)`); err != nil {
		return fmt.Errorf("create messages_fts: %w", err)
	}

	// Migrations for messages.db (run idempotently, ignore "duplicate column" errors)
	for _, mig := range messagesMigrations {
		db.Exec(mig)
	}

	return nil
}

func (s *Store) migrateProxyStateToRuntime() error {
	if s.runtimeDB == nil || s.runtimeDB == s.db {
		return nil
	}

	var runtimeCount int
	if err := s.runtimeDB.QueryRow(`SELECT COUNT(*) FROM proxy_state`).Scan(&runtimeCount); err != nil {
		return err
	}
	if runtimeCount > 0 {
		return nil
	}

	rows, err := s.db.Query(`SELECT key, value FROM proxy_state`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	tx, err := s.runtimeDB.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO proxy_state (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := stmt.Exec(key, value); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) backfillContentHashes() {
	n, err := s.BackfillContentHashes(textutil.ContentHash)
	if err != nil {
		return
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "Backfilled content_hash for %d learnings\n", n)
	}
}

// FTS5 virtual table for learnings full-text search.
const tableLearningsFTS = `CREATE VIRTUAL TABLE IF NOT EXISTS learnings_fts
	USING fts5(content, content_rowid=id, tokenize='porter unicode61')`

// Triggers to keep FTS5 in sync with learnings table.
const triggerLearningsFTSInsert = `CREATE TRIGGER IF NOT EXISTS learnings_fts_insert
	AFTER INSERT ON learnings BEGIN
		INSERT INTO learnings_fts(rowid, content) VALUES (new.id, new.content);
	END`

const triggerLearningsFTSDelete = `CREATE TRIGGER IF NOT EXISTS learnings_fts_delete
	AFTER DELETE ON learnings BEGIN
		INSERT INTO learnings_fts(learnings_fts, rowid, content) VALUES ('delete', old.id, old.content);
	END`

const triggerLearningsFTSUpdate = `CREATE TRIGGER IF NOT EXISTS learnings_fts_update
	AFTER UPDATE OF content ON learnings BEGIN
		INSERT INTO learnings_fts(learnings_fts, rowid, content) VALUES ('delete', old.id, old.content);
		INSERT INTO learnings_fts(rowid, content) VALUES (new.id, new.content);
	END`

var migrations = []string{
	// v0.7: Add source column to learnings (user_stated, claude_suggested, agreed_upon, llm_extracted)
	`ALTER TABLE learnings ADD COLUMN source TEXT DEFAULT 'llm_extracted'`,
	// v0.8: Add hit_count for relevance scoring
	`ALTER TABLE learnings ADD COLUMN hit_count INTEGER DEFAULT 0`,
	// v0.9: Add emotional_intensity for decay scoring
	`ALTER TABLE learnings ADD COLUMN emotional_intensity REAL DEFAULT 0.0`,
	// v0.10: Add last_hit_at for decay stabilization
	`ALTER TABLE learnings ADD COLUMN last_hit_at TEXT`,
	// v0.11: Add session_flavor for experiential context
	`ALTER TABLE learnings ADD COLUMN session_flavor TEXT DEFAULT ''`,
	// v0.12: compacted_blocks schema changed (session_id→thread_id, summary→content, dropped stat columns)
	`DROP TABLE IF EXISTS compacted_blocks`,
	`DROP INDEX IF EXISTS idx_compacted_session`,
	// v0.13: Subagent support
	`ALTER TABLE sessions ADD COLUMN parent_session_id TEXT`,
	`ALTER TABLE sessions ADD COLUMN agent_type TEXT`,
	// v0.14: Temporal validity layer
	`ALTER TABLE learnings ADD COLUMN valid_until TEXT`,
	`ALTER TABLE learnings ADD COLUMN supersedes INTEGER`,
	// v0.15: Trust-based supersede resistance
	`ALTER TABLE learnings ADD COLUMN importance INTEGER DEFAULT 3`,
	`ALTER TABLE learnings ADD COLUMN supersede_status TEXT`,
	// v0.16: Signal Bus — noise tracking for learnings
	`ALTER TABLE learnings ADD COLUMN noise_count INTEGER DEFAULT 0`,
	// v0.17: Learning V2 — structured metadata for better embedding & hook matching
	`ALTER TABLE learnings ADD COLUMN context TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN domain TEXT DEFAULT 'code'`,
	`ALTER TABLE learnings ADD COLUMN trigger_rule TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN embedding_text TEXT DEFAULT ''`,
	// v0.18: Separate fail_count from hit_count (view_count) for auto-escalation blocking
	`ALTER TABLE learnings ADD COLUMN fail_count INTEGER DEFAULT 0`,
	// v0.19: Differentiated learning counters (5-Count Model)
	`ALTER TABLE learnings ADD COLUMN match_count INTEGER DEFAULT 0`,
	`ALTER TABLE learnings ADD COLUMN inject_count INTEGER DEFAULT 0`,
	`ALTER TABLE learnings ADD COLUMN use_count INTEGER DEFAULT 0`,
	`ALTER TABLE learnings ADD COLUMN save_count INTEGER DEFAULT 0`,
	// v0.20: Spaced-repetition stability for Ebbinghaus decay
	`ALTER TABLE learnings ADD COLUMN stability REAL DEFAULT 30.0`,
	// v0.21: Document ingest — doc_sources registry, doc_chunks storage, learnings source tracking
	`CREATE TABLE IF NOT EXISTS doc_sources (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		version     TEXT NOT NULL DEFAULT '',
		path        TEXT,
		url         TEXT,
		project     TEXT NOT NULL DEFAULT '',
		chunk_count INTEGER DEFAULT 0,
		last_sync   DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_doc_sources_name ON doc_sources(name, project)`,
	`CREATE TABLE IF NOT EXISTS doc_chunks (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		source_id       INTEGER NOT NULL REFERENCES doc_sources(id),
		source_file     TEXT NOT NULL,
		source_hash     TEXT NOT NULL,
		heading_path    TEXT NOT NULL DEFAULT '',
		section_level   INTEGER DEFAULT 0,
		content         TEXT NOT NULL,
		content_hash    TEXT NOT NULL,
		tokens_approx   INTEGER DEFAULT 0,
		metadata        TEXT DEFAULT '{}',
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_doc_chunks_source ON doc_chunks(source_id)`,
	`CREATE INDEX IF NOT EXISTS idx_doc_chunks_file ON doc_chunks(source_file)`,
	`CREATE INDEX IF NOT EXISTS idx_doc_chunks_heading ON doc_chunks(heading_path)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunks_fts USING fts5(content, heading_path, metadata, content='doc_chunks', content_rowid='id', tokenize='porter unicode61')`,
	`ALTER TABLE learnings ADD COLUMN source_file TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN source_hash TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN doc_chunk_ref INTEGER DEFAULT 0`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_source_file ON learnings(source_file) WHERE source_file != ''`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_doc_chunk ON learnings(doc_chunk_ref) WHERE doc_chunk_ref > 0`,
	`ALTER TABLE doc_sources ADD COLUMN is_skill BOOLEAN DEFAULT 0`,
	`ALTER TABLE doc_sources ADD COLUMN original_path TEXT DEFAULT ''`,
	// v0.22: SHA-256 content hash for exact dedup
	`ALTER TABLE learnings ADD COLUMN content_hash TEXT DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_content_hash ON learnings(content_hash) WHERE content_hash != ''`,
	// v0.23: Incremental embedding tracking
	`ALTER TABLE learnings ADD COLUMN embedding_status TEXT DEFAULT 'done'`,
	`ALTER TABLE learnings ADD COLUMN embedding_content_hash TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN embedded_at TEXT`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_embedding_status ON learnings(embedding_status) WHERE embedding_status = 'pending'`,
	// v0.24: Persistent extraction marker — prevents re-extracting sessions whose learnings were all superseded
	`ALTER TABLE sessions ADD COLUMN extracted_at TEXT`,
	// Backfill: mark sessions that already have learnings as extracted
	`UPDATE sessions SET extracted_at = indexed_at WHERE extracted_at IS NULL AND id IN (SELECT DISTINCT session_id FROM learnings WHERE session_id IS NOT NULL AND session_id != '')`,
	// v0.25: Persistent narrative marker — prevents re-generating narratives whose learnings were superseded
	`ALTER TABLE sessions ADD COLUMN narrative_at TEXT`,
	// Backfill: mark sessions that already have narratives
	`UPDATE sessions SET narrative_at = indexed_at WHERE narrative_at IS NULL AND id IN (SELECT DISTINCT session_id FROM learnings WHERE category = 'narrative' AND session_id IS NOT NULL AND session_id != '')`,
	// v0.26: Store embedding vectors in SQLite — eliminates 10GB chromem-go load in embed-learnings
	`ALTER TABLE learnings ADD COLUMN embedding_vector BLOB`,
	// v0.27: Skill full content — store complete skill text on doc_sources for whole-file injection
	`ALTER TABLE doc_sources ADD COLUMN full_content TEXT DEFAULT ''`,
	// v0.28: Gap review marker — tracks which gaps have been reviewed by LLM
	`ALTER TABLE knowledge_gaps ADD COLUMN reviewed_at DATETIME`,
	`ALTER TABLE knowledge_gaps ADD COLUMN review_verdict TEXT`, // "keep" or "noise"
	// v0.29: Fixation ratio — proportion of session spent in fixation loops (consecutive errors, edit-build cycles)
	`ALTER TABLE sessions ADD COLUMN fixation_ratio REAL DEFAULT 0`,
	// v0.30: Porter stemming for learnings FTS5 — improves recall for conjugation variants
	`DROP TABLE IF EXISTS learnings_fts`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS learnings_fts USING fts5(content, content_rowid=id, tokenize='porter unicode61')`,
	`INSERT OR IGNORE INTO learnings_fts(rowid, content) SELECT id, content FROM learnings`,
	// v0.31: Multi-agent support — agent_role, broadcast table, dialog lineage
	`ALTER TABLE sessions ADD COLUMN agent_role TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN agent_role TEXT DEFAULT ''`,
	`ALTER TABLE learnings ADD COLUMN dialog_id INTEGER`,
	`CREATE TABLE IF NOT EXISTS agent_broadcasts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		sender      TEXT NOT NULL,
		project     TEXT NOT NULL,
		content     TEXT NOT NULL,
		read_by     TEXT DEFAULT '',
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_broadcasts_project ON agent_broadcasts(project, created_at DESC)`,
	// v0.32: Session quarantine — mark sessions as quarantined to exclude their learnings from search
	`ALTER TABLE sessions ADD COLUMN skip_extraction BOOLEAN DEFAULT 0`,
	`ALTER TABLE learnings ADD COLUMN quarantined_at TEXT`,
	// v0.33: Rules re-injection — condensed CLAUDE.md rules for periodic proxy injection
	`ALTER TABLE doc_sources ADD COLUMN is_rules BOOLEAN DEFAULT 0`,
	// v0.34: Task type classification for unfinished learnings
	`ALTER TABLE learnings ADD COLUMN task_type TEXT DEFAULT ''`,
	// v0.35: FTS5 tokenchars for technical terms — text_editor, go1.24, etc. indexed as single tokens
	// Uses content= sync table, so 'rebuild' re-reads from doc_chunks (no manual INSERT needed)
	`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunks_fts USING fts5(content, heading_path, metadata, content='doc_chunks', content_rowid='id', tokenize="porter unicode61 tokenchars '_-.'")`,
	// v0.36: Doc chunk embeddings — SSE vectors for semantic search alongside BM25
	`ALTER TABLE doc_chunks ADD COLUMN embedding_vector BLOB`,
	`ALTER TABLE doc_chunks ADD COLUMN embedding_hash TEXT DEFAULT ''`,
	// v0.37: Turn-based decay — persistent per-project turn counters + learning creation turn
	`ALTER TABLE learnings ADD COLUMN turns_at_creation INTEGER DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS turn_counters (
		project    TEXT PRIMARY KEY,
		turn_count INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	// v0.38: Agent heartbeat — relay count, spawn depth, token budget tracking
	`ALTER TABLE agents ADD COLUMN relay_count INTEGER DEFAULT 0`,
	`ALTER TABLE agents ADD COLUMN depth INTEGER DEFAULT 0`,
	`ALTER TABLE agents ADD COLUMN token_budget INTEGER DEFAULT 0`,
	// v0.39: Agent crash recovery — retry count
	`ALTER TABLE agents ADD COLUMN retry_count INTEGER DEFAULT 0`,
	// v0.40: Reliable message delivery — per-message delivery tracking
	`ALTER TABLE agent_messages ADD COLUMN delivered INTEGER DEFAULT 0`,
	`ALTER TABLE agent_messages ADD COLUMN delivered_at TEXT`,
	`ALTER TABLE agent_messages ADD COLUMN delivery_retries INTEGER DEFAULT 0`,
	`ALTER TABLE agent_messages ADD COLUMN delivery_failed INTEGER DEFAULT 0`,
	// v0.41: Multi-agent session origin tracking (claude vs codex)
	`ALTER TABLE sessions ADD COLUMN source_agent TEXT DEFAULT 'claude'`,
	// v0.43: Agent backend type (claude/codex) for multi-backend orchestration
	`ALTER TABLE agents ADD COLUMN backend TEXT DEFAULT 'claude'`,
	// v0.44: Contextual doc injection — auto-inject docs when Claude edits matching file types
	`ALTER TABLE doc_sources ADD COLUMN trigger_extensions TEXT DEFAULT ''`,
	// v0.45: Doc type classification — "reference" (auto-inject) vs "style" (explicit search only)
	`ALTER TABLE doc_sources ADD COLUMN doc_type TEXT NOT NULL DEFAULT 'reference'`,
	// v0.46: Example query for plan-based docs reminder
	`ALTER TABLE doc_sources ADD COLUMN example_query TEXT DEFAULT ''`,
	// v0.47: ACK-loop prevention — message type classification
	`ALTER TABLE agent_messages ADD COLUMN msg_type TEXT DEFAULT 'command'`,
	// v0.48: Fork Reflection — impact scoring for learnings
	`ALTER TABLE learnings ADD COLUMN impact_score REAL DEFAULT 0.0`,
	`ALTER TABLE learnings ADD COLUMN impact_count INTEGER DEFAULT 0`,
	// v0.49: Fork Reflection — fork coverage tracking for dedup with post-session extraction
	`CREATE TABLE IF NOT EXISTS fork_coverage (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id   TEXT NOT NULL,
		from_msg_idx INTEGER NOT NULL,
		to_msg_idx   INTEGER NOT NULL,
		fork_index   INTEGER NOT NULL,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_fork_coverage_session ON fork_coverage(session_id)`,
	// v0.50: Fork token usage tracking
	`ALTER TABLE token_usage ADD COLUMN fork_input_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE token_usage ADD COLUMN fork_output_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE token_usage ADD COLUMN fork_request_count INTEGER DEFAULT 0`,
	// v0.51: Typed association graph — semantic edges between learnings
	`ALTER TABLE associations ADD COLUMN relation_type TEXT DEFAULT 'related'`,
	// --- v1.0.0 baseline (schema v0.51, 1006 commits, 1000+ sessions) ---
	// v0.52: Cache breakdown for usage statistics
	`ALTER TABLE token_usage ADD COLUMN cache_read_tokens INTEGER DEFAULT 0`,
	`ALTER TABLE token_usage ADD COLUMN cache_write_tokens INTEGER DEFAULT 0`,
	// v0.53: Learning lineage — source message attribution
	`ALTER TABLE learnings ADD COLUMN source_msg_from INTEGER DEFAULT -1`,
	`ALTER TABLE learnings ADD COLUMN source_msg_to INTEGER DEFAULT -1`,
	// v0.54: CBM index mtime for scan cache invalidation
	`ALTER TABLE project_scan ADD COLUMN cbm_mtime INTEGER NOT NULL DEFAULT 0`,
}

// messagesMigrations runs against messages.db (separate from yesmem.db migrations).
var messagesMigrations = []string{
	// v0.42: Persist source agent on messages for transparent mixed-agent histories
	`ALTER TABLE messages ADD COLUMN source_agent TEXT DEFAULT 'claude'`,
}

const tableSessions = `CREATE TABLE IF NOT EXISTS sessions (
	id              TEXT PRIMARY KEY,
	project         TEXT NOT NULL,
	project_short   TEXT NOT NULL,
	git_branch      TEXT,
	first_message   TEXT,
	message_count   INTEGER DEFAULT 0,
	started_at      TEXT NOT NULL,
	ended_at        TEXT,
	jsonl_path      TEXT NOT NULL,
	jsonl_size      INTEGER,
	indexed_at      TEXT NOT NULL,
	parent_session_id TEXT,
	agent_type        TEXT,
	source_agent      TEXT DEFAULT 'claude',
	extracted_at      TEXT,
	narrative_at      TEXT,
	skip_extraction   BOOLEAN DEFAULT 0
)`

const tableMessages = `CREATE TABLE IF NOT EXISTS messages (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id      TEXT NOT NULL REFERENCES sessions(id),
	source_agent    TEXT DEFAULT 'claude',
	role            TEXT NOT NULL,
	message_type    TEXT NOT NULL,
	content         TEXT,
	content_blob    BLOB,
	tool_name       TEXT,
	file_path       TEXT,
	timestamp       TEXT NOT NULL,
	sequence        INTEGER NOT NULL
)`

const tableLearnings = `CREATE TABLE IF NOT EXISTS learnings (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id          TEXT,
	category            TEXT NOT NULL,
	content             TEXT NOT NULL,
	project             TEXT,
	confidence          REAL DEFAULT 1.0,
	superseded_by       INTEGER,
	supersede_reason    TEXT,
	created_at          TEXT NOT NULL,
	expires_at          TEXT,
	model_used          TEXT NOT NULL,
	source              TEXT DEFAULT 'llm_extracted',
	hit_count           INTEGER DEFAULT 0,
	emotional_intensity REAL DEFAULT 0.0,
	last_hit_at         TEXT,
	session_flavor      TEXT DEFAULT '',
	valid_until         TEXT,
	supersedes          INTEGER,
	importance          INTEGER DEFAULT 3,
	supersede_status    TEXT,
	noise_count         INTEGER DEFAULT 0,
	context             TEXT DEFAULT '',
	domain              TEXT DEFAULT 'code',
	trigger_rule        TEXT DEFAULT '',
	embedding_text      TEXT DEFAULT '',
	embedding_status    TEXT DEFAULT 'done',
	embedding_content_hash TEXT DEFAULT '',
	embedded_at         TEXT,
	fail_count          INTEGER DEFAULT 0,
	match_count         INTEGER DEFAULT 0,
	inject_count        INTEGER DEFAULT 0,
	use_count           INTEGER DEFAULT 0,
	save_count          INTEGER DEFAULT 0,
	stability           REAL DEFAULT 30.0,
	source_file         TEXT DEFAULT '',
	source_hash         TEXT DEFAULT '',
	doc_chunk_ref       INTEGER DEFAULT 0,
	content_hash        TEXT DEFAULT '',
	agent_role          TEXT DEFAULT '',
	dialog_id           INTEGER,
	quarantined_at      TEXT,
	task_type           TEXT DEFAULT '',
	turns_at_creation   INTEGER DEFAULT 0,
	impact_score        REAL DEFAULT 0.0,
	impact_count        INTEGER DEFAULT 0,
	source_msg_from     INTEGER DEFAULT -1,
	source_msg_to       INTEGER DEFAULT -1
)`

const tableAssociations = `CREATE TABLE IF NOT EXISTS associations (
	source_type   TEXT NOT NULL,
	source_id     TEXT NOT NULL,
	target_type   TEXT NOT NULL,
	target_id     TEXT NOT NULL,
	weight        REAL DEFAULT 1.0,
	relation_type TEXT DEFAULT 'related',
	PRIMARY KEY (source_type, source_id, target_type, target_id)
)`

const tableIndexState = `CREATE TABLE IF NOT EXISTS index_state (
	jsonl_path  TEXT PRIMARY KEY,
	file_size   INTEGER NOT NULL,
	file_mtime  TEXT NOT NULL,
	indexed_at  TEXT NOT NULL
)`

const tableProjectProfiles = `CREATE TABLE IF NOT EXISTS project_profiles (
	project       TEXT PRIMARY KEY,
	profile_text  TEXT NOT NULL,
	generated_at  TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	session_count INTEGER NOT NULL,
	model_used    TEXT NOT NULL
)`

const tableStrategicContext = `CREATE TABLE IF NOT EXISTS strategic_context (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	scope         TEXT NOT NULL,
	context       TEXT NOT NULL,
	source        TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	superseded_by INTEGER,
	active        BOOLEAN DEFAULT TRUE
)`

const tableFileCoverage = `CREATE TABLE IF NOT EXISTS file_coverage (
	project         TEXT NOT NULL,
	file_path       TEXT NOT NULL,
	directory       TEXT NOT NULL,
	session_count   INTEGER DEFAULT 0,
	last_touched    TEXT NOT NULL,
	operation_types TEXT,
	PRIMARY KEY (project, file_path)
)`

const tableSelfFeedback = `CREATE TABLE IF NOT EXISTS self_feedback (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id    TEXT,
	feedback_type TEXT NOT NULL,
	description   TEXT NOT NULL,
	pattern       TEXT,
	created_at    TEXT NOT NULL
)`

const tablePersonaTraits = `CREATE TABLE IF NOT EXISTS persona_traits (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id         TEXT NOT NULL DEFAULT 'default',
	dimension       TEXT NOT NULL,
	trait_key       TEXT NOT NULL,
	trait_value     TEXT NOT NULL,
	confidence      REAL NOT NULL DEFAULT 0.5,
	source          TEXT NOT NULL DEFAULT 'auto_extracted',
	evidence_count  INTEGER NOT NULL DEFAULT 1,
	first_seen      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	superseded      BOOLEAN NOT NULL DEFAULT FALSE,
	UNIQUE(user_id, dimension, trait_key)
)`

const tablePersonaDirectives = `CREATE TABLE IF NOT EXISTS persona_directives (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id         TEXT NOT NULL DEFAULT 'default',
	directive       TEXT NOT NULL,
	traits_hash     TEXT NOT NULL,
	generated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
	model_used      TEXT NOT NULL
)`

const tableSessionTracking = `CREATE TABLE IF NOT EXISTS session_tracking (
	project    TEXT NOT NULL,
	session_id TEXT NOT NULL,
	reason     TEXT NOT NULL,
	timestamp  TEXT NOT NULL,
	UNIQUE(project, session_id)
)`

var indices = []string{
	`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_type ON messages(message_type)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_file ON messages(file_path) WHERE file_path IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_short)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions(started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_project_started ON sessions(project, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source_agent)`,
	`CREATE INDEX IF NOT EXISTS idx_assoc_source ON associations(source_type, source_id)`,
	`CREATE INDEX IF NOT EXISTS idx_assoc_target ON associations(target_type, target_id)`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_category ON learnings(category)`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_project ON learnings(project)`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_active ON learnings(category, project) WHERE superseded_by IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_learnings_active_id ON learnings(superseded_by, id DESC) WHERE superseded_by IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_coverage_dir ON file_coverage(project, directory)`,
	`CREATE INDEX IF NOT EXISTS idx_persona_active ON persona_traits(user_id, superseded) WHERE superseded = FALSE`,
	`CREATE INDEX IF NOT EXISTS idx_st_project ON session_tracking(project, timestamp DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_compacted_thread ON compacted_blocks(thread_id, start_idx)`,
	`CREATE INDEX IF NOT EXISTS idx_clusters_project ON learning_clusters(project)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id) WHERE parent_session_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_gaps_project ON knowledge_gaps(project)`,
	`CREATE INDEX IF NOT EXISTS idx_gaps_resolved ON knowledge_gaps(resolved_at)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_entities_lid ON learning_entities(learning_id)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_entities_value ON learning_entities(value)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_actions_lid ON learning_actions(learning_id)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_actions_value ON learning_actions(value)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_keywords_lid ON learning_keywords(learning_id)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_keywords_value ON learning_keywords(value)`,
	`CREATE INDEX IF NOT EXISTS idx_learning_aq_lid ON learning_anticipated_queries(learning_id)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS anticipated_queries_fts USING fts5(value, learning_id UNINDEXED, tokenize='porter unicode61')`,
	`CREATE INDEX IF NOT EXISTS idx_query_log_created ON query_log(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_query_log_cluster ON query_log(cluster_id)`,
	`CREATE INDEX IF NOT EXISTS idx_query_clusters_project ON query_clusters(project)`,
	`CREATE INDEX IF NOT EXISTS idx_lcs_learning ON learning_cluster_scores(learning_id)`,
	`CREATE INDEX IF NOT EXISTS idx_lcs_cluster ON learning_cluster_scores(cluster_id)`,
	`CREATE INDEX IF NOT EXISTS idx_agents_project ON agents(project)`,
	`CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status) WHERE status = 'running'`,
}

const tableClaudeMdState = `CREATE TABLE IF NOT EXISTS claudemd_state (
	project         TEXT PRIMARY KEY,
	last_generated  DATETIME NOT NULL,
	learnings_hash  TEXT NOT NULL,
	output_path     TEXT NOT NULL
)`

const tableRefinedBriefings = `CREATE TABLE IF NOT EXISTS refined_briefings (
	project      TEXT PRIMARY KEY,
	raw_hash     TEXT NOT NULL,
	refined_text TEXT NOT NULL,
	model_used   TEXT NOT NULL,
	generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

const tableCompactedBlocks = `CREATE TABLE IF NOT EXISTS compacted_blocks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	thread_id  TEXT NOT NULL,
	start_idx  INTEGER NOT NULL,
	end_idx    INTEGER NOT NULL,
	content    TEXT NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(thread_id, start_idx)
)`

const tableProxyState = `CREATE TABLE IF NOT EXISTS proxy_state (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableKnowledgeGaps = `CREATE TABLE IF NOT EXISTS knowledge_gaps (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	topic       TEXT NOT NULL,
	project     TEXT,
	first_seen  DATETIME DEFAULT CURRENT_TIMESTAMP,
	last_seen   DATETIME DEFAULT CURRENT_TIMESTAMP,
	hit_count   INTEGER DEFAULT 1,
	resolved_at DATETIME,
	resolved_by INTEGER REFERENCES learnings(id),
	reviewed_at DATETIME,
	review_verdict TEXT,
	UNIQUE(topic, project)
)`

const tableLearningClusters = `CREATE TABLE IF NOT EXISTS learning_clusters (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	project          TEXT NOT NULL,
	label            TEXT NOT NULL,
	learning_count   INTEGER DEFAULT 0,
	avg_recency_days REAL DEFAULT 0,
	avg_hit_count    REAL DEFAULT 0,
	confidence       REAL DEFAULT 0,
	learning_ids     TEXT DEFAULT '[]',
	created_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at       DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableContradictions = `CREATE TABLE IF NOT EXISTS contradictions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	learning_ids TEXT,
	description  TEXT NOT NULL,
	project      TEXT,
	thread_id    TEXT,
	resolved     INTEGER DEFAULT 0,
	created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
)`

const tableDailySpend = `CREATE TABLE IF NOT EXISTS daily_spend (
	day        TEXT NOT NULL,
	bucket     TEXT NOT NULL,
	spent_usd  REAL DEFAULT 0,
	calls      INTEGER DEFAULT 0,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (day, bucket)
)`

// v0.17: Learning V2 junction tables
const tableLearningEntities = `CREATE TABLE IF NOT EXISTS learning_entities (
	learning_id INTEGER NOT NULL REFERENCES learnings(id) ON DELETE CASCADE,
	value       TEXT NOT NULL,
	type        TEXT DEFAULT ''
)`

const tableLearningActions = `CREATE TABLE IF NOT EXISTS learning_actions (
	learning_id INTEGER NOT NULL REFERENCES learnings(id) ON DELETE CASCADE,
	value       TEXT NOT NULL
)`

const tableLearningKeywords = `CREATE TABLE IF NOT EXISTS learning_keywords (
	learning_id INTEGER NOT NULL REFERENCES learnings(id) ON DELETE CASCADE,
	value       TEXT NOT NULL
)`

const tableLearningAnticipatedQueries = `CREATE TABLE IF NOT EXISTS learning_anticipated_queries (
	learning_id INTEGER NOT NULL REFERENCES learnings(id) ON DELETE CASCADE,
	value       TEXT NOT NULL
)`

const tableQueryLog = `CREATE TABLE IF NOT EXISTS query_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL DEFAULT '',
	project TEXT NOT NULL DEFAULT '',
	query_text TEXT NOT NULL,
	query_vector BLOB,
	cluster_id INTEGER,
	injected_learning_ids TEXT DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableQueryClusters = `CREATE TABLE IF NOT EXISTS query_clusters (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project TEXT NOT NULL DEFAULT '',
	centroid_vector BLOB NOT NULL,
	label TEXT DEFAULT '',
	query_count INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableLearningClusterScores = `CREATE TABLE IF NOT EXISTS learning_cluster_scores (
	learning_id INTEGER NOT NULL REFERENCES learnings(id) ON DELETE CASCADE,
	cluster_id INTEGER NOT NULL REFERENCES query_clusters(id) ON DELETE CASCADE,
	inject_count INTEGER DEFAULT 0,
	use_count INTEGER DEFAULT 0,
	noise_count INTEGER DEFAULT 0,
	last_injected_at DATETIME,
	PRIMARY KEY (learning_id, cluster_id)
)`

const tableEmbeddingCache = `CREATE TABLE IF NOT EXISTS embedding_cache (
	query_hash TEXT PRIMARY KEY,
	query_text TEXT NOT NULL,
	vector     BLOB NOT NULL,
	model      TEXT NOT NULL,
	created_at TEXT NOT NULL
)`

const tablePlans = `CREATE TABLE IF NOT EXISTS plans (
	thread_id  TEXT PRIMARY KEY,
	content    TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'active',
	scope      TEXT NOT NULL DEFAULT 'session',
	project    TEXT DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

const tableTurnCounters = `CREATE TABLE IF NOT EXISTS turn_counters (
	project    TEXT PRIMARY KEY,
	turn_count INTEGER NOT NULL DEFAULT 0,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableCodeDescriptions = `CREATE TABLE IF NOT EXISTS code_descriptions (
	project                    TEXT NOT NULL,
	package_name               TEXT NOT NULL,
	description                TEXT NOT NULL DEFAULT '',
	anti_patterns              TEXT NOT NULL DEFAULT '',
	git_head                   TEXT NOT NULL,
	learning_count_at_creation INTEGER NOT NULL DEFAULT 0,
	created_at                 DATETIME DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (project, package_name)
)`

const tableProjectScan = `CREATE TABLE IF NOT EXISTS project_scan (
	project    TEXT PRIMARY KEY,
	scan_json  TEXT NOT NULL,
	git_head   TEXT NOT NULL,
	cbm_mtime  INTEGER NOT NULL DEFAULT 0,
	scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
)`
