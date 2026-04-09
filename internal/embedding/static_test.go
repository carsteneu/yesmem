package embedding

import (
	"context"
	"math"
	"testing"
)

func TestStaticProviderEmbed(t *testing.T) {
	// StaticProvider code still works — test with SSE weights (same format, no DyT step)
	p, err := NewStaticProvider(sseWeightsData, tokenizerData)
	if err != nil {
		t.Fatalf("NewStaticProvider: %v", err)
	}
	if p.Dimensions() != 512 {
		t.Fatalf("expected 512d, got %d", p.Dimensions())
	}

	vecs, err := p.Embed(context.Background(), []string{"hello world", "guten morgen"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}

	// Both vectors must be unit length (L2-normalized)
	for i, v := range vecs {
		var norm float32
		for _, x := range v {
			norm += x * x
		}
		norm = float32(math.Sqrt(float64(norm)))
		if math.Abs(float64(norm-1.0)) > 0.001 {
			t.Errorf("vector[%d] not normalized: norm=%.4f", i, norm)
		}
	}

	// Similar texts should have higher cosine similarity than dissimilar ones
	de, err := p.Embed(context.Background(), []string{"hund", "katze", "automobile"})
	if err != nil {
		t.Fatal(err)
	}
	simDogCat := cosineSim32(de[0], de[1])
	simDogCar := cosineSim32(de[0], de[2])
	if simDogCat <= simDogCar {
		t.Errorf("expected hund/katze (%.3f) > hund/automobile (%.3f)", simDogCat, simDogCar)
	}
}

func TestWordPieceTokenizer(t *testing.T) {
	tok, err := LoadWordPieceTokenizer(tokenizerData)
	if err != nil {
		t.Fatalf("LoadWordPieceTokenizer: %v", err)
	}

	tokens := tok.Tokenize("hello world")
	if len(tokens) == 0 {
		t.Error("expected tokens for 'hello world'")
	}

	// German should also tokenize
	toksDe := tok.Tokenize("Guten Morgen")
	if len(toksDe) == 0 {
		t.Error("expected tokens for 'Guten Morgen'")
	}
}

func cosineSim32(a, b []float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot // already normalized → cosine = dot product
}
