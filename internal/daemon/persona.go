package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/clustering"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// extractPersonaSignals runs persona signal extraction over recent sessions (default limit: 10).
func extractPersonaSignals(store *storage.Store, sessions []models.Session, cfg *config.Config, client extraction.LLMClient) {
	extractPersonaSignalsWithLimit(store, sessions, cfg, client, 10)
}

// ExtractPersonaSignalsWithLimit is the exported version for CLI commands.
func ExtractPersonaSignalsWithLimit(store *storage.Store, sessions []models.Session, cfg *config.Config, client extraction.LLMClient, maxSessions int) {
	extractPersonaSignalsWithLimit(store, sessions, cfg, client, maxSessions)
}

// extractPersonaSignalsWithLimit runs persona signal extraction with a configurable session limit.
// Use limit=0 to process ALL sessions (for bootstrap/initial import).
func extractPersonaSignalsWithLimit(store *storage.Store, sessions []models.Session, cfg *config.Config, client extraction.LLMClient, maxSessions int) {
	if client == nil {
		log.Println("  Skipped: no LLM client")
		return
	}

	// Load existing traits for context
	existingTraits, err := store.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		log.Printf("warn: load persona traits: %v", err)
		return
	}

	// Filter sessions with enough messages
	var candidates []models.Session
	for _, s := range sessions {
		if s.MessageCount >= 10 {
			candidates = append(candidates, s)
		}
	}

	// Apply limit (0 = unlimited for bootstrap)
	if maxSessions > 0 && len(candidates) > maxSessions {
		candidates = candidates[:maxSessions]
	}

	if len(candidates) == 0 {
		log.Println("  No sessions for persona extraction")
		return
	}

	// Build the extraction prompt with existing traits
	basePrompt := extraction.BuildPersonaExtractionPrompt(existingTraits)

	processed := 0
	total := len(candidates)
	for i, s := range candidates {
		msgs, err := store.GetMessagesBySession(s.ID)
		if err != nil || len(msgs) < 10 {
			continue
		}

		// Build session summary for the prompt (reuse narrative summaries)
		summaries := SummarizeMessages(msgs)
		sessionContent := basePrompt + strings.Join(summaries, "\n")

		response, err := client.CompleteJSON(
			extraction.PersonaExtractionSystemPrompt,
			sessionContent,
			extraction.PersonaExtractionSchema(),
		)
		if err != nil {
			if strings.Contains(err.Error(), "rate_limit") {
				log.Printf("  [%d/%d] Rate limited, waiting 30s...", i+1, total)
				time.Sleep(30 * time.Second)
			}
			log.Printf("  [%d/%d] warn: persona extraction for %s: %v", i+1, total, truncID(s.ID), err)
			continue
		}

		result, err := extraction.ParsePersonaExtractionResponse(response)
		if err != nil {
			log.Printf("  [%d/%d] warn: parse persona for %s: %v", i+1, total, truncID(s.ID), err)
			continue
		}

		// Apply updates to existing traits
		applyPersonaUpdates(store, result, client.Model())
		processed++

		if processed%10 == 0 {
			log.Printf("  [%d/%d] %d sessions processed, %d traits active", i+1, total, processed, countActiveTraits(store))
		}

		time.Sleep(2 * time.Second) // Rate limit courtesy
	}

	log.Printf("  Persona extraction: %d sessions processed, %d traits active",
		processed, countActiveTraits(store))

	// Note: synthesizePersonaDirective is called separately in extract.go
	// with the quality client (Opus) for better relationship anchors.
}

// validDimensions defines the allowed persona dimensions.
var validDimensions = map[string]bool{
	"communication": true, "workflow": true, "expertise": true,
	"context": true, "boundaries": true, "learning_style": true,
}

// allowedTraitKeys defines fixed keys per dimension. Dimensions not listed allow any key.
var allowedTraitKeys = map[string]map[string]bool{
	"communication": {"language": true, "formality": true, "tone": true, "answer_length": true, "explain_depth": true, "humor": true, "emoji_usage": true, "introspection": true, "pushback": true, "status_updates": true},
	"workflow":      {"autonomy": true, "debugging_style": true, "commit_style": true, "decision_velocity": true, "analysis_first": true, "methodology": true, "delegation_style": true, "verification": true, "deploy_method": true, "automation": true},
	"boundaries":    {"auto_commit": true, "force_push": true, "design_approval": true, "legal_compliance": true, "external_services": true, "disruptive_actions": true},
	"learning_style": {"examples": true, "tradeoff_tables": true, "visual": true, "depth": true, "format": true},
}

