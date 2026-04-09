package proxy

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCollapseOldMessages_Basic(t *testing.T) {
	// 200 messages, collapse everything before index 150
	msgs := make([]any, 200)
	original := make([]any, 200)

	msgs[0] = map[string]any{"role": "system", "content": "system prompt"}
	original[0] = msgs[0]

	for i := 1; i < 200; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		msg := map[string]any{
			"role":    role,
			"content": fmt.Sprintf("[→] tool result archived #%d", i),
		}
		msgs[i] = msg
		original[i] = map[string]any{
			"role": role,
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/app/file%d.go", i)}},
			},
		}
	}

	result := CollapseOldMessages(msgs, original, 150, time.Time{}, time.Time{}, nil, nil)

	// Should be: system(1) + archive(1) + msgs[150:](50) = 52
	if len(result) > 55 {
		t.Errorf("expected ~52 messages, got %d", len(result))
	}

	// First should be system
	first, _ := result[0].(map[string]any)
	if first["role"] != "system" {
		t.Error("first message should be system")
	}

	// Second should be archive block
	second, _ := result[1].(map[string]any)
	content, _ := second["content"].(string)
	if !strings.Contains(content, "[Archiv:") {
		t.Errorf("second message should be archive, got: %s", truncate(content, 100))
	}
	if second["role"] != "user" {
		t.Error("archive block should have role=user")
	}

	// Archive should mention message count
	if !strings.Contains(content, "149") {
		t.Errorf("archive should mention ~149 msgs, got: %s", truncate(content, 200))
	}

	// Archive should mention get_compacted_stubs
	if !strings.Contains(content, "get_compacted_stubs") {
		t.Error("archive should hint at get_compacted_stubs for reinzoomen")
	}
}

func TestCollapseOldMessages_SmallCutoff(t *testing.T) {
	// 20 messages, cutoff at 10 — small but valid (minCollapseMessages=1)
	msgs := make([]any, 20)
	msgs[0] = map[string]any{"role": "system", "content": "system prompt"}
	for i := 1; i < 20; i++ {
		msgs[i] = map[string]any{"role": "user", "content": "msg"}
	}

	result := CollapseOldMessages(msgs, msgs, 10, time.Time{}, time.Time{}, nil, nil)

	// Should collapse: system(1) + archive(1) + msgs[10:](10) = 12
	if len(result) != 12 {
		t.Errorf("expected 12 (collapsed), got %d", len(result))
	}
}

func TestCollapseOldMessages_PreservesRecent(t *testing.T) {
	msgs := make([]any, 100)
	msgs[0] = map[string]any{"role": "system", "content": "sys"}
	for i := 1; i < 100; i++ {
		msgs[i] = map[string]any{"role": "user", "content": fmt.Sprintf("msg-%d", i)}
	}

	result := CollapseOldMessages(msgs, msgs, 50, time.Time{}, time.Time{}, nil, nil)

	// Messages 50-99 should be preserved verbatim
	archiveOffset := 2 // system + archive
	for i := 50; i < 100; i++ {
		resultIdx := archiveOffset + (i - 50)
		if resultIdx >= len(result) {
			t.Fatalf("result too short: %d, need index %d", len(result), resultIdx)
		}
		m, _ := result[resultIdx].(map[string]any)
		expected := fmt.Sprintf("msg-%d", i)
		if c, _ := m["content"].(string); c != expected {
			t.Errorf("msg[%d]: expected %q, got %q", resultIdx, expected, c)
		}
	}
}

