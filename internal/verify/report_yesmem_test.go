package verify

import "testing"

// The two yesmem markers reach their checkers only through the registry and the
// parser. A missing registry entry would turn the claim into an unknown marker,
// which reads as a defect against the agent, so the wiring is worth one test at
// the seam where it is decided.
func TestYesmemMarkersParse(t *testing.T) {
	claims := ParseReport("[EVIDENCE: cold-review=ses_a, consequence=ses_b]\n[PLAN: yesmem]\n")

	byKind := map[string]int{}
	for _, claim := range claims {
		byKind[claim.Kind]++
	}
	if byKind["evidence_ids"] != 1 || byKind["plan_items"] != 1 {
		t.Fatalf("kinds = %v, want one evidence_ids and one plan_items", byKind)
	}
	if byKind["unknown_marker"] != 0 {
		t.Errorf("unknown markers = %d, want 0: both tokens must be claimed", byKind["unknown_marker"])
	}
	for _, claim := range claims {
		switch claim.Kind {
		case "evidence_ids":
			if len(claim.Evidence) != 2 || claim.Evidence[0].Key != "cold-review" || claim.Evidence[1].ID != "ses_b" {
				t.Errorf("evidence refs = %v, want cold-review/ses_a and consequence/ses_b", claim.Evidence)
			}
		case "plan_items":
			if claim.Plan != "yesmem" {
				t.Errorf("plan = %q, want %q", claim.Plan, "yesmem")
			}
		}
	}
}
