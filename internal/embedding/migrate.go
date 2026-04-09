package embedding

import (
	"context"
	"fmt"
	"log"
	"time"
)

// MigrateStats reports migration progress.
type MigrateStats struct {
	Total       int
	Embedded    int
	Skipped     int
	Errors      int
	EmbeddedIDs []string
}

// MigrateEmbeddings bulk-embeds items using the provider.
// If store is non-nil, vectors are written to the VectorStore (legacy mode).
// If store is nil, vectors are only returned via onBatchDone callback (lightweight mode).
// Items are assumed to be pre-filtered (only pending). batchSize controls batch size.
// throttle adds a pause between batches to reduce CPU pressure.
// onBatchDone is called after each successful batch with the embedded item IDs and vectors.
func MigrateEmbeddings(ctx context.Context, provider Provider, store *VectorStore, items []IndexItem, batchSize int, force bool, throttle time.Duration, onBatchDone ...func(ids []string, vectors [][]float32)) (MigrateStats, error) {
	if !provider.Enabled() {
		return MigrateStats{}, fmt.Errorf("embedding provider is disabled")
	}

	forceAll := force
	stats := MigrateStats{Total: len(items)}

	// Filter items that need embedding (only when VectorStore is available for Has() check)
	var toEmbed []IndexItem
	for _, item := range items {
		if store != nil && !forceAll && store.Has(ctx, item.ID) {
			stats.Skipped++
			continue
		}
		toEmbed = append(toEmbed, item)
	}

	if len(toEmbed) == 0 {
		log.Printf("Nothing to embed (%d skipped, already in store)", stats.Skipped)
		return stats, nil
	}

	log.Printf("Embedding %d items (%d skipped) in batches of %d...", len(toEmbed), stats.Skipped, batchSize)
	totalBatches := (len(toEmbed) + batchSize - 1) / batchSize
	startTime := time.Now()

	// Process in batches
	batchNum := 0
	for i := 0; i < len(toEmbed); i += batchSize {
		end := i + batchSize
		if end > len(toEmbed) {
			end = len(toEmbed)
		}
		batch := toEmbed[i:end]
		batchNum++
		var batchIDs []string
		var batchVectors [][]float32

		batchStart := time.Now()
		texts := make([]string, len(batch))
		for j, item := range batch {
			texts[j] = item.Content
		}

		vectors, err := provider.Embed(ctx, texts)
		if err != nil {
			stats.Errors += len(batch)
			log.Printf("  [%d/%d] FAIL: %v", batchNum, totalBatches, err)
			continue
		}

		for j, item := range batch {
			// Write to VectorStore if available (legacy mode)
			if store != nil {
				if forceAll {
					store.Delete(ctx, item.ID)
				}
				err := store.Add(ctx, VectorDoc{
					ID:        item.ID,
					Content:   item.Content,
					Embedding: vectors[j],
					Metadata:  item.Metadata,
				})
				if err != nil {
					stats.Errors++
					log.Printf("  store doc %s: %v", item.ID, err)
					continue
				}
			}
			stats.Embedded++
			stats.EmbeddedIDs = append(stats.EmbeddedIDs, item.ID)
			batchIDs = append(batchIDs, item.ID)
			batchVectors = append(batchVectors, vectors[j])
		}

		elapsed := time.Since(startTime)
		avgPerBatch := elapsed / time.Duration(batchNum)
		remaining := avgPerBatch * time.Duration(totalBatches-batchNum)
		log.Printf("  [%d/%d] %d embedded (%.1fs/batch, ETA %v)",
			batchNum, totalBatches, stats.Embedded,
			time.Since(batchStart).Seconds(), remaining.Round(time.Second))

		// Notify caller of completed batch (for DB status updates + vector storage)
		if len(onBatchDone) > 0 && onBatchDone[0] != nil && len(batchIDs) > 0 {
			onBatchDone[0](batchIDs, batchVectors)
		}

		// Throttle: yield CPU between batches so search stays responsive
		if throttle > 0 && batchNum < totalBatches {
			time.Sleep(throttle)
		}
	}

	log.Printf("Done. %d embedded, %d skipped, %d errors in %v",
		stats.Embedded, stats.Skipped, stats.Errors, time.Since(startTime).Round(time.Second))

	return stats, nil
}
