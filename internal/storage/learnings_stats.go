package storage

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// StatsFilter holds optional time range and project filters for stats queries.
type StatsFilter struct {
	Project string
	Since   string // ISO datetime or empty
	Before  string // ISO datetime or empty
}

// statsWhere builds a WHERE clause fragment and args from StatsFilter.
func statsWhere(f StatsFilter) (string, []any) {
	clauses := []string{"superseded_by IS NULL"}
	var args []any
	if f.Project != "" {
		clauses = append(clauses, "(project = ? OR project IS NULL OR project = '')")
		args = append(args, f.Project)
	}
	if f.Since != "" {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Before != "" {
		clauses = append(clauses, "created_at < ?")
		args = append(args, f.Before)
	}
	return strings.Join(clauses, " AND "), args
}

// statsWhereAll is like statsWhere but includes superseded learnings (for evolution stats).
func statsWhereAll(f StatsFilter) (string, []any) {
	clauses := []string{"1=1"}
	var args []any
	if f.Project != "" {
		clauses = append(clauses, "(project = ? OR project IS NULL OR project = '')")
		args = append(args, f.Project)
	}
	if f.Since != "" {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Before != "" {
		clauses = append(clauses, "created_at < ?")
		args = append(args, f.Before)
	}
	return strings.Join(clauses, " AND "), args
}

type LearningStats struct {
	ActiveCount      int     `json:"active_count"`
	SupersededCount  int     `json:"superseded_count"`
	ArchivedCount    int     `json:"archived_count"`
	TotalInjects7d   int     `json:"total_injects_7d"`
	UsedInjects7d    int     `json:"used_injects_7d"`
	TotalNoiseCount  int     `json:"total_noise_count"`
	TotalInjectCount int     `json:"total_inject_count"`
	TotalFailCount   int     `json:"total_fail_count"`
	TotalSaveCount   int     `json:"total_save_count"`
	TotalUseCount    int     `json:"total_use_count"`
	ToxicCount       int     `json:"toxic_count"`       // fail_count >= 3
	DeadWeightCount  int     `json:"dead_weight_count"`
	DecayingCount    int     `json:"decaying_count"`
	AvgPrecision     float64 `json:"avg_precision"`
}

// SourceAttribution holds per-source learning counts and effectiveness.
type SourceAttribution struct {
	Source     string  `json:"source"`
	Count      int     `json:"count"`
	WithUses   int     `json:"with_uses"`
	TotalSave  int     `json:"total_save"`
	TotalFail  int     `json:"total_fail"`
}

// GetSourceAttribution returns per-source learning counts with effectiveness.
func (s *Store) GetSourceAttribution(f StatsFilter) ([]SourceAttribution, error) {
	where, args := statsWhere(f)
	rows, err := s.readerDB().Query(`SELECT COALESCE(source, 'unknown'), COUNT(*), SUM(CASE WHEN use_count > 0 THEN 1 ELSE 0 END), COALESCE(SUM(save_count), 0), COALESCE(SUM(fail_count), 0) FROM learnings WHERE `+where+` GROUP BY source ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceAttribution
	for rows.Next() {
		var sa SourceAttribution
		if err := rows.Scan(&sa.Source, &sa.Count, &sa.WithUses, &sa.TotalSave, &sa.TotalFail); err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, rows.Err()
}

// SpendSummary holds aggregated API cost stats.
type SpendSummary struct {
	TotalUSD    float64 `json:"total_usd"`
	TotalCalls  int     `json:"total_calls"`
	ExtractUSD  float64 `json:"extract_usd"`
	QualityUSD  float64 `json:"quality_usd"`
	ProxyUSD    float64 `json:"proxy_usd"`
	ForkUSD     float64 `json:"fork_usd"`
	AvgPerDay   float64 `json:"avg_per_day"`
	Days        int     `json:"days"`
}

// Anthropic token prices (USD per token) — Claude Sonnet 4.5/4.6
// Used for extraction cost estimates (extraction runs on Sonnet).
const (
	priceInputPerToken      = 3.0 / 1_000_000  // $3/MTok
	priceOutputPerToken     = 15.0 / 1_000_000  // $15/MTok
	priceCacheReadPerToken  = 0.30 / 1_000_000  // $0.30/MTok
	priceCacheWritePerToken = 3.75 / 1_000_000  // $3.75/MTok
)

// GetSpendSummary returns aggregated API cost summary for the given period.
func (s *Store) GetSpendSummary(f StatsFilter) (*SpendSummary, error) {
	ss := &SpendSummary{}
	filter := "1=1"
	var args []any
	if f.Since != "" {
		filter += " AND day >= ?"
		args = append(args, f.Since[:10]) // date only
	}
	if f.Before != "" {
		filter += " AND day < ?"
		args = append(args, f.Before[:10])
	}
	if filter == "1=1" {
		// Default: last 30 days
		filter = "day >= date('now', '-30 days')"
	}
	rows, err := s.readerDB().Query(`SELECT bucket, SUM(spent_usd), SUM(calls), COUNT(DISTINCT day) FROM daily_spend WHERE `+filter+` GROUP BY bucket`, args...)
	if err != nil {
		return ss, err
	}
	defer rows.Close()
	totalDays := 0
	for rows.Next() {
		var bucket string
		var rawValue float64
		var calls, dayCount int
		if err := rows.Scan(&bucket, &rawValue, &calls, &dayCount); err != nil {
			return ss, err
		}
		if dayCount > totalDays {
			totalDays = dayCount
		}
		switch bucket {
		case "extract":
			ss.ExtractUSD = rawValue
			ss.TotalUSD += rawValue
		case "quality":
			ss.QualityUSD = rawValue
			ss.TotalUSD += rawValue
		case "proxy_input":
			usd := rawValue * priceInputPerToken
			ss.ProxyUSD += usd
			ss.TotalUSD += usd
		case "proxy_output":
			usd := rawValue * priceOutputPerToken
			ss.ProxyUSD += usd
			ss.TotalUSD += usd
		case "proxy_cache_read":
			usd := rawValue * priceCacheReadPerToken
			ss.ProxyUSD += usd
			ss.TotalUSD += usd
		case "proxy_cache_write":
			usd := rawValue * priceCacheWritePerToken
			ss.ProxyUSD += usd
			ss.TotalUSD += usd
		case "fork_input":
			usd := rawValue * priceInputPerToken
			ss.ForkUSD += usd
			ss.TotalUSD += usd
		case "fork_output":
			usd := rawValue * priceOutputPerToken
			ss.ForkUSD += usd
			ss.TotalUSD += usd
		case "fork_cache_read":
			usd := rawValue * priceCacheReadPerToken
			ss.ForkUSD += usd
			ss.TotalUSD += usd
		case "fork_cache_write":
			usd := rawValue * priceCacheWritePerToken
			ss.ForkUSD += usd
			ss.TotalUSD += usd
		default:
			ss.TotalUSD += rawValue
		}
		ss.TotalCalls += calls
	}
	ss.Days = totalDays
	if totalDays > 0 {
		ss.AvgPerDay = ss.TotalUSD / float64(totalDays)
	}
	return ss, rows.Err()
}

// CrossSessionRecallStats holds metrics about cross-session knowledge transfer.
type CrossSessionRecallStats struct {
	TotalSessions    int     `json:"total_sessions"`
	SessionsWithRecall int   `json:"sessions_with_recall"` // sessions that used learnings from other sessions
	AvgLearningsPerSession float64 `json:"avg_learnings_per_session"`
	UniqueProjectsWithMemory int `json:"unique_projects_with_memory"`
}

// GetCrossSessionRecallStats returns metrics about cross-session knowledge transfer.
func (s *Store) GetCrossSessionRecallStats(f StatsFilter) (*CrossSessionRecallStats, error) {
	cs := &CrossSessionRecallStats{}
	sessFilter := "1=1"
	var sessArgs []any
	if f.Project != "" {
		sessFilter = "project_short = ?"
		sessArgs = append(sessArgs, f.Project)
	}
	if f.Since != "" {
		sessFilter += " AND started_at >= ?"
		sessArgs = append(sessArgs, f.Since)
	}
	if f.Before != "" {
		sessFilter += " AND started_at < ?"
		sessArgs = append(sessArgs, f.Before)
	}
	// Exclude subagent sessions (parent_session_id set) — they don't use recall
	sessFilter += " AND (parent_session_id IS NULL OR parent_session_id = '')"
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE `+sessFilter, sessArgs...).Scan(&cs.TotalSessions)
	s.readerDB().QueryRow(`SELECT COUNT(DISTINCT project_short) FROM sessions WHERE `+sessFilter, sessArgs...).Scan(&cs.UniqueProjectsWithMemory)

	// Sessions with inject_count > 0 on their learnings = sessions where memory was actively used
	where, args := statsWhere(f)
	var totalLearnings int
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE inject_count > 0 AND `+where, args...).Scan(&totalLearnings)
	if cs.TotalSessions > 0 {
		cs.AvgLearningsPerSession = float64(totalLearnings) / float64(cs.TotalSessions)
	}

	// Count sessions where at least one learning created in a DIFFERENT session was injected
	s.readerDB().QueryRow(`SELECT COUNT(DISTINCT session_id) FROM learnings WHERE inject_count > 0 AND session_id IS NOT NULL AND session_id != '' AND `+where, args...).Scan(&cs.SessionsWithRecall)

	return cs, nil
}

// GetLearningStats returns aggregate statistics across all learnings.
func (s *Store) GetLearningStats(project string) (*LearningStats, error) {
	return s.GetLearningStatsF(StatsFilter{Project: project})
}

// GetLearningStatsF returns aggregate statistics with full filter support.
func (s *Store) GetLearningStatsF(f StatsFilter) (*LearningStats, error) {
	st := &LearningStats{}
	activeWhere, activeArgs := statsWhere(f)
	allWhere, allArgs := statsWhereAll(f)

	// Active count
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE (expires_at IS NULL OR expires_at > datetime('now')) AND `+activeWhere, activeArgs...).Scan(&st.ActiveCount)

	// Superseded count (includes superseded, so use allWhere + superseded_by IS NOT NULL)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by IS NOT NULL AND `+allWhere, allArgs...).Scan(&st.SupersededCount)

	// Archived (expired)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE expires_at IS NOT NULL AND expires_at <= datetime('now') AND `+activeWhere, activeArgs...).Scan(&st.ArchivedCount)

	// Injection stats
	s.readerDB().QueryRow(`SELECT COALESCE(SUM(noise_count), 0), COALESCE(SUM(inject_count), 0), COALESCE(SUM(fail_count), 0), COALESCE(SUM(save_count), 0), COALESCE(SUM(use_count), 0) FROM learnings WHERE `+activeWhere, activeArgs...).Scan(&st.TotalNoiseCount, &st.TotalInjectCount, &st.TotalFailCount, &st.TotalSaveCount, &st.TotalUseCount)

	// Toxic: fail_count >= 3
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE fail_count >= 3 AND `+activeWhere, activeArgs...).Scan(&st.ToxicCount)

	// Dead weight: use_count=0 with inject_count >= 10
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE use_count = 0 AND inject_count >= 10 AND `+activeWhere, activeArgs...).Scan(&st.DeadWeightCount)

	// Decaying: stability < 5.0
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE stability < 5.0 AND `+activeWhere, activeArgs...).Scan(&st.DecayingCount)

	// Average precision
	s.readerDB().QueryRow(`SELECT COALESCE(AVG(CAST(use_count AS REAL) / CAST(inject_count AS REAL)), 0) FROM learnings WHERE inject_count >= 1 AND `+activeWhere, activeArgs...).Scan(&st.AvgPrecision)

	return st, nil
}

// GetTopPerformers returns the N learnings with highest use_count.
func (s *Store) GetTopPerformers(limit int, project ...string) ([]models.Learning, error) {
	projectFilter := "1=1"
	args := []any{}
	if len(project) > 0 && project[0] != "" {
		projectFilter = "(project = ? OR project IS NULL OR project = '')"
		args = append(args, project[0])
	}
	args = append(args, limit)
	rows, err := s.readerDB().Query(`SELECT id, category, content, project, COALESCE(use_count, 0), COALESCE(inject_count, 0) FROM learnings WHERE superseded_by IS NULL AND use_count > 0 AND `+projectFilter+` ORDER BY use_count DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Learning
	for rows.Next() {
		var l models.Learning
		if err := rows.Scan(&l.ID, &l.Category, &l.Content, &l.Project, &l.UseCount, &l.InjectCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetDeadWeight returns learnings with 0 uses but N+ injections.
func (s *Store) GetDeadWeight(minInjections int, project ...string) ([]models.Learning, error) {
	projectFilter := "1=1"
	args := []any{minInjections}
	if len(project) > 0 && project[0] != "" {
		projectFilter = "(project = ? OR project IS NULL OR project = '')"
		args = append(args, project[0])
	}
	rows, err := s.readerDB().Query(`SELECT id, category, content, project, COALESCE(inject_count, 0) FROM learnings WHERE superseded_by IS NULL AND use_count = 0 AND inject_count >= ? AND `+projectFilter+` ORDER BY inject_count DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Learning
	for rows.Next() {
		var l models.Learning
		if err := rows.Scan(&l.ID, &l.Category, &l.Content, &l.Project, &l.InjectCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CountExplorationLearnings returns the number of active learnings with fewer than 3 injections.
func (s *Store) CountExplorationLearnings(project string) (int, error) {
	return s.CountExplorationLearningsF(StatsFilter{Project: project})
}

// CountExplorationLearningsF with full filter support.
func (s *Store) CountExplorationLearningsF(f StatsFilter) (int, error) {
	where, args := statsWhere(f)
	var count int
	err := s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE inject_count < 3 AND `+where, args...).Scan(&count)
	return count, err
}

// CategoryPrecision holds per-category effectiveness metrics.
type CategoryPrecision struct {
	Category       string  `json:"category"`
	Count          int     `json:"count"`
	WithUses       int     `json:"with_uses"`
	Precision      float64 `json:"precision"`
	AvgNoise       float64 `json:"avg_noise"`
	AvgFail        float64 `json:"avg_fail"`
	TotalInject    int     `json:"total_inject"`
	TotalSave      int     `json:"total_save"`
}

// GetCategoryPrecision returns per-category precision stats for active learnings with inject_count >= 3.
func (s *Store) GetCategoryPrecision(project string) ([]CategoryPrecision, error) {
	return s.GetCategoryPrecisionF(StatsFilter{Project: project})
}

// GetCategoryPrecisionF returns per-category precision with full filter support.
func (s *Store) GetCategoryPrecisionF(f StatsFilter) ([]CategoryPrecision, error) {
	where, args := statsWhere(f)
	rows, err := s.readerDB().Query(`SELECT category, COUNT(*) AS cnt, SUM(CASE WHEN use_count > 0 THEN 1 ELSE 0 END) AS with_uses, COALESCE(AVG(CASE WHEN inject_count >= 3 THEN CAST(use_count AS REAL) / CAST(inject_count AS REAL) END), 0) AS precision, COALESCE(AVG(noise_count), 0) AS avg_noise, COALESCE(AVG(fail_count), 0) AS avg_fail, COALESCE(SUM(inject_count), 0) AS total_inject, COALESCE(SUM(save_count), 0) AS total_save FROM learnings WHERE `+where+` GROUP BY category ORDER BY cnt DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryPrecision
	for rows.Next() {
		var cp CategoryPrecision
		if err := rows.Scan(&cp.Category, &cp.Count, &cp.WithUses, &cp.Precision, &cp.AvgNoise, &cp.AvgFail, &cp.TotalInject, &cp.TotalSave); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

// CoverageStats holds system health coverage metrics.
type CoverageStats struct {
	EmbeddingTotal   int `json:"embedding_total"`
	EmbeddingDone    int `json:"embedding_done"`
	NarrativeTotal   int `json:"narrative_total"`
	NarrativeDone    int `json:"narrative_done"`
	ProfileTotal     int `json:"profile_total"`
	ProfileDone      int `json:"profile_done"`
	GapsOpen           int `json:"gaps_open"`
	GapsResolved       int `json:"gaps_resolved"`
	GapsReviewResolved int `json:"gaps_review_resolved"`
	TotalMessages      int `json:"total_messages"`
	TotalSessions      int `json:"total_sessions"`
}

// GetCoverageStats returns system coverage metrics (embeddings, narratives, profiles, gaps).
func (s *Store) GetCoverageStats() (*CoverageStats, error) {
	return s.GetCoverageStatsF(StatsFilter{})
}

// GetCoverageStatsF returns system coverage metrics with filter support.
func (s *Store) GetCoverageStatsF(f StatsFilter) (*CoverageStats, error) {
	cs := &CoverageStats{}

	// Embedding coverage: active learnings with/without vectors
	where, args := statsWhere(f)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE `+where, args...).Scan(&cs.EmbeddingTotal)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE embedding_vector IS NOT NULL AND length(embedding_vector) > 0 AND `+where, args...).Scan(&cs.EmbeddingDone)

	// Session-level stats: total messages/sessions (project-filtered)
	sessFilter := "1=1"
	var sessArgs []any
	if f.Project != "" {
		sessFilter = "project_short = ?"
		sessArgs = append(sessArgs, f.Project)
	}
	if f.Since != "" {
		sessFilter += " AND started_at >= ?"
		sessArgs = append(sessArgs, f.Since)
	}
	if f.Before != "" {
		sessFilter += " AND started_at < ?"
		sessArgs = append(sessArgs, f.Before)
	}
	s.readerDB().QueryRow(`SELECT COALESCE(SUM(message_count), 0), COUNT(*) FROM sessions WHERE `+sessFilter, sessArgs...).Scan(&cs.TotalMessages, &cs.TotalSessions)

	// Narrative coverage: sessions with enough messages that have/don't have narratives
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE message_count >= 10`).Scan(&cs.NarrativeTotal)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE message_count >= 10 AND narrative_at IS NOT NULL`).Scan(&cs.NarrativeDone)

	// Profile coverage: projects with enough sessions that have/don't have profiles
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM (SELECT project_short FROM sessions GROUP BY project_short HAVING COUNT(*) >= 3)`).Scan(&cs.ProfileTotal)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM project_profiles`).Scan(&cs.ProfileDone)

	// Gap tracking (filtered by project and time)
	gapFilter := "1=1"
	var gapArgs []any
	if f.Project != "" {
		gapFilter += " AND project = ?"
		gapArgs = append(gapArgs, f.Project)
	}
	if f.Since != "" {
		gapFilter += " AND last_seen >= ?"
		gapArgs = append(gapArgs, f.Since)
	}
	if f.Before != "" {
		gapFilter += " AND first_seen < ?"
		gapArgs = append(gapArgs, f.Before)
	}
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM knowledge_gaps WHERE resolved_at IS NULL AND review_verdict IS NULL AND `+gapFilter, gapArgs...).Scan(&cs.GapsOpen)
	gapResolvedArgs := make([]any, len(gapArgs))
	copy(gapResolvedArgs, gapArgs)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM knowledge_gaps WHERE resolved_at IS NOT NULL AND `+gapFilter, gapResolvedArgs...).Scan(&cs.GapsResolved)
	gapReviewArgs := make([]any, len(gapArgs))
	copy(gapReviewArgs, gapArgs)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM knowledge_gaps WHERE review_verdict = 'resolved' AND `+gapFilter, gapReviewArgs...).Scan(&cs.GapsReviewResolved)

	return cs, nil
}

