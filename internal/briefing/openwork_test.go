package briefing

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestCalcAbsenceHours(t *testing.T) {
	tests := []struct {
		name     string
		sessions []models.Session
		wantMin  float64
		wantMax  float64
	}{
		{"no sessions", nil, 0, 0},
		{"empty StartedAt", []models.Session{{StartedAt: time.Time{}}}, 0, 0},
		{"1 hour ago", []models.Session{{StartedAt: time.Now().Add(-1 * time.Hour)}}, 0.9, 1.1},
		{"5 hours ago", []models.Session{{StartedAt: time.Now().Add(-5 * time.Hour)}}, 4.9, 5.1},
		{"48 hours ago", []models.Session{{StartedAt: time.Now().Add(-48 * time.Hour)}}, 47.9, 48.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calcAbsenceHours(tt.sessions)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("calcAbsenceHours() = %f, want between %f and %f", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestFormatAbsence(t *testing.T) {
	tests := []struct {
		hours float64
		want  string
	}{
		{5, "Du warst 5 Stunden nicht da."},
		{23, "Du warst 23 Stunden nicht da."},
		{25, "Du warst seit gestern nicht da."},
		{47, "Du warst seit gestern nicht da."},
		{49, "Du warst 2 Tage nicht da."},
		{120, "Du warst 5 Tage nicht da."},
	}
	for _, tt := range tests {
		got := formatAbsence(tt.hours)
		if got != tt.want {
			t.Errorf("formatAbsence(%v) = %q, want %q", tt.hours, got, tt.want)
		}
	}
}

func TestFormatDeadline(t *testing.T) {
	tests := []struct {
		trigger string
		want    string
	}{
		{"deadline:2026-03-28", "Sa 28.03."},
		{"deadline:2026-03-24", "Di 24.03."},
		{"deadline:2026-01-01", "Do 01.01."},
		{"deadline:invalid", ""},
		{"", ""},
		{"Freitext trigger", ""},
	}
	for _, tt := range tests {
		got := formatDeadline(tt.trigger)
		if got != tt.want {
			t.Errorf("formatDeadline(%q) = %q, want %q", tt.trigger, got, tt.want)
		}
	}
}

func TestRenderOpenWork_NormalMode(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	items := []models.Learning{
		{Content: "Task A", Importance: 2},
		{Content: "Task B", Importance: 4},
	}
	result := g.renderOpenWork(s, items, 2.0, nil) // < 4h = normal mode
	if strings.Contains(result, s.UserReminder) {
		t.Error("should NOT use reminder heading when absence < 4h")
	}
	if !strings.Contains(result, s.OpenWork) {
		t.Error("should use normal OpenWork heading when absence < 4h")
	}
	if !strings.Contains(result, "Task A") || !strings.Contains(result, "Task B") {
		t.Error("should contain both tasks in normal mode (no importance filter)")
	}
}

func TestRenderOpenWork_ReminderMode(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	items := []models.Learning{
		{Content: "Low importance", Importance: 2},
		{Content: "High importance", Importance: 4},
	}
	result := g.renderOpenWork(s, items, 5.0, nil) // >= 4h = reminder mode
	if !strings.Contains(result, s.UserReminder) {
		t.Error("should use reminder heading when absence >= 4h")
	}
	if strings.Contains(result, "Low importance") {
		t.Error("should filter out importance < 3 in reminder mode")
	}
	if !strings.Contains(result, "High importance") {
		t.Error("should keep importance >= 3 in reminder mode")
	}
	if !strings.Contains(result, "Du warst") {
		t.Error("should contain absence note in reminder mode")
	}
	if !strings.Contains(result, "Bring up these points") {
		t.Error("should contain Claude instruction in reminder mode")
	}
}

func TestRenderOpenWork_DeadlineDisplay(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	items := []models.Learning{
		{Content: "Billing Migration", TriggerRule: "deadline:2026-03-28", Importance: 4},
		{Content: "Normal task", Importance: 3},
	}
	result := g.renderOpenWork(s, items, 0, nil)
	if !strings.Contains(result, "Deadline: Sa 28.03.") {
		t.Errorf("should show deadline suffix, got: %s", result)
	}
	// Normal task should NOT have deadline
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Normal task") && strings.Contains(line, "Deadline") {
			t.Error("normal task should not have deadline suffix")
		}
	}
}

func TestRenderOpenWork_LimitIncreasedForLongAbsence(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	var items []models.Learning
	for i := 0; i < 10; i++ {
		items = append(items, models.Learning{Content: "Task", Importance: 4})
	}

	// Short absence: max 3
	resultShort := g.renderOpenWork(s, items, 2.0, nil)
	countShort := strings.Count(resultShort, "- Task")

	// Long absence (>24h): max 5
	resultLong := g.renderOpenWork(s, items, 25.0, nil)
	countLong := strings.Count(resultLong, "- Task")

	if countShort != 3 {
		t.Errorf("short absence should show 3 items, got %d", countShort)
	}
	if countLong != 5 {
		t.Errorf("long absence should show 5 items, got %d", countLong)
	}
}

func TestRenderOpenWork_Empty(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	result := g.renderOpenWork(s, nil, 10.0, nil)
	if result != "" {
		t.Errorf("empty items should return empty string, got %q", result)
	}
}

func TestRenderOpenWork_TriggerReasonOverridesStaticDeadline(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	items := []models.Learning{
		{ID: 42, Content: "Billing Migration", TriggerRule: "deadline:2026-03-28", Importance: 4},
	}
	reasons := map[int64]string{42: "Deadline morgen"}
	result := g.renderOpenWork(s, items, 0, reasons)
	if !strings.Contains(result, "Deadline morgen") {
		t.Errorf("should show urgency reason, got: %s", result)
	}
	if strings.Contains(result, "Sa 28.03.") {
		t.Error("urgency reason should override static date format")
	}
}

func TestRenderOpenWork_AllFilteredInReminderMode(t *testing.T) {
	s := DefaultStrings()
	g := &Generator{strings: s}
	items := []models.Learning{
		{Content: "Low A", Importance: 1},
		{Content: "Low B", Importance: 2},
	}
	result := g.renderOpenWork(s, items, 5.0, nil) // reminder mode, but all low importance
	if result != "" {
		t.Errorf("should return empty when all items filtered, got %q", result)
	}
}
