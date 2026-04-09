package models

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SourceAgentClaude = "claude"
	SourceAgentCodex  = "codex"
)

// Session represents a Claude Code chat session.
type Session struct {
	ID              string    `json:"id"`
	Project         string    `json:"project"`
	ProjectShort    string    `json:"project_short"`
	GitBranch       string    `json:"git_branch,omitempty"`
	FirstMessage    string    `json:"first_message,omitempty"`
	MessageCount    int       `json:"message_count"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at,omitempty"`
	JSONLPath       string    `json:"jsonl_path"`
	JSONLSize       int64     `json:"jsonl_size"`
	IndexedAt       time.Time `json:"indexed_at"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	AgentType       string    `json:"agent_type,omitempty"`
	SourceAgent     string    `json:"source_agent,omitempty"`
	ExtractedAt     time.Time `json:"extracted_at,omitempty"`
	NarrativeAt     time.Time `json:"narrative_at,omitempty"`
}

// IsSubagent returns true if this session is a subagent of another session.
func (s *Session) IsSubagent() bool {
	return s.ParentSessionID != ""
}

// Message represents a single message extracted from a session.
type Message struct {
	ID          int64     `json:"id"`
	SessionID   string    `json:"session_id"`
	SourceAgent string    `json:"source_agent,omitempty"`
	Role        string    `json:"role"`         // user, assistant
	MessageType string    `json:"message_type"` // text, tool_use, tool_result, thinking, bash_output
	Content     string    `json:"content,omitempty"`
	ContentBlob []byte    `json:"content_blob,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	FilePath    string    `json:"file_path,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	Sequence    int       `json:"sequence"`
}

