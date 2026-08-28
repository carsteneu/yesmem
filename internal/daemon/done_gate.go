package daemon

import (
	"fmt"
	"regexp"
	"strings"
)

// PhaseValidation holds the set of required fields for each yesloop phase.
type PhaseValidation struct {
	Number int
	Name   string
	// RequiredFields are compiled regex patterns, each must match somewhere within the
	// phase block (between its header and the next phase header or EOF).
	RequiredFields []*regexp.Regexp
	// IsStatusField is true for the pattern that checks **Status:** — this field is
	// always required and all others are only checked when status is found.
	IsStatusField bool
	// IfTaskType, when non-empty, makes the phase's TaskTypeFields mandatory only
	// when the Phase 1 **Task type:** value equals this string (e.g. "debug" gates
	// the Depth-lock line in Phase 2). Empty means TaskTypeFields are never checked.
	IfTaskType     string
	TaskTypeFields []*regexp.Regexp
}

// phaseValidations defines the v3 contract: what each phase MUST contain.
var phaseValidations = compileValidations()

func compileValidations() []PhaseValidation {
	return []PhaseValidation{
		{
			Number: 1, Name: "ANALYZE",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				regexp.MustCompile(`(?m)^\*\*Goal understood:\*\*`),
				regexp.MustCompile(`(?m)^\*\*Task type:\*\*\s+(debug|feature|chore|docs)`),
				regexp.MustCompile(`(?m)^\*\*Codebase explored:\*\*`),
				regexp.MustCompile(`(?m)^\*\*Session id:\*\*\s+\S`),
			},
		},
		{
			Number: 2, Name: "PLAN",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				regexp.MustCompile(`(?m)^\*\*Plan stored via set_plan:\*\*`),
				regexp.MustCompile(`(?m)^\*\*Files in scope:\*\*`),
			},
			// Debug tasks must lock their fix layer before implementation; the
			// Depth-lock discipline is only enforced when Task type == debug.
			IfTaskType:     "debug",
			TaskTypeFields: []*regexp.Regexp{depthLockRe},
		},
		{
			Number: 3, Name: "EXECUTE",
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
			},
		},
		{
			Number: 4, Name: "VERIFY",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				// L1.B fix: accept any of these as verification evidence.
				// Pure-build tasks (binary patching, frontend) often have no
				// test suite; **Build:** or **Verification:** is equally valid.
				regexp.MustCompile(`(?m)^\*\*(Tests run|Build|Verification):\*\*`),
				// Autoprompt-Härtung: every task must record what new test was
				// proven RED before the fix — or explicitly say "none — docs-only".
				redProofRe,
				// Deterministic regression discipline: Phase 4 must diff the test-suite
				// results between merge-base and HEAD (see SKILL.md procedure). Value
				// must sit on the same line — [ \t] blocks the \s+\S newline bypass.
				regexp.MustCompile(`(?m)^\*\*Regression baseline:\*\*[ \t]+\S`),
			},
		},
		{
			Number: 5, Name: "REVIEW",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				regexp.MustCompile(`\*\*Stage 2: Cold Review`),
				regexp.MustCompile(`task\(\) dispatched:\*{0,2}\s+(yes|blocked)`),
				regexp.MustCompile(`(?m)^\*\*Security:\*\*[ \t]+\S`),
				// Stage 3 Consequence & Intent: fresh task()-subagent traces second-order
				// effects and intent fidelity (see SKILL.md). Format check only; substance
				// is evidenced by the Subagent ID per BEWEISEN discipline.
				regexp.MustCompile(`\*\*Stage 3: Consequence & Intent`),
				regexp.MustCompile(`(?m)^\*\*consequence dispatched:\*{0,2}\s+(yes|blocked)`),
			},
		},
		{
			Number: 6, Name: "FINISH",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED)`),
				regexp.MustCompile(`\*\*Deploy executed:\*{0,2}\s+(yes|no)|Deploy required:\*{0,2}\s+(yes|no)`),
				regexp.MustCompile(`send_to orchestrator:`),
			},
		},
	}
}

var (
	phaseHeaderRe   = regexp.MustCompile(`(?m)^### Phase (\d+):`)
	taskTypeValueRe = regexp.MustCompile(`(?m)^\*\*Task type:\*\*\s+(debug|feature|chore|docs)`)
	depthLockRe     = regexp.MustCompile(`(?m)^\*\*Depth-lock:\*\*\s+\S`)
	redProofRe      = regexp.MustCompile(`(?m)^\*\*RED proof:\*\*\s+\S`)
)

