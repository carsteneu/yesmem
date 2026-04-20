package briefing

import (
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Generator builds narrative briefing text from stored data.
type Generator struct {
	store          *storage.Store
	maxSessions    int
	maxPerCategory int
	dedupThreshold float64
	unfinishedTTL  int // days; 0 = no limit
	userProfile    bool
	strings        Strings
	recovery       *RecoveryConfig
	skipUnfinished bool
	codeScanner    *codescan.CachedScanner
	codeGraph      *codescan.CodeGraph // built during briefing, available for MCP tools
	codeMapText    string              // built during briefing, appended post-refine
}

// New creates a briefing generator with auto-loaded strings.
func New(store *storage.Store, maxSessions int) *Generator {
	if maxSessions <= 0 {
		maxSessions = 3
	}
	return &Generator{
		store:          store,
		maxSessions:    maxSessions,
		maxPerCategory: 3,
		dedupThreshold: 0.4,
		userProfile:    true,
		strings:        resolveDefaultStrings(),
	}
}

// SetMaxPerCategory overrides the default max items per category.
func (g *Generator) SetMaxPerCategory(n int) {
	if n > 0 {
		g.maxPerCategory = n
	}
}

// SetDedupThreshold overrides the default dedup similarity threshold.
func (g *Generator) SetDedupThreshold(t float64) {
	if t > 0 {
		g.dedupThreshold = t
	}
}

// SetUnfinishedTTL sets the max age in days for unfinished items. 0 = no limit.
func (g *Generator) SetUnfinishedTTL(days int) {
	g.unfinishedTTL = days
}

// SetUserProfile enables or disables the user profile section in the briefing.
func (g *Generator) SetUserProfile(enabled bool) {
	g.userProfile = enabled
}

// SetSkipUnfinished disables loading of unfinished/open-work items.
// Use for agent sessions where todo nudges are not appropriate.
func (g *Generator) SetSkipUnfinished(skip bool) {
	g.skipUnfinished = skip
}

// SetStrings sets translated UI strings for the briefing.
func (g *Generator) SetStrings(s Strings) {
	g.strings = s
}

// CodeGraph returns the code graph built during the last Generate() call.
// Returns nil if no briefing has been generated yet or the project had no source files.
func (g *Generator) CodeGraph() *codescan.CodeGraph {
	return g.codeGraph
}

// CodeMap returns the rendered code map built during Generate.
// Append post-refine to bypass LLM compression.
func (g *Generator) CodeMap() string {
	return g.codeMapText
}

// Generate produces a narrative briefing for the given project directory.
func (g *Generator) Generate(projectDir string) string {
	projectShort := g.resolveProject(projectDir)

	// Load, deduplicate, and score all learnings
	allLearnings := g.loadLearnings(projectShort)
	// Enrich with session fixation ratios before scoring
	g.store.EnrichSessionFixationScores(allLearnings)
	// Sort + limit BEFORE dedup — Deduplicate is O(n²) and becomes very slow with 2000+ learnings.
	// Score first so we keep the most relevant learnings, then dedup within that set.
	ctx := models.QueryContext{Project: projectShort}
	models.ContextualScoreAndSort(allLearnings, ctx)
	const maxBeforeDedup = 400
	if len(allLearnings) > maxBeforeDedup {
		allLearnings = allLearnings[:maxBeforeDedup]
	}
	allLearnings = Deduplicate(allLearnings, g.dedupThreshold)
	allLearnings = filterFaded(allLearnings, fadeThreshold)

	// Load other data
	sessions := g.loadSessions(projectShort)
	profile := g.loadProfile(projectShort)
	unfinished := g.loadUnfinished(projectShort)
	totalSessions := g.countTotalSessions()
	narratives := g.loadNarratives(projectShort)
	docSources := g.loadDocSources(projectShort)

	s := g.strings

	// Render sections — greeting+narratives FIRST for relationship framing
	var b strings.Builder
	b.WriteString(g.renderAwakening(s, allLearnings, totalSessions, narratives, sessions, projectShort, docSources))

	// User profile (full Entwicklerprofil) OR standard persona directive + preferences — mutually exclusive
	// Graceful fallback: if userProfile is enabled but no profile exists yet, show standard path
	showStandardPersona := true
	if g.userProfile {
		userProfile := g.loadUserProfile()
		if userProfile != "" {
			b.WriteString("\n" + userProfile + "\n")
			showStandardPersona = false
		}
	}
	if showStandardPersona {
		// Standard path: persona directive (limited to 10 rules) + preference learnings
		personaDirective := g.loadPersonaDirective()
		if personaDirective != "" {
			re := regexp.MustCompile(`^\d+ Sessions`)
			personaDirective = re.ReplaceAllString(personaDirective, fmt.Sprintf("%d Sessions", totalSessions))
			b.WriteString(FormatPersonaDirectiveLimited(personaDirective, 10))
			b.WriteString("\n")
		}
		b.WriteString(g.renderPerson(s, allLearnings))
	}
	b.WriteString(g.renderKnowledge(s, allLearnings))

	milestones := g.loadMilestones(narratives)
	b.WriteString(g.renderMilestones(s, milestones))

	b.WriteString(g.renderProject(s, projectShort, profile, sessions))
	absenceHours := calcAbsenceHours(sessions)
	// Merge deadline triggers into unfinished items, track urgency reasons.
	// Gated behind skipUnfinished — agent sessions suppress all open-work including deadline items.
	triggerReasons := make(map[int64]string) // learning ID → urgency reason
	if !g.skipUnfinished {
		triggers := checkDeadlineTriggers(g.store, projectShort)
		for _, tm := range triggers {
			triggerReasons[tm.Learning.ID] = tm.Reason
			if !containsLearning(unfinished, tm.Learning.ID) {
				unfinished = append(unfinished, tm.Learning)
			}
		}
	}
	b.WriteString(g.renderOpenWork(s, unfinished, absenceHours, triggerReasons))

	gapData := g.loadGapAwareness(projectShort)
	b.WriteString(g.renderGapAwareness(s, gapData))

	// Recurrence alerts — wiederkehrende Muster
	recurrenceAlerts := g.loadRecurrenceAlerts(projectShort)
	if len(recurrenceAlerts) > 0 {
		b.WriteString("\n")
		b.WriteString(s.Recurrence)
		b.WriteString("\n")
		for _, a := range recurrenceAlerts {
			b.WriteString(fmt.Sprintf("• %s\n", a.Content))
		}
	}

	// Metamemory — Selbsteinschätzung der Wissensqualität
	if mm := g.loadMetamemory(projectShort); mm != "" {
		b.WriteString("\n")
		b.WriteString(mm)
	}

	// Knowledge Index Phase A — enriched briefing sections
	b.WriteString(g.renderKnowledgeIndex(s, projectDir, projectShort, docSources))

	result := b.String()

	// Hard limit: ~20KB (~5000 tokens). Prevents briefing from consuming too much context.
	const maxBytes = 20000
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n... (briefing truncated)"
	}

	return result
}

