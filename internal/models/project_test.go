package models

import "testing"

func TestProjectMatches(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/home/user/myproject", "/home/user/myproject", true},
		{"myproject", "/home/user/myproject", true},
		{"/home/user/myproject", "myproject", true},
		{"/home/alice/myproject", "/home/bob/myproject", true},
		{"/home/user/foo", "/home/user/bar", false},
		{"project", "/home/user/myproject", false},
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

func TestCanonicalProject(t *testing.T) {
	tests := []struct {
		cwd  string
		want string
	}{
		{"/home/chief/memory/yesmem/.worktrees/opencode-proxy", "yesmem"},
		{"/home/chief/memory/yesmem/.worktrees/feat+capability-memory", "yesmem"},
		{"/home/chief/memory/yesmem", "yesmem"},
		{"/home/chief/projects/gluten", "gluten"},
		{"/var/www/html/GreenWashProjekt/greenWebsite", "greenWebsite"},
		{"/home/user/projects/.worktrees/my-feature", "projects"},
	}
	for _, tt := range tests {
		got := CanonicalProject(tt.cwd)
		if got != tt.want {
			t.Errorf("CanonicalProject(%q) = %q, want %q", tt.cwd, got, tt.want)
		}
	}
}
