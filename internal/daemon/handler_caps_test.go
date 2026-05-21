package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func seedCap(t *testing.T, h *Handler, name, project, handlerBash string, tags []string) int64 {
	return seedCapFull(t, h, name, project, handlerBash, "", "", tags)
}

func seedCapFull(t *testing.T, h *Handler, name, project, handlerBash, handlerREPL, schema string, tags []string) int64 {
	t.Helper()
	meta := CapMeta{
		Name:        name,
		Description: "Test: " + name,
		HandlerBash: handlerBash,
		HandlerREPL: handlerREPL,
		Schema:      schema,
		Tags:        tags,
		Version:     1,
		Tested:      true,
	}
	ctx, err := meta.ToJSON()
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	l := &models.Learning{
		Content:     name + " — Test: " + name,
		Category:    "cap",
		Source:      "user_stated",
		Project:     project,
		Context:     ctx,
		Keywords:    tags,
		TriggerRule: "cap:" + name,
	}
	id, err := h.store.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert cap: %v", err)
	}
	return id
}

func unmarshalCaps(t *testing.T, resp Response) []capResult {
	t.Helper()
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var caps []capResult
	if err := json.Unmarshal(resp.Result, &caps); err != nil {
		t.Fatalf("unmarshal caps: %v", err)
	}
	return caps
}

func unmarshalMap(t *testing.T, resp Response) map[string]any {
	t.Helper()
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Result, &m); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	return m
}

// --- handleGetCaps tests ---

func TestHandleGetCapabilities_All(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "reddit_fetch", "global", "curl -s", []string{"web", "reddit"})
	seedCap(t, h, "hn_search", "global", "curl -s", []string{"web", "hn"})

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{}))
	if len(caps) != 2 {
		t.Errorf("expected 2 caps, got %d", len(caps))
	}
}

func TestHandleGetCapabilities_FilterByName(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "reddit_fetch", "global", "curl -s", nil)
	seedCap(t, h, "hn_search", "global", "curl -s", nil)

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "reddit_fetch"}))
	if len(caps) != 1 {
		t.Errorf("expected 1, got %d", len(caps))
	}
}

func TestHandleGetCapabilities_FilterByTag(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "reddit_fetch", "global", "curl -s", []string{"web", "reddit"})
	seedCap(t, h, "daemon_logs", "yesmem", "grep -a", []string{"debug"})

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"tag": "web"}))
	if len(caps) != 1 {
		t.Errorf("expected 1 with tag 'web', got %d", len(caps))
	}
}

func TestHandleGetCapabilities_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{}))
	if len(caps) != 0 {
		t.Errorf("expected 0, got %d", len(caps))
	}
}

func TestHandleGetCapabilities_SkipsMalformed(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "valid_tool", "global", "echo hi", nil)
	h.store.InsertLearning(&models.Learning{
		Content:  "broken",
		Category: "cap",
		Source:   "llm_extracted",
		Context:  "not json{{{",
	})

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{}))
	if len(caps) != 1 {
		t.Errorf("expected 1 valid (skip malformed), got %d", len(caps))
	}
}

// --- handleSaveCap tests ---