// resolveProject finds the best matching project_short for a directory path.
// Tries filepath.Base first, then walks up parent directories.
func (g *Generator) resolveProject(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	// Try exact match first
	short := models.ProjectShortFromPath(projectDir)
	sessions := g.loadSessions(short)
	if len(sessions) > 0 {
		return short
	}
	// Walk up parent directories (max 3 levels)
	dir := projectDir
	for i := 0; i < 3; i++ {
		parent := filepath.Dir(dir)
		if parent == dir || parent == "/" {
			break
		}
		short = models.ProjectShortFromPath(parent)
		sessions = g.loadSessions(short)
		if len(sessions) > 0 {
			return short
		}
		dir = parent
	}
	// Fallback to original
	return models.ProjectShortFromPath(projectDir)
}

// loadLearnings fetches project-specific and global learnings.
func (g *Generator) loadLearnings(projectShort string) []models.Learning {
	learnings, err := g.store.GetActiveLearnings("", projectShort, "", "")
	if err != nil || len(learnings) == 0 {
		learnings, _ = g.store.GetActiveLearnings("", "", "", "")
	}
	return learnings
}

// loadSessions fetches sessions for the current project.
func (g *Generator) loadSessions(projectShort string) []models.Session {
	if projectShort == "" {
		return nil
	}
	sessions, _ := g.store.ListSessions(projectShort, 30)
	return sessions
}

