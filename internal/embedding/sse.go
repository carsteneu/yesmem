package embedding

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"math"
)

//go:embed assets/sse_weights_part0.bin
var sseWeightsPart0 []byte

//go:embed assets/sse_weights_part1.bin
var sseWeightsPart1 []byte

//go:embed assets/sse_weights_part2.bin
var sseWeightsPart2 []byte

// sseWeightsData is assembled from split parts at init time (each part <100MB for GitHub).
var sseWeightsData []byte

func init() {
	sseWeightsData = make([]byte, 0, len(sseWeightsPart0)+len(sseWeightsPart1)+len(sseWeightsPart2))
	sseWeightsData = append(sseWeightsData, sseWeightsPart0...)
	sseWeightsData = append(sseWeightsData, sseWeightsPart1...)
	sseWeightsData = append(sseWeightsData, sseWeightsPart2...)
}

//go:embed assets/sse_dyt_512d.bin
var sseDyTData []byte

//go:embed assets/tokenizer.json
var tokenizerData []byte

// SSEProvider implements Provider using SSE (Stable Static Embeddings)
// with Separable DyT normalization. 4-step pipeline:
// Lookup → Mean Pool → DyT → L2 Normalize
type SSEProvider struct {
	weights   []float32
	dytAlpha  []float32
	dytBeta   []float32
	dytBias   []float32
	dim       int
	vocabSize int
	tokenizer *WordPieceTokenizer
}

// NewSSEProvider creates an SSE provider from weight, DyT, and tokenizer data.
func NewSSEProvider(weightsData, dytData, tokenizerData []byte) (*SSEProvider, error) {
	vocabSize, dim, dtype, payload, err := parseWeightsHeader(weightsData)
	if err != nil {
		return nil, err
	}

	weights, err := loadWeights(payload, vocabSize, dim, dtype)
	if err != nil {
		return nil, fmt.Errorf("load weights: %w", err)
	}

	dytAlpha, dytBeta, dytBias, err := loadDyT(dytData, dim)
	if err != nil {
		return nil, fmt.Errorf("load dyt: %w", err)
	}

	tokenizer, err := LoadWordPieceTokenizer(tokenizerData)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}

	return &SSEProvider{
		weights:   weights,
		dytAlpha:  dytAlpha,
		dytBeta:   dytBeta,
		dytBias:   dytBias,
		dim:       dim,
		vocabSize: vocabSize,
		tokenizer: tokenizer,
	}, nil
}

func (p *SSEProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		results[i] = p.embed(text)
	}
	return results, nil
}

func (p *SSEProvider) embed(text string) []float32 {
	tokens := p.tokenizer.Tokenize(text)
	if len(tokens) == 0 {
		return make([]float32, p.dim)
	}

	embedding := make([]float32, p.dim)

	// Step 1+2: EmbeddingBag (Lookup + Sum + Mean)
	for _, tokenID := range tokens {
		if tokenID < 0 || tokenID >= p.vocabSize {
			continue
		}
		offset := tokenID * p.dim
		for i := 0; i < p.dim; i++ {
			embedding[i] += p.weights[offset+i]
		}
	}
	scale := 1.0 / float32(len(tokens))
	for i := range embedding {
		embedding[i] *= scale
	}

	// Step 3: Separable DyT normalization
	for i := range embedding {
		x := p.dytAlpha[i]*embedding[i] + p.dytBias[i]
		embedding[i] = p.dytBeta[i] * float32(math.Tanh(float64(x)))
	}

	// Step 4: L2 Normalize
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

func (p *SSEProvider) Dimensions() int { return p.dim }
func (p *SSEProvider) Enabled() bool   { return true }
func (p *SSEProvider) Close() error    { return nil }

var _ io.Closer = (*SSEProvider)(nil)
