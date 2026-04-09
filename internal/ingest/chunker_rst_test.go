package ingest

import (
	"strings"
	"testing"
)

// --- findRSTHeadings tests ---

func TestRSTSimpleUnderlineHeadings(t *testing.T) {
	content := "Title\n=====\n\nSome body text.\n\nSubtitle\n--------\n\nMore text."
	headings := findRSTHeadings(content)

	if len(headings) != 2 {
		t.Fatalf("expected 2 headings, got %d: %+v", len(headings), headings)
	}

	if headings[0].title != "Title" {
		t.Errorf("heading 0: expected title %q, got %q", "Title", headings[0].title)
	}
	if headings[0].level != 1 {
		t.Errorf("heading 0: expected level 1, got %d", headings[0].level)
	}

	if headings[1].title != "Subtitle" {
		t.Errorf("heading 1: expected title %q, got %q", "Subtitle", headings[1].title)
	}
	if headings[1].level != 2 {
		t.Errorf("heading 1: expected level 2, got %d", headings[1].level)
	}
}

func TestRSTOverlineUnderlineHeadings(t *testing.T) {
	content := "=====\nTitle\n=====\n\nBody text.\n\n--------\nSubtitle\n--------\n\nMore text."
	headings := findRSTHeadings(content)

	if len(headings) != 2 {
		t.Fatalf("expected 2 headings, got %d: %+v", len(headings), headings)
	}

	if headings[0].title != "Title" {
		t.Errorf("heading 0: expected %q, got %q", "Title", headings[0].title)
	}
	if headings[0].level != 1 {
		t.Errorf("heading 0: expected level 1, got %d", headings[0].level)
	}

	if headings[1].title != "Subtitle" {
		t.Errorf("heading 1: expected %q, got %q", "Subtitle", headings[1].title)
	}
	if headings[1].level != 2 {
		t.Errorf("heading 1: expected level 2, got %d", headings[1].level)
	}
}

func TestRSTLevelByOrder(t *testing.T) {
	// First adornment char encountered = level 1, second = level 2, etc.
	content := "Part\n~~~~\n\nChapter\n=======\n\nSection\n-------\n\nBack to Part\n~~~~~~~~~~~~"
	headings := findRSTHeadings(content)

	if len(headings) != 4 {
		t.Fatalf("expected 4 headings, got %d: %+v", len(headings), headings)
	}

	// ~ first seen → level 1
	if headings[0].level != 1 {
		t.Errorf("Part: expected level 1, got %d", headings[0].level)
	}
	// = second seen → level 2
	if headings[1].level != 2 {
		t.Errorf("Chapter: expected level 2, got %d", headings[1].level)
	}
	// - third seen → level 3
	if headings[2].level != 3 {
		t.Errorf("Section: expected level 3, got %d", headings[2].level)
	}
	// ~ already seen → level 1 again
	if headings[3].level != 1 {
		t.Errorf("Back to Part: expected level 1, got %d", headings[3].level)
	}
}

func TestRSTMixedOverlineAndUnderline(t *testing.T) {
	// Overline+underline and underline-only with same char = same level
	content := "=====\nTitle\n=====\n\nBody.\n\nAnother\n=======\n\nMore."
	headings := findRSTHeadings(content)

	if len(headings) != 2 {
		t.Fatalf("expected 2 headings, got %d: %+v", len(headings), headings)
	}

	// Both use '=' — but overline+underline vs underline-only are DIFFERENT styles in RST.
	// However, for simplicity we treat the adornment character as the key,
	// not the style. Both get the same level.
	if headings[0].level != headings[1].level {
		t.Errorf("same char should give same level: got %d and %d", headings[0].level, headings[1].level)
	}
}

func TestRSTCodeBlockNotHeading(t *testing.T) {
	content := "Title\n=====\n\nSome text.\n\n.. code-block:: python\n\n   Title\n   =====\n   not a heading\n\nAfter code."
	headings := findRSTHeadings(content)

	// Only the real heading, not the one inside code-block
	if len(headings) != 1 {
		t.Fatalf("expected 1 heading (code-block content ignored), got %d: %+v", len(headings), headings)
	}
	if headings[0].title != "Title" {
		t.Errorf("expected %q, got %q", "Title", headings[0].title)
	}
}

func TestRSTLiteralBlockNotHeading(t *testing.T) {
	// :: at end of paragraph starts a literal block (indented content)
	content := "Title\n=====\n\nExample::\n\n   Heading\n   =======\n   not real\n\nAfter literal."
	headings := findRSTHeadings(content)

	if len(headings) != 1 {
		t.Fatalf("expected 1 heading (literal block content ignored), got %d: %+v", len(headings), headings)
	}
}

func TestRSTEmptyDocument(t *testing.T) {
	chunks := ChunkRST("")
	if chunks != nil {
		t.Errorf("expected nil for empty document, got %v", chunks)
	}
}

