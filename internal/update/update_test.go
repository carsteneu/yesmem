package update

import (
	"testing"
	"time"
)

func TestBinaryPath(t *testing.T) {
	path := BinaryPath()
	if path == "" {
		t.Error("BinaryPath should not be empty")
	}
}

func TestRestartArgs(t *testing.T) {
	args := restartArgs()
	if len(args) == 0 {
		t.Error("restartArgs should return at least one argument")
	}
	if args[0] != "restart" {
		t.Errorf("restartArgs[0] = %q, want restart", args[0])
	}
}

func TestParseCheckInterval(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"6h", 6 * time.Hour},
		{"12h", 12 * time.Hour},
		{"24h", 24 * time.Hour},
		{"", 6 * time.Hour},
		{"invalid", 6 * time.Hour},
	}
	for _, tt := range tests {
		got := ParseCheckInterval(tt.input)
		if got != tt.want {
			t.Errorf("ParseCheckInterval(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
