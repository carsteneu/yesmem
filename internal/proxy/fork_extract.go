package proxy

import (
	"encoding/json"
	"fmt"
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
	LearningID  int64   `json:"learning_id"`
	Verdict     string  `json:"verdict"`
	Reason      string  `json:"reason"`
	Action      string  `json:"action"`
	ImpactScore float64 `json:"impact_score"`
}

// ContradictionDetected represents a conflict between two injected learnings.
type ContradictionDetected struct {
	LearningA   int64  `json:"learning_a"`
	LearningB   int64  `json:"learning_b"`
	Description string `json:"description"`
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
	sb.WriteString("- unfinished: open tasks, ideas, blocked work (task_type: task|idea|blocked)\n")
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

	sb.WriteString("Antwortformat — NUR valides JSON, kein anderer Text:\n")
	sb.WriteString(`{"learnings": [{"content": "...", "category": "...", "entities": ["..."], "actions": ["..."], "keywords": ["..."], "anticipated_queries": ["Suchphrase 1", "Suchphrase 2"], "context": "Warum relevant", "status": "new|confirmed|revised|invalidated", "importance": 3, "emotional_intensity": 0.2}], "evaluations": [{"learning_id": N, "verdict": "...", "reason": "...", "action": "...", "impact_score": 0.0}], "contradictions": [{"learning_a": N, "learning_b": M, "description": "..."}], "session_flavor": "kurzer Satz"}`)

	return sb.String()
}

// parseExtractionJSON parses the JSON response from the fork.
func parseExtractionJSON(content string) (*ExtractionResult, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("no JSON found in response")
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
		Gate: func(ctx ForkContext) bool {
			return ctx.CacheReadTokens > 0
		},
		Prompt: extractAndEvaluatePrompt,
		ParseResult: func(resp ForkResponse, s *Server) error {
			result, err := parseExtractionJSON(resp.Content)
			if err != nil {
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
