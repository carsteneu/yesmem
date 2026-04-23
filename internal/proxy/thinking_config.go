package proxy

import "strings"

// adaptivePrefixes lists model prefixes that require thinking.type = "adaptive"
// instead of "enabled". These models use output_config.effort for thinking control.
var adaptivePrefixes = []string{
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-sonnet-4-6",
}

func modelNeedsAdaptiveThinking(model string) bool {
	for _, p := range adaptivePrefixes {
		if strings.HasPrefix(model, p) {
			return true
		}
	}
	return false
}

// NormalizeThinkingType converts thinking.type from "enabled" to "adaptive" for
// models that require it, and ensures output_config.effort is present.
// Returns true if the request was modified.
func NormalizeThinkingType(req map[string]any) bool {
	model, _ := req["model"].(string)
	if !modelNeedsAdaptiveThinking(model) {
		return false
	}

	th, _ := req["thinking"].(map[string]any)
	if th == nil {
		return false
	}
	if th["type"] != "enabled" {
		return false
	}

	th["type"] = "adaptive"
	delete(th, "budget_tokens")

	oc, _ := req["output_config"].(map[string]any)
	if oc == nil {
		oc = map[string]any{}
		req["output_config"] = oc
	}
	if _, exists := oc["effort"]; !exists {
		oc["effort"] = "high"
	}

	return true
}
