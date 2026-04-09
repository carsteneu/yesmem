package proxy

import (
	"testing"
)

func TestDecayTracker_BasicStages(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0) // message 5 stubbed at request 10, no intensity

	// Stage 0: fresh (age < 5)
	if stage := dt.GetStage(5, 14, 100, 2.5); stage != DecayStage0 {
		t.Errorf("age 4: expected stage 0, got %d", stage)
	}

	// Stage 1: middle (age 5-15)
	if stage := dt.GetStage(5, 20, 100, 2.5); stage != DecayStage1 {
		t.Errorf("age 10: expected stage 1, got %d", stage)
	}

	// Stage 2: old (age 15-50)
	if stage := dt.GetStage(5, 30, 100, 2.5); stage != DecayStage2 {
		t.Errorf("age 20: expected stage 2, got %d", stage)
	}

	// Stage 3: compacted (age > 50)
	if stage := dt.GetStage(5, 70, 100, 2.5); stage != DecayStage3 {
		t.Errorf("age 60: expected stage 3, got %d", stage)
	}
}

func TestDecayTracker_EmotionalBoost(t *testing.T) {
	// Without emotional boost: age 8 → stage 1 (s0end=5)
	dt1 := NewDecayTracker()
	dt1.MarkStubbed(5, 10, 0.0)
	if stage := dt1.GetStage(5, 18, 100, 2.5); stage != DecayStage1 {
		t.Errorf("no boost, age 8: expected stage 1, got %d", stage)
	}

	// With high emotional boost (0.8 → +16 requests): age 8 → still stage 0
	dt2 := NewDecayTracker()
	dt2.MarkStubbed(5, 10, 0.8)
	if stage := dt2.GetStage(5, 18, 100, 2.5); stage != DecayStage0 {
		t.Errorf("with boost 0.8, age 8: expected stage 0, got %d", stage)
	}

	// With max boost (1.0 → +20): age 20 → s0end=5+20=25, so still stage 0
	dt3 := NewDecayTracker()
	dt3.MarkStubbed(5, 10, 1.0)
	if stage := dt3.GetStage(5, 30, 50, 2.5); stage != DecayStage0 {
		t.Errorf("with boost 1.0, age 20: expected stage 0, got %d", stage)
	}
}

func TestDecayTracker_UnknownMessage(t *testing.T) {
	dt := NewDecayTracker()
	// Message never stubbed → stage 0
	if stage := dt.GetStage(99, 100, 100, 2.5); stage != DecayStage0 {
		t.Errorf("unknown message: expected stage 0, got %d", stage)
	}
}

func TestDecayTracker_OnlyFirstStubCounts(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)
	dt.MarkStubbed(5, 50, 0.8) // second call should not overwrite

	// Age should be based on first stub (request 10), not second (50)
	// Intensity should be 0.0 (first stub), not 0.8
	// Age = 18 - 10 = 8, s0end=5, s1end=15 → stage 1
	if stage := dt.GetStage(5, 18, 100, 2.5); stage != DecayStage1 {
		t.Errorf("expected stage 1 based on first stub, got %d", stage)
	}
}

func TestApplyDecayToToolStub(t *testing.T) {
	stub := "[→] Read /home/user/main.go — found 15 switch cases, adding proxy case"

	// Stage 0: unchanged
	s0 := ApplyDecayToToolStub(stub, DecayStage0)
	if s0 != stub {
		t.Errorf("stage 0 should be unchanged, got: %q", s0)
	}

	// Stage 1: annotation shortened to first 3 words
	s1 := ApplyDecayToToolStub(stub, DecayStage1)
	if s1 == stub {
		t.Error("stage 1 should differ from original")
	}
	if !containsStr(s1, "Read /home/user/main.go") {
		t.Errorf("stage 1 should keep path: %q", s1)
	}
	if containsStr(s1, "adding proxy case") {
		t.Errorf("stage 1 should have truncated annotation: %q", s1)
	}

	// Stage 2: no annotation at all
	s2 := ApplyDecayToToolStub(stub, DecayStage2)
	if containsStr(s2, "found") {
		t.Errorf("stage 2 should have no annotation: %q", s2)
	}
	if !containsStr(s2, "Read /home/user/main.go") {
		t.Errorf("stage 2 should keep path: %q", s2)
	}
}

