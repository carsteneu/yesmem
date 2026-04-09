package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSearchResults_WrappedFormat(t *testing.T) {
	raw := json.RawMessage(`{"results":[{"content":"learning one","snippet":"snip one","source":"hybrid"},{"content":"learning two","snippet":"snip two","source":"semantic"}]}`)
	results := parseSearchResults(raw)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0] != "learning one" {
		t.Errorf("expected 'learning one', got %q", results[0])
	}
}

func TestParseSearchResults_DirectArray(t *testing.T) {
	raw := json.RawMessage(`[{"content":"result one"},{"snippet":"snip two"}]`)
	results := parseSearchResults(raw)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0] != "result one" {
		t.Errorf("expected 'result one', got %q", results[0])
	}
	if results[1] != "snip two" {
		t.Errorf("expected 'snip two', got %q", results[1])
	}
}

func TestParseSearchResults_Empty(t *testing.T) {
	raw := json.RawMessage(`[]`)
	results := parseSearchResults(raw)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestInjectAssociativeContext(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "do something"},
	}

	// Legacy path (sawtooth disabled): inserts separate messages
	result := injectAssociativeContext(msgs, "[context]", false)
	if len(result) != 5 {
		t.Fatalf("legacy: expected 5 messages, got %d", len(result))
	}

	// Context should be before last user message
	ctx, _ := result[2].(map[string]any)
	if ctx["role"] != "user" || ctx["content"] != "[context]" {
		t.Errorf("context message wrong: %v", ctx)
	}
	ack, _ := result[3].(map[string]any)
	if ack["role"] != "assistant" {
		t.Errorf("ack message wrong: %v", ack)
	}
	last, _ := result[4].(map[string]any)
	if last["content"] != "do something" {
		t.Errorf("last user message moved: %v", last)
	}
}

func TestInjectAssociativeContext_Sawtooth(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "do something"},
	}

	// Sawtooth path: appends to last user message as content block
	result := injectAssociativeContext(msgs, "[context]", true)
	if len(result) != 3 {
		t.Fatalf("sawtooth: expected 3 messages (no extra), got %d", len(result))
	}

	// Last user message should now have content blocks
	last, _ := result[2].(map[string]any)
	blocks, ok := last["content"].([]any)
	if !ok {
		t.Fatalf("sawtooth: last user content should be []any, got %T", last["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("sawtooth: expected 2 content blocks, got %d", len(blocks))
	}

	// First block: original text
	b0, _ := blocks[0].(map[string]any)
	if b0["text"] != "do something" {
		t.Errorf("sawtooth: first block should be original text, got %v", b0)
	}

	// Second block: injected context
	b1, _ := blocks[1].(map[string]any)
	if b1["type"] != "text" {
		t.Errorf("sawtooth: second block type should be text, got %v", b1["type"])
	}
	txt, _ := b1["text"].(string)
	if !strings.Contains(txt, "[context]") {
		t.Errorf("sawtooth: second block should contain context, got %q", txt)
	}

	// Original messages should be unmodified
	origLast, _ := msgs[2].(map[string]any)
	if _, isStr := origLast["content"].(string); !isStr {
		t.Errorf("sawtooth: original message should still have string content")
	}
}

func TestInjectAssociativeContext_Sawtooth_BlockContent(t *testing.T) {
	// User message already has block content (e.g., image + text)
	msgs := []any{
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look at this"},
			map[string]any{"type": "image", "source": "data:..."},
		}},
	}

	result := injectAssociativeContext(msgs, "[context]", true)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	last, _ := result[1].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (text + image + context), got %d", len(blocks))
	}
}

func TestInjectAssociativeContext_SkipsWhenPrevIsUser(t *testing.T) {
	// Simulates tool_result (role=user) followed by user message
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "content": "result"},
		}},
		map[string]any{"role": "user", "content": "question"},
	}

	// Should skip injection to avoid user→user→user
	result := injectAssociativeContext(msgs, "[context]", false)
	if len(result) != 3 {
		t.Errorf("expected no injection (3 msgs), got %d", len(result))
	}
}

func TestInjectAssociativeContext_EmptyContext(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
	}
	result := injectAssociativeContext(msgs, "", false)
	if len(result) != 1 {
		t.Errorf("empty context should not inject")
	}
}

