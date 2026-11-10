package verify

import "testing"

func TestExitCode(t *testing.T) {
	cases := []struct {
		name    string
		verdict []Verdict
		strict  bool
		want    int
	}{
		{
			name:    "all confirmed",
			verdict: []Verdict{Confirmed("a"), Confirmed("b")},
			want:    ExitOK,
		},
		{
			name:    "refuted wins over everything",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassDefect), Refuted("c")},
			want:    ExitRefuted,
		},
		{
			name:    "a defect fails in both modes",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassDefect)},
			want:    ExitDefect,
		},
		{
			name:    "an environment gap is not a defect",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassEnvironment)},
			want:    ExitOK,
		},
		{
			name:    "nothing proven is not success",
			verdict: []Verdict{Unverifiable("a")},
			want:    ExitNothing,
		},
		{
			name:    "an empty report proves nothing",
			verdict: nil,
			want:    ExitNothing,
		},
		{
			name:    "strict reserves exit 0 for a fully proven report",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassEnvironment)},
			strict:  true,
			want:    ExitStrict,
		},
		{
			name:    "an exhausted budget gets its own code",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassLimit)},
			strict:  true,
			want:    ExitLimit,
		},
		{
			name:    "the budget code is strict-only",
			verdict: []Verdict{Confirmed("a"), Unverifiable("b").WithClass(ClassLimit)},
			want:    ExitOK,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ExitCode(test.verdict, test.strict); got != test.want {
				t.Errorf("ExitCode = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSummarizeCountsEveryStatus(t *testing.T) {
	summary := Summarize([]Verdict{Confirmed("a"), Refuted("b"), Unverifiable("c")})

	if summary[StatusConfirmed] != 1 || summary[StatusRefuted] != 1 || summary[StatusUnverifiable] != 1 {
		t.Errorf("summary = %v", summary)
	}
}

// Every class is present in the summary even when it never occurred, so the
// output shape does not depend on the report.
func TestClassSummaryCountsEveryClass(t *testing.T) {
	classes := ClassSummary([]Verdict{
		Confirmed("a"),
		Unverifiable("b").WithClass(ClassEnvironment),
		Unverifiable("c").WithClass(ClassEnvironment),
	})

	if classes[ClassNone] != 1 || classes[ClassEnvironment] != 2 || classes[ClassDefect] != 0 {
		t.Errorf("classes = %v", classes)
	}
}

// A confirmed claim cannot carry a class: the class answers "why not
// confirmed".
func TestWithClassIgnoresConfirmed(t *testing.T) {
	if got := Confirmed("a").WithClass(ClassDefect); got.Class != ClassNone {
		t.Errorf("class = %q, want none", got.Class)
	}
}
