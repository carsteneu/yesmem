// Package guard is the rule guard's judgement core: one tool call in, one decision out.
//
// It owns the model resolution, the request format and the policy narrowing, and nothing else: no stdin, no
// terminal output, no CLI wiring. Two callers use it: the Claude Code hook (internal/hooks) and the daemon
// RPC guard_check, which the opencode plugin dials. Both therefore judge with the same model, the same
// prompt and the same rules.
package guard

import (
	"encoding/json"
	"strings"

	"github.com/carsteneu/yesmem/internal/guardmodel"
)

// Decision is the judgement. The field names are the wire format of both callers: the hook's
// hookSpecificOutput and the plugin's tool result are built from them.
type Decision struct {
	Decision   string   `json:"decision"` // BLOCK, SUGGEST, PASS, UNAVAILABLE
	Violations []string `json:"violations,omitempty"`
	Suggestion string   `json:"suggestion,omitempty"`
	Detail     string   `json:"detail,omitempty"`
	// Enforceable says whether this tool may be refused at all. A BLOCK for a tool the guard may not enforce
	// carries false, and the caller renders a check instead of a rejection.
	Enforceable bool `json:"enforceable,omitempty"`
	// Model names the model that judged, so a caller can log the change instead of inferring it.
	Model string `json:"model,omitempty"`
	// Truncated says the harness's context was cut at the prompt boundary.
	Truncated bool `json:"truncated,omitempty"`
}

// Request is one tool call to judge. The shape is the orchestrator's contract for both harnesses: the hook
// reads it from Claude Code's PreToolUse payload, the RPC reads it from the plugin's call.
type Request struct {
	// Tool is the harness's own name: Bash/REPL/Edit/Write from Claude Code, shell/write/edit/patch from
	// opencode. The core maps both vocabularies onto the one block policy.
	Tool string `json:"tool"`
	// ToolInput is the call's arguments exactly as the harness holds them.
	ToolInput json.RawMessage `json:"tool_input"`
	// CWD is the project directory the call runs in.
	CWD string `json:"cwd,omitempty"`
	// SessionID names the calling session, for the log line of the caller.
	SessionID string `json:"session_id,omitempty"`
	// Context is the harness's addendum: recent conversation, a branch note, a test-file note.
	Context string `json:"context,omitempty"`
	// Rules is the RULES.md content, already filtered by the caller: the catalogue and the Decision Policy.
	Rules string `json:"-"`
	// DataDir is the installation directory (~/.claude/yesmem). Empty means the default installation.
	DataDir string `json:"-"`
	// Purpose selects the prompt template: the guard judges a call, the nudge suggests a skill. One RPC, one
	// model chain, one cache for both.
	Purpose string `json:"purpose,omitempty"`
}

// PurposeGuard is the default purpose: judge a proposed tool call against the rules.
const PurposeGuard = "guard"

// Check answers one question of the requested purpose. It always returns a decision; an unanswered call is
// PASS, never an error the caller has to translate: the guard stays out of the way when it cannot judge.
func Check(req Request) Decision {
	// One RPC, two prompt templates: the nudge that suggests a skill is a different question to the same
	// model. A purpose this core does not know is answered as not implemented, never judged with the wrong
	// prompt.
	switch req.Purpose {
	case "", PurposeGuard:
		return checkGuard(req)
	case PurposeNudge:
		return checkNudge(req)
	}
	return Decision{Decision: "PASS", Detail: "purpose " + req.Purpose + " is not implemented"}
}

// checkGuard judges one tool call against the rules.
func checkGuard(req Request) Decision {
	tool := canonicalTool(req.Tool)
	// The deterministic half runs first and reads no rule text. The hard pre-check bypasses the model entirely
	// and needs no credential: these patterns describe commands no plausible workflow needs (rm -rf /,
	// force-push to main, DROP TABLE), so a false positive is cheaper than a missed block. It sits ahead of the
	// rules check because it does not depend on them — an installation whose RULES.md is missing must still not
	// delete.
	if shellTools[tool] {
		if name := matchDestructivePattern(describe(req)); name != "" {
			return Decision{
				Decision:    "BLOCK",
				Violations:  []string{"destructive pattern: " + name},
				Enforceable: true,
			}
		}
		// A deletion is judged here and nowhere else. The gate is final on purpose: an approved deletion must
		// not be overturned by a model that reads rule 25 as destructive and cannot see the approval, so the
		// model is not asked.
		if decision, judged := checkDeletion(req); judged {
			return decision
		}
	}
	rules := PromptRules(req.Rules)
	if strings.TrimSpace(rules) == "" {
		return Decision{Decision: "PASS", Detail: "no rules to judge against"}
	}
	resolution, err := resolve(req.DataDir)
	if err != nil || resolution.APIKey == "" {
		return Decision{Decision: "PASS", Detail: "no guard credential"}
	}
	// The harness's addendum is untrusted and bounded: it must not be able to inflate the prompt.
	context, truncated := truncateContext(req.Context)
	req.Context = context
	// The verdict names the model that answered, which is the fallback one when it stepped down.
	decision := judge(resolution, req, rules)
	// A citation the prompt does not carry is a model invention and may not reject the call.
	decision = downgradeUnknownCitation(decision, rules)
	decision.Truncated = truncated
	// The block set is enforced here, once, for both harnesses: a BLOCK that names a rule the Decision Policy
	// does not class as blockable is a check, not a refusal.
	blockable := blockableRuleNumbers(installedRules(req))
	decision = downgradeUnauthorizedBlock(decision, tool, blockable)
	if decision.Decision == "BLOCK" {
		decision.Enforceable = canBlockTools[tool] || (shellTools[tool] && citesPolicyBlockRule(decision.Violations, blockable))
	}
	return decision
}