func TestLastUserText(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "reply"},
		map[string]any{"role": "user", "content": "last question"},
	}
	text := lastUserText(msgs)
	if text != "last question" {
		t.Errorf("expected 'last question', got %q", text)
	}
}

func TestLastUserText_Empty(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "assistant", "content": "only assistant"},
	}
	if text := lastUserText(msgs); text != "" {
		t.Errorf("expected empty, got %q", text)
	}
}

// --- Session Repetition Penalty Tests ---

func newTestServerWithInjectionTracking() *Server {
	return &Server{
		lastInjectedIDs:     make(map[string]map[int64]string),
		sessionInjectCounts: make(map[string]map[int64]int),
		lastTurnInjected:    make(map[string]map[int64]bool),
	}
}

func TestRepetitionPenalty_FirstPass(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	if s.shouldSuppressInjection("t1", 42) {
		t.Error("first injection should not be suppressed")
	}
}

func TestRepetitionPenalty_SecondDifferentTurn(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	// Turn 1: inject ID 42
	s.recordInjections("t1", map[int64]bool{42: true})
	// Turn 2: no injections (clears lastTurnInjected for this thread)
	s.recordInjections("t1", map[int64]bool{})
	// Turn 3: ID 42 again — should pass (count=1, not back-to-back)
	if s.shouldSuppressInjection("t1", 42) {
		t.Error("second injection on different turn should not be suppressed")
	}
}

func TestRepetitionPenalty_ThirdSuppressed(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	// Turn 1: inject ID 42
	s.recordInjections("t1", map[int64]bool{42: true})
	// Turn 2: clear last turn, then inject ID 42 again
	s.recordInjections("t1", map[int64]bool{})
	s.recordInjections("t1", map[int64]bool{42: true})
	// Turn 3: ID 42 again — count is 2, should be suppressed
	s.recordInjections("t1", map[int64]bool{})
	if !s.shouldSuppressInjection("t1", 42) {
		t.Error("third injection should be suppressed (max 2x per session)")
	}
}

func TestRepetitionPenalty_BackToBack(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	// Turn 1: inject ID 42
	s.recordInjections("t1", map[int64]bool{42: true})
	// Same turn (no recordInjections call in between) — back-to-back
	if !s.shouldSuppressInjection("t1", 42) {
		t.Error("back-to-back injection should be suppressed")
	}
}

func TestRepetitionPenalty_ThreadIsolation(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	// Max out ID 42 on thread t1
	s.recordInjections("t1", map[int64]bool{42: true})
	s.recordInjections("t1", map[int64]bool{})
	s.recordInjections("t1", map[int64]bool{42: true})
	s.recordInjections("t1", map[int64]bool{})
	// Thread t2 should be unaffected
	if s.shouldSuppressInjection("t2", 42) {
		t.Error("different thread should not be affected by t1's injection counts")
	}
}

func TestRepetitionPenalty_DifferentIDs(t *testing.T) {
	s := newTestServerWithInjectionTracking()
	// Max out ID 42 on thread t1
	s.recordInjections("t1", map[int64]bool{42: true})
	s.recordInjections("t1", map[int64]bool{})
	s.recordInjections("t1", map[int64]bool{42: true})
	s.recordInjections("t1", map[int64]bool{})
	// ID 99 on same thread should be unaffected
	if s.shouldSuppressInjection("t1", 99) {
		t.Error("different learning ID should not be affected")
	}
}

func TestCleanQueryForSearch(t *testing.T) {
	tests := []struct {
		input  string
		expect string // we check that stopwords are removed and content words remain
		minLen int    // minimum expected word count
	}{
		{
			input:  "kannst du mal schauen ob die associatives passen",
			minLen: 2, // "schauen", "associatives", "passen" — at least some survive
		},
		{
			input:  "SQLITE_BUSY error in signal handler",
			minLen: 2, // technical terms survive
		},
		{
			input:  "the quick brown fox jumps over the lazy dog",
			minLen: 2, // "quick", "brown", "fox", "jumps", "lazy", "dog"
		},
		{
			input:  "",
			expect: "",
		},
	}
	for _, tt := range tests {
		result := cleanQueryForSearch(tt.input)
		if tt.expect != "" {
			if result != tt.expect {
				t.Errorf("cleanQueryForSearch(%q) = %q, want %q", tt.input, result, tt.expect)
			}
			continue
		}
		words := len(strings.Fields(result))
		if tt.minLen > 0 && words < tt.minLen {
			t.Errorf("cleanQueryForSearch(%q) = %q (%d words), want >= %d words", tt.input, result, words, tt.minLen)
		}
		// Verify common stopwords are gone
		for _, stop := range []string{"du", "die", "ob", "the", "over"} {
			for _, w := range strings.Fields(result) {
				if w == stop {
					t.Errorf("cleanQueryForSearch(%q) still contains stopword %q: %q", tt.input, stop, result)
				}
			}
		}
	}
}

