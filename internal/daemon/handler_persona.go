package daemon

import (
	"fmt"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// dimensionMap maps well-known trait keys to their dimensions.
var dimensionMap = map[string]string{
	// communication
	"language":          "communication",
	"tone":              "communication",
	"formality":         "communication",
	"humor":             "communication",
	"emoji_usage":       "communication",
	"answer_length":     "communication",
	"format_preference": "communication",

	// workflow
	"autonomy":              "workflow",
	"commit_style":          "workflow",
	"tdd":                   "workflow",
	"review_style":          "workflow",
	"methodology":           "workflow",
	"debugging_style":       "workflow",
	"decision_style":        "workflow",
	"automation_preference": "workflow",
	"proactivity":           "workflow",

	// context
	"os":             "context",
	"shell":          "context",
	"editor":         "context",
	"go_version":     "context",
	"php_version":    "context",
	"node_version":   "context",
	"default_branch": "context",
	"platform":       "context",

	// boundaries
	"never_auto_commit":     "boundaries",
	"no_emoji_unless_asked": "boundaries",
	"no_force_push":         "boundaries",
	"no_overselling":        "boundaries",

	// learning_style
	"prefers_examples":      "learning_style",
	"wants_tradeoff_tables": "learning_style",
	"wants_options":         "learning_style",
	"visual_thinker":        "learning_style",
}

// prefixDimensionMap maps trait key prefixes to dimensions.
var prefixDimensionMap = map[string]string{
	"expertise.": "expertise",
	"context.":   "context",
	"boundary.":  "boundaries",
	"learning.":  "learning_style",
}

// autoDetectDimension guesses the dimension from a trait key.
// Returns "general" if unknown.
func autoDetectDimension(traitKey string) string {
	if dim, ok := dimensionMap[traitKey]; ok {
		return dim
	}
	// Check for known prefixes
	for prefix, dim := range prefixDimensionMap {
		if len(traitKey) > len(prefix) && traitKey[:len(prefix)] == prefix {
			return dim
		}
	}
	return "general"
}

func (h *Handler) handleSetPersona(params map[string]any) Response {
	traitKey, _ := params["trait_key"].(string)
	value, _ := params["value"].(string)
	dimension, _ := params["dimension"].(string)

	if traitKey == "" {
		return errorResponse("trait_key is required")
	}
	if value == "" {
		return errorResponse("value is required")
	}

	if dimension == "" {
		dimension = autoDetectDimension(traitKey)
	}

	err := h.store.UpsertPersonaTrait(&models.PersonaTrait{
		UserID:        "default",
		Dimension:     dimension,
		TraitKey:      traitKey,
		TraitValue:    value,
		Confidence:    1.0,
		Source:        "user_override",
		EvidenceCount: 1,
	})
	if err != nil {
		return errorResponse(fmt.Sprintf("set persona trait: %v", err))
	}

	return jsonResponse(map[string]any{
		"message":   fmt.Sprintf("Persona-Trait %s.%s = %q gesetzt (user_override, confidence 1.0)", dimension, traitKey, value),
		"dimension": dimension,
		"trait_key": traitKey,
		"value":     value,
	})
}

func (h *Handler) handleGetPersona() Response {
	traits, err := h.store.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		return errorResponse(fmt.Sprintf("get persona traits: %v", err))
	}

	directive, _ := h.store.GetPersonaDirective("default")

	directiveText := ""
	lastUpdated := ""
	if directive != nil {
		directiveText = directive.Directive
		lastUpdated = directive.GeneratedAt.Format(time.RFC3339)
	}

	return jsonResponse(map[string]any{
		"traits":       traits,
		"directive":    directiveText,
		"last_updated": lastUpdated,
	})
}
