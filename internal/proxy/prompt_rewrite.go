package proxy

import "strings"

// stripSystemSection removes a markdown section starting with "# header\n"
// from all system blocks. The section ends at the next "\n# " or EOF.
// Returns true if any block was modified.
func stripSystemSection(req map[string]any, header string) bool {
	blocks := ensureSystemArray(req)
	modified := false
	needle := "# " + header + "\n"
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		idx := strings.Index(text, needle)
		if idx < 0 {
			continue
		}
		// Find end of section: next "\n# " after idx+len(needle)
		rest := text[idx+len(needle):]
		end := strings.Index(rest, "\n# ")
		var cleaned string
		if end < 0 {
			// Section goes to EOF — remove from idx
			cleaned = strings.TrimRight(text[:idx], " \t\n")
		} else {
			// Keep everything before idx and from "\n# " onward
			cleaned = text[:idx] + rest[end+1:]
		}
		bm["text"] = cleaned
		blocks[i] = bm
		modified = true
	}
	if modified {
		req["system"] = blocks
	}
	return modified
}

// stripSystemLine removes a specific line from all system blocks.
// Backs up to the previous newline boundary and removes the entire line.
// Returns true if any block was modified.
func stripSystemLine(req map[string]any, line string) bool {
	blocks := ensureSystemArray(req)
	modified := false
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		idx := strings.Index(text, line)
		if idx < 0 {
			continue
		}
		// Find the start of this line (back up to previous newline)
		start := idx
		if start > 0 && text[start-1] == '\n' {
			start--
		}
		// Find the end of this line (include its trailing newline if present)
		end := idx + len(line)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		cleaned := text[:start] + text[end:]
		bm["text"] = cleaned
		blocks[i] = bm
		modified = true
	}
	if modified {
		req["system"] = blocks
	}
	return modified
}

// StripOutputEfficiency removes the "# Output efficiency" markdown section
// from all system prompt blocks. Returns true if any block was modified.
func StripOutputEfficiency(req map[string]any) bool {
	return stripSystemSection(req, "Output efficiency")
}

// StripToneBrevity removes the line "Your responses should be short and concise."
// from all system prompt blocks. Returns true if any block was modified.
func StripToneBrevity(req map[string]any) bool {
	return stripSystemLine(req, "Your responses should be short and concise.")
}

// InjectAntDirectives appends a system block tagged [yesmem-directives] with
// behavioral directives that reinforce honest, verification-first reporting.
func InjectAntDirectives(req map[string]any) {
	const directives = `Before reporting a task complete, verify it actually works: run the test, execute the script, check the output. If you can't verify, say so explicitly rather than claiming success.

Report outcomes faithfully: if tests fail, say so with the relevant output; if you did not run a verification step, say that rather than implying it succeeded. Never claim "all tests pass" when output shows failures, never suppress or simplify failing checks to manufacture a green result, and never characterize incomplete or broken work as done.

If you notice the user's request is based on a misconception, or spot a bug adjacent to what they asked about, say so. You're a collaborator, not just an executor — users benefit from your judgment, not just your compliance.

Err on the side of more explanation. What's most important is the reader understanding your output without mental overhead or follow-ups, not how terse you are.`
	ReplaceSystemBlock(req, "yesmem-directives", directives)
}

// InjectCLAUDEMDAuthority appends a system block tagged [yesmem-enhance] that
// reinforces the authority of CLAUDE.md and MEMORY.md instructions.
func InjectCLAUDEMDAuthority(req map[string]any) {
	const authority = `The CLAUDE.md and MEMORY.md files contain authoritative project rules and user instructions. Follow them precisely — they represent the user's accumulated decisions and are not optional context.

Comment discipline: Write comments only when the WHY is non-obvious. Do not explain WHAT code does — the code speaks for itself. Do not remove existing comments unless you are removing the code they describe.`
	ReplaceSystemBlock(req, "yesmem-enhance", authority)
}

