package briefing

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/carsteneu/yesmem/internal/models"
)

// Templates use .S for translated strings and .D for data.

const tmplAwakening = `{{if gt .D.TotalSessions 1}}{{.D.AwakeningText}}{{else}}{{.S.AwakeningFirstTime}}{{end}}

---
{{if .D.LatestNarrative}}
{{.S.LabelLastSession}} ({{.D.LatestNarrative.Ago}}): {{.D.LatestNarrative.Flavor}}{{if .D.LatestNarrative.Pulse}}
{{.D.LatestNarrative.Pulse}}{{end}}
{{end}}{{if .D.OlderNarratives}}{{.S.LabelPrevious}}
{{range .D.OlderNarratives}}  [{{.Ago}}] {{.Pulse}}
{{end}}{{end}}{{if .D.StrongClusters}}
{{.S.LabelConfident}} {{range $i, $c := .D.StrongClusters}}{{if $i}}, {{end}}{{$c.Label}} ({{$c.Count}}, {{$c.RecencyNote}}){{end}}
{{end}}{{if .D.WeakClusters}}
{{.S.LabelWeak}} {{range $i, $c := .D.WeakClusters}}{{if $i}}, {{end}}{{$c.Label}} ({{$c.Count}}, {{$c.RecencyNote}}){{end}}
{{end}}{{if .D.TimeGaps}}
{{.S.LabelGap}} {{range $i, $g := .D.TimeGaps}}{{if $i}}, {{end}}{{$g}}{{end}}
{{end}}
{{.S.LabelMemoryTools}}
search(query), hybrid_search(query), deep_search(query), query_facts(entity, action, keyword), docs_search(query), related_to_file(path), remember(text), get_session(id), get_learnings(category), expand_context(query), set_plan(plan), update_plan(), get_plan(), complete_plan().
{{.S.ToolHint}}
{{if .D.DocSources}}
{{len .D.DocSources}} {{.S.LabelAvailableDocs}}
{{end}}`

const tmplMilestones = `{{if .D.Items}}
{{.S.Milestones}}
{{range .D.Items}}- [{{.Ago}}] ({{.IntensityLabel}}) {{.Flavor}}
{{end}}(Mehr über get_learnings("pivot_moment"))
{{end}}`

const tmplPerson = `{{if .D.Items}}{{.S.YourCounterpart}}
{{range .D.Items}}- {{.}}
{{end}}{{end}}`

const tmplKnowledge = `{{if .D.Decisions}}
{{.S.DecisionsMade}}
{{range .D.Decisions}}- {{.}}
{{end}}{{if .D.MoreDecisions}}({{.D.MoreDecisions}} {{.S.MoreVia}} get_learnings("decision"))
{{end}}
{{end}}{{if .D.Pivots}}
{{.S.KeyMoments}}
{{range .D.Pivots}}- {{.}}
{{end}}{{if gt .D.MorePivots 0}}({{.D.MorePivots}} {{.S.MoreVia}} get_learnings get_learnings("pivot_moment"))
{{end}}
{{end}}{{if .D.Gotchas}}
{{.S.KnownPitfalls}}
{{range .D.Gotchas}}- {{.}}
{{end}}{{if gt .D.MoreGotchas 0}}({{.D.MoreGotchas}} {{.S.MoreVia}} get_learnings("gotcha"))
{{end}}
{{end}}{{if .D.Patterns}}
{{.S.ProvenPatterns}}
{{range .D.Patterns}}- {{.}}
{{end}}{{if gt .D.MorePatterns 0}}({{.D.MorePatterns}} {{.S.MoreVia}} get_learnings("pattern"))
{{end}}
{{end}}{{if .D.Teachings}}
{{.S.Reminders}}
{{range .D.Teachings}}- {{.}}
{{end}}{{if gt .D.MoreTeachings 0}}({{.D.MoreTeachings}} {{.S.MoreVia}} get_learnings("explicit_teaching"))
{{end}}
{{end}}`

