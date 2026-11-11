package engine

import (
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
		wantErr bool
	}{
		{name: "plain", command: "go test ./...", want: []string{"go", "test", "./..."}},
		{name: "collapses spaces", command: "  go   test  ", want: []string{"go", "test"}},
		{name: "single quotes", command: `python3 -m unittest 'test one'`, want: []string{"python3", "-m", "unittest", "test one"}},
		{name: "double quotes", command: `make test "VAR=a b"`, want: []string{"make", "test", "VAR=a b"}},
		{name: "backslash escape", command: `echo a\ b`, want: []string{"echo", "a b"}},
		{name: "unterminated quote", command: `echo 'x`, wantErr: true},
		{name: "empty", command: "", want: nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := SplitCommand(test.command)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("argv = %v, want %v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Errorf("argv[%d] = %q, want %q", index, got[index], test.want[index])
				}
			}
		})
	}
}

func TestIsAllowed(t *testing.T) {
	allowlist := []string{"go test", "python3 -m unittest"}

	allowed := []string{"go test", "go test ./...", "go   test ./x", "python3 -m unittest test_ok"}
	for _, command := range allowed {
		if !IsAllowed(command, allowlist) {
			t.Errorf("%q should be allowed", command)
		}
	}
	rejected := []string{"go testx", "go", "curl http://example.invalid", "make test"}
	for _, command := range rejected {
		if IsAllowed(command, allowlist) {
			t.Errorf("%q should not be allowed", command)
		}
	}
}

// Token equality, not string prefix: a allowlisted-looking command must not run
// as something else.
func TestAllowedByTokens(t *testing.T) {
	allowlist := []string{"python3 -m unittest"}

	if !AllowedByTokens([]string{"python3", "-m", "unittest", "test_ok"}, allowlist) {
		t.Error("the exact leading tokens should match")
	}
	if AllowedByTokens([]string{"python3", "-m", "unittestx"}, allowlist) {
		t.Error("a longer module name must not match")
	}
	if AllowedByTokens([]string{"python3", "-m"}, allowlist) {
		t.Error("a shorter argv must not match")
	}
}

// Every command the allowlist admits must also pass the argument rules. The two
// lists are what a report is held to, and a name in one that the other refuses
// turns an admitted command into an unverifiable run: the allowlist advertised
// "node --test" while the abbreviation rule read "--test" as "--test-reporter".
func TestDefaultAllowlistCommandsAreAdmitted(t *testing.T) {
	for _, command := range DefaultCommandAllowlist {
		if _, _, verdict := PrepareCommand(command, DefaultCommandAllowlist); verdict != nil {
			t.Errorf("allowlisted command %q is refused: %s", command, verdict.Reason)
		}
	}
}

// The escape rules are what keeps a report command inside the checkout.
func TestArgEscapesCheckout(t *testing.T) {
	cases := []struct {
		arg    string
		strict bool
		want   bool
	}{
		{arg: "test_ok", want: false},
		{arg: "-m", want: false},
		{arg: "--", want: false},
		{arg: "-", want: false},
		{arg: "./pkg/...", want: false},
		{arg: "module=value", want: false},
		{arg: "../../outside", want: true},
		{arg: "/etc/passwd", want: true},
		{arg: "-C/abs", want: true},
		{arg: "-C../x", want: true},
		{arg: "--dir=/etc", want: true},
		{arg: "https://example.invalid", want: true},
		{arg: "--eval=1", want: true},
		{arg: "--ev=1", want: true},
		{arg: "-e", want: false},
		// node ships "--test" as a flag of its own (the allowlist admits
		// "node --test"), so the abbreviation rule must not read it as
		// "--test-reporter". A true abbreviation stays refused, and so does
		// the exact dangerous name.
		{arg: "--test", want: false},
		{arg: "--tes", want: true},
		{arg: "--test-reporter=spec", want: true},
		{arg: "--test-reporter", want: true},
		{arg: "~user/file", strict: true, want: true},
		{arg: "~user/file", strict: false, want: false},
		{arg: "a b", strict: true, want: true},
		{arg: "a b", strict: false, want: false},
	}
	for _, test := range cases {
		if got := ArgEscapesCheckout(test.arg, test.strict); got != test.want {
			t.Errorf("ArgEscapesCheckout(%q, strict=%v) = %v, want %v",
				test.arg, test.strict, got, test.want)
		}
	}
}

// The output cap keeps the tail and counts everything: a runaway command must
// be refused, not truncated into a verdict.
func TestCappedWriterKeepsTailAndCounts(t *testing.T) {
	writer := &cappedWriter{}
	chunk := strings.Repeat("x", TailBytes)
	if _, err := writer.Write([]byte(chunk)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := writer.Write([]byte("tail")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, exceeded := writer.snapshot()
	if exceeded {
		t.Error("the writer reported a limit that was not reached")
	}
	if !strings.HasSuffix(raw, "tail") {
		t.Error("the tail is not kept")
	}
	if len(raw) != TailBytes {
		t.Errorf("kept %d bytes, want %d", len(raw), TailBytes)
	}

	writer = &cappedWriter{}
	if _, err := writer.Write(make([]byte, MaxOutputBytes+1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, exceeded := writer.snapshot(); !exceeded {
		t.Error("the writer did not report the exceeded limit")
	}
}

func TestLastLines(t *testing.T) {
	if got := LastLines("a\nb\nc\nd\ne\nf\n", 2); got != "e\nf" {
		t.Errorf("LastLines = %q", got)
	}
	if got := LastLines("only", 5); got != "only" {
		t.Errorf("LastLines = %q", got)
	}
}

func TestDisplayNameReplacesControlCharacters(t *testing.T) {
	if got := DisplayName("a\x00b\x1bc"); got != "a?b?c" {
		t.Errorf("DisplayName = %q", got)
	}
}