func TestHandleSaveCapability_EmbeddingTextIsCleanNotJSON(t *testing.T) {
	h, store := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "reddit_fetch",
		"description":  "Fetch Reddit posts from a subreddit",
		"handler_bash": "curl -s ${URL}",
		"schema":       `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		"tags":         []any{"web", "reddit"},
		"project":      "global",
	})
	if resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}

	learnings, err := store.GetActiveLearnings("cap", "global", "", "", 0)
	if err != nil {
		t.Fatalf("get learnings: %v", err)
	}
	if len(learnings) != 1 {
		t.Fatalf("expected 1 cap, got %d", len(learnings))
	}
	l := learnings[0]

	if l.EmbeddingText == "" {
		t.Fatal("EmbeddingText must not be empty for a cap")
	}
	// Must contain the human-readable parts.
	for _, need := range []string{"reddit_fetch", "Fetch Reddit posts", "web", "reddit"} {
		if !strings.Contains(l.EmbeddingText, need) {
			t.Errorf("EmbeddingText missing %q: %s", need, l.EmbeddingText)
		}
	}
	// Must NOT contain JSON structure from Context.
	for _, bad := range []string{`{`, `}`, `"type":`, `"properties":`, `"required":`, `handler_bash`, `curl -s`} {
		if strings.Contains(l.EmbeddingText, bad) {
			t.Errorf("EmbeddingText leaked JSON/handler %q: %s", bad, l.EmbeddingText)
		}
	}
}


func TestHandleSaveCapability_Success(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "reddit_fetch",
		"description":  "Fetch Reddit posts",
		"handler_bash": `curl -s -A "YesMem/1.0" "$URL.json"`,
		"tags":         []any{"web", "reddit"},
		"tested":       true,
		"project":      "global",
	})
	result := unmarshalMap(t, resp)
	if result["name"] != "reddit_fetch" {
		t.Errorf("expected name 'reddit_fetch', got %v", result["name"])
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "reddit_fetch"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1 saved, got %d", len(caps))
	}
	if caps[0].Meta.Name != "reddit_fetch" {
		t.Errorf("expected 'reddit_fetch', got %q", caps[0].Meta.Name)
	}
}

func TestHandleSaveCapability_MissingName(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{"handler_bash": "echo hi"})
	if resp.Error == "" {
		t.Fatal("expected error for missing name")
	}
}

func TestHandleSaveCapability_InvalidName(t *testing.T) {
	h, _ := mustHandler(t)
	invalid := []string{
		"My Tool",         // spaces
		"MyTool",          // uppercase
		"tool-name",       // dash
		"123tool",         // leading digit
		"tool;drop",       // semicolon (injection-like)
		"a" + strings.Repeat("b", 64), // over 64 chars
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			resp := h.handleSaveCap(map[string]any{
				"name":         name,
				"handler_bash": "echo hi",
			})
			if resp.Error == "" {
				t.Errorf("expected error for invalid name %q, got success", name)
			}
		})
	}
}

func TestHandleSaveCapability_MissingHandler(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":        "broken",
		"description": "No handler",
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing handler")
	}
}

func TestHandleSaveCapability_WithREPLHandler(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "repl_tool",
		"handler_repl": `async ({x}) => { return sh("echo " + x) }`,
		"tested":       true,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "repl_tool"}))
	if caps[0].Meta.HandlerREPL == "" {
		t.Error("expected non-empty HandlerREPL")
	}
}

func TestHandleSaveCapability_AutoSupersede(t *testing.T) {
	h, _ := mustHandler(t)
	h.handleSaveCap(map[string]any{
		"name": "evolving", "description": "V1",
		"handler_bash": "echo v1", "project": "global",
	})
	h.handleSaveCap(map[string]any{
		"name": "evolving", "description": "V2",
		"handler_bash": "echo v2", "project": "global",
	})

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "evolving"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1 active after supersede, got %d", len(caps))
	}
	if caps[0].Meta.Description != "V2" {
		t.Errorf("expected V2, got %q", caps[0].Meta.Description)
	}
}

func TestHandleSaveCapability_CommaSeparatedTags(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "csv_tags_tool",
		"description":  "CSV tags test",
		"handler_bash": "echo hi",
		"tags":         "web,reddit,fetch",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "csv_tags_tool"}))
	if len(caps[0].Meta.Tags) != 3 {
		t.Errorf("expected 3 tags from CSV, got %d: %v", len(caps[0].Meta.Tags), caps[0].Meta.Tags)
	}
}

// --- Dispatch tests ---

func TestHandle_GetCapabilities_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "test_tool", "global", "echo hi", nil)

	resp := h.Handle(Request{Method: "get_caps", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

func TestHandle_SaveCapability_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "save_cap", Params: map[string]any{
		"name":         "dispatch_test",
		"description":  "Test",
		"handler_bash": "echo dispatched",
	}})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

// --- Full round-trip test ---

func TestCapability_FullRoundTrip(t *testing.T) {
	h, _ := mustHandler(t)

	// 1. No caps at start
	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{}))
	if len(caps) != 0 {
		t.Fatalf("expected 0 at start, got %d", len(caps))
	}

	// 2. Save cap
	result := unmarshalMap(t, h.handleSaveCap(map[string]any{
		"name":         "reddit_fetch",
		"description":  "Fetch Reddit posts and comments",
		"handler_bash": `curl -s -A "YesMem/1.0" "$URL.json?limit=25" --max-time 15`,
		"handler_repl": `async ({url}) => { let raw = sh('curl -s "' + url + '.json"'); return JSON.parse(raw) }`,
		"schema":       `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		"tags":         []any{"web", "reddit", "fetch"},
		"tested":       true,
		"project":      "global",
	}))
	if result["version"].(float64) != 1 {
		t.Errorf("expected version 1, got %v", result["version"])
	}

	// 3. Retrieve by name
	caps = unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "reddit_fetch"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1, got %d", len(caps))
	}
	if caps[0].Meta.HandlerBash == "" || caps[0].Meta.HandlerREPL == "" {
		t.Error("expected both handlers")
	}

	// 4. Retrieve by tag
	caps = unmarshalCaps(t, h.handleGetCaps(map[string]any{"tag": "reddit"}))
	if len(caps) != 1 {
		t.Errorf("expected 1 with tag 'reddit', got %d", len(caps))
	}

	// 5. Update (auto-supersede)
	result2 := unmarshalMap(t, h.handleSaveCap(map[string]any{
		"name":         "reddit_fetch",
		"description":  "v2 — new API format",
		"handler_bash": `curl -s -A "YesMem/2.0" "$URL.json?limit=50"`,
		"tags":         []any{"web", "reddit"},
		"tested":       true,
		"project":      "global",
	}))
	if result2["version"].(float64) != 2 {
		t.Errorf("expected version 2, got %v", result2["version"])
	}

	// 6. Only v2 active
	caps = unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "reddit_fetch"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1 active after supersede, got %d", len(caps))
	}
	if caps[0].Meta.Description != "v2 — new API format" {
		t.Errorf("expected v2, got %q", caps[0].Meta.Description)
	}
}

