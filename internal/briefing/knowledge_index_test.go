package briefing

import (
	"strings"
	"testing"
	"time"
)

func TestRenderDocIndex_Empty(t *testing.T) {
	result := renderDocIndex(nil, defaultStrings())
	if result != "" {
		t.Errorf("empty doc sources should produce empty string, got: %q", result)
	}
}

func TestRenderDocIndex_WithSources(t *testing.T) {
	sources := []DocSourceSummary{
		{Name: "Go stdlib", ChunkCount: 42, TriggerExts: ".go,.mod", DocType: "reference"},
		{Name: "Symfony coding", ChunkCount: 10, TriggerExts: ".php", DocType: "style"},
		{Name: "PaddleOCR", ChunkCount: 5, TriggerExts: "", DocType: "reference"},
	}
	result := renderDocIndex(sources, defaultStrings())

	if !strings.Contains(result, "Go stdlib") {
		t.Error("should contain source name 'Go stdlib'")
	}
	if !strings.Contains(result, ".go,.mod") {
		t.Error("should contain trigger extensions")
	}
	if !strings.Contains(result, "docs_search") {
		t.Error("should contain docs_search() hint")
	}
	if !strings.Contains(result, "style") || !strings.Contains(result, "reference") {
		t.Error("should indicate doc type")
	}
}

func TestRenderKnowledgeTopology_Empty(t *testing.T) {
	result := renderKnowledgeTopology(nil, nil, defaultStrings())
	if result != "" {
		t.Errorf("empty clusters should produce empty string, got: %q", result)
	}
}

func TestRenderKnowledgeTopology_DepthLabels(t *testing.T) {
	strong := []ClusterSummary{
		{Label: "proxy architecture", Count: 45},
		{Label: "extraction pipeline", Count: 30},
	}
	weak := []ClusterSummary{
		{Label: "telegram adapter", Count: 3},
	}
	result := renderKnowledgeTopology(strong, weak, defaultStrings())

	if !strings.Contains(result, "proxy architecture") {
		t.Error("should contain strong cluster label")
	}
	if !strings.Contains(result, "telegram adapter") {
		t.Error("should contain weak cluster label")
	}
	// Strong clusters should have depth indicator
	if !strings.Contains(result, "45") {
		t.Error("should show learning count for strong clusters")
	}
}

func TestRenderHealth_AllZero(t *testing.T) {
	h := HealthData{
		Contradictions: 0,
		Unfinished:     0,
		Stale:          0,
	}
	result := renderHealth(h, defaultStrings())
	if result != "" {
		t.Errorf("all-zero health should produce empty string, got: %q", result)
	}
}

func TestRenderHealth_WithIssues(t *testing.T) {
	h := HealthData{
		Contradictions: 3,
		Unfinished:     7,
		Stale:          12,
	}
	result := renderHealth(h, defaultStrings())

	if !strings.Contains(result, "3") {
		t.Error("should show contradiction count")
	}
	if !strings.Contains(result, "7") {
		t.Error("should show unfinished count")
	}
	if !strings.Contains(result, "12") {
		t.Error("should show stale count")
	}
}

func TestRenderHealth_PartialIssues(t *testing.T) {
	h := HealthData{
		Contradictions: 0,
		Unfinished:     5,
		Stale:          0,
	}
	result := renderHealth(h, defaultStrings())

	if !strings.Contains(result, "5") {
		t.Error("should show unfinished count")
	}
	// Should not mention contradictions or stale if zero
	if strings.Contains(result, "Widersprüche: 0") || strings.Contains(result, "Contradictions: 0") {
		t.Error("should not show zero-count items")
	}
}

func TestRenderRecentContext_Empty(t *testing.T) {
	result := renderRecentContext(nil, defaultStrings())
	if result != "" {
		t.Errorf("empty files should produce empty string, got: %q", result)
	}
}

func TestRenderRecentContext_WithFiles(t *testing.T) {
	now := time.Now()
	files := []RecentFile{
		{Path: "internal/proxy/cache_keepalive.go", LastSeen: now.Add(-2 * time.Hour), SessionCount: 5},
		{Path: "internal/briefing/briefing.go", LastSeen: now.Add(-24 * time.Hour), SessionCount: 12},
		{Path: "cmd/daemon.go", LastSeen: now.Add(-72 * time.Hour), SessionCount: 2},
	}
	result := renderRecentContext(files, defaultStrings())

	if !strings.Contains(result, "cache_keepalive.go") {
		t.Error("should contain recent file path")
	}
	if !strings.Contains(result, "briefing.go") {
		t.Error("should contain second file")
	}
}

func TestRenderRecentContext_MaxFiles(t *testing.T) {
	now := time.Now()
	var files []RecentFile
	for i := 0; i < 20; i++ {
		files = append(files, RecentFile{
			Path:         "internal/proxy/file_" + string(rune('a'+i)) + ".go",
			LastSeen:     now.Add(-time.Duration(i) * time.Hour),
			SessionCount: 10 - i,
		})
	}
	result := renderRecentContext(files, defaultStrings())

	// Should cap at reasonable number (10 max)
	lineCount := strings.Count(result, ".go")
	if lineCount > 10 {
		t.Errorf("should cap at 10 files, got %d", lineCount)
	}
}

// defaultStrings returns German strings for testing.
func defaultStrings() Strings {
	return DefaultStrings()
}
