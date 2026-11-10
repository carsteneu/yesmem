package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

// los68HookOutput is the PreToolUse hook schema the guard prints.
type los68HookOutput struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// TestLos68RunGuardShellPolicyBlockEmitsDeny drives the whole hook, not just the
// composition: RunGuard must turn a policy citation on Bash into the deny JSON
// the host reads. Without this, a post-processor placed between the gate and the
// output could swallow the deny while every unit test stayed green.
func TestLos68RunGuardShellPolicyBlockEmitsDeny(t *testing.T) {
	server := guardDecisionServer(t, `{"decision":"BLOCK","violations":["Rule 32: gh pr merge merges a pull request"]}`)
	defer server.Close()
	dataDir := guardEnv(t, server.URL)
	out := runGuardCapture(t, dataDir,
		`{"tool_name":"Bash","tool_input":{"command":"gh pr merge 1"}}`)
	var parsed los68HookOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%q)", err, out)
	}
	if parsed.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("expected permissionDecision deny, got %q (out=%q)", parsed.HookSpecificOutput.PermissionDecision, out)
	}
	if !strings.Contains(parsed.HookSpecificOutput.PermissionDecisionReason, "Rule 32") {
		t.Errorf("expected the citation in the deny reason, got %q", parsed.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestLos68RunGuardShellAdvisoryBlockStaysSuggest is the same run for the
// downgraded half: an advisory citation must reach the model as a notice and
// must not deny.
func TestLos68RunGuardShellAdvisoryBlockStaysSuggest(t *testing.T) {
	server := guardDecisionServer(t, `{"decision":"BLOCK","violations":["Rule 14: verify before overwriting"]}`)
	defer server.Close()
	dataDir := guardEnv(t, server.URL)
	out := runGuardCapture(t, dataDir,
		`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	if strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Fatalf("an advisory citation must not deny, got %q", out)
	}
	var parsed los68HookOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid json: %v (out=%q)", err, out)
	}
	if !strings.Contains(parsed.HookSpecificOutput.AdditionalContext, "Rule 14") {
		t.Errorf("expected the citation in additionalContext, got %q", parsed.HookSpecificOutput.AdditionalContext)
	}
}

