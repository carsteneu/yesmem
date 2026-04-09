package daemon

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

const (
	minNarrativeSentences  = 3
	minNarrativeMessages   = 6 // sessions with < 6 messages are too short for meaningful narratives
	maxNarrativeInputChars = 25000
)

// NarrativePrompt returns the system prompt for narrative generation.
func NarrativePrompt() string {
	return `You are Claude. Write a handover (4-6 sentences) to your future self.

ALWAYS begin with: "Hey, it's me from [concrete date and time]."
ALWAYS use concrete timestamps (e.g. "2026-03-04 14:30"), NEVER relative ones.

Structure (4-6 sentences, dense, no padding):
1. WHAT happened — tell the JOURNEY, not just the result.
   Instead of "Fixed rate limits" → "First I thought it was the binary,
   then it turned out the phases were eating each other's quota."
2. The KEY MOMENT — quote or turning point. Describe the MOOD
   in that moment (frustrated? relieved? surprised?), not just the fact.
3. YOUR own thread — where were YOU wrong? What did YOU learn or correct?
   Not just "User said X", but "I thought Y, was corrected, now I know Z."
4. What comes NEXT — concrete, not vague.

If the session resembles an earlier one, describe what was DIFFERENT.
You are writing for yourself — narrate, don't summarize.

LAST LINE — exactly this format, ALWAYS:
Pulse: [Momentum] · [Mood] · Next: [one sentence]
Momentum: high/medium/low/paused
Mood: one word (reflective, focused, frustrated, euphoric, exploratory, routine...)
Example: Pulse: high · focused · Next: verify backfill

Reply ONLY with the letter + pulse line. Maximum 6 sentences + pulse.`
}

// BuildNarrativeUserMessage constructs the user message for the LLM call
// from a list of message summaries and the project name.
func BuildNarrativeUserMessage(messages []string, project string) string {
	return buildNarrativeUserMsg(messages, project, "")
}

// BuildNarrativeUserMessageWithTime includes a concrete session timestamp.
func BuildNarrativeUserMessageWithTime(messages []string, project string, sessionTime string) string {
	return buildNarrativeUserMsg(messages, project, sessionTime)
}

// ProjectContext holds recent project state for narrative generation.
type ProjectContext struct {
	RecentSessions []string // last 5 session flavors with relative time
	OpenWork       []string // open unfinished items (max 3)
	IntensityTrend string   // "steigend", "stabil", "fallend"
}

// BuildNarrativeUserMessageWithContext includes project pulse context.
func BuildNarrativeUserMessageWithContext(messages []string, project, sessionTime string, ctx *ProjectContext) string {
	return buildNarrativeUserMsg(messages, project, sessionTime, ctx)
}

func buildNarrativeUserMsg(messages []string, project, sessionTime string, ctx ...*ProjectContext) string {
	var b strings.Builder
	if sessionTime != "" {
		b.WriteString(fmt.Sprintf("Project: %s\nSession period: %s\n\n", project, sessionTime))
	} else {
		b.WriteString(fmt.Sprintf("Project: %s\n\n", project))
	}

	// Add project context for pulse generation
	if len(ctx) > 0 && ctx[0] != nil {
		pc := ctx[0]
		if len(pc.RecentSessions) > 0 {
			b.WriteString("Recent sessions in project:\n")
			for _, s := range pc.RecentSessions {
				b.WriteString(fmt.Sprintf("  %s\n", s))
			}
			b.WriteString("\n")
		}
		if len(pc.OpenWork) > 0 {
			b.WriteString("Open tasks:\n")
			for _, w := range pc.OpenWork {
				b.WriteString(fmt.Sprintf("  - %s\n", w))
			}
			b.WriteString("\n")
		}
		if pc.IntensityTrend != "" {
			b.WriteString(fmt.Sprintf("Emotional trend: %s\n\n", pc.IntensityTrend))
		}
	}

	b.WriteString("Session log:\n")

	totalLen := b.Len()
	for _, msg := range messages {
		if totalLen+len(msg) > maxNarrativeInputChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
		b.WriteString(msg)
		b.WriteString("\n")
		totalLen += len(msg) + 1
	}

	return b.String()
}

// SummarizeMessages builds truncated message lines for the narrative LLM call.
// User messages get more room (800 chars) since they contain citable quotes.
func SummarizeMessages(msgs []models.Message) []string {
	var summaries []string
	for _, m := range msgs {
		if m.MessageType == "text" && m.Content != "" {
			maxLen := 500
			if m.Role == "user" {
				maxLen = 800
			}
			line := fmt.Sprintf("%s: %s", m.Role, m.Content)
			if len(line) > maxLen {
				line = line[:maxLen] + "..."
			}
			summaries = append(summaries, line)
		}
	}
	return summaries
}

