package briefing

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func makeLearning(content string, age time.Duration) models.Learning {
	return models.Learning{
		Content:   content,
		Category:  "gotcha",
		CreatedAt: time.Now().Add(-age),
	}
}

func TestDeduplicateExactMatch(t *testing.T) {
	learnings := []models.Learning{
		makeLearning("CGO_ENABLED=0 muss gesetzt sein", 1*time.Hour),
		makeLearning("CGO_ENABLED=0 muss gesetzt sein", 2*time.Hour),
	}
	result := Deduplicate(learnings, 0.4)
	if len(result) != 1 {
		t.Errorf("exact duplicates: got %d, want 1", len(result))
	}
}

func TestDeduplicateNearDuplicate(t *testing.T) {
	learnings := []models.Learning{
		makeLearning("CGO_ENABLED=0 muss gesetzt sein, da modernc.org/sqlite sonst CGo nutzt", 1*time.Hour),
		makeLearning("CGO_ENABLED=0 setzen weil modernc.org/sqlite pure Go ist", 2*time.Hour),
	}
	result := Deduplicate(learnings, 0.4)
	if len(result) != 1 {
		t.Errorf("near duplicates: got %d, want 1", len(result))
	}
}

func TestDeduplicateKeepsNewest(t *testing.T) {
	old := makeLearning("CGO_ENABLED=0 setzen", 48*time.Hour)
	new := makeLearning("CGO_ENABLED=0 muss gesetzt sein wegen modernc.org/sqlite", 1*time.Hour)
	learnings := []models.Learning{old, new}

	result := Deduplicate(learnings, 0.4)
	if len(result) != 1 {
		t.Fatalf("got %d results, want 1", len(result))
	}
	if result[0].Content != new.Content {
		t.Errorf("should keep newest, got %q", result[0].Content)
	}
}

func TestDeduplicateIndependent(t *testing.T) {
	learnings := []models.Learning{
		makeLearning("Docker braucht kein sudo auf diesem System", 1*time.Hour),
		makeLearning("CGO_ENABLED=0 setzen wegen modernc.org/sqlite", 2*time.Hour),
		makeLearning("TMPDIR=/tmp/claude-1000 muss gesetzt sein fuer go test", 3*time.Hour),
	}
	result := Deduplicate(learnings, 0.4)
	if len(result) != 3 {
		t.Errorf("independent items: got %d, want 3", len(result))
	}
}

func TestDeduplicateEmpty(t *testing.T) {
	result := Deduplicate(nil, 0.4)
	if result != nil {
		t.Errorf("nil input: got %v, want nil", result)
	}

	result = Deduplicate([]models.Learning{}, 0.4)
	if len(result) != 0 {
		t.Errorf("empty input: got %d, want 0", len(result))
	}
}

func TestDeduplicateSingle(t *testing.T) {
	learnings := []models.Learning{
		makeLearning("Einziges Learning", 1*time.Hour),
	}
	result := Deduplicate(learnings, 0.4)
	if len(result) != 1 {
		t.Errorf("single item: got %d, want 1", len(result))
	}
}

func TestDeduplicateMultipleDuplicateGroups(t *testing.T) {
	learnings := []models.Learning{
		// Group 1: CGO
		makeLearning("CGO_ENABLED=0 muss gesetzt sein", 1*time.Hour),
		makeLearning("CGO_ENABLED=0 setzen", 2*time.Hour),
		makeLearning("CGO_ENABLED=0 wegen modernc.org/sqlite", 3*time.Hour),
		// Group 2: TMPDIR
		makeLearning("TMPDIR=/tmp/claude-1000 muss gesetzt sein", 1*time.Hour),
		makeLearning("TMPDIR=/tmp/claude-1000 fuer go test setzen", 2*time.Hour),
		// Group 3: unique
		makeLearning("Docker braucht kein sudo", 1*time.Hour),
	}
	result := Deduplicate(learnings, 0.4)
	if len(result) != 3 {
		t.Errorf("3 groups: got %d, want 3", len(result))
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		want  int // expected token count
	}{
		{"CGO_ENABLED=0 muss gesetzt sein", 2}, // "cgo_enabled=0", "gesetzt" (muss+sein are stop-words)
		{"", 0},
		{"der die das ein eine", 0},     // all stop-words
		{"modernc.org/sqlite pure Go", 4}, // modernc, org, sqlite, pure ("go"=en stop-word)
	}
	for _, tt := range tests {
		tokens := normalize(tt.input)
		if len(tokens) != tt.want {
			t.Errorf("normalize(%q): got %d tokens %v, want %d", tt.input, len(tokens), tokens, tt.want)
		}
	}
}

func TestJaccard(t *testing.T) {
	tests := []struct {
		a, b []string
		min  float64
		max  float64
	}{
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}, 1.0, 1.0},
		{[]string{"a", "b"}, []string{"c", "d"}, 0.0, 0.0},
		{[]string{"a", "b", "c"}, []string{"b", "c", "d"}, 0.49, 0.51}, // 2/4 = 0.5
		{nil, []string{"a"}, 0.0, 0.0},
		{[]string{"a"}, nil, 0.0, 0.0},
	}
	for _, tt := range tests {
		got := jaccard(tt.a, tt.b)
		if got < tt.min || got > tt.max {
			t.Errorf("jaccard(%v, %v) = %f, want [%f, %f]", tt.a, tt.b, got, tt.min, tt.max)
		}
	}
}

func TestSimilarity(t *testing.T) {
	// Containment should catch "short inside long" cases
	short := []string{"cgo_enabled=0", "setzen", "modernc.org/sqlite", "pure", "go"}
	long := []string{"cgo_enabled=0", "gesetzt", "modernc.org/sqlite", "cgo", "nutzt"}
	sim := similarity(short, long)
	if sim < 0.3 {
		t.Errorf("similarity(%v, %v) = %f, want >= 0.3 (containment should help)", short, long, sim)
	}

	// Identical sets should give 1.0
	same := []string{"a", "b", "c"}
	if s := similarity(same, same); s != 1.0 {
		t.Errorf("identical sets: got %f, want 1.0", s)
	}

	// Disjoint sets should give 0.0
	if s := similarity([]string{"a"}, []string{"b"}); s != 0.0 {
		t.Errorf("disjoint sets: got %f, want 0.0", s)
	}
}

func TestDeduplicateDefaultThreshold(t *testing.T) {
	learnings := []models.Learning{
		makeLearning("CGO_ENABLED=0 muss gesetzt sein", 1*time.Hour),
		makeLearning("CGO_ENABLED=0 muss gesetzt sein", 2*time.Hour),
	}
	// threshold 0 should use default 0.4
	result := Deduplicate(learnings, 0)
	if len(result) != 1 {
		t.Errorf("default threshold: got %d, want 1", len(result))
	}
}
