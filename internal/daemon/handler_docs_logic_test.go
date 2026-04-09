package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

func TestIsReferenceHeading(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"schema overview", true},
		{"api endpoint", true},
		{"mcp tool reference", true},
		{"database tables", true},
		{"migration history", true},
		{"hard rules", false},
		{"conventions", false},
		{"deployment", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isReferenceHeading(tt.input)
		if got != tt.want {
			t.Errorf("isReferenceHeading(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIsNonBehavioralLine(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/home/user/path", true},
		{"/path/with spaces/no", false},
		{"https://example.com", true},
		{"https://example.com some context", false},
		{"normal text line", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isNonBehavioralLine(tt.input)
		if got != tt.want {
			t.Errorf("isNonBehavioralLine(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"README.md", true},
		{"notes.txt", true},
		{"guide.rst", true},
		{"config.yaml", true},
		{"config.yml", true},
		{"config.toml", true},
		{"main.go", false},
		{"style.css", false},
		{"image.png", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isTextFile(tt.input)
		if got != tt.want {
			t.Errorf("isTextFile(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestMergeDocResults(t *testing.T) {
	bm25 := []storage.DocChunkResult{
		{DocChunk: storage.DocChunk{ID: 1, Content: "first"}},
		{DocChunk: storage.DocChunk{ID: 2, Content: "second"}},
	}
	vector := []storage.DocChunkResult{
		{DocChunk: storage.DocChunk{ID: 2, Content: "second"}},
		{DocChunk: storage.DocChunk{ID: 3, Content: "third"}},
	}
	merged := mergeDocResults(bm25, vector, 10)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged, got %d", len(merged))
	}
	if merged[0].ID != 2 {
		t.Errorf("expected ID 2 first (appears in both), got ID %d", merged[0].ID)
	}
}

func TestMergeDocResults_Limit(t *testing.T) {
	bm25 := []storage.DocChunkResult{
		{DocChunk: storage.DocChunk{ID: 1}},
		{DocChunk: storage.DocChunk{ID: 2}},
		{DocChunk: storage.DocChunk{ID: 3}},
	}
	vector := []storage.DocChunkResult{
		{DocChunk: storage.DocChunk{ID: 4}},
		{DocChunk: storage.DocChunk{ID: 5}},
	}
	merged := mergeDocResults(bm25, vector, 3)
	if len(merged) != 3 {
		t.Fatalf("expected limit=3, got %d", len(merged))
	}
}

func TestMergeDocResults_Empty(t *testing.T) {
	merged := mergeDocResults(nil, nil, 10)
	if len(merged) != 0 {
		t.Errorf("expected empty, got %d", len(merged))
	}
}

func TestMechanicalPreFilter_SkipsCodeBlocks(t *testing.T) {
	input := "# Rules\n- rule one\n```\ncode block\nshould skip\n```\n- rule two"
	result := mechanicalPreFilter(input)
	if strings.Contains(result, "code block") {
		t.Error("code blocks should be stripped")
	}
	if !strings.Contains(result, "rule one") {
		t.Error("normal content should remain")
	}
}

func TestMechanicalPreFilter_SkipsReferenceSection(t *testing.T) {
	input := "# Important\n- keep this\n## Schema Overview\n- skip this table\n- and this\n## Rules\n- keep this too"
	result := mechanicalPreFilter(input)
	if strings.Contains(result, "skip this") {
		t.Error("reference section should be stripped")
	}
	if !strings.Contains(result, "keep this") {
		t.Error("non-reference sections should remain")
	}
}

func TestMechanicalPreFilter_Empty(t *testing.T) {
	result := mechanicalPreFilter("")
	if strings.TrimSpace(result) != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestExpandWithLinks_SimpleFile(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.md")
	os.WriteFile(mainFile, []byte("# Title\nSome content"), 0644)

	result, err := expandWithLinks(mainFile, 0)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "Some content") {
		t.Error("expected content in result")
	}
}

func TestExpandWithLinks_WithRef(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "rules.md")
	os.WriteFile(refFile, []byte("rule 1\nrule 2"), 0644)

	mainFile := filepath.Join(dir, "main.md")
	os.WriteFile(mainFile, []byte("# Header\n@rules.md\nmore text"), 0644)

	result, err := expandWithLinks(mainFile, 0)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "rule 1") {
		t.Error("expected expanded ref content")
	}
}

func TestExpandWithLinks_MaxDepth(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "deep.md")
	os.WriteFile(f, []byte("@deep.md\nactual content"), 0644)

	result, err := expandWithLinks(f, 2)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// At depth 2, no expansion — raw content returned
	if !strings.Contains(result, "@deep.md") {
		t.Error("expected no expansion at max depth")
	}
}

func TestExpandWithLinks_MissingFile(t *testing.T) {
	_, err := expandWithLinks("/nonexistent/file.md", 0)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
