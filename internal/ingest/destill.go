package ingest

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// docIngestResult holds the parsed LLM response for doc ingest extraction.
type docIngestResult struct {
	Domain    string          `json:"domain"`
	Learnings []docIngestItem `json:"learnings"`
}

// docIngestItem represents a single extracted learning from documentation.
type docIngestItem struct {
	Category string   `json:"category"`
	Content  string   `json:"content"`
	Context  string   `json:"context"`
	Entities []string `json:"entities"`
	Actions  []string `json:"actions"`
	Keywords []string `json:"keywords"`
	Trigger  string   `json:"trigger"`
}

// maxDocChars is the threshold above which we fall back to chunk-by-chunk processing.
const maxDocChars = 100_000

// DestillChunks extracts low-trust learnings from doc chunks using a two-pass LLM approach.
// Pass 1: Filter — extract only specialist knowledge from the full document.
// Pass 2: Structure — group filtered knowledge into cohesive learnings.
// For very large documents (>100k chars), falls back to chunk-by-chunk processing.
func DestillChunks(chunks []storage.DocChunk, cfg Config, client extraction.LLMClient, store *storage.Store) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}

	// Load existing learnings for this project for dedup
	existingLearnings, _ := store.GetActiveLearnings("", cfg.Project, "", "")
	existingTokens := make([][]string, len(existingLearnings))
	for i, l := range existingLearnings {
		existingTokens[i] = textutil.Tokenize(l.Content)
	}

	// Build full document from all chunks
	fullDoc := buildFullDoc(chunks)

	// If document is too large, fall back to chunk-by-chunk processing
	if len(fullDoc) > maxDocChars {
		log.Printf("  Document too large (%d chars > %d limit), falling back to chunk-by-chunk", len(fullDoc), maxDocChars)
		return destillChunkByChunk(chunks, cfg, client, store, existingTokens)
	}

	if len(fullDoc) < 100 {
		return 0, nil
	}

	// --- Pass 1: Filter — extract specialist knowledge ---
	log.Printf("  Pass 1: Filtering specialist knowledge from %d chars...", len(fullDoc))

	specialistText, err := runPass1Filter(client, fullDoc)
	if err != nil {
		return 0, fmt.Errorf("pass 1 filter: %w", err)
	}

	if len(specialistText) < 50 {
		log.Printf("  Pass 1: No specialist knowledge found, skipping Pass 2")
		return 0, nil
	}

	// Check for PROCESS_SKILL detection (may be embedded in JSON or surrounded by text)
	if strings.Contains(strings.ToUpper(specialistText), "PROCESS_SKILL") {
		log.Printf("  Pass 1: Process skill detected — skipping destillation")
		return 0, nil
	}

	log.Printf("  Pass 1: Found specialist knowledge (%d chars)", len(specialistText))

	// --- Pass 2: Structure — group into learnings ---
	log.Printf("  Pass 2: Structuring into learnings...")

	result, err := runPass2Structure(client, specialistText)
	if err != nil {
		return 0, fmt.Errorf("pass 2 structure: %w", err)
	}

	// --- Insert with dedup ---
	created := insertLearnings(result, chunks, cfg, client, store, existingTokens)

	return created, nil
}

// buildFullDoc assembles the complete document text from chunks, skipping
// frontmatter and tiny chunks.
func buildFullDoc(chunks []storage.DocChunk) string {
	var fullDoc strings.Builder
	for _, chunk := range chunks {
		content := strings.TrimSpace(chunk.Content)
		// Skip frontmatter and tiny chunks
		if strings.HasPrefix(content, "---") || chunk.TokensApprox < 100 {
			continue
		}
		if chunk.HeadingPath != "" {
			fullDoc.WriteString("\n## " + chunk.HeadingPath + "\n\n")
		}
		fullDoc.WriteString(content + "\n")
	}
	return fullDoc.String()
}

// runPass1Filter calls the LLM to extract specialist knowledge from the full document.
func runPass1Filter(client extraction.LLMClient, docText string) (string, error) {
	// Use CompleteJSON with a simple wrapper schema so it works with all client types
	response, err := client.CompleteJSON(
		extraction.DocFilterSystemPrompt,
		docText,
		extraction.DocFilterSchema(),
		extraction.WithMaxTokens(4096),
	)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(response) == "" {
		return "", nil
	}

	// Parse the JSON wrapper to extract the text
	var wrapper struct {
		SpecialistPoints string `json:"specialist_points"`
	}
	if err := json.Unmarshal([]byte(response), &wrapper); err != nil {
		// If JSON parsing fails, treat the raw response as text
		return response, nil
	}

	return strings.TrimSpace(wrapper.SpecialistPoints), nil
}

// runPass2Structure calls the LLM to structure specialist knowledge into learnings.
func runPass2Structure(client extraction.LLMClient, specialistText string) (*docIngestResult, error) {
	response, err := client.CompleteJSON(
		extraction.DocIngestSystemPrompt,
		specialistText,
		extraction.DocIngestSchema(),
	)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(response) == "" {
		return &docIngestResult{}, nil
	}

	var result docIngestResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("parse pass 2 response: %w", err)
	}

	return &result, nil
}

