package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripProviderPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"opencode/big-pickle", "big-pickle"},
		{"deepseek/deepseek-chat", "deepseek-chat"},
		{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"openai/gpt-4o", "gpt-4o"},
		{"big-pickle", "big-pickle"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"", ""},
		{"a/b/c", "c"},
		{"provider/", ""},
		{"/leading-slash", "leading-slash"},
	}
	for _, c := range cases {
		got := stripProviderPrefix(c.in)
		if got != c.want {
			t.Errorf("stripProviderPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRewriteModelInBody_RewritesPrefixedModel(t *testing.T) {
	body := []byte(`{"model":"opencode/big-pickle","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`)
	out := rewriteModelInBody(body, "big-pickle")

	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if req["model"] != "big-pickle" {
		t.Errorf("model = %q, want %q", req["model"], "big-pickle")
	}
	if _, ok := req["messages"]; !ok {
		t.Error("messages field was dropped")
	}
	if _, ok := req["max_tokens"]; !ok {
		t.Error("max_tokens field was dropped")
	}
}

func TestRewriteModelInBody_PreservesBareModel(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteModelInBody(body, "big-pickle")

	if string(out) == string(body) {
		t.Errorf("rewriteModelInBody returned input unchanged; re-serialization expected to produce equivalent JSON")
	}

	var req map[string]any
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if req["model"] != "big-pickle" {
		t.Errorf("model = %q, want %q", req["model"], "big-pickle")
	}
}

func TestRewriteModelInBody_HandlesMalformedJSON(t *testing.T) {
	body := []byte(`{this is not valid json}`)
	out := rewriteModelInBody(body, "anything")

	if string(out) != string(body) {
		t.Errorf("malformed JSON should return input unchanged; got %q, want %q", out, body)
	}
}

func TestRewriteModelInBody_EmptyBody(t *testing.T) {
	body := []byte(``)
	out := rewriteModelInBody(body, "model")

	if string(out) != string(body) {
		t.Errorf("empty body should return input unchanged; got %q", out)
	}
}

func TestRewriteModelInBody_Idempotent(t *testing.T) {
	body := []byte(`{"model":"opencode/big-pickle"}`)
	first := rewriteModelInBody(body, "big-pickle")
	second := rewriteModelInBody(first, "big-pickle")

	var f, s map[string]any
	json.Unmarshal(first, &f)
	json.Unmarshal(second, &s)
	if f["model"] != s["model"] {
		t.Errorf("rewrite not idempotent: first model = %q, second = %q", f["model"], s["model"])
	}
}

func TestRewriteModelInBody_PreservesLargeIntegers(t *testing.T) {
	body := []byte(`{"model":"opencode/big-pickle","seed":12345678901234567,"max_tokens":5}`)
	out := rewriteModelInBody(body, "big-pickle")

	var req map[string]any
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	seed, ok := req["seed"].(json.Number)
	if !ok {
		t.Fatalf("seed is not json.Number, got %T", req["seed"])
	}
	if seed.String() != "12345678901234567" {
		t.Errorf("seed precision lost: got %q, want %q", seed.String(), "12345678901234567")
	}
}

func TestRewriteModelInBody_HasContentLength(t *testing.T) {
	body := []byte(`{"model":"opencode/big-pickle","content":"hello world"}`)
	out := rewriteModelInBody(body, "big-pickle")

	trimmed := strings.TrimSpace(string(out))
	if len(out) != len(trimmed) {
		t.Errorf("expected no leading/trailing whitespace; len=%d trimmed=%d", len(out), len(trimmed))
	}
}
