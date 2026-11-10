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
	RequiredFields []requiredField
	// IsStatusField is true for the pattern that checks **Status:** — this field is
	// always required and all others are only checked when status is found.
	IsStatusField bool
	// IfTaskType, when non-empty, makes the phase's TaskTypeFields mandatory only
	// when the Phase 1 **Task type:** value equals this string (e.g. "debug" gates
	// the Depth-lock line in Phase 2). Empty means TaskTypeFields are never checked.
	IfTaskType     string
	TaskTypeFields []requiredField
}

// requiredField pairs a validation pattern with the name the agent is told about.
// The field name is what reaches the DONE-guard relay — a regex there is
// un-actionable (YM-150).
type requiredField struct {
	re    *regexp.Regexp
	label string
}

func mustField(label, pattern string) requiredField {
	return requiredField{re: regexp.MustCompile(pattern), label: label}
}

// donePhaseCount is the length of the loop's phase contract: Phase 6 is the
// daemon's verification, Phase 7 the handoff. Every count in the daemon uses
// this number, so a seventh block cannot be added to the validator alone and
// leave the idle, done-verify, stagnation and broken-toolcall layers counting
// six.
const donePhaseCount = 7

// phaseValidations defines the v3 contract: what each phase MUST contain.
var phaseValidations = compileValidations()

func compileValidations() []PhaseValidation {
	return []PhaseValidation{
		{
			Number: 1, Name: "ANALYZE",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				mustField("Goal understood", `(?m)^\*\*Goal understood:\*\*`),
				mustField("Task type", `(?m)^\*\*Task type:\*\*\s+(debug|feature|chore|docs)`),
				mustField("Codebase explored", `(?m)^\*\*Codebase explored:\*\*`),
				mustField("Session id", `(?m)^\*\*Session id:\*\*\s+\S`),
			},
		},
		{
			Number: 2, Name: "PLAN",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				mustField("Plan stored via set_plan", `(?m)^\*\*Plan stored via set_plan:\*\*`),
				mustField("Files in scope", `(?m)^\*\*Files in scope:\*\*`),
			},
			// Debug tasks must lock their fix layer before implementation; the
			// Depth-lock discipline is only enforced when Task type == debug.
			IfTaskType:     "debug",
			TaskTypeFields: []requiredField{{re: depthLockRe, label: "Depth-lock"}},
		},
		{
			Number: 3, Name: "EXECUTE",
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
			},
		},
		{
			Number: 4, Name: "VERIFY",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				// L1.B fix: accept any of these as verification evidence.
				// Pure-build tasks (binary patching, frontend) often have no
				// test suite; **Build:** or **Verification:** is equally valid.
				mustField("Tests run / Build / Verification", `(?m)^\*\*(Tests run|Build|Verification):\*\*`),
				// Autoprompt-Härtung: every task must record what new test was
				// proven RED before the fix — or explicitly say "none — docs-only".
				{re: redProofRe, label: "RED proof"},
				// Deterministic regression discipline: Phase 4 must diff the test-suite
				// results between merge-base and HEAD (see SKILL.md procedure). Value
				// must sit on the same line — [ \t] blocks the \s+\S newline bypass.
				mustField("Regression baseline", `(?m)^\*\*Regression baseline:\*\*[ \t]+\S`),
			},
		},
		{
			Number: 5, Name: "REVIEW",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				mustField("self-review: done", `(?m)^\*{0,2}self-review:\*{0,2}[ \t]+done\b`),
				// One task() subagent per review sub-phase; the ses_ id is
				// verified against opencode's session DB by VerifySubagentEvidence.
				mustField("cold-review: ses_<id>", `(?m)^\*{0,2}cold-review:\*{0,2}[ \t]+ses_\S+`),
				mustField("consequence: ses_<id>", `(?m)^\*{0,2}consequence:\*{0,2}[ \t]+ses_\S+`),
				mustField("security: ses_<id>", `(?m)^\*{0,2}security:\*{0,2}[ \t]+ses_\S+`),
				// risk-based-code-review classification from the 5.2 cold reviewer:
				// Stufe 1-3 with a same-line rationale, plus the active module
				// selection (value on the same line, as with Regression baseline).
				mustField("Stufe 1-3 with rationale", `(?m)^\*\*Stufe:\*\*[ \t]+[123][ \t]+\S`),
				mustField("Modules", `(?m)^\*\*Modules:\*\*[ \t]+\S`),
			},
		},
		{
			// Phase 6 is the verification itself: the agent runs the pre-check
			// and names its verdict here. A block that only says COMPLETE
			// claims the phase without the fact it stands for, so the accepted
			// proof is a required field of its own.
			Number: 6, Name: "DAEMON-VERIFY",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED|IN PROGRESS)`),
				// The parenthesised form is the shape the skill prints: a line
				// that says "not accepted" or "never accepted" carries the word
				// and none of the fact.
				mustField("Vorab-Prüfung (accepted)", `(?m)^\*\*Vorab-Prüfung:\*\*[^\n]*\(\s*accepted\s*\)`),
			},
		},
		{
			Number: 7, Name: "FINISH",
			IsStatusField: true,
			RequiredFields: []requiredField{
				mustField("Status", `(?m)^\*\*Status:\*\*\s+(COMPLETE|BLOCKED)`),
				mustField("Deploy executed / Deploy required", `\*\*Deploy executed:\*{0,2}\s+(yes|no)|Deploy required:\*{0,2}\s+(yes|no)`),
				mustField("send_to orchestrator", `send_to orchestrator:`),
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

// splitPhases splits scratchpad content into phase blocks keyed by phase number (1-7).
func splitPhases(content string) map[int]string {
	matches := phaseHeaderRe.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil
	}
	phases := make(map[int]string, donePhaseCount)
	for i, m := range matches {
		phaseNum := parseIntOrZero(content[m[2]:m[3]])
		if phaseNum < 1 || phaseNum > donePhaseCount {
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
	Phase  int    // phase number (1-7)
	Field  string // what was expected, as the agent must write it (never the regex)
	Detail string // why it failed
}

// ValidationResult is the outcome of validating a complete yesloop scratchpad.
type ValidationResult struct {
	Compliant     bool         // all phase blocks pass validation
	PhaseCount    int          // how many of the 7 phase blocks were found
	MissingPhases []int        // phase numbers missing from the content
	FieldErrors   []FieldError // individual field validation failures
	// Phase5Evidence maps the review sub-phase key (cold-review, consequence,
	// security) to the reported task() subagent session id. Populated when a
	// Phase 5 block exists; verified against opencode's session DB by
	// VerifySubagentEvidence in the DONE-guard.
	Phase5Evidence map[string]string
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
		for _, rf := range pv.RequiredFields {
			isStatusField := pv.IsStatusField && rf.label == "Status"
			if rf.re.MatchString(block) {
				if isStatusField {
					statusMatched = true
				}
				continue
			}
			if isStatusField {
				// Reported once below, with the reason a bare "not found" lacks.
				continue
			}
			errors = append(errors, FieldError{
				Phase:  pv.Number,
				Field:  rf.label,
				Detail: "required field not found in phase block",
			})
		}

		if pv.IsStatusField && !statusMatched {
			errors = append(errors, FieldError{
				Phase:  pv.Number,
				Field:  "Status",
				Detail: "missing or invalid status line (must be on its own line)",
			})
		}

		// Task-type-conditional fields: e.g. Depth-lock only for debug tasks.
		if pv.IfTaskType != "" && pv.IfTaskType == taskType {
			for _, rf := range pv.TaskTypeFields {
				if rf.re.MatchString(block) {
					continue
				}
				errors = append(errors, FieldError{
					Phase:  pv.Number,
					Field:  rf.label,
					Detail: "required field not found in phase block (conditional on task type " + pv.IfTaskType + ")",
				})
			}
		}
	}

	compliant := len(missing) == 0 && len(errors) == 0
	result := ValidationResult{
		Compliant:     compliant,
		PhaseCount:    len(phases),
		MissingPhases: missing,
		FieldErrors:   errors,
	}
	if p5, ok := phases[5]; ok {
		result.Phase5Evidence = extractPhase5Evidence(p5)
	}
	return result
}

// phase5EvidenceRes extracts the ses_ ids from the mandatory Phase 5 dispatch
// lines. Same shape as the presence regexes plus a capture group.
var phase5EvidenceRes = map[string]*regexp.Regexp{
	"cold-review": regexp.MustCompile(`(?m)^\*{0,2}cold-review:\*{0,2}[ \t]+(ses_\S+)`),
	"consequence": regexp.MustCompile(`(?m)^\*{0,2}consequence:\*{0,2}[ \t]+(ses_\S+)`),
	"security":    regexp.MustCompile(`(?m)^\*{0,2}security:\*{0,2}[ \t]+(ses_\S+)`),
}

// extractPhase5Evidence pulls the reported task() subagent session ids out of
// a Phase 5 block. Returns nil when no evidence line is present (format
// errors for those lines are already reported separately).
func extractPhase5Evidence(phase5 string) map[string]string {
	var evidence map[string]string
	for key, re := range phase5EvidenceRes {
		m := re.FindStringSubmatch(phase5)
		if len(m) >= 2 {
			if evidence == nil {
				evidence = make(map[string]string, len(phase5EvidenceRes))
			}
			evidence[key] = m[1]
		}
	}
	return evidence
}

// completedPhaseRe matches a phase block line with **Status:** COMPLETE.
var completedPhaseRe = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+COMPLETE`)

