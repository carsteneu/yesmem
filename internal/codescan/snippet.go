package codescan

import (
	"fmt"
	"strings"
)

// Symbol represents a top-level symbol in a Go source file.
type Symbol struct {
	Name      string // symbol name
	Kind      string // func, method, var, const, type
	Line      int    // 1-based line number
	Signature string // short signature or declaration
}

// ExtractSymbol extracts the full body of a named symbol (func, method, var, const, type).
// Returns empty string if not found.
func ExtractSymbol(source, name string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// func name( or func (recv) name(
		if strings.HasPrefix(trimmed, "func ") && containsFuncName(trimmed, name) {
			return extractBraceBlock(lines, i)
		}

		// var name = ... (single line or backtick block)
		if strings.HasPrefix(trimmed, "var "+name+" ") || strings.HasPrefix(trimmed, "var "+name+"=") {
			return extractVarOrConst(lines, i)
		}

		// const name = ...
		if strings.HasPrefix(trimmed, "const "+name+" ") || strings.HasPrefix(trimmed, "const "+name+"=") {
			return extractVarOrConst(lines, i)
		}

		// var ( block containing name
		if trimmed == "var (" {
			if block, ok := extractBlockContaining(lines, i, name); ok {
				return block
			}
		}

		// const ( block containing name
		if trimmed == "const (" {
			if block, ok := extractBlockContaining(lines, i, name); ok {
				return block
			}
		}

		// type name struct/interface
		if strings.HasPrefix(trimmed, "type "+name+" ") || strings.HasPrefix(trimmed, "type "+name+"=") {
			if strings.Contains(trimmed, "struct") || strings.Contains(trimmed, "interface") {
				return extractBraceBlock(lines, i)
			}
			return line
		}
	}
	return ""
}

// ExtractRange returns lines start through end (1-based, inclusive).
// Returns empty string if range is invalid.
func ExtractRange(source string, start, end int) string {
	lines := strings.Split(source, "\n")
	if start < 1 || start > len(lines) || end < start {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start - 1; i < end; i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("%4d  %s", i+1, lines[i]))
	}
	return b.String()
}

// ParseFileSymbols returns all top-level symbols in a Go source file.
// Only returns symbols at package level (lines not starting with whitespace).
func ParseFileSymbols(source string) []Symbol {
	lines := strings.Split(source, "\n")
	var symbols []Symbol

	for i, line := range lines {
		// Skip indented lines (not top-level)
		if len(line) > 0 && (line[0] == '\t' || line[0] == ' ') {
			continue
		}
		trimmed := strings.TrimSpace(line)
		lineNum := i + 1

		// func (recv Type) Name( → method
		if strings.HasPrefix(trimmed, "func (") {
			if name := extractMethodName(trimmed); name != "" {
				symbols = append(symbols, Symbol{Name: name, Kind: "method", Line: lineNum, Signature: firstLine(trimmed)})
			}
			continue
		}

		// func Name( → function
		if strings.HasPrefix(trimmed, "func ") {
			if name := extractFuncName(trimmed); name != "" {
				symbols = append(symbols, Symbol{Name: name, Kind: "func", Line: lineNum, Signature: firstLine(trimmed)})
			}
			continue
		}

		// var Name or var (
		if strings.HasPrefix(trimmed, "var ") {
			if trimmed == "var (" {
				symbols = append(symbols, parseBlockSymbols(lines, i, "var")...)
			} else {
				if name := extractDeclName(trimmed, "var"); name != "" {
					symbols = append(symbols, Symbol{Name: name, Kind: "var", Line: lineNum, Signature: strings.TrimPrefix(trimmed, "var ")})
				}
			}
			continue
		}

		// const Name or const (
		if strings.HasPrefix(trimmed, "const ") {
			if trimmed == "const (" {
				symbols = append(symbols, parseBlockSymbols(lines, i, "const")...)
			} else {
				if name := extractDeclName(trimmed, "const"); name != "" {
					symbols = append(symbols, Symbol{Name: name, Kind: "const", Line: lineNum, Signature: strings.TrimPrefix(trimmed, "const ")})
				}
			}
			continue
		}

		// type Name
		if strings.HasPrefix(trimmed, "type ") {
			if name := extractDeclName(trimmed, "type"); name != "" {
				symbols = append(symbols, Symbol{Name: name, Kind: "type", Line: lineNum, Signature: strings.TrimPrefix(trimmed, "type ")})
			}
			continue
		}
	}

	return symbols
}

