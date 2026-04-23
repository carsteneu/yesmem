package mcp

import (
	"os"
	"testing"
)

func TestProcessCWD_Self(t *testing.T) {
	cwd, ok := processCWD(os.Getpid())
	if !ok {
		t.Skip("processCWD not supported on this OS")
	}
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if cwd != want {
		t.Errorf("processCWD(self) = %q, want %q", cwd, want)
	}
}

func TestProcessCWD_InvalidPID(t *testing.T) {
	if _, ok := processCWD(-1); ok {
		t.Error("expected processCWD(-1) to fail")
	}
}

func TestCallerCWD_NonEmpty(t *testing.T) {
	if cwd := callerCWD(); cwd == "" {
		t.Error("callerCWD returned empty string")
	}
}
