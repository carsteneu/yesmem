package proxy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carsteneu/yesmem/internal/briefing"
)

// ExtractionResult holds the parsed output from the extract_and_evaluate fork.
type ExtractionResult struct {
	Learnings      []ExtractedLearning     `json:"learnings"`
	Evaluations    []LearningEvaluation    `json:"evaluations"`
	Contradictions []ContradictionDetected `json:"contradictions,omitempty"`
	SessionFlavor  string                  `json:"session_flavor,omitempty"`
}

// ExtractedLearning is a single learning extracted by the fork.
type ExtractedLearning struct {
	Content            string   `json:"content"`
	Category           string   `json:"category"`
	TaskType           string   `json:"task_type,omitempty"`
	Entities           []string `json:"entities"`
	Status             string   `json:"status"` // new, confirmed, revised, invalidated
	Context            string   `json:"context,omitempty"`
	Actions            []string `json:"actions,omitempty"`
	Keywords           []string `json:"keywords,omitempty"`
	AnticipatedQueries []string `json:"anticipated_queries,omitempty"`
	Importance         int      `json:"importance,omitempty"`          // 1-5, 5=critical
	EmotionalIntensity float64  `json:"emotional_intensity,omitempty"` // 0.0-1.0
}

// LearningEvaluation is a verdict on an injected learning.
type LearningEvaluation struct {
	LearningID  int64   `json:"-"`
	Verdict     string  `json:"verdict"`
	Reason      string  `json:"reason"`
	Action      string  `json:"action"`
	ImpactScore float64 `json:"impact_score"`
}

// rawLearningEvaluation mirrors LearningEvaluation but with json.Number
// for int fields — DeepSeek sometimes outputs strings for numeric fields.
type rawLearningEvaluation struct {
	LearningID  json.Number `json:"learning_id"`
	Verdict     string      `json:"verdict"`
	Reason      string      `json:"reason"`
	Action      string      `json:"action"`
	ImpactScore float64     `json:"impact_score"`
}

func (e *LearningEvaluation) UnmarshalJSON(data []byte) error {
	var raw rawLearningEvaluation
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := parseFlexInt64(raw.LearningID)
	if err != nil {
		return fmt.Errorf("learning_id: %w", err)
	}
	e.LearningID = id
	e.Verdict = raw.Verdict
	e.Reason = raw.Reason
	e.Action = raw.Action
	e.ImpactScore = raw.ImpactScore
	return nil
}

func (e LearningEvaluation) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"learning_id":  e.LearningID,
		"verdict":      e.Verdict,
		"reason":       e.Reason,
		"action":       e.Action,
		"impact_score": e.ImpactScore,
	})
}

// ContradictionDetected represents a conflict between two injected learnings.
type ContradictionDetected struct {
	LearningA   int64  `json:"-"`
	LearningB   int64  `json:"-"`
	Description string `json:"description"`
}

type rawContradiction struct {
	LearningA   json.Number `json:"learning_a"`
	LearningB   json.Number `json:"learning_b"`
	Description string      `json:"description"`
}

func (c *ContradictionDetected) UnmarshalJSON(data []byte) error {
	var raw rawContradiction
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a, err := parseFlexInt64(raw.LearningA)
	if err != nil {
		return fmt.Errorf("learning_a: %w", err)
	}
	b, err := parseFlexInt64(raw.LearningB)
	if err != nil {
		return fmt.Errorf("learning_b: %w", err)
	}
	c.LearningA = a
	c.LearningB = b
	c.Description = raw.Description
	return nil
}

func (c ContradictionDetected) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"learning_a":  c.LearningA,
		"learning_b":  c.LearningB,
		"description": c.Description,
	})
}

// parseFlexInt64 converts a json.Number (which can represent a JSON string
// or JSON number) to int64. Handles DeepSeek outputting numeric fields as
// strings.
func parseFlexInt64(n json.Number) (int64, error) {
	if n == "" {
		return 0, fmt.Errorf("empty value")
	}
	i, err := n.Int64()
	if err != nil {
		// json.Number.Int64() fails on strings like "123" — try strconv
		return strconv.ParseInt(n.String(), 10, 64)
	}
	return i, nil
}