// --- handleRegisterCaps tests ---

func TestHandleRegisterCapabilities_REPLHandler(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "reddit_fetch", "global",
		`curl -s "$URL.json"`,
		`async ({url}) => { return sh('curl -s "' + url + '.json"'); }`,
		`{"type":"object","properties":{"url":{"type":"string"}}}`,
		[]string{"web", "reddit"},
	)

	resp := h.handleRegisterCaps(map[string]any{})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	code, ok := result["code"].(string)
	if !ok || code == "" {
		t.Fatal("expected non-empty code field")
	}
	if !strings.Contains(code, `registerTool("reddit_fetch"`) {
		t.Errorf("expected registerTool call for reddit_fetch, got: %s", code)
	}
	if !strings.Contains(code, "async ({url})") {
		t.Errorf("expected REPL handler in code, got: %s", code)
	}
	count, ok := result["count"].(float64)
	if !ok || count != 1 {
		t.Errorf("expected count=1, got %v", result["count"])
	}
}

func TestHandleRegisterCapabilities_BashFallback(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "bash_tool", "global", `echo "hello"`, []string{"util"})

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)
	if !strings.Contains(code, `registerTool("bash_tool"`) {
		t.Errorf("expected registerTool for bash_tool, got: %s", code)
	}
	if !strings.Contains(code, `sh(`) {
		t.Errorf("expected sh() wrapper for bash handler, got: %s", code)
	}
}

func TestHandleRegisterCapabilities_Empty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)
	if code != "" {
		t.Errorf("expected empty code for no caps, got: %q", code)
	}
	if result["count"].(float64) != 0 {
		t.Errorf("expected count=0, got %v", result["count"])
	}
}

func TestHandleRegisterCapabilities_FilterByProject(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "proj_tool", "myproject", "echo hi", "async () => 'hi'", "", []string{"util"})
	seedCapFull(t, h, "other_tool", "other", "echo bye", "async () => 'bye'", "", []string{"util"})

	resp := h.handleRegisterCaps(map[string]any{"project": "myproject"})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)
	if !strings.Contains(code, "proj_tool") {
		t.Error("expected proj_tool in code")
	}
	if strings.Contains(code, "other_tool") {
		t.Error("should not contain other_tool when filtered by project")
	}
}

func TestHandleRegisterCapabilities_EscapesDescription(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "tricky", "global", "echo ok",
		`async () => "done"`,
		"", []string{"test"},
	)
	// Override the description to include quotes
	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "tricky"}))
	if len(caps) == 0 {
		t.Fatal("expected cap")
	}

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)
	// %q handles escaping — no unescaped quotes should break JS
	if !strings.Contains(code, `registerTool("tricky"`) {
		t.Errorf("expected registerTool call, got: %s", code)
	}
}

func TestHandleRegisterCapabilities_InvalidSchemaSkipped(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "bad_schema", "global", "echo ok",
		`async () => "ok"`,
		`not valid json{{{`,
		[]string{"test"},
	)

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)
	// Invalid schema should be omitted, not injected
	if strings.Contains(code, "not valid json") {
		t.Error("invalid schema should not appear in generated code")
	}
	if !strings.Contains(code, `registerTool("bad_schema"`) {
		t.Error("tool should still be registered even with bad schema")
	}
}

func TestHandle_RegisterCapabilities_Dispatch(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "register_caps", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

// TestHandleRegisterCapabilities_FourPositionalArgs verifies the generator
// emits registerTool(name, desc, schema, handler) with 4 positional args,
// NOT the broken object-form registerTool(name, {description, params, fn}).
// See Learning #53284 (2026-04-17): object-form leaves schema+handler undefined,
// tool is non-callable in Claude Code REPL.
func TestHandleRegisterCapabilities_FourPositionalArgs(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "positional_test", "global", "echo ok",
		`async () => "result"`,
		`{"type":"object","properties":{"url":{"type":"string"}}}`,
		[]string{"test"},
	)

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)

	if strings.Contains(code, "description:") {
		t.Errorf("object-form 'description:' key detected — desc must be 2nd positional arg. Got:\n%s", code)
	}
	if strings.Contains(code, "fn:") {
		t.Errorf("object-form 'fn:' key detected — handler must be 4th positional arg. Got:\n%s", code)
	}
	if strings.Contains(code, "params:") {
		t.Errorf("object-form 'params:' key detected — schema must be 3rd positional arg. Got:\n%s", code)
	}
	if !strings.Contains(code, `registerTool("positional_test", "`) {
		t.Errorf("expected registerTool(\"positional_test\", \"...\" (desc as 2nd positional arg). Got:\n%s", code)
	}
}

