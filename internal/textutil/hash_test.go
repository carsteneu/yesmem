package textutil

import "testing"

func TestNormalizeForHash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"lowercase", "Hello World", "hello world"},
		{"collapse whitespace", "hello   world", "hello world"},
		{"collapse tabs and newlines", "hello\t\n  world", "hello world"},
		{"trim", "  hello world  ", "hello world"},
		{"combined", "  Hello   WORLD\t\n ", "hello world"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeForHash(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeForHash(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestContentHash(t *testing.T) {
	// Same text → same hash
	h1 := ContentHash("hello world")
	h2 := ContentHash("hello world")
	if h1 != h2 {
		t.Errorf("identical text produced different hashes: %s vs %s", h1, h2)
	}

	// Different whitespace → same hash
	h3 := ContentHash("hello   world")
	if h1 != h3 {
		t.Errorf("different whitespace produced different hashes: %s vs %s", h1, h3)
	}

	// Different case → same hash
	h4 := ContentHash("Hello World")
	if h1 != h4 {
		t.Errorf("different case produced different hashes: %s vs %s", h1, h4)
	}

	// Different text → different hash
	h5 := ContentHash("goodbye world")
	if h1 == h5 {
		t.Errorf("different text produced same hash: %s", h1)
	}

	// Empty string → consistent hash
	h6 := ContentHash("")
	h7 := ContentHash("")
	if h6 != h7 {
		t.Errorf("empty string produced different hashes: %s vs %s", h6, h7)
	}

	// Hash is 64 hex chars (SHA-256)
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64", len(h1))
	}
}
