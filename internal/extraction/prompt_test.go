package extraction

import (
	"strings"
	"testing"
	"time"
)

func TestBuildExtractionSystemPrompt_ContainsDate(t *testing.T) {
	prompt := BuildExtractionSystemPrompt()
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(prompt, today) {
		t.Errorf("prompt should contain today's date %s", today)
	}
}

func TestBuildExtractionSystemPrompt_ContainsWeekday(t *testing.T) {
	prompt := BuildExtractionSystemPrompt()
	weekday := germanWeekday(time.Now().Weekday())
	if !strings.Contains(prompt, weekday) {
		t.Errorf("prompt should contain German weekday %s", weekday)
	}
}

func TestBuildExtractionSystemPrompt_ContainsDeadlineExample(t *testing.T) {
	prompt := BuildExtractionSystemPrompt()
	if !strings.Contains(prompt, "deadline:") {
		t.Error("prompt should contain deadline trigger format")
	}
}

func TestGermanWeekday(t *testing.T) {
	tests := []struct {
		day  time.Weekday
		want string
	}{
		{time.Sunday, "Sonntag"},
		{time.Monday, "Montag"},
		{time.Tuesday, "Dienstag"},
		{time.Wednesday, "Mittwoch"},
		{time.Thursday, "Donnerstag"},
		{time.Friday, "Freitag"},
		{time.Saturday, "Samstag"},
	}
	for _, tt := range tests {
		got := germanWeekday(tt.day)
		if got != tt.want {
			t.Errorf("germanWeekday(%v) = %q, want %q", tt.day, got, tt.want)
		}
	}
}

func TestParseDeadlineTrigger(t *testing.T) {
	tests := []struct {
		trigger  string
		wantOK   bool
		wantYear int
		wantMon  time.Month
		wantDay  int
	}{
		{"deadline:2026-03-28", true, 2026, time.March, 29},  // end of day = start of 29th
		{"deadline:2026-12-01", true, 2026, time.December, 2}, // end of day = start of 2nd
		{"deadline:invalid", false, 0, 0, 0},
		{"deadline:", false, 0, 0, 0},
		{"session_count:5", false, 0, 0, 0},
		{"", false, 0, 0, 0},
		{"Wenn der User billing/ öffnet", false, 0, 0, 0},
	}
	for _, tt := range tests {
		expires := ParseDeadlineExpiry(tt.trigger)
		if tt.wantOK {
			if expires == nil {
				t.Errorf("ParseDeadlineExpiry(%q) = nil, want non-nil", tt.trigger)
				continue
			}
			if expires.Year() != tt.wantYear || expires.Month() != tt.wantMon || expires.Day() != tt.wantDay {
				t.Errorf("ParseDeadlineExpiry(%q) = %v, want %d-%02d-%02d", tt.trigger, expires, tt.wantYear, tt.wantMon, tt.wantDay)
			}
		} else {
			if expires != nil {
				t.Errorf("ParseDeadlineExpiry(%q) = %v, want nil", tt.trigger, expires)
			}
		}
	}
}

func TestParseDeadlineExpiry_EndOfDay(t *testing.T) {
	expires := ParseDeadlineExpiry("deadline:2026-03-28")
	if expires == nil {
		t.Fatal("expected non-nil")
	}
	// Should be end of deadline day (start of next day)
	if expires.Day() != 29 {
		t.Errorf("expected day 29 (end of 28), got %d", expires.Day())
	}
}
