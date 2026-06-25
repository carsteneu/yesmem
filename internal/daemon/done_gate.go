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
				regexp.MustCompile(`(?m)^\*\*Codebase explored:\*\*`),
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
				regexp.MustCompile(`(?m)^\*\*Tests run:\*\*`),
			},
		},
		{
			Number: 5, Name: "REVIEW",
			IsStatusField: true,
			RequiredFields: []*regexp.Regexp{
				regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				regexp.MustCompile(`\*\*Stage 2: Cold Review`),
				regexp.MustCompile(`task\(\) dispatched:\*{0,2}\s+(yes|blocked)`),
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

var phaseHeaderRe = regexp.MustCompile(`(?m)^### Phase (\d+):`)

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
