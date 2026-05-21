package models

import "strings"

// PromptProfile names the prompt-generation profile for a specific agent or wire format.
// Profiles are flat specializations of a shared generic baseline:
//
//	generic  (OpenAI-shaped baseline, neutral wording — default)
//	  ├── codex    (OpenAI + Codex CLI quirks)
//	  ├── claude   (Anthropic-specific: cache_control, Messages API, CLAUDE.md)
//	  └── opencode (OpenAI-shaped + opencode plugin system)
//
// All profiles inherit from generic but NOT from each other (no sibling inheritance).
// SourceAgent constants (SourceAgentClaude etc.) denote provenance of stored data.
// PromptProfile controls the output side: prompt templates, injection rules, wire format.
type PromptProfile string

const (
	ProfileGeneric  PromptProfile = "generic"
	ProfileClaude   PromptProfile = "claude"
	ProfileCodex    PromptProfile = "codex"
	ProfileOpencode PromptProfile = "opencode"
)

// SourceAgentToProfile maps a source agent label to its default prompt profile.
// Falls back to generic for unknown agents.
func SourceAgentToProfile(sourceAgent string) PromptProfile {
	switch NormalizeSourceAgent(sourceAgent) {
	case SourceAgentClaude:
		return ProfileClaude
	case SourceAgentCodex:
		return ProfileCodex
	case SourceAgentOpencode:
		return ProfileOpencode
	default:
		return ProfileGeneric
	}
}

// InheritsFrom returns true if this profile shares the given base.
// Currently implements two levels: identity and generic.
// All profiles inherit from ProfileGeneric; there is no sibling inheritance
// (claude does NOT inherit from codex, opencode does NOT inherit from claude, etc.).
func (p PromptProfile) InheritsFrom(base PromptProfile) bool {
	if p == base {
		return true
	}
	if base == ProfileGeneric {
		return true // everything inherits from generic
	}
	return false
}

// IsClaude returns true if the profile requires Anthropic-specific wire format.
func (p PromptProfile) IsClaude() bool {
	return p == ProfileClaude
}

// IsOpenAI returns true if the profile uses OpenAI-compatible wire format.
func (p PromptProfile) IsOpenAI() bool {
	return p == ProfileGeneric || p == ProfileCodex || p == ProfileOpencode
}

// IsOpencode returns true if the profile uses the opencode plugin system.
func (p PromptProfile) IsOpencode() bool {
	return p == ProfileOpencode
}

// NormalizeProfile canonicalizes a profile string, falling back to generic.
func NormalizeProfile(s string) PromptProfile {
	switch PromptProfile(strings.TrimSpace(strings.ToLower(s))) {
	case ProfileClaude:
		return ProfileClaude
	case ProfileCodex:
		return ProfileCodex
	case ProfileOpencode:
		return ProfileOpencode
	default:
		return ProfileGeneric
	}
}