// detectTaskType extracts the Phase 1 **Task type:** value (one of
// debug|feature|chore|docs). Returns "" when absent or invalid — in that case
// no task-type-conditional fields are enforced.
func detectTaskType(phase1 string) string {
	m := taskTypeValueRe.FindStringSubmatch(phase1)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// splitPhases splits scratchpad content into phase blocks keyed by phase number (1-6).
func splitPhases(content string) map[int]string {
	matches := phaseHeaderRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}
	phases := make(map[int]string, 6)
	for i, m := range matches {
		phaseNum := parseIntOrZero(content[m[2]:m[3]])
		if phaseNum < 1 || phaseNum > 6 {
			continue
		}
		start := m[1]
		var end int
		if i+1 < len(matches) {
			end = matches[i+1][0]
		} else {
			end = len(content)
		}
		phases[phaseNum] = strings.TrimSpace(content[start:end])
	}
	return phases
}

func parseIntOrZero(s string) int {
	var n int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			break
		}
	}
	return n
}

// FieldError describes a single validation failure in a phase block.
type FieldError struct {
	Phase  int    // phase number (1-6)
	Field  string // what was expected (the pattern string)
	Detail string // why it failed
}

// ValidationResult is the outcome of validating a complete yesloop scratchpad.
type ValidationResult struct {
	Compliant     bool         // all phase blocks pass validation
	PhaseCount    int          // how many of 6 phase blocks were found
	MissingPhases []int        // phase numbers missing from the content
	FieldErrors   []FieldError // individual field validation failures
}

func (r ValidationResult) String() string {
	if r.Compliant {
		return "Compliant: true"
	}
	var b strings.Builder
	b.WriteString("Compliant: false")
	if len(r.MissingPhases) > 0 {
		b.WriteString(fmt.Sprintf("\n  Missing phases: %v", r.MissingPhases))
	}
	for _, fe := range r.FieldErrors {
		b.WriteString(fmt.Sprintf("\n  Phase %d: %s — %s", fe.Phase, fe.Field, fe.Detail))
	}
	return b.String()
}

// ValidatePhaseBlocks validates a yesloop agent's scratchpad content against
// the v3 phase-block contract. Returns ValidationResult with detailed errors.
func ValidatePhaseBlocks(content string) ValidationResult {
	phases := splitPhases(content)

	var missing []int
	for _, pv := range phaseValidations {
		if _, ok := phases[pv.Number]; !ok {
			missing = append(missing, pv.Number)
		}
	}

	var errors []FieldError

	taskType := detectTaskType(phases[1])

	for _, pv := range phaseValidations {
		block, ok := phases[pv.Number]
		if !ok {
			continue
		}

		statusMatched := false
		for _, re := range pv.RequiredFields {
			if re.MatchString(block) {
				if pv.IsStatusField && re.String() == pv.RequiredFields[0].String() {
					statusMatched = true
				}
				continue
			}
			errors = append(errors, FieldError{
				Phase:  pv.Number,
				Field:  re.String(),
				Detail: "required field not found in phase block",
			})
		}

		if pv.IsStatusField && !statusMatched {
			errors = append(errors, FieldError{
				Phase:  pv.Number,
				Field:  "**Status:**",
				Detail: "missing or invalid status line (must be on its own line)",
			})
		}

		// Task-type-conditional fields: e.g. Depth-lock only for debug tasks.
		if pv.IfTaskType != "" && pv.IfTaskType == taskType {
			for _, re := range pv.TaskTypeFields {
				if re.MatchString(block) {
					continue
				}
				errors = append(errors, FieldError{
					Phase:  pv.Number,
					Field:  re.String(),
					Detail: "required field not found in phase block (conditional on task type " + pv.IfTaskType + ")",
				})
			}
		}
	}

	compliant := len(missing) == 0 && len(errors) == 0
	return ValidationResult{
		Compliant:     compliant,
		PhaseCount:    len(phases),
		MissingPhases: missing,
		FieldErrors:   errors,
	}
}

// completedPhaseRe matches a phase block line with **Status:** COMPLETE.
var completedPhaseRe = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+COMPLETE`)

// CountCompletedPhases counts how many of the 6 phase blocks have **Status:** COMPLETE.
// Useful for dead-agent-detection to assess how far a crashed agent got.
func CountCompletedPhases(content string) int {
	phases := splitPhases(content)
	count := 0
	for _, pv := range phaseValidations {
		block, ok := phases[pv.Number]
		if !ok {
			continue
		}
		if completedPhaseRe.MatchString(block) {
			count++
		}
	}
	return count
}
