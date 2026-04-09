package proxy

import (
	"strings"
	"testing"
)

// --- extractRecentToolCalls tests ---

func TestExtractRecentToolCalls_Empty(t *testing.T) {
	msgs := buildMessages()
	calls := extractRecentToolCalls(msgs, 12)
	if len(calls) != 0 {
		t.Errorf("expected 0 calls, got %d", len(calls))
	}
}

func TestExtractRecentToolCalls_SingleCall(t *testing.T) {
	msgs := buildMessages(
		userMsg("fix it"),
		assistantWithToolUse("Edit", "/src/main.go"),
		toolResult(false, "ok"),
	)
	calls := extractRecentToolCalls(msgs, 12)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	c := calls[0]
	if c.Tool != "Edit" {
		t.Errorf("expected Tool=Edit, got %q", c.Tool)
	}
	if c.FilePath != "/src/main.go" {
		t.Errorf("expected FilePath=/src/main.go, got %q", c.FilePath)
	}
	if c.IsError {
		t.Error("expected IsError=false")
	}
}

func TestExtractRecentToolCalls_MaxN(t *testing.T) {
	var parts []any
	for i := 0; i < 20; i++ {
		parts = append(parts, assistantWithToolUse("Bash", "go test"))
		parts = append(parts, toolResult(false, "ok"))
	}
	msgs := buildMessages(parts...)
	calls := extractRecentToolCalls(msgs, 12)
	if len(calls) != 12 {
		t.Errorf("expected 12 calls (maxN), got %d", len(calls))
	}
}

func TestExtractRecentToolCalls_WithErrors(t *testing.T) {
	msgs := buildMessages(
		assistantWithToolUse("Bash", "go build"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test"),
		toolResult(true, "FAIL: test failed"),
		assistantWithToolUse("Edit", "/src/fix.go"),
		toolResult(false, "edited"),
	)
	calls := extractRecentToolCalls(msgs, 12)
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if calls[0].IsError {
		t.Error("first call (go build) should not be error")
	}
	if !calls[1].IsError {
		t.Error("second call (go test) should be error")
	}
	if calls[1].ErrorMsg == "" {
		t.Error("expected non-empty ErrorMsg for failed call")
	}
	if calls[2].IsError {
		t.Error("third call (Edit) should not be error")
	}
}

// --- DetectLoop tests ---

func TestDetectLoop_NoCycle(t *testing.T) {
	msgs := buildMessages(
		assistantWithToolUse("Bash", "ls"),
		toolResult(false, "file.go"),
		assistantWithToolUse("Read", "/src/foo.go"),
		toolResult(false, "content"),
		assistantWithToolUse("Grep", "pattern"),
		toolResult(false, "match"),
		assistantWithToolUse("Edit", "/src/bar.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(false, "ok"),
	)
	sig := DetectLoop(msgs)
	if sig != nil {
		t.Errorf("expected nil, got signal type=%d desc=%q", sig.Type, sig.Description)
	}
}

func TestDetectLoop_IdenticalCycle2(t *testing.T) {
	// Edit(main.go) → Bash(go test) repeated 2x
	msgs := buildMessages(
		assistantWithToolUse("Edit", "main.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test"),
		toolResult(false, "ok"),
		assistantWithToolUse("Edit", "main.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test"),
		toolResult(false, "ok"),
	)
	sig := DetectLoop(msgs)
	if sig == nil {
		t.Fatal("expected LoopIdenticalCycle signal, got nil")
	}
	if sig.Type != LoopIdenticalCycle {
		t.Errorf("expected LoopIdenticalCycle, got %d", sig.Type)
	}
	if sig.CycleLen != 2 {
		t.Errorf("expected CycleLen=2, got %d", sig.CycleLen)
	}
	if sig.Repetitions < 2 {
		t.Errorf("expected Repetitions≥2, got %d", sig.Repetitions)
	}
}

func TestDetectLoop_IdenticalCycle3(t *testing.T) {
	// 3-step cycle: Read → Edit → Bash, repeated 2x
	msgs := buildMessages(
		assistantWithToolUse("Read", "config.go"),
		toolResult(false, "content"),
		assistantWithToolUse("Edit", "config.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test ./..."),
		toolResult(true, "FAIL"),
		assistantWithToolUse("Read", "config.go"),
		toolResult(false, "content"),
		assistantWithToolUse("Edit", "config.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test ./..."),
		toolResult(true, "FAIL"),
	)
	sig := DetectLoop(msgs)
	if sig == nil {
		t.Fatal("expected LoopIdenticalCycle signal, got nil")
	}
	if sig.Type != LoopIdenticalCycle {
		t.Errorf("expected LoopIdenticalCycle, got %d", sig.Type)
	}
	if sig.CycleLen != 3 {
		t.Errorf("expected CycleLen=3, got %d", sig.CycleLen)
	}
}

func TestDetectLoop_EditTestErrorCycle(t *testing.T) {
	// 3 cycles of Edit(same file) → Bash(go test) → Error
	var parts []any
	for i := 0; i < 3; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "main.go"))
		parts = append(parts, toolResult(false, "edited"))
		parts = append(parts, assistantWithToolUse("Bash", "go test ./..."))
		parts = append(parts, toolResult(true, "FAIL: compilation error"))
	}
	msgs := buildMessages(parts...)
	sig := DetectLoop(msgs)
	if sig == nil {
		t.Fatal("expected loop signal, got nil")
	}
	if sig.Type != LoopEditTestError && sig.Type != LoopIdenticalCycle {
		t.Errorf("expected LoopEditTestError or LoopIdenticalCycle, got %d", sig.Type)
	}
}

