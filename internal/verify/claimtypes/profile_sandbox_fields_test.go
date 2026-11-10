package claimtypes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// writeProfile plants a project profile in a temporary repository root.
func writeProfile(t *testing.T, content string) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ProfilePath), []byte(content), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return repo
}

// The tool declarations reach the language entry: a profile that names them and
// a claim that never sees them would be two different profiles.
func TestTheProfileCarriesTheSandboxToolDeclarations(t *testing.T) {
	repo := writeProfile(t, `languages:
  - name: go
    requires: ["bun"]
    sandbox_readonly: ["/home/example/.bun/bin"]
    sandbox_cache: "/home/example/.cache/yesmem-build"
`)

	resolved, err := ResolveProfile(repo, DefaultLanguages())
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	var goEntry *engine.Language
	for index := range resolved.Languages {
		if resolved.Languages[index].Name == "go" {
			goEntry = &resolved.Languages[index]
		}
	}
	if goEntry == nil {
		t.Fatal("the go entry is missing")
	}
	if len(goEntry.Requires) != 1 || goEntry.Requires[0] != "bun" {
		t.Errorf("requires = %v, want [bun]", goEntry.Requires)
	}
	if len(goEntry.SandboxReadOnly) != 1 || goEntry.SandboxReadOnly[0] != "/home/example/.bun/bin" {
		t.Errorf("sandbox_readonly = %v, want the declared bin directory", goEntry.SandboxReadOnly)
	}
	if goEntry.SandboxCache != "/home/example/.cache/yesmem-build" {
		t.Errorf("sandbox_cache = %q, want the declared root", goEntry.SandboxCache)
	}
}

// requires names programs, not paths: a directory or a relative path in that
// list would be a tool the verifier cannot look up, and a silently dead entry is
// worse than a refused profile.
func TestTheProfileRefusesARequirementThatIsNotAProgramName(t *testing.T) {
	for name, value := range map[string]string{
		"a path":          "/usr/bin/bun",
		"a relative path": "vendor/bin/bun",
		"a parent":        "..",
		"an empty name":   "",
	} {
		t.Run(name, func(t *testing.T) {
			repo := writeProfile(t, `languages:
  - name: go
    requires: ["`+value+`"]
`)
			if _, err := ResolveProfile(repo, DefaultLanguages()); err == nil {
				t.Fatalf("requires %q was accepted", value)
			} else if !strings.Contains(err.Error(), "requires") {
				t.Fatalf("error = %v, want it to name the field", err)
			}
		})
	}
}

// An empty declaration is a statement: it removes the built-in list instead of
// keeping it. That is the same pointer rule every other field follows.
func TestTheProfileCanClearTheRequirements(t *testing.T) {
	builtin := []engine.Language{{
		Name:        "go",
		TestCommand: "go test ./...",
		Requires:    []string{"bun"},
	}}
	repo := writeProfile(t, `languages:
  - name: go
    requires: []
`)

	resolved, err := ResolveProfile(repo, builtin)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(resolved.Languages) != 1 {
		t.Fatalf("languages = %d, want the single built-in entry", len(resolved.Languages))
	}
	if len(resolved.Languages[0].Requires) != 0 {
		t.Fatalf("requires = %v, want the cleared list", resolved.Languages[0].Requires)
	}
}

// An explicit empty cache is a statement too: the project runs without a
// persistent cache even when the built-in entry names one.
func TestTheProfileCanClearTheCache(t *testing.T) {
	builtin := []engine.Language{{
		Name:         "go",
		TestCommand:  "go test ./...",
		SandboxCache: "/home/example/.cache/yesmem-build",
	}}
	repo := writeProfile(t, `languages:
  - name: go
    sandbox_cache: ""
`)

	resolved, err := ResolveProfile(repo, builtin)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if resolved.Languages[0].SandboxCache != "" {
		t.Fatalf("sandbox_cache = %q, want the cleared value", resolved.Languages[0].SandboxCache)
	}
}

// What init writes is what a later read gets back: a writer that drops the new
// fields would publish a profile that silently forgets its toolchain.
func TestTheProfileWriterKeepsTheSandboxDeclarations(t *testing.T) {
	repo := t.TempDir()
	languages := []engine.Language{{
		Name:            "go",
		TestCommand:     "go test ./...",
		Requires:        []string{"bun"},
		SandboxReadOnly: []string{"/home/example/.bun/bin"},
		SandboxCache:    "/home/example/.cache/yesmem-build",
	}}
	if _, err := WriteProfileFile(repo, languages); err != nil {
		t.Fatalf("WriteProfileFile: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(repo, ProfilePath))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	for _, want := range []string{"requires:", "bun", "sandbox_readonly:", "sandbox_cache:"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("profile = %q, want %q", string(written), want)
		}
	}
	resolved, err := ResolveProfile(repo, nil)
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if len(resolved.Languages) != 1 || resolved.Languages[0].SandboxCache == "" ||
		len(resolved.Languages[0].Requires) != 1 {
		t.Fatalf("languages = %+v, want the written declarations back", resolved.Languages)
	}
}
