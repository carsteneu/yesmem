package daemon

import (
	"strings"
	"testing"
)

// agent233Content is the actual scratchpad content from agent-233
// (running yesloop-setup-detection-first). It exhibits the pattern:
//   - Status on same line as phase header (→ **Status:** COMPLETE)
//   - Phase 5 missing **Stage 2: Cold Review** subsection header
//   - Phase 6 missing `send_to orchestrator:`
//   - Only phases 5 and 6 present (missing 1-4)
const agent233Content = `### Phase 5: REVIEW → **Status:** COMPLETE
  - Stage 1 Self-Review: found dead code in runOpenCodeSetup (no-op if condition) → fixed
  - Stage 2 Cold Review via task(): dispatched task() subagent for independent review
  - task() dispatched: yes
  - Findings: 9 analyzed, 1 Minor (dead code) fixed, rest either inapplicable or acceptable
  - Assessment: Ready to commit

### Phase 6: FINISH → **Status:** COMPLETE
  - Deploy required: no (feature branch, not merged to main)
  - Committed: a2e487c9 — feat(setup): detection-first setup wizard + fix 4 template bugs
  - Pushed: yesloop/setup-detection-first → origin
  - PR: https://example.com/pr
  - All tests pass`

// validV3Content is a complete, compliant yesloop scratchpad matching the
// v3 phase-block contract. All 6 phases present with required fields.
const validV3Content = `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Replace LLM-based DONE-guard with Go regex-validator
**Session id:** ses_test123
**Codebase explored:** internal/daemon/
**Constraints identified:** deterministic validation, no LLM dependency
**Risks:** false positives
**Open questions:** none

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Milestones:** 2 total
**Files in scope:** done_gate.go, heartbeat.go
**Test strategy:** table-driven unit tests

### Phase 3: EXECUTE
**Status:** COMPLETE
**Plan items:** 4 total

#### Milestone 1: done_gate.go + tests
**Status:** COMPLETE
**Commits:** abc1234 feat(done_gate): add DONE-guard validator
**Steps completed:** 4/4

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** go test ./internal/daemon/... → exit 0
**Lint/type-check:** go vet ./... → ok
**Build:** go build ./... → success

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 1: Self-Review**
**Strengths:** clean regex API, good test coverage
**Issues:** Critical (0) / Important (0) / Minor (1)
**Recommendations:** n/a
**Assessment:** Yes

**Stage 2: Cold Review via task()**
**task() dispatched:** yes
**Subagent ID:** agent-235
**Findings:** none
**Merged assessment:** Yes
**Fix commits:** none needed
**Security:** none — diff reviewed, no HIGH/MEDIUM/LOW findings

**REVIEW→VERIFY cycles used:** 1/3

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** yes (Go code change)
**Deploy executed:** yes — make build → binary updated
**PR created:** yes — https://example.com/pr
**Worktree:** kept
**send_to orchestrator:** yes — 2026-06-22T21:00:00
**set_plan complete:** yes`

// partialContent is an agent that's still working (Phase 3 IN PROGRESS)
var partialContent = `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Some task
**Session id:** ses_test456
**Codebase explored:** some files

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** some files
**Test strategy:** unit tests

### Phase 3: EXECUTE
**Status:** IN PROGRESS
**Plan items:** 5 total
**Total progress:** 2/5 steps
`

func TestValidatePhaseBlocks_Agent233_Fails(t *testing.T) {
	result := ValidatePhaseBlocks(agent233Content)
	if result.Compliant {
		t.Errorf("agent-233 content should NOT be compliant, got Compliant: true\n%s", result.String())
	}
	if len(result.MissingPhases) < 4 {
		t.Errorf("expected at least 4 missing phases (1-4), got %v", result.MissingPhases)
	}
	if len(result.FieldErrors) < 2 {
		t.Errorf("expected at least 2 field errors (Phase 5 + Phase 6), got %d: %v", len(result.FieldErrors), result.FieldErrors)
	}
	t.Logf("Agent-233 validation:\n%s", result.String())
}

func TestValidatePhaseBlocks_ValidV3_Passes(t *testing.T) {
	result := ValidatePhaseBlocks(validV3Content)
	if !result.Compliant {
		t.Errorf("valid v3 content should be compliant, got:\n%s", result.String())
	}
	if result.PhaseCount != 6 {
		t.Errorf("expected 6 phases, got %d", result.PhaseCount)
	}
}

func TestValidatePhaseBlocks_Empty(t *testing.T) {
	result := ValidatePhaseBlocks("")
	if result.Compliant {
		t.Error("empty content should not be compliant")
	}
	if len(result.MissingPhases) != 6 {
		t.Errorf("expected 6 missing phases for empty content, got %v", result.MissingPhases)
	}
}

