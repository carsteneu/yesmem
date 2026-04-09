package embedding

import (
	"encoding/binary"
	"fmt"
	"math"
)

// parseWeightsHeader reads the 12-byte header from a weights binary file.
// Header: vocab_size (uint32 LE) + dim (uint32 LE) + dtype (uint32 LE: 0=f32, 1=f16)
func parseWeightsHeader(data []byte) (vocabSize, dim int, dtype uint32, payload []byte, err error) {
	if len(data) < 12 {
		return 0, 0, 0, nil, fmt.Errorf("weights data too short: %d bytes", len(data))
	}
	vocabSize = int(binary.LittleEndian.Uint32(data[0:4]))
	dim = int(binary.LittleEndian.Uint32(data[4:8]))
	dtype = binary.LittleEndian.Uint32(data[8:12])
	return vocabSize, dim, dtype, data[12:], nil
}

// loadWeights decodes a float32 or float16 payload into []float32.
func loadWeights(payload []byte, vocabSize, dim int, dtype uint32) ([]float32, error) {
	n := vocabSize * dim
	switch dtype {
	case 0: // float32
		expected := n * 4
		if len(payload) < expected {
			return nil, fmt.Errorf("weights truncated: need %d bytes, got %d", expected, len(payload))
		}
		weights := make([]float32, n)
		for i := range weights {
			weights[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[i*4:]))
		}
		return weights, nil
	case 1: // float16 → float32
		expected := n * 2
		if len(payload) < expected {
			return nil, fmt.Errorf("weights truncated: need %d bytes, got %d", expected, len(payload))
		}
		weights := make([]float32, n)
		for i := range weights {
			weights[i] = float16to32(binary.LittleEndian.Uint16(payload[i*2:]))
		}
		return weights, nil
	default:
		return nil, fmt.Errorf("unknown dtype: %d", dtype)
	}
}

// float16to32 converts an IEEE 754 half-precision float to float32.
func float16to32(h uint16) float32 {
	sign := uint32(h>>15) & 1
	exp := uint32(h>>10) & 0x1F
	frac := uint32(h) & 0x3FF

	switch {
	case exp == 0:
		if frac == 0 {
			return math.Float32frombits(sign << 31) // ±0
		}
		// Denormalized: convert to normalized float32
		exp = 127 - 15 + 1
		for frac&0x400 == 0 {
			frac <<= 1
			exp--
		}
		frac &= 0x3FF
		return math.Float32frombits(sign<<31 | exp<<23 | frac<<13)
	case exp == 0x1F:
		if frac == 0 {
			return math.Float32frombits(sign<<31 | 0x7F800000) // ±Inf
		}
		return math.Float32frombits(sign<<31 | 0x7FC00000) // NaN
	default:
		return math.Float32frombits(sign<<31 | (exp+127-15)<<23 | frac<<13)
	}
}

// loadDyT parses a DyT binary file: [dim:uint32 LE][alpha:dim*f32][beta:dim*f32][bias:dim*f32]
func loadDyT(data []byte, expectedDim int) (alpha, beta, bias []float32, err error) {
	if len(data) < 4 {
		return nil, nil, nil, fmt.Errorf("dyt data too short")
	}
	dim := int(binary.LittleEndian.Uint32(data[0:4]))
	if dim != expectedDim {
		return nil, nil, nil, fmt.Errorf("dyt dim mismatch: got %d, expected %d", dim, expectedDim)
	}
	expected := 4 + 3*dim*4
	if len(data) < expected {
		return nil, nil, nil, fmt.Errorf("dyt data truncated: need %d bytes, got %d", expected, len(data))
	}
	payload := data[4:]
	alpha = make([]float32, dim)
	beta = make([]float32, dim)
	bias = make([]float32, dim)
	for i := 0; i < dim; i++ {
		alpha[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[i*4:]))
		beta[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[(dim+i)*4:]))
		bias[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[(2*dim+i)*4:]))
	}
	return alpha, beta, bias, nil
}
