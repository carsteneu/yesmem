package engine

import (
	"strconv"
	"strings"
)

// ParseChangedLines reads the lines a unified diff adds or changes, per file.
//
// Only the right-hand side counts: a removed line no longer exists, so there is
// nothing to mutate or to cover. The input is the text of `git diff -U0`, where
// every hunk is pure change and the header carries the new line numbers.
//
// A file whose diff has no hunk (a rename, a mode change) does not appear.
func ParseChangedLines(diff string) map[string][]int {
	lines := make(map[string][]int)
	file := ""
	expectHeader := false
	next, count := 0, 0

	for _, raw := range strings.Split(diff, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = ""
			expectHeader = true
			next, count = 0, 0
		case strings.HasPrefix(line, "@@"):
			expectHeader = false
			next, count = parseHunkHeader(line)
		case expectHeader && strings.HasPrefix(line, "+++ "):
			// Only a header position counts: an added line whose content starts
			// with "+++" is content, not a path.
			file = diffPath(strings.TrimSpace(line[4:]))
		case count > 0:
			// Only the new side is scoped, so a removal consumes none of the
			// hunk's new lines; a context line advances without being scoped.
			switch {
			case strings.HasPrefix(line, "-"), strings.HasPrefix(line, "\\"):
				continue
			case strings.HasPrefix(line, "+"):
				count--
				if file != "" {
					lines[file] = append(lines[file], next)
				}
				next++
			default:
				count--
				next++
			}
		}
	}
	return lines
}

// parseHunkHeader reads "@@ -a,b +c,d @@" into the first new line and its count.
//
// The count is optional: git omits it when a hunk adds exactly one line.
func parseHunkHeader(header string) (next, count int) {
	body := strings.TrimPrefix(header, "@@")
	if end := strings.Index(body, "@@"); end >= 0 {
		body = body[:end]
	}
	for _, field := range strings.Fields(body) {
		if !strings.HasPrefix(field, "+") {
			continue
		}
		start, size := field[1:], "1"
		if at := strings.Index(start, ","); at >= 0 {
			start, size = start[:at], start[at+1:]
		}
		first, err := strconv.Atoi(start)
		if err != nil {
			return 0, 0
		}
		parsedSize, err := strconv.Atoi(size)
		if err != nil || parsedSize < 0 {
			return 0, 0
		}
		return first, parsedSize
	}
	return 0, 0
}

// diffPath turns a "+++ b/path" target into a repository-relative path. An
// absent target ("/dev/null") is no path at all.
func diffPath(target string) string {
	target = strings.TrimSpace(target)
	if target == "" || target == "/dev/null" {
		return ""
	}
	if tab := strings.IndexByte(target, '\t'); tab >= 0 {
		target = target[:tab]
	}
	for _, prefix := range []string{"b/", "./"} {
		if strings.HasPrefix(target, prefix) {
			return strings.TrimPrefix(target, prefix)
		}
	}
	return target
}

// ChangedLines asks git which lines the checkout adds or changes between two
// revisions.
//
// The result is the scope of a quality claim, so a failed diff is returned as a
// result and never read as "nothing changed": an empty scope would make an
// unprovable claim look provable.
func (c *Ctx) ChangedLines(base, head string) (map[string][]int, GitResult) {
	args := DiffArgs(base, head)
	result := c.Git(args...)
	if result.Code != 0 {
		return nil, result
	}
	return ParseChangedLines(result.Stdout), result
}

// DiffArgs is the argument list of the content diff a quality claim scopes to.
//
// --no-textconv and --no-ext-diff are not cosmetic: both run a program the
// repository configures (.gitattributes diff drivers, diff.external), which would
// execute repository-owned code outside the sandbox. The same invariant the
// repository helpers already hold for hooks and fsmonitor.
func DiffArgs(base, head string) []string {
	return []string{
		"-c", "core.quotePath=false",
		"diff", "-U0", "--no-color", "--no-textconv", "--no-ext-diff",
		base + ".." + head, "--",
	}
}
