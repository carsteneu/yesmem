package engine

import (
	"strings"
	"unicode/utf8"
)

// maxLineLength bounds one output line a verdict carries: a command that never
// prints a newline would otherwise put its whole tail on one line.
const maxLineLength = 2000

// LastLines keeps the tail of a command output: the part a reader needs, and
// the part a test summary usually lives in.
func LastLines(text string, count int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	for index, line := range lines {
		lines[index] = TruncateUTF8(line, maxLineLength)
	}
	return strings.Join(lines, "\n")
}

// TruncateUTF8 cuts a string at a rune boundary, so a verdict never carries
// half a character.
func TruncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// DisplayName makes a repository-controlled name safe to print on one line.
func DisplayName(name string) string {
	var builder strings.Builder
	builder.Grow(len(name))
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			builder.WriteRune('?')
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}
