// Package repo locates the main repository root for a directory.
// Worktree sessions (.git is a FILE pointing at <main>/.git/worktrees/<name>)
// resolve to the main repository, so all learnings from a worktree session
// are attributed to one project.
package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// Root returns the main repository root for dir, or "" if dir is not inside a
// git working tree. Worktrees (.git is a file with "gitdir: <main>/.git/worktrees/<name>")
// resolve to the main repository path. Limitation: gitdirs not laid out as
// <main>/.git/worktrees/<name> (bare repos, submodules) resolve to the gitdir's
// grandparent directory — best effort, see repo_test.go for covered shapes.
func Root(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		probe := filepath.Join(abs, ".git")
		if info, err := os.Stat(probe); err == nil {
			if info.IsDir() {
				return abs
			}
			if data, err := os.ReadFile(probe); err == nil {
				line := strings.TrimSpace(string(data))
				if gitDir, ok := strings.CutPrefix(line, "gitdir: "); ok {
					if !filepath.IsAbs(gitDir) {
						gitDir = filepath.Join(abs, gitDir)
					}
					return filepath.Clean(filepath.Join(gitDir, "..", "..", ".."))
				}
			}
			return abs // .git file, unparseable — best effort
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}
