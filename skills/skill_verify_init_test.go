package skills

import (
	"os"
	"strings"
	"testing"
)

// The profile init belongs to Phase 1, where the worktree is new and no verifier
// has been asked anything yet. The step has to name the file, the command and the
// commit, because an agent that only knows "init" will leave the file
// uncommitted and the merge carries nothing.
func TestYesloopSkillNamesTheVerifyProfileInit(t *testing.T) {
	data, err := os.ReadFile(yesloopSkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", yesloopSkillPath, err)
	}
	skill := string(data)
	phaseTwo := strings.Index(skill, "### Phase 2: PLAN")
	if phaseTwo < 0 {
		t.Fatalf("%s: no Phase 2 header, the pipeline changed shape", yesloopSkillPath)
	}
	phaseOne := skill[:phaseTwo]

	for _, want := range []string{
		".yesmem/verify.yaml",
		"verify-claims --init",
		"git add -f .yesmem/verify.yaml",
	} {
		if !strings.Contains(phaseOne, want) {
			t.Errorf("%s: Phase 1 does not name %q", yesloopSkillPath, want)
		}
	}
}
