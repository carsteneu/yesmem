package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/itchyny/gojq"
)

// runJSON executes a jq-style filter expression on JSON read from stdin and
// writes results to stdout, one value per line.
//
// Usage:
//
//	yesmem json '<expr>' [-r] [-n] [-R] [-s] [-e] [--indent N]
//	                     [--arg name value] [--argjson name value]
//
// Pairs with `yesmem query --format objects` to give a pure-Go bordmittel
// alternative to the sqlite3-CLI plus jq pipeline. Powered by github.com/itchyny/gojq.
func runJSON(args []string) {
	opts, expr, exitStatus, err := parseJSONArgs(args)
	if err != nil {
		if err == errJSONHelpRequested {
			jsonUsage("")
		}
		jsonUsage(err.Error())
	}
	output, code, runErr := jsonCLIExecute(opts, expr, exitStatus, os.Stdin)
	if runErr != nil {
		log.Fatalf("json: %v", runErr)
	}
	if _, err := os.Stdout.Write(output); err != nil {
		log.Fatalf("write stdout: %v", err)
	}
	if exitStatus || code != 0 {
		os.Exit(code)
	}
}

// errJSONHelpRequested signals that the caller asked for --help; runJSON treats
// this as a clean exit while jsonCLIRun (worker path) treats it as an error.
var errJSONHelpRequested = fmt.Errorf("help requested")

// parseJSONArgs parses CLI-style arguments for `yesmem json` into structured
// options. Returns errJSONHelpRequested if the user asked for --help.
func parseJSONArgs(args []string) (jsonFilterOpts, string, bool, error) {
	var opts jsonFilterOpts
	expr := ""
	exitStatus := false

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-r" || a == "--raw-output":
			opts.raw = true
			i++
		case a == "-n" || a == "--null-input":
			opts.nullInput = true
			i++
		case a == "-R" || a == "--raw-input":
			opts.rawInput = true
			i++
		case a == "-s" || a == "--slurp":
			opts.slurp = true
			i++
		case a == "-e" || a == "--exit-status":
			exitStatus = true
			i++
		case a == "-c" || a == "--compact-output":
			i++
		case a == "--indent":
			if i+1 >= len(args) {
				return opts, "", false, fmt.Errorf("--indent needs a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 {
				return opts, "", false, fmt.Errorf("--indent: bad value %q", args[i+1])
			}
			opts.indent = n
			i += 2
		case strings.HasPrefix(a, "--indent="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--indent="))
			if err != nil || n < 0 {
				return opts, "", false, fmt.Errorf("--indent: bad value %q", a)
			}
			opts.indent = n
			i++
		case a == "--arg":
			if i+2 >= len(args) {
				return opts, "", false, fmt.Errorf("--arg needs <name> <value>")
			}
			opts.varNames = append(opts.varNames, "$"+args[i+1])
			opts.varValues = append(opts.varValues, args[i+2])
			i += 3
		case a == "--argjson":
			if i+2 >= len(args) {
				return opts, "", false, fmt.Errorf("--argjson needs <name> <value>")
			}
			name := args[i+1]
			var v any
			if err := json.Unmarshal([]byte(args[i+2]), &v); err != nil {
				return opts, "", false, fmt.Errorf("--argjson %s: invalid JSON: %v", name, err)
			}
			opts.varNames = append(opts.varNames, "$"+name)
			opts.varValues = append(opts.varValues, v)
			i += 3
		case a == "-h" || a == "--help":
			return opts, "", false, errJSONHelpRequested
		default:
			if expr == "" {
				expr = a
				i++
			} else {
				return opts, "", false, fmt.Errorf("unexpected argument: %q", a)
			}
		}
	}

	if expr == "" {
		return opts, "", false, fmt.Errorf("missing jq expression")
	}
	return opts, expr, exitStatus, nil
}

// jsonCLIRun reproduces `yesmem json` behavior in-process: parse args, read
// stdin, apply filter, return the bytes that would be written to stdout plus
// the exit code that `yesmem json` would have used. Used by the worker's
// json_cli op so cap scripts can route jq calls through the long-lived worker
// instead of spawning a new yesmem binary per filter.
func jsonCLIRun(args []string, stdin io.Reader) ([]byte, int, error) {
	opts, expr, exitStatus, err := parseJSONArgs(args)
	if err != nil {
		return nil, 2, err
	}
	return jsonCLIExecute(opts, expr, exitStatus, stdin)
}

func jsonCLIExecute(opts jsonFilterOpts, expr string, exitStatus bool, stdin io.Reader) ([]byte, int, error) {
	input, err := io.ReadAll(stdin)
	if err != nil {
		return nil, 2, fmt.Errorf("read input: %w", err)
	}
	res, err := applyJSONFilterWithOpts(input, expr, opts)
	if err != nil {
		return nil, 2, err
	}
	exit := 0
	if exitStatus {
		switch {
		case !res.hasOutput:
			exit = 4
		case isFalseOrNull(res.lastVal):
			exit = 1
		}
	}
	return res.output, exit, nil
}