// InjectClaudeToolPrefs appends a system block tagged [yesmem-tool-prefs] that
// restores tool-preference guidance Claude Code dropped when REPL shadowed
// Read/Bash/Grep/Glob/NotebookEdit. For file mutations and error-critical
// operations the structured tools remain safer than REPL shorthands.
// Claude-specific: contains CLAUDE_CODE_REPL text, only invoked for ProfileClaude.
func InjectClaudeToolPrefs(req map[string]any) {
	const prefs = `File mutation: prefer Edit and Write over sh('sed ...') or put() with heredocs. REPL shorthands (sh/cat/rg/gl) are for investigation, not mutation. For error-critical operations (git, deploy, destructive actions) use direct tool calls — sh/cat return errors as strings (soft-fail), not as tool errors.

REPL-native classic tools: under CLAUDE_CODE_REPL=true, Read/Glob/Grep/Bash live as REPL globals — call await Read({file_path}), await Glob({pattern}), await Grep({pattern, path}), await Bash({command}) INSIDE REPL. await Read registers with the Edit/Write file-tracker (required before Edit on existing files). Shorthands cat/rg/gl/sh remain for quick investigation without tracker side-effects. Task → top-level Agent; TodoWrite → TaskCreate/Update/List/Get. Files created via raw sh (echo > file) are invisible to await Read because they are not in CC's registry — use Write for files the session needs to Read later.`
	ReplaceSystemBlock(req, "yesmem-tool-prefs", prefs)
}

// InjectOutputDiscipline appends a system block tagged
// [yesmem-output-discipline] that restores terse-output rules removed from
// Claude Code's `# Output efficiency` and `# Tone and style` sections during
// the 2026-03→04 prompt redesign, and reinforces the
// "don't narrate internal deliberation" rule.
func InjectOutputDiscipline(req map[string]any) {
	const discipline = `Before tool actions: one-sentence status of what you are about to do ("Reading X", "Running Y"). Before text-only answers: start with the content directly, no framing sentence ("Here is...", "Session with 543 messages —"). No multi-sentence preamble describing reasoning. No visible skill-eval blocks or meta-commentary on your own deliberation — evaluation is mental. Keep end-of-turn summaries to 1-2 sentences or omit if output state is already visible. Simple questions get direct answers, no headers or sections for trivial content. For exploratory questions ("what if", "how would you"): 2-3 sentences with recommendation + main tradeoff, redirectable. Do not create planning, decision, or analysis documents unless the user explicitly asks, or you are in an active set_plan/update_plan flow where the document is part of plan persistence. Do not echo injected timestamps or [msg:N] markers at the start of your responses — the proxy-injected [HH:MM:SS] [msg:N] [+delta] is for reference, never repeat it.`
	ReplaceSystemBlock(req, "yesmem-output-discipline", discipline)
}

// InjectDeepSeekOutputDiscipline appends a system block tagged
// [yesmem-output-discipline] with a relaxed variant suited to DeepSeek
// models (naturally concise, no brevity enforcement needed). Retains
// structural guidance (pre-tool status + no framing preamble) but drops
// the "keep it short" clamp and allows natural conversational warmth.
func InjectDeepSeekOutputDiscipline(req map[string]any) {
	const discipline = `ATTENTION — SKILL CHECK: For each response, evaluate whether the current task matches any available skill or /command. If so, use the Skill tool NOW — before proceeding with other work.

Before tool actions: one-sentence status of what you are about to do ("Reading X", "Running Y"). Before text-only answers: start with the content directly, no framing sentence ("Here is...", "Session with 543 messages —"). No visible skill-eval blocks or meta-commentary on your own deliberation — evaluation is mental. Do not echo injected timestamps or [msg:N] markers at the start of your responses — the proxy-injected [HH:MM:SS] [msg:N] [+delta] is for reference, never repeat it.
`
	ReplaceSystemBlock(req, "yesmem-output-discipline", discipline)
}

