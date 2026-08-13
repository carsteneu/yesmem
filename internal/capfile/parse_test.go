package capfile

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse_SingleScriptCap_Defaults(t *testing.T) {
	src := `---
name: hello
description: "Test cap"
---

## Purpose
A minimal hello cap.

## Scripts

### hello
kind: tool

` + "```javascript\nasync ({ name }) => ({ greeting: 'Hello ' + name })\n```" + `
`

	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Name != "hello" {
		t.Errorf("Name = %q, want hello", cf.Name)
	}
	if len(cf.Scripts) != 1 {
		t.Fatalf("len(Scripts) = %d, want 1", len(cf.Scripts))
	}
	sc := cf.Scripts[0]
	if sc.Name != "hello" {
		t.Errorf("Script[0].Name = %q, want hello", sc.Name)
	}
	if sc.Kind != "tool" {
		t.Errorf("Script[0].Kind = %q, want tool", sc.Kind)
	}
	if sc.Runtime != "repl" {
		t.Errorf("Script[0].Runtime = %q, want repl", sc.Runtime)
	}
	if sc.Lang != "javascript" {
		t.Errorf("Script[0].Lang = %q, want javascript", sc.Lang)
	}
	if !strings.Contains(sc.Body, "Hello") {
		t.Errorf("Script[0].Body missing JS body: %q", sc.Body)
	}
	if sc.Schema == "" || sc.Schema == "{}" {
		t.Errorf("Script[0].Schema not derived from signature: %q", sc.Schema)
	}
}

func TestParse_BundleCap_MultipleScripts(t *testing.T) {
	src := `---
name: telegram
description: "Telegram bot bundle"
---

## Purpose
Bidirectional Telegram messaging.

## Scripts

### telegram_send
kind: tool
schema: {"type":"object","properties":{"text":{"type":"string"}}}

` + "```bash\ncurl -s -X POST $URL -d \"text=$TEXT\"\n```" + `

### telegram_poll
kind: handler

` + "```bash\nyesmem cap-store telegram upsert config '{\"id\":\"x\"}'\n```" + `

### telegram_reply
kind: handler

` + "```bash\necho replying\n```" + `
`

	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cf.Scripts) != 3 {
		t.Fatalf("len(Scripts) = %d, want 3", len(cf.Scripts))
	}
	want := []struct {
		name, kind, runtime string
	}{
		{"telegram_send", "tool", "bash"},
		{"telegram_poll", "handler", "bash"},
		{"telegram_reply", "handler", "bash"},
	}
	for i, w := range want {
		sc := cf.Scripts[i]
		if sc.Name != w.name || sc.Kind != w.kind || sc.Runtime != w.runtime {
			t.Errorf("Script[%d] = (%q, %q, %q), want (%q, %q, %q)", i, sc.Name, sc.Kind, sc.Runtime, w.name, w.kind, w.runtime)
		}
	}
	if len(cf.ToolScripts()) != 1 {
		t.Errorf("ToolScripts() len = %d, want 1", len(cf.ToolScripts()))
	}
	if len(cf.HandlerScripts()) != 2 {
		t.Errorf("HandlerScripts() len = %d, want 2", len(cf.HandlerScripts()))
	}
}

func TestParse_InlineMetadata_KindDefaultsToTool(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync ({ a }) => ({})\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Scripts[0].Kind != "tool" {
		t.Errorf("Kind = %q, want tool (default)", cf.Scripts[0].Kind)
	}
}

func TestParse_RuntimeDerivedFromCodeFence(t *testing.T) {
	cases := []struct {
		fence, wantRuntime string
	}{
		{"javascript", "repl"},
		{"js", "repl"},
		{"bash", "bash"},
		{"sh", "bash"},
		{"shell", "bash"},
	}
	for _, tc := range cases {
		t.Run(tc.fence, func(t *testing.T) {
			body := "echo hi"
			if tc.wantRuntime == "repl" {
				body = "async ({ a }) => ({})"
			}
			schemaLine := ""
			if tc.wantRuntime == "bash" {
				schemaLine = "schema: {\"type\":\"object\"}\n"
			}
			src := "---\nname: x\ndescription: \"x\"\n---\n\n## Purpose\nx\n\n## Scripts\n\n### x\nkind: tool\n" + schemaLine + "\n```" + tc.fence + "\n" + body + "\n```\n"
			cf, err := Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse(%s): %v", tc.fence, err)
			}
			if cf.Scripts[0].Runtime != tc.wantRuntime {
				t.Errorf("Runtime = %q, want %q", cf.Scripts[0].Runtime, tc.wantRuntime)
			}
		})
	}
}

