package guard

import "testing"

// A destructive string inside a quoted argument is text, not a command: a
// commit message or an echo that quotes a rule must not be refused. The same
// string handed to an interpreter is still a command, and a REPL call carries
// no shell line at all — its quotes are the payload a shell function receives.
func TestMatchDestructivePattern_QuotedTextIsInert(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{`Bash: echo "rm -rf /"`, false},
		{`Bash: git commit -m "docs: never rm -rf / again"`, false},
		{`Bash: grep -r "rm -rf /" docs/`, false},
		{`Bash: git log --grep='rm -rf /'`, false},
		{`Bash: echo hi && rm -rf /`, true},
		{`Bash: bash -c "rm -rf /"`, true},
		{`Bash: sh -c 'rm -rf ~'`, true},
		{`Bash: eval "rm -rf /"`, true},
		{`Bash: echo hi; bash -c "rm -rf /"`, true},
		{`REPL: await Bash({command:"rm -rf /"})`, true},
		{`Bash: rm -rf /`, true},
		{`Bash: rm -rf /tmp/foo`, false},
	}
	for _, c := range cases {
		if got := matchDestructivePattern(c.desc) != ""; got != c.want {
			t.Errorf("matchDestructivePattern(%q) = %v, want %v (matched=%q)", c.desc, got, c.want, matchDestructivePattern(c.desc))
		}
	}
}
