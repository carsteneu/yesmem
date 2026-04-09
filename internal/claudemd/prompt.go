package claudemd

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

const systemPrompt = `You are a technical editor. You condense learnings from a software project into a compact operations reference.`

func buildPrompt(projectName string, learnings []models.Learning) (string, error) {
	// Group by category for structured input — plaintext is ~40% fewer tokens than JSON
	groups := make(map[string][]models.Learning)
	var order []string
	for _, l := range learnings {
		if _, seen := groups[l.Category]; !seen {
			order = append(order, l.Category)
		}
		groups[l.Category] = append(groups[l.Category], l)
	}

	var sb strings.Builder
	for _, cat := range order {
		fmt.Fprintf(&sb, "\n### %s\n", cat)
		for _, l := range groups[cat] {
			src := ""
			if l.Source == "user_stated" {
				src = " [User]"
			}
			fmt.Fprintf(&sb, "- %s%s\n", l.Content, src)
		}
	}

	return fmt.Sprintf(`You receive learnings from the software project "%s".
Create a compact operations reference in Markdown from them.

Rules:
- Group semantically into sections (e.g. Deployment, Known Pitfalls, Data Flow, Architecture Decisions, Patterns, Conventions)
- Only create sections that have content
- Deduplicate: same info only once, choose the most concise wording
- Each point max 1-2 lines
- Max 60 bullet points total — prioritize the most important ones
- [User]-marked learnings have higher priority
- Language: match the learnings (mixing de/en is ok)
- No introduction, no conclusion — just the reference
- Format: ## Section Name → bullet points

Learnings:
%s`, projectName, sb.String()), nil
}
