package embedding

import (
	"context"
	"math"
	"testing"
)

func TestSSEProviderEmbed(t *testing.T) {
	p, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
	if err != nil {
		t.Fatalf("NewSSEProvider: %v", err)
	}
	if p.Dimensions() != 512 {
		t.Fatalf("expected 512d, got %d", p.Dimensions())
	}
	if !p.Enabled() {
		t.Fatal("SSE provider should be enabled")
	}

	vecs, err := p.Embed(context.Background(), []string{"hello world", "guten morgen"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}

	for i, v := range vecs {
		if len(v) != 512 {
			t.Errorf("vector[%d] has %d dims, expected 512", i, len(v))
		}
		var norm float32
		for _, x := range v {
			norm += x * x
		}
		norm = float32(math.Sqrt(float64(norm)))
		if math.Abs(float64(norm-1.0)) > 0.001 {
			t.Errorf("vector[%d] not L2-normalized: norm=%.4f", i, norm)
		}
	}
}

func TestSSESemanticSimilarity(t *testing.T) {
	p, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
	if err != nil {
		t.Fatal(err)
	}

	vecs, err := p.Embed(context.Background(), []string{"hund", "katze", "automobile"})
	if err != nil {
		t.Fatal(err)
	}
	simDogCat := cosineSim32(vecs[0], vecs[1])
	simDogCar := cosineSim32(vecs[0], vecs[2])
	if simDogCat <= simDogCar {
		t.Errorf("expected hund/katze (%.3f) > hund/automobile (%.3f)", simDogCat, simDogCar)
	}
}

func TestSSEDyTEffect(t *testing.T) {
	sse, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
	if err != nil {
		t.Fatal(err)
	}
	// Same weights through StaticProvider (no DyT) — vectors must differ
	static, err := NewStaticProvider(sseWeightsData, tokenizerData)
	if err != nil {
		t.Fatal(err)
	}

	sseVecs, _ := sse.Embed(context.Background(), []string{"test embedding"})
	staticVecs, _ := static.Embed(context.Background(), []string{"test embedding"})

	// SSE and Static use different pipelines (DyT vs no DyT), so vectors must differ
	sim := cosineSim32(sseVecs[0], staticVecs[0])
	if sim > 0.999 {
		t.Errorf("SSE and Static vectors too similar (%.4f) — DyT should make a difference", sim)
	}
}

func TestSSEEmptyInput(t *testing.T) {
	p, err := NewSSEProvider(sseWeightsData, sseDyTData, tokenizerData)
	if err != nil {
		t.Fatal(err)
	}

	vecs, err := p.Embed(context.Background(), []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs[0]) != 512 {
		t.Fatalf("expected 512d zero vector, got %d dims", len(vecs[0]))
	}
	var sum float32
	for _, v := range vecs[0] {
		sum += v * v
	}
	if sum > 0 {
		t.Error("empty input should produce zero vector")
	}
}

func TestLoadDyT(t *testing.T) {
	_, _, _, err := loadDyT(sseDyTData, 512)
	if err != nil {
		t.Fatalf("loadDyT: %v", err)
	}

	// Wrong dimension should error
	_, _, _, err = loadDyT(sseDyTData, 384)
	if err == nil {
		t.Fatal("expected error for dimension mismatch")
	}

	// Truncated data should error
	_, _, _, err = loadDyT(sseDyTData[:10], 512)
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}