// insertLearnings deduplicates and inserts learnings from a destillation result.
// Returns the number of newly created learnings.
func insertLearnings(result *docIngestResult, chunks []storage.DocChunk, cfg Config, client extraction.LLMClient, store *storage.Store, existingTokens [][]string) int {
	if result == nil || len(result.Learnings) == 0 {
		return 0
	}

	domain := result.Domain
	if domain == "" {
		domain = cfg.Domain
	}
	if domain == "" {
		domain = "code"
	}

	// Use the first chunk's source info for attribution
	var sourceFile, sourceHash string
	var docChunkRef int64
	if len(chunks) > 0 {
		sourceFile = chunks[0].SourceFile
		sourceHash = chunks[0].SourceHash
		docChunkRef = chunks[0].ID
	}

	created := 0
	for _, item := range result.Learnings {
		content := strings.TrimSpace(item.Content)
		if len([]rune(content)) < 10 {
			continue
		}

		// Two-stage dedup: token similarity against existing + intra-run learnings
		candidateTokens := textutil.Tokenize(content)
		isDuplicate := false
		for _, existTok := range existingTokens {
			if textutil.TokenSimilarity(candidateTokens, existTok) >= 0.3 {
				isDuplicate = true
				break
			}
		}
		if isDuplicate {
			continue
		}

		// Build and insert learning
		now := time.Now()
		l := &models.Learning{
			Category:    item.Category,
			Content:     content,
			Context:     item.Context,
			Domain:      domain,
			TriggerRule: item.Trigger,
			Entities:    item.Entities,
			Actions:     item.Actions,
			Keywords:    item.Keywords,
			Project:     cfg.Project,
			Confidence:  0.5, // low trust
			CreatedAt:   now,
			ModelUsed:   client.Model(),
			Source:      "docs_extracted",
			SourceFile:  sourceFile,
			SourceHash:  sourceHash,
			DocChunkRef: docChunkRef,
			Importance:  2, // low trust — not yet validated
		}
		l.EmbeddingText = l.BuildEmbeddingText()

		_, err := store.InsertLearning(l)
		if err != nil {
			log.Printf("  warn: insert destilled learning: %v", err)
			continue
		}
		created++

		// Add to existing tokens for intra-run dedup
		existingTokens = append(existingTokens, candidateTokens)
	}

	log.Printf("  Two-pass result: %d learnings created from %d extracted", created, len(result.Learnings))
	return created
}

// destillChunkByChunk is the fallback for very large documents.
// It processes chunks individually using the same two-pass approach per chunk.
func destillChunkByChunk(chunks []storage.DocChunk, cfg Config, client extraction.LLMClient, store *storage.Store, existingTokens [][]string) (int, error) {
	created := 0
	for i, chunk := range chunks {
		// Rate limit: pause between chunks
		if i > 0 {
			time.Sleep(2 * time.Second)
		}

		// Skip very small chunks and frontmatter
		content := strings.TrimSpace(chunk.Content)
		if chunk.TokensApprox < 100 || strings.HasPrefix(content, "---") {
			continue
		}

		// Build chunk text with heading context
		var chunkDoc strings.Builder
		if chunk.HeadingPath != "" {
			chunkDoc.WriteString("## " + chunk.HeadingPath + "\n\n")
		}
		chunkDoc.WriteString(content)
		chunkText := chunkDoc.String()

		// Pass 1: Filter
		specialistText, err := runPass1Filter(client, chunkText)
		if err != nil {
			log.Printf("  warn: pass 1 chunk %d (heading=%q): %v", chunk.ID, chunk.HeadingPath, err)
			if strings.Contains(err.Error(), "rate_limit") {
				log.Printf("  Rate limited — waiting 60s")
				time.Sleep(60 * time.Second)
			}
			continue
		}

		if len(specialistText) < 50 {
			continue
		}

		// Pass 2: Structure
		result, err := runPass2Structure(client, specialistText)
		if err != nil {
			log.Printf("  warn: pass 2 chunk %d (heading=%q): %v", chunk.ID, chunk.HeadingPath, err)
			continue
		}

		// Insert with dedup — use single-chunk slice for attribution
		singleChunk := []storage.DocChunk{chunk}
		chunkCreated := insertLearnings(result, singleChunk, cfg, client, store, existingTokens)
		created += chunkCreated

		log.Printf("  Chunk %d (%s): %d learnings",
			chunk.ID, truncateHeading(chunk.HeadingPath, 40), chunkCreated)
	}

	return created, nil
}

// truncateHeading shortens a heading path for log output.
func truncateHeading(heading string, maxLen int) string {
	if len(heading) <= maxLen {
		return heading
	}
	return heading[:maxLen-3] + "..."
}
