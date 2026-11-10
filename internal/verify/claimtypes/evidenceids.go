package claimtypes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// evidenceMarkerRe matches an evidence marker: one body of key=value slots.
var evidenceMarkerRe = regexp.MustCompile(`\[EVIDENCE:([^\]\[]*)\]`)

// evidencePairRe matches one slot of the evidence body. The value is everything
// after the first "=", so a value may carry "=" of its own.
var evidencePairRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)=(.*)$`)

// MatchEvidence parses [EVIDENCE: cold-review=ses_a, consequence=ses_b].
//
// A slot whose body is not key=value becomes a slot with an empty id: the
// report names a slot it cannot fill, which the checker reports as a defect.
// Dropping it instead would let a typo remove a slot from the check.
func MatchEvidence(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range evidenceMarkerRe.FindAllStringSubmatch(line, -1) {
		refs := parseEvidenceRefs(match[1])
		if len(refs) == 0 {
			continue
		}
		claims = append(claims, engine.Claim{Kind: "evidence_ids", Evidence: refs})
	}
	return claims
}

func parseEvidenceRefs(body string) []engine.EvidenceRef {
	var refs []engine.EvidenceRef
	for _, part := range strings.Split(body, ",") {
		part = trimQuotes(part)
		if part == "" {
			continue
		}
		pair := evidencePairRe.FindStringSubmatch(part)
		if pair == nil {
			refs = append(refs, engine.EvidenceRef{Key: part})
			continue
		}
		refs = append(refs, engine.EvidenceRef{Key: pair[1], ID: pair[2]})
	}
	return refs
}

// CheckEvidence judges an [EVIDENCE: ...] claim.
//
// The store answers about the world, so a store that is not wired and a store
// that cannot answer are environment gaps: the report is not at fault and the
// verdict must not read as a falsified claim.
func CheckEvidence(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if ctx.Sessions == nil {
		return engine.Unverifiable(
			"no session store is configured; the evidence ids cannot be resolved").
			WithClass(engine.ClassEnvironment)
	}
	if ctx.ReportSession == "" {
		return engine.Unverifiable(
			"the caller named no session the report belongs to; the evidence ids cannot be attributed").
			WithClass(engine.ClassEnvironment)
	}

	findings := EvidenceFindings(claim.Evidence, ctx.ReportSession, ctx.Sessions, ctx.Evidence)
	if len(findings) == 0 {
		return engine.Confirmed(fmt.Sprintf(
			"%d evidence slot(s) resolve to distinct subagents of %s",
			len(claim.Evidence), ctx.ReportSession))
	}
	class := engine.ClassEnvironment
	for _, finding := range findings {
		if finding.Class == engine.ClassDefect {
			class = engine.ClassDefect
			break
		}
	}
	return engine.Unverifiable(joinFindingDetails(findings)).WithClass(class)
}

// EvidenceFindings applies the evidence rule: every slot names a session that
// exists, descends from the report's own session, runs as an agent type the
// slot allows, and no id serves two slots.
//
// This is the single implementation of the rule. The DONE gate reaches it over
// the yesmem session store, the verification operation over whatever store its
// caller wired; neither carries a copy.
func EvidenceFindings(refs []engine.EvidenceRef, reportSession string, store engine.SessionStore, policy engine.EvidencePolicy) []engine.Finding {
	byKey, unknown, duplicated := evidenceByKey(refs, policy.Order)
	order := policy.Order
	if len(order) == 0 {
		order = keysInReportOrder(refs)
	}

	var findings []engine.Finding
	for _, key := range duplicated {
		findings = append(findings, engine.Finding{
			Field:  key,
			Class:  engine.ClassDefect,
			Detail: fmt.Sprintf("evidence slot %s names more than one session id", key),
		})
	}
	for _, key := range order {
		if len(policy.Order) > 0 && !hasKey(byKey, key) {
			// The policy says which slots a complete report carries. A slot the
			// report leaves out is not an omission the checker may fill in.
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("evidence slot %s is missing", key),
			})
			continue
		}
		if hasDuplicate(duplicated, key) {
			// Already reported above; the surviving value names no single session.
			continue
		}
		sessionID := byKey[key]
		if sessionID == "" {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("evidence slot %s names no session id", key),
			})
			continue
		}
		if previous, reused := duplicateSlot(byKey, order[:indexOf(order, key)], sessionID); reused {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("session id %s serves both %s and %s; each slot needs its own dispatch", sessionID, key, previous),
			})
			continue
		}
		record, found, err := store.Session(sessionID)
		if err != nil {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassEnvironment,
				Detail: fmt.Sprintf("INFRASTRUCTURE, not the report: session %s could not be resolved (%v)", sessionID, err),
			})
			continue
		}
		if !found {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("subagent session %s not found in the session store", sessionID),
			})
			continue
		}
		if record.ParentID != reportSession {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("subagent session %s has parent_id %q, expected the report's session %q", sessionID, record.ParentID, reportSession),
			})
		}
		if !agentAllowed(key, record.AgentType, policy.Allowed) {
			findings = append(findings, engine.Finding{
				Field:  key,
				Class:  engine.ClassDefect,
				Detail: fmt.Sprintf("subagent session %s ran as agent %q, not in allowed set %v", sessionID, record.AgentType, policy.Allowed[key]),
			})
		}
	}
	for _, key := range unknown {
		findings = append(findings, engine.Finding{
			Field:  key,
			Class:  engine.ClassDefect,
			Detail: fmt.Sprintf("unknown evidence slot %q: the policy names %v", key, policy.Order),
		})
	}
	return findings
}

// hasKey reports whether the slot appears at all, whatever its value.
func hasKey(byKey map[string]string, key string) bool {
	_, ok := byKey[key]
	return ok
}

func hasDuplicate(duplicated []string, key string) bool {
	for _, candidate := range duplicated {
		if candidate == key {
			return true
		}
	}
	return false
}

func indexOf(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func evidenceByKey(refs []engine.EvidenceRef, order []string) (map[string]string, []string, []string) {
	known := make(map[string]bool, len(order))
	for _, key := range order {
		known[key] = true
	}
	byKey := make(map[string]string, len(refs))
	var unknown, duplicated []string
	for _, ref := range refs {
		if len(order) > 0 && !known[ref.Key] {
			unknown = append(unknown, ref.Key)
			continue
		}
		if _, seen := byKey[ref.Key]; seen {
			// Two ids for one slot: the last one would win silently, and the
			// first would never be checked. The report is contradictory, which is
			// a defect in the report and not a fact about any session.
			if !hasDuplicate(duplicated, ref.Key) {
				duplicated = append(duplicated, ref.Key)
			}
			continue
		}
		byKey[ref.Key] = ref.ID
	}
	return byKey, unknown, duplicated
}

func keysInReportOrder(refs []engine.EvidenceRef) []string {
	var order []string
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if seen[ref.Key] {
			continue
		}
		seen[ref.Key] = true
		order = append(order, ref.Key)
	}
	return order
}

// duplicateSlot names the earlier slot that already carried sessionID.
func duplicateSlot(byKey map[string]string, earlier []string, sessionID string) (string, bool) {
	for _, key := range earlier {
		if byKey[key] != "" && byKey[key] == sessionID {
			return key, true
		}
	}
	return "", false
}

func agentAllowed(key, agentType string, allowed map[string][]string) bool {
	if len(allowed[key]) == 0 {
		// A slot the policy expresses no opinion about accepts any agent: the
		// check is about the session's provenance, not about its name.
		return true
	}
	for _, candidate := range allowed[key] {
		if candidate == agentType {
			return true
		}
	}
	return false
}

// joinFindingDetails keeps a multi-finding verdict one readable sentence.
func joinFindingDetails(findings []engine.Finding) string {
	const maxDetails = 3
	parts := make([]string, 0, maxDetails)
	for index, finding := range findings {
		if index == maxDetails {
			parts = append(parts, fmt.Sprintf("%d more", len(findings)-maxDetails))
			break
		}
		parts = append(parts, finding.Detail)
	}
	return strings.Join(parts, "; ")
}
