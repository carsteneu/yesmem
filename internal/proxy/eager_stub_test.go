package proxy

import (
	"fmt"
	"strings"
	"testing"
)

func simpleEstimate(s string) int { return len(s) / 4 }

func TestEagerStub_ReadResult(t *testing.T) {
	code := "package proxy\n\nfunc handleMessages() error {\n\treturn nil\n}\n\nfunc forward() error {\n\treturn nil\n}\n"
	code += strings.Repeat("// padding\n", 200)

	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "internal/proxy/proxy.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": code},
		}},
		map[string]any{"role": "assistant", "content": "The file has two functions."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)

	msg := result[1].(map[string]any)
	blocks := msg["content"].([]any)
	block := blocks[0].(map[string]any)

	if block["type"] != "tool_result" {
		t.Errorf("type must stay tool_result, got %v", block["type"])
	}
	if block["tool_use_id"] != "t1" {
		t.Errorf("tool_use_id must be preserved, got %v", block["tool_use_id"])
	}

	stub := block["content"].(string)
	if !strings.Contains(stub, "proxy.go") {
		t.Errorf("stub must contain file path, got: %s", stub)
	}
	if !strings.Contains(stub, "handleMessages") {
		t.Errorf("stub must contain function name, got: %s", stub)
	}
	if len(stub) > 500 {
		t.Errorf("stub must be compact, got %d chars", len(stub))
	}
}

func TestEagerStub_SkipSmall(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "small.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "package main"},
		}},
		map[string]any{"role": "assistant", "content": "Tiny file."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)

	block := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["content"] != "package main" {
		t.Error("small tool_result must not be stubbed")
	}
}

func TestEagerStub_SkipNoFollowingAssistant(t *testing.T) {
	big := strings.Repeat("code\n", 500)
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "big.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": big},
		}},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)

	block := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["content"] != big {
		t.Error("tool_result without following assistant must not be stubbed")
	}
}

func TestEagerStub_SkipFrozenPrefix(t *testing.T) {
	big := strings.Repeat("frozen\n", 500)
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "old.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": big},
		}},
		map[string]any{"role": "assistant", "content": "Old analysis."},
	}

	result := EagerStubToolResults(messages, 3, simpleEstimate)

	block := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if block["content"] != big {
		t.Error("frozen tool_result must not be stubbed")
	}
}

func TestEagerStub_GrepResult(t *testing.T) {
	grep := strings.Repeat("file.go:42: match here\n", 100)
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Grep",
				"input": map[string]any{"pattern": "handleMessages", "path": "internal/proxy/"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": grep},
		}},
		map[string]any{"role": "assistant", "content": "Found in several files."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)
	stub := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)

	if !strings.Contains(stub, "handleMessages") {
		t.Errorf("grep stub must contain pattern, got: %s", stub)
	}
	if !strings.Contains(stub, "101") {
		t.Errorf("grep stub must contain match count, got: %s", stub)
	}
	if len(stub) > 500 {
		t.Errorf("grep stub too large: %d chars", len(stub))
	}
}

func TestEagerStub_BashResult(t *testing.T) {
	bash := "Building...\n" + strings.Repeat("compiling pkg\n", 200) + "Build OK.\n"
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash",
				"input": map[string]any{"command": "go build ./..."}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": bash},
		}},
		map[string]any{"role": "assistant", "content": "Build succeeded."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)
	stub := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)

	if !strings.Contains(stub, "go build") {
		t.Errorf("bash stub must contain command, got: %s", stub)
	}
	if !strings.Contains(stub, "Building") {
		t.Errorf("bash stub must contain head lines, got: %s", stub)
	}
	if !strings.Contains(stub, "Build OK") {
		t.Errorf("bash stub must contain tail lines, got: %s", stub)
	}
}

func TestEagerStub_GlobResult(t *testing.T) {
	var paths []string
	for i := 0; i < 100; i++ {
		paths = append(paths, fmt.Sprintf("internal/proxy/file%d.go", i))
	}
	glob := strings.Join(paths, "\n")
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Glob",
				"input": map[string]any{"pattern": "**/*.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": glob},
		}},
		map[string]any{"role": "assistant", "content": "Found 50 files."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)
	stub := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)

	if !strings.Contains(stub, "**/*.go") {
		t.Errorf("glob stub must contain pattern, got: %s", stub)
	}
	if !strings.Contains(stub, "100") {
		t.Errorf("glob stub must contain count, got: %s", stub)
	}
}

func TestEagerStub_MultipleResults(t *testing.T) {
	big1 := strings.Repeat("a\n", 1500)
	big2 := strings.Repeat("b\n", 1500)
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "a.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": big1},
		}},
		map[string]any{"role": "assistant", "content": "Analyzed a.go"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t2", "name": "Read",
				"input": map[string]any{"file_path": "b.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": big2},
		}},
		map[string]any{"role": "assistant", "content": "Analyzed b.go"},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)

	for _, idx := range []int{1, 4} {
		stub := result[idx].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)
		if len(stub) > 500 {
			t.Errorf("tool_result at %d should be stubbed, got %d chars", idx, len(stub))
		}
	}
}

func TestEagerStub_PreservesLength(t *testing.T) {
	big := strings.Repeat("x\n", 500)
	messages := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Read",
				"input": map[string]any{"file_path": "f.go"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": big},
		}},
		map[string]any{"role": "assistant", "content": "Done."},
		map[string]any{"role": "user", "content": "thanks"},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)

	if len(result) != 5 {
		t.Errorf("message count must not change: got %d, want 5", len(result))
	}
	if result[0].(map[string]any)["content"] != "start" {
		t.Error("non-tool messages must be unchanged")
	}
}

func TestEagerStub_UnknownTool(t *testing.T) {
	big := strings.Repeat("data\n", 500)
	messages := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "WebSearch",
				"input": map[string]any{"query": "golang proxy"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": big},
		}},
		map[string]any{"role": "assistant", "content": "Found results."},
	}

	result := EagerStubToolResults(messages, 0, simpleEstimate)
	stub := result[1].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].(string)

	if !strings.Contains(stub, "WebSearch") {
		t.Errorf("unknown tool stub must contain tool name, got: %s", stub)
	}
	if !strings.Contains(stub, "archived") {
		t.Errorf("unknown tool stub must say archived, got: %s", stub)
	}
}
