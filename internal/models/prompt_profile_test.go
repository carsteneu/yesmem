package models

import "testing"

func TestSourceAgentToProfile(t *testing.T) {
	tests := []struct {
		agent string
		want  PromptProfile
	}{
		{"claude", ProfileClaude},
		{"codex", ProfileCodex},
		{"opencode", ProfileOpencode},
		{"open-code", ProfileOpencode},
		{"unknown", ProfileGeneric},
		{"", ProfileClaude}, // legacy default
	}
	for _, tt := range tests {
		got := SourceAgentToProfile(tt.agent)
		if got != tt.want {
			t.Errorf("SourceAgentToProfile(%q) = %q, want %q", tt.agent, got, tt.want)
		}
	}
}

func TestPromptProfile_InheritsFrom(t *testing.T) {
	if !ProfileClaude.InheritsFrom(ProfileGeneric) {
		t.Error("claude should inherit from generic")
	}
	if !ProfileCodex.InheritsFrom(ProfileGeneric) {
		t.Error("codex should inherit from generic")
	}
	if !ProfileOpencode.InheritsFrom(ProfileGeneric) {
		t.Error("opencode should inherit from generic")
	}
	if ProfileClaude.InheritsFrom(ProfileCodex) {
		t.Error("claude should not inherit from codex")
	}
}

func TestPromptProfile_IsClaude(t *testing.T) {
	if !ProfileClaude.IsClaude() {
		t.Error("claude should be IsClaude")
	}
	if ProfileCodex.IsClaude() {
		t.Error("codex should not be IsClaude")
	}
	if ProfileOpencode.IsClaude() {
		t.Error("opencode should not be IsClaude")
	}
}

func TestPromptProfile_IsOpenAI(t *testing.T) {
	if !ProfileGeneric.IsOpenAI() {
		t.Error("generic should be IsOpenAI")
	}
	if !ProfileCodex.IsOpenAI() {
		t.Error("codex should be IsOpenAI")
	}
	if !ProfileOpencode.IsOpenAI() {
		t.Error("opencode should be IsOpenAI")
	}
	if ProfileClaude.IsOpenAI() {
		t.Error("claude should not be IsOpenAI")
	}
}

func TestPromptProfile_IsOpencode(t *testing.T) {
	if !ProfileOpencode.IsOpencode() {
		t.Error("opencode should be IsOpencode")
	}
	if ProfileClaude.IsOpencode() {
		t.Error("claude should not be IsOpencode")
	}
	if ProfileCodex.IsOpencode() {
		t.Error("codex should not be IsOpencode")
	}
}

func TestNormalizeProfile(t *testing.T) {
	tests := map[string]PromptProfile{
		"claude":   ProfileClaude,
		"CLAUDE":   ProfileClaude,
		"codex":    ProfileCodex,
		"opencode": ProfileOpencode,
		"openCode": ProfileOpencode,
		"garbage":  ProfileGeneric,
		"":         ProfileGeneric,
	}
	for in, want := range tests {
		got := NormalizeProfile(in)
		if got != want {
			t.Errorf("NormalizeProfile(%q) = %q, want %q", in, got, want)
		}
	}
}