// blockedStatusRe matches a phase block line with **Status:** BLOCKED.
var blockedStatusRe = regexp.MustCompile(`(?m)^\*\*Status:\*\*\s+BLOCKED`)

// lastPhaseBlock returns the highest phase number present and its block. A
// pipeline is written in order, so the last block is where the agent stands.
func lastPhaseBlock(content string) (int, string) {
	phases := splitPhases(content)
	last := 0
	for number := range phases {
		if number > last {
			last = number
		}
	}
	return last, phases[last]
}

// claimsPipelineEnd reports whether the agent claims the pipeline is finished:
// the last two contract phases are written (7, or 6 for a loop that started
// before the verification became its own phase) and every block up to there is
// COMPLETE. A skeleton of headers, or a pipeline still being filled in, is not
// a claim.
func claimsPipelineEnd(content string) bool {
	last, _ := lastPhaseBlock(content)
	if last < donePhaseCount-1 {
		return false
	}
	phases := splitPhases(content)
	for number := 1; number <= last; number++ {
		block, ok := phases[number]
		if !ok || !completedPhaseRe.MatchString(block) {
			return false
		}
	}
	return true
}

// lastPhaseBlocked reports whether the agent's last block carries a BLOCKED
// status, where the pipeline can actually stop: the handoff blocks. A blocked
// Phase 1 is a legitimate pipeline stop, not a claim that the pipeline ended,
// and chasing it would turn a documented state into a "dead agent" (the status
// every early phase's validation explicitly allows).
func lastPhaseBlocked(content string) bool {
	last, block := lastPhaseBlock(content)
	if last < donePhaseCount-1 {
		return false
	}
	return blockedStatusRe.MatchString(block)
}

// CountCompletedPhases counts how many of the 7 phase blocks have **Status:** COMPLETE.
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
