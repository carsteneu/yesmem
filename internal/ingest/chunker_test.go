package ingest

import (
	"strings"
	"testing"
)

func TestSimpleHeadings(t *testing.T) {
	contentA := "some text here " + strings.Repeat("padding ", 30)
	contentB := "more text here " + strings.Repeat("padding ", 30)
	md := "# A\n" + contentA + "\n## B\n" + contentB
	chunks := ChunkMarkdown(md)

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	if chunks[0].HeadingPath != "A" {
		t.Errorf("chunk 0 heading_path: expected %q, got %q", "A", chunks[0].HeadingPath)
	}
	if chunks[0].SectionLevel != 1 {
		t.Errorf("chunk 0 level: expected 1, got %d", chunks[0].SectionLevel)
	}
	if !strings.Contains(chunks[0].Content, "some text here") {
		t.Errorf("chunk 0 content missing expected text: %q", chunks[0].Content)
	}

	if chunks[1].HeadingPath != "A > B" {
		t.Errorf("chunk 1 heading_path: expected %q, got %q", "A > B", chunks[1].HeadingPath)
	}
	if chunks[1].SectionLevel != 2 {
		t.Errorf("chunk 1 level: expected 2, got %d", chunks[1].SectionLevel)
	}
	if !strings.Contains(chunks[1].Content, "more text here") {
		t.Errorf("chunk 1 content missing expected text: %q", chunks[1].Content)
	}
}

func TestDeepNesting(t *testing.T) {
	md := "# A\n## B\n### C\ndeep content"
	chunks := ChunkMarkdown(md)

	// A and B are empty (<200 chars) so they get merged; C has content.
	// With the merge logic, we expect the deep content to survive.
	found := false
	for _, c := range chunks {
		if c.HeadingPath == "A > B > C" && strings.Contains(c.Content, "deep content") {
			found = true
			if c.SectionLevel != 3 {
				t.Errorf("expected level 3, got %d", c.SectionLevel)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected chunk with path 'A > B > C' containing 'deep content', got chunks: %+v", chunks)
	}
}

func TestLargeSectionSplit(t *testing.T) {
	// Build a section with >8000 chars using paragraphs separated by double newlines.
	var paras []string
	for i := 0; i < 20; i++ {
		paras = append(paras, strings.Repeat("word ", 200)) // ~1000 chars each
	}
	body := strings.Join(paras, "\n\n")
	md := "# Big Section\n" + body

	chunks := ChunkMarkdown(md)

	if len(chunks) < 2 {
		t.Errorf("expected large section to be split into multiple chunks, got %d", len(chunks))
	}

	for _, c := range chunks {
		if len(c.Content) > largeChunkChars+1000 {
			t.Errorf("chunk too large after split: %d chars", len(c.Content))
		}
		if c.HeadingPath != "Big Section" {
			t.Errorf("split chunk should retain heading path 'Big Section', got %q", c.HeadingPath)
		}
	}
}

func TestSmallSectionMerge(t *testing.T) {
	// First section is big enough, second is tiny (<200 chars).
	bigContent := strings.Repeat("x", 300)
	md := "# First\n" + bigContent + "\n## Tiny\nhi"

	chunks := ChunkMarkdown(md)

	// The tiny section should be merged into the first one.
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk after merge, got %d: %+v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0].Content, bigContent) {
		t.Error("merged chunk should contain the big content")
	}
	if !strings.Contains(chunks[0].Content, "hi") {
		t.Error("merged chunk should contain the small content")
	}
}

func TestCodeBlockDetection(t *testing.T) {
	md := "# Code Section\nSome intro\n```go\nfunc main() {}\n```\nAfter code"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	found := false
	for _, c := range chunks {
		if c.Metadata["code_example"] == "true" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected metadata code_example=true on chunk with code block")
	}
}

func TestCodeBlockLanguageExtraction(t *testing.T) {
	md := "# Section\nIntro\n```php\necho 'hi';\n```\nMiddle\n```yaml\nkey: value\n```"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	if chunks[0].Metadata["languages"] != "php,yaml" {
		t.Errorf("expected languages=php,yaml, got %q", chunks[0].Metadata["languages"])
	}
}