// InjectCodingDiscipline appends a system block tagged
// [yesmem-coding-discipline] that restores rules removed from Claude Code's
// `# Doing tasks` section: read-before-propose, no brute-force retry,
// no half-finished work, UI/frontend browser-test requirement.
func InjectCodingDiscipline(req map[string]any) {
	const discipline = `Before code changes: read the affected files first — no proposals for unread code. When blocked (failing test, API, build): don't brute-force retry — test an alternative or use AskUserQuestion. No half-finished implementations; done = tested AND verified. Do not report UI/frontend work as done without browser-testing golden path + edge cases + regressions. No time estimates. Always TDD for Go code: write tests first, then implement. Set a plan early: at the start of iterative work — debugging across more than ~5 tool cycles, exploring more than one hypothesis, or touching multiple files/worktrees — call set_plan first with a short 3-point plan, then update_plan after each pivot. Plans are thread-scoped, survive collapse via re-injection, and are the only context-loss-proof anchor for the active task.`
	ReplaceSystemBlock(req, "yesmem-coding-discipline", discipline)
}

// InjectBeweislast appends a system block tagged [yesmem-beweislast] that
// consolidates verification and honesty principles: fabrication guard,
// claim-vs-proof, stance-under-challenge, tool-result-honesty, long-context-erosion,
// self-check.
func InjectBeweislast(req map[string]any) {
	const beweislast = `Verify before claiming. Fabrication guard: do not invent file paths, function names, API endpoints, or version numbers — if uncertain, grep/read first or mark the claim as unverified. Claim-vs-proof: do not report success ("tests pass", "deploy succeeded") without running the verification step and showing the output; if you skipped verification, say so explicitly. Stance-under-challenge: when the user pushes back, do not collapse into agreement if you have a verified reason — restate the evidence and only update your position when new information appears. Tool-result-honesty: if a tool returned errors, partial output, or nothing, report that literally, not a smoothed-over summary. Long-context-erosion: early-conversation facts stay authoritative unless superseded — if a current claim contradicts something stated earlier in the same conversation, flag the contradiction before proceeding. Self-check: before finalizing a consequential answer, run a mental self-check — which concrete claims did I make, is each one verified or explicitly marked as an assumption; if a claim appears without evidence, re-verify or retract before sending. Self-check is internal — no visible "let me verify" blocks in the response.`
	ReplaceSystemBlock(req, "yesmem-beweislast", beweislast)
}

// InjectScopeDiscipline appends a system block tagged [yesmem-scope-discipline]
// that enforces scope-bound authorization while mandating that bugs, security
// issues, and misconceptions adjacent to the work MUST be surfaced — silence
// is worse than scope-drift.
func InjectScopeDiscipline(req map[string]any) {
	const scope = `Execute the user's request, not adjacent improvements. When the user asks for A, deliver A — do not silently bundle B and C into the change because they seem related. But: bugs, security issues, broken assumptions, or misconceptions you notice adjacent to the work MUST be surfaced as a separate note or question — silence on what you see is worse than scope-drift. The rule is: don't silently fix extra things, but do flag what you see. Authorization covers doing, not seeing.`
	ReplaceSystemBlock(req, "yesmem-scope-discipline", scope)
}

// InjectDelegationContract appends a system block tagged
// [yesmem-delegation-contract] that defines the contract for dispatching
// subagents: self-contained prompts, explicit report form, parallel dispatch,
// plus model tier guidance for choosing Opus/Sonnet/Haiku.
func InjectDelegationContract(req map[string]any) {
	const delegation = `Agent prompts must be self-contained. Include: the goal in one sentence, what has already been checked or ruled out, the expected report form (text vs artifact vs yes/no), and whether the agent should write code or only research. Do not reference "we just discussed" or "the previous session" — the agent has no shared context. For parallel dispatch: all agents launched in a single message block, not sequentially. Each agent's report is the tool result; synthesize it yourself rather than asking the agent to synthesize across other agents. Model tier guidance: Opus for orchestration and high-judgment work (planning, design synthesis, architecture decisions). Sonnet for implementation, focused code tasks, and nuanced documentation. Haiku for structured outputs (schema-bound extraction, pattern-matching refactors, diff generation, templated changelog entries) — not for prose documentation requiring judgment on what matters.`
	ReplaceSystemBlock(req, "yesmem-delegation-contract", delegation)
}

