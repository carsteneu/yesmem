package briefing

import "github.com/carsteneu/yesmem/internal/models"

// fadeThreshold is the minimum score for a learning to appear in the briefing.
// Learnings below this are "faded" — still in DB and searchable, but not shown.
const fadeThreshold = 0.1

// filterFaded removes learnings with score below threshold.
// Unfinished and pivot_moment items are never faded.
func filterFaded(learnings []models.Learning, threshold float64) []models.Learning {
	if threshold <= 0 {
		return learnings
	}
	filtered := make([]models.Learning, 0, len(learnings))
	for _, l := range learnings {
		if l.Category == "unfinished" || l.Category == "pivot_moment" || l.Score >= threshold {
			filtered = append(filtered, l)
		}
	}
	return filtered
}
