package storage

import (
	"context"
	"fmt"
)

// MigrateResult holds per-table row counts affected by a project migration.
type MigrateResult struct {
	Sessions       int64
	Learnings      int64
	Profiles       int64
	Coverage       int64
	Tracking       int64
	ClaudeMD       int64
	Briefings      int64
	Clusters       int64
	DocSources     int64
	Gaps           int64
	Pins           int64
	Contradictions int64
	QueryLog       int64
	QueryClusters  int64
	Broadcasts     int64
}

// Total returns the sum of all affected rows.
func (r *MigrateResult) Total() int64 {
	return r.Sessions + r.Learnings + r.Profiles + r.Coverage +
		r.Tracking + r.ClaudeMD + r.Briefings + r.Clusters + r.DocSources +
		r.Gaps + r.Pins + r.Contradictions + r.QueryLog + r.QueryClusters + r.Broadcasts
}

// MigrateProject renames all occurrences of a project across every table.
// Sessions and session_tracking store full paths; all other tables store short names.
// Both fromPath+toPath AND fromShort+toShort are applied to the correct tables.
func (s *Store) MigrateProject(fromPath, toPath, fromShort, toShort string) (*MigrateResult, error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var res MigrateResult

	// --- Tables using FULL PATH ---

	// 1. sessions — project is full path, project_short is short name
	r, err := tx.Exec(`UPDATE sessions SET project = ?, project_short = ? WHERE project = ?`,
		toPath, toShort, fromPath)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	res.Sessions, _ = r.RowsAffected()

	// 2. session_tracking — uses full path
	tx.Exec(`DELETE FROM session_tracking WHERE project = ? AND session_id IN (SELECT session_id FROM session_tracking WHERE project = ?)`, toPath, fromPath)
	r, err = tx.Exec(`UPDATE session_tracking SET project = ? WHERE project = ?`, toPath, fromPath)
	if err != nil {
		return nil, fmt.Errorf("session_tracking: %w", err)
	}
	res.Tracking, _ = r.RowsAffected()

	// --- Tables using SHORT NAME ---

	// 3. learnings
	r, err = tx.Exec(`UPDATE learnings SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("learnings: %w", err)
	}
	res.Learnings, _ = r.RowsAffected()

	// 4. project_profiles (PK: project)
	tx.Exec(`DELETE FROM project_profiles WHERE project = ?`, toShort)
	r, err = tx.Exec(`UPDATE project_profiles SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("project_profiles: %w", err)
	}
	res.Profiles, _ = r.RowsAffected()

	// 5. file_coverage (composite key: project, file_path)
	tx.Exec(`DELETE FROM file_coverage WHERE project = ? AND file_path IN (SELECT file_path FROM file_coverage WHERE project = ?)`, toShort, fromShort)
	r, err = tx.Exec(`UPDATE file_coverage SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("file_coverage: %w", err)
	}
	res.Coverage, _ = r.RowsAffected()

	// 6. claudemd_state (PK: project)
	tx.Exec(`DELETE FROM claudemd_state WHERE project = ?`, toShort)
	r, err = tx.Exec(`UPDATE claudemd_state SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("claudemd_state: %w", err)
	}
	res.ClaudeMD, _ = r.RowsAffected()

	// 7. refined_briefings (PK: project)
	tx.Exec(`DELETE FROM refined_briefings WHERE project = ?`, toShort)
	r, err = tx.Exec(`UPDATE refined_briefings SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("refined_briefings: %w", err)
	}
	res.Briefings, _ = r.RowsAffected()

	// 8. learning_clusters
	r, err = tx.Exec(`UPDATE learning_clusters SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("learning_clusters: %w", err)
	}
	res.Clusters, _ = r.RowsAffected()

	// 9. doc_sources (UNIQUE: name, project)
	tx.Exec(`DELETE FROM doc_sources WHERE project = ? AND name IN (SELECT name FROM doc_sources WHERE project = ?)`, toShort, fromShort)
	r, err = tx.Exec(`UPDATE doc_sources SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("doc_sources: %w", err)
	}
	res.DocSources, _ = r.RowsAffected()

	// 10. knowledge_gaps (UNIQUE: topic, project)
	tx.Exec(`DELETE FROM knowledge_gaps WHERE project = ? AND topic IN (SELECT topic FROM knowledge_gaps WHERE project = ?)`, toShort, fromShort)
	r, err = tx.Exec(`UPDATE knowledge_gaps SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("knowledge_gaps: %w", err)
	}
	res.Gaps, _ = r.RowsAffected()

	// 11. pinned_learnings
	r, err = tx.Exec(`UPDATE pinned_learnings SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("pinned_learnings: %w", err)
	}
	res.Pins, _ = r.RowsAffected()

	// 12. contradictions
	r, err = tx.Exec(`UPDATE contradictions SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("contradictions: %w", err)
	}
	res.Contradictions, _ = r.RowsAffected()

	// 13. query_log
	r, err = tx.Exec(`UPDATE query_log SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("query_log: %w", err)
	}
	res.QueryLog, _ = r.RowsAffected()

	// 14. query_clusters
	r, err = tx.Exec(`UPDATE query_clusters SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("query_clusters: %w", err)
	}
	res.QueryClusters, _ = r.RowsAffected()

	// 15. agent_broadcasts
	r, err = tx.Exec(`UPDATE agent_broadcasts SET project = ? WHERE project = ?`, toShort, fromShort)
	if err != nil {
		return nil, fmt.Errorf("agent_broadcasts: %w", err)
	}
	res.Broadcasts, _ = r.RowsAffected()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &res, nil
}

