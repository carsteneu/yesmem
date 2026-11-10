package skills

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The campaign skill is the entry point for the yesmem bugfix campaign: where to
// start, in which order to work, what the orchestrator must check, and what it
// may not do without asking. Its value is exactly the part a rewrite drops
// first, so the anchors are pinned here instead of trusted. Progress and dates
// stay out of the text (campaign state lives in the learning chain and in Jira),
// which the last test enforces.
const bugfixKampagneSkillPath = "bundled-skills/bugfix-kampagne/SKILL.md"

func readKampagneSkill(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(bugfixKampagneSkillPath)
	if err != nil {
		t.Fatalf("read %s: %v", bugfixKampagneSkillPath, err)
	}
	return string(data)
}

// kampagneSection returns one "## " section body, so an anchor cannot be
// satisfied by a sentence that drifted into a different section.
func kampagneSection(t *testing.T, skill, header string) string {
	t.Helper()
	i := strings.Index(skill, header)
	if i < 0 {
		t.Fatalf("%s: section %q missing", bugfixKampagneSkillPath, header)
	}
	rest := skill[i+len(header):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// kampagneFrontmatter reads the name and description fields the way
// loadSkillCatalog does, without importing the setup package: the catalogue only
// needs these two fields, and the skill must survive that reading.
func kampagneFrontmatter(t *testing.T, skill string) (name, description string) {
	t.Helper()
	if !strings.HasPrefix(skill, "---\n") {
		t.Fatalf("%s: no frontmatter, the catalogue skips the skill", bugfixKampagneSkillPath)
	}
	end := strings.Index(skill[4:], "\n---")
	if end < 0 {
		t.Fatalf("%s: unclosed frontmatter", bugfixKampagneSkillPath)
	}
	for _, line := range strings.Split(skill[4:4+end], "\n") {
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, description
}

// installed by InstallBundledSkills to ~/.claude/skills/bugfix-kampagne and
// discovered by loadSkillCatalog through its frontmatter. A name or description
// that does not carry the campaign wording is invisible to both.
func TestBugfixKampagneSkillIsDiscoverable(t *testing.T) {
	name, description := kampagneFrontmatter(t, readKampagneSkill(t))

	if name != "bugfix-kampagne" {
		t.Errorf("name = %q, want %q (the install directory and the catalog name)", name, "bugfix-kampagne")
	}
	for _, want := range []string{"Bugfix-Kampagne", "Jira YM", "yesloop"} {
		if !strings.Contains(description, want) {
			t.Errorf("description does not name %q, the skill stays unfindable: %q", want, description)
		}
	}
}

func TestBugfixKampagneSkillNamesTheEntry(t *testing.T) {
	entry := kampagneSection(t, readKampagneSkill(t), "## Einstieg")

	for _, want := range []string{"102019", "get_learnings", "ses_ebc14c083ffeX3thcK0w81q4OZ", "YM-1", "164"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry section does not name %q", want)
		}
	}
	// The learning chain is read through its head, not through a snapshot: the
	// head may be superseded and the successor carries the current state.
	if !strings.Contains(entry, "superseded") {
		t.Errorf("entry section does not say what to do when the learning is superseded")
	}
}

func TestBugfixKampagneSkillCarriesTheOrder(t *testing.T) {
	order := kampagneSection(t, readKampagneSkill(t), "## Reihenfolge")

	// Ascending order in the text is the contract, not mere presence: briefing
	// before spawn, DONE check before merge, live check before Jira.
	steps := []string{"batches.py", "spawn", "DONE", "merge", "make deploy", "Live-Prüfung", "Bitbucket"}
	last := -1
	for _, step := range steps {
		idx := strings.Index(order, step)
		if idx < 0 {
			t.Errorf("order section does not name %q", step)
			continue
		}
		if idx < last {
			t.Errorf("order section names %q before the step preceding it", step)
		}
		last = idx
	}
	if !strings.Contains(order, "dateidisjunkt") {
		t.Errorf("order section does not carry the file-disjointness condition for parallel lots")
	}
	if !strings.Contains(order, "NACHEINANDER") {
		t.Errorf("order section does not carry the spawn-one-after-another rule")
	}
}

func TestBugfixKampagneSkillCarriesTheChecks(t *testing.T) {
	checks := kampagneSection(t, readKampagneSkill(t), "## Prüfpflichten")

	for _, want := range []string{"Regressionstest", "Cold Review", "Diff", "proof", "Kombinationstest"} {
		if !strings.Contains(checks, want) {
			t.Errorf("check duties do not name %q", want)
		}
	}
	// Acceptance happens on the commit, never on an agent status.
	if !strings.Contains(checks, "nie auf Agentenstatus") {
		t.Errorf("check duties do not anchor acceptance on the commit instead of the agent status")
	}
}

func TestBugfixKampagneSkillCarriesTheApprovalLimits(t *testing.T) {
	limits := kampagneSection(t, readKampagneSkill(t), "## Freigabegrenzen")

	for _, want := range []string{"GitHub", "yesmem setup", "gh pr merge", "Bitbucket"} {
		if !strings.Contains(limits, want) {
			t.Errorf("approval limits do not name %q", want)
		}
	}
	// The delegation boundary: the orchestrator may merge and deploy under the
	// standing approval, a campaign agent never may.
	if !strings.Contains(limits, "Agenten mergen") || !strings.Contains(limits, "deployen") {
		t.Errorf("approval limits do not separate the orchestrator from the campaign agents")
	}
}

func TestBugfixKampagneSkillRefersToTheOrchestratorDuties(t *testing.T) {
	skill := readKampagneSkill(t)

	if !strings.Contains(skill, "Orchestrator-Pflichten") {
		t.Errorf("%s does not point at the Orchestrator-Pflichten of the yesloop skill", bugfixKampagneSkillPath)
	}
	// A copy of the spawn briefing would drift against the loop skill, so the
	// campaign skill names it and stays silent on its wording.
	if strings.Contains(skill, "YOU ARE A YESLOOP-AGENT") {
		t.Errorf("%s copies the yesloop spawn briefing instead of referring to it", bugfixKampagneSkillPath)
	}
}

// Campaign state is a moving target: a frozen snapshot inside the skill reads as
// truth long after it stopped being one. Jira and the learning chain carry the
// state, the skill carries the procedure.
func TestBugfixKampagneSkillHasNoDateAndNoProgress(t *testing.T) {
	skill := readKampagneSkill(t)

	dates := regexp.MustCompile(`\b\d{1,2}\.\d{1,2}\.\d{2,4}\b|\b20\d{2}-\d{2}-\d{2}\b`)
	if m := dates.FindString(skill); m != "" {
		t.Errorf("%s carries a date (%q); the campaign state belongs in Jira and the learning chain", bugfixKampagneSkillPath, m)
	}
	for _, progress := range []string{"Welle 1a", "Stand:", "abgeschlossen"} {
		if strings.Contains(skill, progress) {
			t.Errorf("%s carries the progress marker %q", bugfixKampagneSkillPath, progress)
		}
	}
}
