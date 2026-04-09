package ingest

import (
	"strings"
)

// rstAdornmentChars contains all valid RST section title adornment characters.
const rstAdornmentChars = "=-~^\"*+#._:`"

// ChunkRST splits an RST document into structured sections.
func ChunkRST(content string) []Chunk {
	if content == "" {
		return nil
	}

	headings := findRSTHeadings(content)
	sections := splitIntoSectionsWithHeadings([]byte(content), headings)
	sections = mergeSmallSections(sections)

	var chunks []Chunk
	for _, s := range sections {
		expanded := maybeExpandLargeSection(s)
		for _, c := range expanded {
			c.ContentHash = contentHash(c.Content)
			c.TokensApprox = len(c.Content) / 4
			c.Metadata = detectRSTMetadata(c.Content)
			chunks = append(chunks, c)
		}
	}

	return chunks
}

// findRSTHeadings scans RST content for section headings.
// RST headings: text line + underline of repeated adornment chars (>= text length).
// Optional: overline + text + underline (same char, same length).
// Level determined by order of first appearance of each adornment character.
func findRSTHeadings(content string) []headingInfo {
	lines := strings.Split(content, "\n")
	var headings []headingInfo
	charToLevel := make(map[byte]int)
	nextLevel := 1
	inCodeBlock := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimRight(line, " \t\r")

		// Track code blocks: .. code-block:: or literal blocks (::)
		if !inCodeBlock {
			stripped := strings.TrimSpace(trimmed)
			if strings.HasPrefix(stripped, ".. code-block::") || strings.HasPrefix(stripped, ".. sourcecode::") {
				inCodeBlock = true
				continue
			}
			if strings.HasSuffix(stripped, "::") && len(stripped) > 2 {
				// Check if next non-empty line is indented (literal block)
				for j := i + 1; j < len(lines); j++ {
					nextTrimmed := strings.TrimRight(lines[j], " \t\r")
					if nextTrimmed == "" {
						continue
					}
					if len(nextTrimmed) > 0 && (nextTrimmed[0] == ' ' || nextTrimmed[0] == '\t') {
						inCodeBlock = true
					}
					break
				}
				continue
			}
		}

		if inCodeBlock {
			// Exit code block when we hit a non-empty, non-indented line
			stripped := strings.TrimRight(line, " \t\r")
			if stripped == "" {
				continue
			}
			if stripped[0] != ' ' && stripped[0] != '\t' {
				inCodeBlock = false
				// Fall through to process this line normally
			} else {
				continue
			}
		}

		// Check for overline+title+underline pattern
		if i+2 < len(lines) && isAdornmentLine(trimmed) {
			titleLine := strings.TrimRight(lines[i+1], " \t\r")
			underLine := strings.TrimRight(lines[i+2], " \t\r")
			title := strings.TrimSpace(titleLine)

			if title != "" && !isAdornmentLine(titleLine) &&
				isAdornmentLine(underLine) &&
				trimmed[0] == underLine[0] &&
				len(trimmed) >= len(title) && len(underLine) >= len(title) {

				adornChar := trimmed[0]
				level, ok := charToLevel[adornChar]
				if !ok {
					level = nextLevel
					charToLevel[adornChar] = level
					nextLevel++
				}

				// byteStart: start of the overline
				byteStart := byteOffsetOfLine(lines, i)
				// byteEnd: after the underline
				byteEnd := byteOffsetOfLine(lines, i+3)

				headings = append(headings, headingInfo{
					level:     level,
					title:     title,
					byteStart: byteStart,
					byteEnd:   byteEnd,
				})
				i += 2 // skip title + underline
				continue
			}
		}

		// Check for title+underline pattern
		if i+1 < len(lines) {
			titleLine := trimmed
			underLine := strings.TrimRight(lines[i+1], " \t\r")
			title := strings.TrimSpace(titleLine)

			if title != "" && !isAdornmentLine(titleLine) &&
				isAdornmentLine(underLine) &&
				len(underLine) >= len(title) {

				adornChar := underLine[0]
				level, ok := charToLevel[adornChar]
				if !ok {
					level = nextLevel
					charToLevel[adornChar] = level
					nextLevel++
				}

				byteStart := byteOffsetOfLine(lines, i)
				byteEnd := byteOffsetOfLine(lines, i+2)

				headings = append(headings, headingInfo{
					level:     level,
					title:     title,
					byteStart: byteStart,
					byteEnd:   byteEnd,
				})
				i++ // skip underline
				continue
			}
		}
	}

	return headings
}

