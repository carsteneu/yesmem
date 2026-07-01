package daemon

import (
	"testing"
)

func TestResolveSpawnModelEmptyDefault(t *testing.T) {
	got := resolveSpawnModel("", "opencode", nil)
	if got != "" {
		t.Fatalf("empty + opencode = %q, want empty (let opencode CLI pick default)", got)
	}
}

func TestResolveSpawnModelEmptyNonOpencode(t *testing.T) {
	got := resolveSpawnModel("", "claude", nil)
	if got != "" {
		t.Fatalf("empty + claude = %q, want empty (let CLI pick default)", got)
	}
	got = resolveSpawnModel("", "codex", nil)
	if got != "" {
		t.Fatalf("empty + codex = %q, want empty", got)
	}
}

func TestResolveSpawnModelWithSlashPassthrough(t *testing.T) {
	providerMap := map[string]string{"glm-5.2": "zai"}
	for _, in := range []string{"zai/glm-5.2", "zai-coding-plan/glm-5.2", "anthropic/claude-sonnet-4-6"} {
		got := resolveSpawnModel(in, "opencode", providerMap)
		if got != in {
			t.Fatalf("slash model %q returned %q, want passthrough", in, got)
		}
	}
}

func TestResolveSpawnModelBareKnownResolvesToProvider(t *testing.T) {
	providerMap := map[string]string{
		"glm-5.2":          "zai",
		"deepseek-v4-pro":  "zai",
	}
	cases := []struct {
		in, want string
	}{
		{"glm-5.2", "zai/glm-5.2"},
		{"deepseek-v4-pro", "zai/deepseek-v4-pro"},
		// Lookup is case-insensitive, but the user's original spelling is preserved
		// in the output (consistent with slash-passthrough and unknown-passthrough).
		{"GLM-5.2", "zai/GLM-5.2"},
	}
	for _, c := range cases {
		got := resolveSpawnModel(c.in, "opencode", providerMap)
		if got != c.want {
			t.Fatalf("resolveSpawnModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveSpawnModelBareUnknownPassthroughWithWarning(t *testing.T) {
	providerMap := map[string]string{"glm-5.2": "zai"}
	got := resolveSpawnModel("totally-unknown-model", "opencode", providerMap)
	if got != "totally-unknown-model" {
		t.Fatalf("unknown bare model = %q, want passthrough unchanged", got)
	}
}

func TestResolveSpawnModelNilMapPassthrough(t *testing.T) {
	// Auto-discovery unavailable — nil map. Bare name should pass through, not panic.
	got := resolveSpawnModel("glm-5.2", "opencode", nil)
	if got != "glm-5.2" {
		t.Fatalf("nil map + bare name = %q, want passthrough", got)
	}
}

func TestResolveSpawnModelCodingVariantIncludedWhenOnlyProvider(t *testing.T) {
	// Real-world case: zai-coding-plan is the only provider that carries glm-5.2.
	// The map should contain it (no coding-filter upstream), and bare glm-5.2
	// resolves to zai-coding-plan.
	providerMap := map[string]string{
		"glm-5.2": "zai-coding-plan",
	}
	got := resolveSpawnModel("glm-5.2", "opencode", providerMap)
	if got != "zai-coding-plan/glm-5.2" {
		t.Fatalf("bare glm-5.2 = %q, want zai-coding-plan/glm-5.2", got)
	}
	// Explicit coding-variant request also honored (slash passthrough)
	got = resolveSpawnModel("zai-coding-plan/glm-5.2", "opencode", providerMap)
	if got != "zai-coding-plan/glm-5.2" {
		t.Fatalf("explicit coding variant = %q, want passthrough", got)
	}
}
