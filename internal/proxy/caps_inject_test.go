package proxy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeCapabilitiesResponse_ConvertsToInjection(t *testing.T) {
	raw := json.RawMessage(`[
      {
        "id": 42,
        "project": "test",
        "source": "user_stated",
        "meta": {
          "cap_name": "git_log",
          "cap_description": "Show git log",
          "cap_scripts": [
            {
              "name": "git_log",
              "kind": "tool",
              "runtime": "bash",
              "body": "git log --oneline",
              "schema": "{\"type\":\"object\"}"
            }
          ]
        }
      },
      {
        "id": 43,
        "source": "agreed_upon",
        "meta": {
          "cap_name": "weather",
          "cap_description": "Fetch weather",
          "cap_scripts": [
            {
              "name": "weather",
              "kind": "tool",
              "runtime": "repl",
              "body": "async (a) => fetch(a.url)",
              "schema": "{}"
            }
          ]
        }
      }
    ]`)

	caps, err := decodeCapsResponse(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("want 2 caps, got %d", len(caps))
	}
	c1 := caps[0]
	if c1.Name != "git_log" {
		t.Errorf("c1.Name: want git_log, got %q", c1.Name)
	}
	if c1.Description != "Show git log" {
		t.Errorf("c1.Description: want 'Show git log', got %q", c1.Description)
	}
	if len(c1.Scripts) != 1 {
		t.Fatalf("c1: want 1 script, got %d", len(c1.Scripts))
	}
	if c1.Scripts[0].Runtime != "bash" {
		t.Errorf("c1.Scripts[0].Runtime: want bash, got %q", c1.Scripts[0].Runtime)
	}
	if c1.Scripts[0].Body != "git log --oneline" {
		t.Errorf("c1.Scripts[0].Body: want 'git log --oneline', got %q", c1.Scripts[0].Body)
	}
	if c1.Scripts[0].Schema != `{"type":"object"}` {
		t.Errorf("c1.Scripts[0].Schema: want object schema, got %q", c1.Scripts[0].Schema)
	}
	c2 := caps[1]
	if c2.Name != "weather" {
		t.Errorf("c2.Name: want weather, got %q", c2.Name)
	}
	if len(c2.Scripts) != 1 {
		t.Fatalf("c2: want 1 script, got %d", len(c2.Scripts))
	}
	if c2.Scripts[0].Runtime != "repl" {
		t.Errorf("c2.Scripts[0].Runtime: want repl, got %q", c2.Scripts[0].Runtime)
	}
	if c2.Scripts[0].Body != "async (a) => fetch(a.url)" {
		t.Errorf("c2.Scripts[0].Body: want REPL handler, got %q", c2.Scripts[0].Body)
	}
}

func TestRenderCapabilitiesBlock_WrapsInSystemReminder(t *testing.T) {
	caps := []CapInjection{
		{Name: "git_log", Description: "Show git log", Scripts: []ScriptInjection{
			{Name: "git_log", Kind: "tool", Runtime: "bash", Body: "git log", Schema: "{}"},
		}},
	}
	block := renderCapabilitiesBlock(caps)
	if !strings.Contains(block, "<system-reminder>") {
		t.Errorf("want <system-reminder>, got: %q", block)
	}
	if !strings.Contains(block, "<caps-active>") && !strings.Contains(block, "<caps-available>") {
		t.Errorf("want <caps-active> or <caps-available>, got: %q", block)
	}
	if !strings.Contains(block, `registerTool("git_log"`) {
		t.Errorf("want registerTool for git_log, got: %q", block)
	}
	if !strings.Contains(block, "MANDATORY") {
		t.Errorf("want MANDATORY directive to assert tool preference, got: %q", block)
	}
}

func TestRenderCapabilitiesBlock_EmptyReturnsEmpty(t *testing.T) {
	if block := renderCapabilitiesBlock(nil); block != "" {
		t.Errorf("empty caps: want empty block, got %q", block)
	}
}