func TestRSTPreambleBeforeHeading(t *testing.T) {
	preamble := strings.Repeat("preamble text ", 20)
	content := preamble + "\n\nTitle\n=====\n\n" + strings.Repeat("body ", 60)
	chunks := ChunkRST(content)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}

	// First chunk should have no heading path (preamble)
	if chunks[0].HeadingPath != "" {
		t.Errorf("preamble chunk should have empty heading path, got %q", chunks[0].HeadingPath)
	}
	if chunks[0].SectionLevel != 0 {
		t.Errorf("preamble chunk should have level 0, got %d", chunks[0].SectionLevel)
	}
}

func TestRSTHeadingHierarchy(t *testing.T) {
	body := strings.Repeat("content ", 40)
	content := "Part\n====\n\n" + body + "\n\nChapter\n-------\n\n" + body + "\n\nSection\n~~~~~~~\n\n" + body
	chunks := ChunkRST(content)

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	paths := make(map[string]bool)
	for _, c := range chunks {
		paths[c.HeadingPath] = true
	}

	if !paths["Part"] {
		t.Error("missing chunk with HeadingPath 'Part'")
	}
	if !paths["Part > Chapter"] {
		t.Errorf("missing chunk with HeadingPath 'Part > Chapter', got: %v", paths)
	}
	if !paths["Part > Chapter > Section"] {
		t.Errorf("missing chunk with HeadingPath 'Part > Chapter > Section', got: %v", paths)
	}
}

func TestRSTUnderlineTooShort(t *testing.T) {
	// Underline must be at least as long as the title text
	content := "Long Title Here\n===\n\nBody text " + strings.Repeat("x", 200)
	headings := findRSTHeadings(content)

	// "===" is shorter than "Long Title Here" — not a valid heading
	if len(headings) != 0 {
		t.Errorf("expected 0 headings (underline too short), got %d: %+v", len(headings), headings)
	}
}

func TestRSTContentHashAndTokens(t *testing.T) {
	content := "Title\n=====\n\n" + strings.Repeat("a", 400)
	chunks := ChunkRST(content)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if chunks[0].ContentHash == "" {
		t.Error("content hash should not be empty")
	}
	if chunks[0].TokensApprox < 90 || chunks[0].TokensApprox > 110 {
		t.Errorf("expected tokens ~100, got %d", chunks[0].TokensApprox)
	}
}

func TestRSTSmallSectionMerge(t *testing.T) {
	bigContent := strings.Repeat("x", 300)
	content := "First\n=====\n\n" + bigContent + "\n\nTiny\n----\n\nhi"
	chunks := ChunkRST(content)

	// Tiny section (<200 chars) merged into First
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk after merge, got %d: %+v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0].Content, bigContent) {
		t.Error("merged chunk should contain big content")
	}
	if !strings.Contains(chunks[0].Content, "hi") {
		t.Error("merged chunk should contain small content")
	}
}

func TestRSTLargeSectionSplit(t *testing.T) {
	var paras []string
	for i := 0; i < 20; i++ {
		paras = append(paras, strings.Repeat("word ", 200))
	}
	body := strings.Join(paras, "\n\n")
	content := "Big Section\n===========\n\n" + body

	chunks := ChunkRST(content)
	if len(chunks) < 2 {
		t.Errorf("expected large section split into multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.HeadingPath != "Big Section" {
			t.Errorf("split chunk should retain heading path, got %q", c.HeadingPath)
		}
	}
}

func TestRSTVariousAdornmentChars(t *testing.T) {
	// Test that various RST adornment characters are recognized
	chars := []struct {
		char  string
		title string
	}{
		{"=", "Equals"},
		{"-", "Dashes"},
		{"~", "Tildes"},
		{"^", "Carets"},
		{"\"", "Quotes"},
		{"*", "Stars"},
		{"+", "Plus"},
		{"#", "Hash"},
		{".", "Dots"},
	}

	var parts []string
	for _, c := range chars {
		underline := strings.Repeat(c.char, len(c.title))
		parts = append(parts, c.title+"\n"+underline+"\n\n"+strings.Repeat("body ", 50))
	}
	content := strings.Join(parts, "\n\n")
	headings := findRSTHeadings(content)

	if len(headings) != len(chars) {
		t.Fatalf("expected %d headings, got %d: %+v", len(chars), len(headings), headings)
	}

	for i, c := range chars {
		if headings[i].title != c.title {
			t.Errorf("heading %d: expected %q, got %q", i, c.title, headings[i].title)
		}
		// Each uses a different char, so level increments
		if headings[i].level != i+1 {
			t.Errorf("heading %d (%s): expected level %d, got %d", i, c.title, i+1, headings[i].level)
		}
	}
}

// --- detectRSTMetadata tests ---

