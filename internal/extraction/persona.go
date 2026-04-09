package extraction

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

// PersonaExtractionSystemPrompt is the system prompt for extracting persona signals.
const PersonaExtractionSystemPrompt = `Analyze this session and extract persona signals.

PRIORITY: UPDATE existing traits rather than creating new ones.

Look for:
1. CONFIRMATIONS of existing traits (increase confidence via updates)
2. CONTRADICTIONS to existing traits (decrease confidence / change value via updates)
3. NEW traits ONLY when no existing one fits (max 2 per session)

Signals can be:
- Explicit statements ("always do this in German")
- Implicit behavior (user skips explanations → expertise high)
- Reactions (user praises short answer → answer_length:concise)
- Corrections (user says "I know that" → explain_depth:minimal)

## FIXED TAXONOMY — use only these keys

### communication (How the user communicates)
| Key | Allowed values |
|-----|----------------|
| language | de, en, fr, ... |
| formality | informal_du, formal_sie, mixed |
| tone | direct, diplomatic, playful |
| answer_length | minimal, concise, detailed |
| explain_depth | minimal, decision_context, full |
| humor | none, occasional, frequent |
| emoji_usage | never, on_request, frequent |
| introspection | first_person, analytical, none |
| pushback | direct, gentle, none |
| status_updates | proactive, on_request, minimal |

### workflow (How the user works)
| Key | Allowed values |
|-----|----------------|
| autonomy | low, medium, high, full |
| debugging_style | systematic, exploratory, guided |
| commit_style | explicit_only, with_approval, auto |
| decision_velocity | cautious, moderate, fast |
| analysis_first | true, false |
| methodology | tdd, iterative, plan_first |
| delegation_style | detailed_spec, fire_and_forget |
| verification | explicit_data, trust_based |
| deploy_method | make_deploy, manual, ci_cd |
| automation | low, medium, high |

### expertise (What the user knows — dynamic keys allowed)
| Key | Allowed values |
|-----|----------------|
| {topic} | beginner, intermediate, advanced, expert |
Examples: go, php, docker, postgresql, javascript, css, nginx, ansible

### context (Technical environment — dynamic keys allowed)
| Key | Allowed values |
|-----|----------------|
| os | linux, macos, windows |
| shell | bash, zsh, fish |
| editor | vscode, vim, neovim, claude_code |
| primary_language | go, php, python, ... |
| {tool_or_version} | free text |

### boundaries (User's hard rules)
| Key | Allowed values |
|-----|----------------|
| auto_commit | never, with_approval, auto |
| force_push | never, with_approval, auto |
| design_approval | required, optional |
| legal_compliance | strict, conscious, relaxed |
| external_services | none, approved_only, any |
| disruptive_actions | never, with_approval |

### learning_style (How the user learns)
| Key | Allowed values |
|-----|----------------|
| examples | true, false |
| tradeoff_tables | true, false |
| visual | true, false |
| depth | surface, moderate, deep |
| format | prose, bullets, tables |

## RULES
- confidence_delta: max +0.1 per session, -0.2 on contradiction
- New traits start at confidence 0.5
- Only real signals, no speculation
- Empty arrays when nothing found
- NO dimension prefix in key (WRONG: "communication.directness", RIGHT: "tone")
- PREFER updates to existing traits over creating new ones
- Max 2 new traits per session — if more signals exist, update existing ones`

// PersonaExtractionResult holds the parsed LLM response for persona signals.
type PersonaExtractionResult struct {
	Updates   []PersonaTraitUpdate `json:"updates"`
	NewTraits []PersonaNewTrait    `json:"new_traits"`
}

// PersonaTraitUpdate represents a confidence change for an existing trait.
type PersonaTraitUpdate struct {
	Dimension       string  `json:"dimension"`
	TraitKey        string  `json:"trait_key"`
	TraitValue      string  `json:"trait_value"`
	ConfidenceDelta float64 `json:"confidence_delta"`
	Evidence        string  `json:"evidence"`
}

// PersonaNewTrait represents a newly discovered trait.
type PersonaNewTrait struct {
	Dimension  string  `json:"dimension"`
	TraitKey   string  `json:"trait_key"`
	TraitValue string  `json:"trait_value"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"`
}

// PersonaExtractionSchema returns the JSON schema for structured persona extraction.
func PersonaExtractionSchema() map[string]any {
	updateItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dimension":        map[string]any{"type": "string"},
			"trait_key":        map[string]any{"type": "string"},
			"trait_value":      map[string]any{"type": "string"},
			"confidence_delta": map[string]any{"type": "number"},
			"evidence":         map[string]any{"type": "string"},
		},
		"required":             []string{"dimension", "trait_key", "trait_value", "confidence_delta", "evidence"},
		"additionalProperties": false,
	}

	newTraitItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dimension":  map[string]any{"type": "string"},
			"trait_key":  map[string]any{"type": "string"},
			"trait_value": map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number"},
			"evidence":   map[string]any{"type": "string"},
		},
		"required":             []string{"dimension", "trait_key", "trait_value", "confidence", "evidence"},
		"additionalProperties": false,
	}

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"updates":    map[string]any{"type": "array", "items": updateItem},
			"new_traits": map[string]any{"type": "array", "items": newTraitItem},
		},
		"required":             []string{"updates", "new_traits"},
		"additionalProperties": false,
	}
}

// ParsePersonaExtractionResponse parses the JSON response from persona extraction.
func ParsePersonaExtractionResponse(response string) (*PersonaExtractionResult, error) {
	var result PersonaExtractionResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("parse persona extraction: %w", err)
	}
	return &result, nil
}

// BuildPersonaExtractionPrompt builds the user message for persona signal extraction.
// Only includes high-confidence traits (>=0.6) to keep context focused and avoid overwhelming the LLM.
func BuildPersonaExtractionPrompt(existingTraits []models.PersonaTrait) string {
	var sb strings.Builder
	sb.WriteString("Current persona traits (confidence >= 0.6 only):\n")

	var included int
	if len(existingTraits) == 0 {
		sb.WriteString("(none — first profiling)\n")
	} else {
		for _, t := range existingTraits {
			if t.Confidence < 0.6 {
				continue
			}
			sb.WriteString(fmt.Sprintf("- %s.%s = %q (confidence: %.1f)\n",
				t.Dimension, t.TraitKey, t.TraitValue, t.Confidence))
			included++
		}
		if included == 0 {
			sb.WriteString("(none with confidence >= 0.6)\n")
		}
	}
	sb.WriteString(fmt.Sprintf("\n(%d of %d traits shown)\n", included, len(existingTraits)))

	sb.WriteString("\nSession transcript:\n")
	return sb.String()
}
