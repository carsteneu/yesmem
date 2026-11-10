package guard

import (
	"regexp"
	"strings"
)

// destructivePatterns are hardcoded regexes for tool calls that must BLOCK
// regardless of model output. These bypass the DeepSeek roundtrip entirely
// — no network call, no downgrade — so destructive shell commands can never
// reach the OS even when the model would emit PASS.
//
// Scoped to Bash/REPL only (Edit/Write don't shell out). Each pattern is
// carefully bounded to avoid catching benign use:
//   - `git push origin feature/x` is fine; `git push --force origin main` is not
//   - `DROP TABLE` is always blocked (no benign use in tool calls)
//
// A deletion is not decided here: deletion.go judges every `rm` on its own, including the
// ones these regexes would let through (`rm -rf /tmp/foo`). What is caught here is the
// shape that gate does not parse — a script a shell is told to read, or a REPL cell whose
// shell string is not a literal — and the two root/home forms below, as a second line.
var destructivePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"rm -rf on filesystem root or home", regexp.MustCompile(`(?i)\brm\s+(?:-[a-z]*\s+)*-[a-z]*r[a-z]*f?[a-z]*\s+(?:/|~|\$HOME)(?:[\s"';)\\]|$)`)},
	{"git force-push to main/master", regexp.MustCompile(`(?i)\bgit\s+push\s+(?:--force(?:-with-lease)?|--no-verify|-f)\b[^|;&]*\b(?:origin\s+)?(?:main|master|production|prod)\b`)},
	{"git reset --hard on main/master", regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\s+\S*\b(?:main|master|production|prod)\b`)},
	{"DROP TABLE/DATABASE", regexp.MustCompile(`(?i)\bDROP\s+(?:TABLE|DATABASE|SCHEMA)\b`)},
	{"fork bomb", regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`)},
	{"dd to raw disk", regexp.MustCompile(`(?i)\bdd\s+[^|;&]*\bof=/dev/(?:(?:sd|hd|vd|xvd)[a-z]\d*|nvme\d+(?:n\d+)?(?:p\d+)?)\b`)},
	{"mkfs on raw disk", regexp.MustCompile(`(?i)\bmkfs(?:\.\w+)?\s+/dev/(?:(?:sd|hd|vd|xvd)[a-z]\d*|nvme\d+(?:n\d+)?(?:p\d+)?)\b`)},
	{"redirect to raw disk", regexp.MustCompile(`>\s*/dev/(?:(?:sd|hd|vd|xvd)[a-z]\d*|nvme\d+(?:n\d+)?(?:p\d+)?)\b`)},
	{"chmod 777 system root", regexp.MustCompile(`(?i)\bchmod\s+-R?\s+0*777\s+/(?:\s|$)`)},
	{"chown to root recursively at /", regexp.MustCompile(`(?i)\bchown\s+-R\s+\S+\s+/(?:\s|$)`)},
}

// matchDestructivePattern returns the name of the first pattern that matches,
// or "" if none match. toolDesc must already include the tool prefix
// ("Bash: " or "REPL: ") from describeToolCall.
//
// The match runs on the line with its inert quotes blanked: a commit message or
// an echo that quotes a rule reads like the command it warns about, and
// refusing it blocks the report instead of the act.
func matchDestructivePattern(toolDesc string) string {
	text := inertQuotedText(toolDesc)
	for _, p := range destructivePatterns {
		if p.re.MatchString(text) {
			return p.name
		}
	}
	return ""
}

// shellRunners execute the string they are handed, so a quoted span that
// follows one of them is a command, not text. The clients and interpreters
// belong here for the same reason: `psql -c 'DROP TABLE users'` runs its
// argument, while `grep "DROP TABLE" docs/` only looks for it.
var shellRunners = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true,
	"ksh": true, "eval": true, "source": true, ".": true,
	"psql": true, "mysql": true, "mariadb": true, "sqlite3": true,
	"mongosh": true, "mongo": true, "redis-cli": true,
	"python": true, "python3": true, "node": true, "perl": true,
	"ruby": true, "php": true,
}

// inertQuotedText blanks quoted spans of a Bash line that nothing will run.
// A REPL call is returned unchanged: its quotes hold the payload a shell
// function receives, so those strings are live. The text keeps its length, so
// a pattern that spans a quote boundary still lines up.
func inertQuotedText(toolDesc string) string {
	const prefix = "Bash: "
	if !strings.HasPrefix(toolDesc, prefix) {
		return toolDesc
	}
	cmd := toolDesc[len(prefix):]
	var b strings.Builder
	b.Grow(len(toolDesc))
	b.WriteString(prefix)
	segmentStart := 0
	for i := 0; i < len(cmd); i++ {
		switch cmd[i] {
		case '\'', '"':
			end := afterClosingQuote(cmd, i+1, cmd[i])
			if runsAShell(cmd[segmentStart:i]) {
				b.WriteString(cmd[i:end])
			} else {
				b.WriteByte(cmd[i])
				for k := i + 1; k < end-1; k++ {
					b.WriteByte(' ')
				}
				if end-1 > i {
					b.WriteByte(cmd[i])
				}
			}
			i = end - 1
		case ';', '|', '&', '\n':
			b.WriteByte(cmd[i])
			segmentStart = i + 1
		default:
			b.WriteByte(cmd[i])
		}
	}
	return b.String()
}

// afterClosingQuote returns the index just past the closing quote, or the end
// of the string when the quote is never closed.
func afterClosingQuote(s string, from int, quote byte) int {
	for i := from; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i + 1
		}
	}
	return len(s)
}

// runsAShell reports whether the segment hands its text to an interpreter.
// Only tokens before the quote count: a runner name inside the quoted text is
// itself just text.
func runsAShell(segment string) bool {
	for _, field := range strings.Fields(segment) {
		name := strings.Trim(field, `"'`)
		if shellRunners[name] {
			return true
		}
	}
	return false
}