func isAllowedTraitKey(dimension, key string) bool {
	allowed, hasWhitelist := allowedTraitKeys[dimension]
	if !hasWhitelist {
		return true
	}
	return allowed[key]
}

// applyPersonaUpdates applies extracted persona signals to the trait store.
func applyPersonaUpdates(store *storage.Store, result *extraction.PersonaExtractionResult, model string) {
	// Apply confidence deltas to existing traits
	for _, update := range result.Updates {
		dim := update.Dimension
		key := update.TraitKey
		if !validDimensions[dim] {
			log.Printf("  warn: skipping trait with invalid dimension %q", dim)
			continue
		}
		if !isAllowedTraitKey(dim, key) {
			log.Printf("  warn: rejecting unknown %s key %q", dim, key)
			continue
		}

		// Clamp delta to max +0.1 per session
		delta := update.ConfidenceDelta
		if delta > 0.1 {
			delta = 0.1
		}
		if delta < -0.2 {
			delta = -0.2
		}

		existing, _ := store.GetPersonaTrait("default", dim, key)
		if existing != nil {
			// If the value changed (contradiction), apply negative delta
			if existing.TraitValue != update.TraitValue && delta > 0 {
				delta = -0.2
			}
			store.ApplyConfidenceDelta("default", dim, key, delta)
		} else {
			// Trait doesn't exist yet — treat as new
			store.UpsertPersonaTrait(&models.PersonaTrait{
				UserID:        "default",
				Dimension:     dim,
				TraitKey:      key,
				TraitValue:    update.TraitValue,
				Confidence:    0.5 + delta,
				Source:        "auto_extracted",
				EvidenceCount: 1,
			})
		}
	}

	// Insert new traits (max 2 enforced server-side)
	newCount := 0
	for _, newTrait := range result.NewTraits {
		if newCount >= 2 {
			log.Printf("  warn: dropping excess new trait %s.%s (max 2 per session)", newTrait.Dimension, newTrait.TraitKey)
			break
		}

		dim := newTrait.Dimension
		key := newTrait.TraitKey
		if !validDimensions[dim] {
			log.Printf("  warn: skipping new trait with invalid dimension %q", dim)
			continue
		}
		if !isAllowedTraitKey(dim, key) {
			log.Printf("  warn: rejecting unknown %s key %q", dim, key)
			continue
		}

		confidence := newTrait.Confidence
		if confidence <= 0 || confidence > 1.0 {
			confidence = 0.5
		}
		store.UpsertPersonaTrait(&models.PersonaTrait{
			UserID:        "default",
			Dimension:     dim,
			TraitKey:      key,
			TraitValue:    newTrait.TraitValue,
			Confidence:    confidence,
			Source:        "auto_extracted",
			EvidenceCount: 1,
		})
		newCount++
	}
}

// synthesizePersonaDirective generates a new persona directive if traits have changed.
func synthesizePersonaDirective(store *storage.Store, client extraction.LLMClient) {
	if client == nil {
		return
	}

	// Only use well-evidenced traits for directive (evidence >= 3, or user_override/bootstrapped)
	traits, err := store.GetWellEvidencedTraits("default", 0.4, 3)
	if err != nil || len(traits) == 0 {
		return
	}

	// Check if hash changed since last directive
	newHash := briefing.TraitsHash(traits)
	existing, _ := store.GetPersonaDirective("default")
	if existing != nil && existing.TraitsHash == newHash {
		log.Println("  Persona directive up-to-date (hash unchanged)")
		return
	}

	// Count total sessions for the synthesis prompt
	projects, _ := store.ListProjects()
	totalSessions := 0
	for _, p := range projects {
		totalSessions += p.SessionCount
	}

	// Load top pivot moments for relationship anchors
	pivots := loadTopPivotMoments(store, 5)

	prompt := briefing.BuildSynthesisPrompt(traits, totalSessions, pivots)
	response, err := client.Complete(briefing.PersonaSynthesisSystemPrompt, prompt)
	if err != nil {
		log.Printf("  warn: persona synthesis: %v", err)
		return
	}

	directive := strings.TrimSpace(response)
	if directive == "" {
		return
	}

	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "default",
		Directive:   directive,
		TraitsHash:  newHash,
		GeneratedAt: time.Now(),
		ModelUsed:   client.Model(),
	})
	log.Printf("  Persona directive generated (%d traits, hash: %s)", len(traits), truncID(newHash))
}

