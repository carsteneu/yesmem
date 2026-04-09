package hooks

import "testing"

func TestIsHarmlessExit(t *testing.T) {
	tests := []struct {
		cmd      string
		output   string
		harmless bool
	}{
		{"grep foo bar.txt", "", true},
		{"grep -r pattern .", "some match", true},
		{"diff a.txt b.txt", "1c1\n< foo\n---\n> bar", true},
		{"test -f /tmp/x", "", true},
		{"[ -d /tmp/foo ]", "", true},
		{"rg pattern .", "", true},
		{"make build", "undefined: NewBudgetTracker", false},
		{"go test ./...", "FAIL\tpkg/foo", false},
		{"ls /nonexistent", "No such file or directory", false},
		{"cd /nonexistent && ls", "bash: cd: /nonexistent: No such file", false},
		{"curl https://example.com", "Connection refused", false},
	}

	for _, tt := range tests {
		got := isHarmlessExit(tt.cmd, tt.output)
		if got != tt.harmless {
			t.Errorf("isHarmlessExit(%q, %q) = %v, want %v", tt.cmd, tt.output, got, tt.harmless)
		}
	}
}

func TestBuildSearchQuery(t *testing.T) {
	tests := []struct {
		cmd    string
		errOut string
		empty  bool
	}{
		{"make build", "internal/config/config.go:42: undefined: NewBudgetTracker", false},
		{"go test ./...", "FAIL\tpkg/foo\t0.003s", false},
		{"npm install", "ERESOLVE could not resolve", false},
		{"", "", true},
	}

	for _, tt := range tests {
		query := buildSearchQuery(tt.cmd, tt.errOut)
		if tt.empty && query != "" {
			t.Errorf("buildSearchQuery(%q, %q) = %q, want empty", tt.cmd, tt.errOut, query)
		}
		if !tt.empty && query == "" {
			t.Errorf("buildSearchQuery(%q, %q) = empty, want non-empty", tt.cmd, tt.errOut)
		}
		if len(query) > 200 {
			t.Errorf("query too long: %d chars", len(query))
		}
	}
}

func TestAssistStripANSI(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"\033[43mhello\033[0m world", "hello world"},
		{"no codes here", "no codes here"},
		{"\033[1m\033[31merror\033[0m", "error"},
	}
	for _, tt := range tests {
		got := stripANSI(tt.in)
		if got != tt.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
