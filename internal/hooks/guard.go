package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/guard"
	"github.com/carsteneu/yesmem/internal/setup"
)

// suggestCooldownTTL is the minimum time between identical SUGGESTs for the
// same skill. Repeats within this window are silenced to keep system-reminder
// noise tolerable across long sessions.
const suggestCooldownTTL = 10 * time.Minute

// GuardDecision is the core's decision: the hook only formats it for Claude Code.
type GuardDecision = guard.Decision

// formatGuardOutput converts a GuardDecision into the Claude Code PreToolUse
// hook output schema so SUGGEST/BLOCK reach the model as system-reminders.
// PASS and ill-formed decisions return "" (silent — no stdout, exit 0),
// which Claude Code treats as "no decision; normal permission flow applies".
func formatGuardOutput(d GuardDecision) string {
	switch d.Decision {
	case "SUGGEST":
		if d.Suggestion == "" {
			return ""
		}
		out := map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     "PreToolUse",
				"additionalContext": guard.SanitizeMessage(d.Suggestion),
			},
		}
		b, _ := json.Marshal(out)
		return string(b)
	case "BLOCK":
		// The violations are model text, and they reach the agent's context: one line, capped.
		reason := guard.SanitizeMessage(strings.Join(d.Violations, "; "))
		if reason == "" {
			reason = "blocked by RULES.md"
		}
		out := map[string]any{
			// Legacy decision field retained for older Claude Code versions;
			// modern versions prefer hookSpecificOutput.permissionDecision.
			"decision": "block",
			"reason":   reason,
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
				"additionalContext":        reason,
			},
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	return ""
}

// RunGuard reads PreToolUse JSON from stdin, judges the call through the guard core, and outputs the decision
// in Claude Code's hook format. Always exits 0: BLOCK is signaled via hookSpecificOutput.permissionDecision
// ("deny"), SUGGEST via additionalContext, PASS via silence (no stdout). An exit 2 would make Claude Code
// ignore the JSON entirely.
//
// Everything about the judgement lives in internal/guard, which the daemon's guard_check RPC serves to the
// opencode plugin: one model, one prompt, one policy for both harnesses.
func RunGuard(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook HookInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	// Only evaluate Bash, REPL, Edit, Write
	switch hook.ToolName {
	case "Bash", "REPL", "Edit", "Write":
		// continue
	default:
		return
	}

	// Load RULES.md. An empty result is no reason to skip the judgement: the deterministic half of the guard
	// (destructive patterns, the deletion gate) needs no rule text, and Check answers PASS with a reason when
	// there is nothing to judge against.
	rules := guard.LoadRules(dataDir, hook.CWD)

	// Filter out catalog entries for skills that aren't loadable on this
	// machine (ghost dirs without SKILL.md). Prevents false-positive
	// SUGGESTs naming skills the agent cannot activate. No-op if the
	// filesystem scan fails — pass through conservatively.
	rules = setup.FilterStaleSkillRules(rules, setup.LoadAgentLoadableSkillNames())

	decision := guard.Check(guard.Request{
		Tool:      hook.ToolName,
		ToolInput: hook.ToolInput,
		CWD:       hook.CWD,
		SessionID: hook.SessionID,
		Rules:     rules,
		DataDir:   dataDir,
	})

	cooldownPath := filepath.Join(dataDir, "guard_state.db")

	// Drop suggestions for skills that aren't loadable on this machine
	// (ghost dirs without SKILL.md). Runs before the cooldown check so an
	// unavailable suggestion doesn't get recorded as fired. Conservative:
	// empty available set (filesystem scan failed) = pass through.
	decision = maybeSuppressUnavailableSkill(decision, setup.LoadAgentLoadableSkillNames())

	if maybeSuppressSuggestion(decision, cooldownPath, suggestCooldownTTL, time.Now()) {
		return
	}

	if hookOut := formatGuardOutput(decision); hookOut != "" {
		fmt.Println(hookOut)
	}
}