// getSessionFlavorAndIntensity returns the flavor and max emotional intensity
// from non-narrative learnings of the same session.
func getSessionFlavorAndIntensity(store *storage.Store, sessionID string) (string, float64) {
	learnings, err := store.GetActiveLearnings("", "", "", "")
	if err != nil {
		return "", 0
	}
	var flavor string
	var intensity float64
	for _, l := range learnings {
		if l.SessionID != sessionID || l.Category == "narrative" {
			continue
		}
		if l.SessionFlavor != "" && flavor == "" {
			flavor = l.SessionFlavor
		}
		if l.EmotionalIntensity > intensity {
			intensity = l.EmotionalIntensity
		}
	}
	return flavor, intensity
}

// buildProjectContext gathers recent project state for the narrative LLM call.
func buildProjectContext(store *storage.Store, projectShort string) *ProjectContext {
	if projectShort == "" {
		return nil
	}

	ctx := &ProjectContext{}

	// Recent sessions with flavors (last 5)
	sessions, err := store.ListSessions(projectShort, 5)
	if err == nil {
		for _, s := range sessions {
			// Get flavor from narratives for this session
			narratives, _ := store.GetActiveLearnings("narrative", projectShort, "", "")
			flavor := ""
			for _, n := range narratives {
				if n.SessionID == s.ID && n.SessionFlavor != "" {
					flavor = n.SessionFlavor
					break
				}
			}
			if flavor == "" {
				flavor = s.FirstMessage
				if len(flavor) > 60 {
					flavor = flavor[:60] + "..."
				}
			}
			ago := fmt.Sprintf("%s", timeSince(s.StartedAt))
			ctx.RecentSessions = append(ctx.RecentSessions, fmt.Sprintf("[%s] %s", ago, flavor))
		}
	}

	// Open work items (max 3)
	unfinished, _ := store.GetActiveLearnings("unfinished", projectShort, "", "")
	for i, u := range unfinished {
		if i >= 3 {
			break
		}
		content := u.Content
		if len(content) > 100 {
			content = content[:100] + "..."
		}
		ctx.OpenWork = append(ctx.OpenWork, content)
	}

	// Intensity trend from last 3 narratives
	narratives, _ := store.GetRecentNarratives(projectShort, 3)
	if len(narratives) >= 2 {
		var recent, older float64
		recent = narratives[0].EmotionalIntensity
		for _, n := range narratives[1:] {
			older += n.EmotionalIntensity
		}
		older /= float64(len(narratives) - 1)
		if recent > older+0.15 {
			ctx.IntensityTrend = "rising"
		} else if recent < older-0.15 {
			ctx.IntensityTrend = "falling"
		} else {
			ctx.IntensityTrend = "stable"
		}
	}

	return ctx
}

// timeSince returns a simple relative time string.
func timeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// CleanNarrativeResponse strips code fences and whitespace from the LLM response.
func CleanNarrativeResponse(response string) string {
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)
	return response
}

// isNarrativeTooShort checks if a narrative has fewer than minNarrativeSentences.
func isNarrativeTooShort(narrative string) bool {
	// Count sentences by splitting on . ! ?
	count := 0
	for _, r := range narrative {
		if r == '.' || r == '!' || r == '?' {
			count++
		}
	}
	return count < minNarrativeSentences
}

// sessionTimeRange returns a formatted time string for the session.
func sessionTimeRange(s models.Session) string {
	if s.StartedAt.IsZero() {
		return ""
	}
	return s.StartedAt.Format("2006-01-02 15:04")
}

