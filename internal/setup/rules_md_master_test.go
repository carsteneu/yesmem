package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/buildinfo"
)

// digestOf is the body hash the provenance marker carries, computed here rather than taken from the
// implementation: the marker is a wire format, and a test that borrows the writer's own helper would agree
// with a broken marker as readily as with a correct one.
func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// markerOf renders a provenance line for a body, as an older release would have written it.
func markerOf(version, body string) string {
	return "<!-- yesmem-rules version=" + version + " body=sha256:" + digestOf(body) + " -->\n"
}

// The master is the one place the standard rules are maintained, and the model reads it as the prompt. A
// catalogue without a Decision Policy leaves the model to guess which rules may BLOCK, which is how the
// PR-merge rule became a notice on every fresh installation (#121004, #120065). The numbered set the policy
// cites has to be the generated one: 1 commits, 2 secrets, 25 destructive commands, 32 PR merge.
func TestInstalledMasterCarriesTheDecisionPolicyAndItsNumbering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := installRULESmd(filepath.Join(home, ".claude", "yesmem"))
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read master: %v", err)
	}
	text := string(content)

	for _, want := range []string{
		"rule enforcement system",
		"Never auto-commit",
		"Never commit secrets",
		"No workarounds",
		"feature branch",
		"make deploy",
		"Memory queries",
		"Conventional Commits",
		"root cause",
		"destructive Bash",
		"API contracts",
		"## Decision Policy",
		"## Skill Catalog",
		"priority: MUST",
		"triggers:",
		"skill: subagent-driven-development",
		"skill: yesmem-agents",
		"skill: yesmem-cap-builder",
		"skill: yesmem-config",
		"skill: yesmem-remember",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("master missing %q", want)
		}
	}

	// The policy has to be readable by the guard's own parser, not merely present: blockableRuleNumbers
	// scans its bullets for the word BLOCK and for cited numbers.
	if set := blockableSetOf(t, path); len(set) != 4 || !set[1] || !set[2] || !set[25] || !set[32] {
		t.Errorf("master policy yields blockable set %v, want {1,2,25,32}", set)
	}
}

// blockableSetOf reads the master the way internal/guard reads it: bullets that mention BLOCK, restricted to
// the rule numbers the file numbers itself. It folds continuation lines into their bullet and joins the
// citations of a wrapped bullet, because a per-line read loses a rule written on the second line (YM-456) —
// the parser is duplicated here rather than imported because it is unexported in internal/guard.
func blockableSetOf(t *testing.T, path string) map[int]bool {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	start := strings.Index(text, "## Decision Policy")
	if start < 0 {
		return nil
	}
	policy := text[start:]
	if end := strings.Index(policy, "## End of Decision Policy"); end >= 0 {
		policy = policy[:end]
	} else if end := strings.Index(policy, "\n## "); end >= 0 {
		policy = policy[:end]
	}

	known := map[int]bool{}
	for _, match := range regexp.MustCompile(`(?m)^\s*(\d+)\.\s`).FindAllStringSubmatch(text, -1) {
		if n, convErr := strconv.Atoi(match[1]); convErr == nil {
			known[n] = true
		}
	}

	var bullets []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			bullets = append(bullets, current.String())
			current.Reset()
		}
	}
	for _, line := range strings.Split(policy, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || trimmed == "" {
			flush()
			current.WriteString(trimmed)
			continue
		}
		if current.Len() > 0 {
			current.WriteString(" ")
			current.WriteString(trimmed)
		}
	}
	flush()

	set := map[int]bool{}
	citation := regexp.MustCompile(`(?i)\b(rules?)\b[\s#]*(?:number|no\.?|nr\.?)?[\s\-]*(#?\d+(?:(?:\s*,\s*|\s+and\s+|\s*&\s*)#?\d+)*)`)
	for _, bullet := range bullets {
		if !strings.Contains(strings.ToUpper(bullet), "BLOCK") {
			continue
		}
		for _, match := range citation.FindAllStringSubmatchIndex(bullet, -1) {
			word, list := bullet[match[2]:match[3]], bullet[match[4]:match[5]]
			// The singular form takes only the first number: behind "Rule 1," the comma is prose. Copied from
			// citedRuleNumbers in internal/guard/policy.go, because a parser that is merely similar proves
			// compatibility with a lookalike instead of with the guard.
			if !strings.EqualFold(word, "rules") {
				if comma := strings.Index(list, ","); comma >= 0 {
					list = list[:comma]
				}
			}
			for _, digits := range regexp.MustCompile(`\d+`).FindAllString(list, -1) {
				if n, convErr := strconv.Atoi(digits); convErr == nil && known[n] {
					set[n] = true
				}
			}
		}
	}
	return set
}

