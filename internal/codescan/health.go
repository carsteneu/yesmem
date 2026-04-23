package codescan

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CodeHealth holds health signals extracted from a project scan.
type CodeHealth struct {
	TodoCount         int // TODO + FIXME + HACK comments
	FilesWithoutTests int // Go source files without corresponding _test.go
}

// ScanHealth walks the project directory and collects health signals.
func ScanHealth(rootDir string) CodeHealth {
	var health CodeHealth

	// Track Go files and their test counterparts per directory
	goFiles := make(map[string]map[string]bool) // dir -> set of base names (without .go)
	testFiles := make(map[string]map[string]bool)

	filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(rootDir, path)
		if shouldSkipDir(rel, info) {
			return filepath.SkipDir
		}
		if info.IsDir() || !isSourceFile(path) {
			return nil
		}

		// Count TODOs
		health.TodoCount += countTodos(path)

		// Track Go test coverage
		if strings.HasSuffix(path, ".go") {
			dir := filepath.Dir(rel)
			base := filepath.Base(rel)

			if strings.HasSuffix(base, "_test.go") {
				if testFiles[dir] == nil {
					testFiles[dir] = make(map[string]bool)
				}
				// handler_test.go -> handler
				stripped := strings.TrimSuffix(base, "_test.go")
				testFiles[dir][stripped] = true
			} else {
				if goFiles[dir] == nil {
					goFiles[dir] = make(map[string]bool)
				}
				stripped := strings.TrimSuffix(base, ".go")
				goFiles[dir][stripped] = true
			}
		}

		return nil
	})

	// Count Go files without tests
	for dir, files := range goFiles {
		tests := testFiles[dir]
		for name := range files {
			if tests == nil || !tests[name] {
				health.FilesWithoutTests++
			}
		}
	}

	return health
}

// countTodos counts TODO, FIXME, HACK comments in a file.
func countTodos(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.ToUpper(scanner.Text())
		if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") || strings.Contains(line, "HACK") {
			count++
		}
	}
	return count
}

// RenderCodeHealth renders health signals as a markdown section.
// Returns empty string if everything is clean.
func RenderCodeHealth(health CodeHealth) string {
	if health.TodoCount == 0 && health.FilesWithoutTests == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("### Code Health\n")

	if health.TodoCount > 0 {
		b.WriteString(fmt.Sprintf("- %d TODO/FIXME/HACK comments\n", health.TodoCount))
	}
	if health.FilesWithoutTests > 0 {
		b.WriteString(fmt.Sprintf("- %d Go files without test coverage\n", health.FilesWithoutTests))
	}
	b.WriteString("\n")

	return b.String()
}
