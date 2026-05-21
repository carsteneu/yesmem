package main

import (
	"strings"
	"testing"
)

func TestApplyJSONFilter_Identity(t *testing.T) {
	in := []byte(`{"a":1,"b":"x"}`)
	out, err := applyJSONFilter(in, ".", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != `{"a":1,"b":"x"}` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_FieldAccess(t *testing.T) {
	in := []byte(`{"a":1,"b":"x"}`)
	out, err := applyJSONFilter(in, ".b", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != `"x"` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_FieldAccess_RawString(t *testing.T) {
	in := []byte(`{"a":1,"b":"x"}`)
	out, err := applyJSONFilter(in, ".b", true, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != `x` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_ArrayIterate(t *testing.T) {
	in := []byte(`[{"id":1},{"id":2},{"id":3}]`)
	out, err := applyJSONFilter(in, ".[] | .id", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "1\n2\n3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_StringInterpolation_Raw(t *testing.T) {
	in := []byte(`[{"id":1,"cat":"a"},{"id":2,"cat":"b"}]`)
	out, err := applyJSONFilter(in, `.[] | "\(.id):\(.cat)"`, true, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "1:a\n2:b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_Select(t *testing.T) {
	in := []byte(`[{"id":1,"keep":true},{"id":2,"keep":false},{"id":3,"keep":true}]`)
	out, err := applyJSONFilter(in, `.[] | select(.keep) | .id`, false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "1\n3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_Base64(t *testing.T) {
	in := []byte(`"hello"`)
	out, err := applyJSONFilter(in, `@base64`, true, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "aGVsbG8=" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_Length(t *testing.T) {
	in := []byte(`[1,2,3,4]`)
	out, err := applyJSONFilter(in, "length", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "4" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_InvalidJSONInput(t *testing.T) {
	in := []byte(`not json`)
	_, err := applyJSONFilter(in, ".", false, 0)
	if err == nil {
		t.Fatalf("expected error on invalid JSON input")
	}
}

func TestApplyJSONFilter_InvalidExpr(t *testing.T) {
	in := []byte(`[1]`)
	_, err := applyJSONFilter(in, ".[", false, 0)
	if err == nil {
		t.Fatalf("expected error on invalid expr")
	}
}

func TestApplyJSONFilter_RuntimeError(t *testing.T) {
	in := []byte(`"not an array"`)
	_, err := applyJSONFilter(in, ".[]", false, 0)
	if err == nil {
		t.Fatalf("expected runtime error on iterating non-array")
	}
}

func TestApplyJSONFilter_Indent(t *testing.T) {
	in := []byte(`{"a":1,"b":2}`)
	out, err := applyJSONFilter(in, ".", false, 2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "{\n  \"a\": 1,\n  \"b\": 2\n}"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_RawWithNonString(t *testing.T) {
	// -r should only strip quotes from string results; numbers stay as JSON
	in := []byte(`[1,"two",3]`)
	out, err := applyJSONFilter(in, ".[]", true, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "1\ntwo\n3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_NullOutput(t *testing.T) {
	in := []byte(`{"a":1}`)
	out, err := applyJSONFilter(in, ".missing", false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "null" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_MultipleResults(t *testing.T) {
	in := []byte(`[1,2,3]`)
	out, err := applyJSONFilter(in, `.[], "end"`, false, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "1\n2\n3\n\"end\""
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// --- new jq-compat flags ---

func TestApplyJSONFilter_NullInput(t *testing.T) {
	res, err := applyJSONFilterWithOpts(nil, `"hello"`, jsonFilterOpts{nullInput: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != `"hello"` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_NullInputWithInputs(t *testing.T) {
	// Two JSON values piped on stdin, -n reads all via input(s)/0.
	in := []byte(`{"a":1} {"b":2}`)
	res, err := applyJSONFilterWithOpts(in, `[inputs]`, jsonFilterOpts{nullInput: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != `[{"a":1},{"b":2}]` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_NullInputWithInputsSingle(t *testing.T) {
	// Single JSON value on stdin, inputs yields it.
	in := []byte(`{"x":"hello"}`)
	res, err := applyJSONFilterWithOpts(in, `input`, jsonFilterOpts{nullInput: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != `{"x":"hello"}` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_NullInputEmptyStdin(t *testing.T) {
	// Empty stdin, -n, no inputs filter — should work without iterator.
	res, err := applyJSONFilterWithOpts([]byte{}, `"ok"`, jsonFilterOpts{nullInput: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != `"ok"` {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_RawInput(t *testing.T) {
	in := []byte("hello\nworld\n")
	res, err := applyJSONFilterWithOpts(in, ".", jsonFilterOpts{rawInput: true, raw: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	want := "hello\nworld"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !res.hasOutput {
		t.Fatal("expected hasOutput")
	}
}

func TestApplyJSONFilter_RawInputSlurp(t *testing.T) {
	in := []byte("a\nb\nc\n")
	res, err := applyJSONFilterWithOpts(in, ".[]", jsonFilterOpts{rawInput: true, slurp: true, raw: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	want := "a\nb\nc"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestApplyJSONFilter_Slurp(t *testing.T) {
	in := []byte(`{"a":1}`)
	res, err := applyJSONFilterWithOpts(in, ".[]|.a", jsonFilterOpts{slurp: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_Arg(t *testing.T) {
	in := []byte(`[1,2,3]`)
	res, err := applyJSONFilterWithOpts(in, `.[] | select(. == $x)`, jsonFilterOpts{
		varNames:  []string{"$x"},
		varValues: []any{float64(2)},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != "2" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_ArgJSON(t *testing.T) {
	in := []byte(`[{"a":1},{"a":2}]`)
	res, err := applyJSONFilterWithOpts(in, `.[] | select(.a > $min) | .a`, jsonFilterOpts{
		varNames:  []string{"$min"},
		varValues: []any{float64(1)},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != "2" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_MultipleArgs(t *testing.T) {
	var in []byte
	res, err := applyJSONFilterWithOpts(in, `$x * $y`, jsonFilterOpts{
		nullInput: true,
		varNames:  []string{"$x", "$y"},
		varValues: []any{float64(6), float64(7)},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != "42" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_ExitStatus_False(t *testing.T) {
	in := []byte(`[false]`)
	res, err := applyJSONFilterWithOpts(in, `.[]`, jsonFilterOpts{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.hasOutput {
		t.Fatal("expected output")
	}
	if !isFalseOrNull(res.lastVal) {
		t.Fatalf("expected lastVal to be false, got %v", res.lastVal)
	}
}

func TestApplyJSONFilter_ExitStatus_Null(t *testing.T) {
	in := []byte(`{"x":1}`)
	res, err := applyJSONFilterWithOpts(in, `.missing`, jsonFilterOpts{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !res.hasOutput {
		t.Fatal("expected output")
	}
	if !isFalseOrNull(res.lastVal) {
		t.Fatalf("expected lastVal to be null, got %v", res.lastVal)
	}
}

func TestApplyJSONFilter_ExitStatus_Truthy(t *testing.T) {
	in := []byte(`[true]`)
	res, err := applyJSONFilterWithOpts(in, `.[]`, jsonFilterOpts{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if isFalseOrNull(res.lastVal) {
		t.Fatalf("expected lastVal to be truthy, got %v", res.lastVal)
	}
}

func TestApplyJSONFilter_NoOutput(t *testing.T) {
	in := []byte(`[]`)
	res, err := applyJSONFilterWithOpts(in, `.[]`, jsonFilterOpts{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.hasOutput {
		t.Fatal("expected no output")
	}
}

func TestApplyJSONFilter_RawInputLength(t *testing.T) {
	in := []byte("abc\n")
	res, err := applyJSONFilterWithOpts(in, "length", jsonFilterOpts{rawInput: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	if got != "3" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyJSONFilter_RawInputSelect(t *testing.T) {
	in := []byte("keep\nskip\nkeep\n")
	res, err := applyJSONFilterWithOpts(in, `select(. == "keep")`, jsonFilterOpts{rawInput: true, raw: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	got := strings.TrimSpace(string(res.output))
	want := "keep\nkeep"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// --- jsonCLIRun: end-to-end shape used by the worker's json_cli op ---

func TestJSONCLIRun_RawField(t *testing.T) {
	out, exit, err := jsonCLIRun([]string{"-r", ".foo"}, strings.NewReader(`{"foo":"bar"}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0", exit)
	}
	if string(out) != "bar\n" {
		t.Fatalf("got %q, want %q", out, "bar\n")
	}
}

func TestJSONCLIRun_NullInputBuildsObject(t *testing.T) {
	// Mirrors how caps build payloads: yesmem json -n --arg k V --argjson n 1 '{k:$k, n:$n}'
	out, exit, err := jsonCLIRun([]string{
		"-n",
		"--arg", "k", "hello",
		"--argjson", "n", "42",
		`{k:$k, n:$n}`,
	}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d, want 0", exit)
	}
	got := strings.TrimSpace(string(out))
	want := `{"k":"hello","n":42}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJSONCLIRun_ExitStatusNull(t *testing.T) {
	// -e on null result → exit 1
	_, exit, err := jsonCLIRun([]string{"-e", ".missing"}, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exit != 1 {
		t.Fatalf("exit=%d, want 1", exit)
	}
}

func TestJSONCLIRun_ExitStatusNoOutput(t *testing.T) {
	// -e with empty iteration → exit 4
	_, exit, err := jsonCLIRun([]string{"-e", ".[]"}, strings.NewReader(`[]`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exit != 4 {
		t.Fatalf("exit=%d, want 4", exit)
	}
}

func TestJSONCLIRun_DefaultFallback(t *testing.T) {
	// Cap pattern: .rows[0].value // "0" with empty rows → "0"
	out, exit, err := jsonCLIRun([]string{"-r", `.rows[0].value // "0"`}, strings.NewReader(`{"rows":[]}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	if string(out) != "0\n" {
		t.Fatalf("got %q, want %q", out, "0\n")
	}
}

func TestJSONCLIRun_BadArg(t *testing.T) {
	_, exit, err := jsonCLIRun([]string{"--indent", "notanumber"}, strings.NewReader(`{}`))
	if err == nil {
		t.Fatalf("expected error on bad --indent")
	}
	if exit != 2 {
		t.Fatalf("exit=%d, want 2", exit)
	}
}

func TestParseJSONArgs_HelpRequested(t *testing.T) {
	_, _, _, err := parseJSONArgs([]string{"--help"})
	if err != errJSONHelpRequested {
		t.Fatalf("got %v, want errJSONHelpRequested", err)
	}
}

func TestParseJSONArgs_MissingExpression(t *testing.T) {
	_, _, _, err := parseJSONArgs([]string{"-r"})
	if err == nil {
		t.Fatalf("expected error on missing expression")
	}
}
