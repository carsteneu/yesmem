package codescan

import (
	"strings"
	"testing"
)

const testSource = `package proxy

import "fmt"

var skillEvalBlock = ` + "`" + `some template
with multiple lines` + "`" + `

var singleLineVar = 42

const defaultTimeout = 30

const (
	modeA = "alpha"
	modeB = "beta"
)

type Generator struct {
	store    Store
	config   Config
	language string
}

type StringAlias = string

func (g *Generator) Generate(ctx context.Context) string {
	result := g.store.Load()
	return fmt.Sprintf("%s: %s", g.language, result)
}

func buildSkillEvalBlock(skills []SkillInfo) string {
	var b strings.Builder
	for _, s := range skills {
		b.WriteString(s.Name)
	}
	return b.String()
}

func simpleHelper() int {
	return 42
}
`

func TestExtractSymbol_Function(t *testing.T) {
	body := ExtractSymbol(testSource, "buildSkillEvalBlock")
	if body == "" {
		t.Fatal("expected function body, got empty")
	}
	if !strings.Contains(body, "func buildSkillEvalBlock") {
		t.Errorf("expected func signature, got: %s", body)
	}
	if !strings.Contains(body, "return b.String()") {
		t.Errorf("expected return statement, got: %s", body)
	}
}

func TestExtractSymbol_Method(t *testing.T) {
	body := ExtractSymbol(testSource, "Generate")
	if body == "" {
		t.Fatal("expected method body, got empty")
	}
	if !strings.Contains(body, "func (g *Generator) Generate") {
		t.Errorf("expected method signature, got: %s", body)
	}
}

func TestExtractSymbol_Var_MultiLine(t *testing.T) {
	body := ExtractSymbol(testSource, "skillEvalBlock")
	if body == "" {
		t.Fatal("expected var body, got empty")
	}
	if !strings.Contains(body, "var skillEvalBlock") {
		t.Errorf("expected var declaration, got: %s", body)
	}
	if !strings.Contains(body, "with multiple lines") {
		t.Errorf("expected multi-line content, got: %s", body)
	}
}

func TestExtractSymbol_Var_SingleLine(t *testing.T) {
	body := ExtractSymbol(testSource, "singleLineVar")
	if body == "" {
		t.Fatal("expected var body, got empty")
	}
	if !strings.Contains(body, "var singleLineVar = 42") {
		t.Errorf("expected single-line var, got: %s", body)
	}
}

func TestExtractSymbol_Const_SingleLine(t *testing.T) {
	body := ExtractSymbol(testSource, "defaultTimeout")
	if body == "" {
		t.Fatal("expected const body, got empty")
	}
	if !strings.Contains(body, "const defaultTimeout = 30") {
		t.Errorf("expected const, got: %s", body)
	}
}

func TestExtractSymbol_Const_Block(t *testing.T) {
	body := ExtractSymbol(testSource, "modeA")
	if body == "" {
		t.Fatal("expected const block, got empty")
	}
	if !strings.Contains(body, "modeA") {
		t.Errorf("expected modeA in block, got: %s", body)
	}
}

func TestExtractSymbol_TypeStruct(t *testing.T) {
	body := ExtractSymbol(testSource, "Generator")
	if body == "" {
		t.Fatal("expected type body, got empty")
	}
	if !strings.Contains(body, "type Generator struct") {
		t.Errorf("expected struct declaration, got: %s", body)
	}
	if !strings.Contains(body, "language string") {
		t.Errorf("expected struct fields, got: %s", body)
	}
}

func TestExtractSymbol_TypeAlias(t *testing.T) {
	body := ExtractSymbol(testSource, "StringAlias")
	if body == "" {
		t.Fatal("expected type alias, got empty")
	}
	if !strings.Contains(body, "type StringAlias = string") {
		t.Errorf("expected type alias, got: %s", body)
	}
}

func TestExtractSymbol_NotFound(t *testing.T) {
	body := ExtractSymbol(testSource, "nonExistent")
	if body != "" {
		t.Errorf("expected empty for non-existent symbol, got: %s", body)
	}
}

func TestExtractRange(t *testing.T) {
	lines := strings.Split(testSource, "\n")
	result := ExtractRange(testSource, 1, 3)
	if result == "" {
		t.Fatal("expected range result, got empty")
	}
	parts := strings.Split(result, "\n")
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(parts))
	}
	_ = lines // avoid unused
}

func TestExtractRange_OutOfBounds(t *testing.T) {
	result := ExtractRange(testSource, 999, 1000)
	if result != "" {
		t.Errorf("expected empty for out-of-bounds, got: %s", result)
	}
}

func TestParseFileSymbols(t *testing.T) {
	symbols := ParseFileSymbols(testSource)
	if len(symbols) == 0 {
		t.Fatal("expected symbols, got none")
	}

	names := make(map[string]bool)
	for _, s := range symbols {
		names[s.Name] = true
	}

	expected := []string{"skillEvalBlock", "singleLineVar", "defaultTimeout", "modeA", "modeB", "Generator", "StringAlias", "Generate", "buildSkillEvalBlock", "simpleHelper"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing symbol: %s", name)
		}
	}

	// Verify line numbers are set
	for _, s := range symbols {
		if s.Line == 0 {
			t.Errorf("symbol %s has zero line number", s.Name)
		}
	}

	// Verify kinds are set
	kindMap := make(map[string]string)
	for _, s := range symbols {
		kindMap[s.Name] = s.Kind
	}
	if kindMap["skillEvalBlock"] != "var" {
		t.Errorf("expected var kind for skillEvalBlock, got %s", kindMap["skillEvalBlock"])
	}
	if kindMap["Generator"] != "type" {
		t.Errorf("expected type kind for Generator, got %s", kindMap["Generator"])
	}
	if kindMap["buildSkillEvalBlock"] != "func" {
		t.Errorf("expected func kind for buildSkillEvalBlock, got %s", kindMap["buildSkillEvalBlock"])
	}
	if kindMap["Generate"] != "method" {
		t.Errorf("expected method kind for Generate, got %s", kindMap["Generate"])
	}
	if kindMap["defaultTimeout"] != "const" {
		t.Errorf("expected const kind for defaultTimeout, got %s", kindMap["defaultTimeout"])
	}
}
