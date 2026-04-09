package mcp

import (
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func TestWithMaxResultSize_SetsMetaField(t *testing.T) {
	result := mcplib.NewToolResultText("test content")
	withMaxResultSize(result, 500000)

	if result.Meta == nil {
		t.Fatal("Meta should not be nil")
	}
	val, ok := result.Meta.AdditionalFields["anthropic/maxResultSizeChars"]
	if !ok {
		t.Fatal("anthropic/maxResultSizeChars not set")
	}
	if val != 500000 {
		t.Fatalf("expected 500000, got %v", val)
	}
}

func TestWithMaxResultSize_PreservesContent(t *testing.T) {
	result := mcplib.NewToolResultText("hello world")
	withMaxResultSize(result, 100000)

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// Verify _meta is present in serialized output
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	meta, ok := raw["_meta"].(map[string]any)
	if !ok {
		t.Fatal("_meta not found in serialized JSON")
	}
	if meta["anthropic/maxResultSizeChars"] != float64(100000) {
		t.Fatalf("expected 100000 in JSON, got %v", meta["anthropic/maxResultSizeChars"])
	}
}
