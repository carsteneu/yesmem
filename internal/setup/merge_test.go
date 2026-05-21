package setup

import (
	"encoding/json"
	"testing"
)

func TestDeepMergeJSON_FlatMerge(t *testing.T) {
	target := map[string]any{"a": 1, "b": "hello"}
	source := map[string]any{"b": "world", "c": "new"}

	deepMergeJSON(target, source)

	if target["a"] != 1 {
		t.Errorf("existing key 'a' changed: got %v", target["a"])
	}
	if target["b"] != "hello" {
		t.Errorf("existing key 'b' was overwritten: got %v", target["b"])
	}
	if target["c"] != "new" {
		t.Errorf("new key 'c' not added: got %v", target["c"])
	}
	if len(target) != 3 {
		t.Errorf("expected 3 keys, got %d: %v", len(target), target)
	}
}

func TestDeepMergeJSON_NestedMerge(t *testing.T) {
	target := map[string]any{
		"provider": map[string]any{
			"deepseek": map[string]any{
				"options": map[string]any{
					"baseURL": "https://custom.example.com",
				},
			},
		},
	}
	source := map[string]any{
		"provider": map[string]any{
			"deepseek": map[string]any{
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
					"timeout": 30000,
				},
				"models": map[string]any{
					"deepseek-chat": "chat-model",
				},
			},
			"openai": map[string]any{
				"options": map[string]any{
					"baseURL": "http://localhost:9099/v1",
				},
			},
		},
	}

	deepMergeJSON(target, source)

	// Existing deepseek.baseURL should NOT be overwritten
	dsOpts := target["provider"].(map[string]any)["deepseek"].(map[string]any)["options"].(map[string]any)
	if dsOpts["baseURL"] != "https://custom.example.com" {
		t.Errorf("existing deepseek baseURL was overwritten: %v", dsOpts["baseURL"])
	}
	// New deepseek.timeout should be added
	if dsOpts["timeout"] != 30000 {
		t.Errorf("new deepseek timeout not added: %v", dsOpts["timeout"])
	}
	// New deepseek.models should be added
	ds := target["provider"].(map[string]any)["deepseek"].(map[string]any)
	if _, ok := ds["models"]; !ok {
		t.Error("new deepseek.models not added")
	}
	// New openai provider should be added
	if _, ok := target["provider"].(map[string]any)["openai"]; !ok {
		t.Error("new openai provider not added")
	}
}

func TestDeepMergeJSON_DeeplyNested(t *testing.T) {
	target := map[string]any{}
	source := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": map[string]any{
					"d": "deep",
				},
			},
		},
	}

	deepMergeJSON(target, source)

	a := target["a"].(map[string]any)
	b := a["b"].(map[string]any)
	c := b["c"].(map[string]any)
	if c["d"] != "deep" {
		t.Errorf("deeply nested value wrong: %v", c["d"])
	}
}

func TestDeepMergeJSON_ExistingDeepNestedPreserved(t *testing.T) {
	target := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "original",
			},
		},
	}
	source := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "new",
				"d": "added",
			},
		},
	}

	deepMergeJSON(target, source)

	ab := target["a"].(map[string]any)["b"].(map[string]any)
	if ab["c"] != "original" {
		t.Errorf("existing deeply nested key was overwritten: %v", ab["c"])
	}
	if ab["d"] != "added" {
		t.Errorf("new deeply nested key not added: %v", ab["d"])
	}
}

func TestDeepMergeJSON_TargetNil(t *testing.T) {
	target := make(map[string]any)
	source := map[string]any{"key": "value"}

	deepMergeJSON(target, source)

	if target["key"] != "value" {
		t.Errorf("nil target merge failed")
	}
}

func TestDeepMergeJSON_SourceNil(t *testing.T) {
	target := map[string]any{"key": "value"}
	deepMergeJSON(target, nil)

	if len(target) != 1 || target["key"] != "value" {
		t.Errorf("nil source changed target: %v", target)
	}
}

func TestDeepMergeJSON_TargetScalarNotOverwritten(t *testing.T) {
	target := map[string]any{"key": 42}
	source := map[string]any{"key": 99}

	deepMergeJSON(target, source)

	if target["key"] != 42 {
		t.Errorf("scalar was overwritten: %v", target["key"])
	}
}

func TestDeepMergeJSON_ArrayNotOverwritten(t *testing.T) {
	target := map[string]any{"arr": []any{1, 2, 3}}
	source := map[string]any{"arr": []any{4, 5}}

	deepMergeJSON(target, source)

	arr, ok := target["arr"].([]any)
	if !ok || len(arr) != 3 || arr[0] != 1 {
		t.Errorf("array was overwritten: %v", target["arr"])
	}
}

func TestDeepMergeJSON_PreservesOpenCodeScenario(t *testing.T) {
	existingJSON := `{
		"$schema": "https://opencode.ai/config.json",
		"plugin": ["/custom/plugin.ts"],
		"provider": {
			"deepseek": {
				"options": {
					"baseURL": "https://my-proxy.example.com/v1"
				}
			}
		},
		"mcp": {
			"custom_server": {
				"type": "local",
				"command": ["custom", "server"]
			}
		}
	}`

	var existing map[string]any
	if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
		t.Fatal(err)
	}

	sourceJSON := `{
		"provider": {
			"deepseek": {
				"options": {
					"baseURL": "http://localhost:9099/v1"
				},
				"models": {
					"deepseek-chat": {
						"name": "DeepSeek V4 Flash",
						"limit": { "context": 1000000, "output": 8192 }
					}
				}
			},
			"openai": {
				"options": {
					"baseURL": "http://localhost:9099/v1"
				}
			}
		},
		"mcp": {
			"yesmem": {
				"type": "local",
				"command": ["yesmem", "mcp"],
				"enabled": true,
				"timeout": 30000
			}
		},
		"compaction": {
			"auto": false
		}
	}`

	var source map[string]any
	if err := json.Unmarshal([]byte(sourceJSON), &source); err != nil {
		t.Fatal(err)
	}

	deepMergeJSON(existing, source)

	// Verify existing custom plugin is preserved
	plugins := existing["plugin"].([]any)
	found := false
	for _, p := range plugins {
		if p == "/custom/plugin.ts" {
			found = true
		}
	}
	if !found {
		t.Error("existing plugin entry was lost")
	}

	// Verify existing deepseek baseURL is NOT overwritten
	ds := existing["provider"].(map[string]any)["deepseek"].(map[string]any)
	dsOpts := ds["options"].(map[string]any)
	if dsOpts["baseURL"] != "https://my-proxy.example.com/v1" {
		t.Errorf("existing deepseek baseURL was overwritten: %v", dsOpts["baseURL"])
	}

	// Verify deepseek.models were added
	if ds["models"] == nil {
		t.Error("deepseek.models not added")
	}

	// Verify openai provider was added
	pMap := existing["provider"].(map[string]any)
	if pMap["openai"] == nil {
		t.Error("openai provider not added")
	}

	// Verify yesmem MCP was added but custom_server preserved
	mcpMap := existing["mcp"].(map[string]any)
	if mcpMap["yesmem"] == nil {
		t.Error("yesmem MCP not added")
	}
	if mcpMap["custom_server"] == nil {
		t.Error("custom_server MCP lost")
	}

	// Verify compaction was added
	if existing["compaction"] == nil {
		t.Error("compaction not added")
	}
	compaction := existing["compaction"].(map[string]any)
	if compaction["auto"] != false {
		t.Errorf("compaction.auto wrong: %v", compaction["auto"])
	}
}
