package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestInjectCacheBreakpoints_ToolsAndSystem(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"name": "Bash", "description": "runs commands"},
			map[string]any{"name": "Read", "description": "reads files"},
		},
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code..."},
		},
	}

	n := InjectCacheBreakpoints(req)
	if n != 2 {
		t.Errorf("expected 2 breakpoints injected, got %d", n)
	}

	// Last tool should have cache_control
	tools := req["tools"].([]any)
	lastTool := tools[len(tools)-1].(map[string]any)
	cc, ok := lastTool["cache_control"]
	if !ok {
		t.Fatal("expected cache_control on last tool")
	}
	if cc.(map[string]any)["type"] != "ephemeral" {
		t.Errorf("expected type=ephemeral, got %v", cc)
	}
	if _, hasTTL := cc.(map[string]any)["ttl"]; hasTTL {
		t.Errorf("default ephemeral cache_control should not set ttl explicitly: %v", cc)
	}

	// First tool should NOT have cache_control
	firstTool := tools[0].(map[string]any)
	if _, ok := firstTool["cache_control"]; ok {
		t.Error("first tool should not have cache_control")
	}

	// Last system block should have cache_control
	system := req["system"].([]any)
	lastSys := system[len(system)-1].(map[string]any)
	cc2, ok := lastSys["cache_control"]
	if !ok {
		t.Fatal("expected cache_control on last system block")
	}
	if cc2.(map[string]any)["type"] != "ephemeral" {
		t.Error("expected type=ephemeral on system block")
	}
	if _, hasTTL := cc2.(map[string]any)["ttl"]; hasTTL {
		t.Errorf("default ephemeral cache_control should not set ttl explicitly: %v", cc2)
	}
}

func TestInjectCacheBreakpoints_NoTools(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}

	n := InjectCacheBreakpoints(req)
	if n != 0 {
		t.Errorf("expected 0 breakpoints, got %d", n)
	}

	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["cache_control"]; ok {
		t.Error("messages should not have cache_control")
	}
}

func TestInjectCacheBreakpoints_SystemString(t *testing.T) {
	req := map[string]any{
		"system": "You are Claude Code...",
		"tools": []any{
			map[string]any{"name": "Bash"},
		},
	}

	n := InjectCacheBreakpoints(req)
	if n != 2 {
		t.Errorf("expected 2 breakpoints, got %d", n)
	}

	system, ok := req["system"].([]any)
	if !ok {
		t.Fatal("expected system to be converted to array")
	}
	lastSys := system[len(system)-1].(map[string]any)
	if _, ok := lastSys["cache_control"]; !ok {
		t.Error("expected cache_control on converted system block")
	}
}

func TestInjectCacheBreakpoints_Idempotent(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"name": "Bash"},
			map[string]any{"name": "Read"},
		},
	}

	n1 := InjectCacheBreakpoints(req)
	if n1 != 1 {
		t.Errorf("first inject: expected 1, got %d", n1)
	}

	n2 := InjectCacheBreakpoints(req)
	if n2 != 0 {
		t.Errorf("second inject: expected 0 (already has breakpoint), got %d", n2)
	}

	data, _ := json.Marshal(req)
	var parsed map[string]any
	json.Unmarshal(data, &parsed)
	tools := parsed["tools"].([]any)
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
}

func TestInjectCacheBreakpoints_RespectsMaxLimit(t *testing.T) {
	// Simulate Claude Code already having 4 cache_control blocks
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "block1", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "block2", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "msg1", "cache_control": map[string]any{"type": "ephemeral"}},
				},
			},
			map[string]any{
				"role":          "assistant",
				"content":       "response",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"tools": []any{
			map[string]any{"name": "Bash"},
			map[string]any{"name": "Read"},
		},
	}

	n := InjectCacheBreakpoints(req)
	if n != 0 {
		t.Errorf("expected 0 injections when already at max (4), got %d", n)
	}

	// Verify tools were NOT modified
	tools := req["tools"].([]any)
	lastTool := tools[len(tools)-1].(map[string]any)
	if _, has := lastTool["cache_control"]; has {
		t.Error("should NOT inject cache_control when at max limit")
	}
}

func TestInjectCacheBreakpoints_PartialBudget(t *testing.T) {
	// 3 existing breakpoints — budget for 1 more
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "block1", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "block2", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "block3"},
		},
		"messages": []any{
			map[string]any{
				"role":          "user",
				"content":       "hello",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		"tools": []any{
			map[string]any{"name": "Bash"},
		},
	}

	n := InjectCacheBreakpoints(req)
	if n != 1 {
		t.Errorf("expected 1 injection (budget=1), got %d", n)
	}

	// System last block should get it (priority 1)
	system := req["system"].([]any)
	lastSys := system[len(system)-1].(map[string]any)
	if _, has := lastSys["cache_control"]; !has {
		t.Error("system last block should get the one available slot")
	}

	// Tools should NOT get it (budget exhausted)
	tools := req["tools"].([]any)
	lastTool := tools[len(tools)-1].(map[string]any)
	if _, has := lastTool["cache_control"]; has {
		t.Error("tools should NOT get cache_control when budget exhausted")
	}
}

