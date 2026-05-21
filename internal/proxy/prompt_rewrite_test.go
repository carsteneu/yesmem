package proxy

import (
	"strings"
	"testing"
)

// --- StripOutputEfficiency ---

func TestStripOutputEfficiency_RemovesSection(t *testing.T) {
	text := "# Introduction\nSome intro text.\n\n# Output efficiency\nBe brief.\nUse short answers.\n\n# Next Section\nMore content here."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripOutputEfficiency(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)

	if strings.Contains(result, "Output efficiency") {
		t.Error("section header should be removed")
	}
	if strings.Contains(result, "Be brief.") {
		t.Error("section body should be removed")
	}
	if !strings.Contains(result, "Introduction") {
		t.Error("preceding section should be preserved")
	}
	if !strings.Contains(result, "Next Section") {
		t.Error("following section should be preserved")
	}
}

func TestStripOutputEfficiency_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "# Introduction\nSome intro text."},
		},
	}

	modified := StripOutputEfficiency(req)
	if modified {
		t.Error("expected false when section not present")
	}
}

func TestStripOutputEfficiency_PreservesCacheControl(t *testing.T) {
	text := "# Output efficiency\nBe terse.\n\n# Other\nContent."
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          text,
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}

	StripOutputEfficiency(req)

	blocks := req["system"].([]any)
	block := blocks[0].(map[string]any)
	cc, ok := block["cache_control"]
	if !ok {
		t.Fatal("cache_control should be preserved after modification")
	}
	if cc.(map[string]any)["type"] != "ephemeral" {
		t.Error("cache_control type should remain ephemeral")
	}
}

func TestStripOutputEfficiency_SectionAtEnd(t *testing.T) {
	text := "# Introduction\nSome intro.\n\n# Output efficiency\nBe brief and short."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripOutputEfficiency(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "Output efficiency") {
		t.Error("section should be removed even at EOF")
	}
	if !strings.Contains(result, "Introduction") {
		t.Error("preceding section should be preserved")
	}
}

// --- StripToneBrevity ---

func TestStripToneBrevity_RemovesLine(t *testing.T) {
	text := "You are a helpful assistant.\nYour responses should be short and concise.\nAnswer questions accurately."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	modified := StripToneBrevity(req)
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)

	if strings.Contains(result, "Your responses should be short and concise.") {
		t.Error("line should be removed")
	}
	if !strings.Contains(result, "You are a helpful assistant.") {
		t.Error("preceding line should be preserved")
	}
	if !strings.Contains(result, "Answer questions accurately.") {
		t.Error("following line should be preserved")
	}
}

func TestStripToneBrevity_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are a helpful assistant."},
		},
	}

	modified := StripToneBrevity(req)
	if modified {
		t.Error("expected false when line not present")
	}
}

// --- InjectAntDirectives ---

func TestInjectAntDirectives_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectAntDirectives(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-directives]") {
		t.Errorf("block should be tagged yesmem-directives, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "verify it actually works") {
		t.Error("should contain verification directive")
	}
	if !strings.Contains(text, "Report outcomes faithfully") {
		t.Error("should contain reporting directive")
	}
	if !strings.Contains(text, "collaborator") {
		t.Error("should contain collaborator directive")
	}
	if !strings.Contains(text, "Err on the side of more explanation") {
		t.Error("should contain explanation directive")
	}
}

// --- InjectCLAUDEMDAuthority ---

func TestInjectCLAUDEMDAuthority_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectCLAUDEMDAuthority(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-enhance]") {
		t.Errorf("block should be tagged yesmem-enhance, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "CLAUDE.md") {
		t.Error("should mention CLAUDE.md")
	}
	if !strings.Contains(text, "authoritative") {
		t.Error("should mention authoritative")
	}
	if !strings.Contains(text, "Comment discipline") {
		t.Error("should contain comment discipline section")
	}
}

// --- InjectClaudeToolPrefs ---

func TestInjectClaudeToolPrefs_AddsBlock(t *testing.T) {
	req := map[string]any{"system": "old"}

	InjectClaudeToolPrefs(req)
	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-tool-prefs]") {
		t.Errorf("block should be tagged yesmem-tool-prefs, got: %s", text[:min(50, len(text))])
	}
	for _, keyword := range []string{"Edit", "Write", "REPL", "soft-fail", "await Read", "file-tracker", "Agent", "TaskCreate"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("tool-prefs block should mention %q", keyword)
		}
	}
}

// --- InjectOutputDiscipline ---