// splitIntoSectionsWithHeadings splits content into sections using pre-computed headings.
// Uses byteEnd to skip past heading lines (supports multi-line RST headings).
func splitIntoSectionsWithHeadings(source []byte, headings []headingInfo) []section {
	var sections []section
	var headingStack []string
	currentLevel := 0
	prevEnd := 0

	for _, h := range headings {
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

		if h.level <= len(headingStack) {
			headingStack = headingStack[:h.level-1]
		} else {
			for len(headingStack) < h.level-1 {
				headingStack = append(headingStack, "")
			}
		}
		headingStack = append(headingStack, h.title)
		currentLevel = h.level

		// Use byteEnd if available, otherwise scan past the heading line
		if h.byteEnd > 0 {
			prevEnd = h.byteEnd
		} else {
			pos := h.byteStart
			for pos < len(source) && source[pos] != '\n' {
				pos++
			}
			if pos < len(source) {
				pos++
			}
			prevEnd = pos
		}
	}

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

// isAdornmentLine checks if a line consists entirely of the same RST adornment character.
// Must be at least 3 characters long (RST spec minimum).
func isAdornmentLine(line string) bool {
	if len(line) < 3 {
		return false
	}
	ch := line[0]
	if !strings.ContainsRune(rstAdornmentChars, rune(ch)) {
		return false
	}
	for i := 1; i < len(line); i++ {
		if line[i] != ch {
			return false
		}
	}
	return true
}

// byteOffsetOfLine calculates the byte offset of line n in the original content.
// If n >= len(lines), returns the total byte length.
func byteOffsetOfLine(lines []string, n int) int {
	offset := 0
	for i := 0; i < n && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for \n
	}
	return offset
}

// detectRSTMetadata extracts structured metadata from RST content.
func detectRSTMetadata(content string) map[string]string {
	meta := make(map[string]string)

	var languages []string
	var entities []string
	var docRefs []string
	var admonitions []string
	inCodeBlock := false

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		// Code block detection
		if strings.HasPrefix(trimmed, ".. code-block::") || strings.HasPrefix(trimmed, ".. sourcecode::") {
			meta["code_example"] = "true"
			// Extract language
			parts := strings.SplitN(trimmed, "::", 2)
			if len(parts) == 2 {
				lang := strings.TrimSpace(parts[1])
				if lang != "" {
					languages = appendUnique(languages, lang)
				}
			}
			inCodeBlock = true
			continue
		}

		// Literal block (:: at end of line)
		if strings.HasSuffix(trimmed, "::") && len(trimmed) > 2 {
			meta["code_example"] = "true"
		}

		// Track indented blocks (code blocks end at non-indented line)
		if inCodeBlock {
			if trimmed == "" {
				continue
			}
			if line[0] != ' ' && line[0] != '\t' {
				inCodeBlock = false
			} else {
				continue
			}
		}

		// Version directives
		if strings.HasPrefix(trimmed, ".. deprecated::") {
			meta["deprecated_since"] = strings.TrimSpace(strings.TrimPrefix(trimmed, ".. deprecated::"))
		}
		if strings.HasPrefix(trimmed, ".. versionadded::") {
			meta["version_added"] = strings.TrimSpace(strings.TrimPrefix(trimmed, ".. versionadded::"))
		}
		if strings.HasPrefix(trimmed, ".. versionchanged::") {
			meta["version_changed"] = strings.TrimSpace(strings.TrimPrefix(trimmed, ".. versionchanged::"))
		}

		// Admonition directives
		for _, adm := range []string{"warning", "note", "tip", "caution", "danger", "important", "hint"} {
			if strings.HasPrefix(trimmed, ".. "+adm+"::") {
				admonitions = appendUnique(admonitions, adm)
			}
		}

		// RST cross-reference roles: :class:`...`, :func:`...`, :method:`...`, :doc:`...`
		extractRSTRoles(trimmed, "class", &entities)
		extractRSTRoles(trimmed, "func", &entities)
		extractRSTRoles(trimmed, "method", &entities)
		extractRSTRoles(trimmed, "doc", &docRefs)

		// Table detection (grid tables use +---+ or | patterns)
		if !inCodeBlock && (strings.Contains(trimmed, "|") || (strings.HasPrefix(trimmed, "+") && strings.Contains(trimmed, "-"))) {
			meta["has_table"] = "true"
		}
	}

	if len(languages) > 0 {
		meta["languages"] = strings.Join(languages, ",")
	}
	if len(entities) > 0 {
		meta["rst_entities"] = strings.Join(entities, ",")
	}
	if len(docRefs) > 0 {
		meta["rst_doc_refs"] = strings.Join(docRefs, ",")
	}
	if len(admonitions) > 0 {
		meta["admonition"] = strings.Join(admonitions, ",")
	}

	return meta
}

// extractRSTRoles finds :role:`value` patterns and appends values to the slice.
func extractRSTRoles(line, role string, target *[]string) {
	prefix := ":" + role + ":`"
	rest := line
	for {
		idx := strings.Index(rest, prefix)
		if idx == -1 {
			return
		}
		rest = rest[idx+len(prefix):]
		end := strings.Index(rest, "`")
		if end == -1 {
			return
		}
		val := rest[:end]
		// Strip leading ~ (RST shorthand for abbreviated display)
		val = strings.TrimPrefix(val, "~")
		if val != "" {
			*target = appendUnique(*target, val)
		}
		rest = rest[end+1:]
	}
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
