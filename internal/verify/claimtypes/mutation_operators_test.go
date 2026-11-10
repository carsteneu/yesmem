package claimtypes

import (
	"strings"
	"testing"
)

// Each operator has to change what the line means while keeping the line
// syntactically plausible: a mutant that does not build is discarded and proves
// nothing, so the operator set is deliberately narrow.
func TestLineMutantsOperators(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		operator string
		want     string
	}{
		{name: "less-or-equal becomes less", line: "if a <= b {", operator: "<= -> <", want: "if a < b {"},
		{name: "less becomes less-or-equal", line: "if a < b {", operator: "< -> <=", want: "if a <= b {"},
		{name: "greater-or-equal becomes greater", line: "if a >= b {", operator: ">= -> >", want: "if a > b {"},
		{name: "greater becomes greater-or-equal", line: "if a > b {", operator: "> -> >=", want: "if a >= b {"},
		{name: "equality becomes inequality", line: "if a == b {", operator: "== -> !=", want: "if a != b {"},
		{name: "inequality becomes equality", line: "if a != b {", operator: "!= -> ==", want: "if a == b {"},
		{name: "and becomes or", line: "if a && b {", operator: "&& -> ||", want: "if a || b {"},
		{name: "or becomes and", line: "if a || b {", operator: "|| -> &&", want: "if a && b {"},
		{name: "true becomes false", line: "return true", operator: "true -> false", want: "return false"},
		{name: "false becomes true", line: "ok := false", operator: "false -> true", want: "ok := true"},
		{name: "plus becomes minus", line: "n := a + b", operator: "+ -> -", want: "n := a - b"},
		{name: "minus becomes plus", line: "n := a - b", operator: "- -> +", want: "n := a + b"},
		{name: "zero becomes one", line: "if n == 0 {", operator: "0 -> 1", want: "if n == 1 {"},
		{name: "one becomes zero", line: "if n == 1 {", operator: "1 -> 0", want: "if n == 0 {"},
		{name: "negation is removed", line: "if !ok {", operator: "! -> removed", want: "if ok {"},
		{name: "return value becomes the null value", line: "\treturn value", operator: "return -> 0", want: "\treturn 0"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			mutants := lineMutants(test.line)
			for _, mutant := range mutants {
				if mutant.Operator != test.operator {
					continue
				}
				if mutant.Line != test.want {
					t.Fatalf("operator %q produced %q, want %q", mutant.Operator, mutant.Line, test.want)
				}
				return
			}
			t.Fatalf("operator %q missing from %v for %q", test.operator, operatorsOf(mutants), test.line)
		})
	}
}

// The order is part of the contract: the selection gives every changed line its
// first mutant before any line gets a second, so the strongest operators have to
// come first.
func TestLineMutantsOrder(t *testing.T) {
	mutants := lineMutants("if n == 0 {")
	want := []string{"== -> !=", "0 -> 1"}
	if len(mutants) != len(want) {
		t.Fatalf("mutants = %v, want %v", operatorsOf(mutants), want)
	}
	for index := range want {
		if mutants[index].Operator != want[index] {
			t.Errorf("mutant[%d] = %q, want %q", index, mutants[index].Operator, want[index])
		}
	}
}

// A literal is not code: a mutant inside a string is not a behaviour change and
// would only produce a survivor that is not a finding.
func TestLineMutantsLeaveLiteralsAlone(t *testing.T) {
	for _, line := range []string{
		`s := "a <= b"`,
		`s := 'x == y'`,
		"raw := `a != b`",
	} {
		if mutants := lineMutants(line); len(mutants) != 0 {
			t.Errorf("%q: mutants = %v, want none", line, operatorsOf(mutants))
		}
	}
}

// A comment is not code either, and a mutant in a comment would read as a
// survivor: a false finding against the report.
func TestLineMutantsLeaveCommentsAlone(t *testing.T) {
	for _, test := range []struct{ line, operator, want string }{
		{line: "x := 1 // retry when a <= b", operator: "1 -> 0", want: "x := 0 // retry when a <= b"},
		{line: "$x = 1; # keep a <= b", operator: "1 -> 0", want: "$x = 0; # keep a <= b"},
	} {
		mutants := lineMutants(test.line)
		if len(mutants) != 1 {
			t.Fatalf("%q: mutants = %v, want one", test.line, operatorsOf(mutants))
		}
		if mutants[0].Operator != test.operator || mutants[0].Line != test.want {
			t.Errorf("%q: got %q -> %q, want %q -> %q",
				test.line, mutants[0].Operator, mutants[0].Line, test.operator, test.want)
		}
	}
}

// Operators that would produce the same line twice, or a line that is no longer
// that line, must not be generated: the mutant count is the claim's budget.
func TestLineMutantsSkipNoOps(t *testing.T) {
	for _, line := range []string{"return 0", "x := 10", "y := 1.5", "a++", "x += 1", "chan<- int"} {
		for _, mutant := range lineMutants(line) {
			if mutant.Line == line {
				t.Errorf("%q: a no-op mutant was generated (%q)", line, mutant.Operator)
			}
			if strings.Contains(mutant.Line, "==") && strings.Contains(mutant.Line, "=== ") {
				t.Errorf("%q: broken mutant %q", line, mutant.Line)
			}
		}
	}
}

func operatorsOf(mutants []lineMutant) []string {
	names := make([]string, 0, len(mutants))
	for _, mutant := range mutants {
		names = append(names, mutant.Operator)
	}
	return names
}