func TestApplyDecay_TextMessages(t *testing.T) {
	longText := longString(300) // 300 chars

	// Stage 0: unchanged
	s0 := ApplyDecay(longText, DecayStage0, "assistant")
	if s0 != longText {
		t.Error("stage 0 should not modify text")
	}

	// Stage 1: truncated to 120 for assistant
	s1 := ApplyDecay(longText, DecayStage1, "assistant")
	if len([]rune(s1)) > 124 { // 120 + "..."
		t.Errorf("stage 1 assistant should truncate to ~120, got %d", len([]rune(s1)))
	}

	// Stage 1: truncated to 200 for user
	s1u := ApplyDecay(longText, DecayStage1, "user")
	if len([]rune(s1u)) > 204 {
		t.Errorf("stage 1 user should truncate to ~200, got %d", len([]rune(s1u)))
	}

	// Stage 2: truncated to 50 for assistant
	s2 := ApplyDecay(longText, DecayStage2, "assistant")
	if len([]rune(s2)) > 54 {
		t.Errorf("stage 2 assistant should truncate to ~50, got %d", len([]rune(s2)))
	}

	// Stage 2: truncated to 80 for user
	s2u := ApplyDecay(longText, DecayStage2, "user")
	if len([]rune(s2u)) > 84 {
		t.Errorf("stage 2 user should truncate to ~80, got %d", len([]rune(s2u)))
	}
}

// --- Adaptive Decay Boundaries ---

func TestDecayBoundaries_Aggressive(t *testing.T) {
	// All thread lengths should now have much lower boundaries
	s0, s1, s2 := decayBoundaries(50, 2.5)
	if s0 > 10 || s1 > 25 || s2 > 100 {
		t.Errorf("short thread too conservative: (%d,%d,%d)", s0, s1, s2)
	}

	s0, s1, s2 = decayBoundaries(300, 2.5)
	if s0 > 8 || s1 > 20 || s2 > 60 {
		t.Errorf("medium thread too conservative: (%d,%d,%d)", s0, s1, s2)
	}
}

func TestApplyDecayToToolStub_Stage2_NoDeepSearch(t *testing.T) {
	stub := "[→] Read /home/user/main.go — found stuff → deep_search('Read /home/user/main.go')"
	result := ApplyDecayToToolStub(stub, DecayStage2)
	if contains(result, "deep_search") {
		t.Errorf("stage 2 should strip deep_search hints, got: %s", result)
	}
	if !contains(result, "Read") {
		t.Error("stage 2 should keep tool name")
	}
}

