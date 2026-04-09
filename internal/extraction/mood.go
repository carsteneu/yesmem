package extraction

import (
	"encoding/json"
	"fmt"
)

const moodPrompt = `Analyze the communication dynamics of these recent user messages.

Mood types:
- relaxed_exploratory: Relaxed, open to discussion
- focused_productive: In the flow, knows what they want
- stressed_urgent: Under pressure, needs quick help
- frustrated_debugging: Frustrated, problem remains unsolved
- learning_curious: Wants to understand, not just results

Reply as JSON:
{
  "mood": "...",
  "confidence": 0.0-1.0,
  "indicators": ["..."],
  "recommendation": "..."
}`

// MoodResult holds the mood analysis result.
type MoodResult struct {
	Mood           string   `json:"mood"`
	Confidence     float64  `json:"confidence"`
	Indicators     []string `json:"indicators"`
	Recommendation string   `json:"recommendation"`
}

// SenseMood analyzes the communication dynamics from recent messages.
func SenseMood(client *Client, recentMessages []string) (*MoodResult, error) {
	var content string
	for i, msg := range recentMessages {
		content += fmt.Sprintf("Message %d: %s\n\n", i+1, msg)
	}

	response, err := client.Complete(moodPrompt, content)
	if err != nil {
		return nil, fmt.Errorf("mood sensing: %w", err)
	}

	jsonStr := extractJSON(response)
	var result MoodResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse mood: %w", err)
	}

	return &result, nil
}
