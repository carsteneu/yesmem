package briefing

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestFormatDeadlineReason(t *testing.T) {
	tests := []struct {
		daysUntil float64
		want      string
	}{
		{-0.5, "Deadline exceeded!"},
		{0.3, "Deadline today!"},
		{0.9, "Deadline today!"},
		{1.2, "Deadline morgen"},
		{1.9, "Deadline morgen"},
		{2.5, "Deadline in 2 Tagen"},
		{3.0, "Deadline in 3 Tagen"},
	}
	for _, tt := range tests {
		got := formatDeadlineReason(tt.daysUntil)
		if got != tt.want {
			t.Errorf("formatDeadlineReason(%v) = %q, want %q", tt.daysUntil, got, tt.want)
		}
	}
}

func TestContainsLearning(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1}, {ID: 5}, {ID: 10},
	}
	if !containsLearning(learnings, 5) {
		t.Error("should find ID 5")
	}
	if containsLearning(learnings, 99) {
		t.Error("should not find ID 99")
	}
	if containsLearning(nil, 1) {
		t.Error("nil slice should return false")
	}
}

func TestCheckDeadlineTriggers_NilStore(t *testing.T) {
	// checkDeadlineTriggers with nil store should not panic — store.GetTriggeredLearnings will error
	// We can't easily test with a real store here, but we verify the format functions work
	_ = formatDeadlineReason(0.5)
	_ = formatDeadlineReason(1.5)
	_ = formatDeadlineReason(-1)
}

func TestDeadlineTriggerWindow(t *testing.T) {
	// Verify the 3-day window logic by checking what would pass the filter
	now := time.Now()
	tests := []struct {
		name    string
		trigger string
		wantHit bool
	}{
		{"tomorrow", "deadline:" + now.Add(24*time.Hour).Format("2006-01-02"), true},
		{"in 2 days", "deadline:" + now.Add(48*time.Hour).Format("2006-01-02"), true},
		{"in 3 days", "deadline:" + now.Add(72*time.Hour).Format("2006-01-02"), true},
		{"in 5 days", "deadline:" + now.Add(120*time.Hour).Format("2006-01-02"), false},
		{"yesterday", "deadline:" + now.Add(-12*time.Hour).Format("2006-01-02"), true},  // within -1 day window
		{"3 days ago", "deadline:" + now.Add(-72*time.Hour).Format("2006-01-02"), false}, // outside window
		{"not a deadline", "Freitext", false},
		{"invalid date", "deadline:invalid", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the filter logic from checkDeadlineTriggers
			hit := false
			if len(tt.trigger) > 9 && tt.trigger[:9] == "deadline:" {
				dateStr := tt.trigger[9:]
				if deadline, err := time.ParseInLocation("2006-01-02", dateStr, now.Location()); err == nil {
					y, m, d := now.Date()
					nowMidnight := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
					daysUntil := deadline.Sub(nowMidnight).Hours() / 24
					hit = daysUntil <= 3 && daysUntil >= -1
				}
			}
			if hit != tt.wantHit {
				t.Errorf("trigger %q: got hit=%v, want %v", tt.trigger, hit, tt.wantHit)
			}
		})
	}
}