func TestInjectOutputDiscipline_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}
	InjectOutputDiscipline(req)
	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-output-discipline]") {
		t.Errorf("block should be tagged yesmem-output-discipline, got: %s", text[:min(50, len(text))])
	}
	for _, keyword := range []string{"preamble", "skill-eval", "exploratory", "set_plan", "timestamps", "msg:N"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("output-discipline block should mention %q", keyword)
		}
	}
}

// --- InjectCodingDiscipline ---

func TestInjectCodingDiscipline_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}
	InjectCodingDiscipline(req)
	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-coding-discipline]") {
		t.Errorf("block should be tagged yesmem-coding-discipline, got: %s", text[:min(50, len(text))])
	}
	for _, keyword := range []string{"read", "AskUserQuestion", "half-finished", "browser", "TDD"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("coding-discipline block should mention %q", keyword)
		}
	}
}

// Shared: all three new injects preserve existing blocks.
func TestInjectDirectives_PreserveExistingBlocks(t *testing.T) {
	injects := []func(map[string]any){
		InjectClaudeToolPrefs,
		InjectOutputDiscipline,
		InjectCodingDiscipline,
		InjectBeweislast,
		InjectScopeDiscipline,
		InjectDelegationContract,
		InjectClarifyFirst,
	}
	for _, inject := range injects {
		req := map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "Block one."},
				map[string]any{"type": "text", "text": "Block two."},
			},
		}
		inject(req)
		blocks := req["system"].([]any)
		if len(blocks) != 3 {
			t.Fatalf("expected 3 blocks after inject, got %d", len(blocks))
		}
		first := blocks[0].(map[string]any)["text"].(string)
		if first != "Block one." {
			t.Error("existing blocks should be preserved")
		}
	}
}

// --- InjectPersonaTone ---

func TestInjectPersonaTone_Verbose(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "verbose")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.HasPrefix(text, "[yesmem-tone]") {
		t.Errorf("block should be tagged yesmem-tone, got: %s", text[:min(50, len(text))])
	}
	if !strings.Contains(text, "explanation") {
		t.Error("verbose tone should mention explanation")
	}
}

func TestInjectPersonaTone_EmptyIsNoop(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "")

	blocks := req["system"].([]any)
	if len(blocks) != 1 {
		t.Errorf("empty verbosity should be no-op, got %d blocks", len(blocks))
	}
}

func TestInjectPersonaTone_UnknownIsNoop(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "chatterbox")

	blocks := req["system"].([]any)
	if len(blocks) != 1 {
		t.Errorf("unknown verbosity should be no-op, got %d blocks", len(blocks))
	}
}

func TestInjectPersonaTone_Concise(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "concise")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	last := blocks[len(blocks)-1].(map[string]any)
	text, _ := last["text"].(string)

	if !strings.Contains(text, "concise") {
		t.Error("concise tone should mention concise")
	}
}

// min helper for safe string slicing in test error messages
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- replaceSystemText ---

func TestReplaceSystemText_ReplacesExactMatch(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "Some preamble.\nDon't add features beyond what was asked.\nMore text here."},
		},
	}

	modified := replaceSystemText(req, "Don't add features beyond what was asked.", "Fix adjacent issues when clearly appropriate.")
	if !modified {
		t.Fatal("expected modification")
	}

	blocks := req["system"].([]any)
	result := blocks[0].(map[string]any)["text"].(string)

	if strings.Contains(result, "Don't add features beyond what was asked.") {
		t.Error("original text should be removed")
	}
	if !strings.Contains(result, "Fix adjacent issues when clearly appropriate.") {
		t.Error("replacement text should be present")
	}
	if !strings.Contains(result, "Some preamble.") {
		t.Error("surrounding text should be preserved")
	}
}

func TestReplaceSystemText_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text here."},
		},
	}

	if replaceSystemText(req, "nonexistent needle", "replacement") {
		t.Error("expected false when text not present")
	}
}

func TestReplaceSystemText_MultipleBlocks(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "Block one without match."},
			map[string]any{"type": "text", "text": "Block two with: needle text here."},
		},
	}

	modified := replaceSystemText(req, "needle text", "replaced text")
	if !modified {
		t.Fatal("expected modification in second block")
	}

	blocks := req["system"].([]any)
	result := blocks[1].(map[string]any)["text"].(string)
	if !strings.Contains(result, "replaced text") {
		t.Error("second block should contain replacement")
	}
}

func TestReplaceSystemText_PreservesCacheControl(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type":          "text",
				"text":          "Text with needle to replace.",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
	}

	replaceSystemText(req, "needle", "replaced")

	blocks := req["system"].([]any)
	block := blocks[0].(map[string]any)
	cc, ok := block["cache_control"]
	if !ok {
		t.Fatal("cache_control should be preserved")
	}
	if cc.(map[string]any)["type"] != "ephemeral" {
		t.Error("cache_control type should remain ephemeral")
	}
}

