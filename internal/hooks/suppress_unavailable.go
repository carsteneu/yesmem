package hooks

// maybeSuppressUnavailableSkill drops a SUGGEST decision to PASS when the
// suggested skill is not in the available (agent-loadable) set. This catches
// suggestions that originate from prose references in Project-Specific rules
// (e.g. "prefer yesmem MCP code tools") where DeepSeek infers a skill name
// without it being in the catalog — the catalog filter in FilterStaleSkillRules
// cannot reach these.
//
// Conservative fallback: if available is nil/empty (e.g. filesystem scan
// failed), pass through unchanged — never silently drop a suggestion due to
// missing data. Non-SUGGEST decisions and empty suggestions pass through
// unchanged. Uses extractSkillName to parse the "<skill>: <reason>" format
// produced by DeepSeek.
func maybeSuppressUnavailableSkill(d GuardDecision, available map[string]bool) GuardDecision {
	if d.Decision != "SUGGEST" || d.Suggestion == "" {
		return d
	}
	if len(available) == 0 {
		return d
	}
	skill := extractSkillName(d.Suggestion)
	if skill == "" {
		return d
	}
	if !available[skill] {
		return GuardDecision{Decision: "PASS"}
	}
	return d
}
