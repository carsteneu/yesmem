package models

// LearningCluster represents a group of related learnings with a label and confidence score.
// Used by the Metamemory system to surface knowledge density in the briefing.
type LearningCluster struct {
	ID             int64   `json:"id"`
	Project        string  `json:"project"`
	Label          string  `json:"label"`
	LearningCount  int     `json:"learning_count"`
	AvgRecencyDays float64 `json:"avg_recency_days"`
	AvgHitCount    float64 `json:"avg_hit_count"`
	Confidence     float64 `json:"confidence"`
	LearningIDs    string  `json:"learning_ids"` // JSON array of learning IDs
}
