package extraction

import "testing"

func TestIsSubstanzlos(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"short", true},
		{"Dies ist ein valides Learning", false},
		{`{"key": "value"}`, true},
		{"```go\nfoo()```", true},
		{"Nur zwei", true},
		{"User bevorzugt Deutsch, locker und direkt", false},
		{"[{\"array\": true}]", true},
	}
	for _, tt := range tests {
		got := IsSubstanzlos(tt.input)
		if got != tt.want {
			t.Errorf("IsSubstanzlos(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestBigramJaccard(t *testing.T) {
	// Identical → 1.0
	if j := BigramJaccard("User bevorzugt Deutsch", "User bevorzugt Deutsch"); j != 1.0 {
		t.Errorf("identical: got %f", j)
	}
	// Near-duplicate — different word order shares some bigrams
	j := BigramJaccard("User bevorzugt Deutsch, locker", "Sprache: Deutsch, User bevorzugt locker")
	if j < 0.1 || j > 0.5 {
		t.Errorf("near-dup: got %f, expected between 0.1 and 0.5", j)
	}
	// Actual near-duplicate (same words, slight variation) → moderate-high
	j = BigramJaccard("User bevorzugt Deutsch locker", "User bevorzugt Deutsch und locker")
	if j < 0.3 {
		t.Errorf("close near-dup: got %f, expected > 0.3", j)
	}
	// Completely different → low
	j = BigramJaccard("Go ist schnell", "Python hat gute Libraries")
	if j > 0.2 {
		t.Errorf("different: got %f, expected < 0.2", j)
	}
	// Both empty
	if j := BigramJaccard("", ""); j != 1.0 {
		t.Errorf("both empty: got %f, want 1.0", j)
	}
	// One empty
	if j := BigramJaccard("some words here", ""); j != 0.0 {
		t.Errorf("one empty: got %f, want 0.0", j)
	}
}