// --- activate_cap ---

func TestHandleActivate_NewCap_ReturnsCode(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "git_log", "", "git log --oneline", nil)

	resp := h.handleActivateCap(map[string]any{
		"name":      "git_log",
		"thread_id": "thread-1",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	m := unmarshalMap(t, resp)
	code, _ := m["code"].(string)
	if !strings.Contains(code, "registerTool(") {
		t.Errorf("expected registerTool( in code, got: %q", code)
	}
	if !strings.Contains(code, `"git_log"`) {
		t.Errorf("expected \"git_log\" literal in code, got: %q", code)
	}
}

func TestHandleActivate_PersistsInStore(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo 1", nil)

	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})

	caps, err := h.store.GetSessionCaps("t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(caps) != 1 || caps[0].CapName != "c1" {
		t.Errorf("expected c1 activated for thread t, got %+v", caps)
	}
}

func TestHandleActivate_Idempotent(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo 1", nil)

	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})
	resp := h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})
	if resp.Error != "" {
		t.Errorf("second activate should succeed, got error: %s", resp.Error)
	}
	caps, _ := h.store.GetSessionCaps("t")
	if len(caps) != 1 {
		t.Errorf("expected 1 cap after duplicate activate, got %d", len(caps))
	}
}

func TestHandleActivate_NonexistentCap_Errors(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleActivateCap(map[string]any{
		"name":      "does_not_exist",
		"thread_id": "t",
	})
	if resp.Error == "" {
		t.Errorf("expected error for nonexistent cap, got result: %s", string(resp.Result))
	}
}

func TestHandleActivate_MissingName(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleActivateCap(map[string]any{"thread_id": "t"})
	if resp.Error == "" {
		t.Errorf("expected error when name missing")
	}
}

func TestHandleActivate_MissingThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo 1", nil)
	resp := h.handleActivateCap(map[string]any{"name": "c1"})
	if resp.Error == "" {
		t.Errorf("expected error when thread_id missing")
	}
}

func TestHandleActivate_ProjectScope_MatchingProject(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "project_cap", "projA", "echo", nil)

	resp := h.handleActivateCap(map[string]any{
		"name":      "project_cap",
		"thread_id": "t",
		"project":   "projA",
	})
	if resp.Error != "" {
		t.Errorf("matching-project activate should succeed, got: %s", resp.Error)
	}
}

func TestHandleActivate_ProjectScope_WrongProject(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "project_cap", "projA", "echo", nil)

	resp := h.handleActivateCap(map[string]any{
		"name":      "project_cap",
		"thread_id": "t",
		"project":   "projB",
	})
	if resp.Error == "" {
		t.Errorf("activating project-scoped cap from wrong project should fail")
	}
}

func TestHandleActivate_ProjectScope_MissingProject(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "project_cap", "projA", "echo", nil)

	resp := h.handleActivateCap(map[string]any{
		"name":      "project_cap",
		"thread_id": "t",
	})
	if resp.Error == "" {
		t.Errorf("activating project-scoped cap without project should fail")
	}
}

func TestHandleActivate_GlobalCap_FromAnyProject(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "global_cap", "", "echo", nil)

	resp := h.handleActivateCap(map[string]any{
		"name":      "global_cap",
		"thread_id": "t",
		"project":   "anything",
	})
	if resp.Error != "" {
		t.Errorf("global cap should activate from any project, got: %s", resp.Error)
	}
}

// --- deactivate_cap ---

func TestHandleDeactivate_RemovesCap(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo", nil)
	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})

	resp := h.handleDeactivateCap(map[string]any{"name": "c1", "thread_id": "t"})
	if resp.Error != "" {
		t.Fatalf("deactivate failed: %s", resp.Error)
	}
	m := unmarshalMap(t, resp)
	if dec, _ := m["deactivated"].(bool); !dec {
		t.Errorf("expected deactivated=true, got %v", m["deactivated"])
	}
	caps, _ := h.store.GetSessionCaps("t")
	if len(caps) != 0 {
		t.Errorf("expected 0 caps after deactivate, got %d", len(caps))
	}
}

func TestHandleDeactivate_Nonexistent_ReturnsFalse(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleDeactivateCap(map[string]any{"name": "c1", "thread_id": "t"})
	if resp.Error != "" {
		t.Errorf("deactivating nonexistent should not error, got: %s", resp.Error)
	}
	m := unmarshalMap(t, resp)
	if dec, _ := m["deactivated"].(bool); dec {
		t.Errorf("expected deactivated=false for nonexistent, got true")
	}
}