// --- RewriteGoldPlating ---

func TestRewriteGoldPlating_Replaces(t *testing.T) {
	text := "Some intro.\nDon't add features, refactor, or introduce abstractions beyond what the task requires. A bug fix doesn't need surrounding cleanup; a one-shot operation doesn't need a helper.\nMore text."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteGoldPlating(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "beyond what the task requires") {
		t.Error("original gold-plating text should be replaced")
	}
	if !strings.Contains(result, "adjacent code is broken") {
		t.Error("replacement should allow adjacent fixes")
	}
	if !strings.Contains(result, "Some intro.") {
		t.Error("surrounding text should be preserved")
	}
}

func TestRewriteGoldPlating_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No gold plating text here."},
		},
	}
	if RewriteGoldPlating(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteErrorHandling ---

func TestRewriteErrorHandling_Replaces(t *testing.T) {
	text := "Intro.\nDon't add error handling, fallbacks, or validation for scenarios that can't happen. Trust internal code and framework guarantees. Only validate at system boundaries (user input, external APIs). Don't use feature flags.\nEnd."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteErrorHandling(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "scenarios that can't happen") {
		t.Error("original error handling cap should be replaced")
	}
	if !strings.Contains(result, "real boundaries where failures can realistically occur") {
		t.Error("replacement should specify real boundaries")
	}
}

func TestRewriteErrorHandling_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteErrorHandling(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteThreeLinesRule ---

func TestRewriteThreeLinesRule_Replaces(t *testing.T) {
	text := "Some context. Three similar lines is better than a premature abstraction. More text."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteThreeLinesRule(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "Three similar lines") {
		t.Error("original three-lines rule should be replaced")
	}
	if !strings.Contains(result, "judgment about when to extract") {
		t.Error("replacement should encourage judgment")
	}
}

func TestRewriteThreeLinesRule_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteThreeLinesRule(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteSubagentCompleteness ---

func TestRewriteSubagentCompleteness_Replaces(t *testing.T) {
	text := "You are a subagent. Complete the task fully\u2014don't gold-plate, but don't leave it half-done. Report back."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteSubagentCompleteness(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "gold-plate") {
		t.Error("original gold-plate text should be replaced")
	}
	if !strings.Contains(result, "careful senior developer") {
		t.Error("replacement should set senior-developer standard")
	}
}

func TestRewriteSubagentCompleteness_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteSubagentCompleteness(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteExploreAgentSpeed ---

func TestRewriteExploreAgentSpeed_Replaces(t *testing.T) {
	text := "You are an explore agent.\nNOTE: You are meant to be a fast agent that returns output as quickly as possible. In order to achieve this you must:\n- Make efficient use of tools"
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteExploreAgentSpeed(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "fast agent that returns output as quickly as possible") {
		t.Error("speed-first bias should be replaced")
	}
	if !strings.Contains(result, "thorough in your exploration") {
		t.Error("replacement should emphasize thoroughness")
	}
}

func TestRewriteExploreAgentSpeed_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteExploreAgentSpeed(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteSubagentCodeSuppression ---

func TestRewriteSubagentCodeSuppression_Replaces(t *testing.T) {
	text := "Report results.\nInclude code snippets only when the exact text is load-bearing (e.g., a bug you found, a function signature the caller asked for) \u2014 do not recap code you merely read.\nDone."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteSubagentCodeSuppression(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(result, "do not recap code you merely read") {
		t.Error("code suppression should be replaced")
	}
	if !strings.Contains(result, "provide useful context") {
		t.Error("replacement should allow useful code context")
	}
}

func TestRewriteSubagentCodeSuppression_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteSubagentCodeSuppression(req) {
		t.Error("expected false when text not present")
	}
}

// --- RewriteScopeMatching ---

func TestRewriteScopeMatching_Replaces(t *testing.T) {
	text := "Be careful with actions.\nMatch the scope of your actions to what was actually requested.\nMore instructions."
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": text},
		},
	}

	if !RewriteScopeMatching(req) {
		t.Fatal("expected modification")
	}

	result := req["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(result, "closely related issues") {
		t.Error("replacement should allow adjacent issue fixing")
	}
	if !strings.Contains(result, "Be careful with actions.") {
		t.Error("surrounding text should be preserved")
	}
}

func TestRewriteScopeMatching_ReturnsFalseWhenAbsent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "No matching text."},
		},
	}
	if RewriteScopeMatching(req) {
		t.Error("expected false when text not present")
	}
}

// --- InjectBeweislast ---