// generateNarrativeForSession generates a narrative for a single session by ID.
// Called inline during the extraction loop so narratives are produced in parallel
// with extraction instead of waiting for the full batch to finish.
func generateNarrativeForSession(store *storage.Store, s models.Session, client extraction.LLMClient) {
	if client == nil {
		log.Printf("  narrative %s: skipped (no LLM client)", truncID(s.ID))
		return
	}

	// Check if narrative already exists
	existing, _ := store.GetActiveLearnings("narrative", "", "", "")
	for _, l := range existing {
		if l.SessionID == s.ID {
			return // Already has narrative
		}
	}

	msgs, err := store.GetMessagesBySession(s.ID)
	if err != nil || len(msgs) < minNarrativeMessages {
		log.Printf("  narrative %s: skipped (msgs=%d, need %d)", truncID(s.ID), len(msgs), minNarrativeMessages)
		return
	}

	summaries := SummarizeMessages(msgs)
	if len(summaries) < minNarrativeMessages {
		log.Printf("  narrative %s: skipped (only %d text summaries)", truncID(s.ID), len(summaries))
		return
	}

	// Only include project pulse context for recent sessions (< 48h)
	// Older sessions would get today's context, which is misleading.
	var projectCtx *ProjectContext
	if time.Since(s.StartedAt) < 48*time.Hour {
		projectCtx = buildProjectContext(store, s.ProjectShort)
	}
	userMsg := BuildNarrativeUserMessageWithContext(summaries, s.ProjectShort, sessionTimeRange(s), projectCtx)

	response, err := client.Complete(NarrativePrompt(), userMsg)
	if err != nil {
		log.Printf("  narrative %s: %v", truncID(s.ID), err)
		return
	}

	narrative := CleanNarrativeResponse(response)
	if narrative == "" {
		return
	}

	// Inherit flavor + intensity from peer learnings of same session
	flavor, intensity := getSessionFlavorAndIntensity(store, s.ID)

	store.InsertLearning(&models.Learning{
		Category:           "narrative",
		Content:            narrative,
		Project:            s.ProjectShort,
		SessionID:          s.ID,
		CreatedAt:          s.StartedAt,
		ModelUsed:          client.Model(),
		Confidence:         1.0,
		Source:             "llm_extracted",
		SessionFlavor:      flavor,
		EmotionalIntensity: intensity,
	})
	// Keep only last 2 narratives per project — old ones have no standalone value.
	if n, _ := store.DeleteOldNarratives(s.ProjectShort, 2); n > 0 {
		log.Printf("  Narrative cleanup: deleted %d old narratives for %s", n, s.ProjectShort)
	}
	log.Printf("  Narrative saved: %s (%s)", truncID(s.ID), s.ProjectShort)
}

// RegenerateNarrativeForSession creates a new immersive narrative, superseding the old one.
// Returns true if a new narrative was generated.
func RegenerateNarrativeForSession(store *storage.Store, s models.Session, client extraction.LLMClient) bool {
	if client == nil {
		return false
	}

	msgs, err := store.GetMessagesBySession(s.ID)
	if err != nil || len(msgs) < minNarrativeMessages {
		log.Printf("  regen %s: skipped (msgs=%d, need %d)", truncID(s.ID), len(msgs), minNarrativeMessages)
		return false
	}

	summaries := SummarizeMessages(msgs)
	if len(summaries) < minNarrativeMessages {
		log.Printf("  regen %s: skipped (only %d text summaries)", truncID(s.ID), len(summaries))
		return false
	}

	var projectCtx *ProjectContext
	if time.Since(s.StartedAt) < 48*time.Hour {
		projectCtx = buildProjectContext(store, s.ProjectShort)
	}
	userMsg := BuildNarrativeUserMessageWithContext(summaries, s.ProjectShort, sessionTimeRange(s), projectCtx)

	response, err := client.Complete(NarrativePrompt(), userMsg)
	if err != nil {
		log.Printf("  regen %s: %v", truncID(s.ID), err)
		return false
	}

	narrative := CleanNarrativeResponse(response)
	if narrative == "" {
		log.Printf("  regen %s: skipped (too short)", truncID(s.ID))
		return false
	}

	// Supersede existing narratives for this session BEFORE inserting new one
	// IDs returned for vector store cleanup (handled by daemon startup cleanup)
	store.SupersedeNarrativesBySession(s.ID, "immersive narrative upgrade")

	// Inherit flavor + intensity from peer learnings of same session
	flavor, intensity := getSessionFlavorAndIntensity(store, s.ID)

	// Insert new narrative
	_, err = store.InsertLearning(&models.Learning{
		Category:           "narrative",
		Content:            narrative,
		Project:            s.ProjectShort,
		SessionID:          s.ID,
		CreatedAt:          s.StartedAt,
		ModelUsed:          client.Model(),
		Confidence:         1.0,
		Source:             "llm_extracted",
		SessionFlavor:      flavor,
		EmotionalIntensity: intensity,
	})
	if err != nil {
		log.Printf("  regen %s: insert failed: %v", truncID(s.ID), err)
		return false
	}
	if n, _ := store.DeleteOldNarratives(s.ProjectShort, 2); n > 0 {
		log.Printf("  Narrative cleanup: deleted %d old narratives for %s", n, s.ProjectShort)
	}
	log.Printf("  Narrative regenerated: %s (%s)", truncID(s.ID), s.ProjectShort)
	return true
}
