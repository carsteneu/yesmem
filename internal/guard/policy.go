package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// canBlockTools is the set of tool names where a BLOCK is honoured unconditionally: a file write is judged on
// its content alone.
var canBlockTools = map[string]bool{
	"Write": true,
	"Edit":  true,
}

// shellTools are the tools that carry a command. Their BLOCK is not honoured unconditionally: the guard has no
// conversation context and cannot tell whether the user authorised the action this turn, so only a citation
// the Decision Policy classifies as objectively detectable is enforced (policyBlockRules).
var shellTools = map[string]bool{
	"Bash": true,
	"REPL": true,
}

// policyBlockRules are the rule numbers the Decision Policy in RULES.md allows the guard to BLOCK on from the
// tool input alone: LLM signature in commit message text (1), secret in the call (2), destructive command
// (25), PR merge via CLI or API (32). Everything else is a SUGGEST there.
var policyBlockRules = map[int]bool{
	1:  true,
	2:  true,
	25: true,
	32: true,
}

// ruleCitation matches the word that introduces a rule citation, with the singular/plural form kept as a
// submatch.
var ruleCitation = regexp.MustCompile(`(?i)\b(rules?)\b`)

// citationHead captures the number list that follows a rule word: "Rule 25", "Rule #32", "Rule No. 25",
// "Rule-25", "Rules 2, 4 and 25". Case-insensitive for the qualifier too: the model writes "Rule No. 25" as
// readily as "Rule no. 25", and a lost citation is a lost block.
var citationHead = regexp.MustCompile(`(?i)^[\s#]*(?:number|no\.?|nr\.?)?[\s\-]*(#?\d+(?:(?:\s*,\s*|\s+and\s+|\s*&\s*)#?\d+)*)`)

var citationDigits = regexp.MustCompile(`\d+`)

// citedRuleNumbers returns the rule numbers a violation cites in the positions that make them citations.
func citedRuleNumbers(violation string) []int {
	var numbers []int
	matches := ruleCitation.FindAllStringSubmatchIndex(violation, -1)
	for _, match := range matches {
		head := citationHead.FindStringSubmatch(violation[match[1]:])
		if head == nil {
			continue
		}
		list := head[1]
		// A comma list belongs to the plural form ("Rules 2, 4 and 25"). Behind a singular citation the comma
		// is prose ("Rule 14, 25 characters exceed the limit").
		if !strings.EqualFold(violation[match[2]:match[3]], "rules") {
			if comma := strings.Index(list, ","); comma >= 0 {
				list = list[:comma]
			}
		}
		for _, digits := range citationDigits.FindAllString(list, -1) {
			if n, err := strconv.Atoi(digits); err == nil {
				numbers = append(numbers, n)
			}
		}
	}
	return numbers
}

// citesPolicyBlockRule reports whether a violation cites at least one rule of the given set. One is enough: a
// violation naming a policy rule among advisory ones still describes an act the policy calls blockable, and
// downgrading it would let exactly that act through.
func citesPolicyBlockRule(violations []string, blockable map[int]bool) bool {
	for _, violation := range violations {
		for _, number := range citedRuleNumbers(violation) {
			if blockable[number] {
				return true
			}
		}
	}
	return false
}

// numberedRuleHead is a rule line with the number as a submatch: the catalogue's own numbering, not a
// citation.
var numberedRuleHead = regexp.MustCompile(`(?m)^\s*(\d+)\.\s`)

// catalogueRuleNumbers is the set of rules the prompt actually carries. Only the numbered catalogue counts:
// the Decision Policy is prose and may be written as a numbered list, and its numbers must not make a rule
// exist (YM-456). The project's deviations count, because a rule the checkout numbers in the reserved range is
// one the model may cite; every line below that range was stripped of its number when the block was composed
// (stripUnreservedNumbers), so it cannot ground a citation of the standard rule of the same number.
func catalogueRuleNumbers(rules string) map[int]bool {
	numbers := make(map[int]bool)
	for _, match := range numberedRuleHead.FindAllStringSubmatch(citableRulesText(rules), -1) {
		if number, err := strconv.Atoi(match[1]); err == nil {
			numbers[number] = true
		}
	}
	return numbers
}

// citableRulesText is the rules text without the sections that carry no citable rule: the Decision Policy (prose,
// possibly numbered) and the Skill Catalog (data). Everything else stays, the project's deviations included.
func citableRulesText(rules string) string {
	const policyHeading, policyEnd, catalogHeading = "## Decision Policy", "## End of Decision Policy", "## Skill Catalog"
	start := strings.Index(rules, policyHeading)
	if start < 0 {
		return rules
	}
	rest := rules[start:]
	end := strings.Index(rest, policyEnd)
	if end < 0 {
		return rules[:start]
	}
	after := rest[end+len(policyEnd):]
	if catalog := strings.Index(after, catalogHeading); catalog >= 0 {
		after = after[:catalog]
	}
	return rules[:start] + after
}