func TestDetectLoop_RepeatedSameError(t *testing.T) {
	// 3 identical error messages
	msgs := buildMessages(
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "undefined: Foo"),
		assistantWithToolUse("Edit", "main.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "undefined: Foo"),
		assistantWithToolUse("Edit", "main.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go build"),
		toolResult(true, "undefined: Foo"),
	)
	sig := DetectLoop(msgs)
	if sig == nil {
		t.Fatal("expected loop signal, got nil")
	}
	if sig.Type != LoopRepeatedError && sig.Type != LoopIdenticalCycle {
		t.Errorf("expected LoopRepeatedError or LoopIdenticalCycle, got %d", sig.Type)
	}
}

func TestDetectLoop_SimilarButDifferent(t *testing.T) {
	// Different files/commands — should not trigger
	msgs := buildMessages(
		assistantWithToolUse("Edit", "a.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test ./pkg/a/..."),
		toolResult(false, "ok"),
		assistantWithToolUse("Edit", "b.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test ./pkg/b/..."),
		toolResult(false, "ok"),
		assistantWithToolUse("Edit", "c.go"),
		toolResult(false, "ok"),
		assistantWithToolUse("Bash", "go test ./pkg/c/..."),
		toolResult(false, "ok"),
	)
	sig := DetectLoop(msgs)
	if sig != nil {
		t.Errorf("expected nil (different files/commands), got signal type=%d desc=%q", sig.Type, sig.Description)
	}
}

func TestDetectLoop_EditTestErrorCycle_VaryingContent(t *testing.T) {
	// Same file edited with different content each time, same test command fails.
	// detectIdenticalCycle should NOT fire (different ArgHashes for Edit).
	// detectEditTestErrorCycle SHOULD fire (same file, same build error pattern).
	msgs := buildMessages(
		userMsg("fix"),
		assistantWithToolUse("Edit", "broken.go"),   // edit 1
		toolResult(false, "edited v1"),
		assistantWithToolUse("Bash", "go test ./..."), // test → fail
		toolResult(true, "FAIL: TestBroken line 10"),
		assistantWithToolUse("Write", "broken.go"),   // edit 2 (Write, not Edit — different hash)
		toolResult(false, "wrote v2"),
		assistantWithToolUse("Bash", "go test ./..."), // test → fail
		toolResult(true, "FAIL: TestBroken line 15"),
		assistantWithToolUse("Edit", "broken.go"),    // edit 3
		toolResult(false, "edited v3"),
		assistantWithToolUse("Bash", "go test ./..."), // test → fail
		toolResult(true, "FAIL: TestBroken line 20"),
	)
	sig := DetectLoop(msgs)
	if sig == nil {
		t.Fatal("expected loop detection for varying-content edit-test-error cycle")
	}
	if sig.Type != LoopEditTestError {
		t.Errorf("expected LoopEditTestError specifically (not IdenticalCycle), got %v", sig.Type)
	}
	if sig.Repetitions < 3 {
		t.Errorf("expected ≥3 repetitions, got %d", sig.Repetitions)
	}
}

// --- FormatLoopWarning tests ---

func TestFormatLoopWarning_Level1(t *testing.T) {
	sig := &LoopSignal{
		Type:        LoopRepeatedError,
		CycleLen:    1,
		Repetitions: 3,
		Description: "same error repeated 3 times",
	}
	w := FormatLoopWarning(sig, 1)
	if !strings.Contains(w, "[YesMem Loop Detection]") {
		t.Errorf("expected [YesMem Loop Detection] prefix, got: %q", w)
	}
	if !strings.Contains(w, "Step back") {
		t.Errorf("expected 'Step back' in level-1 warning, got: %q", w)
	}
}