// loadProfile fetches the project profile, limited to maxLines.
func (g *Generator) loadProfile(projectShort string) string {
	if projectShort == "" {
		return ""
	}
	profile, err := g.store.GetProjectProfile(projectShort)
	if err != nil || profile.ProfileText == "" {
		return ""
	}
	// Limit to 5 lines for compact briefing
	lines := strings.Split(profile.ProfileText, "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "\n")
}

// loadUnfinished fetches unfinished work items for the current project only.
// Items older than unfinishedTTL days are excluded (0 = no limit).
func (g *Generator) loadUnfinished(projectShort string) []models.Learning {
	if g.skipUnfinished {
		return nil
	}
	if projectShort == "" {
		return nil
	}
	unfinished, _ := g.store.GetActiveLearnings("unfinished", projectShort, "", "")
	if g.unfinishedTTL <= 0 || len(unfinished) == 0 {
		return unfinished
	}
	cutoff := time.Now().AddDate(0, 0, -g.unfinishedTTL)
	filtered := unfinished[:0]
	for _, u := range unfinished {
		if u.CreatedAt.After(cutoff) {
			filtered = append(filtered, u)
		}
	}
	return filtered
}

// countTotalSessions returns the total number of sessions across all projects.
func (g *Generator) countTotalSessions() int {
	projects, err := g.store.ListProjects()
	if err != nil {
		return 0
	}
	total := 0
	for _, p := range projects {
		total += p.SessionCount
	}
	return total
}

// loadPersonaDirective fetches the cached persona directive for the default user.
func (g *Generator) loadPersonaDirective() string {
	directive, err := g.store.GetPersonaDirective("default")
	if err != nil || directive == nil {
		return ""
	}
	return directive.Directive
}

// loadUserProfile fetches the synthesized user profile.
func (g *Generator) loadUserProfile() string {
	directive, err := g.store.GetPersonaDirective("user_profile")
	if err != nil || directive == nil {
		return ""
	}
	return directive.Directive
}

// loadNarratives fetches recent session narratives for the current project.
// Narratives within 1h of each other are consolidated into a single entry.
func (g *Generator) loadNarratives(projectShort string) []NarrativeSummary {
	if projectShort == "" {
		return nil
	}
	// Fetch more than needed so consolidation still yields enough
	learnings, err := g.store.GetRecentNarratives(projectShort, 10)
	if err != nil {
		log.Printf("[briefing] loadNarratives failed for %q: %v", projectShort, err)
		return nil
	}
	if len(learnings) == 0 {
		return nil
	}

	// No consolidation — show individual narratives (newest first).
	// Limit to 3 most recent.
	if len(learnings) > 3 {
		learnings = learnings[:3]
	}

	var result []NarrativeSummary
	for _, l := range learnings {
		content := l.Content
		if len(content) > 1500 {
			content = content[:1500] + "..."
		}

		result = append(result, NarrativeSummary{
			Ago:       relativeTime(l.CreatedAt),
			Flavor:    l.SessionFlavor,
			Content:   content,
			SessionID: l.SessionID,
		})
	}
	return result
}

func (g *Generator) renderAwakening(s Strings, learnings []models.Learning, totalSessions int, narratives []NarrativeSummary, sessions []models.Session, projectShort string, docSources []DocSourceSummary) string {
	// Extract pulse lines
	for i := range narratives {
		narratives[i].Pulse = extractPulse(narratives[i].Content)
	}

	data := AwakeningData{
		HasLearnings:  len(learnings) > 0,
		TotalSessions: totalSessions,
		AwakeningText: fmt.Sprintf(s.AwakeningNarrative, totalSessions, totalSessions),
	}

	if len(narratives) > 0 {
		latest := narratives[0]
		data.LatestNarrative = &latest
		if len(narratives) > 1 {
			// Truncate flavors at sentence boundary for compact display
			older := make([]NarrativeSummary, len(narratives)-1)
			copy(older, narratives[1:])
			for i := range older {
				if older[i].Flavor != "" {
					older[i].Flavor = truncateAtSentence(older[i].Flavor, 120)
				}
			}
			data.OlderNarratives = older
		}
	}

	// Load clusters from DB
	data.StrongClusters, data.WeakClusters = g.loadClusters(projectShort)

	// Doc sources
	data.DocSources = docSources

	// Compute time gaps from session start times (last 90 days only)
	cutoff := time.Now().AddDate(0, 0, -90)
	var startTimes []time.Time
	for _, sess := range sessions {
		if !sess.StartedAt.IsZero() && sess.StartedAt.After(cutoff) {
			startTimes = append(startTimes, sess.StartedAt)
		}
	}
	data.TimeGaps = computeTimeGaps(startTimes)

	return renderTemplate("awakening", tmplAwakening, s, data)
}

// loadClusters fetches learning clusters and splits them into strong (confidence >= 0.5) and weak.
func (g *Generator) loadClusters(projectShort string) (strong, weak []ClusterSummary) {
	clusters, err := g.store.GetLearningClusters(projectShort)
	if err != nil {
		log.Printf("[briefing] loadClusters failed for %q: %v", projectShort, err)
		return nil, nil
	}
	if len(clusters) == 0 {
		return nil, nil
	}

	for _, c := range clusters {
		summary := ClusterSummary{
			Label:       c.Label,
			Count:       c.LearningCount,
			RecencyNote: g.recencyNote(c.AvgRecencyDays),
			Confidence:  c.Confidence,
		}
		if c.Confidence >= 0.5 {
			strong = append(strong, summary)
		} else {
			weak = append(weak, summary)
		}
	}
	return strong, weak
}

// recencyNote returns a translated recency description.
func (g *Generator) recencyNote(avgDays float64) string {
	s := g.strings
	switch {
	case avgDays < 7:
		return s.RecencyFresh
	case avgDays < 30:
		return s.RecencyWeeks
	case avgDays < 90:
		return s.RecencyMonth
	default:
		return s.RecencyOlder
	}
}

func (g *Generator) renderPerson(s Strings, learnings []models.Learning) string {
	var items []string
	for _, l := range learnings {
		if l.Category == "preference" || l.Category == "relationship" {
			items = append(items, RewriteToPersonalTone(l.Content))
		}
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) > g.maxPerCategory {
		items = items[:g.maxPerCategory]
	}
	return renderTemplate("person", tmplPerson, s, PersonData{Items: items})
}

func (g *Generator) renderKnowledge(s Strings, learnings []models.Learning) string {
	groups := map[string][]models.Learning{}
	for _, l := range learnings {
		groups[l.Category] = append(groups[l.Category], l)
	}

	// With relevance scoring, all categories get equal space.
	// The score already prioritizes gotchas via categoryWeight.
	max := g.maxPerCategory

	gotchas, moreGotchas := limitLearningsTruncated(groups["gotcha"], max, 120)
	decisions, moreDecisions := limitLearningsTruncated(groups["decision"], max, 120)
	patterns, morePatterns := limitLearningsTruncated(groups["pattern"], max, 120)
	teachings, moreTeachings := limitLearningsTruncated(groups["explicit_teaching"], max, 120)
	pivots, morePivots := limitLearnings(groups["pivot_moment"], 5)

	return renderTemplate("knowledge", tmplKnowledge, s, KnowledgeData{
		Gotchas:       gotchas,
		MoreGotchas:   moreGotchas,
		Decisions:     decisions,
		MoreDecisions: moreDecisions,
		Patterns:      patterns,
		MorePatterns:  morePatterns,
		Teachings:     teachings,
		MoreTeachings: moreTeachings,
		Pivots:        pivots,
		MorePivots:    morePivots,
	})
}

func (g *Generator) renderProject(s Strings, projectShort, profile string, sessions []models.Session) string {
	if projectShort == "" || len(sessions) == 0 {
		return ""
	}

	limit := g.maxSessions
	if limit > len(sessions) {
		limit = len(sessions)
	}

	// Bulk-load subagent counts for shown sessions
	sessionIDs := make([]string, limit)
	for i := 0; i < limit; i++ {
		sessionIDs[i] = sessions[i].ID
	}
	subagentCounts, _ := g.store.GetSubagentCounts(sessionIDs)

	var summaries []SessionSummary
	for i := 0; i < limit; i++ {
		sess := sessions[i]
		msg := sess.FirstMessage
		if len(msg) > 80 {
			msg = msg[:80] + "..."
		}
		summaries = append(summaries, SessionSummary{
			Ago:           relativeTime(sess.StartedAt),
			FirstMessage:  msg,
			Branch:        sess.GitBranch,
			SubagentCount: subagentCounts[sess.ID],
		})
	}

	return renderTemplate("project", tmplProject, s, ProjectData{
		Name:          projectShort,
		Profile:       profile,
		Sessions:      summaries,
		TotalSessions: len(sessions),
		ShownCount:    limit,
	})
}

// loadRecurrenceAlerts fetches active recurrence alerts for the current project.
func (g *Generator) loadRecurrenceAlerts(projectShort string) []models.Learning {
	alerts, err := g.store.GetLearningsByCategory("recurrence_alert", projectShort, 3)
	if err != nil {
		return nil
	}
	return alerts
}

// loadMetamemory returns a rendered self-assessment of knowledge quality.
func (g *Generator) loadMetamemory(projectShort string) string {
	db := g.store.DB()
	if db == nil {
		return ""
	}

	projFilter := "AND (project = ? OR project IS NULL OR project = '')"
	base := "FROM learnings WHERE superseded_by IS NULL " + projFilter

	var total, solid, noisy, expired int
	db.QueryRow("SELECT COUNT(*) "+base, projectShort).Scan(&total)
	db.QueryRow("SELECT COUNT(*) "+base+" AND save_count >= 3", projectShort).Scan(&solid)
	db.QueryRow("SELECT COUNT(*) "+base+" AND noise_count > 3", projectShort).Scan(&noisy)
	db.QueryRow("SELECT COUNT(*) "+base+" AND valid_until IS NOT NULL AND valid_until < datetime('now')", projectShort).Scan(&expired)

	// Only render if there's something to say
	if solid == 0 && noisy == 0 && expired == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("My knowledge quality here:\n")
	if solid > 0 {
		sb.WriteString(fmt.Sprintf("• %d/%d learnings proven (save_count ≥3)\n", solid, total))
	}
	if noisy > 0 {
		sb.WriteString(fmt.Sprintf("• %d fragile (often irrelevantly injected — use with caution)\n", noisy))
	}
	if expired > 0 {
		sb.WriteString(fmt.Sprintf("• %d possibly outdated (valid_until expired)\n", expired))
	}
	return sb.String()
}

// loadGapAwareness computes what knowledge exists but isn't shown in the briefing.
func (g *Generator) loadGapAwareness(projectShort string) *GapAwarenessData {
	counts, err := g.store.GetLearningCounts(projectShort)
	if err != nil {
		counts = nil
	}
	overflow := make(map[string]int)
	for cat, count := range counts {
		if extra := count - g.maxPerCategory; extra > 0 {
			overflow[cat] = extra
		}
	}

	projects, _ := g.store.ListProjects()
	var others []ProjectGap
	moreCount := 0
	for _, p := range projects {
		if p.ProjectShort == projectShort || p.ProjectShort == "" {
			continue
		}
		if p.SessionCount < 5 {
			continue
		}
		lastActive, _ := time.Parse(time.RFC3339, p.LastActive)
		daysAgo := int(time.Since(lastActive).Hours() / 24)
		if len(others) < 5 {
			others = append(others, ProjectGap{
				Name:         p.ProjectShort,
				SessionCount: p.SessionCount,
				DaysAgo:      daysAgo,
			})
		} else {
			moreCount++
		}
	}

	// Load knowledge gaps
	var knowledgeGaps []KnowledgeGapSummary
	if activeGaps, err := g.store.GetActiveGaps(projectShort, 3); err == nil {
		for _, gap := range activeGaps {
			daysAgo := int(time.Since(gap.FirstSeen).Hours() / 24)
			knowledgeGaps = append(knowledgeGaps, KnowledgeGapSummary{
				Topic:    gap.Topic,
				HitCount: gap.HitCount,
				DaysAgo:  daysAgo,
			})
		}
	}

	if len(overflow) == 0 && len(others) == 0 && len(knowledgeGaps) == 0 {
		return nil
	}

	return &GapAwarenessData{
		ProjectShort:  projectShort,
		Overflow:      overflow,
		OtherProjects: others,
		MoreCount:     moreCount,
		KnowledgeGaps: knowledgeGaps,
	}
}

func (g *Generator) renderGapAwareness(s Strings, data *GapAwarenessData) string {
	if data == nil {
		return ""
	}
	// Pre-compute DaysAgoText for each project gap
	for i := range data.OtherProjects {
		if data.OtherProjects[i].DaysAgo > 0 {
			data.OtherProjects[i].DaysAgoText = fmt.Sprintf(s.LabelDaysAgoFmt, data.OtherProjects[i].DaysAgo)
		}
	}
	return renderTemplate("gap", tmplGapAwareness, s, data)
}

// extractPulse finds and returns a "Puls: ..." line from narrative content.
func extractPulse(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Puls:") {
			return trimmed
		}
	}
	return ""
}