// userProfileSystemPrompt is the system prompt for user profile synthesis.
const userProfileSystemPrompt = `Du bist ein Analyst der ein praegnantes Profil eines Software-Entwicklers erstellt.
Basierend auf den folgenden Beobachtungen aus der Zusammenarbeit, erstelle ein
Profil mit diesen Abschnitten:

- Hintergrund & Beruf (1-2 Saetze)
- Technische Expertise (2-3 Saetze)
- Denkweise & Entscheidungsstil (2-3 Saetze)
- Arbeitsstil & Kommunikation (2-3 Saetze)
- Werte & Prinzipien (2-3 Saetze)
- Zusammenarbeit mit KI (1-2 Saetze)

Regeln:
- Max 500 Tokens, deutsch
- Keine Floskeln, keine Wertungen — nur Beobachtungen
- Schreibe in dritter Person ("Er/Sie...")
- Konkrete Beispiele statt Abstraktionen`

// synthesizeUserProfile generates a user profile from learnings and persona traits.
// Re-synthesizes only when input data changed AND the profile is older than 72h.
// Time guard prevents excessive re-synthesis during active extraction periods.
func synthesizeUserProfile(store *storage.Store, client extraction.LLMClient) {
	if client == nil {
		return
	}

	// Load input data: learnings with user_stated/agreed_upon source
	var allLearnings []models.Learning
	for _, cat := range []string{"preference", "explicit_teaching", "pattern", "relationship"} {
		learnings, _ := store.GetActiveLearnings(cat, "", "", "")
		for _, l := range learnings {
			if l.Source == "user_stated" || l.Source == "agreed_upon" {
				allLearnings = append(allLearnings, l)
			}
		}
	}

	// Load expertise traits
	expertiseTraits, _ := store.GetActivePersonaTraits("default", 0.0)
	var filteredTraits []models.PersonaTrait
	for _, t := range expertiseTraits {
		if t.Dimension == "expertise" {
			filteredTraits = append(filteredTraits, t)
		}
	}

	// Bail if no input data
	if len(allLearnings) == 0 && len(filteredTraits) == 0 {
		return
	}

	// Compute hash over input data
	newHash := userProfileInputHash(allLearnings, filteredTraits)

	// Check existing profile
	existing, _ := store.GetPersonaDirective("user_profile")
	if existing != nil {
		// Hash check: skip if input unchanged
		if existing.TraitsHash == newHash {
			log.Println("  User profile up-to-date (hash unchanged)")
			return
		}
		// Time guard: skip if profile is less than 72h old (even if hash changed)
		if time.Since(existing.GeneratedAt) < 72*time.Hour {
			log.Println("  User profile hash changed but less than 72h old, skipping")
			return
		}
	}

	// Build user prompt
	var sb strings.Builder
	sb.WriteString("Beobachtungen aus der Zusammenarbeit:\n\n")

	if len(allLearnings) > 0 {
		sb.WriteString("Praeferenzen & Muster:\n")
		for _, l := range allLearnings {
			content := l.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", l.Category, content))
		}
	}

	if len(filteredTraits) > 0 {
		sb.WriteString("\nTechnische Expertise (aus Analyse):\n")
		for _, t := range filteredTraits {
			sb.WriteString(fmt.Sprintf("- %s: %s (confidence: %.1f, evidence: %d)\n",
				t.TraitKey, t.TraitValue, t.Confidence, t.EvidenceCount))
		}
	}

	response, err := client.Complete(userProfileSystemPrompt, sb.String())
	if err != nil {
		log.Printf("  warn: user profile synthesis: %v", err)
		return
	}

	profile := strings.TrimSpace(response)
	if profile == "" {
		return
	}

	store.SavePersonaDirective(&models.PersonaDirective{
		UserID:      "user_profile",
		Directive:   profile,
		TraitsHash:  newHash,
		GeneratedAt: time.Now(),
		ModelUsed:   client.Model(),
	})
	log.Printf("  User profile generated (%d learnings, %d expertise traits, hash: %s)",
		len(allLearnings), len(filteredTraits), truncID(newHash))
}

// userProfileInputHash computes a deterministic hash over the input data for cache invalidation.
func userProfileInputHash(learnings []models.Learning, traits []models.PersonaTrait) string {
	h := fmt.Sprintf("%d|", len(learnings))
	for _, l := range learnings {
		h += fmt.Sprintf("%d:%s;", l.ID, l.Category)
	}
	h += fmt.Sprintf("|%d|", len(traits))
	for _, t := range traits {
		h += fmt.Sprintf("%s:%.2f;", t.TraitKey, t.Confidence)
	}
	return briefing.TraitsHash([]models.PersonaTrait{
		{Dimension: "hash_input", TraitKey: "user_profile", TraitValue: h, Confidence: 1.0},
	})
}