func TestCountCacheBreakpoints(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "a", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "b"},
		},
		"tools": []any{
			map[string]any{"name": "Bash", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}

	count := countCacheBreakpoints(req)
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestCountCacheBreakpoints_Recursive(t *testing.T) {
	req := map[string]any{
		"metadata": map[string]any{
			"nested": []any{
				map[string]any{"cache_control": map[string]any{"type": "ephemeral"}},
			},
		},
	}

	count := countCacheBreakpoints(req)
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}
}

func TestEnforceCacheBreakpointLimit_TrimsBriefingFirst(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "system 1", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "system 2", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": "[yesmem-briefing]\nbrief", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "msg 1", "cache_control": map[string]any{"type": "ephemeral"}},
					map[string]any{"type": "text", "text": "msg 2", "cache_control": map[string]any{"type": "ephemeral"}},
				},
			},
		},
	}

	removed := EnforceCacheBreakpointLimit(req, 4)
	if removed != 1 {
		t.Fatalf("expected 1 trimmed block, got %d", removed)
	}
	if countCacheBreakpoints(req) != 4 {
		t.Fatalf("expected 4 remaining breakpoints, got %d", countCacheBreakpoints(req))
	}

	briefing := req["system"].([]any)[2].(map[string]any)
	if _, ok := briefing["cache_control"]; ok {
		t.Fatal("briefing cache_control should be trimmed first")
	}
}

func TestUpgradeCacheTTL_NormalizesAllBlocksRecursively(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"name": "Bash", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"system": []any{
			map[string]any{"type": "text", "text": "[yesmem-briefing]\nbrief", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "msg", "cache_control": map[string]any{"type": "ephemeral"}},
				},
			},
		},
	}

	n := UpgradeCacheTTL(req, "1h")
	if n != 3 {
		t.Fatalf("expected 3 upgraded blocks, got %d", n)
	}

	for _, holder := range collectCacheControlHolders(req) {
		cc := holder.holder["cache_control"].(map[string]any)
		if cc["ttl"] != "1h" {
			t.Fatalf("holder %s has wrong ttl: %#v", holder.path, cc)
		}
	}
}

// helper: message with a single content block
func msgWithContent(text string) map[string]any {
	return map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
	}
}

func TestInjectFrozenStubCacheBreakpoint_InjectsOnLastFrozenMsg(t *testing.T) {
	frozen1 := msgWithContent("stub 1")
	frozen2 := msgWithContent("stub 2")
	fresh := msgWithContent("fresh message")
	req := map[string]any{
		"messages": []any{frozen1, frozen2, fresh},
	}
	got := InjectFrozenStubCacheBreakpoint(req, 2)
	if !got {
		t.Fatal("want true, got false")
	}
	// frozen2 (index 1) must have cache_control
	content := frozen2["content"].([]any)
	lastBlock := content[len(content)-1].(map[string]any)
	if _, ok := lastBlock["cache_control"]; !ok {
		t.Error("expected cache_control on last content block of frozen2")
	}
	// fresh (index 2) must NOT have cache_control
	freshContent := fresh["content"].([]any)
	freshBlock := freshContent[len(freshContent)-1].(map[string]any)
	if _, ok := freshBlock["cache_control"]; ok {
		t.Error("expected no cache_control on fresh message")
	}
	// frozen1 (index 0) must NOT have cache_control
	f1Content := frozen1["content"].([]any)
	f1Block := f1Content[len(f1Content)-1].(map[string]any)
	if _, ok := f1Block["cache_control"]; ok {
		t.Error("expected no cache_control on frozen1")
	}
}

func TestInjectFrozenStubCacheBreakpoint_ZeroFrozenCount(t *testing.T) {
	req := map[string]any{"messages": []any{msgWithContent("foo")}}
	if got := InjectFrozenStubCacheBreakpoint(req, 0); got {
		t.Error("want false for frozenCount=0")
	}
}

func TestInjectFrozenStubCacheBreakpoint_NoopIfAlreadyHasBreakpoint(t *testing.T) {
	msg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{
				"type":          "text",
				"text":          "stub",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}
	req := map[string]any{"messages": []any{msg}}
	if got := InjectFrozenStubCacheBreakpoint(req, 1); got {
		t.Error("want false when cache_control already present")
	}
}

func TestIsAPIKeyAuth(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{
			name:   "API key via x-api-key header",
			header: http.Header{"X-Api-Key": {"sk-ant-test-key123"}},
			want:   true,
		},
		{
			name:   "OAuth via Authorization Bearer",
			header: http.Header{"Authorization": {"Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"}},
			want:   false,
		},
		{
			name:   "no auth headers at all",
			header: http.Header{},
			want:   false,
		},
		{
			name:   "both headers present — API key wins",
			header: http.Header{"X-Api-Key": {"sk-ant-test-key123"}, "Authorization": {"Bearer eyJ"}},
			want:   true,
		},
		{
			name:   "empty x-api-key value",
			header: http.Header{"X-Api-Key": {""}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAPIKeyAuth(tt.header)
			if got != tt.want {
				t.Errorf("IsAPIKeyAuth() = %v, want %v", got, tt.want)
			}
		})
	}
}