func TestRSTMetadataCodeBlockLanguage(t *testing.T) {
	content := "Some intro text.\n\n.. code-block:: python\n\n   print('hello')\n\nMore text.\n\n.. code-block:: yaml\n\n   key: value"
	meta := detectRSTMetadata(content)

	if meta["code_example"] != "true" {
		t.Error("expected code_example=true")
	}
	if meta["languages"] != "python,yaml" {
		t.Errorf("expected languages=python,yaml, got %q", meta["languages"])
	}
}

func TestRSTMetadataSourcecode(t *testing.T) {
	content := ".. sourcecode:: php\n\n   echo 'hi';"
	meta := detectRSTMetadata(content)

	if meta["code_example"] != "true" {
		t.Error("expected code_example=true")
	}
	if meta["languages"] != "php" {
		t.Errorf("expected languages=php, got %q", meta["languages"])
	}
}

func TestRSTMetadataLiteralBlock(t *testing.T) {
	content := "Example output::\n\n   $ ls -la"
	meta := detectRSTMetadata(content)

	if meta["code_example"] != "true" {
		t.Error("expected code_example=true for literal block")
	}
}

func TestRSTMetadataVersionDirectives(t *testing.T) {
	content := ".. deprecated:: 5.4\n\n   Use new_func() instead.\n\n.. versionadded:: 6.1\n\n.. versionchanged:: 6.2\n\n   Now accepts arrays."
	meta := detectRSTMetadata(content)

	if meta["deprecated_since"] != "5.4" {
		t.Errorf("expected deprecated_since=5.4, got %q", meta["deprecated_since"])
	}
	if meta["version_added"] != "6.1" {
		t.Errorf("expected version_added=6.1, got %q", meta["version_added"])
	}
	if meta["version_changed"] != "6.2" {
		t.Errorf("expected version_changed=6.2, got %q", meta["version_changed"])
	}
}

func TestRSTMetadataAdmonitions(t *testing.T) {
	content := ".. warning::\n\n   Be careful!\n\n.. tip::\n\n   Try this instead.\n\n.. note::\n\n   FYI."
	meta := detectRSTMetadata(content)

	if meta["admonition"] != "warning,tip,note" {
		t.Errorf("expected admonition=warning,tip,note, got %q", meta["admonition"])
	}
}

func TestRSTMetadataEntities(t *testing.T) {
	content := "Use :class:`Symfony\\Component\\Messenger\\Transport` and :func:`retry` or :method:`handle`."
	meta := detectRSTMetadata(content)

	expected := "Symfony\\Component\\Messenger\\Transport,retry,handle"
	if meta["rst_entities"] != expected {
		t.Errorf("expected rst_entities=%q, got %q", expected, meta["rst_entities"])
	}
}

func TestRSTMetadataEntitiesWithTilde(t *testing.T) {
	// ~ prefix means "show only last component" in RST display, but we store the full path
	content := "See :class:`~Symfony\\Messenger\\Envelope` for details."
	meta := detectRSTMetadata(content)

	if meta["rst_entities"] != "Symfony\\Messenger\\Envelope" {
		t.Errorf("expected tilde stripped, got %q", meta["rst_entities"])
	}
}

func TestRSTMetadataDocRefs(t *testing.T) {
	content := "See :doc:`/messenger` and :doc:`/components/event_dispatcher` for details."
	meta := detectRSTMetadata(content)

	if meta["rst_doc_refs"] != "/messenger,/components/event_dispatcher" {
		t.Errorf("expected rst_doc_refs=/messenger,/components/event_dispatcher, got %q", meta["rst_doc_refs"])
	}
}

func TestRSTMetadataGridTable(t *testing.T) {
	content := "+--------+--------+\n| Col A  | Col B  |\n+--------+--------+\n| 1      | 2      |\n+--------+--------+"
	meta := detectRSTMetadata(content)

	if meta["has_table"] != "true" {
		t.Error("expected has_table=true for grid table")
	}
}

func TestRSTMetadataNoFalsePositives(t *testing.T) {
	content := "Just some plain text without any RST directives or special formatting."
	meta := detectRSTMetadata(content)

	if len(meta) != 0 {
		t.Errorf("expected empty metadata for plain text, got %v", meta)
	}
}

func TestRSTMetadataMultipleEntitiesPerLine(t *testing.T) {
	content := "Compare :class:`Foo` with :class:`Bar` and call :func:`baz`."
	meta := detectRSTMetadata(content)

	if meta["rst_entities"] != "Foo,Bar,baz" {
		t.Errorf("expected rst_entities=Foo,Bar,baz, got %q", meta["rst_entities"])
	}
}

func TestRSTMetadataDuplicateLanguages(t *testing.T) {
	content := ".. code-block:: php\n\n   echo 1;\n\n.. code-block:: php\n\n   echo 2;"
	meta := detectRSTMetadata(content)

	if meta["languages"] != "php" {
		t.Errorf("expected deduplicated languages=php, got %q", meta["languages"])
	}
}