func TestRenderCapabilitiesBlock_IncludesAdapterJS(t *testing.T) {
	caps := []CapInjection{
		{Name: "my_tool", Description: "Uses store", Scripts: []ScriptInjection{
			{Name: "my_tool", Kind: "tool", Runtime: "repl", Body: "async (a) => { let r = await store({action:'query',table:'t'}); return r; }", Schema: "{}"},
		}},
	}
	block := renderCapabilitiesBlock(caps)
	if !strings.Contains(block, `((store)=>`) {
		t.Errorf("want store-closure wrapper in registerTool line, got: %s", block[:min(len(block), 300)])
	}
	if !strings.Contains(block, `"my_tool"`) {
		t.Errorf("want capability name in store closure, got: %s", block[:min(len(block), 300)])
	}
}

func TestRenderCapabilitiesBlock_NoAdapterForBashOnly(t *testing.T) {
	caps := []CapInjection{
		{Name: "deploy", Description: "Deploy", Scripts: []ScriptInjection{
			{Name: "deploy", Kind: "tool", Runtime: "bash", Body: "make deploy", Schema: "{}"},
		}},
	}
	block := renderCapabilitiesBlock(caps)
	if strings.Contains(block, "globalThis.store") {
		t.Errorf("bash-only cap should NOT include adapter JS")
	}
}

// TestInjectCapabilitiesTurnImpl_PassesProjectScope verifies the proxy sends
// the request's project so the daemon can filter out foreign projects' caps.
func TestInjectCapabilitiesTurnImpl_PassesProjectScope(t *testing.T) {
	var gotProject any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		if method == "get_active_caps" {
			gotProject = params["project"]
		}
		return json.RawMessage(`[]`), nil
	}
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "# Environment\n - Primary working directory: /home/me/proj-a\n"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	injectCapabilitiesTurnImpl(req, "tid", "", queryFn, nil, nil)
	if gotProject != "/home/me/proj-a" {
		t.Errorf("expected project scope /home/me/proj-a, got %v", gotProject)
	}
}

