package briefing

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// PersonaSynthesisSystemPrompt is the system prompt for generating persona directives.
const PersonaSynthesisSystemPrompt = `Generate a persona directive for Claude based on persona traits and key moments.

FORMAT — MANDATORY:

1. RELATIONSHIP ANCHOR (first 2-3 sentences): Use the provided key moments
   to build a personal connection. Concrete quotes and situations,
   not abstract properties. Instead of "pragmatic" write "the person who said X".
   Start with the session count. The goal: Claude should feel when reading
   "I know this person", not "I read a profile".
   If no key moments are available, use the strongest traits
   to still provide a personal description.

2. HARD RULES: Bullet list with rules that MUST NOT be forgotten.
   Only traits with confidence >= 0.7 here. Short, imperative.

3. COMMUNICATION STYLE: If proactive_status_updates or explain_decisions
   traits are present, a rule MUST be included that makes clear:
   "concise" does NOT mean "silent" — brief but transparent. Proactive
   status updates at milestones, explain decisions,
   provide summaries after larger blocks.

Confidence mapping:
- 0.9+ → In relationship anchor as fact + in hard rules
- 0.7-0.9 → In hard rules
- 0.4-0.7 → Omit (too uncertain)

Maximum 18 lines total. No meta-talk. No "Persona Directive" header —
that is added automatically.`

// TraitsHash computes a stable hash over active traits for cache invalidation.
func TraitsHash(traits []models.PersonaTrait) string {
	// Sort for deterministic hashing
	sorted := make([]models.PersonaTrait, len(traits))
	copy(sorted, traits)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Dimension != sorted[j].Dimension {
			return sorted[i].Dimension < sorted[j].Dimension
		}
		return sorted[i].TraitKey < sorted[j].TraitKey
	})

	h := sha256.New()
	for _, t := range sorted {
		fmt.Fprintf(h, "%s|%s|%s|%.2f\n", t.Dimension, t.TraitKey, t.TraitValue, t.Confidence)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// BuildSynthesisPrompt builds the user message for persona directive synthesis.
func BuildSynthesisPrompt(traits []models.PersonaTrait, sessionCount int, pivots []models.Learning) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session count: %d\n\nActive persona traits:\n", sessionCount))

	for _, t := range traits {
		sb.WriteString(fmt.Sprintf("- %s.%s = %q (confidence: %.1f, source: %s, evidence: %d)\n",
			t.Dimension, t.TraitKey, t.TraitValue, t.Confidence, t.Source, t.EvidenceCount))
	}

	if len(pivots) > 0 {
		sb.WriteString("\nKey moments (for relationship anchors):\n")
		for _, p := range pivots {
			content := p.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("- %s\n", content))
		}
	}

	return sb.String()
}

// FormatPersonaDirective wraps a directive in the delivery format for the briefing.
func FormatPersonaDirective(directive string) string {
	if directive == "" {
		return ""
	}
	return directive + "\n"
}

// FormatPersonaDirectiveLimited limits persona directive to maxLines bullet points.
// Lines starting with "- " are counted as rules. Adds a search hint at the end.
func FormatPersonaDirectiveLimited(directive string, maxLines int) string {
	if directive == "" {
		return ""
	}
	lines := strings.Split(directive, "\n")
	var result []string
	ruleCount := 0
	totalRules := 0

	// Count total rules first
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- ") {
			totalRules++
		}
	}

	// Add headline with spacing
	result = append(result, "\nArbeitsbeziehung:")

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "- ") {
			ruleCount++
			if ruleCount > maxLines {
				continue
			}
		}
		result = append(result, l)
	}

	if totalRules > maxLines {
		result = append(result, fmt.Sprintf("(%d more rules via get_learnings(\"preference\"))", totalRules-maxLines))
	}

	return strings.Join(result, "\n") + "\n"
}
