package daemon

import (
	"os"
	"strings"
	"testing"
)

// --- YM-309 (Los 38l): the relay docs still describe the old first rung ---
//
// Since YM-306 the daemon picks the first rung's wording per delivery: the
// six-phase question goes to an agent that claims DONE or reports Phase 6, every
// other agent gets the mid-pipeline text. The docs kept quoting only the
// six-phase question as "State 0 -> 1", and the existing docs guard passed
// because it checks containment, not the choice.

// idleRelayChoiceDocMarkers names the sentence each doc must carry to state the
// choice. The two English files are quoted verbatim copies of the daemon's
// messages; the architecture note is German.
var idleRelayChoiceDocMarkers = map[string]string{
	"../../skills/bundled-skills/yesloop/SKILL.md":       "Relay 1 has two wordings",
	"../../skills/bundled-commands/opencode/yesloop.md":  "Relay 1 has two wordings",
	"../../yesdocs/architecture/yesloop-phase-guards.md": "Relay 1 hat zwei Fassungen",
}

// idleRelayChoiceDocCondition is the wording every doc has to put next to the
// marker: the seven-phase question is tied to a DONE claim and to a reported
// Phase 7. Checking the marker alone would pass a doc that names the choice and
// then states the opposite condition — the drift this ticket is about.
var idleRelayChoiceDocCondition = []string{"DONE", "Phase 7"}

// los38lParagraphContaining returns the blank-line-delimited block around marker.
// The docs wrap their sentences, so the conditions may sit a line below the
// marker.
func los38lParagraphContaining(doc, marker string) string {
	idx := strings.Index(doc, marker)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(doc[:idx], "\n\n")
	if start < 0 {
		start = 0
	}
	end := strings.Index(doc[idx:], "\n\n")
	if end < 0 {
		end = len(doc) - idx
	}
	return doc[start : idx+end]
}

// TestLos38l_FirstRelayWordingIsDocumented pins the daemon's choice and the
// docs that describe it to each other: the mid-pipeline text must be quoted
// verbatim where the relay texts are quoted, and every doc must name the
// condition under which the six-phase question is sent.
func TestLos38l_FirstRelayWordingIsDocumented(t *testing.T) {
	if got := idleRelayOneText("", "Phase 3/7 EXECUTE"); got != idleMidPipelineRelayMessage() {
		t.Errorf("a mid-pipeline agent must get the mid-pipeline text, got %q", got)
	}
	if got := idleRelayOneText("", "Phase 7/7 FINISH"); got != "" {
		t.Errorf("an agent that reports Phase 7 must get the seven-phase question (empty override), got %q", got)
	}
	// A loop that started under the six-block contract still reports its old
	// last phase; the question is the one it can answer by then as well.
	if got := idleRelayOneText("", "Phase 6/6 FINISH"); got != "" {
		t.Errorf("an agent that reports the legacy Phase 6 must get the question (empty override), got %q", got)
	}
	if got := idleRelayOneText("### Phase 7: FINISH\n**Status:** IN PROGRESS\nDONE: files committed", ""); got != "" {
		t.Errorf("an agent that claims DONE must get the seven-phase question (empty override), got %q", got)
	}

	for path, marker := range idleRelayChoiceDocMarkers {
		data, err := os.ReadFile(path)
		if err != nil {
			// The markers live in yesdocs/, which the public mirror does not carry.
			if os.IsNotExist(err) {
				t.Logf("%s is not part of this checkout, skipped", path)
				continue
			}
			t.Fatalf("read %s: %v", path, err)
		}
		doc := string(data)
		if !strings.Contains(doc, marker) {
			t.Errorf("%s does not name the first rung's choice (want %q); the docs still present the six-phase question as the unconditional first relay", path, marker)
			continue
		}
		para := los38lParagraphContaining(doc, marker)
		for _, cond := range idleRelayChoiceDocCondition {
			if !strings.Contains(para, cond) {
				t.Errorf("%s names the first rung's choice but not the condition %q next to it; naming the choice without its trigger is the drift this guard exists for", path, cond)
			}
		}
	}

	for _, path := range idleSkillDocs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		doc := string(data)
		if !strings.Contains(doc, idleMidPipelineRelayMessage()) {
			t.Errorf("%s quotes the relay texts but not the mid-pipeline wording; the docs drift from the daemon", path)
		}
		// The six-phase text stays, and it stays tied to its condition: a doc that
		// lists it as the plain "State 0 -> 1" message would re-invite the ticked
		// phases YM-306 fixed.
		if !strings.Contains(doc, idleRelayMessage(1)) {
			t.Errorf("%s must keep quoting the six-phase wording verbatim", path)
		}
	}
}
