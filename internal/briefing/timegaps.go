package briefing

import (
	"fmt"
	"sort"
	"time"
)

var germanMonths = []string{
	"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

// computeTimeGaps finds gaps > 7 days between sessions and returns
// German-formatted strings like "14 Tage im Februar".
func computeTimeGaps(startedAts []time.Time) []string {
	if len(startedAts) < 2 {
		return nil
	}

	sorted := make([]time.Time, len(startedAts))
	copy(sorted, startedAts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Before(sorted[j])
	})

	var gaps []string
	for i := 1; i < len(sorted); i++ {
		diff := sorted[i].Sub(sorted[i-1])
		days := int(diff.Hours() / 24)
		if days > 7 {
			month := germanMonths[sorted[i-1].Month()]
			gaps = append(gaps, fmt.Sprintf("%d Tage im %s", days, month))
		}
	}
	return gaps
}