// The rendered master is hashed into its own provenance marker, so the render has to be reproducible. It was
// not: the trigger extraction walked a Go map, whose iteration order is randomised, so every run produced a
// different catalogue of the same length — invisible while the file was written once, and fatal to a refresh,
// which would have treated its own output as an edit and stopped refreshing forever.
func TestRenderRulesmdBodyIsReproducible(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := renderRulesmdBody()
	for i := 0; i < 10; i++ {
		if again := renderRulesmdBody(); again != first {
			t.Fatalf("render %d differs from the first: the catalogue order is not stable", i)
		}
	}
}

// An unreadable master is not an absent one. Turning a read error into a write would destroy the file the
// function exists to protect, and a permission problem would look like a successful install.
func TestInstallRULESmdLeavesAnUnreadableMasterAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the read permission cannot be withheld")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	rulesPath := filepath.Join(dataDir, "RULES.md")
	writeRulesFile(t, rulesPath, "the operator's own master\n")
	if err := os.Chmod(rulesPath, 0o200); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want the unreadable master left as it is", path)
	}
	if err := os.Chmod(rulesPath, 0o644); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	content, readErr := os.ReadFile(rulesPath)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(content) != "the operator's own master\n" {
		t.Error("the unreadable master was overwritten")
	}
}

// The overlay has the same requirement: it is written once and read as the project's word, so two runs must not
// describe the same checkout differently.
func TestRenderProjectRulesmdIsReproducible(t *testing.T) {
	projectDir := t.TempDir()
	for _, file := range []string{"go.mod", "package.json", "requirements.txt"} {
		writeRulesFile(t, filepath.Join(projectDir, file), "x\n")
	}

	first := renderProjectRulesmd(detectProjectType(projectDir))
	for i := 0; i < 10; i++ {
		if again := renderProjectRulesmd(detectProjectType(projectDir)); again != first {
			t.Fatalf("render %d differs from the first: the detected type order is not stable", i)
		}
	}
}

// A release ships new standard rules. Refusing to replace the master meant a machine could never receive
// them (#121017), and replacing it blindly would discard the operator's narrowing of the Decision Policy
// (#121015). The provenance marker decides: a body that still hashes to the digest in its own marker was not
// touched by anyone but the generator.
func TestInstallRULESmdRefreshesAProvablyUnchangedMaster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	rulesPath := filepath.Join(dataDir, "RULES.md")
	oldBody := "# Rules\n\n1. A rule of the previous release.\n"
	writeRulesFile(t, rulesPath, markerOf("0.0.0", oldBody)+oldBody)

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	if path != rulesPath {
		t.Fatalf("path = %q, want the master refreshed", path)
	}
	content, _ := os.ReadFile(rulesPath)
	text := string(content)
	if strings.Contains(text, "A rule of the previous release") {
		t.Error("the previous release's body survived the refresh")
	}
	if !strings.Contains(text, "Never auto-commit") {
		t.Error("the master does not carry the current standard rules")
	}
	if !strings.HasPrefix(text, "<!-- yesmem-rules version="+buildinfo.Version) {
		t.Errorf("marker was not restamped with the current version: %q", firstLine(text))
	}
}

// A file whose body no longer hashes to the digest in its marker was edited after it was written. It belongs
// to the operator, and a setup run may not touch it — not even to offer a .new copy next to it.
func TestInstallRULESmdKeepsALocallyEditedMaster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	rulesPath := filepath.Join(dataDir, "RULES.md")
	writerBody := "# Rules\n\n1. Never auto-commit.\n"
	edited := markerOf("0.0.0", writerBody) + "# Rules\n\n1. Our own rule that replaces the standard set.\n"
	writeRulesFile(t, rulesPath, edited)

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want the edited master kept", path)
	}
	content, _ := os.ReadFile(rulesPath)
	if string(content) != edited {
		t.Error("the edited master was overwritten")
	}
	if _, statErr := os.Stat(rulesPath + ".new"); statErr == nil {
		t.Error("a .new file was written next to a locally edited master")
	}
}

// The second setup run on an untouched machine has nothing to write, and rewriting the identical bytes would
// only churn the file's timestamp.
func TestInstallRULESmdIsANoOpWhenTheMasterIsCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	if _, err := installRULESmd(dataDir); err != nil {
		t.Fatalf("first installRULESmd: %v", err)
	}
	rulesPath := filepath.Join(dataDir, "RULES.md")
	before, err := os.Stat(rulesPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("second installRULESmd: %v", err)
	}
	after, statErr := os.Stat(rulesPath)
	if statErr != nil {
		t.Fatalf("stat after: %v", statErr)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the current master was rewritten")
	}
	if path != "" {
		t.Errorf("path = %q, want the API to say nothing was written", path)
	}
}

