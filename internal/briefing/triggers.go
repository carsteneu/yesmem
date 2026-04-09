package briefing

import (
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// TriggerMatch is a learning whose trigger_rule fired.
type TriggerMatch struct {
	Learning models.Learning
	Reason   string // e.g. "Deadline morgen", "Deadline in 2 Tagen"
}

// checkDeadlineTriggers finds learnings with approaching deadlines (within 3 days).
// Updates last_hit_at on matched learnings to prevent re-triggering within 12h.
// Returns at most 3 matches.
func checkDeadlineTriggers(store *storage.Store, project string) []TriggerMatch {
	learnings, err := store.GetTriggeredLearnings(project)
	if err != nil || len(learnings) == 0 {
		return nil
	}

	now := time.Now()
	var matches []TriggerMatch
	var hitIDs []int64

	for _, l := range learnings {
		if !strings.HasPrefix(l.TriggerRule, "deadline:") {
			continue
		}
		dateStr := strings.TrimPrefix(l.TriggerRule, "deadline:")
		deadline, err := time.ParseInLocation("2006-01-02", dateStr, now.Location())
		if err != nil {
			continue
		}

		// Calendar-day comparison: truncate now to local midnight so daysUntil is an integer
		y, m, d := now.Date()
		nowMidnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
		daysUntil := deadline.Sub(nowMidnight).Hours() / 24
		if daysUntil > 3 || daysUntil < -1 {
			continue // only trigger within 3 days before and 1 day after deadline
		}

		reason := formatDeadlineReason(daysUntil)
		matches = append(matches, TriggerMatch{Learning: l, Reason: reason})
		hitIDs = append(hitIDs, l.ID)

		if len(matches) >= 3 {
			break
		}
	}

	// Update last_hit_at for cooldown (don't bump counters — just set timestamp)
	if len(hitIDs) > 0 {
		store.UpdateLastHitAt(hitIDs)
	}

	return matches
}

// formatDeadlineReason returns a human-readable German deadline proximity note.
func formatDeadlineReason(daysUntil float64) string {
	switch {
	case daysUntil < 0:
		return "Deadline exceeded!"
	case daysUntil < 1:
		return "Deadline today!"
	case daysUntil < 2:
		return "Deadline morgen"
	default:
		return fmt.Sprintf("Deadline in %d Tagen", int(daysUntil))
	}
}

// containsLearning checks if a learning ID is already in the slice.
func containsLearning(learnings []models.Learning, id int64) bool {
	for _, l := range learnings {
		if l.ID == id {
			return true
		}
	}
	return false
}
