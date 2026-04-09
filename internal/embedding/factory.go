package embedding

import (
	"fmt"
	"sync"
)

// EmbeddingConfig holds all embedding-related configuration.
type EmbeddingConfig struct {
	Provider string       `yaml:"provider"` // "sse" (default), "static", "none"
	Search   SearchConfig `yaml:"search"`
}

// SearchConfig configures the vector search algorithm.
type SearchConfig struct {
	Method       string    `yaml:"method"`        // "" (auto) | "brute_force" | "ivf"
	IVFThreshold int       `yaml:"ivf_threshold"` // auto-switch threshold (default: 5000)
	IVF          IVFConfig `yaml:"ivf,omitempty"`
}

// IVFConfig configures IVF index parameters.
type IVFConfig struct {
	K      int `yaml:"k"`      // number of clusters (0 = auto sqrt(n))
	NProbe int `yaml:"nprobe"` // clusters to probe per search (default: 5)
}

// DefaultEmbeddingConfig returns sensible defaults.
func DefaultEmbeddingConfig() EmbeddingConfig {
	return EmbeddingConfig{
		Provider: "sse",
		Search: SearchConfig{
			IVFThreshold: 50000,
			IVF: IVFConfig{
				NProbe: 15,
			},
		},
	}
}

// sseProvider singleton — SSE weights are ~108MB, avoid loading twice.
var (
	sseOnce     sync.Once
	sseInstance Provider
	sseInitErr  error
)

// resetSSESingleton clears the singleton (for testing only).
func resetSSESingleton() {
	sseOnce = sync.Once{}
	sseInstance = nil
	sseInitErr = nil
}

// ReleaseWeightData frees the raw embedded weight bytes after the singleton
// provider has been created. The provider holds parsed []float32 weights,
// so the raw []byte data (~108MB) is no longer needed.
func ReleaseWeightData() {
	sseWeightsData = nil
	sseWeightsPart0 = nil
	sseWeightsPart1 = nil
	sseWeightsPart2 = nil
}

// NewProviderFromConfig creates an embedding provider based on config.
func NewProviderFromConfig(cfg EmbeddingConfig) (Provider, error) {
	switch cfg.Provider {
	case "", "none":
		return NewNoneProvider(), nil
	case "sse":
		sseOnce.Do(func() {
			prov, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
			if err != nil {
				sseInitErr = err
				return
			}
			sseInstance = prov
		})
		if sseInitErr != nil {
			return nil, sseInitErr
		}
		return sseInstance, nil
	case "static":
		return nil, fmt.Errorf("static provider: model not bundled (removed in favor of sse)")
	default:
		return nil, fmt.Errorf("unknown embedding provider: %q (supported: sse, none)", cfg.Provider)
	}
}
