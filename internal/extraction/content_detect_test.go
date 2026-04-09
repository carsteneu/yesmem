package extraction

import (
	"strings"
	"testing"
)

func TestLooksLikePaste(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"html", "<h1>Building LLM Apps</h1>\n<p>This skill helps you...", true},
		{"stdout", "<local-command-stdout>Version 0.2.21:\n• Fuzzy matching", true},
		{"stack_trace", strings.Repeat("at com.example.Foo.bar(Foo.java:42)\n", 20), true},
		{"repetitive_logs", strings.Repeat("2026-03-13 ERROR: connection timeout\n", 20), true},
		{"normal_text", "Also, wir machen das so weil X und Y wichtig ist", false},
		{"code_block", "```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```", true},
		{"empty", "", false},
		{"div_html", "<div class=\"container\"><p>Some content here</p></div>", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePaste(tt.input); got != tt.want {
				preview := tt.input
				if len(preview) > 50 {
					preview = preview[:50]
				}
				t.Errorf("looksLikePaste(%q...) = %v, want %v", preview, got, tt.want)
			}
		})
	}
}

func TestLooksLikePlan(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"impl_plan", "# Implementation Plan\n\n## Goal\nBuild X\n\n### Task 1: Setup", true},
		{"numbered_steps", "## Step 1\nDo A\n## Step 2\nDo B\n## Step 3\nDo C", true},
		{"normal_response", "Ok, ich mache das so: erst A dann B.", false},
		{"task_list", "Here is the approach:\n\n### Task 1: Config\n### Task 2: Wiring", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePlan(tt.input); got != tt.want {
				preview := tt.input
				if len(preview) > 50 {
					preview = preview[:50]
				}
				t.Errorf("looksLikePlan(%q...) = %v, want %v", preview, got, tt.want)
			}
		})
	}
}

func TestKeepFirstAndLast(t *testing.T) {
	short := "short text"
	if got := keepFirstAndLast(short, 1000, 500); got != short {
		t.Errorf("short text should be unchanged, got %q", got)
	}

	long := strings.Repeat("A", 1000) + strings.Repeat("B", 5000) + strings.Repeat("C", 500)
	result := keepFirstAndLast(long, 1000, 500)
	if !strings.HasPrefix(result, strings.Repeat("A", 1000)) {
		t.Error("first 1000 chars not preserved")
	}
	if !strings.HasSuffix(result, strings.Repeat("C", 500)) {
		t.Error("last 500 chars not preserved")
	}
	if !strings.Contains(result, "truncated") {
		t.Error("missing truncation marker")
	}
	if len(result) > 1600 {
		t.Errorf("expected ~1520 chars, got %d", len(result))
	}
}

func TestTruncateText_PasteHTML(t *testing.T) {
	html := "<h1>Title</h1>\n<p>" + strings.Repeat("lorem ipsum ", 500) + "</p>"
	result := truncateText(html, "user")
	if len(result) > 1100 {
		t.Errorf("HTML paste not truncated to ~1000: got %d chars", len(result))
	}
}

func TestTruncateText_PlanFirstAndLast(t *testing.T) {
	plan := "# Implementation Plan\n## Goal\nBuild X because Y\n"
	plan += strings.Repeat("### Task N: Details...\n", 200)
	plan += "## Summary\nKey tradeoff: A vs B chosen for reason Z"
	result := truncateText(plan, "assistant")
	if !strings.Contains(result, "Build X because Y") {
		t.Error("plan: beginning (decision) was lost")
	}
	if !strings.Contains(result, "A vs B chosen") {
		t.Error("plan: ending (summary) was lost")
	}
	if len(result) > 1600 {
		t.Errorf("plan not truncated: got %d chars", len(result))
	}
}

func TestTruncateText_NaturalUserText(t *testing.T) {
	text := strings.Repeat("Also wir machen das so und so weil ", 115)
	result := truncateText(text, "user")
	if len(result) < 3000 || len(result) > 3100 {
		t.Errorf("natural user text: expected ~3000 chars, got %d", len(result))
	}
}

func TestTruncateText_NaturalAssistantText(t *testing.T) {
	text := strings.Repeat("Ich analysiere das Problem und ", 100)
	result := truncateText(text, "assistant")
	if len(result) < 1500 || len(result) > 1600 {
		t.Errorf("natural assistant text: expected ~1500 chars, got %d", len(result))
	}
}

func TestTruncateText_ShortTextUnchanged(t *testing.T) {
	text := "kurzer normaler Text"
	if got := truncateText(text, "user"); got != text {
		t.Errorf("short text was modified: %q", got)
	}
}
