
package locomo

import (
	"fmt"
	"log"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// IngestStats holds counters for a single sample ingestion.
type IngestStats struct {
	Project  string
	Sessions int
	Messages int
}

// IngestSample creates sessions and messages in the store for a single LoCoMo sample.
// Project naming: "locomo_{sample_id}", session IDs: "{project}_session_{num}".
// Speaker mapping: speaker_a -> "user", speaker_b -> "assistant".
// Timestamps are parsed from session DateTime, NOT time.Now().
func IngestSample(store *storage.Store, sample Sample) (IngestStats, error) {
	project := fmt.Sprintf("locomo_%s", sample.ID)
	stats := IngestStats{Project: project}

	for _, sess := range sample.Sessions {
		sessionID := fmt.Sprintf("%s_session_%d", project, sess.Index)

		// Parse session timestamp from LoCoMo data
		sessionTime, err := parseLocomoTime(sess.DateTime)
		if err != nil {
			return stats, fmt.Errorf("parse datetime for session %d: %w", sess.Index, err)
		}

		// Build messages
		var msgs []models.Message
		for i, turn := range sess.Turns {
			role := mapSpeaker(turn.Speaker)
			// Offset each turn by its sequence index to preserve ordering
			ts := sessionTime.Add(time.Duration(i) * time.Second)
			msgs = append(msgs, models.Message{
				SessionID:   sessionID,
				Role:        role,
				MessageType: "text",
				Content:     turn.Text,
				Timestamp:   ts,
				Sequence:    i,
			})
		}

		// Determine first message text
		firstMessage := ""
		if len(sess.Turns) > 0 {
			firstMessage = sess.Turns[0].Text
		}

		// Compute end time
		endedAt := sessionTime
		if len(msgs) > 0 {
			endedAt = msgs[len(msgs)-1].Timestamp
		}

		// Upsert session
		modelSess := &models.Session{
			ID:           sessionID,
			Project:      project,
			ProjectShort: project,
			FirstMessage: firstMessage,
			MessageCount: len(msgs),
			StartedAt:    sessionTime,
			EndedAt:      endedAt,
			IndexedAt:    time.Now(),
		}
		if err := store.UpsertSession(modelSess); err != nil {
			return stats, fmt.Errorf("upsert session %s: %w", sessionID, err)
		}

		// Insert messages (delete old ones first to prevent duplicates across runs)
		if len(msgs) > 0 {
			if delErr := store.DeleteMessagesBySession(sessionID); delErr != nil {
				log.Printf("  [warn] delete old messages for %s: %v", sessionID, delErr)
			}
			if err := store.InsertMessages(msgs); err != nil {
				return stats, fmt.Errorf("insert messages for %s: %w", sessionID, err)
			}
		}

		stats.Sessions++
		stats.Messages += len(msgs)
	}

	return stats, nil
}

// IngestAll ingests all samples into the store, returning per-sample stats.
func IngestAll(store *storage.Store, samples []Sample) ([]IngestStats, error) {
	results := make([]IngestStats, 0, len(samples))
	for _, sample := range samples {
		stats, err := IngestSample(store, sample)
		if err != nil {
			return results, fmt.Errorf("ingest sample %s: %w", sample.ID, err)
		}
		results = append(results, stats)
	}
	return results, nil
}

// IngestGoldObservations inserts LoCoMo gold-standard observations as learnings.
// No LLM extraction needed — these are pre-annotated facts from the dataset.
// Used to measure search-quality ceiling (perfect extraction).
func IngestGoldObservations(store *storage.Store, samples []Sample) (int, error) {
	total := 0
	for _, sample := range samples {
		log.Printf("  [gold] %s: %d observations", sample.ID, len(sample.Observations))
		if len(sample.Observations) == 0 {
			continue
		}
		project := fmt.Sprintf("locomo_%s", sample.ID)
		var learnings []models.Learning
		for _, obs := range sample.Observations {
			learnings = append(learnings, models.Learning{
				Category:   "fact",
				Content:    obs.Text,
				Project:    project,
				Confidence: 1.0,
				Source:     "user_stated",
				CreatedAt:  time.Now(),
				Entities:   []string{obs.Speaker},
				AnticipatedQueries: []string{},
				Keywords:   []string{obs.Speaker},
			})
		}
		var plearnings []*models.Learning
		for i := range learnings {
			plearnings = append(plearnings, &learnings[i])
		}
		ids, err := store.InsertLearningBatch(plearnings)
		if err != nil {
			return total, fmt.Errorf("insert gold observations for %s: %w", project, err)
		}
		total += len(ids)
	}
	return total, nil
}
func mapSpeaker(speaker string) string {
	switch speaker {
	case "speaker_a":
		return "user"
	case "speaker_b":
		return "assistant"
	default:
		return speaker
	}
}

// parseLocomoTime parses LoCoMo datetime formats.
// Supports: "2023-03-15T10:00:00", RFC3339, and "3:04 pm on 2 January, 2006".
func parseLocomoTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty datetime")
	}
	formats := []string{
		"2006-01-02T15:04:05",
		time.RFC3339,
		"3:04 pm on 2 January, 2006",
		"3:04 am on 2 January, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse %q: no matching format", s)
}
