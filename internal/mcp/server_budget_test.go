package mcp

import (
	"encoding/json"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Token budget: 31000 chars in Anthropic wire format ≈ 8950 tokens.
// Raised from 30000 after bilingual query_en addition on search + deep_search
// (mixed-language sessions require both lanes to be described in the schema).
// Earlier raise: 27000 → 30000 for Cap-Spec v1.1 migration (save_cap scripts JSON array).
const maxToolDefChars = 60000

func TestToolDefinitionBudget(t *testing.T) {
	srv := &Server{}
	srv.srv = mcpserver.NewMCPServer("test", "0.0.0")
	srv.registerTools()

	tools := srv.srv.ListTools()

	totalChars := 0
	for _, st := range tools {
		anthropicTool := map[string]any{
			"name":         "mcp__yesmem__" + st.Tool.Name,
			"description":  st.Tool.Description,
			"input_schema": st.Tool.InputSchema,
		}
		data, err := json.Marshal(anthropicTool)
		if err != nil {
			t.Fatalf("marshal tool %s: %v", st.Tool.Name, err)
		}
		totalChars += len(data)
	}

	t.Logf("Total tool definition size: %d chars (%d tools), budget: %d", totalChars, len(tools), maxToolDefChars)

	if totalChars > maxToolDefChars {
		t.Errorf("Tool definitions exceed budget: %d > %d chars (over by %d)", totalChars, maxToolDefChars, totalChars-maxToolDefChars)
	}
}

func TestToolCount(t *testing.T) {
	srv := &Server{}
	srv.srv = mcpserver.NewMCPServer("test", "0.0.0")
	srv.registerTools()

	tools := srv.srv.ListTools()
	t.Logf("Tool count: %d", len(tools))

	// Cap raised 70 → 75 after adding scratchpad_append (previously implemented
	// in the daemon but missing from MCP registration). 5 slots of headroom.
	if len(tools) > 75 {
		t.Errorf("Too many tools: %d > 75 — consider consolidation", len(tools))
	}
}
