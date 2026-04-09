package briefing

import (
	"strings"
	"testing"
	"time"
)

func TestComputeTimeGaps_OneGap(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		// 14-day gap
		time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	gaps := computeTimeGaps(times)
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d: %v", len(gaps), gaps)
	}
	if !strings.Contains(gaps[0], "14 Tage") {
		t.Errorf("expected '14 Tage', got %q", gaps[0])
	}
	if !strings.Contains(gaps[0], "Januar") {
		t.Errorf("expected 'Januar', got %q", gaps[0])
	}
}

func TestComputeTimeGaps_NoGaps(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
	}

	gaps := computeTimeGaps(times)
	if len(gaps) != 0 {
		t.Errorf("expected 0 gaps, got %d: %v", len(gaps), gaps)
	}
}

func TestComputeTimeGaps_Empty(t *testing.T) {
	gaps := computeTimeGaps(nil)
	if gaps != nil {
		t.Errorf("expected nil, got %v", gaps)
	}
}

func TestComputeTimeGaps_SingleSession(t *testing.T) {
	gaps := computeTimeGaps([]time.Time{time.Now()})
	if gaps != nil {
		t.Errorf("expected nil, got %v", gaps)
	}
}

func TestComputeTimeGaps_MultipleGaps(t *testing.T) {
	times := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC), // 19-day gap
		time.Date(2026, 1, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), // 39-day gap
	}

	gaps := computeTimeGaps(times)
	if len(gaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d: %v", len(gaps), gaps)
	}
}

func TestComputeTimeGaps_UnsortedInput(t *testing.T) {
	// Input not sorted — function should sort internally
	times := []time.Time{
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}

	gaps := computeTimeGaps(times)
	// Gap between Jan 2 and Mar 1 = ~58 days
	if len(gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d: %v", len(gaps), gaps)
	}
}
