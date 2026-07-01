package daemon

import (
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/proxy"
)

// defaultOpencodeSpawnModel was removed — opencode now behaves like all other
// backends: when model is empty, the daemon omits the --model flag entirely,
// letting opencode pick its default from its own configuration (opencode.json
// model key, auto-discovered models.json, or built-in first-entry default).
//
// Previously this was "zai/deepseek-v4-pro" (learning #76240, corrected to
// deepseek/deepseek-v4-pro in #80677). Removed because the daemon has no
// business hardcoding model strings — that's opencode's config responsibility.
//
// See Learning #80677 for the full history of model-string corrections.

// resolveSpawnModel resolves a user-supplied model argument for agent spawn.
//
//   - Empty model returns "" for ALL backends (lets the CLI/backend pick its
//     own default from its configuration — opencode.json model key, models.json
//     first-entry, or built-in defaults).
//   - Models containing "/" are returned verbatim (already provider-qualified).
//   - Bare model names are looked up in providerMap (lowercased). On hit,
//     "providerID/modelID" is returned. Coding variants are excluded from
//     providerMap upstream, so they are never auto-selected.
//   - Unknown bare names log a warning and pass through unchanged (best-effort,
//     no error — user may know something the auto-discovery doesn't).
//
// providerMap may be nil (auto-discovery unavailable); bare lookups then miss
// and fall through to the passthrough-with-warning path.
func resolveSpawnModel(model, backend string, providerMap map[string]string) string {
	if model == "" {
		return ""
	}
	if strings.Contains(model, "/") {
		return model
	}
	if providerMap != nil {
		if providerID, ok := providerMap[strings.ToLower(model)]; ok {
			return providerID + "/" + model
		}
	}
	log.Printf("[agents] WARNING: bare model %q not in auto-discovered provider map; passing through unchanged", model)
	return model
}

// resolvedAgentModel wraps resolveSpawnModel with lazy-cached auto-discovery.
// The provider map is built once on first call (sync.Once) and reused for all
// subsequent spawns. Daemon restart re-runs discovery.
func (h *Handler) resolvedAgentModel(model, backend string) string {
	h.modelProviderMapOnce.Do(func() {
		h.modelProviderMap = proxy.BuildModelProviderMap()
	})
	return resolveSpawnModel(model, backend, h.modelProviderMap)
}