func TestInjectCapabilitiesTurnImpl_InsertsAfterBriefingAndCodeMap(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "<system-reminder>\nYour full session briefing\n...\n</system-reminder>"},
			map[string]any{"role": "assistant", "content": "Understood. I've read the session briefing."},
			map[string]any{"role": "user", "content": "<system-reminder>\n## Code Map\n...\n</system-reminder>"},
			map[string]any{"role": "assistant", "content": "Understood. I've read the code map."},
			map[string]any{"role": "user", "content": "REAL"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)
	if !injected {
		t.Fatal("expected injected=true")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 7 {
		t.Fatalf("want 7 messages (5 original + caps pair), got %d", len(msgs))
	}
	capsUser, _ := msgs[4].(map[string]any)
	if capsUser["role"] != "user" {
		t.Errorf("msgs[4].role: want user, got %v", capsUser["role"])
	}
	content, _ := capsUser["content"].(string)
	if !strings.Contains(content, "<caps-active") && !strings.Contains(content, "<caps-available") {
		t.Errorf("msgs[4] should contain <caps-active> or <caps-available>, got: %q", content)
	}
	capsAssist, _ := msgs[5].(map[string]any)
	if capsAssist["role"] != "assistant" {
		t.Errorf("msgs[5].role: want assistant, got %v", capsAssist["role"])
	}
	realUser, _ := msgs[6].(map[string]any)
	if realUser["content"] != "REAL" {
		t.Errorf("msgs[6] must preserve REAL user msg, got: %v", realUser["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_InsertsAfterBriefingWithoutCodeMap(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "<system-reminder>\nYour full session briefing\n...\n</system-reminder>"},
			map[string]any{"role": "assistant", "content": "Understood. I've read the session briefing."},
			map[string]any{"role": "user", "content": "REAL"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)
	if !injected {
		t.Fatal("expected injected=true")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("want 5 messages, got %d", len(msgs))
	}
	capsUser, _ := msgs[2].(map[string]any)
	if capsUser["role"] != "user" {
		t.Errorf("msgs[2] want caps user, got role %v", capsUser["role"])
	}
	realUser, _ := msgs[4].(map[string]any)
	if realUser["content"] != "REAL" {
		t.Errorf("msgs[4] want real user, got %v", realUser["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_InsertsAtZeroWithoutHeaders(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "REAL"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)
	if !injected {
		t.Fatal("expected injected=true")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	capsUser, _ := msgs[0].(map[string]any)
	if capsUser["role"] != "user" {
		t.Errorf("msgs[0] want caps user, got role %v", capsUser["role"])
	}
	realUser, _ := msgs[2].(map[string]any)
	if realUser["content"] != "REAL" {
		t.Errorf("msgs[2] want real user, got %v", realUser["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_PreservesToolUseAdjacency(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "initial"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "running tool"},
				map[string]any{"type": "tool_use", "id": "tu_abc", "name": "X", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_abc", "content": "result"},
			}},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)
	if !injected {
		t.Fatal("expected injected=true")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("want 5 messages, got %d", len(msgs))
	}
	// Caps pair at 0-1. Original messages at 2-3-4.
	// Adjacency: msgs[3] (assistant tool_use) must remain immediately before msgs[4] (user tool_result).
	assist, _ := msgs[3].(map[string]any)
	user, _ := msgs[4].(map[string]any)
	if assist["role"] != "assistant" || user["role"] != "user" {
		t.Fatalf("tool_use/tool_result adjacency broken: msgs[3].role=%v msgs[4].role=%v", assist["role"], user["role"])
	}
	assistContent, _ := assist["content"].([]any)
	hasToolUse := false
	for _, b := range assistContent {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_use" {
			hasToolUse = true
		}
	}
	if !hasToolUse {
		t.Error("tool_use block must stay in msgs[3] assistant")
	}
	userContent, _ := user["content"].([]any)
	hasToolResult := false
	for _, b := range userContent {
		if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("tool_result block must stay in msgs[4] user")
	}
}

func TestInjectCapabilitiesTurnImpl_InsertsAfterHeaderInLongSession(t *testing.T) {
	// Regression 2026-04-17: walk-back-from-lastUser landed the caps-pair
	// near the end of long sessions because plain-text assistant messages
	// in the conversation tail broke the walk-back loop (it only skipped
	// tool_use/tool_result pairs). Caps-pair must always land after the
	// briefing+codemap header regardless of how long the conversation has
	// grown afterwards — required for Anthropic prefix-cache stability
	// (caps change should invalidate only content below the header, not
	// drift position every turn).
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "<system-reminder>\nYour full session briefing\n...\n</system-reminder>"},
			map[string]any{"role": "assistant", "content": "Understood. I've read the session briefing."},
			map[string]any{"role": "user", "content": "<system-reminder>\n## Code Map\n...\n</system-reminder>"},
			map[string]any{"role": "assistant", "content": "Understood. I've read the code map."},
			map[string]any{"role": "user", "content": "first question"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second question"},
			map[string]any{"role": "assistant", "content": "second answer"},
			map[string]any{"role": "user", "content": "third question"},
			map[string]any{"role": "assistant", "content": "third answer"},
			map[string]any{"role": "user", "content": "CURRENT"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)
	if !injected {
		t.Fatal("expected injected=true")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 13 {
		t.Fatalf("want 13 messages (11 original + caps pair), got %d", len(msgs))
	}
	capsUser, _ := msgs[4].(map[string]any)
	if capsUser["role"] != "user" {
		t.Errorf("msgs[4].role: want user (caps block), got %v", capsUser["role"])
	}
	content, _ := capsUser["content"].(string)
	if !strings.Contains(content, "<caps-active") && !strings.Contains(content, "<caps-available") {
		t.Errorf("msgs[4] should contain caps block, got: %q", content[:min(len(content), 80)])
	}
	capsAssist, _ := msgs[5].(map[string]any)
	if capsAssist["role"] != "assistant" {
		t.Errorf("msgs[5].role: want assistant ack, got %v", capsAssist["role"])
	}
	firstQuestion, _ := msgs[6].(map[string]any)
	if firstQuestion["content"] != "first question" {
		t.Errorf("msgs[6] must preserve first question after shift, got: %v", firstQuestion["content"])
	}
	lastUser, _ := msgs[12].(map[string]any)
	if lastUser["content"] != "CURRENT" {
		t.Errorf("msgs[12] must preserve CURRENT user turn, got: %v", lastUser["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_InjectsEvenWhenMarkerInPriorContent(t *testing.T) {
	// Regression 2026-04-17: earlier idempotency check scanned all prior
	// messages for the literal string "<caps-active>" and skipped
	// injection on any match. Archive-stub summaries (e.g., gotcha notes
	// documenting the injector) contain that string as plain text, which
	// silently suppressed every subsequent injection once such a stub
	// entered history. Injection must fire every turn regardless of prior
	// text content — Claude Code never resends proxy-injected markers, so
	// stacking cannot occur.
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"git_log","cap_description":"Show git log","cap_scripts":[{"name":"git_log","kind":"tool","runtime":"bash","body":"git log","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "prior gotcha mentioned <caps-active> in plain text"},
			map[string]any{"role": "assistant", "content": "ack"},
			map[string]any{"role": "user", "content": "REAL"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)

	if !injected {
		t.Fatal("expected injected=true; marker in prior free text must not suppress injection")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages after injection (3 prior + 2 injected), got %d", len(msgs))
	}
}

func TestInjectCapabilitiesTurnImpl_NoCapsReturnsFalse(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "REAL"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)

	if injected {
		t.Error("expected injected=false when no caps active")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages must be unchanged (no caps), got %d messages", len(msgs))
	}
	user, _ := msgs[0].(map[string]any)
	if user["content"] != "REAL" {
		t.Errorf("original message must be preserved, got %v", user["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_NoUserMessageReturnsFalse(t *testing.T) {
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`[{"id":1,"source":"user_stated","meta":{"cap_name":"foo","cap_description":"bar","cap_scripts":[{"name":"foo","kind":"tool","runtime":"bash","body":"echo foo","schema":"{}"}]}}]`), nil
	}
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "content": "ack"},
		},
	}

	injected := injectCapabilitiesTurnImpl(req, "thread-xyz", "", queryFn, nil, nil)

	if injected {
		t.Error("expected injected=false when no user message present")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages must be unchanged, got %d", len(msgs))
	}
}

func TestRenderCapabilitiesBlock_SortsCapsByNameForByteEquality(t *testing.T) {
	// Two renders with the same caps in different order must yield byte-identical
	// output. Without this invariant Anthropic's byte-equality prompt cache
	// misses on every turn even though the active set is unchanged.
	capsAZ := []CapInjection{
		{Name: "alpha", Description: "a", Scripts: []ScriptInjection{{Name: "alpha", Kind: "tool", Runtime: "bash", Body: "echo a", Schema: "{}"}}},
		{Name: "bravo", Description: "b", Scripts: []ScriptInjection{{Name: "bravo", Kind: "tool", Runtime: "bash", Body: "echo b", Schema: "{}"}}},
		{Name: "charlie", Description: "c", Scripts: []ScriptInjection{{Name: "charlie", Kind: "tool", Runtime: "bash", Body: "echo c", Schema: "{}"}}},
	}
	capsZA := []CapInjection{
		{Name: "charlie", Description: "c", Scripts: []ScriptInjection{{Name: "charlie", Kind: "tool", Runtime: "bash", Body: "echo c", Schema: "{}"}}},
		{Name: "bravo", Description: "b", Scripts: []ScriptInjection{{Name: "bravo", Kind: "tool", Runtime: "bash", Body: "echo b", Schema: "{}"}}},
		{Name: "alpha", Description: "a", Scripts: []ScriptInjection{{Name: "alpha", Kind: "tool", Runtime: "bash", Body: "echo a", Schema: "{}"}}},
	}

	outAZ := renderCapabilitiesBlock(capsAZ)
	outZA := renderCapabilitiesBlock(capsZA)

	if outAZ != outZA {
		t.Errorf("renderCapabilitiesBlock must be order-independent for byte-equality caching\nAZ: %q\nZA: %q", outAZ, outZA)
	}

	// And the canonical order is alphabetical by name: alpha < bravo < charlie.
	idxAlpha := strings.Index(outAZ, `"alpha"`)
	idxBravo := strings.Index(outAZ, `"bravo"`)
	idxCharlie := strings.Index(outAZ, `"charlie"`)
	if idxAlpha < 0 || idxBravo < 0 || idxCharlie < 0 {
		t.Fatalf("rendered block missing one of the cap names: %q", outAZ)
	}
	if !(idxAlpha < idxBravo && idxBravo < idxCharlie) {
		t.Errorf("caps must be rendered in alphabetical order; got alpha@%d bravo@%d charlie@%d", idxAlpha, idxBravo, idxCharlie)
	}
}

func TestInjectCaps_DaemonError_UsesCachedCaps(t *testing.T) {
	cache := NewCapsCache()
	validRaw := json.RawMessage(`[{"name":"cap1","description":"d","input_schema":"{}","handler_repl":"() => {}"}]`)
	cache.Set("tid-1", validRaw)
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question"},
	}}
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return nil, errors.New("daemon unreachable")
	}
	injected := injectCapabilitiesTurnImpl(req, "tid-1", "", queryFn, cache, nil)
	if !injected {
		t.Fatalf("expected inject=true via cache fallback, got false")
	}
	msgs := req["messages"].([]any)
	found := false
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			if s, ok := mm["content"].(string); ok && (strings.Contains(s, "<caps-active>") || strings.Contains(s, "<caps-available>")) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected <caps-active> in messages after cache fallback")
	}
}

func TestInjectCaps_DecodeError_UsesCachedCaps(t *testing.T) {
	cache := NewCapsCache()
	validRaw := json.RawMessage(`[{"name":"cap1","description":"d","input_schema":"{}","handler_repl":"() => {}"}]`)
	cache.Set("tid-2", validRaw)
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question"},
	}}
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{not valid json`), nil
	}
	injected := injectCapabilitiesTurnImpl(req, "tid-2", "", queryFn, cache, nil)
	if !injected {
		t.Fatalf("expected inject=true via cache fallback on decode error, got false")
	}
}

func TestInjectCaps_DaemonSuccess_UpdatesCache(t *testing.T) {
	cache := NewCapsCache()
	validRaw := json.RawMessage(`[{"name":"cap1","description":"d","input_schema":"{}","handler_repl":"() => {}"}]`)
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question"},
	}}
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return validRaw, nil
	}
	injected := injectCapabilitiesTurnImpl(req, "tid-3", "", queryFn, cache, nil)
	if !injected {
		t.Fatalf("expected inject=true on daemon success")
	}
	if _, ok := cache.Get("tid-3"); !ok {
		t.Fatalf("expected capsCache to be populated after successful fetch")
	}
}

func TestInjectCaps_ErrorAndEmptyCache_ReturnsFalse(t *testing.T) {
	cache := NewCapsCache()
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question"},
	}}
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		return nil, errors.New("daemon down")
	}
	injected := injectCapabilitiesTurnImpl(req, "tid-4", "", queryFn, cache, nil)
	if injected {
		t.Fatalf("expected inject=false when both daemon fails and cache empty")
	}
}

func TestRenderCapabilitiesCatalog_ShortOutput(t *testing.T) {
	caps := []CapInjection{
		{
			Name:        "reddit_fetch",
			Description: "Fetch a Reddit post URL and return structured data",
			Scripts: []ScriptInjection{{
				Name:    "reddit_fetch",
				Kind:    "tool",
				Runtime: "repl",
				Body:    `async ({url}) => { /* 500 lines of JS */ return {post: {}, comments: []}; }`,
				Schema:  `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
			}},
		},
		{
			Name:        "reddit_search",
			Description: "Search Reddit for posts, persist and classify",
			Scripts: []ScriptInjection{{
				Name:    "reddit_search",
				Kind:    "tool",
				Runtime: "repl",
				Body:    `async ({query}) => { /* 400 lines of JS */ return {posts: []}; }`,
				Schema:  `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
			}},
		},
		{
			Name:        "cap_collect",
			Description: "Generic collect-and-prep over any cap_store table",
			Scripts: []ScriptInjection{{
				Name:    "cap_collect",
				Kind:    "tool",
				Runtime: "repl",
				Body:    `async ({cap, table}) => { /* 300 lines of JS */ return {rows: []}; }`,
				Schema:  `{"type":"object","properties":{"cap":{"type":"string"},"table":{"type":"string"}},"required":["cap","table"]}`,
			}},
		},
	}

	full := renderCapabilitiesBlock(caps)
	catalog := renderCapabilitiesCatalog(caps)

	if len(catalog) >= len(full) {
		t.Errorf("catalog (%d bytes) should be much smaller than full block (%d bytes)", len(catalog), len(full))
	}
	if len(catalog) > 3000 {
		t.Errorf("catalog too large: %d bytes, expected < 3000", len(catalog))
	}

	if !strings.Contains(catalog, "reddit_fetch") {
		t.Error("catalog should list reddit_fetch")
	}
	if !strings.Contains(catalog, "reddit_search") {
		t.Error("catalog should list reddit_search")
	}
	if !strings.Contains(catalog, "cap_collect") {
		t.Error("catalog should list cap_collect")
	}
	if !strings.Contains(catalog, "Fetch a Reddit post") {
		t.Error("catalog should include description")
	}

	if strings.Contains(catalog, "500 lines of JS") {
		t.Error("catalog must NOT contain full handler code")
	}
	if strings.Contains(catalog, "400 lines of JS") {
		t.Error("catalog must NOT contain full handler code")
	}

	if !strings.Contains(catalog, "mcp__yesmem__activate_cap") {
		t.Error("catalog should include direct MCP activation instruction")
	}
	if strings.Contains(catalog, "registerTool") {
		t.Error("catalog must NOT contain bootstrapper registerTool code")
	}
	if strings.Contains(catalog, "new Function") {
		t.Error("catalog must NOT contain new Function eval pattern")
	}
}

func TestInjectCaps_SuccessAfterError_RecoversFromCache(t *testing.T) {
	cache := NewCapsCache()
	validRaw := json.RawMessage(`[{"name":"cap1","description":"d","input_schema":"{}","handler_repl":"() => {}"}]`)
	reqA := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question A"},
	}}
	queryOK := func(method string, params map[string]any) (json.RawMessage, error) {
		return validRaw, nil
	}
	if !injectCapabilitiesTurnImpl(reqA, "tid-5", "", queryOK, cache, nil) {
		t.Fatalf("first call with daemon success should inject")
	}
	if _, ok := cache.Get("tid-5"); !ok {
		t.Fatalf("expected cache populated after first success")
	}
	reqB := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "question B"},
	}}
	queryFail := func(method string, params map[string]any) (json.RawMessage, error) {
		return nil, errors.New("transient")
	}
	if !injectCapabilitiesTurnImpl(reqB, "tid-5", "", queryFail, cache, nil) {
		t.Fatalf("second call with daemon error should inject via cache fallback")
	}
}

func TestInjectCaps_ParentThreadIDPassedToQuery(t *testing.T) {
	var capturedParams map[string]any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		capturedParams = params
		return json.RawMessage(`[]`), nil
	}
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hi"},
	}}
	injectCapabilitiesTurnImpl(req, "sub-thread", "parent-thread", queryFn, nil, nil)
	if capturedParams["parent_thread_id"] != "parent-thread" {
		t.Errorf("expected parent_thread_id=parent-thread, got %v", capturedParams["parent_thread_id"])
	}
	if capturedParams["thread_id"] != "sub-thread" {
		t.Errorf("expected thread_id=sub-thread, got %v", capturedParams["thread_id"])
	}
}

func TestInjectCaps_EmptyParentOmitted(t *testing.T) {
	var capturedParams map[string]any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		capturedParams = params
		return json.RawMessage(`[]`), nil
	}
	req := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "hi"},
	}}
	injectCapabilitiesTurnImpl(req, "my-thread", "", queryFn, nil, nil)
	if _, exists := capturedParams["parent_thread_id"]; exists {
		t.Errorf("parent_thread_id should not be present when empty, got %v", capturedParams["parent_thread_id"])
	}
}

func TestGetParentThread_NeverReturnsFalseParent(t *testing.T) {
	s := &Server{}

	// No thread should ever get a false parent assigned
	if got := s.getParentThread("thread-1"); got != "" {
		t.Errorf("thread-1 should have no parent, got %q", got)
	}
	if got := s.getParentThread("thread-2"); got != "" {
		t.Errorf("thread-2 should have no parent, got %q", got)
	}
	if got := s.getParentThread(""); got != "" {
		t.Errorf("empty thread should have no parent, got %q", got)
	}
}

func TestInjectCapabilitiesTurnImpl_AppendsReplPatternSuggestionToLastUser(t *testing.T) {
	capsRaw := json.RawMessage(`[{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"reddit_fetch","cap_description":"Reddit","cap_scripts":[]}}]`)
	suggRaw := json.RawMessage(`{"pattern":{"id":99,"project":"alpha","shape_hash":"deadbeefcafebabe","first_cmd_example":"sqlite3 caps.db SELECT FROM cap_reddit__posts","matched_cap":"reddit_fetch","count":7,"dismiss_count":0}}`)

	var lastSuggParams map[string]any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "get_active_caps":
			return capsRaw, nil
		case "get_repl_pattern_suggestion":
			lastSuggParams = params
			return suggRaw, nil
		}
		return nil, errors.New("unexpected: " + method)
	}

	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "do the thing"},
		},
	}

	if !injectCapabilitiesTurnImpl(req, "thread-1", "", queryFn, nil, nil) {
		t.Fatalf("inject returned false")
	}

	if lastSuggParams == nil {
		t.Fatalf("get_repl_pattern_suggestion not called")
	}
	if lastSuggParams["project"] != "alpha" {
		t.Errorf("expected project=alpha, got %v", lastSuggParams["project"])
	}
	activeCaps, _ := lastSuggParams["active_caps"].([]any)
	if len(activeCaps) != 1 || activeCaps[0] != "reddit_fetch" {
		t.Errorf("expected active_caps=[reddit_fetch], got %v", activeCaps)
	}

	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("expected last role=user, got %v", last["role"])
	}
	content, _ := last["content"].(string)
	for _, want := range []string{"deadbeefcafebabe", "reddit_fetch", "dismiss_repl_pattern", "<system-reminder>"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected content to contain %q, got: %s", want, content)
		}
	}
}

func TestInjectCapabilitiesTurnImpl_NoReplSuggestionLeavesLastUserUnchanged(t *testing.T) {
	capsRaw := json.RawMessage(`[{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"reddit_fetch","cap_description":"Reddit","cap_scripts":[]}}]`)

	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "get_active_caps":
			return capsRaw, nil
		case "get_repl_pattern_suggestion":
			return json.RawMessage(`null`), nil
		}
		return nil, errors.New("unexpected: " + method)
	}

	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "do the thing"},
		},
	}

	if !injectCapabilitiesTurnImpl(req, "tid", "", queryFn, nil, nil) {
		t.Fatalf("inject returned false")
	}

	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != "do the thing" {
		t.Errorf("expected last content unchanged, got: %v", last["content"])
	}
}

func TestInjectCapabilitiesTurnImpl_FiltersActiveCapsByProject(t *testing.T) {
	capsRaw := json.RawMessage(`[` +
		`{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"reddit_fetch","cap_description":"R","cap_scripts":[]}},` +
		`{"id":2,"project":"beta","source":"user_stated","meta":{"cap_name":"telegram","cap_description":"T","cap_scripts":[]}}` +
		`]`)
	suggRaw := json.RawMessage(`{"pattern":{"id":99,"project":"alpha","shape_hash":"abc","first_cmd_example":"x","matched_cap":"reddit_fetch","count":7,"dismiss_count":0}}`)

	var lastSuggParams map[string]any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "get_active_caps":
			return capsRaw, nil
		case "get_repl_pattern_suggestion":
			lastSuggParams = params
			return suggRaw, nil
		}
		return nil, errors.New("unexpected: " + method)
	}

	req := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
	}
	if !injectCapabilitiesTurnImpl(req, "tid", "", queryFn, nil, nil) {
		t.Fatalf("inject returned false")
	}
	if lastSuggParams == nil {
		t.Fatalf("get_repl_pattern_suggestion not called")
	}
	if lastSuggParams["project"] != "alpha" {
		t.Errorf("expected project=alpha, got %v", lastSuggParams["project"])
	}
	activeCaps, _ := lastSuggParams["active_caps"].([]any)
	if len(activeCaps) != 1 || activeCaps[0] != "reddit_fetch" {
		t.Errorf("expected active_caps=[reddit_fetch] only, got %v", activeCaps)
	}
}

func TestInjectCapabilitiesTurnImpl_GlobalCapsIncludedInActiveCaps(t *testing.T) {
	capsRaw := json.RawMessage(`[` +
		`{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"alpha_tool","cap_description":"A","cap_scripts":[]}},` +
		`{"id":2,"project":"","source":"user_stated","meta":{"cap_name":"reddit","cap_description":"R","cap_scripts":[]}}` +
		`]`)
	suggRaw := json.RawMessage(`{"pattern":{"id":99,"project":"alpha","shape_hash":"abc","first_cmd_example":"x","matched_cap":"reddit","count":7,"dismiss_count":0}}`)

	var lastSuggParams map[string]any
	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "get_active_caps":
			return capsRaw, nil
		case "get_repl_pattern_suggestion":
			lastSuggParams = params
			return suggRaw, nil
		}
		return nil, errors.New("unexpected: " + method)
	}

	req := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "x"}},
	}
	if !injectCapabilitiesTurnImpl(req, "tid", "", queryFn, nil, nil) {
		t.Fatalf("inject returned false")
	}
	if lastSuggParams == nil {
		t.Fatalf("get_repl_pattern_suggestion not called")
	}
	if lastSuggParams["project"] != "alpha" {
		t.Errorf("expected project=alpha, got %v", lastSuggParams["project"])
	}
	activeCaps, _ := lastSuggParams["active_caps"].([]any)
	if len(activeCaps) != 2 {
		t.Fatalf("expected active_caps len=2 (alpha_tool + reddit global), got %d: %v", len(activeCaps), activeCaps)
	}
	hasAlpha, hasReddit := false, false
	for _, c := range activeCaps {
		switch c {
		case "alpha_tool":
			hasAlpha = true
		case "reddit":
			hasReddit = true
		}
	}
	if !hasAlpha || !hasReddit {
		t.Errorf("expected active_caps to contain {alpha_tool, reddit}, got %v", activeCaps)
	}
}

func TestInjectCapabilitiesTurnImpl_EmptyMatchedCapSkipsInject(t *testing.T) {
	capsRaw := json.RawMessage(`[{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"reddit_fetch","cap_description":"R","cap_scripts":[]}}]`)
	suggRaw := json.RawMessage(`{"pattern":{"id":99,"project":"alpha","shape_hash":"abc","first_cmd_example":"x","matched_cap":"","count":7,"dismiss_count":0}}`)

	queryFn := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "get_active_caps":
			return capsRaw, nil
		case "get_repl_pattern_suggestion":
			return suggRaw, nil
		}
		return nil, errors.New("unexpected: " + method)
	}

	req := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "untouched"}},
	}
	if !injectCapabilitiesTurnImpl(req, "tid", "", queryFn, nil, nil) {
		t.Fatalf("inject returned false")
	}
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["content"] != "untouched" {
		t.Errorf("expected last content untouched, got: %v", last["content"])
	}
	content, _ := last["content"].(string)
	if strings.Contains(content, "unknown") || strings.Contains(content, "<system-reminder>") {
		t.Errorf("empty matched_cap leaked into prompt: %s", content)
	}
}

func TestInjectCapabilitiesTurnImpl_CapsBlockUnchangedWithSuggestion(t *testing.T) {
	capsRaw := json.RawMessage(`[{"id":1,"project":"alpha","source":"user_stated","meta":{"cap_name":"reddit_fetch","cap_description":"R","cap_scripts":[]}}]`)
	suggRaw := json.RawMessage(`{"pattern":{"id":99,"project":"alpha","shape_hash":"abc","first_cmd_example":"x","matched_cap":"reddit_fetch","count":7,"dismiss_count":0}}`)

	makeQueryFn := func(suggestion json.RawMessage) func(string, map[string]any) (json.RawMessage, error) {
		return func(method string, params map[string]any) (json.RawMessage, error) {
			switch method {
			case "get_active_caps":
				return capsRaw, nil
			case "get_repl_pattern_suggestion":
				return suggestion, nil
			}
			return nil, errors.New("unexpected: " + method)
		}
	}

	mkReq := func() map[string]any {
		return map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": "do the thing"}},
		}
	}

	reqWith := mkReq()
	if !injectCapabilitiesTurnImpl(reqWith, "tid", "", makeQueryFn(suggRaw), nil, nil) {
		t.Fatalf("inject(with) returned false")
	}
	reqWithout := mkReq()
	if !injectCapabilitiesTurnImpl(reqWithout, "tid", "", makeQueryFn(json.RawMessage(`null`)), nil, nil) {
		t.Fatalf("inject(without) returned false")
	}

	withMsgs := reqWith["messages"].([]any)
	withoutMsgs := reqWithout["messages"].([]any)
	if len(withMsgs) != len(withoutMsgs) {
		t.Fatalf("message count differs: with=%d without=%d", len(withMsgs), len(withoutMsgs))
	}
	for i := 0; i < len(withMsgs)-1; i++ {
		w, _ := json.Marshal(withMsgs[i])
		wo, _ := json.Marshal(withoutMsgs[i])
		if string(w) != string(wo) {
			t.Errorf("caps-prefix message at index %d diverges:\n  with:    %s\n  without: %s", i, w, wo)
		}
	}
}