func TestCollapseOldMessages_ExtractsStats(t *testing.T) {
	msgs := make([]any, 80)
	original := make([]any, 80)

	msgs[0] = map[string]any{"role": "system", "content": "sys"}
	original[0] = msgs[0]

	for i := 1; i < 80; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		msgs[i] = map[string]any{"role": role, "content": "[stub]"}
		original[i] = map[string]any{
			"role": role,
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/app/proxy.go"}},
			},
		}
	}

	result := CollapseOldMessages(msgs, original, 50, time.Time{}, time.Time{}, nil, nil)
	second, _ := result[1].(map[string]any)
	content, _ := second["content"].(string)

	// Should contain tool stats
	if !strings.Contains(content, "Edit") {
		t.Errorf("archive should contain tool stats, got: %s", truncate(content, 200))
	}
	// Should contain file stats
	if !strings.Contains(content, "proxy.go") {
		t.Errorf("archive should contain file stats, got: %s", truncate(content, 200))
	}
}

func TestCollapseOldMessages_TokenReduction(t *testing.T) {
	// 500 messages — realistic session
	msgs := make([]any, 500)
	original := make([]any, 500)
	msgs[0] = map[string]any{"role": "system", "content": "system prompt"}
	original[0] = msgs[0]

	for i := 1; i < 500; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		// Each stub is ~50 tokens
		msgs[i] = map[string]any{
			"role":    role,
			"content": fmt.Sprintf("[→] deep_search('some query about topic %d with extra context')", i),
		}
		original[i] = map[string]any{
			"role": role,
			"content": []any{
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/app/file%d.go", i)}},
			},
		}
	}

	before := estimateTokensFromMessages(msgs, testEstimate)
	result := CollapseOldMessages(msgs, original, 450, time.Time{}, time.Time{}, nil, nil)
	after := estimateTokensFromMessages(result, testEstimate)

	savings := float64(before-after) / float64(before) * 100
	t.Logf("Collapse: %d → %d tokens (%.1f%% reduction), %d → %d msgs", before, after, savings, len(msgs), len(result))

	// Should save at least 70% of tokens from old messages
	if savings < 50 {
		t.Errorf("expected >50%% savings, got %.1f%%", savings)
	}
}

func TestCalcCollapseCutoff_SkipsOrphanToolResult(t *testing.T) {
	// Build 60 messages where the natural cutoff lands on a user message
	// containing tool_result — the cutoff must advance past it.
	n := 60
	msgs := make([]any, n)
	msgs[0] = map[string]any{"role": "system", "content": "system"}

	for i := 1; i < n; i++ {
		if i%2 == 1 {
			// assistant with tool_use
			msgs[i] = map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    fmt.Sprintf("toolu_%d", i),
						"name":  "Read",
						"input": map[string]any{"file_path": "/a.go"},
					},
				},
			}
		} else {
			// user with tool_result
			msgs[i] = map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": fmt.Sprintf("toolu_%d", i-1),
						"content":     "ok",
					},
				},
			}
		}
	}

	estimate := func(jsonText string) int { return len(jsonText) / 4 }
	// Low floor so cutoff triggers with our small messages
	cutoff := CalcCollapseCutoff(msgs, 5, 200, estimate)

	if cutoff == -1 {
		t.Fatal("expected a cutoff, got -1")
	}

	// The cutoff must NOT land on a user message with tool_result
	msg, ok := msgs[cutoff].(map[string]any)
	if !ok {
		t.Fatalf("messages[cutoff=%d] is not a map", cutoff)
	}
	if msg["role"] == "user" && hasToolResultContent(msg["content"]) {
		t.Errorf("cutoff=%d lands on orphan tool_result — pair would be split", cutoff)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestStubifyThenCollapse_ReachesFloor(t *testing.T) {
	estimate := func(text string) int { return len(text) / 4 }

	msgs := make([]any, 100)
	msgs[0] = map[string]any{"role": "system", "content": "system prompt"}
	for i := 1; i < 100; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		msgs[i] = map[string]any{"role": role, "content": longString(6400)}
	}

	// Phase 1: Stubify with threshold=120k
	stubResult := Stubify(msgs, 120000, 10, 50, nil, nil, estimate)

	if stubResult.StubCount == 0 {
		t.Fatal("expected Stubify to stub some messages, got 0 stubs")
	}
	t.Logf("After Stubify: %dk tokens, %d stubs", stubResult.TokensAfter/1000, stubResult.StubCount)

	// Phase 2: Collapse with floor=70k
	cutoff := CalcCollapseCutoff(stubResult.Messages, 10, 70000, estimate)
	final := stubResult.Messages
	if cutoff > 0 {
		final = CollapseOldMessages(stubResult.Messages, msgs, cutoff, time.Time{}, time.Time{}, nil, nil)
	}

	// Verify: final tokens should be around 70k (not 88k+)
	totalTokens := 0
	for _, msg := range final {
		if m, ok := msg.(map[string]any); ok {
			totalTokens += estimateMessageContentTokens(m, func(text string) int {
				return estimate(text)
			})
		}
	}

	if totalTokens > 80000 {
		t.Errorf("expected final tokens <= 80k, got %dk", totalTokens/1000)
	}

	reduction := 100 - totalTokens*100/158000
	if reduction < 50 {
		t.Errorf("expected >=50%% reduction, got %d%%", reduction)
	}

	t.Logf("Pipeline result: %dk tokens, %d messages (cutoff=%d)", totalTokens/1000, len(final), cutoff)
}