func TestHandleDeactivate_MissingName(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleDeactivateCap(map[string]any{"thread_id": "t"})
	if resp.Error == "" {
		t.Errorf("expected error when name missing")
	}
}

func TestHandleDeactivate_MissingThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleDeactivateCap(map[string]any{"name": "c1"})
	if resp.Error == "" {
		t.Errorf("expected error when thread_id missing")
	}
}

// --- get_active_caps ---

func TestHandleGetActive_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "t"})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	caps := unmarshalCaps(t, resp)
	if len(caps) != 0 {
		t.Errorf("expected empty active list, got %d", len(caps))
	}
}

func TestHandleGetActive_ReturnsMetaForActiveOnly(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo 1", []string{"tag1"})
	seedCap(t, h, "c2", "", "echo 2", []string{"tag2"})
	seedCap(t, h, "c3", "", "echo 3", nil)

	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})
	_ = h.handleActivateCap(map[string]any{"name": "c2", "thread_id": "t"})

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "t"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 2 {
		t.Fatalf("expected 2 active caps, got %d", len(caps))
	}
	names := map[string]bool{}
	for _, c := range caps {
		names[c.Meta.Name] = true
	}
	if !names["c1"] || !names["c2"] {
		t.Errorf("expected c1 and c2 active, got %v", names)
	}
	if names["c3"] {
		t.Errorf("c3 was not activated — should not appear in active list")
	}
}

func TestHandleGetActive_ThreadIsolation(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo", nil)
	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t1"})

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "t2"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 0 {
		t.Errorf("thread t2 should have 0 active caps, got %d", len(caps))
	}
}

func TestHandleGetActive_MissingThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetActiveCaps(map[string]any{})
	if resp.Error == "" {
		t.Errorf("expected error when thread_id missing")
	}
}

func TestHandleGetActive_SkipsDeletedCapability(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "c1", "", "echo", nil)
	_ = h.handleActivateCap(map[string]any{"name": "c1", "thread_id": "t"})

	learnings, _ := h.store.GetActiveLearnings("cap", "", "", "", 0)
	for _, l := range learnings {
		if meta, err := ParseCapMeta(l.Context); err == nil && meta.Name == "c1" {
			_ = h.store.SupersedeLearning(l.ID, -1, "test: cap deleted")
			break
		}
	}

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "t"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 0 {
		t.Errorf("stale activation to a deleted cap should be skipped, got %d", len(caps))
	}
}

// --- adapter direction tests ---
// The adapter contract:
//   CAP.md / user input = generic (store(), web(), file())
//   DB storage          = internal/provider (mcp__yesmem__cap_store())
//   Output / register   = generic again (store()) + adapter aliases

func TestHandleSaveCapability_StoresInternalForm(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "adapter_save",
		"description":  "Tests adapter direction on save",
		"handler_repl": `async ({q}) => { return store({action:'query', table:'t', where:q}); }`,
	})
	if resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "adapter_save"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1 cap, got %d", len(caps))
	}
	if !strings.Contains(caps[0].Meta.HandlerREPL, "mcp__yesmem__cap_store(") {
		t.Errorf("DB should store internal form with mcp__yesmem__cap_store(), got: %s", caps[0].Meta.HandlerREPL)
	}
	// Verify no standalone generic store() remains (strip provider name first to avoid substring false positive)
	stripped := strings.ReplaceAll(caps[0].Meta.HandlerREPL, "mcp__yesmem__cap_store", "")
	if strings.Contains(stripped, "store(") {
		t.Errorf("DB should not contain standalone generic store() after conversion, got: %s", caps[0].Meta.HandlerREPL)
	}
}

func TestHandleRegisterCapabilities_OutputsGenericForm(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "adapter_reg", "global", "",
		`async ({q}) => { return mcp__yesmem__cap_store({action:'query', table:'t', where:q}); }`,
		`{}`, []string{"test"},
	)

	resp := h.handleRegisterCaps(map[string]any{})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	code := result["code"].(string)

	if !strings.Contains(code, `((store)=>`) {
		t.Errorf("register_caps output should wrap tool with store closure, got: %s", code)
	}
	// Check the registerTool line specifically — adapter block naturally contains provider names
	regLine := ""
	for _, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "registerTool(") {
			regLine = line
			break
		}
	}
	if regLine == "" {
		t.Fatalf("no registerTool line found in output: %s", code)
	}
	if !strings.Contains(regLine, "store(") {
		t.Errorf("registerTool handler should contain store in generic form inside closure, got: %s", regLine)
	}
}

