package logrotate

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}

func TestWrite_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	w, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "seed\n") || !strings.Contains(string(data), "hello\n") {
		t.Errorf("expected both seed and hello, got %q", data)
	}
}

func TestWrite_RotatesWhenSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path, WithMaxSize(20))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// First write fits
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	// Second write pushes over threshold (10+15 > 20)
	if _, err := w.Write([]byte("012345678901234")); err != nil {
		t.Fatal(err)
	}
	// There should now be a backup file with timestamp suffix
	entries, _ := os.ReadDir(dir)
	backups := 0
	for _, e := range entries {
		if e.Name() != "app.log" {
			backups++
		}
	}
	if backups != 1 {
		t.Errorf("expected 1 backup after rotation, got %d (entries: %v)", backups, entries)
	}
}

func TestRotate_BackupNameHasTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path, WithMaxSize(5))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write([]byte("abcdefghij")) // forces rotation
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "app.log" {
			if !strings.Contains(e.Name(), time.Now().Format("20060102")) {
				t.Errorf("backup %q missing today's date stamp", e.Name())
			}
		}
	}
}

func TestCleanupOld_RemovesOldestBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path, WithMaxSize(10), WithMaxBackups(2))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// Force 4 rotations
	w.Write([]byte("0123456789A")) // rotate 1
	w.Write([]byte("0123456789A")) // rotate 2
	w.Write([]byte("0123456789A")) // rotate 3
	w.Write([]byte("0123456789A")) // rotate 4
	entries, _ := os.ReadDir(dir)
	backups := 0
	for _, e := range entries {
		if e.Name() != "app.log" {
			backups++
		}
	}
	if backups > 2 {
		t.Errorf("expected at most 2 backups, got %d", backups)
	}
}

func TestConcurrentWrites_ThreadSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path, WithMaxSize(100), WithMaxBackups(100))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				w.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()
	// All 1000 bytes must be accounted for (in current file + backups)
	current, _ := os.ReadFile(path)
	entries, _ := os.ReadDir(dir)
	total := len(current)
	for _, e := range entries {
		if e.Name() != "app.log" {
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			total += len(b)
		}
	}
	if total != 1000 {
		t.Errorf("expected 1000 bytes total across current+backups, got %d (lost writes?)", total)
	}
}

func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