// --- Timeline tests (replaced old extractDigests tests) ---

func TestExtractTimeline_IncludesUserMessages(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "user", "content": "ja, mach das"},
		map[string]any{"role": "assistant", "content": "OK, ich starte."},
		map[string]any{"role": "user", "content": "teste bitte auf der VM"},
		map[string]any{"role": "assistant", "content": "Alles klar."},
	}

	events := extractTimeline(msgs, 1, 4)

	// ALL user messages should be included (they're steering signals)
	userCount := 0
	for _, e := range events {
		if strings.Contains(e, "U:") {
			userCount++
		}
	}
	if userCount != 2 {
		t.Errorf("expected 2 user messages in timeline, got %d", userCount)
	}
	for _, e := range events {
		t.Logf("event: %s", e)
	}
}

func TestExtractTimeline_SkipsSystemReminders(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "user", "content": "[system-reminder] task tools..."},
		map[string]any{"role": "user", "content": "fix the bug"},
	}

	events := extractTimeline(msgs, 1, 2)

	// System reminders (starting with [) should be skipped
	if len(events) != 1 {
		t.Errorf("expected 1 event (only real user msg), got %d", len(events))
	}
	if len(events) > 0 && !strings.Contains(events[0], "fix the bug") {
		t.Errorf("expected user message, got: %s", events[0])
	}
}

func TestExtractTimeline_ExtractsToolEvents(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/home/user/project/internal/proxy/collapse.go"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git commit -m \"fix collapse bug\""}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/app/file.go"}},
		}},
	}

	events := extractTimeline(msgs, 1, 3)

	// Edit should appear with shortened path
	hasEdit := false
	hasCommit := false
	hasRead := false
	for _, e := range events {
		if strings.Contains(e, "Edit:") && strings.Contains(e, "proxy/collapse.go") {
			hasEdit = true
		}
		if strings.Contains(e, "git commit:") {
			hasCommit = true
		}
		if strings.Contains(e, "Read") {
			hasRead = true
		}
		t.Logf("event: %s", e)
	}

	if !hasEdit {
		t.Error("expected Edit event with file path")
	}
	if !hasCommit {
		t.Error("expected git commit event")
	}
	if hasRead {
		t.Error("Read events should be skipped (too noisy)")
	}
}

func TestExtractTimeline_GitCommitExtraction(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git commit -m \"fix: CLI extraction parsing + fence stripping\""}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ssh testhost@10.0.0.1 \"systemctl --user restart yesmem\""}},
		}},
	}

	events := extractTimeline(msgs, 1, 3)

	found := map[string]bool{}
	for _, e := range events {
		if strings.Contains(e, "git commit:") {
			found["commit"] = true
		}
		if strings.Contains(e, "deploy") {
			found["deploy"] = true
		}
		if strings.Contains(e, "ssh") {
			found["ssh"] = true
		}
		t.Logf("event: %s", e)
	}

	if !found["commit"] {
		t.Error("expected git commit event")
	}
	if !found["deploy"] {
		t.Error("expected deploy event")
	}
	if !found["ssh"] {
		t.Error("expected ssh event")
	}
}

