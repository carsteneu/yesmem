package ingest

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Chunk represents a section of a markdown document.
type Chunk struct {
	HeadingPath  string            // "Messenger Component > Redis Transport > Retry Strategy"
	SectionLevel int               // 0=root, 1=h1, 2=h2, etc.
	Content      string            // The actual text content
	ContentHash  string            // SHA256 of content
	TokensApprox int               // Rough token count (len/4)
	Metadata     map[string]string // {"code_example": "true", "has_table": "true"}
}

const (
	largeChunkChars = 8000 // ~2000 tokens
	smallChunkChars = 200  // ~50 tokens
)

// ChunkMarkdown splits a markdown document into structured sections.
func ChunkMarkdown(content string) []Chunk {
	if content == "" {
		return nil
	}

	sections := splitIntoSections(content)
	sections = mergeSmallSections(sections)

	var chunks []Chunk
	for _, s := range sections {
		expanded := maybeExpandLargeSection(s)
		for _, c := range expanded {
			c.ContentHash = contentHash(c.Content)
			c.TokensApprox = len(c.Content) / 4
			c.Metadata = detectMetadata(c.Content)
			chunks = append(chunks, c)
		}
	}

	return chunks
}

// section is an intermediate representation before final chunk creation.
type section struct {
	headings []string // heading stack at this point
	level    int      // 0=root, 1=h1, etc.
	content  string
}

func (s section) headingPath() string {
	return strings.Join(s.headings, " > ")
}

// headingInfo holds the position and metadata of a heading found by a parser.
type headingInfo struct {
	level int
	title string
	// byteStart is the first byte of the heading line in the source.
	byteStart int
	// byteEnd is the first byte after the heading (used by RST for multi-line headings).
	// 0 means single-line heading (Markdown) — scan past the line instead.
	byteEnd int
}

// findHeadings walks the goldmark AST and extracts heading positions from source.
func findHeadings(source []byte) []headingInfo {
	reader := text.NewReader(source)
	parser := goldmark.DefaultParser()
	doc := parser.Parse(reader)

	var headings []headingInfo
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Kind() != ast.KindHeading {
			continue
		}
		h := child.(*ast.Heading)
		title := strings.TrimSpace(string(child.Text(source)))

		// Determine the byte start of this heading.
		// The heading node's Lines() contain the heading line itself for ATX headings,
		// or for setext headings, the content line(s). We also need to check if there
		// are lines in the heading node.
		byteStart := -1
		lines := h.Lines()
		if lines.Len() > 0 {
			byteStart = lines.At(0).Start
		}

		// For ATX headings, the line starts with #, so we need to search backwards
		// from the first line start to find the actual beginning of the # line.
		// The Lines() of a Heading node contain only the text after the # prefix,
		// so we need to scan backward to find the # characters.
		if byteStart > 0 {
			// Walk backwards to find the start of the line (past any leading #)
			pos := byteStart - 1
			for pos >= 0 && source[pos] != '\n' {
				pos--
			}
			byteStart = pos + 1 // start of the line
		} else if byteStart == 0 {
			// Already at the start
		} else {
			// Heading with no lines (empty heading) -- try to find it via child nodes.
			// This shouldn't normally happen but handle gracefully.
			continue
		}

		headings = append(headings, headingInfo{
			level:     h.Level,
			title:     title,
			byteStart: byteStart,
		})
	}
	return headings
}

// splitIntoSections parses the markdown into raw sections based on headings
// using goldmark's AST parser for heading detection, then slices the original
// source between headings to preserve all content (including fence markers).
func splitIntoSections(content string) []section {
	source := []byte(content)
	headings := findHeadings(source)

	var sections []section
	var headingStack []string
	currentLevel := 0

	// Process sections between headings.
	// Each heading starts a new section; content before the first heading is the preamble.
	prevEnd := 0

	for _, h := range headings {
		// Content before this heading belongs to the previous section.
		if h.byteStart > prevEnd {
			bodyText := strings.TrimSpace(string(source[prevEnd:h.byteStart]))
			if bodyText != "" {
				headingsCopy := make([]string, len(headingStack))
				copy(headingsCopy, headingStack)
				sections = append(sections, section{
					headings: headingsCopy,
					level:    currentLevel,
					content:  bodyText,
				})
			}
		}

		// Update heading stack.
		if h.level <= len(headingStack) {
			headingStack = headingStack[:h.level-1]
		} else {
			for len(headingStack) < h.level-1 {
				headingStack = append(headingStack, "")
			}
		}
		headingStack = append(headingStack, h.title)
		currentLevel = h.level

		// Move prevEnd past the heading line(s).
		// Find the end of the heading line.
		pos := h.byteStart
		for pos < len(source) && source[pos] != '\n' {
			pos++
		}
		if pos < len(source) {
			pos++ // skip the \n
		}
		prevEnd = pos
	}

	// Remaining content after the last heading.
	if prevEnd < len(source) {
		bodyText := strings.TrimSpace(string(source[prevEnd:]))
		if bodyText != "" {
			headingsCopy := make([]string, len(headingStack))
			copy(headingsCopy, headingStack)
			sections = append(sections, section{
				headings: headingsCopy,
				level:    currentLevel,
				content:  bodyText,
			})
		}
	}

	return sections
}

// mergeSmallSections merges sections that are too small (<200 chars) with
// their preceding section at the same or higher level.
func mergeSmallSections(sections []section) []section {
	if len(sections) == 0 {
		return nil
	}

	var result []section
	for _, s := range sections {
		if len(s.content) < smallChunkChars && len(result) > 0 {
			// Merge into the last section in result.
			last := &result[len(result)-1]
			last.content = last.content + "\n\n" + s.content
		} else {
			result = append(result, s)
		}
	}
	return result
}

// maybeExpandLargeSection splits a section at paragraph boundaries if it
// exceeds the large chunk threshold.
func maybeExpandLargeSection(s section) []Chunk {
	if len(s.content) <= largeChunkChars {
		return []Chunk{{
			HeadingPath:  s.headingPath(),
			SectionLevel: s.level,
			Content:      s.content,
		}}
	}

	paragraphs := strings.Split(s.content, "\n\n")
	var chunks []Chunk
	var buf strings.Builder

	flushBuf := func() {
		text := strings.TrimSpace(buf.String())
		if text != "" {
			chunks = append(chunks, Chunk{
				HeadingPath:  s.headingPath(),
				SectionLevel: s.level,
				Content:      text,
			})
		}
		buf.Reset()
	}

	for i, para := range paragraphs {
		if buf.Len()+len(para) > largeChunkChars && buf.Len() > 0 {
			flushBuf()
		}
		if i > 0 && buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}
	flushBuf()

	return chunks
}

// detectMetadata scans content for code blocks, languages, and tables.
func detectMetadata(content string) map[string]string {
	meta := make(map[string]string)
	var languages []string

	inCode := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inCode {
				// Opening fence — extract language
				meta["code_example"] = "true"
				lang := strings.TrimSpace(trimmed[3:])
				// Strip trailing fence chars and info string extras
				if idx := strings.IndexByte(lang, ' '); idx != -1 {
					lang = lang[:idx]
				}
				lang = strings.TrimRight(lang, "`")
				if lang != "" {
					languages = appendUnique(languages, lang)
				}
			}
			inCode = !inCode
			continue
		}
		if !inCode && strings.Contains(trimmed, "|") {
			meta["has_table"] = "true"
		}
	}

	if len(languages) > 0 {
		meta["languages"] = strings.Join(languages, ",")
	}

	return meta
}

// contentHash returns the hex-encoded SHA256 hash of s.
func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}