// --- internal helpers ---

func containsFuncName(line, name string) bool {
	// "func name(" or "func (x Type) name("
	if strings.Contains(line, " "+name+"(") {
		return true
	}
	if strings.Contains(line, ")"+name+"(") {
		return true
	}
	if strings.Contains(line, ") "+name+"(") {
		return true
	}
	return false
}

func extractBraceBlock(lines []string, start int) string {
	depth := 0
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
		for _, ch := range lines[i] {
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return b.String()
				}
			}
		}
	}
	return b.String()
}

func extractVarOrConst(lines []string, start int) string {
	line := lines[start]
	// Check for backtick string (multi-line)
	backticks := strings.Count(line, "`")
	if backticks%2 == 1 {
		var b strings.Builder
		b.WriteString(line)
		for i := start + 1; i < len(lines); i++ {
			b.WriteByte('\n')
			b.WriteString(lines[i])
			if strings.Contains(lines[i], "`") {
				return b.String()
			}
		}
		return b.String()
	}

	// Multi-line expression (continuation via +, ||, &&, comma, or opening paren/brace)
	trimmed := strings.TrimSpace(line)
	if isContinuationLine(trimmed) {
		var b strings.Builder
		b.WriteString(line)
		for i := start + 1; i < len(lines); i++ {
			b.WriteByte('\n')
			b.WriteString(lines[i])
			t := strings.TrimSpace(lines[i])
			if !isContinuationLine(t) {
				return b.String()
			}
		}
		return b.String()
	}

	return line
}

func isContinuationLine(trimmed string) bool {
	return strings.HasSuffix(trimmed, "+") ||
		strings.HasSuffix(trimmed, ",") ||
		strings.HasSuffix(trimmed, "||") ||
		strings.HasSuffix(trimmed, "&&") ||
		strings.HasSuffix(trimmed, "(") ||
		strings.HasSuffix(trimmed, "{")
}

func extractBlockContaining(lines []string, start int, name string) (string, bool) {
	found := false
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, name+" ") || strings.HasPrefix(trimmed, name+"=") || trimmed == name {
			found = true
		}
		if trimmed == ")" {
			if found {
				return b.String(), true
			}
			return "", false
		}
	}
	return "", false
}

func extractMethodName(line string) string {
	// func (x Type) Name(
	idx := strings.Index(line, ") ")
	if idx < 0 {
		idx = strings.Index(line, ")")
		if idx < 0 {
			return ""
		}
	}
	rest := strings.TrimSpace(line[idx+1:])
	paren := strings.Index(rest, "(")
	if paren <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:paren])
}

func extractFuncName(line string) string {
	// func Name(
	rest := strings.TrimPrefix(line, "func ")
	paren := strings.Index(rest, "(")
	if paren <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:paren])
}

func extractDeclName(line, keyword string) string {
	rest := strings.TrimPrefix(strings.TrimSpace(line), keyword+" ")
	// name = value, name Type, name(, etc.
	for i, ch := range rest {
		if ch == ' ' || ch == '=' || ch == '(' || ch == '\t' {
			return rest[:i]
		}
	}
	return rest
}

func parseBlockSymbols(lines []string, start int, kind string) []Symbol {
	var symbols []Symbol
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == ")" || trimmed == "" {
			if trimmed == ")" {
				break
			}
			continue
		}
		// Each line in block: Name = value or Name Type
		name := extractDeclName(trimmed, "")
		if name != "" && name != "//" {
			symbols = append(symbols, Symbol{Name: name, Kind: kind, Line: i + 1, Signature: trimmed})
		}
	}
	return symbols
}

func extractDeclNameFromBlock(line string) string {
	for i, ch := range line {
		if ch == ' ' || ch == '=' || ch == '\t' {
			return line[:i]
		}
	}
	return line
}

func firstLine(s string) string {
	if idx := strings.Index(s, "\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// ExtractFunctionBody is the legacy function for backward compatibility.
// Prefer ExtractSymbol which handles all symbol types.
func ExtractFunctionBody(source, funcName string) string {
	return ExtractSymbol(source, funcName)
}
