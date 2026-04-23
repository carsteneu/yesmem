package codescan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanHealth_TodoCount(t *testing.T) {
	dir := t.TempDir()
	content := `package main

// TODO: refactor this
func main() {
	// FIXME: broken edge case
	// HACK: temporary workaround
	println("hello")
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	health := ScanHealth(dir)
	if health.TodoCount != 3 {
		t.Errorf("expected 3 TODOs, got %d", health.TodoCount)
	}
}

func TestScanHealth_MissingTests(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package api\n\nfunc Handle() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "utils.go"), []byte("package api\n\nfunc Util() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte("package api\n\nfunc TestHandle() {}\n"), 0644)

	health := ScanHealth(dir)
	// utils.go has no corresponding test file
	if health.FilesWithoutTests != 1 {
		t.Errorf("expected 1 file without tests, got %d", health.FilesWithoutTests)
	}
}

func TestScanHealth_AllTested(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package api\n\nfunc Handle() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte("package api\n\nfunc TestHandle() {}\n"), 0644)

	health := ScanHealth(dir)
	if health.FilesWithoutTests != 0 {
		t.Errorf("expected 0 files without tests, got %d", health.FilesWithoutTests)
	}
}

func TestScanHealth_IgnoresNonGoForTestCoverage(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "script.py"), []byte("def main():\n    pass\n"), 0644)
	// Python test coverage checking would need different logic
	// For now, only Go files are checked for _test.go pairing

	health := ScanHealth(dir)
	if health.FilesWithoutTests != 0 {
		t.Errorf("should not count non-Go files as missing tests, got %d", health.FilesWithoutTests)
	}
}

func TestScanHealth_SkipsVendor(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "vendor", "pkg", "lib.go"), []byte("package pkg\n// TODO: fix\n"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// TODO: one\n"), 0644)

	health := ScanHealth(dir)
	if health.TodoCount != 1 {
		t.Errorf("should only count TODOs in source files, got %d", health.TodoCount)
	}
}

func TestRenderHealth_Integration(t *testing.T) {
	health := CodeHealth{
		TodoCount:        5,
		FilesWithoutTests: 3,
	}

	md := RenderCodeHealth(health)
	if md == "" {
		t.Error("should render non-empty health section")
	}
}

func TestRenderHealth_Clean(t *testing.T) {
	health := CodeHealth{
		TodoCount:        0,
		FilesWithoutTests: 0,
	}

	md := RenderCodeHealth(health)
	if md != "" {
		t.Errorf("clean project should not render health section, got: %s", md)
	}
}
