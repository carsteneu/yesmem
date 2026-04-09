package extraction

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeOpenAIResponsesURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", defaultOpenAIResponsesURL},
		{"https://example.test", "https://example.test/v1/responses"},
		{"https://example.test/", "https://example.test/v1/responses"},
		{"https://example.test/v1", "https://example.test/v1/responses"},
		{"https://example.test/v1/responses", "https://example.test/v1/responses"},
	}

	for _, tt := range tests {
		if got := normalizeOpenAIResponsesURL(tt.in); got != tt.want {
			t.Errorf("normalizeOpenAIResponsesURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOpenAIClientCompleteJSON(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"{\"ok\":true}","usage":{"input_tokens":12,"output_tokens":7}}`))
	}))
	defer srv.Close()

	oldOnUsage := OnUsage
	defer func() { OnUsage = oldOnUsage }()

	var usageModel string
	var usageIn, usageOut int
	OnUsage = func(model string, inputTokens, outputTokens int) {
		usageModel = model
		usageIn = inputTokens
		usageOut = outputTokens
	}

	client := NewOpenAIClient("sk-openai", "gpt-5-mini", srv.URL, "openai_compatible")
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
		"required":             []string{"ok"},
		"additionalProperties": false,
	}

	out, err := client.CompleteJSON("system prompt", "user prompt", schema, WithMaxTokens(321))
	if err != nil {
		t.Fatalf("CompleteJSON() error = %v", err)
	}
	if out != `{"ok":true}` {
		t.Fatalf("CompleteJSON() = %q", out)
	}
	if gotAuth != "Bearer sk-openai" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotBody["model"] != "gpt-5-mini" {
		t.Fatalf("model = %#v", gotBody["model"])
	}
	if gotBody["instructions"] != "system prompt" {
		t.Fatalf("instructions = %#v", gotBody["instructions"])
	}
	if gotBody["input"] != "user prompt" {
		t.Fatalf("input = %#v", gotBody["input"])
	}
	if gotBody["max_output_tokens"] != float64(321) {
		t.Fatalf("max_output_tokens = %#v", gotBody["max_output_tokens"])
	}
	if gotBody["store"] != false {
		t.Fatalf("store = %#v, want false", gotBody["store"])
	}

	textCfg, ok := gotBody["text"].(map[string]any)
	if !ok {
		t.Fatalf("text config missing: %#v", gotBody["text"])
	}
	format, ok := textCfg["format"].(map[string]any)
	if !ok {
		t.Fatalf("format missing: %#v", textCfg["format"])
	}
	if format["type"] != "json_schema" {
		t.Fatalf("format.type = %#v", format["type"])
	}
	if format["name"] != "yesmem_output" {
		t.Fatalf("format.name = %#v", format["name"])
	}
	if format["strict"] != true {
		t.Fatalf("format.strict = %#v", format["strict"])
	}

	if usageModel != "gpt-5-mini" || usageIn != 12 || usageOut != 7 {
		t.Fatalf("usage callback = (%q, %d, %d)", usageModel, usageIn, usageOut)
	}
}

func TestOpenAIClientFallsBackToOutputParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output_text":"",
			"output":[
				{"type":"message","content":[
					{"type":"output_text","text":"first"},
					{"type":"output_text","text":"second"}
				]}
			]
		}`))
	}))
	defer srv.Close()

	client := NewOpenAIClient("sk-openai", "gpt-5-mini", srv.URL, "openai")
	out, err := client.Complete("system", "user")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if out != "first\nsecond" {
		t.Fatalf("Complete() = %q, want %q", out, "first\nsecond")
	}
}

func TestPricingForGPTModels(t *testing.T) {
	tests := []struct {
		model      string
		inputPerM  float64
		outputPerM float64
	}{
		{"gpt-5-mini", 0.25, 2.0},
		{"gpt-5.2", 1.75, 14.0},
		{"gpt-5.2-codex", 1.75, 14.0},
		{"gpt-5.4", 2.5, 15.0},
	}

	for _, tt := range tests {
		in, out := PricingForModel(tt.model)
		if in != tt.inputPerM || out != tt.outputPerM {
			t.Errorf("PricingForModel(%q) = (%v, %v), want (%v, %v)", tt.model, in, out, tt.inputPerM, tt.outputPerM)
		}
	}
}