func TestFormatDocResult(t *testing.T) {
	result := formatDocResult("go-release-notes", "1.22", "# Changes > ## New Features", "The new log/slog package provides structured logging.")
	if !strings.Contains(result, "[yesmem doc context]") {
		t.Error("should have doc context marker")
	}
	if !strings.Contains(result, "go-release-notes") {
		t.Error("should contain source name")
	}
	if !strings.Contains(result, "log/slog") {
		t.Error("should contain doc content")
	}
	if !strings.Contains(result, "[/yesmem doc context]") {
		t.Error("should have closing marker")
	}
}

func TestFormatDocResult_Empty(t *testing.T) {
	result := formatDocResult("", "", "", "")
	if result != "" {
		t.Errorf("empty content should return empty, got %q", result)
	}
}

func TestParseDocSearchResults(t *testing.T) {
	raw := json.RawMessage(`{"results":[
		{"id":1,"source":"go-docs","version":"1.22","heading_path":"# Concurrency","content":"Goroutines are lightweight","score":-5.2,"source_file":"goroutines.md","tokens_approx":20,"is_skill":false},
		{"id":2,"source":"nginx","version":"","heading_path":"# Config","content":"worker_processes auto","score":-3.1,"source_file":"config.md","tokens_approx":15,"is_skill":false}
	]}`)

	results := parseDocSearchResults(raw)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Source != "go-docs" {
		t.Errorf("first source = %q, want go-docs", results[0].Source)
	}
	if results[0].Content != "Goroutines are lightweight" {
		t.Errorf("first content = %q", results[0].Content)
	}
}

func TestExtractURLDomains(t *testing.T) {
	tests := []struct {
		input    string
		contains []string // domains that must appear in result
	}{
		{
			input:    "schau dir bitte das an https://www.reddit.com/r/AIMemory/comments/abc123",
			contains: []string{"reddit.com"},
		},
		{
			input:    "check https://github.com/foo/bar and https://stackoverflow.com/q/1",
			contains: []string{"github.com", "stackoverflow.com"},
		},
		{
			input:    "kein Link hier",
			contains: []string{},
		},
		{
			input:    "https://sub.example.com/path?q=1",
			contains: []string{"sub.example.com"},
		},
	}
	for _, tt := range tests {
		domains := extractURLDomains(tt.input)
		for _, want := range tt.contains {
			found := false
			for _, d := range domains {
				if d == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("extractURLDomains(%q): missing domain %q, got %v", tt.input, want, domains)
			}
		}
		if len(tt.contains) == 0 && len(domains) != 0 {
			t.Errorf("extractURLDomains(%q): expected no domains, got %v", tt.input, domains)
		}
	}
}

func TestCleanQueryForSearch_URLDomain(t *testing.T) {
	// When the query contains a URL, the domain should appear as a search term
	query := "schau dir bitte das an https://www.reddit.com/r/AIMemory/comments/abc123 irgendwas"
	result := cleanQueryForSearch(query)
	if !strings.Contains(result, "reddit.com") {
		t.Errorf("cleanQueryForSearch with URL: expected domain 'reddit.com' in result, got %q", result)
	}
}

func TestParseDocSearchResults_SkipSkills(t *testing.T) {
	raw := json.RawMessage(`{"results":[
		{"id":1,"source":"go-docs","version":"1.22","heading_path":"# Intro","content":"Go intro","score":-5.0,"is_skill":false},
		{"id":2,"source":"tdd-skill","version":"","heading_path":"# TDD","content":"TDD content","score":-4.0,"is_skill":true}
	]}`)

	results := parseDocSearchResults(raw)
	for _, r := range results {
		if r.IsSkill {
			t.Errorf("skill result should be filtered: %s", r.Source)
		}
	}
}

