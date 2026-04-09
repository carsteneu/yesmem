package embedding

import (
	"context"
	"fmt"
	"log"
)

// IndexItem represents a single item to index.
type IndexItem struct {
	ID       string
	Content  string
	Metadata map[string]string
}

// Indexer connects an embedding Provider with a VectorStore.
type Indexer struct {
	provider Provider
	store    *VectorStore
}

// NewIndexer creates a new indexer with the given provider and store.
func NewIndexer(provider Provider, store *VectorStore) *Indexer {
	return &Indexer{
		provider: provider,
		store:    store,
	}
}

// Index embeds a single text and stores it in the vector store.
// If the provider is disabled, this is a no-op.
func (idx *Indexer) Index(ctx context.Context, id, content string, metadata map[string]string) error {
	if !idx.provider.Enabled() {
		return nil
	}

	vectors, err := idx.provider.Embed(ctx, []string{content})
	if err != nil {
		return fmt.Errorf("embed %s: %w", id, err)
	}

	// Delete existing doc first (upsert)
	if err := idx.store.Delete(ctx, id); err != nil {
		log.Printf("indexer: delete before upsert for %s: %v", id, err)
	}

	return idx.store.Add(ctx, VectorDoc{
		ID:        id,
		Content:   content,
		Embedding: vectors[0],
		Metadata:  metadata,
	})
}

// IndexBatch embeds and stores multiple items.
func (idx *Indexer) IndexBatch(ctx context.Context, items []IndexItem) error {
	if !idx.provider.Enabled() {
		return nil
	}
	if len(items) == 0 {
		return nil
	}

	// Collect all texts for batch embedding
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = item.Content
	}

	vectors, err := idx.provider.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("batch embed: %w", err)
	}

	docs := make([]VectorDoc, len(items))
	for i, item := range items {
		docs[i] = VectorDoc{
			ID:        item.ID,
			Content:   item.Content,
			Embedding: vectors[i],
			Metadata:  item.Metadata,
		}
	}

	return idx.store.AddBatch(ctx, docs)
}