// extractAndEvaluatePrompt generates the fork prompt for combined extraction + evaluation.
func extractAndEvaluatePrompt(ctx ForkContext) string {
	strs := briefing.ResolveStrings(briefing.DefaultStringsPath())
	var sb strings.Builder

	sb.WriteString(strs.ForkReflectionIntro)
	sb.WriteString("\n\n")

	// Task 1: Learnings
	sb.WriteString(strs.ForkTaskLearnings)
	sb.WriteString("\n")
	sb.WriteString(strs.ForkTaskLearningsBody)
	sb.WriteString("\n")

	if len(ctx.PreviousForkLearnings) == 0 {
		sb.WriteString(strs.ForkNoPrevious)
	} else {
		for _, l := range ctx.PreviousForkLearnings {
			fmt.Fprintf(&sb, "- [%s] %s\n", l.Category, l.Content)
		}
	}
	sb.WriteString("\n")

	sb.WriteString(strs.ForkTaskLearningsQuestions)
	sb.WriteString("\n\n")
	sb.WriteString("Categories: gotcha, decision, pattern, preference, explicit_teaching, strategic, unfinished, pivot_moment\n")
	sb.WriteString("- unfinished: open tasks, ideas, blocked work (task_type: task|idea|blocked|cap_idea)\n")
	sb.WriteString("- pivot_moment: direction change in the session — approach abandoned, new path taken\n")
	sb.WriteString("Status: new, confirmed, revised, invalidated\n")
	sb.WriteString("importance: 1-5 (1=nice-to-know, 3=useful, 5=critical — without this knowledge something breaks)\n")
	sb.WriteString("emotional_intensity: 0.0-1.0 (0=factual, 0.5=engaged, 1.0=frustration/breakthrough/strong emotion)\n\n")

	// Session flavor: one-line summary of what happened
	sb.WriteString("Additionally: Summarize the session in a concise one-liner (max 80 chars) as \"session_flavor\". ")
	sb.WriteString("Examples: \"3h cache debugging until API key swap fixes everything\", \"Refactoring proxy pipeline for per-thread state\".\n\n")

	// Task 2: Evaluate injected learnings (only if there are any)
	if len(ctx.InjectedIDs) > 0 {
		sb.WriteString(strs.ForkTaskEvaluate)
		sb.WriteString("\n")
		sb.WriteString(strs.ForkTaskEvaluateBody)
		sb.WriteString("\n")
		for id, source := range ctx.InjectedIDs {
			fmt.Fprintf(&sb, "- Learning #%d (source: %s)\n", id, source)
		}
		sb.WriteString("\n")
		sb.WriteString(strs.ForkTaskEvaluateImpact)
		sb.WriteString("\n\n")

		// Task 3: Contradictions (only if there are injected learnings to compare)
		sb.WriteString(strs.ForkTaskContradictions)
		sb.WriteString("\n")
		sb.WriteString(strs.ForkTaskContradictionsBody)
		sb.WriteString("\n\n")
	}

	sb.WriteString("**Cap suggestions** — when you observe a *cluster of 2+ tool-calls that together\n")
	sb.WriteString("accomplish a single intent that the user might want to script as a reusable cap*,\n")
	sb.WriteString("emit it as a learning with category=\"unfinished\" and task_type=\"cap_idea\".\n")
	sb.WriteString("Prefer stable, concise workflow names over session-specific details so repeated\n")
	sb.WriteString("sessions can deduplicate to the same intent. Examples worth emitting: a curl+jq+cap_store\n")
	sb.WriteString("polling loop, a multi-step git+sqlite verification sequence, a sequence of greps/reads that always run\n")
	sb.WriteString("together. NOT cap-worthy: one-off debug greps that converged on a fix; isolated\n")
	sb.WriteString("single tool-calls; clusters without a clear shared intent.\n\n")
	sb.WriteString("For cap suggestions emit `content` in this format:\n")
	sb.WriteString("  \"Cap: <intent (<=80 chars)>; <skeleton-hint (<=80 chars)>\"\n\n")
	sb.WriteString("Example:\n")
	sb.WriteString("  {\n")
	sb.WriteString("    \"category\": \"unfinished\",\n")
	sb.WriteString("    \"task_type\": \"cap_idea\",\n")
	sb.WriteString("    \"content\": \"Cap: Telegram getUpdates polling with offset persistence; yesmem telegram poll [--offset N]\"\n")
	sb.WriteString("  }\n\n")
	sb.WriteString("Be conservative. If unsure, skip — empty emission is a valid answer. Do NOT\n")
	sb.WriteString("emit cap_idea entries for one-off exploration; only for workflows you'd expect\n")
	sb.WriteString("to recur.\n\n")

	sb.WriteString("Response format — ONLY valid JSON, no other text:\n")
	sb.WriteString(`{"learnings": [{"content": "...", "category": "...", "task_type": "task|idea|blocked|cap_idea", "entities": ["..."], "actions": ["..."], "keywords": ["..."], "anticipated_queries": ["search phrase 1", "search phrase 2"], "context": "why relevant", "status": "new|confirmed|revised|invalidated", "importance": 3, "emotional_intensity": 0.2}], "evaluations": [{"learning_id": N, "verdict": "...", "reason": "...", "action": "...", "impact_score": 0.0}], "contradictions": [{"learning_a": N, "learning_b": M, "description": "..."}], "session_flavor": "summarize what happened in one line"}`)

	return sb.String()
}