// EvolutionStats holds knowledge evolution effectiveness metrics.
type EvolutionStats struct {
	TotalSuperseded  int `json:"total_superseded"`
	RuleBasedCount   int `json:"rule_based_count"`
	LLMCount         int `json:"llm_count"`
	AvgChainLength   float64 `json:"avg_chain_length"`
}

// GetEvolutionStats returns knowledge evolution metrics.
func (s *Store) GetEvolutionStats() (*EvolutionStats, error) {
	return s.GetEvolutionStatsF(StatsFilter{})
}

// GetEvolutionStatsF with full filter support.
func (s *Store) GetEvolutionStatsF(f StatsFilter) (*EvolutionStats, error) {
	es := &EvolutionStats{}
	where, args := statsWhereAll(f)

	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by IS NOT NULL AND `+where, args...).Scan(&es.TotalSuperseded)
	s.readerDB().QueryRow(`SELECT COUNT(*) FROM learnings WHERE superseded_by IS NOT NULL AND supersede_reason LIKE 'rule-based%' AND `+where, args...).Scan(&es.RuleBasedCount)
	es.LLMCount = es.TotalSuperseded - es.RuleBasedCount

	s.readerDB().QueryRow(`SELECT COALESCE(AVG(chain_len), 1.0) FROM (SELECT COUNT(*) AS chain_len FROM learnings WHERE supersedes IS NOT NULL GROUP BY supersedes)`).Scan(&es.AvgChainLength)

	return es, nil
}

// GetToxicLearnings returns learnings with fail_count >= threshold (actively harmful).
func (s *Store) GetToxicLearnings(threshold int, project string) ([]models.Learning, error) {
	projectFilter := "1=1"
	args := []any{threshold}
	if project != "" {
		projectFilter = "(project = ? OR project IS NULL OR project = '')"
		args = append(args, project)
	}
	rows, err := s.readerDB().Query(`SELECT id, category, content, project, COALESCE(use_count, 0), COALESCE(fail_count, 0) FROM learnings WHERE superseded_by IS NULL AND fail_count >= ? AND `+projectFilter+` ORDER BY fail_count DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Learning
	for rows.Next() {
		var l models.Learning
		if err := rows.Scan(&l.ID, &l.Category, &l.Content, &l.Project, &l.UseCount, &l.FailCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetPersonaVolatility returns the average number of trait changes per day over the last N days.
func (s *Store) GetPersonaVolatility(days int) (float64, error) {
	if days <= 0 {
		days = 30
	}
	var changes int
	err := s.readerDB().QueryRow(`SELECT COUNT(*) FROM persona_traits WHERE superseded = FALSE AND updated_at > datetime('now', '-' || ? || ' days') AND evidence_count > 1`, days).Scan(&changes)
	if err != nil {
		return 0, err
	}
	return float64(changes) / float64(days), nil
}

// PersonaConfidenceStats holds persona trait statistics.
type PersonaConfidenceStats struct {
	AvgConfidence float64 `json:"avg_confidence"`
	TraitCount    int     `json:"trait_count"`
	LowestDim     string  `json:"lowest_dimension"`
	LowestConf    float64 `json:"lowest_confidence"`
	HighestDim    string  `json:"highest_dimension"`
	HighestConf   float64 `json:"highest_confidence"`
}

// GetPersonaConfidenceStats returns aggregate persona confidence stats.
func (s *Store) GetPersonaConfidenceStats() (*PersonaConfidenceStats, error) {
	st := &PersonaConfidenceStats{}
	s.readerDB().QueryRow(`SELECT COALESCE(AVG(confidence), 0), COUNT(*) FROM persona_traits WHERE superseded = FALSE`).Scan(&st.AvgConfidence, &st.TraitCount)
	s.readerDB().QueryRow(`SELECT dimension, COALESCE(AVG(confidence), 0) as avg_conf FROM persona_traits WHERE superseded = FALSE GROUP BY dimension ORDER BY avg_conf ASC LIMIT 1`).Scan(&st.LowestDim, &st.LowestConf)
	s.readerDB().QueryRow(`SELECT dimension, COALESCE(AVG(confidence), 0) as avg_conf FROM persona_traits WHERE superseded = FALSE GROUP BY dimension ORDER BY avg_conf DESC LIMIT 1`).Scan(&st.HighestDim, &st.HighestConf)
	return st, nil
}

// SessionsWithoutFlavor returns session IDs that have active learnings with empty session_flavor.
// Limit 0 means no limit.
func (s *Store) SessionsWithoutFlavor(limit int) ([]string, error) {
	query := `SELECT DISTINCT session_id FROM learnings
		WHERE superseded_by IS NULL
		AND session_id != ''
		AND (session_flavor IS NULL OR session_flavor = '')
		AND session_id NOT IN (
			SELECT DISTINCT session_id FROM learnings
			WHERE superseded_by IS NULL AND session_flavor != '' AND session_flavor IS NOT NULL
		)
		ORDER BY created_at DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.readerDB().Query(query)
	if err != nil {
		return nil, fmt.Errorf("sessions without flavor: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpdateSessionFlavor sets session_flavor on all active learnings for a session.
func (s *Store) UpdateSessionFlavor(sessionID, flavor string) (int64, error) {
	result, err := s.db.Exec(`UPDATE learnings SET session_flavor = ?
		WHERE session_id = ? AND superseded_by IS NULL`, flavor, sessionID)
	if err != nil {
		return 0, fmt.Errorf("update session flavor: %w", err)
	}
	return result.RowsAffected()
}

// UpdateSessionFlavorAndIntensity sets both session_flavor and emotional_intensity on all active learnings for a session.
func (s *Store) UpdateSessionFlavorAndIntensity(sessionID, flavor string, intensity float64) (int64, error) {
	result, err := s.db.Exec(`UPDATE learnings SET session_flavor = ?, emotional_intensity = ?
		WHERE session_id = ? AND superseded_by IS NULL`, flavor, intensity, sessionID)
	if err != nil {
		return 0, fmt.Errorf("update session flavor+intensity: %w", err)
	}
	return result.RowsAffected()
}

// AllSessionIDs returns all distinct session IDs with active learnings, ordered by created_at DESC.
func (s *Store) AllSessionIDs(limit int) ([]string, error) {
	query := `SELECT DISTINCT session_id FROM learnings
		WHERE superseded_by IS NULL AND session_id != ''
		ORDER BY created_at DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.readerDB().Query(query)
	if err != nil {
		return nil, fmt.Errorf("all session ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetMilestoneNarratives returns the most impactful narrative learnings across all projects,
// scored by emotional_intensity * (1 + use_count * 0.2). Returns one per session.
func (s *Store) GetMilestoneNarratives(limit int) ([]models.Learning, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT COALESCE(session_flavor, ''), COALESCE(emotional_intensity, 0.0),
		created_at, session_id
		FROM learnings
		WHERE category = 'narrative'
		AND superseded_by IS NULL
		AND session_flavor != ''
		AND emotional_intensity > 0.5
		GROUP BY session_id
		ORDER BY emotional_intensity * (1 + COALESCE(use_count, 0) * 0.2) DESC
		LIMIT ?`
	rows, err := s.readerDB().Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("get milestone narratives: %w", err)
	}
	defer rows.Close()

	var results []models.Learning
	for rows.Next() {
		var l models.Learning
		var createdAt string
		if err := rows.Scan(&l.SessionFlavor, &l.EmotionalIntensity, &createdAt, &l.SessionID); err != nil {
			return nil, err
		}
		l.CreatedAt = parseTime(createdAt)
		results = append(results, l)
	}
	return results, rows.Err()
}