// Learning represents an extracted piece of knowledge.
type Learning struct {
	ID              int64      `json:"id"`
	SessionID       string     `json:"session_id,omitempty"`
	Category        string     `json:"category"`
	Content         string     `json:"content"`
	Project         string     `json:"project,omitempty"`
	Confidence      float64    `json:"confidence"`
	SupersededBy    *int64     `json:"superseded_by,omitempty"`
	SupersedeReason string     `json:"supersede_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	ModelUsed       string     `json:"model_used"`
	Source          string     `json:"source,omitempty"` // user_stated, claude_suggested, agreed_upon, llm_extracted
	SourceAgent     string     `json:"source_agent,omitempty"`
	TargetAgent     string     `json:"target_agent,omitempty"`
	HitCount           int        `json:"hit_count"`
	FailCount          int        `json:"fail_count"`
	EmotionalIntensity float64    `json:"emotional_intensity,omitempty"`
	SessionFlavor      string     `json:"session_flavor,omitempty"`
	LastHitAt          *time.Time `json:"last_hit_at,omitempty"`
	ValidUntil         *time.Time `json:"valid_until,omitempty"`
	Supersedes         *int64     `json:"supersedes,omitempty"`
	Importance         int        `json:"importance"`
	SupersedeStatus    string     `json:"supersede_status,omitempty"`
	NoiseCount         int        `json:"noise_count"`
	MatchCount         int        `json:"match_count"`
	InjectCount        int        `json:"inject_count"`
	UseCount           int        `json:"use_count"`
	SaveCount          int        `json:"save_count"`
	Stability          float64    `json:"stability"`
	Score              float64    `json:"score,omitempty"`

	// V2 fields (empty for legacy learnings)
	Context            string   `json:"context,omitempty"`
	Domain             string   `json:"domain,omitempty"`
	TriggerRule        string   `json:"trigger_rule,omitempty"`
	EmbeddingText      string   `json:"embedding_text,omitempty"`
	Entities           []string `json:"entities,omitempty"`
	Actions            []string `json:"actions,omitempty"`
	Keywords           []string `json:"keywords,omitempty"`
	AnticipatedQueries []string `json:"anticipated_queries,omitempty"`

	// Content dedup (v0.22)
	ContentHash string `json:"content_hash,omitempty"`

	// Document ingest tracking (v0.21)
	SourceFile  string `json:"source_file,omitempty"`
	SourceHash  string `json:"source_hash,omitempty"`
	DocChunkRef int64  `json:"doc_chunk_ref,omitempty"`

	// Multi-agent (v0.31)
	AgentRole string `json:"agent_role,omitempty"`
	DialogID  int64  `json:"dialog_id,omitempty"`

	// Task classification (v0.34) — only meaningful for category="unfinished"
	TaskType string `json:"task_type,omitempty"` // task, idea, blocked, stale

	// Turn-based decay (v0.37)
	TurnsAtCreation int64 `json:"turns_at_creation,omitempty"`

	// Learning lineage (v0.53) — message index range this learning was extracted from
	SourceMsgFrom int `json:"source_msg_from,omitempty"`
	SourceMsgTo   int `json:"source_msg_to,omitempty"`

	// Fork Reflection impact scoring (v0.47)
	ImpactScore float64 `json:"impact_score"`
	ImpactCount int     `json:"impact_count"`

	// Computed (not persisted) — loaded from sessions table during enrichment
	SessionFixationRatio float64 `json:"session_fixation_ratio,omitempty"`
	CurrentTurnCount     int64   `json:"current_turn_count,omitempty"` // set by caller before scoring
}

// IsActive returns true if this learning has not been superseded and has not expired.
func (l *Learning) IsActive() bool {
	if l.SupersededBy != nil {
		return false
	}
	if l.ExpiresAt != nil && l.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// IsV2 returns true if this learning has structured V2 metadata.
func (l *Learning) IsV2() bool {
	return len(l.Entities) > 0 || len(l.Actions) > 0 || l.TriggerRule != ""
}

// BuildEmbeddingText generates enriched text for embedding that includes all V2 metadata.
func (l *Learning) BuildEmbeddingText() string {
	var sb strings.Builder
	proj := l.Project
	if proj == "" {
		proj = "general"
	}
	domain := l.Domain
	if domain == "" {
		domain = "code"
	}
	sb.WriteString(fmt.Sprintf("[%s %s %s] ", proj, domain, l.Category))
	sb.WriteString(l.Content)
	if l.Context != "" {
		sb.WriteString(" | Context: ")
		sb.WriteString(l.Context)
	}
	if l.TriggerRule != "" {
		sb.WriteString(" | Trigger: ")
		sb.WriteString(l.TriggerRule)
	}
	if len(l.Entities) > 0 {
		sb.WriteString(" | Entities: ")
		sb.WriteString(strings.Join(l.Entities, ", "))
	}
	if len(l.Actions) > 0 {
		sb.WriteString(" | Actions: ")
		sb.WriteString(strings.Join(l.Actions, ", "))
	}
	if len(l.AnticipatedQueries) > 0 {
		sb.WriteString(" | Queries: ")
		sb.WriteString(strings.Join(l.AnticipatedQueries, "; "))
	}
	return sb.String()
}

// Association represents an edge in the association graph.
type Association struct {
	SourceType   string  `json:"source_type"` // session, file, command, project, concept, learning
	SourceID     string  `json:"source_id"`
	TargetType   string  `json:"target_type"`
	TargetID     string  `json:"target_id"`
	Weight       float64 `json:"weight"`
	RelationType string  `json:"relation_type"` // related, supports, contradicts, depends_on
}

// ProjectProfile represents a living project portrait.
type ProjectProfile struct {
	Project      string    `json:"project"`
	ProfileText  string    `json:"profile_text"`
	GeneratedAt  time.Time `json:"generated_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SessionCount int       `json:"session_count"`
	ModelUsed    string    `json:"model_used"`
}

// StrategicContext represents a long-term strategic goal or context.
type StrategicContext struct {
	ID           int64     `json:"id"`
	Scope        string    `json:"scope"` // "global" or project name
	Context      string    `json:"context"`
	Source       string    `json:"source"` // user_stated, llm_inferred, self_noted
	CreatedAt    time.Time `json:"created_at"`
	SupersededBy *int64    `json:"superseded_by,omitempty"`
	Active       bool      `json:"active"`
}

// FileCoverage tracks which files Claude has worked with.
type FileCoverage struct {
	Project        string `json:"project"`
	FilePath       string `json:"file_path"`
	Directory      string `json:"directory"`
	SessionCount   int    `json:"session_count"`
	LastTouched    string `json:"last_touched"`
	OperationTypes string `json:"operation_types"` // comma-separated: read, write, grep, bash
}