func TestHandleActivateCapability_OutputsGenericFormWithAdapter(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapFull(t, h, "adapter_act", "", "",
		`async ({q}) => { return mcp__yesmem__cap_store({action:'query', table:'t', where:q}); }`,
		`{}`, nil,
	)

	resp := h.handleActivateCap(map[string]any{
		"name":      "adapter_act",
		"thread_id": "t-adapt",
	})
	if resp.Error != "" {
		t.Fatalf("activate: %s", resp.Error)
	}
	m := unmarshalMap(t, resp)
	code := m["code"].(string)

	if !strings.Contains(code, `((store)=>`) {
		t.Errorf("activate_cap output should wrap tool with store closure, got: %s", code)
	}
	// Check the registerTool line specifically
	regLine := ""
	for _, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "registerTool(") {
			regLine = line
			break
		}
	}
	if regLine == "" {
		t.Fatalf("no registerTool line found in output: %s", code)
	}
	if !strings.Contains(regLine, "store(") {
		t.Errorf("registerTool handler should contain store in generic form inside closure, got: %s", regLine)
	}
}

func TestHandleActivateCapability_ResolvesThreadIDFromCallerPID(t *testing.T) {
	h, _ := mustHandler(t)
	h.pidMapMu.Lock()
	h.pidMap["session-from-pid"] = 12345
	h.pidMapMu.Unlock()

	resp := h.handleActivateCap(map[string]any{
		"name":        "does_not_matter",
		"_caller_pid": float64(12345),
	})

	if strings.Contains(resp.Error, "'thread_id' is required") {
		t.Errorf("thread_id must be resolved via _caller_pid fallback, got error: %s", resp.Error)
	}
}


// ---------- auto_active ----------
//
// 2026-04-17 feature: caps can be marked auto_active so new threads
// receive them as injected registerTool snippets without having to call
// activate_cap explicitly. Flag lives on the CapMeta stored
// in learning.context; GetActiveCapabilities returns the union of
// per-thread explicit activations and all caps with AutoActive=true,
// deduplicated by name.

func TestHandleSaveCapability_StoresAutoActiveFlag(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSaveCap(map[string]any{
		"name":         "auto_cap",
		"description":  "Auto-active test cap",
		"handler_bash": "echo hi",
		"auto_active":  true,
	})
	if resp.Error != "" {
		t.Fatalf("save failed: %s", resp.Error)
	}

	learnings, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		t.Fatalf("GetActiveLearnings: %v", err)
	}
	var meta CapMeta
	found := false
	for _, l := range learnings {
		m, err := ParseCapMeta(l.Context)
		if err == nil && m.Name == "auto_cap" {
			meta = m
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("saved cap not found in learnings store")
	}
	if !meta.AutoActive {
		t.Errorf("meta.AutoActive: want true, got false")
	}
}

func TestHandleGetActiveCapabilities_IncludesAutoActiveForUnactivatedThread(t *testing.T) {
	h, _ := mustHandler(t)
	if resp := h.handleSaveCap(map[string]any{
		"name":         "auto_cap",
		"description":  "Always on",
		"handler_bash": "echo",
		"auto_active":  true,
	}); resp.Error != "" {
		t.Fatalf("save auto_cap: %s", resp.Error)
	}
	if resp := h.handleSaveCap(map[string]any{
		"name":         "manual_cap",
		"description":  "Opt-in",
		"handler_bash": "echo",
		"auto_active":  false,
	}); resp.Error != "" {
		t.Fatalf("save manual_cap: %s", resp.Error)
	}

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "fresh-thread"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 1 {
		t.Fatalf("fresh thread should see 1 auto-active cap, got %d", len(caps))
	}
	if caps[0].Meta.Name != "auto_cap" {
		t.Errorf("expected auto_cap, got %+v", caps[0].Meta)
	}
}

func TestHandleGetActiveCapabilities_DeduplicatesAutoActivePlusExplicit(t *testing.T) {
	h, _ := mustHandler(t)
	if resp := h.handleSaveCap(map[string]any{
		"name":         "auto_cap",
		"description":  "Always on",
		"handler_bash": "echo",
		"auto_active":  true,
	}); resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}
	if resp := h.handleActivateCap(map[string]any{"name": "auto_cap", "thread_id": "t"}); resp.Error != "" {
		t.Fatalf("activate: %s", resp.Error)
	}

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "t"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 1 {
		t.Fatalf("auto-active + explicit activation should dedupe to 1, got %d", len(caps))
	}
	if caps[0].Meta.Name != "auto_cap" {
		t.Errorf("expected auto_cap, got %+v", caps[0].Meta)
	}
}

func TestHandleGetActiveCaps_ParentThreadPropagation(t *testing.T) {
	h, _ := mustHandler(t)

	if resp := h.handleSaveCap(map[string]any{
		"name":         "parent_tool",
		"description":  "Activated in parent session",
		"handler_repl": "async () => 'parent'",
		"auto_active":  false,
	}); resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}
	if resp := h.handleActivateCap(map[string]any{
		"name":      "parent_tool",
		"thread_id": "parent-session-abc",
	}); resp.Error != "" {
		t.Fatalf("activate: %s", resp.Error)
	}

	resp := h.handleGetActiveCaps(map[string]any{"thread_id": "subagent-xyz"})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 0 {
		t.Fatalf("subagent without parent_thread_id should get 0 caps, got %d", len(caps))
	}

	resp = h.handleGetActiveCaps(map[string]any{
		"thread_id":        "subagent-xyz",
		"parent_thread_id": "parent-session-abc",
	})
	caps = unmarshalCaps(t, resp)
	if len(caps) != 1 {
		t.Fatalf("subagent with parent_thread_id should get 1 cap, got %d", len(caps))
	}
	if caps[0].Meta.Name != "parent_tool" {
		t.Errorf("expected parent_tool, got %s", caps[0].Meta.Name)
	}
}