// SynthesizeUserProfile is the exported version for CLI commands.
func SynthesizeUserProfile(store *storage.Store, client extraction.LLMClient) {
	synthesizeUserProfile(store, client)
}

// BootstrapPersonaFromLearnings is the exported version for CLI commands.
// If force is true, skips the "already has traits" check.
func BootstrapPersonaFromLearnings(store *storage.Store, force bool) int {
	if force {
		return bootstrapPersonaFromLearningsForce(store)
	}
	return bootstrapPersonaFromLearnings(store)
}

// SynthesizePersonaDirective is the exported version for CLI commands.
func SynthesizePersonaDirective(store *storage.Store, client extraction.LLMClient) {
	synthesizePersonaDirective(store, client)
}

// bootstrapPersonaFromLearnings converts existing preference/relationship learnings
// into initial persona traits. Skips if traits already exist (not a first-time setup).
// Returns the number of traits created.
func bootstrapPersonaFromLearnings(store *storage.Store) int {
	// Skip if traits already exist — bootstrap is only for initial setup
	existing, _ := store.GetActivePersonaTraits("default", 0.0)
	if len(existing) > 0 {
		log.Printf("  Bootstrap skipped: %d traits already exist", len(existing))
		return 0
	}
	return bootstrapPersonaFromLearningsForce(store)
}

// bootstrapPersonaFromLearningsForce runs bootstrap regardless of existing traits.
func bootstrapPersonaFromLearningsForce(store *storage.Store) int {
	// Load preference and relationship learnings
	preferences, _ := store.GetActiveLearnings("preference", "", "", "")
	relationships, _ := store.GetActiveLearnings("relationship", "", "", "")

	all := append(preferences, relationships...)
	if len(all) == 0 {
		log.Println("  Bootstrap: no preference/relationship learnings found")
		return 0
	}

	count := 0
	for _, l := range all {
		traits := learningToTraits(l)
		for _, t := range traits {
			if err := store.UpsertPersonaTrait(&t); err == nil {
				count++
			}
		}
	}

	log.Printf("  Bootstrap: %d traits created from %d learnings", count, len(all))
	return count
}

// learningToTraits converts a preference/relationship learning into persona traits.
// Uses keyword matching to extract structured traits from free-text learnings.
func learningToTraits(l models.Learning) []models.PersonaTrait {
	content := strings.ToLower(l.Content)
	var traits []models.PersonaTrait

	// Language detection
	if strings.Contains(content, "deutsch") || strings.Contains(content, "german") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "communication", TraitKey: "language",
			TraitValue: "de", Confidence: 0.7, Source: "bootstrapped", EvidenceCount: 1,
		})
	}

	// Tone detection
	if strings.Contains(content, "locker") || strings.Contains(content, "casual") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "communication", TraitKey: "tone",
			TraitValue: "casual", Confidence: 0.7, Source: "bootstrapped", EvidenceCount: 1,
		})
	}
	if strings.Contains(content, "du") && (strings.Contains(content, "bevorzug") || strings.Contains(content, "prefer")) {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "communication", TraitKey: "formality",
			TraitValue: "informal_du", Confidence: 0.7, Source: "bootstrapped", EvidenceCount: 1,
		})
	}

	// Workflow detection
	if strings.Contains(content, "automatisierung") || strings.Contains(content, "automat") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "workflow", TraitKey: "automation_preference",
			TraitValue: "high", Confidence: 0.6, Source: "bootstrapped", EvidenceCount: 1,
		})
	}
	if strings.Contains(content, "qualit") && strings.Contains(content, "kosten") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "workflow", TraitKey: "quality_vs_cost",
			TraitValue: "quality_first", Confidence: 0.6, Source: "bootstrapped", EvidenceCount: 1,
		})
	}

	// Boundaries detection
	if strings.Contains(content, "nie") && strings.Contains(content, "commit") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "boundaries", TraitKey: "never_auto_commit",
			TraitValue: "true", Confidence: 0.7, Source: "bootstrapped", EvidenceCount: 1,
		})
	}
	if strings.Contains(content, "kein") && strings.Contains(content, "emoji") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "boundaries", TraitKey: "no_emoji_unless_asked",
			TraitValue: "true", Confidence: 0.7, Source: "bootstrapped", EvidenceCount: 1,
		})
	}

	// Learning style detection
	if strings.Contains(content, "tabelle") || strings.Contains(content, "table") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "learning_style", TraitKey: "wants_tradeoff_tables",
			TraitValue: "true", Confidence: 0.6, Source: "bootstrapped", EvidenceCount: 1,
		})
	}
	if strings.Contains(content, "option") && strings.Contains(content, "entscheid") {
		traits = append(traits, models.PersonaTrait{
			UserID: "default", Dimension: "learning_style", TraitKey: "wants_options",
			TraitValue: "true", Confidence: 0.6, Source: "bootstrapped", EvidenceCount: 1,
		})
	}

	// No generic fallback — only structured traits from keyword matching.
	// Unmatched learnings are left for LLM-based signal extraction (Phase 2).
	return traits
}

