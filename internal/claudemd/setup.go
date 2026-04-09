package claudemd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SetupProject injects the @-import into CLAUDE.md and adds to .gitignore.
func SetupProject(projectDir, outputFileName string) error {
	if outputFileName == "" {
		outputFileName = "yesmem-ops.md"
	}
	importRef := fmt.Sprintf("@.claude/%s", outputFileName)

	claudeMdPath := filepath.Join(projectDir, "CLAUDE.md")
	if err := ensureImportInClaudeMd(claudeMdPath, importRef); err != nil {
		return err
	}

	gitignorePath := filepath.Join(projectDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		if err := ensureLineInFile(gitignorePath, fmt.Sprintf(".claude/%s", outputFileName)); err != nil {
			return err
		}
	}
	return nil
}

func ensureImportInClaudeMd(path, importRef string) error {
	var existing string
	data, err := os.ReadFile(path)
	if err == nil {
		existing = string(data)
	}
	if strings.Contains(existing, importRef) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open CLAUDE.md: %w", err)
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(existing, "\n") {
		fmt.Fprintln(f)
	}
	_, err = fmt.Fprintln(f, importRef)
	return err
}

func ensureLineInFile(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == line {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}