func TestInjectBeweislast_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectBeweislast(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-beweislast]") {
		t.Errorf("block should be tagged: %s", text)
	}
	for _, keyword := range []string{"Fabrication", "Claim-vs-proof", "Stance-under-challenge", "Tool-result-honesty", "Long-context-erosion", "Self-check", "mental self-check"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("beweislast block should mention %q", keyword)
		}
	}
}

// --- InjectScopeDiscipline ---

func TestInjectScopeDiscipline_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectScopeDiscipline(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-scope-discipline]") {
		t.Errorf("block should be tagged: %s", text)
	}
	for _, keyword := range []string{"deliver A", "MUST be surfaced", "scope-drift", "Authorization covers doing"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("scope-discipline block should mention %q", keyword)
		}
	}
}

// --- InjectDelegationContract ---

func TestInjectDelegationContract_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectDelegationContract(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-delegation-contract]") {
		t.Errorf("block should be tagged: %s", text)
	}
	for _, keyword := range []string{"self-contained", "goal in one sentence", "parallel dispatch", "Opus", "Sonnet", "Haiku", "structured outputs"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("delegation-contract block should mention %q", keyword)
		}
	}
}

// --- InjectClarifyFirst ---

func TestInjectClarifyFirst_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectClarifyFirst(req)

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-clarify-first]") {
		t.Errorf("block should be tagged: %s", text)
	}
	for _, keyword := range []string{"materially different work", "state your assumption", "fire-and-forget"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("clarify-first block should mention %q", keyword)
		}
	}
}

func TestInjectWikiFirst_AddsBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectWikiFirst(req, "")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-wiki-first]") {
		t.Errorf("block should be tagged: %s", text)
	}
	for _, keyword := range []string{"~/.claude/yesmem/wiki/", "BEFORE editing", "gotchas", "learnings", "search_code_index"} {
		if !strings.Contains(text, keyword) {
			t.Errorf("wiki-first block should mention %q", keyword)
		}
	}
	if strings.Contains(text, "Project:") {
		t.Errorf("block should not contain Project prefix when project is empty: %s", text)
	}
}

func TestInjectWikiFirst_AddsBlockWithProject(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectWikiFirst(req, "/home/user/projects/yesmem")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	text, _ := blocks[1].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, "[yesmem-wiki-first]") {
		t.Errorf("block should be tagged: %s", text)
	}
	if !strings.Contains(text, "Project:") {
		t.Errorf("block should contain Project prefix when project is set: %s", text)
	}
	if !strings.Contains(text, "/home/user/projects/yesmem") {
		t.Errorf("block should contain project path: %s", text)
	}
}

// --- Idempotence: repeated inject must not duplicate blocks ---

func TestInjectDirectives_Idempotent(t *testing.T) {
	cases := []struct {
		name string
		tag  string
		fn   func(map[string]any)
	}{
		{"AntDirectives", "yesmem-directives", InjectAntDirectives},
		{"CLAUDEMDAuthority", "yesmem-enhance", InjectCLAUDEMDAuthority},
		{"ToolPrefs", "yesmem-tool-prefs", InjectClaudeToolPrefs},
		{"OutputDiscipline", "yesmem-output-discipline", InjectOutputDiscipline},
		{"CodingDiscipline", "yesmem-coding-discipline", InjectCodingDiscipline},
		{"Beweislast", "yesmem-beweislast", InjectBeweislast},
		{"ScopeDiscipline", "yesmem-scope-discipline", InjectScopeDiscipline},
		{"DelegationContract", "yesmem-delegation-contract", InjectDelegationContract},
		{"ClarifyFirst", "yesmem-clarify-first", InjectClarifyFirst},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := map[string]any{
				"system": []any{
					map[string]any{"type": "text", "text": "You are Claude."},
				},
			}

			tc.fn(req)
			tc.fn(req)

			blocks := req["system"].([]any)
			if len(blocks) != 2 {
				t.Fatalf("expected 2 blocks after 2 inject calls, got %d (duplicate injection)", len(blocks))
			}
			count := 0
			prefix := "[" + tc.tag + "]"
			for _, b := range blocks {
				text, _ := b.(map[string]any)["text"].(string)
				if strings.HasPrefix(text, prefix) {
					count++
				}
			}
			if count != 1 {
				t.Errorf("expected exactly one %s block, found %d", prefix, count)
			}
		})
	}
}

func TestInjectPersonaTone_Idempotent(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude."},
		},
	}

	InjectPersonaTone(req, "concise")
	InjectPersonaTone(req, "concise")

	blocks := req["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks after 2 inject calls, got %d", len(blocks))
	}
	count := 0
	for _, b := range blocks {
		text, _ := b.(map[string]any)["text"].(string)
		if strings.HasPrefix(text, "[yesmem-tone]") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one [yesmem-tone] block, found %d", count)
	}
}