func TestDecayBoundaries_ShortThread(t *testing.T) {
	// threadLen < 500 → base (5,15,50)
	s0, s1, s2 := decayBoundaries(50, 2.5)
	if s0 != 5 || s1 != 15 || s2 != 50 {
		t.Errorf("short thread: expected (5,15,50), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_MediumThread(t *testing.T) {
	// threadLen < 500 → same base (5,15,50)
	s0, s1, s2 := decayBoundaries(300, 2.5)
	if s0 != 5 || s1 != 15 || s2 != 50 {
		t.Errorf("medium thread: expected (5,15,50), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_LongThread(t *testing.T) {
	s0, s1, s2 := decayBoundaries(1500, 2.5)
	if s0 != 5 || s1 != 12 || s2 != 40 {
		t.Errorf("long thread: expected (5,12,40), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_VeryLongThread(t *testing.T) {
	s0, s1, s2 := decayBoundaries(5000, 2.5)
	if s0 != 4 || s1 != 10 || s2 != 30 {
		t.Errorf("very long thread: expected (4,10,30), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestGetStage_WithThreadLen_ReachesStage3(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)

	// Thread of 1500 messages → boundaries (5, 12, 40)
	// Age = 60 - 10 = 50 → > 40 → stage 3
	stage := dt.GetStage(5, 60, 1500, 2.5)
	if stage != DecayStage3 {
		t.Errorf("long thread, age 50: expected stage 3, got %d", stage)
	}
}

func TestGetStage_ShortThreadReachesStage3(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)

	// Thread of 50 messages → boundaries (5, 15, 50)
	// Age = 500 - 10 = 490 → > 50 → stage 3
	stage := dt.GetStage(5, 500, 50, 2.5)
	if stage != DecayStage3 {
		t.Errorf("short thread, age 490: expected stage 3, got %d", stage)
	}
}

func TestApplyDecay_Stage3_ReturnsEmpty(t *testing.T) {
	stub := "some long text content that should vanish at stage 3"
	result := ApplyDecay(stub, DecayStage3, "assistant")
	if result != "" {
		t.Errorf("stage 3 should return empty, got: %q", result)
	}
}

func TestDecayTracker_PinnedPath_SlowsDecay(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)
	dt.SetFilePath(5, "/home/user/memory/yesmem/yesdocs/plans/my-plan.md")
	dt.SetPinnedPaths([]string{"yesdocs/plans/my-plan.md"})

	// Without pinning: age 8, s0end=5 → stage 1
	// With pinning: +30 boost → s0end=5+30=35, age 8 → still stage 0
	if stage := dt.GetStage(5, 18, 100, 2.5); stage != DecayStage0 {
		t.Errorf("pinned path, age 8: expected stage 0, got %d", stage)
	}

	// Even at age 30, should still be stage 0 (s0end=5+30=35)
	if stage := dt.GetStage(5, 40, 100, 2.5); stage != DecayStage0 {
		t.Errorf("pinned path, age 30: expected stage 0, got %d", stage)
	}

	// At age 40, stage 1 (s1end=15+30=45)
	if stage := dt.GetStage(5, 50, 100, 2.5); stage != DecayStage1 {
		t.Errorf("pinned path, age 40: expected stage 1, got %d", stage)
	}
}

func TestDecayTracker_PinnedPath_SuffixMatch(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)
	dt.SetFilePath(5, "/home/user/memory/yesmem/internal/proxy/proxy.go")
	dt.SetPinnedPaths([]string{"proxy/proxy.go"})

	// Suffix match should work
	if stage := dt.GetStage(5, 18, 100, 2.5); stage != DecayStage0 {
		t.Errorf("suffix-matched pinned path, age 8: expected stage 0, got %d", stage)
	}
}

func TestDecayTracker_UnpinnedPath_NormalDecay(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)
	dt.SetFilePath(5, "/home/user/some/other/file.go")
	dt.SetPinnedPaths([]string{"proxy/proxy.go"})

	// Not pinned → normal decay, age 8 → stage 1
	if stage := dt.GetStage(5, 18, 100, 2.5); stage != DecayStage1 {
		t.Errorf("unpinned path, age 8: expected stage 1, got %d", stage)
	}
}

func TestApplyDecayToToolStub_Stage3_ReturnsEmpty(t *testing.T) {
	stub := "[→] Read /home/user/main.go — found 15 switch cases"
	result := ApplyDecayToToolStub(stub, DecayStage3)
	if result != "" {
		t.Errorf("stage 3 tool stub should return empty, got: %q", result)
	}
}

// --- Pressure Stretch Tests ---

func TestDecayBoundaries_LowPressure_StretchesBoundaries(t *testing.T) {
	// pressure=1.0 → stretch = 1.0 + (2.5-1.0)/0.75 = 3.0
	s0, s1, s2 := decayBoundaries(100, 1.0)
	if s0 != 15 || s1 != 45 || s2 != 150 {
		t.Errorf("pressure 1.0: expected (15,45,150), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_ZeroPressure_ClampedStretch(t *testing.T) {
	// pressure=0 → stretch = 1.0 + 2.5/0.75 = 4.33 → clamped to 4.0
	s0, s1, s2 := decayBoundaries(100, 0)
	if s0 != 20 || s1 != 60 || s2 != 200 {
		t.Errorf("pressure 0: expected (20,60,200), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_MidPressure(t *testing.T) {
	// pressure=1.75 → stretch = 1.0 + (2.5-1.75)/0.75 = 2.0
	s0, s1, s2 := decayBoundaries(100, 1.75)
	if s0 != 10 || s1 != 30 || s2 != 100 {
		t.Errorf("pressure 1.75: expected (10,30,100), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestDecayBoundaries_HighPressure_NoStretch(t *testing.T) {
	// pressure=3.0 → stretch = 1.0 (no stretch above 2.5)
	s0, s1, s2 := decayBoundaries(100, 3.0)
	if s0 != 5 || s1 != 15 || s2 != 50 {
		t.Errorf("pressure 3.0: expected (5,15,50), got (%d,%d,%d)", s0, s1, s2)
	}
}

func TestGetStage_PressureChangesStage(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.0)

	// Age = 25-10 = 15. At pressure=2.5: s0end=5, s1end=15 → stage 2
	highP := dt.GetStage(5, 25, 100, 2.5)
	if highP != DecayStage2 {
		t.Errorf("high pressure, age 15: expected stage 2, got %d", highP)
	}

	// Same age at pressure=1.0: s0end=15, s1end=45 → stage 1
	lowP := dt.GetStage(5, 25, 100, 1.0)
	if lowP != DecayStage1 {
		t.Errorf("low pressure, age 15: expected stage 1, got %d", lowP)
	}

	// Same age at pressure=0: s0end=20, s1end=60 → stage 0
	zeroP := dt.GetStage(5, 25, 100, 0)
	if zeroP != DecayStage0 {
		t.Errorf("zero pressure, age 15: expected stage 0, got %d", zeroP)
	}
}

func TestEstimateIntensity(t *testing.T) {
	// Calm session: no errors, few tools, short messages
	calm := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
	}
	if intensity := estimateIntensity(calm); intensity != 0.0 {
		t.Errorf("calm session: expected 0.0, got %f", intensity)
	}

	// Debugging session: errors in tool results
	debugging := []any{
		map[string]any{"role": "user", "content": "fix this"},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":    "tool_result",
					"is_error": true,
					"content": "command failed",
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":    "tool_result",
					"content": "exit code 1",
				},
			},
		},
	}
	intensity := estimateIntensity(debugging)
	if intensity < 0.2 {
		t.Errorf("debugging session: expected >= 0.2, got %f", intensity)
	}
}