// Explicit per-script timeout: seconds must be parsed into Script.Timeout so the
// daemon grant the script its own execution budget (instead of the 30s default
// that kills slow first-time image generations).
func TestParse_InlineMetadata_Timeout(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### gen
kind: tool
timeout: 180
schema: {"type":"object"}
` + "```bash\ncurl --max-time 180 ...\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Scripts[0].Timeout != 180 {
		t.Errorf("Timeout = %d, want 180", cf.Scripts[0].Timeout)
	}
}

// A non-numeric timeout must be rejected, not silently ignored.
func TestParse_InlineMetadata_InvalidTimeout(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### gen
kind: tool
timeout: high
schema: {"type":"object"}
` + "```bash\necho hi\n```" + `
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatalf("expected error for invalid timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
}

func TestParse_DerivedSchemaFromJSSignature(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync ({ subreddit, topic, limit = 25 }) => ({})\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	schema := cf.Scripts[0].Schema
	for _, want := range []string{"subreddit", "topic", "limit"} {
		if !strings.Contains(schema, want) {
			t.Errorf("Schema missing %q: %s", want, schema)
		}
	}
}

func TestParse_RequiresAggregatesAcrossScripts(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### a

` + "```javascript\nasync () => { await store('x') }\n```" + `

### b

` + "```javascript\nasync () => { await web('x') }\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := map[string]bool{}
	for _, r := range cf.Requires {
		got[r] = true
	}
	if !got["store"] || !got["web"] {
		t.Errorf("Requires = %v, want store + web aggregated", cf.Requires)
	}
}

func TestParse_ExplicitRequiresOverridesDetection(t *testing.T) {
	src := `---
name: x
description: "x"
requires: [store, fetch]
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync () => ({})\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"store", "fetch"}
	if !reflect.DeepEqual(cf.Requires, want) {
		t.Errorf("Requires = %v, want %v", cf.Requires, want)
	}
}

// --- Validation errors ---

func TestParse_Error_MissingScriptsSection(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "Scripts section missing") {
		t.Errorf("expected Scripts-missing error, got %v", err)
	}
}

func TestParse_Error_EmptyScripts(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Errorf("expected empty-Scripts error, got %v", err)
	}
}

func TestParse_Error_DuplicateScriptName(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo

` + "```javascript\nasync () => ({})\n```" + `

### foo

` + "```javascript\nasync () => ({})\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-script error, got %v", err)
	}
}

func TestParse_Error_MissingCodeFence(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
kind: tool

just prose, no fence
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "missing code fence") {
		t.Errorf("expected missing-fence error, got %v", err)
	}
}

func TestParse_Error_MultipleCodeFences(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo

` + "```javascript\nasync () => ({})\n```" + `

` + "```javascript\nasync () => ({})\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "multiple code fences") {
		t.Errorf("expected multiple-fences error, got %v", err)
	}
}

func TestParse_Error_UnknownKind(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
kind: weird

` + "```javascript\nasync () => ({})\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("expected invalid-kind error, got %v", err)
	}
}

func TestParse_Error_UnknownRuntime(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
runtime: cobol

` + "```javascript\nasync () => ({})\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "invalid runtime") {
		t.Errorf("expected invalid-runtime error, got %v", err)
	}
}

func TestParse_Error_UnknownMetadataKey(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
mood: cheerful

` + "```javascript\nasync () => ({})\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "unknown metadata key") {
		t.Errorf("expected unknown-metadata-key error, got %v", err)
	}
}

func TestParse_Error_ToolBashWithoutSchema(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
kind: tool

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "requires explicit schema") {
		t.Errorf("expected bash-tool-no-schema error, got %v", err)
	}
}

func TestParse_Error_HandlerWithSchema(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
kind: handler
schema: {"type":"object"}

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "handlers cannot have a schema") {
		t.Errorf("expected handler-schema error, got %v", err)
	}
}

func TestParse_Error_RuntimeMismatch(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### foo
runtime: repl

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "runtime mismatch") {
		t.Errorf("expected runtime-mismatch error, got %v", err)
	}
}

func TestParse_Frontmatter_Validation(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			"missing name",
			"---\ndescription: \"x\"\n---\n\n## Purpose\nx\n\n## Scripts\n\n### x\n\n```javascript\nasync () => ({})\n```\n",
			"name is required",
		},
		{
			"missing description",
			"---\nname: x\n---\n\n## Purpose\nx\n\n## Scripts\n\n### x\n\n```javascript\nasync () => ({})\n```\n",
			"description is required",
		},
		{
			"invalid name format",
			"---\nname: \"Bad-Name\"\ndescription: \"x\"\n---\n\n## Purpose\nx\n\n## Scripts\n\n### x\n\n```javascript\nasync () => ({})\n```\n",
			"must match",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.src)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestParse_StatelessCap_NoDatabase(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync () => ({})\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.DatabaseSQL != "" {
		t.Errorf("DatabaseSQL = %q, want empty for stateless cap", cf.DatabaseSQL)
	}
}

