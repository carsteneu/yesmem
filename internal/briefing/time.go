package briefing

import (
	"fmt"
	"time"
)

// relativeTime formats a timestamp as a human-readable German relative time.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("vor %d Min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("vor %dh", int(d.Hours()))
	case d < 48*time.Hour:
		return "gestern"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("vor %d Tagen", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("vor %d Wo", int(d.Hours()/24/7))
	default:
		return fmt.Sprintf("vor %d Mo", int(d.Hours()/24/30))
	}
}
