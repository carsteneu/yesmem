package embedding

import (
	"context"
	"fmt"
	"io"
	"math"
)

// StaticProvider implements the Provider interface using static embeddings.
// Pure Go, no ONNX — array lookup + mean pooling + L2 normalization.
type StaticProvider struct {
	weights   []float32 // flat: weights[token_id * dim + i]
	dim       int
	vocabSize int
	tokenizer *WordPieceTokenizer
}

// NewStaticProvider creates a provider from pre-exported weight and tokenizer data.
// weightsData: binary format (header + float16/float32 weights), tokenizerData: tokenizer.json
func NewStaticProvider(weightsData []byte, tokenizerData []byte) (*StaticProvider, error) {
	vocabSize, dim, dtype, payload, err := parseWeightsHeader(weightsData)
	if err != nil {
		return nil, err
	}

	weights, err := loadWeights(payload, vocabSize, dim, dtype)
	if err != nil {
		return nil, err
	}

	tokenizer, err := LoadWordPieceTokenizer(tokenizerData)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}

	return &StaticProvider{
		weights:   weights,
		dim:       dim,
		vocabSize: vocabSize,
		tokenizer: tokenizer,
	}, nil
}

// Embed converts texts to embedding vectors.
func (p *StaticProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		results[i] = p.embed(text)
	}
	return results, nil
}

func (p *StaticProvider) embed(text string) []float32 {
	tokens := p.tokenizer.Tokenize(text)
	if len(tokens) == 0 {
		return make([]float32, p.dim)
	}

	embedding := make([]float32, p.dim)

	// 1. EmbeddingBag: Lookup + Sum
	for _, tokenID := range tokens {
		if tokenID < 0 || tokenID >= p.vocabSize {
			continue
		}
		offset := tokenID * p.dim
		for i := 0; i < p.dim; i++ {
			embedding[i] += p.weights[offset+i]
		}
	}

	// 2. Mean Pooling
	scale := 1.0 / float32(len(tokens))
	for i := range embedding {
		embedding[i] *= scale
	}

	// 3. L2 Normalization (for cosine similarity)
	var norm float32
	for _, v := range embedding {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 0 {
		invNorm := 1.0 / norm
		for i := range embedding {
			embedding[i] *= invNorm
		}
	}

	return embedding
}

func (p *StaticProvider) Dimensions() int { return p.dim }
func (p *StaticProvider) Enabled() bool   { return true }
func (p *StaticProvider) Close() error    { return nil }

// Ensure StaticProvider satisfies io.Closer (used in some contexts)
var _ io.Closer = (*StaticProvider)(nil)