// A project states its deviations in a file that carries no numbers, or a citation of a numbered rule would
// start naming a rule of the project instead of the standard set. The language rules of the checkout are the
// deviation the wizard can detect on its own.
func TestSeedProjectRulesWritesAnUnnumberedOverlay(t *testing.T) {
	projectDir := t.TempDir()
	writeRulesFile(t, filepath.Join(projectDir, "go.mod"), "module example\n")

	path, err := seedProjectRules(projectDir)
	if err != nil {
		t.Fatalf("seedProjectRules: %v", err)
	}
	if want := filepath.Join(projectDir, "PROJECT_RULES.md"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	content, _ := os.ReadFile(path)
	text := string(content)

	if !strings.Contains(text, "Go: always TDD") {
		t.Errorf("overlay missing the detected language rule:\n%s", text)
	}
	if numbered := regexp.MustCompile(`(?m)^\s*\d+\.\s`).FindString(text); numbered != "" {
		t.Errorf("overlay carries a numbered rule (%q): a citation would no longer name the standard set", numbered)
	}
	if strings.Contains(text, "## Decision Policy") {
		t.Error("overlay carries a Decision Policy: the enforceable set belongs to the installation")
	}
}

// A project with a complete catalogue of its own is left alone. Its numbered rules are the ones the model is
// judged against, and an overlay next to it would never be read anyway.
func TestSeedProjectRulesLeavesACompleteProjectFileAlone(t *testing.T) {
	projectDir := t.TempDir()
	writeRulesFile(t, filepath.Join(projectDir, "go.mod"), "module example\n")
	writeRulesFile(t, filepath.Join(projectDir, "RULES.md"), "1. Our own catalogue.\n")

	path, err := seedProjectRules(projectDir)
	if err != nil {
		t.Fatalf("seedProjectRules: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want no overlay beside a complete project file", path)
	}
	if _, statErr := os.Stat(filepath.Join(projectDir, "PROJECT_RULES.md")); statErr == nil {
		t.Error("an overlay was created beside a complete project file")
	}
}

// A deviation file the project maintains itself is never a wizard artifact, and a re-run may not replace it.
func TestSeedProjectRulesKeepsAnExistingOverlay(t *testing.T) {
	projectDir := t.TempDir()
	writeRulesFile(t, filepath.Join(projectDir, "go.mod"), "module example\n")
	existing := filepath.Join(projectDir, "PROJECT_RULES.md")
	writeRulesFile(t, existing, "- Deploy target is staging.\n")

	path, err := seedProjectRules(projectDir)
	if err != nil {
		t.Fatalf("seedProjectRules: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want the existing overlay kept", path)
	}
	content, _ := os.ReadFile(existing)
	if string(content) != "- Deploy target is staging.\n" {
		t.Error("the project's own overlay was overwritten")
	}
}

// A directory with nothing to declare gets no file: an empty overlay is noise on every checkout that has no
// deviation to state.
func TestSeedProjectRulesWithoutAProjectTypeWritesNothing(t *testing.T) {
	projectDir := t.TempDir()

	path, err := seedProjectRules(projectDir)
	if err != nil {
		t.Fatalf("seedProjectRules: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want no overlay without a detected project type", path)
	}
}

// A checkout can ship PROJECT_RULES.md as a symlink to a file that does not exist yet. Stat follows the link, so
// the seed would create the operator's file at the target instead of leaving the checkout as it found it.
func TestSeedProjectRulesRefusesASymlinkedOverlay(t *testing.T) {
	projectDir := t.TempDir()
	writeRulesFile(t, filepath.Join(projectDir, "go.mod"), "module example\n")
	target := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Symlink(target, filepath.Join(projectDir, "PROJECT_RULES.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	path, err := seedProjectRules(projectDir)
	if err != nil {
		t.Fatalf("seedProjectRules: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want the symlink left alone", path)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("the seed wrote through the symlink")
	}
}

// The same guard on the write itself, so no caller can reintroduce the path by accident.
func TestWriteRulesmdFileRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	link := filepath.Join(dir, "RULES.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	if err := writeRulesmdFile(link, "generated\n"); err == nil {
		t.Fatal("writeRulesmdFile wrote through a symlink")
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatal("the target was created")
	}
}

func writeRulesFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}