func TestCodeBlockLanguageDeduplicated(t *testing.T) {
	md := "# Section\n```go\nfoo()\n```\ntext\n```go\nbar()\n```"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if chunks[0].Metadata["languages"] != "go" {
		t.Errorf("expected deduplicated languages=go, got %q", chunks[0].Metadata["languages"])
	}
}

func TestCodeBlockNoLanguage(t *testing.T) {
	md := "# Section\n```\nplain code\n```"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if _, ok := chunks[0].Metadata["languages"]; ok {
		t.Errorf("expected no languages key for bare code block, got %q", chunks[0].Metadata["languages"])
	}
}

func TestTableDetection(t *testing.T) {
	md := "# Data\n| Col A | Col B |\n|-------|-------|\n| 1     | 2     |"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	found := false
	for _, c := range chunks {
		if c.Metadata["has_table"] == "true" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected metadata has_table=true on chunk with table")
	}
}

func TestContentBeforeHeading(t *testing.T) {
	md := "This is preamble text that is long enough to not be merged.\n" + strings.Repeat("more preamble ", 20) + "\n# First Heading\nHeading content here and more text to avoid merge."
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	if chunks[0].HeadingPath != "" {
		t.Errorf("first chunk should have empty heading_path, got %q", chunks[0].HeadingPath)
	}
	if chunks[0].SectionLevel != 0 {
		t.Errorf("first chunk should have level 0, got %d", chunks[0].SectionLevel)
	}
}

func TestEmptyDocument(t *testing.T) {
	chunks := ChunkMarkdown("")
	if chunks != nil {
		t.Errorf("expected nil for empty document, got %v", chunks)
	}
}

func TestHeadingLevelReset(t *testing.T) {
	// h2 then h1: h1 should reset the heading path.
	content := strings.Repeat("x", 250) // enough to avoid merge
	md := "## B\n" + content + "\n# A\n" + content

	chunks := ChunkMarkdown(md)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Find the chunk for heading A.
	foundA := false
	for _, c := range chunks {
		if c.HeadingPath == "A" {
			foundA = true
			if c.SectionLevel != 1 {
				t.Errorf("A chunk: expected level 1, got %d", c.SectionLevel)
			}
			break
		}
	}
	if !foundA {
		paths := make([]string, len(chunks))
		for i, c := range chunks {
			paths[i] = c.HeadingPath
		}
		t.Errorf("expected chunk with path 'A', got paths: %v", paths)
	}
}

func TestContentHashConsistency(t *testing.T) {
	md := "# Test\nSome content"
	chunks1 := ChunkMarkdown(md)
	chunks2 := ChunkMarkdown(md)

	if len(chunks1) == 0 || len(chunks2) == 0 {
		t.Fatal("expected chunks")
	}

	if chunks1[0].ContentHash != chunks2[0].ContentHash {
		t.Error("same content should produce same hash")
	}
	if chunks1[0].ContentHash == "" {
		t.Error("hash should not be empty")
	}
}

func TestTokensApprox(t *testing.T) {
	content := strings.Repeat("a", 400)
	md := "# Heading\n" + content
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}

	// TokensApprox = len(Content)/4. Content is 400 chars so ~100 tokens.
	if chunks[0].TokensApprox < 90 || chunks[0].TokensApprox > 110 {
		t.Errorf("expected tokens ~100, got %d", chunks[0].TokensApprox)
	}
}

func TestCodeBlockNotTreatedAsHeading(t *testing.T) {
	md := "# Real Heading\nSome text\n```\n# Not a heading\n## Also not\n```\nAfter code"
	chunks := ChunkMarkdown(md)

	// Should be 1 chunk: the code block headings are not real headings.
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk (code block headings ignored), got %d", len(chunks))
		for i, c := range chunks {
			t.Logf("  chunk %d: path=%q content=%q", i, c.HeadingPath, c.Content[:min(50, len(c.Content))])
		}
	}
}

func TestPipeInsideCodeBlockNotTable(t *testing.T) {
	md := "# Section\n```\necho foo | grep bar\n```"
	chunks := ChunkMarkdown(md)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}

	if chunks[0].Metadata["has_table"] == "true" {
		t.Error("pipe inside code block should not trigger has_table")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
