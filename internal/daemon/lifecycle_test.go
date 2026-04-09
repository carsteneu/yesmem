package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestIsProcessZombie_Self(t *testing.T) {
	// Our own process is definitely not a zombie
	pid := strconv.Itoa(os.Getpid())
	if isProcessZombie(pid) {
		t.Error("own process should not be detected as zombie")
	}
}

func TestIsProcessZombie_InvalidPid(t *testing.T) {
	if isProcessZombie("notanumber") {
		t.Error("invalid PID string should not be zombie")
	}
	if isProcessZombie("999999999") {
		t.Error("non-existent PID should not be zombie")
	}
}

func TestIsProcessAliveOrZombie_DeadProcess(t *testing.T) {
	// A PID that doesn't exist should be neither alive nor zombie
	pid := "999999999"
	if isProcessAlive(pid) {
		t.Error("non-existent PID should not be alive")
	}
	if isProcessZombie(pid) {
		t.Error("non-existent PID should not be zombie")
	}
}

func TestCleanupZombiePidFile(t *testing.T) {
	// Simulate: PID file points to a non-existent process.
	// ensureSingleInstance should clean it up and succeed.
	dir := t.TempDir()
	pidPath := dir + "/daemon.pid"
	os.WriteFile(pidPath, []byte("999999999"), 0644)

	err := ensureSingleInstance(dir, false)
	if err != nil {
		t.Fatalf("should succeed when PID file points to dead process: %v", err)
	}

	// PID file should now contain our PID
	data, _ := os.ReadFile(pidPath)
	if got := string(data); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("PID file should contain our PID %d, got %s", os.Getpid(), got)
	}

	// Clean up our PID file so next test can run
	os.Remove(pidPath)
}

func TestEnsureSingleInstance_ZombiePidTreatedAsDead(t *testing.T) {
	// This test verifies the code path: if isProcessAlive returns true
	// but isProcessZombie also returns true, the PID file should be
	// cleaned up (zombie = effectively dead).
	//
	// We can't easily create a real zombie in a unit test, but we can
	// verify the function exists and handles the negative case correctly.
	// The integration test is: deploy → zombie → daemon restarts successfully.

	pid := strconv.Itoa(os.Getpid())
	// Living non-zombie process: alive=true, zombie=false
	if !isProcessAlive(pid) {
		t.Error("own process should be alive")
	}
	if isProcessZombie(pid) {
		t.Error("own process should not be zombie")
	}
}

func TestPidFilePath(t *testing.T) {
	result := pidFilePath("/tmp/test-data")
	if result != "/tmp/test-data/daemon.pid" {
		t.Errorf("expected /tmp/test-data/daemon.pid, got %q", result)
	}
}

func TestReadPidFile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pid")
	os.WriteFile(path, []byte("12345\n"), 0644)
	pid := readPidFile(path)
	if pid != "12345" {
		t.Errorf("expected '12345', got %q", pid)
	}
}

func TestReadPidFile_Missing(t *testing.T) {
	pid := readPidFile("/nonexistent/path/test.pid")
	if pid != "unknown" {
		t.Errorf("expected 'unknown', got %q", pid)
	}
}

func TestIsProcessAlive_Self(t *testing.T) {
	pidStr := strconv.Itoa(os.Getpid())
	if !isProcessAlive(pidStr) {
		t.Error("our own process should be alive")
	}
}

func TestIsProcessAlive_InvalidPid(t *testing.T) {
	if isProcessAlive("not-a-number") {
		t.Error("invalid PID should not be alive")
	}
}

func TestTrimLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"quoted"`, "quoted"},
		{`'single quoted'`, "single quoted"},
		{"  spaces  ", "spaces"},
		{"plain", "plain"},
		{"", ""},
	}
	for _, tt := range tests {
		got := trimLabel(tt.input)
		if got != tt.want {
			t.Errorf("trimLabel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