// MigrateProjectDryRun returns what MigrateProject would affect without making changes.
func (s *Store) MigrateProjectDryRun(fromPath, toPath, fromShort, toShort string) (*MigrateResult, error) {
	var res MigrateResult
	db := s.readerDB()

	count := func(query string, args ...any) int64 {
		var n int64
		db.QueryRow(query, args...).Scan(&n)
		return n
	}

	// Full path tables
	res.Sessions = count("SELECT COUNT(*) FROM sessions WHERE project = ?", fromPath)
	res.Tracking = count("SELECT COUNT(*) FROM session_tracking WHERE project = ?", fromPath)

	// Short name tables
	res.Learnings = count("SELECT COUNT(*) FROM learnings WHERE project = ?", fromShort)
	res.Profiles = count("SELECT COUNT(*) FROM project_profiles WHERE project = ?", fromShort)
	res.Coverage = count("SELECT COUNT(*) FROM file_coverage WHERE project = ?", fromShort)
	res.ClaudeMD = count("SELECT COUNT(*) FROM claudemd_state WHERE project = ?", fromShort)
	res.Briefings = count("SELECT COUNT(*) FROM refined_briefings WHERE project = ?", fromShort)
	res.Clusters = count("SELECT COUNT(*) FROM learning_clusters WHERE project = ?", fromShort)
	res.DocSources = count("SELECT COUNT(*) FROM doc_sources WHERE project = ?", fromShort)
	res.Gaps = count("SELECT COUNT(*) FROM knowledge_gaps WHERE project = ?", fromShort)
	res.Pins = count("SELECT COUNT(*) FROM pinned_learnings WHERE project = ?", fromShort)
	res.Contradictions = count("SELECT COUNT(*) FROM contradictions WHERE project = ?", fromShort)
	res.QueryLog = count("SELECT COUNT(*) FROM query_log WHERE project = ?", fromShort)
	res.QueryClusters = count("SELECT COUNT(*) FROM query_clusters WHERE project = ?", fromShort)
	res.Broadcasts = count("SELECT COUNT(*) FROM agent_broadcasts WHERE project = ?", fromShort)

	return &res, nil
}
