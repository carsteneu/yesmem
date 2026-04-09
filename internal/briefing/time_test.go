package briefing

import (
	"testing"
	"time"
)

func TestRelativeTime_Zero(t *testing.T) {
	got := relativeTime(time.Time{})
	if got != "?" {
		t.Errorf("expected '?' for zero time, got %q", got)
	}
}

func TestRelativeTime_Minutes(t *testing.T) {
	got := relativeTime(time.Now().Add(-15 * time.Minute))
	if got != "vor 15 Min" {
		t.Errorf("expected 'vor 15 Min', got %q", got)
	}
}

func TestRelativeTime_ZeroMinutes(t *testing.T) {
	got := relativeTime(time.Now().Add(-30 * time.Second))
	if got != "vor 0 Min" {
		t.Errorf("expected 'vor 0 Min', got %q", got)
	}
}

func TestRelativeTime_Hours(t *testing.T) {
	got := relativeTime(time.Now().Add(-3 * time.Hour))
	if got != "vor 3h" {
		t.Errorf("expected 'vor 3h', got %q", got)
	}
}

func TestRelativeTime_Yesterday(t *testing.T) {
	got := relativeTime(time.Now().Add(-30 * time.Hour))
	if got != "gestern" {
		t.Errorf("expected 'gestern', got %q", got)
	}
}

func TestRelativeTime_Days(t *testing.T) {
	got := relativeTime(time.Now().Add(-4 * 24 * time.Hour))
	if got != "vor 4 Tagen" {
		t.Errorf("expected 'vor 4 Tagen', got %q", got)
	}
}

func TestRelativeTime_Weeks(t *testing.T) {
	got := relativeTime(time.Now().Add(-14 * 24 * time.Hour))
	if got != "vor 2 Wo" {
		t.Errorf("expected 'vor 2 Wo', got %q", got)
	}
}

func TestRelativeTime_Months(t *testing.T) {
	got := relativeTime(time.Now().Add(-60 * 24 * time.Hour))
	if got != "vor 2 Mo" {
		t.Errorf("expected 'vor 2 Mo', got %q", got)
	}
}

func TestRelativeTime_Boundary_HourMinus1(t *testing.T) {
	got := relativeTime(time.Now().Add(-59 * time.Minute))
	if got != "vor 59 Min" {
		t.Errorf("expected 'vor 59 Min' at 59m boundary, got %q", got)
	}
}

func TestRelativeTime_Boundary_ExactHour(t *testing.T) {
	got := relativeTime(time.Now().Add(-60 * time.Minute))
	if got != "vor 1h" {
		t.Errorf("expected 'vor 1h' at exact hour boundary, got %q", got)
	}
}