// downgradeUnknownCitation turns a BLOCK that cites no rule of the prompt into a notice. The prompt embeds
// RULES.md, so the answer is untrusted input: the audit saw a model-invented "Rule 42" reject a write that no
// rule described.
//
// A notice, not a pass: the model was worried about something and a silent release would hide it, so the text
// travels with the call ("SUGGEST - rule check did not block" for the opencode adapter, additionalContext for
// the hook). A catalogue without numbered rules cannot be checked at all; there the decision stands unchanged,
// as it did before the policy was read. A catalogue that mixes prose rules with a numbered list is judged by
// the numbered lines alone (PromptRules sends only those), so a citation outside them is not grounded in what
// the model was given and cannot reject.
func downgradeUnknownCitation(d Decision, rules string) Decision {
	if d.Decision != "BLOCK" {
		return d
	}
	known := catalogueRuleNumbers(rules)
	if len(known) == 0 {
		return d
	}
	cited := 0
	for _, violation := range d.Violations {
		for _, number := range citedRuleNumbers(violation) {
			cited++
			if known[number] {
				return d
			}
		}
	}
	// No citation at all is not the same as a false one: a block that names a rule in prose ("writes a live
	// key into the repository") was always allowed to refuse, and this filter is about numbers.
	if cited == 0 {
		return d
	}
	reason := strings.Join(d.Violations, "; ")
	if reason == "" {
		reason = "review against RULES.md before proceeding"
	}
	return Decision{
		Decision:   "SUGGEST",
		Suggestion: "yesmem-remember: mandatory check — " + reason,
		Violations: d.Violations,
		Model:      d.Model,
	}
}

// downgradeUnauthorizedBlock converts a BLOCK decision to a mandatory-check SUGGEST when the tool may not
// block on the cited rules. Write/Edit always may; shell tools only on a citation of the policy's own BLOCK
// set. The original violations are preserved in the suggestion text so the user sees what triggered the check.
func downgradeUnauthorizedBlock(d Decision, toolName string, blockable map[int]bool) Decision {
	if d.Decision != "BLOCK" {
		return d
	}
	if canBlockTools[toolName] || (shellTools[toolName] && citesPolicyBlockRule(d.Violations, blockable)) {
		return d
	}
	reason := strings.Join(d.Violations, "; ")
	if reason == "" {
		reason = "review against RULES.md before proceeding"
	}
	// The violations stay on the decision: a harness that renders "activate <suggestion>" reads them as the
	// advisory note they are, instead of turning this text into a skill name (opencode's adapter does).
	return Decision{
		Decision:   "SUGGEST",
		Suggestion: "yesmem-remember: mandatory check — " + reason,
		Violations: d.Violations,
	}
}

// blockOption is the BLOCK line the prompt offers for this tool. For shell tools it is narrowed to the rules
// the policy enforces: offering the unrestricted option would invite BLOCKs that have to be downgraded again.
func blockOption(toolName string) string {
	switch {
	case canBlockTools[toolName]:
		return `- BLOCK: call violates a rule → {"decision":"BLOCK","violations":["Rule X: reason"]}` + "\n"
	case shellTools[toolName]:
		return `- BLOCK: the command itself violates rule 1 (LLM signature in a commit message), rule 2 (secret on the command line), rule 25 (destructive command) or rule 32 (PR merge via CLI or API) → {"decision":"BLOCK","violations":["Rule X: reason"]}` + "\n" +
			`  Every other rule is a SUGGEST, never a BLOCK: the guard sees no conversation and cannot judge authorization.` + "\n"
	}
	return ""
}

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return "", os.ErrNotExist
	}
	return home, nil
}

func defaultDataDir(home string) string {
	return filepath.Join(home, ".claude", "yesmem")
}

// maxMessageChars bounds a message that reaches a harness's context.
const maxMessageChars = 1000

// SanitizeMessage collapses a model-produced string to one bounded line. The prompt embeds RULES.md, so a
// violation string can carry text an attacker steered; collapsing whitespace and control characters keeps it
// from forging structure (a second "RULE:" block, a fake system line) in the text the agent reads. It is not a
// boundary against a hostile repository, which can answer PASS just as well. The opencode adapter applies the
// same rule in TypeScript (guardMessage), because it builds the tool error itself.
func SanitizeMessage(text string) string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r <= 0x1f || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029
	})
	collapsed := strings.Join(fields, " ")
	collapsed = strings.Join(strings.Fields(collapsed), " ")
	if len(collapsed) > maxMessageChars {
		return collapsed[:maxMessageChars] + "…"
	}
	return collapsed
}
