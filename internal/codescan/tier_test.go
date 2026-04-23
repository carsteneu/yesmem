package codescan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectTier_Tiny(t *testing.T) {
	dir := t.TempDir()
	// Create 5 small Go files
	for i := 0; i < 5; i++ {
		content := "package main\n\nfunc handler() {}\n"
		os.WriteFile(filepath.Join(dir, "file_"+string(rune('a'+i))+".go"), []byte(content), 0644)
	}

	tier, stats := DetectTier(dir)
	if tier != TierTiny {
		t.Errorf("expected TierTiny, got %v", tier)
	}
	if stats.FileCount != 5 {
		t.Errorf("expected 5 files, got %d", stats.FileCount)
	}
	if stats.TotalLOC == 0 {
		t.Error("TotalLOC should be > 0")
	}
}

func TestDetectTier_Small(t *testing.T) {
	dir := t.TempDir()
	// Create 20 files
	for i := 0; i < 20; i++ {
		content := "package main\n\nfunc handler() {\n\treturn\n}\n"
		os.WriteFile(filepath.Join(dir, "file_"+string(rune('a'+i))+".go"), []byte(content), 0644)
	}

	tier, _ := DetectTier(dir)
	if tier != TierSmall {
		t.Errorf("expected TierSmall, got %v", tier)
	}
}

func TestDetectTier_Medium(t *testing.T) {
	dir := t.TempDir()
	// Create 100 files across subdirectories
	for i := 0; i < 10; i++ {
		subdir := filepath.Join(dir, "pkg_"+string(rune('a'+i)))
		os.MkdirAll(subdir, 0755)
		for j := 0; j < 10; j++ {
			content := "package pkg\n\nfunc Do() {}\n"
			os.WriteFile(filepath.Join(subdir, "file_"+string(rune('a'+j))+".go"), []byte(content), 0644)
		}
	}

	tier, stats := DetectTier(dir)
	if tier != TierMedium {
		t.Errorf("expected TierMedium, got %v", tier)
	}
	if stats.FileCount != 100 {
		t.Errorf("expected 100 files, got %d", stats.FileCount)
	}
}

func TestDetectTier_Large(t *testing.T) {
	dir := t.TempDir()
	// Create 250 files
	for i := 0; i < 25; i++ {
		subdir := filepath.Join(dir, "pkg_"+string(rune('a'+i)))
		os.MkdirAll(subdir, 0755)
		for j := 0; j < 10; j++ {
			content := "package pkg\n\nfunc Do() {}\n"
			os.WriteFile(filepath.Join(subdir, "file_"+string(rune('a'+j))+".go"), []byte(content), 0644)
		}
	}

	tier, _ := DetectTier(dir)
	if tier != TierLarge {
		t.Errorf("expected TierLarge, got %v", tier)
	}
}

func TestDetectTier_IgnoresHiddenAndVendor(t *testing.T) {
	dir := t.TempDir()
	// Hidden directory
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "objects", "pack.go"), []byte("package git\n"), 0644)
	// Vendor
	os.MkdirAll(filepath.Join(dir, "vendor", "github.com"), 0755)
	os.WriteFile(filepath.Join(dir, "vendor", "github.com", "lib.go"), []byte("package lib\n"), 0644)
	// node_modules
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("module.exports = {};\n"), 0644)
	// Actual source
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	_, stats := DetectTier(dir)
	if stats.FileCount != 1 {
		t.Errorf("expected 1 file (only main.go), got %d", stats.FileCount)
	}
}

func TestDetectTier_CountsLOC(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	_, stats := DetectTier(dir)
	if stats.TotalLOC != 5 {
		t.Errorf("expected 5 non-empty LOC, got %d", stats.TotalLOC)
	}
}

func TestCountFiles_OnlySourceFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Hello\n"), 0644)
	os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "app.tsx"), []byte("export default () => null;\n"), 0644)

	_, stats := DetectTier(dir)
	// Should count .go, .tsx but not .md, .json, .css
	if stats.FileCount != 2 {
		t.Errorf("expected 2 source files, got %d", stats.FileCount)
	}
}