func TestExtractTimeline_BudgetLimit(t *testing.T) {
	// Create 200 user messages — should be capped at 120
	msgs := make([]any, 201)
	msgs[0] = map[string]any{"role": "system", "content": "sys"}
	for i := 1; i <= 200; i++ {
		msgs[i] = map[string]any{"role": "user", "content": fmt.Sprintf("message number %d", i)}
	}

	events := extractTimeline(msgs, 1, 200)

	// Budget: first 20 + gap marker + last 100 = 121
	if len(events) > 121 {
		t.Errorf("expected max 121 events (budget limit), got %d", len(events))
	}

	// Should contain gap marker
	hasGap := false
	for _, e := range events {
		if strings.Contains(e, "events omitted") {
			hasGap = true
		}
	}
	if !hasGap {
		t.Error("expected gap marker for omitted events")
	}
}

func TestExtractTimeline_MixedConversation(t *testing.T) {
	// Realistic conversation: user asks, assistant edits, user confirms, assistant deploys
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "user", "content": "fix den CLI Bug bitte"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "Ich schaue mir das an."},
			map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": "/app/cli_client.go"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "/app/cli_client.go"}},
		}},
		map[string]any{"role": "user", "content": "ja, deploy das"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "git commit -m \"fix cli bug\""}},
		}},
	}

	events := extractTimeline(msgs, 1, 6)

	// Expected: user msg, Edit, user msg, deploy, git commit
	// Read is skipped, assistant text is skipped
	expected := []string{"fix den CLI", "Edit:", "ja, deploy", "deploy", "git commit:"}
	for _, exp := range expected {
		found := false
		for _, e := range events {
			if strings.Contains(e, exp) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected event containing %q", exp)
		}
	}

	for _, e := range events {
		t.Logf("event: %s", e)
	}
}

func TestBuildArchiveBlock_IncludesTimeline(t *testing.T) {
	stats := compactionStats{
		ToolStats: "Read×5, Bash×3",
		FileStats: "stubify.go, collapse.go",
		Digests:   []string{"  [12] U: fix den Bug", "  [14] Edit: proxy/collapse.go", "  [16] git commit: fix collapse"},
	}

	block := buildArchiveBlock(1, 100, stats, "test-thread-123")

	if !strings.Contains(block, "Timeline:") {
		t.Error("expected Timeline section")
	}
	if !strings.Contains(block, "fix den Bug") {
		t.Error("expected user message in timeline")
	}
	if !strings.Contains(block, "git commit:") {
		t.Error("expected git commit in timeline")
	}
	// Decisions section should NOT be in archive (redundant with timeline)
	if strings.Contains(block, "Decisions:") {
		t.Error("Decisions section should be removed (user msgs are in timeline)")
	}
}

func TestBuildArchiveBlock_WithLearnings(t *testing.T) {
	stats := compactionStats{
		ToolStats: "Read×3",
		Learnings: []ArchiveLearning{
			{Category: "gotcha", Content: "Calibrator Default 0.15 war falsch", CreatedAt: time.Date(2026, 3, 12, 15, 0, 0, 0, time.UTC)},
			{Category: "unfinished", Content: "Timeline braucht noch Timestamps", CreatedAt: time.Date(2026, 3, 12, 15, 30, 0, 0, time.UTC)},
		},
	}

	block := buildArchiveBlock(1, 100, stats, "test-thread-123")

	if !strings.Contains(block, "Gotchas:") {
		t.Error("expected Gotchas section")
	}
	if !strings.Contains(block, "Calibrator Default") {
		t.Error("expected gotcha content")
	}
	if !strings.Contains(block, "Offen:") {
		t.Error("expected Offen section")
	}
	if !strings.Contains(block, "Timeline braucht") {
		t.Error("expected unfinished content")
	}
}

