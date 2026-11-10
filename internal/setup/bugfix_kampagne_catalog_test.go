package setup

import "testing"

// The campaign skill is only worth its file if a session can find it. Discovery
// runs through loadSkillCatalog, which derives the triggers from the description
// and cuts the trigger list at the first period (extractTriggers). A description
// that lists `batches.py` or `docs/bugs.md` among the triggers therefore loses
// everything after the dot, and the skill stays invisible while its file looks
// fine. This test pins the catalogue result, not the file text.
//
// "yesloop" is deliberately not a trigger: that word belongs to the yesloop
// skill, and a campaign trigger would fire on every plain loop invocation.
func TestSkillCatalogCarriesBugfixKampagne(t *testing.T) {
	catalog, err := loadSkillCatalog()
	if err != nil {
		t.Fatalf("loadSkillCatalog: %v", err)
	}

	var entry *skillCatalogEntry
	for i := range catalog {
		if catalog[i].Skill == "bugfix-kampagne" {
			entry = &catalog[i]
			break
		}
	}
	if entry == nil {
		t.Fatalf("the catalogue does not carry bugfix-kampagne; the skill is installed but undiscoverable")
	}

	triggers := map[string]bool{}
	for _, trigger := range entry.Triggers {
		triggers[trigger] = true
	}
	// Every trigger of the description must arrive. A single period in the list
	// truncates it, and the truncation keeps the early entries, so only the full
	// set catches it.
	for _, want := range []string{
		"Bugfix-Kampagne", "Jira YM", "Board 164", "YM-1", "Los", "Welle", "docs/bugs", "batches", "Audit-Befund",
	} {
		if !triggers[want] {
			t.Errorf("trigger %q missing, the trigger list was cut short: %v", want, entry.Triggers)
		}
	}
	if triggers["yesloop"] {
		t.Errorf("the campaign skill claims the yesloop trigger and fires on every loop: %v", entry.Triggers)
	}
}
