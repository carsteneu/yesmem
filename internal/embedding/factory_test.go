package embedding

import (
	"context"
	"testing"
)

func TestNewProviderFromConfigNone(t *testing.T) {
	p, err := NewProviderFromConfig(EmbeddingConfig{
		Provider: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.Enabled() {
		t.Fatal("none provider should not be enabled")
	}
	if p.Dimensions() != 0 {
		t.Fatalf("expected 0 dims, got %d", p.Dimensions())
	}
}

func TestNewProviderFromConfigDefault(t *testing.T) {
	// Empty config should default to "none"
	p, err := NewProviderFromConfig(EmbeddingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if p.Enabled() {
		t.Fatal("default provider should not be enabled")
	}
}

func TestNewProviderFromConfigUnknown(t *testing.T) {
	_, err := NewProviderFromConfig(EmbeddingConfig{
		Provider: "quantum",
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestEmbeddingConfigDefaults(t *testing.T) {
	cfg := DefaultEmbeddingConfig()
	if cfg.Provider != "sse" {
		t.Fatalf("expected default provider 'sse', got %q", cfg.Provider)
	}
}

func TestSSEProviderSingleton(t *testing.T) {
	resetSSESingleton() // clean state for test

	cfg := EmbeddingConfig{Provider: "sse"}

	p1, err := NewProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	p2, err := NewProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Both must be the same underlying provider
	if p1 != p2 {
		t.Fatal("expected same instance from two NewProviderFromConfig calls")
	}

	// Close on one must not break the other
	p1.Close()

	vecs, err := p2.Embed(context.Background(), []string{"singleton test"})
	if err != nil {
		t.Fatalf("Embed after Close on sibling: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 512 {
		t.Fatalf("expected 1x512d vector, got %dx%d", len(vecs), len(vecs[0]))
	}
}

func TestReleaseWeightData(t *testing.T) {
	resetSSESingleton()

	// Save originals for restore after test
	origData := sseWeightsData
	origP0 := sseWeightsPart0
	origP1 := sseWeightsPart1
	origP2 := sseWeightsPart2
	defer func() {
		sseWeightsData = origData
		sseWeightsPart0 = origP0
		sseWeightsPart1 = origP1
		sseWeightsPart2 = origP2
	}()

	// Create singleton first
	cfg := EmbeddingConfig{Provider: "sse"}
	p, err := NewProviderFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Release raw weight data
	ReleaseWeightData()

	// Raw data must be nil
	if sseWeightsData != nil {
		t.Fatal("sseWeightsData should be nil after ReleaseWeightData")
	}

	// Provider must still work (it holds parsed []float32, not raw bytes)
	vecs, err := p.Embed(context.Background(), []string{"still works after release"})
	if err != nil {
		t.Fatalf("Embed after ReleaseWeightData: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 512 {
		t.Fatalf("expected 1x512d, got %dx%d", len(vecs), len(vecs[0]))
	}
}

func TestSSESingletonIndependentOfDirect(t *testing.T) {
	resetSSESingleton()

	// Direct NewSSEProvider must still work (separate from singleton)
	direct, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
	if err != nil {
		t.Fatalf("direct: %v", err)
	}

	cfg := EmbeddingConfig{Provider: "sse"}
	factory, err := NewProviderFromConfig(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	// Direct and factory must be different instances
	if direct == factory {
		t.Fatal("direct NewSSEProvider should not return singleton")
	}
}
