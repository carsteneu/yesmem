package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

const recurrencePrompt = `Du bekommst einen Cluster ähnlicher Learnings die wiederholt auftauchen.
Frage: Deuten diese auf ein tieferes Architektur-/Design-Problem hin?

Wenn ja: Beschreibe in 1-2 Sätzen (max 200 Zeichen) was die wahrscheinliche Ursache ist und was geprüft werden sollte.
Wenn nein (z.B. normales Feature-Cluster, Lern-Fortschritt, verschiedene Aspekte eines Themas): antworte NUR mit "kein_problem".`

// DetectRecurrence finds clusters with recurring patterns and generates alerts.
// Step 1: Heuristic pre-filter (zero cost). Step 2: Haiku interpretation (only candidates).
func DetectRecurrence(store *storage.Store, client extraction.LLMClient) int {
	projects, err := store.ListProjects()
	if err != nil {
		return 0
	}

	var totalAlerts int
	for _, p := range projects {
		alerts := detectForProject(store, client, p.Project)
		totalAlerts += alerts
	}
	return totalAlerts
}

func detectForProject(store *storage.Store, client extraction.LLMClient, project string) int {
	clusters, err := store.GetLearningClusters(project)
	if err != nil {
		return 0
	}

	// Load existing recurrence alerts for dedup
	existingAlerts, _ := store.GetLearningsByCategory("recurrence_alert", project, 100)
	alertLabels := make(map[string]bool, len(existingAlerts))
	for _, a := range existingAlerts {
		alertLabels[a.Content] = true
	}

	var alertCount int
	for _, cluster := range clusters {
		if !isRecurrenceCandidate(cluster) {
			continue
		}

		// Parse learning IDs
		var ids []int64
		if err := json.Unmarshal([]byte(cluster.LearningIDs), &ids); err != nil {
			continue
		}

		// Load active learnings + count distinct sessions
		var learnings []models.Learning
		sessionSet := make(map[string]bool)
		for _, id := range ids {
			l, err := store.GetLearning(id)
			if err != nil || l == nil || l.SupersededBy != nil {
				continue
			}
			learnings = append(learnings, *l)
			if l.SessionID != "" {
				sessionSet[l.SessionID] = true
			}
		}

		if len(learnings) < 3 || len(sessionSet) < 2 {
			continue
		}

		// Dedup: skip if alert for this cluster label already exists
		if containsLabel(alertLabels, cluster.Label) {
			continue
		}

		// Step 2: LLM interpretation (or template fallback)
		alertText := interpretCluster(client, cluster, learnings, len(sessionSet))
		if alertText == "" {
			continue
		}

		// Insert as learning
		newLearning := &models.Learning{
			Content:    alertText,
			Category:   "recurrence_alert",
			Project:    project,
			Source:     "consolidated",
			Importance: 5,
		}
		if _, err := store.InsertLearning(newLearning); err != nil {
			log.Printf("  warn: insert recurrence alert: %v", err)
			continue
		}

		alertCount++
		log.Printf("  ⚠ Recurrence: %q → %s", cluster.Label, alertText)
	}

	return alertCount
}

func isRecurrenceCandidate(c models.LearningCluster) bool {
	return c.LearningCount >= 3 && c.AvgRecencyDays < 14
}

func interpretCluster(client extraction.LLMClient, cluster models.LearningCluster, learnings []models.Learning, sessionCount int) string {
	if client == nil {
		return templateAlert(cluster, learnings, sessionCount)
	}

	// Build context for LLM
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Cluster: %q (%d Learnings, %d Sessions, Ø%.0f Tage alt)\n\n",
		cluster.Label, len(learnings), sessionCount, cluster.AvgRecencyDays))
	for _, l := range learnings {
		sb.WriteString(fmt.Sprintf("- [%s] [%s] %s\n", l.Category, l.CreatedAt.Format(time.DateOnly), l.Content))
	}

	response, err := client.Complete(recurrencePrompt, sb.String())
	if err != nil {
		log.Printf("  warn: recurrence LLM call: %v", err)
		return templateAlert(cluster, learnings, sessionCount)
	}

	response = strings.TrimSpace(response)
	if strings.Contains(strings.ToLower(response), "kein_problem") {
		return ""
	}

	return fmt.Sprintf("⚠ %q: %s", cluster.Label, response)
}

func templateAlert(cluster models.LearningCluster, learnings []models.Learning, sessionCount int) string {
	days := int(cluster.AvgRecencyDays)
	if days < 1 {
		days = 1
	}
	return fmt.Sprintf("Recurring pattern: %q (%d learnings in %d sessions over %d days). Consider architectural fix.",
		cluster.Label, len(learnings), sessionCount, days)
}

func containsLabel(alertSet map[string]bool, label string) bool {
	for content := range alertSet {
		if strings.Contains(content, label) {
			return true
		}
	}
	return false
}
