package verify

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
)

// ParseReport turns a plaintext agent report into verifiable claims.
//
// Recognised sources are the core markers [COMMIT:], [BRANCH:], [MERGE:] and
// [DEPLOY:], a "Tests run: <command> -> exit <n>" line (exit 0 is a green
// claim, a non-zero exit an honest failure report), a "Files in scope:" line,
// and the markers of every registered claim type.
//
// A marker body that reads as a placeholder is a template, not an assertion,
// and yields no claim. A marker token no registered type claims is a defect:
// ignoring it silently would let a report smuggle a claim of an unknown shape
// past the verifier. The one exemption is the platform's citation form
// [ID: 97352], which names a source rather than a claim.
//
// Parsing is deliberately permissive: unknown lines are ignored. Overlong
// lines are skipped so a hostile line cannot make the scan expensive.
func ParseReport(text string) []Claim {
	var claims []Claim
	var commitValues []string
	claimed := coreMarkerTokens()
	for token := range claimtypes.MarkerTokens() {
		claimed[token] = true
	}

	type deferredTests struct {
		line    int
		raw     string
		command string
		code    int
	}
	var testsLines []deferredTests
	var filesClaim *Claim

	for index, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		line := index + 1
		if len(raw) > maxLineLength {
			continue
		}

		for _, match := range coreMarkerRe.FindAllStringSubmatch(raw, -1) {
			value := trimBackticks(strings.TrimSpace(match[2]))
			if looksLikePlaceholder(value) {
				// A template names no commit, no branch, no merge and no
				// deploy, and must not enter the commit binding below.
				continue
			}
			switch match[1] {
			case "COMMIT":
				claims = append(claims, Claim{Kind: "commit_exists", Line: line, Raw: raw, Commit: value})
				commitValues = append(commitValues, value)
			case "BRANCH":
				claims = append(claims, Claim{Kind: "branch_pushed", Line: line, Raw: raw, Branch: value})
			case "MERGE":
				claims = append(claims, Claim{Kind: "merge", Line: line, Raw: raw, Merge: value})
			case "DEPLOY":
				claims = append(claims, Claim{Kind: "deploy", Line: line, Raw: raw, Deploy: value})
			}
		}

		for _, match := range unknownMarkerRe.FindAllStringSubmatch(raw, -1) {
			if claimed[match[1]] {
				continue
			}
			if citationRe.MatchString(match[0]) {
				continue
			}
			if looksLikePlaceholder(match[2]) {
				continue
			}
			claims = append(claims, Claim{
				Kind:  "unknown_marker",
				Line:  line,
				Raw:   raw,
				Token: match[1],
			})
		}

		for _, claimType := range claimTypes() {
			if claimType.Match == nil {
				continue
			}
			for _, claim := range claimType.Match(raw) {
				if claimAssertsNothing(claim) {
					continue
				}
				claim.Line = line
				claim.Raw = raw
				claims = append(claims, claim)
			}
		}

		if match := testsRunRe.FindStringSubmatch(raw); match != nil {
			command := strings.TrimSpace(match[1])
			if !looksLikePlaceholder(command) {
				code, err := strconv.Atoi(match[2])
				if err == nil {
					testsLines = append(testsLines, deferredTests{line: line, raw: raw, command: command, code: code})
				}
			}
		}

		if match := filesInScopeRe.FindStringSubmatch(raw); match != nil {
			planned := splitList(match[1])
			if !anyPlaceholder(planned) {
				filesClaim = &Claim{Kind: "diff_scope", Line: line, Raw: raw, Files: planned}
			}
		}
	}

	// Dependent claims bind to the report's commit only when the report names
	// exactly one: two distinct commits make the binding a guess, and a guess
	// must never confirm anything.
	bound := boundCommit(commitValues)
	if bound != "" {
		for index := range claims {
			claim := &claims[index]
			claimType, known := claimTypeFor(claim.Kind)
			if claim.Kind != "branch_pushed" && claim.Kind != "merge" && !(known && claimType.BindsCommit) {
				continue
			}
			if claim.Commit == "" {
				claim.Commit = bound
			}
		}
	}

	for _, declined := range testsLines {
		kind := "tests_exit"
		if declined.code == 0 {
			kind = "tests_green"
		}
		claims = append(claims, Claim{
			Kind:        kind,
			Line:        declined.line,
			Raw:         declined.raw,
			Command:     declined.command,
			ClaimedExit: declined.code,
			HasExit:     true,
			Commit:      bound,
		})
	}

	if filesClaim != nil {
		filesClaim.Commit = bound
		claims = append(claims, *filesClaim)
	}

	sort.SliceStable(claims, func(i, j int) bool { return claims[i].Line < claims[j].Line })
	return claims
}

