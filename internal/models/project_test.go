package models

import "testing"

func TestProjectMatches(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		// exact match
		{"/home/user/myproject", "/home/user/myproject", true},
		// suffix match: b is suffix of a
		{"myproject", "/home/user/myproject", true},
		// suffix match: a is suffix of b
		{"/home/user/myproject", "myproject", true},
		// basename match
		{"/home/alice/myproject", "/home/bob/myproject", true},
		// different projects
		{"/home/user/foo", "/home/user/bar", false},
		// partial name should not match
		{"project", "/home/user/myproject", false},
		// empty strings
		{"", "", true},
		{"", "/home/user/project", false},
	}
	for _, tt := range tests {
		got := ProjectMatches(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("ProjectMatches(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
