package hooks

import (
	"strings"
	"unicode"
)

// shellNoise contains common shell tokens to skip during keyword extraction.
var shellNoise = map[string]bool{
	"sudo": true, "bash": true, "sh": true, "zsh": true,
	"set": true, "echo": true, "printf": true,
	"if": true, "then": true, "else": true, "fi": true,
	"do": true, "done": true, "for": true, "while": true,
	"case": true, "esac": true, "in": true,
	"export": true, "local": true, "readonly": true,
	"true": true, "false": true, "exit": true,
	"dev": true, "null": true, "tmp": true,
	"cat": true, "head": true, "tail": true, "grep": true,
	"and": true, "the": true, "not": true,
	"test": true, "internal": true, "count": true, "timeout": true, "run": true,
}

// extractKeywords extracts meaningful tokens from a bash command.
func extractKeywords(cmd string) []string {
	lower := strings.ToLower(cmd)

	// Replace non-alphanumeric chars with spaces (keep _ - .)
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			return r
		}
		return ' '
	}, lower)

	words := strings.Fields(normalized)
	seen := make(map[string]bool)
	var keywords []string

	for _, w := range words {
		// Skip flags
		if strings.HasPrefix(w, "-") {
			continue
		}
		// Skip short words
		if len(w) < 3 {
			continue
		}
		// Skip shell noise
		if shellNoise[w] {
			continue
		}
		// Deduplicate
		if seen[w] {
			continue
		}
		seen[w] = true
		keywords = append(keywords, w)
	}
	return keywords
}

// matchScore counts how many keywords appear in the gotcha content.
func matchScore(keywords []string, content string) int {
	lower := strings.ToLower(content)
	score := 0
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}
	return score
}

// hasLongKeywordMatch returns true if any keyword >= 6 chars matches the content.
// This allows single-keyword matches for specific terms like "sandbox", "systemctl".
func hasLongKeywordMatch(keywords []string, content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range keywords {
		if len(kw) >= 6 && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// hasFileKeywordMatch returns true if any keyword containing a "." (filename-like) matches.
// e.g. "main.go", "settings.json", "config.yaml" are specific enough for single-match.
func hasFileKeywordMatch(keywords []string, content string) bool {
	lower := strings.ToLower(content)
	for _, kw := range keywords {
		if strings.Contains(kw, ".") && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// pathNoise contains generic path segments to skip during file path keyword extraction.
var pathNoise = map[string]bool{
	"home": true, "root": true, "usr": true, "var": true,
	"etc": true, "opt": true, "tmp": true, "dev": true,
	"src": true, "lib": true, "bin": true, "www": true,
	"html": true, "chief": true, "internal": true,
	"cmd": true, "pkg": true, "vendor": true,
}

// extractPathKeywords extracts meaningful tokens from a file path.
// /home/user/project/internal/hooks/check.go
// → ["user", "project", "hooks", "check.go", "check"]
func extractPathKeywords(path string) []string {
	lower := strings.ToLower(path)
	parts := strings.Split(lower, "/")

	seen := make(map[string]bool)
	var keywords []string

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || len(p) < 3 {
			continue
		}
		if pathNoise[p] {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		keywords = append(keywords, p)

		// Also add filename without extension as separate keyword
		if strings.Contains(p, ".") {
			name := p[:strings.LastIndex(p, ".")]
			if len(name) >= 3 && !seen[name] {
				seen[name] = true
				keywords = append(keywords, name)
			}
		}
	}
	return keywords
}

// stripANSI removes ANSI escape sequences from text.
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}
