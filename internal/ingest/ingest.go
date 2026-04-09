package ingest

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Config holds the ingest pipeline configuration.
type Config struct {
	Project           string
	Domain            string // code, marketing, legal, finance, general
	DryRun            bool
	Destill           bool                 // run LLM destillation (Phase 6)
	LLMClient         extraction.LLMClient // required when Destill=true, can be nil otherwise
	Name              string               // doc source name
	Version           string               // doc source version
	TriggerExtensions string               // comma-separated, e.g. ".go,.mod" — auto-inject docs for these file types
	DocType           string               // "reference" (auto-inject) or "style" (explicit search only)
}

// Result holds the outcome of an ingest run.
type Result struct {
	FilesProcessed      int
	FilesSkipped        int // unchanged hash
	ChunksCreated       int
	ChunksUpdated       int
	LearningsSuperseded int
	LearningsCreated    int // from destillation
}

// Run executes the ingest pipeline for given paths.
func Run(cfg Config, paths []string, store *storage.Store) (*Result, error) {
	result := &Result{}
	var changedChunks []storage.DocChunk

	// 1. Collect all files
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if info.IsDir() {
			if err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".md" || ext == ".txt" || ext == ".rst" || ext == ".pdf" {
					files = append(files, path)
				}
				return nil
			}); err != nil {
				return nil, fmt.Errorf("walk %s: %w", p, err)
			}
		} else {
			files = append(files, p)
		}
	}

	if len(files) == 0 {
		log.Printf("No indexable files found in %v", paths)
		return result, nil
	}

	// 2. Upsert doc source
	ds := &storage.DocSource{
		Name:              cfg.Name,
		Version:           cfg.Version,
		Path:              paths[0],
		Project:           cfg.Project,
		TriggerExtensions: cfg.TriggerExtensions,
		DocType:           cfg.DocType,
	}

	sourceID, err := store.UpsertDocSource(ds)
	if err != nil {
		return nil, fmt.Errorf("upsert doc source: %w", err)
	}

	// 3. Process each file
	for i, file := range files {
		log.Printf("[%d/%d] Processing %s", i+1, len(files), file)

		processed, deltaChunks, err := processFile(cfg, file, paths[0], sourceID, store, result)
		if err != nil {
			log.Printf("  ERROR: %v", err)
			continue
		}
		if !processed {
			result.FilesSkipped++
			continue
		}
		result.FilesProcessed++
		changedChunks = append(changedChunks, deltaChunks...)
	}

	// 4. Update source stats
	if !cfg.DryRun {
		store.UpdateDocSourceStats(sourceID)
	}

	// 5. Destillation: extract learnings from chunks via LLM
	if cfg.Destill && !cfg.DryRun {
		if cfg.LLMClient == nil {
			log.Printf("warn: --destill requested but no LLM client configured, skipping")
		} else {
			if len(changedChunks) > 0 {
				log.Printf("Destilling %d changed chunks via %s...", len(changedChunks), cfg.LLMClient.Model())
				created, err := DestillChunks(changedChunks, cfg, cfg.LLMClient, store)
				if err != nil {
					log.Printf("warn: destillation: %v", err)
				}
				result.LearningsCreated = created
				log.Printf("Destillation complete: %d learnings created", created)
			}
		}
	}

	return result, nil
}

func processFile(cfg Config, path, basePath string, sourceID int64, store *storage.Store, result *Result) (bool, []storage.DocChunk, error) {
	// Compute relative path for storage (portable across machines)
	relPath, err := filepath.Rel(basePath, path)
	if err != nil {
		relPath = path // fallback to absolute if Rel fails
	}

	// Read file content (handle .pdf via pdftotext)
	content, err := readFile(path)
	if err != nil {
		return false, nil, err
	}

	// SHA256 hash
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

	// Check existing chunks for this file
	existing, _ := store.GetDocChunksByFile(relPath)
	if len(existing) > 0 && existing[0].SourceHash == hash {
		log.Printf("  Unchanged (hash match), skipping")
		return false, nil, nil
	}

	if cfg.DryRun {
		chunks := chunkByFormat(path, content)
		log.Printf("  DRY RUN: would create %d chunks", len(chunks))
		return true, nil, nil
	}

	newChunks := chunkByFormat(path, content)
	existingByHash := make(map[string][]storage.DocChunk, len(existing))
	for _, chunk := range existing {
		existingByHash[chunk.ContentHash] = append(existingByHash[chunk.ContentHash], chunk)
	}

	var changedChunks []storage.DocChunk
	var removedChunkIDs []int64
	var retainedExistingIDs = make(map[int64]bool, len(existing))

	for _, c := range newChunks {
		if matches := existingByHash[c.ContentHash]; len(matches) > 0 {
			match := matches[0]
			existingByHash[c.ContentHash] = matches[1:]
			retainedExistingIDs[match.ID] = true
			continue
		}
		dc := &storage.DocChunk{
			SourceID:     sourceID,
			SourceFile:   relPath,
			SourceHash:   hash,
			HeadingPath:  c.HeadingPath,
			SectionLevel: c.SectionLevel,
			Content:      c.Content,
			ContentHash:  c.ContentHash,
			TokensApprox: c.TokensApprox,
			Metadata:     c.Metadata,
		}


		id, err := store.InsertDocChunk(dc)
		if err != nil {
			log.Printf("  ERROR inserting chunk: %v", err)
			continue
		}
		dc.ID = id
		result.ChunksCreated++
		result.ChunksUpdated++
		changedChunks = append(changedChunks, *dc)
	}

	for _, chunk := range existing {
		if retainedExistingIDs[chunk.ID] {
			continue
		}
		removedChunkIDs = append(removedChunkIDs, chunk.ID)
	}

	if len(removedChunkIDs) > 0 {
		ids, _ := store.SupersedeByDocChunkRefs(removedChunkIDs, "re-ingest: chunk changed")
		result.LearningsSuperseded += len(ids)
		if err := store.DeleteDocChunksByIDs(removedChunkIDs); err != nil {
			log.Printf("  ERROR deleting old chunks: %v", err)
		}
	}

	if err := store.UpdateDocChunksSourceHashByFile(relPath, hash); err != nil {
		log.Printf("  ERROR updating source hash on retained chunks: %v", err)
	}

	log.Printf("  Incremental sync: %d new/changed chunks, %d unchanged, %d removed",
		len(changedChunks), len(retainedExistingIDs), len(removedChunkIDs))
	return true, changedChunks, nil
}

// readFile reads a file, using pdftotext for PDFs.
func readFile(path string) (string, error) {
	if strings.HasSuffix(strings.ToLower(path), ".pdf") {
		out, err := exec.Command("pdftotext", "-layout", path, "-").Output()
		if err != nil {
			return "", fmt.Errorf("pdftotext failed (is poppler-utils installed?): %w", err)
		}
		return string(out), nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// chunkByFormat selects the appropriate chunker based on file extension.
func chunkByFormat(relPath, content string) []Chunk {
	if strings.HasSuffix(strings.ToLower(relPath), ".rst") {
		return ChunkRST(content)
	}
	return ChunkMarkdown(content)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