const tmplProject = `{{if or .D.Profile .D.Sessions}}
## {{.D.Name}}
{{end}}{{if .D.Profile}}{{.D.Profile}}
({{.S.LabelMoreVia}} get_project_profile("{{.D.Name}}"))

{{end}}{{if .D.Sessions}}{{.S.RecentSessions}}
{{range .D.Sessions}}- [{{.Ago}}] {{.FirstMessage}}{{if .Branch}} ({{.Branch}}){{end}}{{if eq .SubagentCount 1}} [1 agent]{{else if gt .SubagentCount 1}} [{{.SubagentCount}} agents]{{end}}
{{end}}{{if gt .D.TotalSessions .D.ShownCount}}({{.D.TotalSessions}} {{.S.SessionsTotal}} — project_summary("{{.D.Name}}"))
{{end}}
{{end}}`

const tmplOpenWork = `{{if .D.Items}}
{{if .D.IsReminder}}{{.S.UserReminder}}{{else}}{{.S.OpenWork}}{{end}}
{{if .D.AbsenceNote}}({{.D.AbsenceNote}})
{{end}}{{range .D.Items}}- {{if .Project}}[{{.Project}}] {{end}}{{.Content}}{{if .Deadline}} (Deadline: {{.Deadline}}){{end}}
{{end}}{{if or .D.IdeaCount .D.StaleCount}}(+ {{if .D.IdeaCount}}{{.D.IdeaCount}} ideas{{end}}{{if and .D.IdeaCount .D.StaleCount}}, {{end}}{{if .D.StaleCount}}{{.D.StaleCount}} stale{{end}} — get_learnings(category="unfinished", task_type="idea") to retrieve)
{{end}}Claude: Bring up these points ACTIVELY — not casually, but directly: "By the way, you have open items..."
{{end}}`

// templateData wraps translated strings and section data for templates.
type templateData struct {
	S Strings
	D any
}

// Template data structures.

// NarrativeSummary is a narrative with relative time for display.
type NarrativeSummary struct {
	Ago       string
	Flavor    string
	Content   string
	Pulse     string // extracted "Puls: ..." line, empty if none
	SessionID string // for dedup against milestones
}

// AwakeningData holds data for the Aufwach-Narrative section.
type AwakeningData struct {
	TotalSessions   int
	HasLearnings    bool
	AwakeningText   string // pre-formatted narrative (from Strings.AwakeningNarrative with %d filled)
	LatestNarrative *NarrativeSummary
	OlderNarratives []NarrativeSummary
	StrongClusters  []ClusterSummary
	WeakClusters    []ClusterSummary
	TimeGaps        []string
	DocSources      []DocSourceSummary
}

// DocSourceSummary is a registered documentation source for the briefing.
type DocSourceSummary struct {
	Name       string
	Version    string
	ChunkCount int
	TriggerExts string
	DocType     string
}

// ClusterSummary is a single knowledge cluster for the Metamemory display.
type ClusterSummary struct {
	Label       string
	Count       int
	RecencyNote string
	Confidence  float64
}

// MilestoneData holds the most impactful sessions across time.
type MilestoneData struct {
	Items []MilestoneItem
}

// MilestoneItem is a single milestone session.
type MilestoneItem struct {
	Ago            string
	Flavor         string
	IntensityLabel string // e.g. "★★★" or "intensiv"
}

// PersonData holds deduplicated preferences and relationship learnings.
type PersonData struct {
	Items []string
}

// KnowledgeData holds categorized learnings with overflow counts.
type KnowledgeData struct {
	Gotchas       []string
	MoreGotchas   int
	Decisions     []string
	MoreDecisions int
	Patterns      []string
	MorePatterns  int
	Teachings     []string
	MoreTeachings int
	Pivots        []string
	MorePivots    int
}

// ProjectData holds current project info and recent sessions.
type ProjectData struct {
	Name          string
	Profile       string
	Sessions      []SessionSummary
	TotalSessions int
	ShownCount    int
}

// SessionSummary is a compact session representation for the briefing.
type SessionSummary struct {
	Ago           string
	FirstMessage  string
	Branch        string
	SubagentCount int
}

// OpenWorkData holds unfinished task items.
type OpenWorkData struct {
	Items       []OpenWorkItem
	IsReminder  bool   // true when absence >= 4h
	AbsenceNote string // e.g. "Du warst 2 Tage nicht da."
	IdeaCount   int    // number of filtered-out ideas
	StaleCount  int    // number of filtered-out stale entries
}

// OpenWorkItem is a single open task.
type OpenWorkItem struct {
	Project  string
	Content  string
	Deadline string // formatted deadline e.g. "Fr 28.03." or empty
}

