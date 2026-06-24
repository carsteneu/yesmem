package daemon

import (
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/proxy"
)

// defaultOpencodeSpawnModel is the model used when a spawn request omits the
// model argument for the opencode backend. Per learning #76240 this is the
// yesloop default. Other backends keep their CLI default (no --model flag).
const defaultOpencodeSpawnModel = "zai/deepseek-v4-pro"

// resolveSpawnModel resolves a user-supplied model argument for agent spawn.
//
//   - Empty model returns the backend default: defaultOpencodeSpawnModel for
//     "opencode", empty string for all other backends (lets the CLI pick).
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
		if backend == "opencode" {
			return defaultOpencodeSpawnModel
		}
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