func jsonUsage(msg string) {
	if msg != "" {
		fmt.Fprintln(os.Stderr, "yesmem json:", msg)
	}
	fmt.Fprintln(os.Stderr, "Usage: yesmem json '<expr>' [-r] [-n] [-R] [-s] [-e] [--indent N]")
	fmt.Fprintln(os.Stderr, "                     [--arg name value] [--argjson name value]")
	fmt.Fprintln(os.Stderr, "  -r, --raw-output      write strings without JSON quoting")
	fmt.Fprintln(os.Stderr, "  -n, --null-input      use null as single input value")
	fmt.Fprintln(os.Stderr, "  -R, --raw-input       read raw strings, not JSON")
	fmt.Fprintln(os.Stderr, "  -s, --slurp           read all inputs into array")
	fmt.Fprintln(os.Stderr, "  -e, --exit-status     exit 1 if last value is false/null, 4 if no output")
	fmt.Fprintln(os.Stderr, "  -c, --compact-output  compact output (default)")
	fmt.Fprintln(os.Stderr, "  --indent N            pretty-print with N-space indent (default: compact)")
	fmt.Fprintln(os.Stderr, "  --arg name value      pass $name as string")
	fmt.Fprintln(os.Stderr, "  --argjson name value  pass $name as JSON-parsed value")
	fmt.Fprintln(os.Stderr, "  Reads JSON from stdin. Powered by gojq (full jq syntax).")
	if msg != "" {
		os.Exit(2)
	}
	os.Exit(0)
}

type jsonFilterOpts struct {
	raw       bool
	indent    int
	nullInput bool
	rawInput  bool
	slurp     bool
	varNames  []string
	varValues []any
}

type jsonFilterResult struct {
	output    []byte
	lastVal   any
	hasOutput bool
}

// applyJSONFilter parses input according to opts, runs expr, and emits results.
// The legacy 5-arg signature is kept for backward compat with tests.
func applyJSONFilter(input []byte, expr string, raw bool, indent int) ([]byte, error) {
	res, err := applyJSONFilterWithOpts(input, expr, jsonFilterOpts{raw: raw, indent: indent})
	if err != nil {
		return nil, err
	}
	return res.output, nil
}

func applyJSONFilterWithOpts(input []byte, expr string, opts jsonFilterOpts) (*jsonFilterResult, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid jq expression: %w", err)
	}

	// Parse extra stdin values for use with input(s)/0 in -n mode.
	var extraInputs []any
	if opts.nullInput && len(input) > 0 {
		dec := json.NewDecoder(bytes.NewReader(input))
		for {
			var v any
			if err := dec.Decode(&v); err != nil {
				break
			}
			extraInputs = append(extraInputs, v)
		}
	}

	compileOpts := []gojq.CompilerOption{gojq.WithVariables(opts.varNames)}
	if len(extraInputs) > 0 {
		compileOpts = append(compileOpts, gojq.WithInputIter(&sliceIter{vals: extraInputs}))
	}
	code, err := gojq.Compile(query, compileOpts...)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	var buf bytes.Buffer
	var lastVal any
	hasOutput := false

	run := func(inputVal any) error {
		iter := code.Run(inputVal, opts.varValues...)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if errVal, ok := v.(error); ok {
				return fmt.Errorf("jq runtime: %w", errVal)
			}
			hasOutput = true
			lastVal = v
			if opts.raw {
				if s, ok := v.(string); ok {
					buf.WriteString(s)
					buf.WriteByte('\n')
					continue
				}
			}
			var line []byte
			var err error
			if opts.indent > 0 {
				line, err = json.MarshalIndent(v, "", strings.Repeat(" ", opts.indent))
			} else {
				line, err = json.Marshal(v)
			}
			if err != nil {
				return fmt.Errorf("marshal result: %w", err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
		return nil
	}

	if opts.nullInput {
		// -n: don't read stdin, pass nil as input. Extra values
		// were pre-parsed and wired via WithInputIter above.
		if err := run(nil); err != nil {
			return nil, err
		}
	} else if opts.rawInput && opts.slurp {
		// -R -s: read all lines, build array of strings, run once
		var lines []any
		sc := bufio.NewScanner(bytes.NewReader(input))
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read raw input: %w", err)
		}
		if err := run(lines); err != nil {
			return nil, err
		}
	} else if opts.rawInput {
		// -R: each line is a string input, run filter once per line
		sc := bufio.NewScanner(bytes.NewReader(input))
		for sc.Scan() {
			if err := run(sc.Text()); err != nil {
				return nil, err
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read raw input: %w", err)
		}
	} else if opts.slurp {
		// -s: parse JSON input, wrap in array, run once
		var inputVal any
		if err := json.Unmarshal(input, &inputVal); err != nil {
			return nil, fmt.Errorf("invalid JSON input: %w", err)
		}
		if err := run([]any{inputVal}); err != nil {
			return nil, err
		}
	} else {
		// normal: parse JSON input, run once
		var inputVal any
		if err := json.Unmarshal(input, &inputVal); err != nil {
			return nil, fmt.Errorf("invalid JSON input: %w", err)
		}
		if err := run(inputVal); err != nil {
			return nil, err
		}
	}

	return &jsonFilterResult{output: buf.Bytes(), lastVal: lastVal, hasOutput: hasOutput}, nil
}

// sliceIter implements gojq.Iter for a pre-parsed slice of JSON values.
type sliceIter struct {
	vals []any
	idx  int
}

func (it *sliceIter) Next() (any, bool) {
	if it.idx < len(it.vals) {
		v := it.vals[it.idx]
		it.idx++
		return v, true
	}
	return nil, false
}

func isFalseOrNull(v any) bool {
	if v == nil {
		return true
	}
	if b, ok := v.(bool); ok {
		return !b
	}
	return false
}