// loadDocSources fetches registered documentation sources for this project.
func (g *Generator) loadDocSources(projectShort string) []DocSourceSummary {
	if projectShort == "" {
		return nil
	}
	// Load project-specific + global (project='') doc sources
	sources, err := g.store.ListDocSources(projectShort)
	globalSources, _ := g.store.ListDocSources("")
	for _, gs := range globalSources {
		found := false
		for _, s := range sources {
			if s.Name == gs.Name {
				found = true
				break
			}
		}
		if !found {
			sources = append(sources, gs)
		}
	}
	if err != nil || len(sources) == 0 {
		return nil
	}
	var result []DocSourceSummary
	for _, s := range sources {
		result = append(result, DocSourceSummary{
			Name:        s.Name,
			Version:     s.Version,
			ChunkCount:  s.ChunkCount,
			TriggerExts: s.TriggerExtensions,
			DocType:     s.DocType,
		})
	}
	return result
}

// loadMilestones fetches the most emotionally impactful sessions across all time.
// Results are sorted chronologically (oldest first).
func (g *Generator) loadMilestones(narratives []NarrativeSummary) []MilestoneItem {
	// Exclude sessions already shown in narratives
	excludeIDs := make(map[string]bool)
	for _, n := range narratives {
		if n.SessionID != "" {
			excludeIDs[n.SessionID] = true
		}
	}

	learnings, err := g.store.GetMilestoneNarratives(10 + len(excludeIDs))
	if err != nil || len(learnings) == 0 {
		return nil
	}

	// Sort chronologically (oldest first) — query returns by intensity
	sort.Slice(learnings, func(i, j int) bool {
		return learnings[i].CreatedAt.Before(learnings[j].CreatedAt)
	})

	var items []MilestoneItem
	for _, l := range learnings {
		if l.SessionFlavor == "" || excludeIDs[l.SessionID] {
			continue
		}
		items = append(items, MilestoneItem{
			Ago:            relativeTime(l.CreatedAt),
			Flavor:         truncateAtSentence(l.SessionFlavor, 300),
			IntensityLabel: g.intensityLabel(l.EmotionalIntensity),
		})
		if len(items) >= 3 {
			break
		}
	}
	return items
}

