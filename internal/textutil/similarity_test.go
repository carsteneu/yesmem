package textutil

import (
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "basic words",
			input: "Hello World",
			want:  []string{"hello", "world"},
		},
		{
			name:  "punctuation stripped to spaces, words kept",
			input: "foo, bar! baz.",
			want:  []string{"foo", "bar", "baz."},
		},
		{
			name:  "keeps underscores dashes slashes dots",
			input: "some_var my-flag /path/to file.go",
			want:  []string{"some_var", "my-flag", "/path/to", "file.go"},
		},
		{
			name:  "single-char tokens dropped",
			input: "a bb ccc",
			want:  []string{"bb", "ccc"},
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "lowercases input",
			input: "UPPER lower MiXeD",
			want:  []string{"upper", "lower", "mixed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokenize(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("Tokenize(%q) = %v, want %v", tc.input, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Tokenize(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTokenSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{
			name: "identical sets",
			a:    []string{"foo", "bar"},
			b:    []string{"foo", "bar"},
			want: 1.0,
		},
		{
			name: "no overlap",
			a:    []string{"foo", "bar"},
			b:    []string{"baz", "qux"},
			want: 0.0,
		},
		{
			name: "partial overlap — shorter fully contained",
			a:    []string{"foo"},
			b:    []string{"foo", "bar", "baz"},
			want: 1.0,
		},
		{
			name: "partial overlap — half match",
			a:    []string{"foo", "bar"},
			b:    []string{"foo", "baz"},
			want: 0.5,
		},
		{
			name: "empty a",
			a:    []string{},
			b:    []string{"foo"},
			want: 0.0,
		},
		{
			name: "empty b",
			a:    []string{"foo"},
			b:    []string{},
			want: 0.0,
		},
		{
			name: "both empty",
			a:    []string{},
			b:    []string{},
			want: 0.0,
		},
		{
			name: "symmetric — larger a, smaller b",
			a:    []string{"foo", "bar", "baz"},
			b:    []string{"foo"},
			want: 1.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TokenSimilarity(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("TokenSimilarity(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
