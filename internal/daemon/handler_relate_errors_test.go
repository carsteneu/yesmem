package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// relate_learnings answered "ok" to everything it could store: a learning related to
// itself, an ID that names nothing, and a second call that silently replaced the type
// of an existing edge. Each of those is a mistake the caller can see at the moment it
// makes it, so the answer names it there — that is the place a rule belongs, not a
// paragraph in the tool description.
func TestHandleRelateRefusesASelfLink(t *testing.T) {
	h, _ := mustHandler(t)
	id := relateTestLearning(t, h, "the daemon retries a socket bind after a restart")

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(id),
			"learning_id_b": float64(id),
			"relation_type": "relates_to",
		},
	})

	if resp.Error == "" {
		t.Fatal("a learning related to itself was accepted")
	}
	if !strings.Contains(resp.Error, "itself") {
		t.Errorf("error = %q, want it to say the learning cannot relate to itself", resp.Error)
	}
}

func TestHandleRelateNamesAnUnknownID(t *testing.T) {
	h, _ := mustHandler(t)
	id := relateTestLearning(t, h, "coverage counts edited files per project")

	resp := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(id),
			"learning_id_b": float64(999999),
			"relation_type": "supports",
		},
	})

	if resp.Error == "" {
		t.Fatal("an edge to a learning that does not exist was accepted")
	}
	if !strings.Contains(resp.Error, "999999") {
		t.Errorf("error = %q, want it to name the id that does not exist", resp.Error)
	}
	if !strings.Contains(resp.Error, "hybrid_search") {
		t.Errorf("error = %q, want it to say how to find the right id", resp.Error)
	}
}

// A pair carries one type, and a second call replaces the first. Replacing is
// legitimate, doing it without a word is not: the caller believes it added a relation
// where it removed one.
func TestHandleRelateReportsAReplacedType(t *testing.T) {
	h, _ := mustHandler(t)
	a := relateTestLearning(t, h, "the worker pool drains before shutdown")
	b := relateTestLearning(t, h, "sandbox denies network egress to the checkout")

	first := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(a), "learning_id_b": float64(b), "relation_type": "supports",
		},
	})
	if first.Error != "" {
		t.Fatalf("first relate: %s", first.Error)
	}

	second := h.Handle(Request{
		Method: "relate_learnings",
		Params: map[string]any{
			"learning_id_a": float64(a), "learning_id_b": float64(b), "relation_type": "relates_to",
		},
	})
	if second.Error != "" {
		t.Fatalf("second relate: %s", second.Error)
	}

	var out map[string]any
	if err := json.Unmarshal(second.Result, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["replaced"] != "supports" {
		t.Errorf("replaced = %v, want %v — a replaced type is invisible to the caller otherwise",
			out["replaced"], "supports")
	}
	if out["relation_type"] != "relates_to" {
		t.Errorf("relation_type = %v, want %v", out["relation_type"], "relates_to")
	}
}

// relateTestLearning stores a learning and returns its id.
//
// remember dedupes by token similarity (Jaccard >= 0.5) before it inserts, so a text
// that shares most of its tokens with a stored one returns that row's id instead of a
// new one. A test that relates two such texts would relate an id to itself — which is
// how the two older relate tests ended up asserting nothing.
func relateTestLearning(t *testing.T, h *Handler, text string) int64 {
	t.Helper()
	resp := h.Handle(Request{
		Method: "remember",
		Params: map[string]any{"text": text, "category": "pattern", "project": "yesmem"},
	})
	if resp.Error != "" {
		t.Fatalf("remember %q: %s", text, resp.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode remember: %v", err)
	}
	if deduplicated, _ := out["deduplicated"].(bool); deduplicated {
		t.Fatalf("remember deduplicated %q into #%v; give the learnings different wording", text, out["id"])
	}
	id, ok := out["id"].(float64)
	if !ok {
		t.Fatalf("remember %q answered without an id: %s", text, string(resp.Result))
	}
	return int64(id)
}
