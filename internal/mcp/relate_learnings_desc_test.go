package mcp

import (
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// The relate_learnings description named the four types and nothing else, so callers
// kept getting the semantics wrong. Two of them are unreadable from the edge itself and
// belong in the text: the direction (the target surfaces when the source is a hit, not
// the other way round) and the ranking order between the types. The mistakes a caller
// can see at the moment it makes them — a self-link, an unknown id, a replaced type —
// are answered where they happen, not here (TestHandleRelate* in internal/daemon).
func TestRelateLearningsDescriptionCarriesTheSemantics(t *testing.T) {
	srv := &Server{}
	srv.srv = mcpserver.NewMCPServer("test", "0.0.0")
	srv.registerTools()

	for _, st := range srv.srv.ListTools() {
		if st.Tool.Name != "relate_learnings" {
			continue
		}
		// Tool text and parameter descriptions together: a caller reads both, and the
		// direction of the edge belongs next to the parameter that sets it.
		desc := st.Tool.Description
		for _, prop := range st.Tool.InputSchema.Properties {
			if schema, ok := prop.(map[string]any); ok {
				if paramDesc, ok := schema["description"].(string); ok {
					desc += "\n" + paramDesc
				}
			}
		}
		for _, want := range []struct{ phrase, why string }{
			{"source", "the direction: the edge points away from learning_id_a"},
			{"weight", "a repeated call only adds weight"},
			{"one type", "a pair carries one type"},
			{"depends_on", "the type that promotes strongest"},
			{"relates_to", "the type the extraction already adds by itself"},
		} {
			if !strings.Contains(desc, want.phrase) {
				t.Errorf("the description does not say %s (missing %q):\n%s", want.why, want.phrase, desc)
			}
		}
		// The four types are the only accepted values; a caller that guesses gets an
		// error instead of an edge.
		for _, typ := range []string{"supports", "contradicts", "depends_on", "relates_to"} {
			if !strings.Contains(desc, typ) {
				t.Errorf("the description does not name the type %q:\n%s", typ, desc)
			}
		}
		return
	}
	t.Fatal("relate_learnings is not registered")
}