// parseExtractionJSON parses the JSON response from the fork.
// DeepSeek without response_format sometimes outputs text before/after
// the JSON object or adds trailing braces. This parser finds the first
// balanced JSON object in the content (handles nested {}).
func parseExtractionJSON(content string) (*ExtractionResult, error) {
	start := strings.Index(content, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON found in response")
	}

	// Find the balanced closing brace using depth counting
	depth := 0
	inString := false
	escaped := false
	end := -1
	for i := start; i < len(content); i++ {
		b := content[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			continue
		}
		if b == '{' {
			depth++
		} else if b == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("unbalanced JSON in response")
	}

	jsonStr := content[start : end+1]

	var result ExtractionResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &result, nil
}

// NewExtractAndEvaluateConfig returns the ForkConfig for the combined extraction+evaluation fork.
func NewExtractAndEvaluateConfig(model string) ForkConfig {
	return ForkConfig{
		Name:      "extract_and_evaluate",
		Model:     model,
		MaxTokens: 3072,
		// APIFormat is auto-detected from the actual model at runtime in fireForkedAgents.
		// If the fork model is empty, it inherits from the main request — and the format
		// is inferred from the inherited model name (deepseek → openai, claude → anthropic).
		Gate: func(ctx ForkContext) bool {
			return ctx.CacheReadTokens > 0
		},
		Prompt: extractAndEvaluatePrompt,
	ParseResult: func(resp ForkResponse, s *Server) error {
		result, err := parseExtractionJSON(resp.Content)
		if err != nil {
			preview := resp.Content
			if len(preview) > 300 {
				preview = preview[:300]
			}
			s.logger.Printf("[fork] extract_and_evaluate: parse error (%v), response preview: %s", err, preview)
			return err
		}

			debugFork := s.cfg.ForkedAgentsDebug

			if len(result.Learnings) > 0 {
				learningsJSON, err := json.Marshal(result.Learnings)
				if err != nil {
					s.logger.Printf("fork extract: marshal learnings: %v", err)
				} else {
					s.queryDaemon("fork_extract_learnings", map[string]any{
						"learnings":       string(learningsJSON),
						"session_id":      resp.SessionID,
						"project":         resp.Project,
						"source_msg_from": resp.SourceMsgFrom,
						"source_msg_to":   resp.SourceMsgTo,
					})
				}
			}

			// Set session flavor on all learnings for this session
			if result.SessionFlavor != "" && resp.SessionID != "" {
				flavor := result.SessionFlavor
				s.queryDaemon("fork_set_session_flavor", map[string]any{
					"session_id": resp.SessionID,
					"flavor":     flavor,
				})
			}

			// Evaluate injected learnings + update impact scores
			for _, eval := range result.Evaluations {
				s.queryDaemon("fork_evaluate_learning", map[string]any{
					"learning_id":  eval.LearningID,
					"verdict":      eval.Verdict,
					"reason":       eval.Reason,
					"action":       eval.Action,
					"impact_score": eval.ImpactScore,
				})
			}

			// Resolve contradictions — fail_count++ both sides
			for _, c := range result.Contradictions {
				s.queryDaemon("fork_resolve_contradiction", map[string]any{
					"learning_a":  c.LearningA,
					"learning_b":  c.LearningB,
					"description": c.Description,
				})
			}

			if debugFork {
				s.logger.Printf("[fork] result: %d learnings, %d evaluations, %d contradictions", len(result.Learnings), len(result.Evaluations), len(result.Contradictions))
				for _, l := range result.Learnings {
					s.logger.Printf("[fork]   learning [%s/%s]: %s", l.Category, l.Status, truncateStr(l.Content, 120))
				}
				for _, e := range result.Evaluations {
					s.logger.Printf("[fork]   eval #%d: %s/%s (impact=%.2f)", e.LearningID, e.Verdict, e.Action, e.ImpactScore)
				}
				for _, c := range result.Contradictions {
					s.logger.Printf("[fork]   contradiction #%d vs #%d: %s", c.LearningA, c.LearningB, truncateStr(c.Description, 100))
				}
			}

			return nil
		},
	}
}