// installedRules is the installation's own rules text, the source of the enforceable set. It is read apart
// from req.Rules on purpose: the prompt may carry the project's catalogue, the enforcement may not.
func installedRules(req Request) string {
	return LoadInstalledRules(req.DataDir)
}

// canonicalTool maps a harness's tool name onto the guard's own vocabulary. The block policy is written once,
// in these names.
func canonicalTool(name string) string {
	switch strings.ToLower(name) {
	case "shell", "bash":
		return "Bash"
	case "repl":
		return "REPL"
	case "edit":
		return "Edit"
	case "write", "patch":
		// patch mutates a file exactly as a write does, and the policy treats it the same way.
		return "Write"
	}
	return name
}

// describe turns a tool input into the line the model reads. Claude Code names the command or the file path in
// a field; opencode hands a JSON object, which is capped so a long description cannot push the deciding field
// out of the excerpt.
func describe(req Request) string {
	tool := canonicalTool(req.Tool)
	input := req.ToolInput
	switch tool {
	case "Bash":
		var bash struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &bash) == nil && bash.Command != "" {
			// The whole command is what rule 25 turns on, and a tail must be visible. Bounded anyway: a
			// pathological command would otherwise push the prompt past what the endpoint accepts, and a
			// rejected request refuses writes for the refusal lock's lifetime.
			return "Bash: " + capText(bash.Command, maxCommandChars)
		}
	case "REPL":
		var repl struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(input, &repl) == nil && repl.Code != "" {
			return "REPL: " + repl.Code
		}
	case "Edit", "Write":
		var file struct {
			FilePath  string `json:"file_path"`
			FilePathC string `json:"filePath"`
			Path      string `json:"path"`
		}
		if json.Unmarshal(input, &file) == nil {
			if path := firstNonEmpty(file.FilePath, file.FilePathC, file.Path); path != "" {
				// The path names the file, the arguments say what is written. A write is enforced on its
				// content alone, and the content is where a secret (rule 2) or an LLM signature (rule 1)
				// sits; the path alone would leave both unreachable.
				return tool + ": " + path + " " + excerpt(string(input))
			}
		}
	}
	return req.Tool + ": " + excerpt(string(input))
}

// excerpt bounds the argument text that reaches the prompt. The command and the working directory carry a
// shell decision, so they are written first: marshalling a map would sort the keys and a long description
// would push the command past the cap.
func excerpt(args string) string {
	var fields map[string]any
	if json.Unmarshal([]byte(args), &fields) != nil {
		if len(args) > maxArgChars {
			return args[:maxArgChars]
		}
		return args
	}
	head := make([]string, 0, 2)
	for _, key := range []string{"command", "workdir"} {
		if value, ok := fields[key]; ok {
			encoded, _ := json.Marshal(value)
			head = append(head, `"`+key+`":`+string(encoded))
			delete(fields, key)
		}
	}
	parts := head
	if len(fields) > 0 {
		if encoded, err := json.Marshal(fields); err == nil {
			if rest := strings.TrimSuffix(strings.TrimPrefix(string(encoded), "{"), "}"); rest != "" {
				parts = append(parts, rest)
			}
		}
	}
	ordered := "{" + strings.Join(parts, ",") + "}"
	if len(ordered) > maxArgChars {
		return ordered[:maxArgChars]
	}
	return ordered
}

// maxArgChars is the argument cap the prompt has always used.
const maxArgChars = 300

// maxCommandChars bounds the described command. Far above any real command line, far below a prompt the
// endpoint would refuse.
const maxCommandChars = 4000

func capText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// resolve finds the credential and the model for one call through guardmodel, the same resolution setup
// reads: one source for the guard and for the installation report.
func resolve(dataDir string) (*guardmodel.Resolution, error) {
	home, err := homeDir()
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		dataDir = defaultDataDir(home)
	}
	return guardmodel.Resolve(home, dataDir)
}

// Resolve exposes the core's own resolution. The installation report reads it so that the model it names is
// the model the guard asks, not a second derivation that can drift from it.
func Resolve(dataDir string) (*guardmodel.Resolution, error) {
	return resolve(dataDir)
}