// ResetPersona deletes all persona traits and directives for a clean start.
func ResetPersona(store *storage.Store) {
	deleted := store.DeleteAllPersonaData("default")
	log.Printf("  Deleted %d traits + all directives", deleted)
}

// CleanupGenericTraits removes bootstrapped "preferences.*" traits that stored full learning text.
// These were a design mistake — only structured dimensional traits should exist.
func CleanupGenericTraits(store *storage.Store) int {
	traits, _ := store.GetActivePersonaTraits("default", 0.0)
	cleaned := 0
	for _, t := range traits {
		if t.Dimension == "preferences" && t.Source == "bootstrapped" {
			store.SupersedePersonaTrait("default", t.Dimension, t.TraitKey)
			cleaned++
		}
	}
	if cleaned > 0 {
		log.Printf("  Cleaned up %d generic 'preferences.*' traits", cleaned)
	}
	return cleaned
}

// techKeywords maps technology keywords to expertise trait keys.
// Each keyword is matched case-insensitively in learning content.
var techKeywords = map[string]string{
	"php":        "php",
	"symfony":    "symfony",
	"twig":       "twig",
	"doctrine":   "doctrine",
	"composer":   "composer",
	"go ":        "go",
	"golang":     "go",
	"sqlite":     "sqlite",
	"docker":     "docker",
	"ansible":    "ansible",
	"javascript": "javascript",
	"alpine.js":  "alpinejs",
	"mautic":     "mautic",
	"python":     "python",
	"css":        "css",
	"html":       "html",
	"bash":       "bash",
	"nginx":      "nginx",
	"apache":     "apache",
	"mysql":      "mysql",
	"postgresql": "postgresql",
	"redis":      "redis",
	"sulu":       "sulu",
	"wordpress":  "wordpress",
	"datatables": "datatables",
	"xdebug":     "xdebug",
}

// minExpertiseEvidence is the minimum number of keyword matches required
// to create an expertise trait. Below this threshold, evidence is too weak.
const minExpertiseEvidence = 3

// ExtractExpertiseFromLearnings is the exported version for CLI commands.
func ExtractExpertiseFromLearnings(store *storage.Store) int {
	return extractExpertiseFromLearnings(store)
}

// extractExpertiseFromLearnings scans all learnings for technology keywords
// and creates expertise traits when evidence exceeds the threshold.
// Returns the number of traits created/updated.
func extractExpertiseFromLearnings(store *storage.Store) int {
	// Count keyword hits across all learnings
	counts := make(map[string]int)

	// Scan all categories
	for _, cat := range []string{"pattern", "gotcha", "decision", "explicit_teaching"} {
		learnings, _ := store.GetActiveLearnings(cat, "", "", "")
		for _, l := range learnings {
			content := strings.ToLower(l.Content)
			for keyword, traitKey := range techKeywords {
				if strings.Contains(content, keyword) {
					counts[traitKey]++
				}
			}
		}
	}

	created := 0
	for traitKey, count := range counts {
		if count < minExpertiseEvidence {
			continue
		}

		// Confidence scales with evidence: 3→0.5, 10→0.7, 50+→0.9
		confidence := 0.5 + float64(count)*0.01
		if confidence > 0.9 {
			confidence = 0.9
		}

		store.UpsertPersonaTrait(&models.PersonaTrait{
			UserID:        "default",
			Dimension:     "expertise",
			TraitKey:      traitKey,
			TraitValue:    "high",
			Confidence:    confidence,
			Source:        "learning_scan",
			EvidenceCount: count,
		})
		created++
	}

	if created > 0 {
		log.Printf("  Expertise scan: %d traits from %d keyword matches", created, len(counts))
	}
	return created
}

