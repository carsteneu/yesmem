package verify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The eval corpus embeds commit hashes of a deterministically built fixture
// repository. This file rebuilds it: fixed content, fixed identity, fixed
// dates, sha1 objects. Rebuilding in another implementation reproduces the same
// hashes, which is what lets the corpus double as a contract between the Python
// reference and this port.
//
// The fixture follows the reference's build order up to the merge commit. The
// reference adds Lean sources afterwards; no ported case references them, so
// they are not built here (see testdata/eval/NOTICE.md).
const (
	fixtureName  = "Fixture"
	fixtureEmail = "fixture@example.com"
)

// fixture is a built fixture repository with its named commits.
type fixture struct {
	Root    string
	Repo    string
	Remote  string
	Commits map[string]string
}

// evalFixture builds the fixture once per test binary run: it is expensive
// compared to a case and identical for every case.
var cachedFixture *fixture

func fixtureForTest(t *testing.T) *fixture {
	t.Helper()
	if cachedFixture != nil {
		return cachedFixture
	}
	root, err := os.MkdirTemp(fixtureTmpRoot(t), "fixture-")
	if err != nil {
		t.Fatalf("cannot create the fixture root: %v", err)
	}
	built, buildErr := buildFixture(t, root)
	if buildErr != nil {
		t.Fatalf("cannot build the fixture: %v", buildErr)
	}
	cachedFixture = built
	return cachedFixture
}

// fixtureTmpRoot is the project-local temp root. Nothing here writes to /tmp:
// the worktree carries its own scratch space.
func fixtureTmpRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp"))
	if err != nil {
		t.Fatalf("cannot resolve the project temp root: %v", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("cannot create the project temp root: %v", err)
	}
	return root
}

func buildFixture(t *testing.T, root string) (*fixture, error) {
	t.Helper()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	for _, dir := range []string{home, repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	commits := make(map[string]string)

	run := func(minute int, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = fixtureEnv(home, minute)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, string(output))
		}
		return strings.TrimSpace(string(output)), nil
	}
	gitIn := func(minute int, dir string, args ...string) (string, error) {
		return run(minute, append([]string{"git", "-C", dir}, args...)...)
	}
	write := func(name, content string) error {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	commit := func(minute int, key, message string) error {
		if _, err := gitIn(minute, repo, "add", "-A"); err != nil {
			return err
		}
		if _, err := gitIn(minute, repo, "commit", "-q", "-m", message); err != nil {
			return err
		}
		hash, err := gitIn(minute, repo, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		commits[key] = hash
		return nil
	}

	if _, err := run(0, "git", "-c", "init.defaultBranch=main", "init", "-q", repo); err != nil {
		return nil, err
	}

	steps := []struct {
		writes  map[string]string
		key     string
		message string
	}{
		{map[string]string{"base.txt": "base\n"}, "base", "base"},
		{map[string]string{"good.txt": "good\n", "test_ok.py": fixtureTestOK}, "good", "good"},
		{map[string]string{"test_bad.py": fixtureTestBad}, "bad", "bad tests"},
		{map[string]string{"alpha.txt": "alpha\n", "beta.txt": "beta\n"}, "scoped", "scoped changes"},
	}
	minute := 1
	for _, step := range steps {
		for name, content := range step.writes {
			if err := write(name, content); err != nil {
				return nil, err
			}
		}
		if err := commit(minute, step.key, step.message); err != nil {
			return nil, err
		}
		minute++
	}

	if _, err := run(0, "git", "-c", "init.defaultBranch=main", "init", "-q", "--bare", remote); err != nil {
		return nil, err
	}
	if _, err := gitIn(0, repo, "remote", "add", "origin", remote); err != nil {
		return nil, err
	}
	if _, err := gitIn(0, repo, "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	if _, err := gitIn(0, repo, "branch", "feature", commits["bad"]); err != nil {
		return nil, err
	}
	if _, err := gitIn(0, repo, "push", "-q", "origin", "feature"); err != nil {
		return nil, err
	}

	if err := write("after.txt", "after\n"); err != nil {
		return nil, err
	}
	if err := commit(5, "unpushed", "unpushed"); err != nil {
		return nil, err
	}
	if err := write("test_bad.py", fixtureTestBadFixed); err != nil {
		return nil, err
	}
	if err := commit(6, "fixed", "fix failing test"); err != nil {
		return nil, err
	}
	if err := write("proofboy/turing.py", fixtureTuring); err != nil {
		return nil, err
	}
	if err := commit(7, "tool", "fixture tool"); err != nil {
		return nil, err
	}
	if err := write("proofboy/experiments/erdos_straus.py", fixtureErdosStraus); err != nil {
		return nil, err
	}
	if err := commit(8, "experiment", "fixture experiment"); err != nil {
		return nil, err
	}

	// A real merge commit for the [MERGE] cases: a topic branch off "good",
	// merged without fast-forward. The pushed refs stay where they were, so
	// the branch tip stays at the merge's second parent, which is the shape
	// the checker confirms.
	if _, err := gitIn(0, repo, "checkout", "-q", "-b", "topic", commits["good"]); err != nil {
		return nil, err
	}
	if err := write("topic.txt", "topic\n"); err != nil {
		return nil, err
	}
	if err := commit(9, "topic", "topic work"); err != nil {
		return nil, err
	}
	if _, err := gitIn(0, repo, "checkout", "-q", "main"); err != nil {
		return nil, err
	}
	if _, err := gitIn(10, repo, "merge", "-q", "--no-ff", "-m", "merge topic", "topic"); err != nil {
		return nil, err
	}
	merge, err := gitIn(0, repo, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	commits["merge"] = merge

	return &fixture{Root: root, Repo: repo, Remote: remote, Commits: commits}, nil
}

// fixtureEnv isolates the fixture build from the host: every GIT_* variable is
// dropped, then HOME, identity, dates and the object format are pinned. An
// ambient GIT_DEFAULT_HASH or a host gitconfig would otherwise flip the hashes.
func fixtureEnv(home string, minute int) []string {
	env := make([]string, 0, len(os.Environ())+12)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, entry)
	}
	date := fmt.Sprintf("2026-09-01T12:%02d:00+0000", minute)
	return append(env,
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_DEFAULT_HASH=sha1",
		"GIT_AUTHOR_NAME="+fixtureName,
		"GIT_AUTHOR_EMAIL="+fixtureEmail,
		"GIT_COMMITTER_NAME="+fixtureName,
		"GIT_COMMITTER_EMAIL="+fixtureEmail,
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
	)
}

const (
	fixtureTestOK = "import unittest\n" +
		"\n\n" +
		"class TestOk(unittest.TestCase):\n" +
		"    def test_addition(self):\n" +
		"        self.assertEqual(1 + 1, 2)\n"

	fixtureTestBad = "import unittest\n" +
		"\n\n" +
		"class TestBad(unittest.TestCase):\n" +
		"    def test_fails(self):\n" +
		"        self.assertEqual(1, 2)\n"

	fixtureTestBadFixed = "import unittest\n" +
		"\n\n" +
		"class TestBad(unittest.TestCase):\n" +
		"    def test_now_passes(self):\n" +
		"        self.assertEqual(1, 1)\n"

	fixtureTuring = "\"\"\"Fixture stub: prints a fixed line, arguments are ignored.\"\"\"\n" +
		"print(\"fixture\")\n"

	fixtureErdosStraus = "\"\"\"Fixture stub: prints a fixed line, arguments are ignored.\"\"\"\n" +
		"print(\"fixture erdos-straus\")\n"
)
