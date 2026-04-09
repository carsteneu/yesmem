package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/carsteneu/yesmem/internal/clustering"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func runTraitCleanup() {
	dryRun := false
	threshold := 0.85
	for i, arg := range os.Args[2:] {
		switch arg {
		case "--dry-run", "-n":
			dryRun = true
		case "--threshold", "-t":
			if i+1 < len(os.Args[2:]) {
				fmt.Sscanf(os.Args[2:][i+1], "%f", &threshold)
			}
		}
	}

	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	store, err := storage.Open(filepath.Join(dataDir, "yesmem.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	provider, err := embedding.NewProviderFromConfig(cfg.Embedding)
	if err != nil {
		log.Fatalf("embedding provider: %v", err)
	}
	defer provider.Close()

	if !provider.Enabled() {
		log.Fatal("Embedding provider disabled — set embedding.provider to 'static' in config.yaml")
	}

	traits, err := store.GetActivePersonaTraits("default", 0.0)
	if err != nil {
		log.Fatalf("get traits: %v", err)
	}

	if len(traits) == 0 {
		fmt.Fprintln(os.Stderr, "No active traits found.")
		return
	}

	fmt.Fprintf(os.Stderr, "Embedding %d traits (threshold: %.2f)...\n", len(traits), threshold)

	// Build text representations for embedding
	texts := make([]string, len(traits))
	for i, t := range traits {
		texts[i] = fmt.Sprintf("%s %s %s", t.Dimension, t.TraitKey, t.TraitValue)
	}

	// Embed in one batch
	vectors, err := provider.Embed(context.Background(), texts)
	if err != nil {
		log.Fatalf("embed: %v", err)
	}

	// Find clusters via pairwise cosine similarity
	type pair struct {
		i, j int
		sim  float64
	}
	var similar []pair
	for i := 0; i < len(vectors); i++ {
		for j := i + 1; j < len(vectors); j++ {
			// Only compare within same dimension
			if traits[i].Dimension != traits[j].Dimension {
				continue
			}
			sim := clustering.CosineSimilarity(vectors[i], vectors[j])
			if sim >= threshold {
				similar = append(similar, pair{i, j, sim})
			}
		}
	}

	if len(similar) == 0 {
		fmt.Fprintln(os.Stderr, "No similar trait pairs found. Data is clean.")
		return
	}

	// Sort by similarity descending
	sort.Slice(similar, func(a, b int) bool { return similar[a].sim > similar[b].sim })

	fmt.Fprintf(os.Stderr, "\nFound %d similar pairs:\n\n", len(similar))

	// Track which traits to supersede (keep highest confidence per cluster)
	superseded := make(map[int]bool)
	var toSupersede []models.PersonaTrait

	for _, p := range similar {
		if superseded[p.i] || superseded[p.j] {
			continue // Already handled
		}

		ti, tj := traits[p.i], traits[p.j]
		keep, drop := ti, tj
		keepIdx, dropIdx := p.i, p.j
		if tj.Confidence > ti.Confidence || (tj.Confidence == ti.Confidence && tj.EvidenceCount > ti.EvidenceCount) {
			keep, drop = tj, ti
			keepIdx, dropIdx = p.j, p.i
		}

		fmt.Fprintf(os.Stderr, "  [%.2f] KEEP: %s.%s=%q (conf:%.2f, ev:%d)\n",
			p.sim, keep.Dimension, keep.TraitKey, keep.TraitValue, keep.Confidence, keep.EvidenceCount)
		fmt.Fprintf(os.Stderr, "         DROP: %s.%s=%q (conf:%.2f, ev:%d)\n",
			drop.Dimension, drop.TraitKey, drop.TraitValue, drop.Confidence, drop.EvidenceCount)

		superseded[dropIdx] = true
		_ = keepIdx
		toSupersede = append(toSupersede, drop)
	}

	fmt.Fprintf(os.Stderr, "\n%d traits to supersede.\n", len(toSupersede))

	if dryRun {
		fmt.Fprintln(os.Stderr, "--dry-run: no changes made.")
		return
	}

	for _, t := range toSupersede {
		store.SupersedePersonaTrait("default", t.Dimension, t.TraitKey)
	}
	fmt.Fprintf(os.Stderr, "Done: %d traits superseded. %d remaining.\n", len(toSupersede), len(traits)-len(toSupersede))
}
