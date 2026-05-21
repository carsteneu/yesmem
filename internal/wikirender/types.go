package wikirender

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/storage"
)

type RenderConfig struct {
	Project   string
	OutputDir string
	Store     *storage.Store
	ScanJSON  []byte
	CodeGraph *codescan.CodeGraph
	Quiet     bool
}

type Result struct {
	Project        string
	Learnings      int
	Quarantined    int
	Topics         int
	Files          int
	Sessions       int
	Contradictions int
	BuiltAt        string
	DurationMs     int64
	LoadMs         int64
	ComputeMs      int64
	WriteMs        int64
	TplMs          int64
	SkippedWrites  int
}

type Learning struct {
	ID                int64
	Content           string
	Category          string
	Source            string
	CreatedAt         string
	UseCount          int
	QuarantinedAt     string
	Importance        float64
	Stability         float64
	Confidence        float64
	TriggerRule       string
	Context           string
	Domain            string
	TaskType          string
	ModelUsed         string
	OriginTool        string
	AgentRole         string
	SessionID         string
	SourceMsgFrom     int
	SourceMsgTo       int
	DialogID          string
	Supersedes        int64
	SupersedeReason   string
	HitCount          int
	InjectCount       int
	SaveCount         int
	LastHitAt         string
	EmbeddingStatus   string
	TurnsAtCreation   int
	Project           string
	SupersedesContent string

	Entities []string
	Actions  []string
	Keywords []string
}

type Topic struct {
	Name      string
	Learnings []Learning
	CoTopics  []CoTopic
}

type CoTopic struct {
	Name   string
	Shared int
}

type RelatedLearning struct {
	ID       int64
	Snippet  string
	Source   string
	Category string
	Overlap  int
}

type FilePage struct {
	Path           string
	Directory      string
	SessionCount   int
	LastTouched    string
	OperationTypes string
	Learnings      []Learning
	Sessions       []SessionRef
	Code           *FileCode
	CoEdited       []CoEdit
	Package        string
	Imports        []string
	ImportedBy     []string
}

type FileCode struct {
	Language   string
	LOC        int
	IsTest     bool
	TestCount  int
	Signatures []string
	Imports    []string
}

type CoEdit struct {
	Path  string
	Count int
}

type SessionRef struct {
	ID        string
	StartedAt string
	Messages  int
}

type PackagePage struct {
	Name       string
	Intent     string
	FileCount  int
	TotalLOC   int
	Language   string
	TestCount  int
	Gotchas    int
	TODOs      int
	LastEdited string
	Files      []FilePage
	Learnings  []Learning
	Symbols    []string
	Imports    []string
	ImportedBy []string
	CoEdited   []CoEdit
	Sessions   []SessionRef
}

type Session struct {
	ID           string
	ShortID      string
	StartedAt    string
	EndedAt      string
	MessageCount int
	Learnings    []Learning
}

type Contradiction struct {
	ID          int64
	LearningIDs []int64
	Description string
	CreatedAt   string
}

func (c *RenderConfig) Validate() error {
	if c.Project == "" {
		return fmt.Errorf("Project is required")
	}
	if c.OutputDir == "" {
		return fmt.Errorf("OutputDir is required")
	}
	if c.Store == nil {
		return fmt.Errorf("Store is required")
	}
	return nil
}