func TestValidatePhaseBlocks_PartialProgress(t *testing.T) {
	// Agent still working (Phase 3 IN PROGRESS) — missing phases 4-6 is
	// a validation failure, but the heartbeat integration will only escalate
	// when ALL 6 phases are present (agent claimed DONE).
	result := ValidatePhaseBlocks(partialContent)
	if result.Compliant {
		t.Error("partial content should NOT be compliant (missing phases 4-6)")
	}
	if len(result.MissingPhases) != 3 {
		t.Errorf("expected 3 missing phases (4,5,6), got %v", result.MissingPhases)
	}
	// The IN PROGRESS phase should validate with just **Status:**
	// (no field errors for Phase 3 since it only requires **Status:**)
	for _, fe := range result.FieldErrors {
		if fe.Phase == 3 {
			t.Errorf("Phase 3 with IN PROGRESS should not have field errors: %v", fe)
		}
	}
}

func TestValidatePhaseBlocks_MissingPhase5Stage2(t *testing.T) {
	content := `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** test
**Session id:** ses_test789
**Codebase explored:** test

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** test
**Test strategy:** test

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** test

### Phase 5: REVIEW
**Status:** COMPLETE
**task() dispatched:** yes
**Subagent ID:** agent-x
**Findings:** none

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** no
**send_to orchestrator:** yes`
	// Note: Phase 5 is missing **Stage 2: Cold Review** subsection header
	result := ValidatePhaseBlocks(content)
	if result.Compliant {
		t.Error("content missing Phase 5 Stage 2 header should not be compliant")
	}
	if len(result.FieldErrors) == 0 {
		t.Error("expected field error for missing Stage 2 header")
	}
	t.Logf("Missing Stage 2:\n%s", result.String())
}

// TestValidatePhaseBlocks_MissingPhase5Security: Phase 5 without **Security:**
// field must fail validation per the security-review skill integration.
func TestValidatePhaseBlocks_MissingPhase5Security(t *testing.T) {
	content := `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** test
**Codebase explored:** test

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** test
**Test strategy:** test

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** test

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 2: Cold Review via task()**
**task() dispatched:** yes
**Subagent ID:** agent-x
**Findings:** none
**Merged assessment:** Yes
**Fix commits:** none needed

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** no
**send_to orchestrator:** yes`
	// Note: Phase 5 has Stage 2 but is missing **Security:** field
	result := ValidatePhaseBlocks(content)
	if result.Compliant {
		t.Error("content missing Phase 5 **Security:** field should not be compliant")
	}
	found := false
	for _, fe := range result.FieldErrors {
		if fe.Phase == 5 && strings.Contains(fe.Field, "Security") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected field error for missing **Security:** in Phase 5, got: %v", result.FieldErrors)
	}
	t.Logf("Missing Security:\n%s", result.String())
}

// TestValidatePhaseBlocks_Phase5SecurityEdgeCases: the **Security:** regex
// (?m)^\*\*Security:\*\*\s+\S must reject whitespace-only values and indented
// placements. Guards against a future "let me make this more lenient" regression.
func TestValidatePhaseBlocks_Phase5SecurityEdgeCases(t *testing.T) {
	base := func(securityLine string) string {
		return `### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** test
**Codebase explored:** test

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Files in scope:** test
**Test strategy:** test

### Phase 3: EXECUTE
**Status:** COMPLETE

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** test

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 2: Cold Review via task()**
**task() dispatched:** yes
**Subagent ID:** agent-x
**Findings:** none
` + securityLine + `
**Merged assessment:** Yes

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** no
**send_to orchestrator:** yes`
	}

	t.Run("whitespace_only_value_rejected", func(t *testing.T) {
		// **Security:** followed by only spaces/tabs then newline — \S must reject.
		result := ValidatePhaseBlocks(base("**Security:**   "))
		if result.Compliant {
			t.Error("whitespace-only **Security:** value should not be compliant")
		}
	})

	t.Run("indented_placement_rejected", func(t *testing.T) {
		// Leading whitespace before **Security:** breaks the (?m)^ anchor.
		result := ValidatePhaseBlocks(base("  **Security:** none"))
		if result.Compliant {
			t.Error("indented **Security:** line should not be compliant (anchor is ^)")
		}
	})

	t.Run("inline_prefixed_rejected", func(t *testing.T) {
		// Text before **Security:** on same line — must not false-positive.
		result := ValidatePhaseBlocks(base("note: **Security:** none"))
		if result.Compliant {
			t.Error("inline-prefixed **Security:** should not be compliant")
		}
	})
}

func TestCountCompletedPhases_AllComplete(t *testing.T) {
	count := CountCompletedPhases(validV3Content)
	if count != 6 {
		t.Errorf("expected 6 completed phases, got %d", count)
	}
}

func TestCountCompletedPhases_PartialProgress(t *testing.T) {
	count := CountCompletedPhases(partialContent)
	if count != 2 {
		t.Errorf("expected 2 completed phases (1+2), got %d", count)
	}
}

func TestCountCompletedPhases_Agent233_Count(t *testing.T) {
	count := CountCompletedPhases(agent233Content)
	// Agent-233 has **Status:** inline with the phase header, which our regex
	// correctly doesn't match (status must be on its own line per v3 spec).
	if count != 0 {
		t.Errorf("expected 0 completed phases (inline status not valid), got %d", count)
	}
}

func TestCountCompletedPhases_Empty(t *testing.T) {
	count := CountCompletedPhases("")
	if count != 0 {
		t.Errorf("expected 0 completed phases for empty content, got %d", count)
	}
}