// SelfFeedback tracks Claude's performance.
type SelfFeedback struct {
	ID           int64     `json:"id"`
	SessionID    string    `json:"session_id"`
	FeedbackType string    `json:"feedback_type"` // correction, rejection, praise, clarification
	Description  string    `json:"description"`
	Pattern      string    `json:"pattern,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// PersonaTrait represents a single user trait with confidence scoring.
type PersonaTrait struct {
	ID            int64     `json:"id"`
	UserID        string    `json:"user_id"`
	Dimension     string    `json:"dimension"`      // communication, workflow, expertise, context, boundaries, learning_style
	TraitKey      string    `json:"trait_key"`
	TraitValue    string    `json:"trait_value"`
	Confidence    float64   `json:"confidence"`
	Source        string    `json:"source"`          // auto_extracted, user_override
	EvidenceCount int       `json:"evidence_count"`
	FirstSeen     time.Time `json:"first_seen"`
	UpdatedAt     time.Time `json:"updated_at"`
	Superseded    bool      `json:"superseded"`
}

// PersonaDirective represents a cached synthesized persona prompt.
type PersonaDirective struct {
	ID          int64     `json:"id"`
	UserID      string    `json:"user_id"`
	Directive   string    `json:"directive"`
	TraitsHash  string    `json:"traits_hash"`
	GeneratedAt time.Time `json:"generated_at"`
	ModelUsed   string    `json:"model_used"`
}

// KnowledgeGap represents a topic where no learnings were found.
type KnowledgeGap struct {
	ID         int64     `json:"id"`
	Topic      string    `json:"topic"`
	Project    string    `json:"project,omitempty"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	HitCount   int       `json:"hit_count"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy *int64    `json:"resolved_by,omitempty"`
}

// SearchResult represents a search hit from Bleve.
type SearchResult struct {
	SessionID       string  `json:"session_id"`
	Project         string  `json:"project"`
	Timestamp       string  `json:"timestamp"`
	Snippet         string  `json:"snippet"`
	Score           float64 `json:"score"`
	Role            string  `json:"role"`
	MessageType     string  `json:"message_type"`
	AgentType       string  `json:"agent_type,omitempty"`
	ParentSessionID string  `json:"parent_session_id,omitempty"`
	SourceAgent     string  `json:"source_agent,omitempty"`
}

// IndexState tracks whether a JSONL file has been indexed.
type IndexState struct {
	JSONLPath string    `json:"jsonl_path"`
	FileSize  int64     `json:"file_size"`
	FileMtime time.Time `json:"file_mtime"`
	IndexedAt time.Time `json:"indexed_at"`
}

// ProjectShortFromPath extracts the last path segment as a short project name.
func ProjectShortFromPath(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	return filepath.Base(path)
}

// NormalizeSourceAgent canonicalizes session/message provenance labels.
// Empty values map to Claude for backward compatibility with legacy rows.
func NormalizeSourceAgent(sourceAgent string) string {
	sourceAgent = strings.ToLower(strings.TrimSpace(sourceAgent))
	if sourceAgent == "" {
		return SourceAgentClaude
	}
	return sourceAgent
}

// NormalizeTargetAgent canonicalizes agent-target labels while preserving
// empty values as "applies to all agents".
func NormalizeTargetAgent(targetAgent string) string {
	targetAgent = strings.ToLower(strings.TrimSpace(targetAgent))
	if targetAgent == "" {
		return ""
	}
	return NormalizeSourceAgent(targetAgent)
}

// DetectSourceAgentFromPath infers the originating agent from a session path.
// Unknown layouts fall back to Claude's historic .claude/projects scheme.
func DetectSourceAgentFromPath(path string) string {
	clean := filepath.Clean(path)
	codexMarker := string(os.PathSeparator) + ".codex" + string(os.PathSeparator) + "sessions"
	if strings.Contains(clean, codexMarker+string(os.PathSeparator)) || strings.HasSuffix(clean, codexMarker) {
		return SourceAgentCodex
	}
	return SourceAgentClaude
}

// NormalizeSessionID namespaces source-specific session IDs when needed.
// Claude sessions keep their historic IDs for backward compatibility.
func NormalizeSessionID(sourceAgent, id string) string {
	sourceAgent = NormalizeSourceAgent(sourceAgent)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if sourceAgent == SourceAgentClaude {
		return id
	}
	prefix := sourceAgent + ":"
	if strings.HasPrefix(id, prefix) {
		return id
	}
	return prefix + id
}

var validMessageTypes = map[string]bool{
	"text": true, "tool_use": true, "tool_result": true,
	"thinking": true, "bash_output": true,
}

// IsValidMessageType checks if a message type string is valid.
func IsValidMessageType(mt string) bool {
	return validMessageTypes[mt]
}

var validCategories = map[string]bool{
	"explicit_teaching": true, "gotcha": true, "decision": true,
	"pattern": true, "preference": true, "unfinished": true,
	"relationship": true, "strategic": true, "pivot_moment": true,
}

// IsValidCategory checks if a learning category string is valid.
func IsValidCategory(cat string) bool {
	return validCategories[cat]
}