func TestHandleGetActiveCaps_ParentAndOwnCaps(t *testing.T) {
	h, _ := mustHandler(t)

	if resp := h.handleSaveCap(map[string]any{
		"name": "shared_tool", "description": "From parent", "handler_repl": "async () => 'shared'", "auto_active": false,
	}); resp.Error != "" {
		t.Fatalf("save shared: %s", resp.Error)
	}
	if resp := h.handleSaveCap(map[string]any{
		"name": "own_tool", "description": "Subagent's own", "handler_repl": "async () => 'own'", "auto_active": false,
	}); resp.Error != "" {
		t.Fatalf("save own: %s", resp.Error)
	}

	h.handleActivateCap(map[string]any{"name": "shared_tool", "thread_id": "parent-id"})
	h.handleActivateCap(map[string]any{"name": "own_tool", "thread_id": "sub-id"})

	resp := h.handleGetActiveCaps(map[string]any{
		"thread_id":        "sub-id",
		"parent_thread_id": "parent-id",
	})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 2 {
		t.Fatalf("should get parent + own caps = 2, got %d", len(caps))
	}
	names := map[string]bool{}
	for _, c := range caps {
		names[c.Meta.Name] = true
	}
	if !names["shared_tool"] || !names["own_tool"] {
		t.Errorf("expected shared_tool + own_tool, got %v", names)
	}
}

func TestHandleGetActiveCaps_ParentSameAsThread(t *testing.T) {
	h, _ := mustHandler(t)

	if resp := h.handleSaveCap(map[string]any{
		"name": "main_tool", "description": "Main session", "handler_repl": "async () => 'main'", "auto_active": false,
	}); resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}
	h.handleActivateCap(map[string]any{"name": "main_tool", "thread_id": "main-session"})

	resp := h.handleGetActiveCaps(map[string]any{
		"thread_id":        "main-session",
		"parent_thread_id": "main-session",
	})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 1 {
		t.Fatalf("same parent/thread should dedupe to 1, got %d", len(caps))
	}
}

func TestHandleGetActiveCaps_ParentChildOverlapDedup(t *testing.T) {
	h, _ := mustHandler(t)

	if resp := h.handleSaveCap(map[string]any{
		"name": "overlap_tool", "description": "Activated in both", "handler_repl": "async () => 'overlap'", "auto_active": false,
	}); resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}

	h.handleActivateCap(map[string]any{"name": "overlap_tool", "thread_id": "parent-t"})
	h.handleActivateCap(map[string]any{"name": "overlap_tool", "thread_id": "child-t"})

	resp := h.handleGetActiveCaps(map[string]any{
		"thread_id":        "child-t",
		"parent_thread_id": "parent-t",
	})
	caps := unmarshalCaps(t, resp)
	if len(caps) != 1 {
		t.Fatalf("same cap in parent+child should dedupe to 1, got %d", len(caps))
	}
	if caps[0].Meta.Name != "overlap_tool" {
		t.Errorf("expected overlap_tool, got %s", caps[0].Meta.Name)
	}
}

func TestHandleActivateCapability_BashOnlyOmitsAdapterJS(t *testing.T) {
	h, _ := mustHandler(t)

	if resp := h.handleSaveCap(map[string]any{
		"name":         "bash_tool",
		"description":  "Bash only, no adapter needed",
		"handler_bash": "echo hello",
	}); resp.Error != "" {
		t.Fatalf("save: %s", resp.Error)
	}

	resp := h.handleActivateCap(map[string]any{
		"name":      "bash_tool",
		"thread_id": "t-bash",
	})
	if resp.Error != "" {
		t.Fatalf("activate: %s", resp.Error)
	}
	m := unmarshalMap(t, resp)
	code := m["code"].(string)
	if strings.Contains(code, "globalThis.store") {
		t.Errorf("bash-only cap should NOT include adapter JS, got: %s", code)
	}
	if !strings.Contains(code, "registerTool(") {
		t.Errorf("should still contain registerTool, got: %s", code)
	}
}

// --- T8: save_cap Field-Merge (Variante B) ---

