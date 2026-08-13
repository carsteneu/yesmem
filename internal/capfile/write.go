package capfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func Render(cf *CapFile) []byte {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("name: %s\n", cf.Name))
	b.WriteString(fmt.Sprintf("description: %q\n", cf.Description))
	if cf.Version > 0 {
		b.WriteString(fmt.Sprintf("version: %d\n", cf.Version))
	}
	if len(cf.Tags) > 0 {
		b.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(cf.Tags, ", ")))
	}
	if len(cf.Requires) > 0 {
		b.WriteString(fmt.Sprintf("requires: [%s]\n", strings.Join(cf.Requires, ", ")))
	}
	if cf.Scope != "" {
		b.WriteString(fmt.Sprintf("scope: %s\n", cf.Scope))
	}
	if cf.Tested {
		b.WriteString("tested: true\n")
	}
	if cf.AutoActive {
		b.WriteString("auto_active: true\n")
	}
	b.WriteString("---\n")

	b.WriteString("\n## Purpose\n\n")
	b.WriteString(cf.Purpose)
	b.WriteString("\n")

	b.WriteString("\n## Scripts\n")
	for _, sc := range cf.Scripts {
		renderScript(&b, sc)
	}

	if cf.DatabaseSQL != "" {
		b.WriteString("\n## Database\n\n")
		b.WriteString("```sql\n")
		b.WriteString(formatSQL(cf.DatabaseSQL))
		b.WriteString("\n```\n")
	}

	if len(cf.Actions) > 0 {
		b.WriteString("\n## Actions\n\n")
		names := make([]string, 0, len(cf.Actions))
		for k := range cf.Actions {
			names = append(names, k)
		}
		sort.Strings(names)
		for i, name := range names {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("### %s\n\n", name))
			body := strings.TrimRight(cf.Actions[name], "\n")
			b.WriteString(body)
			b.WriteString("\n")
		}
	}

	return []byte(b.String())
}

func renderScript(b *strings.Builder, sc Script) {
	b.WriteString(fmt.Sprintf("\n### %s\n", sc.Name))

	// Inline metadata: only emit non-default values.
	if sc.Kind != "" && sc.Kind != "tool" {
		b.WriteString(fmt.Sprintf("kind: %s\n", sc.Kind))
	} else if sc.Kind == "tool" {
		// For clarity, always emit kind: tool when explicitly tool — readers know default but readability wins.
		b.WriteString("kind: tool\n")
	}

	// Only emit explicit runtime if it differs from what the code fence implies.
	derived := deriveRuntime(sc.Lang)
	if sc.Runtime != "" && sc.Runtime != derived {
		b.WriteString(fmt.Sprintf("runtime: %s\n", sc.Runtime))
	}

	// Schema: emit when present and non-default. For repl-tool scripts, the schema
	// is derived from the JS signature on parse — only emit if it's non-derivable.
	if sc.Kind == "tool" && sc.Schema != "" && sc.Schema != "{}" {
		derivedSchema := DeriveSchema(sc.Body)
		if sc.Schema != derivedSchema {
			b.WriteString(fmt.Sprintf("schema: %s\n", sc.Schema))
		}
	}

	if sc.Sandbox != "" {
		b.WriteString(fmt.Sprintf("sandbox: %s\n", sc.Sandbox))
	}
	if sc.Timeout > 0 {
		b.WriteString(fmt.Sprintf("timeout: %d\n", sc.Timeout))
	}

	b.WriteString("\n")

	lang := sc.Lang
	if lang == "" {
		switch sc.Runtime {
		case "repl":
			lang = "javascript"
		case "bash":
			lang = "bash"
		}
	}

	b.WriteString("```" + lang + "\n")
	body := sc.Body
	if lang == "javascript" || lang == "js" {
		body = formatJS(body)
	}
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
}

func WriteFile(cf *CapFile, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cap directory: %w", err)
	}

	data := Render(cf)
	target := filepath.Join(dir, "CAP.md")
	tmp := target + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

func formatSQL(sql string) string {
	stmts := strings.Split(sql, ";")
	var formatted []string
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		formatted = append(formatted, formatCreateStatement(stmt)+";")
	}
	return strings.Join(formatted, "\n\n")
}

func formatCreateStatement(stmt string) string {
	openParen := strings.Index(stmt, "(")
	if openParen < 0 {
		return stmt
	}
	closeParen := strings.LastIndex(stmt, ")")
	if closeParen < 0 || closeParen <= openParen {
		return stmt
	}
	prefix := stmt[:openParen]
	cols := stmt[openParen+1 : closeParen]
	suffix := stmt[closeParen+1:]

	parts := splitColumns(cols)
	if len(parts) <= 1 {
		return stmt
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString("(\n")
	for i, col := range parts {
		b.WriteString("  ")
		b.WriteString(strings.TrimSpace(col))
		if i < len(parts)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(")")
	b.WriteString(suffix)
	return b.String()
}

func splitColumns(cols string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, c := range cols {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, cols[start:i])
				start = i + 1
			}
		}
	}
	if start < len(cols) {
		parts = append(parts, cols[start:])
	}
	return parts
}

func formatJS(code string) string {
	if strings.Contains(code, "\n") {
		return code
	}

	var b strings.Builder
	indent := 0
	inString := byte(0)
	inTemplate := false

	for i := 0; i < len(code); i++ {
		c := code[i]

		if inString != 0 {
			b.WriteByte(c)
			if c == inString && (i == 0 || code[i-1] != '\\') {
				inString = 0
			}
			continue
		}
		if c == '`' {
			inTemplate = !inTemplate
			b.WriteByte(c)
			continue
		}
		if inTemplate {
			b.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' {
			inString = c
			b.WriteByte(c)
			continue
		}

		switch c {
		case '{':
			b.WriteString(" {\n")
			indent++
			writeIndent(&b, indent)
		case '}':
			b.WriteByte('\n')
			indent--
			if indent < 0 {
				indent = 0
			}
			writeIndent(&b, indent)
			b.WriteByte('}')
		case ';':
			b.WriteByte(';')
			if i+1 < len(code) && code[i+1] != '\n' && code[i+1] != '}' {
				b.WriteByte('\n')
				writeIndent(&b, indent)
			}
		case '\n':
			b.WriteByte('\n')
			writeIndent(&b, indent)
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimRight(b.String(), " \t\n")
}

func writeIndent(b *strings.Builder, level int) {
	for i := 0; i < level; i++ {
		b.WriteString("  ")
	}
}