// intensityLabel converts a 0.0-1.0 intensity to a translated label.
func (g *Generator) intensityLabel(intensity float64) string {
	s := g.strings
	switch {
	case intensity >= 0.8:
		return s.IntensityBreakthrough
	case intensity >= 0.6:
		return s.IntensityIntense
	case intensity >= 0.4:
		return s.IntensityVivid
	default:
		return s.IntensityCalm
	}
}

// truncateAtSentence truncates text at the last sentence boundary before maxLen.
func truncateAtSentence(text string, maxLen int) string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	// Find last sentence-ending punctuation before maxLen
	lastEnd := -1
	for i, r := range runes[:maxLen] {
		if r == '.' || r == '!' || r == '?' || r == '—' {
			lastEnd = i
		}
	}
	if lastEnd > maxLen/3 { // only use if we keep at least 1/3 of the text
		return string(runes[:lastEnd+1])
	}
	// Fallback: cut at last space
	for i := maxLen - 1; i > maxLen/2; i-- {
		if runes[i] == ' ' {
			return string(runes[:i]) + "..."
		}
	}
	return string(runes[:maxLen]) + "..."
}

func (g *Generator) renderMilestones(s Strings, items []MilestoneItem) string {
	if len(items) == 0 {
		return ""
	}
	return renderTemplate("milestones", tmplMilestones, s, MilestoneData{Items: items})
}

