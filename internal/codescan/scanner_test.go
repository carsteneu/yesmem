package codescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryScanner_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Packages) != 0 {
		t.Errorf("expected 0 packages, got %d", len(result.Packages))
	}
}

func TestDirectoryScanner_SingleFile(t *testing.T) {
	dir := t.TempDir()
	content := `package main

import "fmt"

// UserHandler handles user requests.
func UserHandler() {
	fmt.Println("hello")
}

type User struct {
	Name string
	Age  int
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	f := result.Files[0]
	if f.Path != "main.go" {
		t.Errorf("expected relative path main.go, got %s", f.Path)
	}
	if f.Language != "go" {
		t.Errorf("expected language go, got %s", f.Language)
	}

	// Should extract function and type signatures
	hasFunc := false
	hasType := false
	for _, sig := range f.Signatures {
		if strings.Contains(sig, "UserHandler") {
			hasFunc = true
		}
		if strings.Contains(sig, "User") {
			hasType = true
		}
	}
	if !hasFunc {
		t.Error("should extract UserHandler function signature")
	}
	if !hasType {
		t.Error("should extract User struct signature")
	}
}

func TestDirectoryScanner_PackageGrouping(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal", "proxy"), 0755)
	os.MkdirAll(filepath.Join(dir, "internal", "storage"), 0755)

	os.WriteFile(filepath.Join(dir, "internal", "proxy", "cache.go"), []byte("package proxy\n\nfunc CacheGet() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "internal", "proxy", "forward.go"), []byte("package proxy\n\nfunc Forward() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "internal", "storage", "db.go"), []byte("package storage\n\nfunc Query() {}\n"), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Packages) != 2 {
		t.Errorf("expected 2 packages, got %d", len(result.Packages))
	}

	// Check package names
	names := make(map[string]bool)
	for _, p := range result.Packages {
		names[p.Name] = true
	}
	if !names["internal/proxy"] {
		t.Error("should have internal/proxy package")
	}
	if !names["internal/storage"] {
		t.Error("should have internal/storage package")
	}
}

func TestDirectoryScanner_GoSignatures(t *testing.T) {
	dir := t.TempDir()
	content := `package api

type Config struct {
	Host string
	Port int
}

type Handler interface {
	ServeHTTP()
}

func NewConfig() *Config {
	return &Config{}
}

func (c *Config) Validate() error {
	return nil
}

const MaxRetries = 3

var DefaultTimeout = 30
`
	os.WriteFile(filepath.Join(dir, "api.go"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	sigs := strings.Join(f.Signatures, "\n")

	checks := []string{"Config struct", "Handler interface", "func NewConfig", "func (c *Config) Validate", "const MaxRetries", "var DefaultTimeout"}
	for _, check := range checks {
		if !strings.Contains(sigs, check) {
			t.Errorf("missing signature containing %q in:\n%s", check, sigs)
		}
	}
}

func TestDirectoryScanner_PHPSignatures(t *testing.T) {
	dir := t.TempDir()
	content := `<?php

namespace App\Controller;

class UserController extends AbstractController
{
    public function index(): Response
    {
        return $this->render('user/index.html.twig');
    }

    private function validate(User $user): bool
    {
        return true;
    }
}
`
	os.WriteFile(filepath.Join(dir, "UserController.php"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	sigs := strings.Join(f.Signatures, "\n")

	if !strings.Contains(sigs, "class UserController") {
		t.Errorf("missing class signature in:\n%s", sigs)
	}
	if !strings.Contains(sigs, "function index") {
		t.Errorf("missing function signature in:\n%s", sigs)
	}
}

func TestDirectoryScanner_PythonSignatures(t *testing.T) {
	dir := t.TempDir()
	content := `import os

class DataProcessor:
    def __init__(self, path):
        self.path = path

    def process(self):
        pass

def main():
    dp = DataProcessor("/tmp")
    dp.process()
`
	os.WriteFile(filepath.Join(dir, "processor.py"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	sigs := strings.Join(f.Signatures, "\n")

	if !strings.Contains(sigs, "class DataProcessor") {
		t.Errorf("missing class signature in:\n%s", sigs)
	}
	if !strings.Contains(sigs, "def main") {
		t.Errorf("missing function signature in:\n%s", sigs)
	}
}

func TestDirectoryScanner_FullContent_TinyFile(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f := result.Files[0]
	if f.Content != content {
		t.Errorf("expected full content to be stored, got %q", f.Content)
	}
}

func TestDirectoryScanner_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package api\n\nfunc Handle() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "handler_test.go"), []byte("package api\n\nfunc TestHandle() {}\n"), 0644)

	s := &DirectoryScanner{}
	result, err := s.Scan(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Test files are scanned but marked
	testCount := 0
	for _, f := range result.Files {
		if f.IsTest {
			testCount++
		}
	}
	if testCount != 1 {
		t.Errorf("expected 1 test file, got %d", testCount)
	}
}
