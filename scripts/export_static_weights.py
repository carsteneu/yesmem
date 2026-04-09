#!/usr/bin/env python3
"""Export static-similarity-mrl-multilingual-v1 weights for Go embedding."""
import struct
import numpy as np
from sentence_transformers import SentenceTransformer

MODEL = "sentence-transformers/static-similarity-mrl-multilingual-v1"
DIM = 512
OUT_WEIGHTS = "internal/embedding/assets/static_weights_512d.bin"
OUT_TOKENIZER = "internal/embedding/assets/tokenizer.json"

print(f"Loading {MODEL}...")
model = SentenceTransformer(MODEL)
static_emb = model[0]
state = static_emb.state_dict()

# Embedding weights: (vocab_size, full_dim)
weights = state["embedding.weight"].detach().cpu().numpy().astype(np.float32)
vocab_size, full_dim = weights.shape
print(f"Vocab: {vocab_size}, Full dim: {full_dim}")

# Matryoshka truncation to target dim
if DIM < full_dim:
    weights = weights[:, :DIM]
    # Re-normalize each row
    norms = np.linalg.norm(weights, axis=1, keepdims=True)
    norms[norms == 0] = 1
    weights = weights / norms
    print(f"Truncated to {DIM}d, re-normalized")

# Convert to float16 for storage (~108MB instead of ~216MB)
# Go will convert back to float32 at load time — no quality loss for inference
weights_f16 = weights.astype(np.float16)
print(f"Weights: {weights_f16.nbytes / 1e6:.1f}MB ({weights_f16.shape}, float16)")

# Write binary: header + float16 weights
import os
os.makedirs("assets", exist_ok=True)

with open(OUT_WEIGHTS, "wb") as f:
    # Header: vocab_size (uint32) + dim (uint32) + dtype marker (uint32: 0=f32, 1=f16)
    f.write(struct.pack("<III", vocab_size, DIM, 1))
    f.write(weights_f16.tobytes())

print(f"Wrote {OUT_WEIGHTS} ({os.path.getsize(OUT_WEIGHTS) / 1e6:.1f}MB)")

# Export tokenizer
tokenizer = static_emb.tokenizer
tokenizer.save(OUT_TOKENIZER)
print(f"Wrote {OUT_TOKENIZER}")

# Verify round-trip
with open(OUT_WEIGHTS, "rb") as f:
    v, d, dtype = struct.unpack("<III", f.read(12))
    check = np.frombuffer(f.read(), dtype=np.float16).reshape(v, d)
    assert check.shape == weights_f16.shape
    assert np.array_equal(check, weights_f16)
print("Round-trip verified OK")

# Quick sanity: embed a test sentence with the model and compare dimensions
test = model.encode(["hello world"], normalize_embeddings=True)
print(f"Model output dim: {test.shape[1]} (we export {DIM}d)")
