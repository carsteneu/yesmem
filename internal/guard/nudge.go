package guard

import (
	"fmt"
	"strings"
)

// PurposeNudge is the second question the same RPC answers: which skill in the catalogue matches the user's
// message. One transport, one model chain, one cache; the template is the only difference.
const PurposeNudge = "nudge"

// nudgeTool names the question in the cache key. No tool call is judged, so neither harness's tool vocabulary
// applies.
const nudgeTool = "skill"

// maxCatalogChars bounds the catalogue in the nudge prompt. The file is repository content, and this prompt
// runs once per user turn.
const maxCatalogChars = 32000

// checkNudge asks the model which skill fits the message. The answer is a SUGGEST whose suggestion is the
// skill name, or PASS. Nothing here is enforced: a nudge suggests, the guard blocks.
func checkNudge(req Request) Decision {
	catalog := sectionBody(req.Rules, "Skill Catalog")
	if strings.TrimSpace(catalog) == "" {
		return Decision{Decision: "PASS", Detail: "no skill catalog to match against"}
	}
	if len(catalog) > maxCatalogChars {
		catalog = catalog[:maxCatalogChars]
	}
	// The message is the harness's addendum and untrusted: the same cap the guard applies to its context.
	message, truncated := truncateContext(req.Context)
	if strings.TrimSpace(message) == "" {
		return Decision{Decision: "PASS", Detail: "no message to match against"}
	}
	resolution, err := resolve(req.DataDir)
	if err != nil || resolution.APIKey == "" {
		return Decision{Decision: "PASS", Detail: "no guard credential", Truncated: truncated}
	}
	req.Context = message
	// The question has one tool name of its own: the caller's tool vocabulary is the guard's business, and the
	// answer cache is keyed on this.
	req.Tool = nudgeTool
	decision := judge(resolution, req, catalog)
	// The nudge answers with a skill name or nothing. A PASS the model answered stays a PASS *without* a
	// detail: that is the caller's "definitely no match" and may be cached. A guard-shaped answer (BLOCK,
	// UNAVAILABLE) is not an answer to this question, so it becomes a PASS with a reason and is retried.
	if decision.Decision != "SUGGEST" {
		if decision.Decision == "PASS" && decision.Detail == "" {
			return Decision{Decision: "PASS", Model: decision.Model, Truncated: truncated}
		}
		detail := decision.Detail
		if detail == "" {
			detail = "answer outside the nudge's decision set"
		}
		return Decision{Decision: "PASS", Detail: detail, Model: decision.Model, Truncated: truncated}
	}
	decision.Truncated = truncated
	return decision
}

// buildNudgePrompt is the nudge's template. It asks for the same JSON shape the guard answers in, so one
// parser serves both questions and a second decision format cannot drift into existence.
func buildNudgePrompt(catalog, message string) string {
	return fmt.Sprintf(`You match a user message against a skill catalog. Output ONLY valid JSON. Do not reason, explain, or think. No explanation.

SKILL CATALOG:
%s

USER MESSAGE: %s

Response options:
- SUGGEST: exactly one skill of the catalog matches the message → {"decision":"SUGGEST","suggestion":"<skill name>"}
- PASS: no skill matches → {"decision":"PASS"}

For SUGGEST, the suggestion field MUST contain one skill name from the catalog: no reason, no formatting, nothing before or after the name.`, catalog, message)
}