func TestExtractTimeline_YesmemMCPTools(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "mcp__yesmem__hybrid_search", "input": map[string]any{"query": "CLI extraction"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "mcp__yesmem__remember", "input": map[string]any{"text": "CLI bug fixed"}},
		}},
	}

	events := extractTimeline(msgs, 1, 2)

	hasSearch := false
	hasRemember := false
	for _, e := range events {
		if strings.Contains(e, "yesmem:hybrid_search") {
			hasSearch = true
		}
		if strings.Contains(e, "yesmem:remember") {
			hasRemember = true
		}
		t.Logf("event: %s", e)
	}

	if !hasSearch {
		t.Error("expected yesmem:hybrid_search event")
	}
	if !hasRemember {
		t.Error("expected yesmem:remember event")
	}
}

func TestExtractTimeline_DeduplicatesRepetitiveEvents(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "sys"},
		// 5 consecutive deploys
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make deploy"}},
		}},
		// User message breaks the streak
		map[string]any{"role": "user", "content": "hat es geklappt?"},
		// 4 SSH commands to same host
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ssh testhost@10.0.0.1 \"cat /tmp/test\""}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ssh testhost@10.0.0.1 \"ls -la\""}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ssh testhost@10.0.0.1 \"grep api_key config.yaml\""}},
		}},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ssh testhost@10.0.0.1 \"systemctl status yesmem\""}},
		}},
	}

	events := extractTimeline(msgs, 1, 11)

	for _, e := range events {
		t.Logf("event: %s", e)
	}

	// 5 deploys should be collapsed to 1 line with (5x)
	deployCount := 0
	hasDedup := false
	for _, e := range events {
		if strings.Contains(e, "deploy") {
			deployCount++
		}
		if strings.Contains(e, "(5x)") {
			hasDedup = true
		}
	}
	if deployCount != 1 {
		t.Errorf("expected 1 deploy event (deduped), got %d", deployCount)
	}
	if !hasDedup {
		t.Error("expected (5x) dedup marker for deploys")
	}

	// SSH commands should be collapsed too
	sshCount := 0
	for _, e := range events {
		if strings.Contains(e, "ssh") {
			sshCount++
		}
	}
	if sshCount != 1 {
		t.Errorf("expected 1 ssh event (deduped), got %d", sshCount)
	}

	// User message should NOT be collapsed
	userCount := 0
	for _, e := range events {
		if strings.Contains(e, "U:") {
			userCount++
		}
	}
	if userCount != 1 {
		t.Errorf("expected 1 user message, got %d", userCount)
	}
}

func TestExtractGitCommitMessage_HeredocFormat(t *testing.T) {
	// Heredoc format used by Claude Code
	cmd := "git commit -m \"$(cat <<'EOF'\nfix: CLI extraction parsing + fence stripping\n\nStrips markdown fences and extracts from JSON wrapper.\nEOF\n)\""
	msg := extractGitCommitMessage(cmd)
	if !strings.Contains(msg, "fix: CLI extraction") {
		t.Errorf("expected first line of heredoc commit, got: %q", msg)
	}
}

func TestExtractGitCommitMessage_SimpleFormat(t *testing.T) {
	cmd := `git commit -m "fix the typo in README"`
	msg := extractGitCommitMessage(cmd)
	if msg != "fix the typo in README" {
		t.Errorf("expected simple message, got: %q", msg)
	}
}

func TestExtractGitCommitMessage_SingleQuotes(t *testing.T) {
	cmd := "git commit -m 'add new feature'"
	msg := extractGitCommitMessage(cmd)
	if msg != "add new feature" {
		t.Errorf("expected single-quoted message, got: %q", msg)
	}
}