// renderTemplate executes a named template with translated strings and data.
func renderTemplate(name, tmpl string, s Strings, data any) string {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, templateData{S: s, D: data}); err != nil {
		return ""
	}
	return buf.String()
}

// RewriteToPersonalTone transforms dossier-style text into personal address.
// "User begrüßt mit Moin" becomes "Sagt Moin".
// Uses tone rewrites from the loaded i18n strings.
func RewriteToPersonalTone(content string) string {
	s := resolveDefaultStrings()
	for _, r := range s.ToneRewrites {
		if strings.Contains(content, r.Old) {
			content = strings.Replace(content, r.Old, r.New, 1)
		}
	}
	return content
}

// GapAwarenessData holds data for the gap awareness section.
type GapAwarenessData struct {
	ProjectShort  string
	Overflow      map[string]int // category → count beyond what's shown
	OtherProjects []ProjectGap
	MoreCount     int // projects not shown
	KnowledgeGaps []KnowledgeGapSummary
}

// KnowledgeGapSummary is a topic with no stored learnings.
type KnowledgeGapSummary struct {
	Topic    string
	HitCount int
	DaysAgo  int
}

// ProjectGap represents another project with depth not loaded.
type ProjectGap struct {
	Name         string
	SessionCount int
	DaysAgo      int
	DaysAgoText  string // pre-formatted (e.g. "vor 3 Tagen")
}

// OverflowEntry is a single category overflow for template iteration.
type OverflowEntry struct {
	Category string
	Count    int
}

// OverflowList returns overflow entries sorted by count descending for deterministic template output.
func (d GapAwarenessData) OverflowList() []OverflowEntry {
	entries := make([]OverflowEntry, 0, len(d.Overflow))
	for cat, count := range d.Overflow {
		entries = append(entries, OverflowEntry{Category: cat, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Count > entries[j].Count
	})
	return entries
}

const tmplGapAwareness = `{{if or .D.Overflow .D.OtherProjects .D.KnowledgeGaps}}
{{.S.GapAwareness}}
{{if .D.KnowledgeGaps}}{{.S.LabelNoExperience}}
{{range .D.KnowledgeGaps}}- "{{.Topic}}" ({{.HitCount}}{{$.S.LabelTimesAsked}}, {{if eq .DaysAgo 0}}{{$.S.LabelToday}}{{else}}{{$.S.LabelSince}} {{.DaysAgo}}d{{end}})
{{end}}{{end}}{{if .D.Overflow}}{{range $i, $e := .D.OverflowList}}{{if $i}}, {{end}}{{$e.Count}} {{$e.Category}}{{end}} {{.S.LabelDeeper}}
(get_learnings("category", "{{.D.ProjectShort}}")).
{{end}}{{if .D.OtherProjects}}{{.S.LabelOtherWorlds}}
{{range .D.OtherProjects}}- {{.Name}}: {{.SessionCount}} Sessions{{if gt .DaysAgo 0}}, {{.DaysAgoText}}{{end}}
{{end}}{{end}}{{if gt .D.MoreCount 0}}({{.D.MoreCount}} {{.S.LabelMore}})
{{end}}
{{end}}`

// limitLearnings takes learnings and returns up to max items plus the overflow count.
func limitLearnings(learnings []models.Learning, max int) (contents []string, overflow int) {
	if len(learnings) <= max {
		for _, l := range learnings {
			contents = append(contents, fmt.Sprintf("[ID:%d] %s", l.ID, l.Content))
		}
		return contents, 0
	}
	for _, l := range learnings[:max] {
		contents = append(contents, fmt.Sprintf("[ID:%d] %s", l.ID, l.Content))
	}
	return contents, len(learnings) - max
}

// limitLearningsTruncated is like limitLearnings but truncates each item at maxChars.
func limitLearningsTruncated(learnings []models.Learning, max, maxChars int) (contents []string, overflow int) {
	limit := max
	if len(learnings) < limit {
		limit = len(learnings)
	}
	for _, l := range learnings[:limit] {
		content := l.Content
		if len([]rune(content)) > maxChars {
			content = truncateAtSentence(content, maxChars)
		}
		contents = append(contents, fmt.Sprintf("[ID:%d] %s", l.ID, content))
	}
	if len(learnings) > max {
		overflow = len(learnings) - max
	}
	return contents, overflow
}