// InjectClarifyFirst appends a system block tagged [yesmem-clarify-first] that
// restores a conservative clarification discipline Claude Code dropped in the
// 2026-03→04 prompt redesign. Threshold is deliberately narrow: clarify ONLY
// when alternative interpretations would produce materially different work.
// Surface ambiguity (naming/ordering/style) → state assumption and proceed.
// Explicit fire-and-forget signals skip clarification entirely, preserving the
// CLAUDE.md hard rule.
func InjectClarifyFirst(req map[string]any) {
	const clarify = `Before implementing an ambiguous request, ask 1–3 clarifying questions ONLY when alternative interpretations would produce materially different work (different architecture, different API shape, different scope). For surface ambiguity (naming, ordering, minor style), state your assumption and proceed. Skip clarifying entirely when the user said "do it", "fire-and-forget", or gave an explicit execution signal.`
	ReplaceSystemBlock(req, "yesmem-clarify-first", clarify)
}

// InjectCodeToolsFirst appends a system block tagged [yesmem-code-tools-first]
// that directs all agents (Claude, Codex, Opencode) to prefer yesmem MCP
// code-navigation tools over shell commands or Agent spawns for codebase
// exploration. The MCP tools are faster, use less context, and leverage the
// pre-built code graph.
// project is the full path used as the `project` parameter for MCP code tools.
func InjectCodeToolsFirst(req map[string]any, project string) {
	const codeTools = `IMPORTANT: For code navigation and codebase understanding, always use yesmem code tools first — never raw grep/find/cat/shell for symbol lookups or file browsing:

- search_code_index("pattern", project) — find symbols by name
- get_file_index(project, dir) — list files with gotcha annotations
- get_code_snippet("qualified_name", project) — full source without file I/O
- graph_traverse("node", project) — all call paths in one call
- get_file_symbols("file", project) — top-level symbols with line numbers

Never spawn Agent for simple symbol lookups or file browsing — yesmem tools are faster.
You must docs_search("query") before guessing API behavior, parameters, or return types.`
	block := codeTools
	if project != "" {
		block = "Project: `" + project + "`\n\n" + codeTools
	}
	ReplaceSystemBlock(req, "yesmem-code-tools-first", block)
}

// InjectWikiFirst appends a system block tagged [yesmem-wiki-first] that
// directs all agents to check the per-file wiki page BEFORE editing any file.
// The wiki contains accumulated learnings, gotchas, co-edit context, and
// session history for each file — editing blind is editing dangerous.
func InjectWikiFirst(req map[string]any, project string) {
	const wiki = `BEFORE editing any file, check its wiki page first for per-file learnings, gotchas, and co-edit context. The wiki lives at ~/.claude/yesmem/wiki/<project>/files/<path>.md (path encoding: / → _). This is not optional — files carry accumulated gotchas and past decisions that you cannot infer from reading the source alone. If the wiki page does not exist yet, that is fine, but you must check. Falls back to search_code_index / get_code_snippet for deep symbol exploration after the wiki check.`
	block := wiki
	if project != "" {
		block = "Project: `" + project + "`\n\n" + wiki
	}
	ReplaceSystemBlock(req, "yesmem-wiki-first", block)
}

// personaTones maps verbosity preference to tone directives.
var personaTones = map[string]string{
	"verbose": "The user prefers detailed explanations. Err on the side of more explanation — show your reasoning, describe trade-offs, and explain why you chose an approach. Thoroughness matters more than brevity.",
	"concise": "The user prefers concise responses. Be direct and efficient, but never sacrifice clarity for brevity. When something needs explanation, explain it fully — then stop.",
}

// InjectPersonaTone appends a system block tagged [yesmem-tone] with a tone
// directive derived from the verbosity preference. No-op if verbosity is empty
// or unknown.
func InjectPersonaTone(req map[string]any, verbosity string) {
	tone, ok := personaTones[verbosity]
	if !ok {
		return
	}
	ReplaceSystemBlock(req, "yesmem-tone", tone)
}

