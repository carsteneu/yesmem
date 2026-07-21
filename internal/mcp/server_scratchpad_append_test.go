package mcp

import (
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// TestScratchpadAppend_Registered verifies that the scratchpad_append tool
// (implemented in the daemon layer but historically missing from MCP
// registration) is exposed to MCP clients with the required parameters and
// the content-language directive.
func TestScratchpadAppend_Registered(t *testing.T) {
	srv := &Server{}
	srv.srv = mcpserver.NewMCPServer("test", "0.0.0")
	srv.registerTools()

	tools := srv.srv.ListTools()
	var found bool
	for _, st := range tools {
		if st.Tool.Name != "scratchpad_append" {
			continue
		}
		found = true

		props := st.Tool.InputSchema.Properties
		required := st.Tool.InputSchema.Required

		for _, p := range []string{"project", "section", "content"} {
			if _, ok := props[p]; !ok {
				t.Errorf("scratchpad_append missing required param %q", p)
			}
		}
		for _, p := range []string{"project", "section", "content"} {
			if !contains(required, p) {
				t.Errorf("scratchpad_append param %q must be in Required list", p)
			}
		}

		if !strings.Contains(st.Tool.Description, wantLangPhrase) {
			t.Errorf("scratchpad_append description missing language directive %q", wantLangPhrase)
		}
	}
	if !found {
		t.Fatal("scratchpad_append tool not registered — daemon handler exists but MCP layer does not expose it")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
