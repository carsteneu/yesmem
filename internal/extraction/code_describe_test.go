package extraction

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestGeneratePackageDescriptions_Basic(t *testing.T) {
	// Mock LLM returns structured JSON
	mock := &mockLLMClient{
		completeJSONFunc: func(system, user string, schema map[string]any) (string, error) {
			// Verify the prompt contains package info
			if !strings.Contains(user, "proxy") {
				t.Errorf("prompt should contain package name, got: %s", user[:200])
			}
			if !strings.Contains(user, "CacheGet") {
				t.Errorf("prompt should contain signatures, got: %s", user[:200])
			}
			resp := PackageDescriptionResponse{
				Description:  "HTTP proxy for API request interception and context injection.",
				AntiPatterns: []string{"→ New injection = new *_inject.go file"},
			}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	}

	result := &codescan.ScanResult{
		Packages: []codescan.PackageInfo{
			{Name: "proxy", FileCount: 3, TotalLOC: 500, Files: []codescan.FileInfo{
				{Path: "proxy/cache.go", Signatures: []string{"func CacheGet(key string) ([]byte, bool)", "func CacheSet(key string, val []byte)"}, Imports: []string{"sync", "time"}},
				{Path: "proxy/inject.go", Signatures: []string{"func InjectContext(req *http.Request)"}, Imports: []string{"net/http"}},
			}},
		},
	}

	counts := map[string]storage.EntityCounts{
		"proxy": {Total: 5, Gotchas: 2},
	}

	descs, err := GeneratePackageDescriptions(mock, result, counts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(descs) != 1 {
		t.Fatalf("expected 1 description, got %d", len(descs))
	}
	d := descs["proxy"]
	if d.Description != "HTTP proxy for API request interception and context injection." {
		t.Errorf("description: %q", d.Description)
	}
	if len(d.AntiPatterns) != 1 || !strings.Contains(d.AntiPatterns[0], "inject") {
		t.Errorf("anti_patterns: %v", d.AntiPatterns)
	}
}

func TestGeneratePackageDescriptions_SkipsSmallPackages(t *testing.T) {
	callCount := 0
	mock := &mockLLMClient{
		completeJSONFunc: func(system, user string, schema map[string]any) (string, error) {
			callCount++
			resp := PackageDescriptionResponse{Description: "desc"}
			b, _ := json.Marshal(resp)
			return string(b), nil
		},
	}

	result := &codescan.ScanResult{
		Packages: []codescan.PackageInfo{
			// 1 file, 1 signature — too small, should be skipped
			{Name: "tiny", FileCount: 1, TotalLOC: 20, Files: []codescan.FileInfo{
				{Path: "tiny/main.go", Signatures: []string{"func main()"}},
			}},
			// 3 files — substantial enough
			{Name: "real", FileCount: 3, TotalLOC: 200, Files: []codescan.FileInfo{
				{Path: "real/a.go", Signatures: []string{"func A()", "func B()"}},
				{Path: "real/b.go", Signatures: []string{"func C()"}},
				{Path: "real/c.go", Signatures: []string{"func D()"}},
			}},
		},
	}

	descs, _ := GeneratePackageDescriptions(mock, result, nil)
	if callCount != 1 {
		t.Errorf("expected 1 LLM call (skip tiny), got %d", callCount)
	}
	if _, ok := descs["tiny"]; ok {
		t.Error("tiny package should be skipped")
	}
	if _, ok := descs["real"]; !ok {
		t.Error("real package should have description")
	}
}

func TestGeneratePackageDescriptions_LLMError(t *testing.T) {
	mock := &mockLLMClient{
		completeJSONFunc: func(system, user string, schema map[string]any) (string, error) {
			return "", fmt.Errorf("rate limited")
		},
	}

	result := &codescan.ScanResult{
		Packages: []codescan.PackageInfo{
			{Name: "pkg", FileCount: 3, TotalLOC: 200, Files: []codescan.FileInfo{
				{Path: "pkg/a.go", Signatures: []string{"func A()", "func B()"}},
			}},
		},
	}

	// Should not return error — just skip failed packages
	descs, err := GeneratePackageDescriptions(mock, result, nil)
	if err != nil {
		t.Fatalf("should not error on LLM failure: %v", err)
	}
	if len(descs) != 0 {
		t.Errorf("should have 0 descriptions after LLM error, got %d", len(descs))
	}
}