func (g *Generator) renderOpenWork(s Strings, unfinished []models.Learning, absenceHours float64, triggerReasons map[int64]string) string {
	if len(unfinished) == 0 {
		return ""
	}

	isReminder := absenceHours >= 4

	// Count ideas and stale before filtering them out
	ideaCount := 0
	staleCount := 0
	var actionable []models.Learning
	for _, l := range unfinished {
		switch l.TaskType {
		case "idea":
			ideaCount++
		case "stale":
			staleCount++
		default: // "task", "blocked", "" (legacy)
			actionable = append(actionable, l)
		}
	}
	unfinished = actionable

	// Filter by importance when in reminder mode
	if isReminder {
		var filtered []models.Learning
		for _, l := range unfinished {
			if l.Importance >= 3 {
				filtered = append(filtered, l)
			}
		}
		unfinished = filtered
		if len(unfinished) == 0 && ideaCount == 0 && staleCount == 0 {
			return ""
		}
	}

	// Prioritize deadline items — sort triggered items to front so they survive the limit
	sort.SliceStable(unfinished, func(i, j int) bool {
		_, iHasTrigger := triggerReasons[unfinished[i].ID]
		_, jHasTrigger := triggerReasons[unfinished[j].ID]
		if iHasTrigger != jHasTrigger {
			return iHasTrigger // triggered items first
		}
		return false // preserve existing order
	})

	// Limit: 5 items when long absence, 3 otherwise
	limit := 3
	if absenceHours >= 24 {
		limit = 5
	}
	if len(unfinished) > limit {
		unfinished = unfinished[:limit]
	}

	var items []OpenWorkItem
	for _, l := range unfinished {
		deadline := ""
		if reason, ok := triggerReasons[l.ID]; ok {
			deadline = reason // urgency-based: "Deadline morgen", "Deadline heute!"
		} else {
			deadline = formatDeadline(l.TriggerRule) // static: "Fr 28.03."
		}
		items = append(items, OpenWorkItem{
			Project:  l.Project,
			Content:  l.Content,
			Deadline: deadline,
		})
	}

	data := OpenWorkData{
		Items:      items,
		IsReminder: isReminder,
		IdeaCount:  ideaCount,
		StaleCount: staleCount,
	}
	if isReminder {
		data.AbsenceNote = formatAbsence(absenceHours)
	}

	result := renderTemplate("openwork", tmplOpenWork, s, data)
	return result
}

// calcAbsenceHours returns hours since the last session started.
// Returns 0 if no sessions exist (first session ever).
func calcAbsenceHours(sessions []models.Session) float64 {
	if len(sessions) == 0 {
		return 0
	}
	if sessions[0].StartedAt.IsZero() {
		return 0
	}
	return time.Since(sessions[0].StartedAt).Hours()
}

// formatAbsence returns a German absence note like "Du warst 2 Tage nicht da."
func formatAbsence(hours float64) string {
	switch {
	case hours < 24:
		return fmt.Sprintf("Du warst %d Stunden nicht da.", int(hours))
	case hours < 48:
		return "Du warst seit gestern nicht da."
	default:
		return fmt.Sprintf("Du warst %d Tage nicht da.", int(hours/24))
	}
}

// formatDeadline extracts a human-readable deadline from a trigger_rule.
// Returns "" if trigger is not a deadline format.
func formatDeadline(triggerRule string) string {
	if !strings.HasPrefix(triggerRule, "deadline:") {
		return ""
	}
	dateStr := strings.TrimPrefix(triggerRule, "deadline:")
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return ""
	}
	weekday := [...]string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
	return fmt.Sprintf("%s %02d.%02d.", weekday[t.Weekday()], t.Day(), t.Month())
}