func TestExtractFileExtensionsFromMessages(t *testing.T) {
	registered := []string{".go", ".mod", ".twig", ".html.twig", ".py", ".php"}

	tests := []struct {
		name string
		msgs []any
		want []string
	}{
		{
			"Read .go",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/home/user/main.go"}},
				}},
			},
			[]string{".go"},
		},
		{
			"Edit .html.twig — compound match",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/app/template.html.twig"}},
				}},
			},
			[]string{".html.twig"},
		},
		{
			"Write .py",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Write", "input": map[string]any{"file_path": "/tmp/script.py"}},
				}},
			},
			[]string{".py"},
		},
		{
			"mixed — deduped",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/a/foo.go"}},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/b/bar.go"}},
				}},
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/c/baz.twig"}},
				}},
			},
			[]string{".go", ".twig"},
		},
		{
			"no tool_use",
			[]any{map[string]any{"role": "user", "content": "hello"}},
			nil,
		},
		{
			"Bash ignored",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go test"}},
				}},
			},
			nil,
		},
		{
			"Glob ignored — uses pattern not file_path",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Glob", "input": map[string]any{"pattern": "**/*.go"}},
				}},
			},
			nil,
		},
		{
			"NotebookEdit uses notebook_path",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "NotebookEdit", "input": map[string]any{"notebook_path": "/home/user/analysis.py"}},
				}},
			},
			[]string{".py"},
		},
		{
			"unregistered extension ignored",
			[]any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/a/file.rs"}},
				}},
			},
			nil,
		},
		{
			"only last 10 messages scanned",
			buildExtTestMessages(20, "/early/old.py", 2, "/recent/new.go"),
			[]string{".go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFileExtensionsFromMessages(tt.msgs, registered)
			if len(tt.want) == 0 {
				if len(got) != 0 {
					t.Errorf("expected empty, got %v", got)
				}
				return
			}
			gotSet := make(map[string]bool)
			for _, g := range got {
				gotSet[g] = true
			}
			for _, w := range tt.want {
				if !gotSet[w] {
					t.Errorf("missing %q in %v", w, got)
				}
			}
		})
	}
}

func buildExtTestMessages(total int, earlyFile string, earlyIdx int, recentFile string) []any {
	msgs := make([]any, total)
	for i := range msgs {
		msgs[i] = map[string]any{"role": "user", "content": "filler"}
	}
	msgs[earlyIdx] = map[string]any{
		"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": earlyFile}},
		},
	}
	msgs[total-1] = map[string]any{
		"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": recentFile}},
		},
	}
	return msgs
}

// --- extractCodingQuery Tests ---

func makeAssistantToolUse(toolName string, input map[string]any) any {
	return map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type":  "tool_use",
				"name":  toolName,
				"input": input,
			},
		},
	}
}

func TestExtractCodingQuery_EditGoCode(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "fix the handler"},
		makeAssistantToolUse("Edit", map[string]any{
			"file_path":  "/home/user/project/internal/proxy/handler.go",
			"new_string": "func HandleRequest(w http.ResponseWriter, r *http.Request) {\n\tctx := r.Context()\n\tresp, err := processRequest(ctx)\n}",
		}),
	}
	query, ext := extractCodingQuery(msgs)
	if query == "" {
		t.Error("expected non-empty query from Edit new_string")
	}
	if ext != ".go" {
		t.Errorf("expected ext .go, got %q", ext)
	}
}

func TestExtractCodingQuery_WriteTwig(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "create template"},
		makeAssistantToolUse("Write", map[string]any{
			"file_path": "/templates/layout.html.twig",
			"content":   "{% extends 'base.html.twig' %}\n{% block body %}\n<div class=\"container\">{{ content }}</div>\n{% endblock %}",
		}),
	}
	query, ext := extractCodingQuery(msgs)
	if query == "" {
		t.Error("expected non-empty query from Write content")
	}
	if ext != ".html.twig" {
		t.Errorf("expected ext .html.twig, got %q", ext)
	}
}

func TestExtractCodingQuery_NoToolUse(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "world"},
	}
	query, ext := extractCodingQuery(msgs)
	if query != "" {
		t.Errorf("expected empty query with no tool_use, got %q", query)
	}
	if ext != "" {
		t.Errorf("expected empty ext, got %q", ext)
	}
}

func TestExtractCodingQuery_MostRecentWins(t *testing.T) {
	// Build messages: older Edit with .go, newer Write with .twig
	msgs := []any{
		map[string]any{"role": "user", "content": "first"},
		makeAssistantToolUse("Edit", map[string]any{
			"file_path":  "/project/old.go",
			"new_string": "package main\nimport \"net/http\"\nfunc ServeHTTP(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(200)\n}",
		}),
		map[string]any{"role": "user", "content": "second"},
		makeAssistantToolUse("Write", map[string]any{
			"file_path": "/templates/new.html.twig",
			"content":   "{% extends 'base.html.twig' %}\n{% block content %}\n<div class=\"container\">{{ form_widget(form) }}</div>\n{% endblock %}",
		}),
	}
	_, ext := extractCodingQuery(msgs)
	if ext != ".html.twig" {
		t.Errorf("expected most recent tool ext .html.twig, got %q", ext)
	}
}

func TestExtractCodingQuery_QueryCapped(t *testing.T) {
	// new_string longer than 120 chars should be capped
	long := strings.Repeat("func LongFunctionName() { doSomethingComplexWithManyParameters() } ", 10)
	msgs := []any{
		makeAssistantToolUse("Edit", map[string]any{
			"file_path":  "/project/code.go",
			"new_string": long,
		}),
	}
	query, _ := extractCodingQuery(msgs)
	if len(query) > 150 {
		// Allow some slack for word boundaries, but must be roughly capped
		t.Errorf("query should be capped, got %d chars: %q", len(query), query)
	}
}

func TestExtractCodingQuery_ScansLast5Messages(t *testing.T) {
	// Put a tool_use at position -6 (outside scan window) and nothing in last 5
	msgs := make([]any, 8)
	for i := range msgs {
		msgs[i] = map[string]any{"role": "user", "content": "filler"}
	}
	// Position 1 = 7 messages ago → outside the 5-message scan window
	msgs[1] = makeAssistantToolUse("Edit", map[string]any{
		"file_path":  "/project/old.go",
		"new_string": "func OldFunc() {}",
	})
	query, _ := extractCodingQuery(msgs)
	if query != "" {
		t.Errorf("tool_use outside scan window should be ignored, got %q", query)
	}
}

// --- extractExtension Tests ---

func TestExtractExtension_Simple(t *testing.T) {
	ext := extractExtension("handler.go")
	if ext != ".go" {
		t.Errorf("expected .go, got %q", ext)
	}
}

func TestExtractExtension_CompoundTwig(t *testing.T) {
	ext := extractExtension("layout.html.twig")
	if ext != ".html.twig" {
		t.Errorf("expected .html.twig, got %q", ext)
	}
}

func TestExtractExtension_XmlTwig(t *testing.T) {
	ext := extractExtension("config.xml.twig")
	if ext != ".xml.twig" {
		t.Errorf("expected .xml.twig, got %q", ext)
	}
}

func TestExtractExtension_NoExtension(t *testing.T) {
	ext := extractExtension("Makefile")
	if ext != "" {
		t.Errorf("expected empty, got %q", ext)
	}
}

func TestExtractExtension_FullPath(t *testing.T) {
	ext := extractExtension("/home/user/project/templates/layout.html.twig")
	if ext != ".html.twig" {
		t.Errorf("expected .html.twig from full path, got %q", ext)
	}
}

func TestContradictionWarning(t *testing.T) {
	warning := formatContradictionWarning(42, 99)
	expected := "Contradiction: [ID:42] contradicts previously injected [ID:99] — verify both."
	if warning != expected {
		t.Errorf("expected %q, got %q", expected, warning)
	}
}

func TestContradictionWarning_Empty(t *testing.T) {
	warning := formatContradictionWarning(0, 0)
	if warning != "" {
		t.Errorf("expected empty for zero IDs, got %q", warning)
	}
}
