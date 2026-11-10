package claimtypes

import "strings"

// lineMutant is one single-edit mutation of one source line.
type lineMutant struct {
	Operator string
	Line     string
}

// mutationOperator changes one syntactic construct and leaves the rest of the
// line as it was: the smallest edit that can change what the line means.
//
// The set is deliberately textual and deliberately narrow. A mutant that no
// compiler accepts proves nothing, so an operator that would only produce
// syntax errors costs budget without buying evidence.
type mutationOperator struct {
	name    string
	replace func(segment string) (string, bool)
}

// mutationOperators is ordered by signal: every changed line gets its first
// mutant before any line gets a second, so the operators most likely to be
// observable come first.
var mutationOperators = []mutationOperator{
	{name: "<= -> <", replace: replaceToken("<=", "<", anyContext)},
	{name: ">= -> >", replace: replaceToken(">=", ">", anyContext)},
	{name: "== -> !=", replace: replaceToken("==", "!=", anyContext)},
	{name: "!= -> ==", replace: replaceToken("!=", "==", anyContext)},
	{name: "< -> <=", replace: replaceToken("<", "<=", func(before, after byte) bool {
		// Not the tail of "<=", "<<" or a channel arrow "<-".
		return after != '=' && after != '<' && after != '-'
	})},
	{name: "> -> >=", replace: replaceToken(">", ">=", func(before, after byte) bool {
		return before != '-' && after != '=' && after != '>'
	})},
	{name: "&& -> ||", replace: replaceToken("&&", "||", anyContext)},
	{name: "|| -> &&", replace: replaceToken("||", "&&", anyContext)},
	{name: "true -> false", replace: replaceToken("true", "false", wordBoundary)},
	{name: "false -> true", replace: replaceToken("false", "true", wordBoundary)},
	{name: "+ -> -", replace: replaceToken("+", "-", func(before, after byte) bool {
		// Not "++", not "+=".
		return before != '+' && after != '+' && after != '='
	})},
	{name: "- -> +", replace: replaceToken("-", "+", func(before, after byte) bool {
		// Not "--", not "-=", not a channel arrow "->".
		return before != '-' && after != '-' && after != '=' && after != '>'
	})},
	{name: "0 -> 1", replace: replaceToken("0", "1", numberBoundary)},
	{name: "1 -> 0", replace: replaceToken("1", "0", numberBoundary)},
	{name: "! -> removed", replace: removeNegation},
	{name: "return -> 0", replace: zeroReturn},
}

// lineMutants returns the single-edit mutants of one source line, in operator
// order.
//
// Only code is mutated. A literal is a value, not a branch, and a comment is not
// executed at all: a mutant there can only survive, and a survivor is reported as
// a finding against the report.
func lineMutants(line string) []lineMutant {
	var mutants []lineMutant
	for _, operator := range mutationOperators {
		mutated, ok := operator.apply(line)
		if !ok || mutated == line {
			continue
		}
		mutants = append(mutants, lineMutant{Operator: operator.name, Line: mutated})
	}
	return mutants
}

// apply mutates the first piece of code the operator recognises.
func (o mutationOperator) apply(line string) (string, bool) {
	for _, span := range mutableSpans(line) {
		mutated, ok := o.replace(line[span.start:span.end])
		if ok {
			return line[:span.start] + mutated + line[span.end:], true
		}
	}
	return "", false
}

// span is a half-open byte range of one line.
type span struct {
	start int
	end   int
}

// mutableSpans returns the pieces of a line that are code: outside string and
// character literals and before a comment marker.
func mutableSpans(line string) []span {
	var spans []span
	quote := byte(0)
	start := 0
	for index := 0; index < len(line); index++ {
		char := line[index]
		if quote != 0 {
			if char == '\\' {
				index++
				continue
			}
			if char == quote {
				quote = 0
				start = index + 1
			}
			continue
		}
		switch char {
		case '"', '\'', '`':
			if index > start {
				spans = append(spans, span{start: start, end: index})
			}
			quote = char
		case '/':
			if index+1 < len(line) && line[index+1] == '/' {
				if index > start {
					spans = append(spans, span{start: start, end: index})
				}
				return spans
			}
		case '#':
			if index > start {
				spans = append(spans, span{start: start, end: index})
			}
			return spans
		}
	}
	if quote != 0 {
		// An unterminated literal: the rest of the line is not code.
		return spans
	}
	if start < len(line) {
		spans = append(spans, span{start: start, end: len(line)})
	}
	return spans
}

func anyContext(_, _ byte) bool { return true }

// wordBoundary refuses to cut an identifier: "trueish" is not "true".
func wordBoundary(before, after byte) bool {
	return !identifierByte(before) && !identifierByte(after)
}

// numberBoundary refuses to cut a number out of a longer one: "10" is not "1"
// and "0.5" is not "0".
func numberBoundary(before, after byte) bool {
	return !identifierByte(before) && !identifierByte(after) && before != '.' && after != '.'
}

func identifierByte(char byte) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return true
	case char == '_', char == '$':
		return true
	}
	return false
}

// replaceToken replaces the first occurrence whose neighbours pass the guard.
func replaceToken(token, replacement string, accept func(before, after byte) bool) func(string) (string, bool) {
	return func(segment string) (string, bool) {
		for offset := 0; offset <= len(segment); {
			index := strings.Index(segment[offset:], token)
			if index < 0 {
				return "", false
			}
			at := offset + index
			var before, after byte
			if at > 0 {
				before = segment[at-1]
			}
			if end := at + len(token); end < len(segment) {
				after = segment[end]
			}
			if accept(before, after) {
				return segment[:at] + replacement + segment[at+len(token):], true
			}
			offset = at + 1
		}
		return "", false
	}
}

// removeNegation deletes a boolean negation, so "!ok" reads "ok".
func removeNegation(segment string) (string, bool) {
	for index := 0; index < len(segment); index++ {
		if segment[index] != '!' {
			continue
		}
		var before, after byte
		if index > 0 {
			before = segment[index-1]
		}
		if index+1 < len(segment) {
			after = segment[index+1]
		}
		if before == '=' || before == '!' || after == '=' {
			continue
		}
		end := index + 1
		if end < len(segment) && segment[end] == ' ' {
			end++
		}
		return segment[:index] + segment[end:], true
	}
	return "", false
}

// zeroReturn replaces a returned value with the null value of the call site.
//
// The null value of a typed language is only "0" for numbers, so a mutant in a
// string-returning function fails to build. That is what the discarded class is
// for: it costs budget, it never becomes a false confirmation.
func zeroReturn(segment string) (string, bool) {
	trimmed := strings.TrimLeft(segment, " \t")
	if !strings.HasPrefix(trimmed, "return") {
		return "", false
	}
	rest := trimmed[len("return"):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		// "returns", "returnValue": not a return statement.
		return "", false
	}
	expression := strings.TrimSpace(rest)
	if expression == "" || expression == "0" {
		return "", false
	}
	indent := segment[:len(segment)-len(trimmed)]
	return indent + "return 0", true
}