const maxLineLength = 8192

var (
	// The core markers. One lazy "anything but a bracket" capture keeps the
	// scan linear: a hostile whitespace run behind an unterminated marker
	// cannot backtrack cubically.
	coreMarkerRe = regexp.MustCompile(`\[(COMMIT|BRANCH|MERGE|DEPLOY):([^\]\[]*?)\]`)
	// A marker for a token no type claims: an upper-case token, a colon and a
	// bracket-free body. The body class never crosses a bracket, so every
	// attempt is bounded by the distance to the next one.
	unknownMarkerRe = regexp.MustCompile(`\[([A-Z][A-Z0-9]*):([^\[\]]*)\]`)
	// The platform's citation form is a citation, not a claim. The exemption
	// is exactly that shape: [ID: foo] stays a defect.
	citationRe     = regexp.MustCompile(`\A\[ID:\s*\d+(?:\s*,\s*\d+)*\]\z`)
	testsRunRe     = regexp.MustCompile(`^[ \t]*(?:\*\*)?Tests? run:[ \t]*(\S(?:.*\S)?)[ \t]+(?:->|→)[ \t]+exit[ \t]+(-?\d+)[ \t]*$`)
	filesInScopeRe = regexp.MustCompile(`^[ \t]*(?:\*\*)?Files in scope:[ \t]*(?:\*\*)?[ \t]*(\S.*?)[ \t]*$`)
	// An angle token is a template slot. The class excludes brackets and
	// whitespace, so scanning a hostile run of "<" stays linear.
	angleTokenRe = regexp.MustCompile(`<[^<>\s]+>`)
	commitShape  = regexp.MustCompile(`\A[0-9a-fA-F]{4,64}\z`)
)

func coreMarkerTokens() map[string]bool {
	return map[string]bool{"COMMIT": true, "BRANCH": true, "MERGE": true, "DEPLOY": true}
}

// looksLikePlaceholder reports whether a value reads as a template.
//
// True for an angle token anywhere in the value, for a truncating ellipsis (a
// truncated digest) and for the literal TODO. Surrounding backticks are
// ignored, so a marker-quoted template is recognised like an unquoted one.
//
// Everything else is a value, however unusual it looks: the parser must never
// silently drop a claim that some real world could satisfy.
func looksLikePlaceholder(value string) bool {
	value = trimBackticks(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	if angleTokenRe.MatchString(value) {
		return true
	}
	if truncatingEllipsis(value) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(value), "todo")
}

// truncatingEllipsis reports whether a value ends in an ellipsis that cuts a
// token short.
//
// "go test ./..." is not a template: its ASCII ellipsis is the tail of a path
// pattern, and a real command must stay a claim. A Unicode ellipsis never ends
// a path pattern.
func truncatingEllipsis(value string) bool {
	value = strings.TrimSpace(value)
	for _, ellipsis := range []string{"...", "\u2026"} {
		if !strings.HasSuffix(value, ellipsis) {
			continue
		}
		cut := len(value) - len(ellipsis)
		if cut == 0 {
			return true
		}
		if ellipsis == "..." && (value[cut-1] == '/' || value[cut-1] == '.') {
			return false
		}
		return true
	}
	return false
}

func trimBackticks(value string) string {
	return strings.Trim(value, "`")
}

// splitList splits a report list on commas and strips the quoting a report may
// carry around one entry.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.Trim(strings.TrimSpace(part), "`*")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func anyPlaceholder(values []string) bool {
	for _, value := range values {
		if looksLikePlaceholder(value) {
			return true
		}
	}
	return false
}

// claimAssertsNothing reports whether a parsed claim is a template: one
// placeholder field makes the whole claim name no value.
func claimAssertsNothing(claim Claim) bool {
	for _, value := range []string{
		claim.Commit, claim.Branch, claim.Merge, claim.Deploy, claim.Command,
		claim.Token, claim.Path, claim.SHA256, claim.Profile, claim.Plan,
	} {
		if looksLikePlaceholder(value) {
			return true
		}
	}
	for _, ref := range claim.Evidence {
		if looksLikePlaceholder(ref.Key) || looksLikePlaceholder(ref.ID) {
			return true
		}
	}
	return anyPlaceholder(claim.Files)
}

// boundCommit is the report's single hash-shaped commit, or "".
func boundCommit(values []string) string {
	candidates := make(map[string]bool, len(values))
	for _, value := range values {
		if commitShape.MatchString(value) {
			candidates[value] = true
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	for candidate := range candidates {
		return candidate
	}
	return ""
}
