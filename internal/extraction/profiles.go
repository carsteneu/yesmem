package extraction

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

const profilePrompt = `Create a compact project profile based on learnings, narratives, and sessions.

Format (each line under 80 characters, facts only, do not fabricate):

Character:     [Type of project, state, direction]
Tech stack:    [Languages, frameworks, tools — ONLY what is evidenced in learnings/narratives]
Minefields:    [Fragile/complex areas]
Clean parts:   [Well-structured areas]
Testing:       [Test situation]
Deployment:    [How deployment works]
Distinguisher: [What makes this project unique]

IMPORTANT: The tech stack MUST be derived from the learnings and narratives, NOT guessed.
If there are no clear indicators for a technology, write "unclear" instead of guessing.`

// GenerateProjectProfile creates or updates a project profile via LLM.
func GenerateProjectProfile(client LLMClient, store *storage.Store, project string) error {
	sessions, err := store.ListSessions(project, 20)
	if err != nil || len(sessions) < 3 {
		return nil // Not enough data yet
	}

	// Build summary of sessions
	var sessionSummary strings.Builder
	for i, s := range sessions {
		msg := s.FirstMessage
		if len(msg) > 100 {
			msg = msg[:100]
		}
		sessionSummary.WriteString(fmt.Sprintf("%d. [%s] %s (Branch: %s, %d msgs)\n",
			i+1, s.StartedAt.Format("2006-01-02"), msg, s.GitBranch, s.MessageCount))
	}

	// Load learnings for this project (top 5 per relevant category)
	var learningsSummary strings.Builder
	categories := []string{"gotcha", "decision", "pattern", "explicit_teaching"}
	for _, cat := range categories {
		learnings, err := store.GetActiveLearnings(cat, project, "", "")
		if err != nil || len(learnings) == 0 {
			continue
		}
		limit := 5
		if len(learnings) < limit {
			limit = len(learnings)
		}
		learningsSummary.WriteString(fmt.Sprintf("\n### %s\n", cat))
		for i := 0; i < limit; i++ {
			content := learnings[i].Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			learningsSummary.WriteString(fmt.Sprintf("- %s\n", content))
		}
	}

	// Load recent narratives
	var narrativesSummary strings.Builder
	narratives, _ := store.GetRecentNarratives(project, 3)
	for _, n := range narratives {
		content := n.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		narrativesSummary.WriteString(fmt.Sprintf("- %s\n", content))
	}

	// Build full prompt
	var prompt strings.Builder
	prompt.WriteString(fmt.Sprintf("Project: %s\n\n", project))

	if learningsSummary.Len() > 0 {
		prompt.WriteString("## Learnings (verified knowledge)\n")
		prompt.WriteString(learningsSummary.String())
		prompt.WriteString("\n")
	}

	if narrativesSummary.Len() > 0 {
		prompt.WriteString("## Narratives (recent session handovers)\n")
		prompt.WriteString(narrativesSummary.String())
		prompt.WriteString("\n")
	}

	prompt.WriteString(fmt.Sprintf("## Sessions (%d analyzed)\n", len(sessions)))
	prompt.WriteString(sessionSummary.String())

	response, err := client.Complete(profilePrompt, prompt.String())
	if err != nil {
		return fmt.Errorf("generate profile for %s: %w", project, err)
	}

	profile := &models.ProjectProfile{
		Project:      project,
		ProfileText:  response,
		GeneratedAt:  time.Now(),
		UpdatedAt:    time.Now(),
		SessionCount: len(sessions),
		ModelUsed:    client.Model(),
	}

	if err := store.UpsertProjectProfile(profile); err != nil {
		return fmt.Errorf("store profile: %w", err)
	}

	log.Printf("Project profile updated for %s (%d sessions)", project, len(sessions))
	return nil
}
