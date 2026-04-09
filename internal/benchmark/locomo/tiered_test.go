
package locomo

import "testing"

func TestExtractKeywords(t *testing.T) {
	tests := []struct{ input, want string }{
		{"What did Bob eat in Paris?", "bob eat paris"},
		{"When was the meeting?", "meeting"},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractKeywords(tt.input)
		if got != tt.want {
			t.Errorf("extractKeywords(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBigramJaccard(t *testing.T) {
	if s := bigramJaccard("hello world", "hello world"); s != 1.0 {
		t.Errorf("identical: got %f, want 1.0", s)
	}
	if s := bigramJaccard("hello", "completely different"); s > 0.3 {
		t.Errorf("different: got %f, want < 0.3", s)
	}
	if s := bigramJaccard("a", "b"); s != 0 {
		t.Errorf("short strings: got %f, want 0", s)
	}
}

func TestMergeDedup(t *testing.T) {
	existing := []SearchResult{
		{Content: "Bob went to Paris in March", Score: 0.9},
	}
	additions := []SearchResult{
		{Content: "Bob went to Paris in March 2023", Score: 0.7}, // near-duplicate
		{Content: "Alice stayed home", Score: 0.5},               // unique
	}
	merged := mergeDedup(existing, additions)
	if len(merged) != 2 {
		t.Errorf("merged: got %d, want 2 (1 original + 1 unique)", len(merged))
	}
}

func TestCountGood(t *testing.T) {
	results := []SearchResult{
		{Score: 0.9}, {Score: 0.5}, {Score: 0.1}, {Score: 0.4},
	}
	if n := countGood(results, 0.3); n != 3 {
		t.Errorf("countGood(0.3): got %d, want 3", n)
	}
}