// replaceSystemText finds old in all system blocks and replaces it with repl.
// Returns true if any block was modified.
func replaceSystemText(req map[string]any, old, repl string) bool {
	blocks := ensureSystemArray(req)
	modified := false
	for i, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := bm["text"].(string)
		if !strings.Contains(text, old) {
			continue
		}
		bm["text"] = strings.Replace(text, old, repl, 1)
		blocks[i] = bm
		modified = true
	}
	if modified {
		req["system"] = blocks
	}
	return modified
}

// RewriteGoldPlating replaces the anti-gold-plating directive to allow
// fixing adjacent issues discovered during investigation.
func RewriteGoldPlating(req map[string]any) bool {
	return replaceSystemText(req,
		"Don't add features, refactor, or introduce abstractions beyond what the task requires. A bug fix doesn't need surrounding cleanup; a one-shot operation doesn't need a helper.",
		"Don't add unrelated features or speculative improvements. However, if adjacent code is broken, fragile, or directly contributes to the problem being solved, fix it as part of the task. A bug fix should address related issues discovered during investigation.",
	)
}

// RewriteErrorHandling replaces the error handling cap to encourage
// validation at real system boundaries.
func RewriteErrorHandling(req map[string]any) bool {
	return replaceSystemText(req,
		"Don't add error handling, fallbacks, or validation for scenarios that can't happen. Trust internal code and framework guarantees. Only validate at system boundaries (user input, external APIs).",
		"Add error handling and validation at real boundaries where failures can realistically occur (user input, external APIs, I/O, network). Trust internal code and framework guarantees for truly internal paths.",
	)
}

// RewriteThreeLinesRule replaces the rigid three-lines rule with
// judgment-based extraction guidance.
func RewriteThreeLinesRule(req map[string]any) bool {
	return replaceSystemText(req,
		"Three similar lines is better than a premature abstraction.",
		"Use judgment about when to extract shared logic. Avoid premature abstractions for hypothetical reuse, but do extract when duplication causes real maintenance risk.",
	)
}

// RewriteSubagentCompleteness replaces the subagent gold-plating cap
// with a senior-developer thoroughness standard.
func RewriteSubagentCompleteness(req map[string]any) bool {
	return replaceSystemText(req,
		"Complete the task fully\u2014don't gold-plate, but don't leave it half-done.",
		"Complete the task fully and thoroughly. Do the work that a careful senior developer would do, including edge cases and fixing obviously related issues you discover. Don't add purely cosmetic or speculative improvements unrelated to the task.",
	)
}

// RewriteExploreAgentSpeed replaces the explore agent's speed-over-thoroughness
// bias with a thoroughness-first directive.
func RewriteExploreAgentSpeed(req map[string]any) bool {
	return replaceSystemText(req,
		"NOTE: You are meant to be a fast agent that returns output as quickly as possible. In order to achieve this you must:",
		"NOTE: Be thorough in your exploration. Use efficient search strategies but do not sacrifice completeness for speed:",
	)
}

// RewriteSubagentCodeSuppression replaces the subagent code snippet
// suppression with a more permissive context-sharing directive.
func RewriteSubagentCodeSuppression(req map[string]any) bool {
	return replaceSystemText(req,
		"Include code snippets only when the exact text is load-bearing (e.g., a bug you found, a function signature the caller asked for) \u2014 do not recap code you merely read.",
		"Include code snippets when they provide useful context (e.g., bugs found, function signatures, relevant patterns, code that informs the decision). Summarize rather than quoting large blocks verbatim.",
	)
}

// RewriteScopeMatching replaces the strict scope-matching directive
// to allow fixing closely related issues discovered during work.
func RewriteScopeMatching(req map[string]any) bool {
	return replaceSystemText(req,
		"Match the scope of your actions to what was actually requested.",
		"Match the scope of your actions to what was actually requested, but do address closely related issues you discover during the work when fixing them is clearly the right thing to do.",
	)
}