func TestSaveCap_PreservesScriptsWhenOmitted(t *testing.T) {
	h, _ := mustHandler(t)

	// Create cap with two scripts
	h.handleSaveCap(map[string]any{
		"name":        "merge_test",
		"description": "V1",
		"scripts":     `[{"name":"run","kind":"tool","runtime":"bash","body":"echo run","schema":"{}"},{"name":"setup","kind":"handler","runtime":"bash","body":"echo setup"}]`,
	})

	// Update only description, no scripts
	resp := h.handleSaveCap(map[string]any{
		"name":        "merge_test",
		"description": "V2 updated description",
	})
	if resp.Error != "" {
		t.Fatalf("save without scripts: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "merge_test"}))
	if len(caps) != 1 {
		t.Fatalf("expected 1 active cap, got %d", len(caps))
	}
	if caps[0].Meta.Description != "V2 updated description" {
		t.Errorf("description = %q, want %q", caps[0].Meta.Description, "V2 updated description")
	}
	if len(caps[0].Meta.Scripts) != 2 {
		t.Fatalf("expected 2 scripts preserved, got %d", len(caps[0].Meta.Scripts))
	}
	if caps[0].Meta.Scripts[0].Name != "run" || caps[0].Meta.Scripts[1].Name != "setup" {
		t.Errorf("scripts = %v, want run,setup", scriptNames(caps[0].Meta.Scripts))
	}
}

func TestSaveCap_MergesScripts(t *testing.T) {
	h, _ := mustHandler(t)

	// Create cap with run + setup
	h.handleSaveCap(map[string]any{
		"name":        "merge_test",
		"description": "V1",
		"scripts":     `[{"name":"run","kind":"tool","runtime":"bash","body":"echo v1","schema":"{}"},{"name":"setup","kind":"handler","runtime":"bash","body":"echo setup"}]`,
	})

	// Update: modify run, add teardown, omit setup
	resp := h.handleSaveCap(map[string]any{
		"name":        "merge_test",
		"description": "V2",
		"scripts":     `[{"name":"run","kind":"tool","runtime":"bash","body":"echo v2","schema":"{}"},{"name":"teardown","kind":"handler","runtime":"bash","body":"echo teardown"}]`,
	})
	if resp.Error != "" {
		t.Fatalf("save with merge: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "merge_test"}))
	if len(caps[0].Meta.Scripts) != 3 {
		t.Fatalf("expected 3 scripts after merge (run updated, setup kept, teardown added), got %d: %v", len(caps[0].Meta.Scripts), scriptNames(caps[0].Meta.Scripts))
	}
	byName := make(map[string]ScriptMeta)
	for _, sc := range caps[0].Meta.Scripts {
		byName[sc.Name] = sc
	}
	if byName["run"].Body != "echo v2" {
		t.Errorf("run body = %q, want echo v2", byName["run"].Body)
	}
	if byName["setup"].Body != "echo setup" {
		t.Errorf("setup body = %q, want echo setup (should be preserved)", byName["setup"].Body)
	}
	if _, ok := byName["teardown"]; !ok {
		t.Error("teardown script should have been added")
	}
}

func TestSaveCap_PreservesScriptSandboxOnMetadataOnlyUpdate(t *testing.T) {
	h, _ := mustHandler(t)

	// Create cap with run script having sandbox=none
	h.handleSaveCap(map[string]any{
		"name":        "sandbox_test",
		"description": "V1",
		"scripts":     `[{"name":"run","kind":"tool","runtime":"bash","body":"echo hi","schema":"{}","sandbox":"none"}]`,
	})

	// Update only description
	resp := h.handleSaveCap(map[string]any{
		"name":        "sandbox_test",
		"description": "V2 updated",
	})
	if resp.Error != "" {
		t.Fatalf("save without scripts: %s", resp.Error)
	}

	caps := unmarshalCaps(t, h.handleGetCaps(map[string]any{"name": "sandbox_test"}))
	if len(caps[0].Meta.Scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(caps[0].Meta.Scripts))
	}
	if caps[0].Meta.Scripts[0].Sandbox != "none" {
		t.Errorf("sandbox = %q, want none (should be preserved)", caps[0].Meta.Scripts[0].Sandbox)
	}
}

func TestSaveCap_ExplicitEmptyScriptsClearsAll(t *testing.T) {
	h, _ := mustHandler(t)

	// Create cap with scripts
	h.handleSaveCap(map[string]any{
		"name":        "clear_test",
		"description": "V1",
		"scripts":     `[{"name":"run","kind":"tool","runtime":"bash","body":"echo hi","schema":"{}"}]`,
	})

	// Explicit empty scripts array
	resp := h.handleSaveCap(map[string]any{
		"name":        "clear_test",
		"description": "V2",
		"scripts":     `[]`,
	})
	if resp.Error == "" {
		t.Fatal("expected error for scripts:[] — empty scripts should be rejected")
	}
}

func scriptNames(scripts []ScriptMeta) []string {
	names := make([]string, len(scripts))
	for i, sc := range scripts {
		names[i] = sc.Name
	}
	return names
}