// loadTopPivotMoments loads the most impactful pivot moments across all projects.
func loadTopPivotMoments(store *storage.Store, limit int) []models.Learning {
	pivots, err := store.GetActiveLearnings("pivot_moment", "", "", "")
	if err != nil || len(pivots) == 0 {
		return nil
	}
	models.ScoreAndSort(pivots)
	if len(pivots) > limit {
		pivots = pivots[:limit]
	}
	return pivots
}

// DecayContextTraits reduces confidence of stale context traits.
// Traits older than 30 days lose 0.1 per 30-day period.
// Traits dropping below 0.2 are superseded automatically.
func DecayContextTraits(store *storage.Store) int {
	traits, err := store.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		return 0
	}

	decayed := 0
	now := time.Now()
	for _, t := range traits {
		if t.Dimension != "context" {
			continue
		}
		age := now.Sub(t.UpdatedAt)
		if age < 30*24*time.Hour {
			continue
		}
		periods := int(age / (30 * 24 * time.Hour))
		decay := float64(periods) * 0.1
		newConf := t.Confidence - decay

		if newConf < 0.2 {
			store.SupersedePersonaTrait(t.UserID, t.Dimension, t.TraitKey)
		} else {
			store.ApplyConfidenceDelta(t.UserID, t.Dimension, t.TraitKey, -decay)
		}
		decayed++
	}
	if decayed > 0 {
		log.Printf("  Context trait decay: %d traits affected", decayed)
	}
	return decayed
}

// DedupPersonaTraits finds semantically duplicate traits within the same dimension
// using embedding cosine similarity and supersedes the weaker one.
// Exported for CLI commands. Called automatically in Phase 6 and bootstrap-persona.
func DedupPersonaTraits(store *storage.Store, provider embedding.Provider) {
	dedupPersonaTraits(store, provider)
}

// dedupPersonaTraits finds semantically duplicate traits within the same dimension
// using embedding cosine similarity and supersedes the weaker one.
// This runs automatically after persona extraction in Phase 6.
func dedupPersonaTraits(store *storage.Store, provider embedding.Provider) {
	if provider == nil || !provider.Enabled() {
		return
	}

	traits, err := store.GetActivePersonaTraits("default", 0.0)
	if err != nil || len(traits) < 2 {
		return
	}

	const threshold = 0.75

	// Build text representations for embedding — without dimension (same-dimension check is separate)
	texts := make([]string, len(traits))
	for i, t := range traits {
		texts[i] = fmt.Sprintf("%s %s", t.TraitKey, t.TraitValue)
	}

	vectors, err := provider.Embed(context.Background(), texts)
	if err != nil {
		log.Printf("  Persona dedup: embed failed: %v", err)
		return
	}

	// Find similar pairs within same dimension
	superseded := make(map[int]bool)
	dedupCount := 0

	for i := 0; i < len(vectors); i++ {
		if superseded[i] {
			continue
		}
		for j := i + 1; j < len(vectors); j++ {
			if superseded[j] {
				continue
			}
			if traits[i].Dimension != traits[j].Dimension {
				continue
			}
			sim := clustering.CosineSimilarity(vectors[i], vectors[j])
			if sim < threshold {
				continue
			}

			// Keep the one with higher confidence (or more evidence on tie)
			keep, drop := traits[i], traits[j]
			dropIdx := j
			if drop.Confidence > keep.Confidence || (drop.Confidence == keep.Confidence && drop.EvidenceCount > keep.EvidenceCount) {
				keep, drop = traits[j], traits[i]
				dropIdx = i
			}

			log.Printf("  Persona dedup: [%.2f] KEEP %s.%s=%q (conf:%.2f) DROP %s.%s=%q (conf:%.2f)",
				sim, keep.Dimension, keep.TraitKey, keep.TraitValue, keep.Confidence,
				drop.Dimension, drop.TraitKey, drop.TraitValue, drop.Confidence)

			store.SupersedePersonaTrait("default", drop.Dimension, drop.TraitKey)
			superseded[dropIdx] = true
			dedupCount++
		}
	}

	if dedupCount > 0 {
		log.Printf("  Persona dedup: %d traits superseded, %d remaining", dedupCount, len(traits)-dedupCount)
	}
}

// countActiveTraits returns the number of active persona traits.
func countActiveTraits(store *storage.Store) int {
	traits, err := store.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		return 0
	}
	return len(traits)
}
