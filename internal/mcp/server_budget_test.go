package mcp

import (
	"encoding/json"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Token budget: 19000 chars in Anthropic wire format ≈ 5400 tokens.
// Baseline before slimdown: 24863 chars ≈ 7100 tokens.
const maxToolDefChars = 19000

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

	if len(tools) > 55 {
		t.Errorf("Too many tools: %d > 55 — consider consolidation", len(tools))
	}
}