func TestFormatLoopWarning_Level2(t *testing.T) {
	sig := &LoopSignal{
		Type:        LoopEditTestError,
		CycleLen:    3,
		Repetitions: 3,
		Description: "Edit→Test→Error cycle on main.go repeated 3 times",
	}
	w := FormatLoopWarning(sig, 2)
	if !strings.Contains(w, "[YesMem Loop Detection]") {
		t.Errorf("expected [YesMem Loop Detection] prefix, got: %q", w)
	}
	if !strings.Contains(w, "previous warning") {
		t.Errorf("expected 'previous warning' in level-2 warning, got: %q", w)
	}
	if !strings.Contains(w, "ask the user") {
		t.Errorf("expected 'ask the user' in level-2 warning, got: %q", w)
	}
}

func TestFormatLoopWarning_Level3Plus(t *testing.T) {
	sig := &LoopSignal{
		Type:        LoopIdenticalCycle,
		CycleLen:    2,
		Repetitions: 4,
		Description: "identical 2-step cycle repeated 4 times",
	}
	w := FormatLoopWarning(sig, 3)
	if !strings.Contains(w, "[YesMem Loop Detection]") {
		t.Errorf("expected [YesMem Loop Detection] prefix, got: %q", w)
	}
	if !strings.Contains(w, "persistent loop") {
		t.Errorf("expected 'persistent loop' in level-3 warning, got: %q", w)
	}
}

// --- LoopState tests ---

func TestLoopState_Fresh(t *testing.T) {
	s := &LoopState{}
	if s.WarningCount != 0 {
		t.Errorf("expected WarningCount=0, got %d", s.WarningCount)
	}
	if s.InCooldown() {
		t.Error("fresh state should not be in cooldown")
	}
}

func TestLoopState_Cooldown(t *testing.T) {
	s := &LoopState{}
	s.RecordWarning()
	if !s.InCooldown() {
		t.Error("expected in cooldown after RecordWarning")
	}
	// Tick loopCooldownRequests times → cooldown should clear
	for i := 0; i < loopCooldownRequests; i++ {
		s.Tick()
	}
	if s.InCooldown() {
		t.Error("expected cooldown cleared after loopCooldownRequests ticks")
	}
}

func TestLoopState_Escalation(t *testing.T) {
	s := &LoopState{}
	s.RecordWarning()
	// drain cooldown
	for i := 0; i < loopCooldownRequests; i++ {
		s.Tick()
	}
	s.RecordWarning()
	if s.WarningCount != 2 {
		t.Errorf("expected WarningCount=2, got %d", s.WarningCount)
	}
}

// --- CheckLoopAndFormat tests ---

func TestCheckLoopAndFormat_NoLoop(t *testing.T) {
	msgs := buildMessages(
		assistantWithToolUse("Bash", "ls"),
		toolResult(false, "file.go"),
		assistantWithToolUse("Read", "/src/foo.go"),
		toolResult(false, "content"),
	)
	s := &LoopState{}
	w, level := CheckLoopAndFormat(msgs, s)
	if w != "" || level != 0 {
		t.Errorf("expected (\"\", 0), got (%q, %d)", w, level)
	}
}

func TestCheckLoopAndFormat_DetectsAndFormats(t *testing.T) {
	// Edit→Test→Error cycle 3x
	var parts []any
	for i := 0; i < 3; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "main.go"))
		parts = append(parts, toolResult(false, "edited"))
		parts = append(parts, assistantWithToolUse("Bash", "go test ./..."))
		parts = append(parts, toolResult(true, "FAIL: compilation error"))
	}
	msgs := buildMessages(parts...)
	s := &LoopState{}
	w, level := CheckLoopAndFormat(msgs, s)
	if w == "" {
		t.Error("expected non-empty warning for looping messages")
	}
	if level != 1 {
		t.Errorf("expected level=1 on first detection, got %d", level)
	}
	if s.WarningCount != 1 {
		t.Errorf("expected WarningCount=1 after detection, got %d", s.WarningCount)
	}
	if !strings.Contains(w, "[YesMem Loop Detection]") {
		t.Errorf("expected [YesMem Loop Detection] in warning, got: %q", w)
	}
}

func TestCheckLoopAndFormat_RespectsCooldown(t *testing.T) {
	// Put state in cooldown
	s := &LoopState{}
	s.RecordWarning()
	// looping messages — should be suppressed by cooldown
	var parts []any
	for i := 0; i < 3; i++ {
		parts = append(parts, assistantWithToolUse("Edit", "main.go"))
		parts = append(parts, toolResult(false, "edited"))
		parts = append(parts, assistantWithToolUse("Bash", "go test ./..."))
		parts = append(parts, toolResult(true, "FAIL: compilation error"))
	}
	msgs := buildMessages(parts...)
	w, level := CheckLoopAndFormat(msgs, s)
	if w != "" || level != 0 {
		t.Errorf("expected (\"\", 0) during cooldown, got (%q, %d)", w, level)
	}
}