func TestParse_DatabaseSQL_Validation(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync () => ({})\n```" + `

## Database

` + "```sql\nDROP TABLE bad;\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "dangerous SQL") {
		t.Errorf("expected dangerous-SQL error, got %v", err)
	}
}

func TestParse_Actions(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x

` + "```javascript\nasync () => ({})\n```" + `

## Actions

### Setup

Set the API token via store("config", {token: "..."})

### Teardown

Delete the config row.
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Actions["setup"] == "" {
		t.Errorf("Actions[setup] = empty")
	}
	if cf.Actions["teardown"] == "" {
		t.Errorf("Actions[teardown] = empty")
	}
}

// --- sandbox field ---

func TestParse_ScriptSandboxValidValues(t *testing.T) {
	for _, value := range []string{"none", "standard", "strict"} {
		t.Run(value, func(t *testing.T) {
			src := "---\nname: x\ndescription: \"x\"\n---\n\n## Purpose\nx\n\n## Scripts\n\n### x\nkind: handler\nsandbox: " + value + "\n\n" + "```bash\necho hi\n```" + "\n"
			cf, err := Parse([]byte(src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cf.Scripts[0].Sandbox != value {
				t.Errorf("Sandbox = %q, want %q", cf.Scripts[0].Sandbox, value)
			}
		})
	}
}

func TestParse_ScriptSandboxInvalid(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x
kind: handler
sandbox: yolo

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("expected sandbox-invalid error, got %v", err)
	}
}

func TestParse_ScriptSandboxOmittedIsEmpty(t *testing.T) {
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x
kind: handler

` + "```bash\necho hi\n```" + `
`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Scripts[0].Sandbox != "" {
		t.Errorf("Sandbox = %q, want empty string", cf.Scripts[0].Sandbox)
	}
}

// --- scope=project + sandbox=none guard ---

func TestParse_ProjectScopeRejectsSandboxNone(t *testing.T) {
	src := `---
name: x
description: "x"
scope: project
---

## Purpose
x

## Scripts

### x
kind: handler
sandbox: none

` + "```bash\necho hi\n```" + `
`
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for scope=project + sandbox=none, got nil")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Errorf("error message missing 'scope': %v", err)
	}
	if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error message missing 'sandbox': %v", err)
	}
}

func TestParse_UserScopeAllowsSandboxNone(t *testing.T) {
	src := `---
name: x
description: "x"
scope: user
---

## Purpose
x

## Scripts

### x
kind: handler
sandbox: none

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("expected no error for scope=user + sandbox=none, got %v", err)
	}
}

func TestParse_NoScopeAllowsSandboxNone(t *testing.T) {
	// No scope in frontmatter — historically defaults to "project" internally,
	// but the T4 guard applies only when scope is explicitly "project".
	// The test documents the expected behaviour: omitting scope is allowed.
	// NOTE: parse.go currently defaults empty scope to "project", which means
	// this test will pass only if the guard is restricted to explicit "project".
	// If the guard fires on the default, update this test and the guard together.
	src := `---
name: x
description: "x"
---

## Purpose
x

## Scripts

### x
kind: handler
sandbox: none

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("expected no error for omitted scope + sandbox=none, got %v", err)
	}
}

func TestParse_ProjectScopeAllowsSandboxStandard(t *testing.T) {
	src := `---
name: x
description: "x"
scope: project
---

## Purpose
x

## Scripts

### x
kind: handler
sandbox: standard

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("expected no error for scope=project + sandbox=standard, got %v", err)
	}
}

func TestParse_ProjectScopeAllowsSandboxOmitted(t *testing.T) {
	src := `---
name: x
description: "x"
scope: project
---

## Purpose
x

## Scripts

### x
kind: handler

` + "```bash\necho hi\n```" + `
`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("expected no error for scope=project + sandbox omitted, got %v", err)
	}
}

func TestFindScript(t *testing.T) {
	cf := &CapFile{Scripts: []Script{{Name: "a"}, {Name: "b"}}}
	if got := cf.FindScript("a"); got == nil || got.Name != "a" {
		t.Errorf("FindScript(a) = %+v, want script a", got)
	}
	if got := cf.FindScript("missing"); got != nil {
		t.Errorf("FindScript(missing) = %+v, want nil", got)
	}
}
