package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/clustering"
	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// clusterLearnings groups project learnings by embedding similarity and labels them via LLM.
// Runs as Phase 4.5 between Narratives and Profiles.
// Embeddings are read from the VectorStore — no on-the-fly computation.
// If vecStore is nil or empty, clustering is skipped.
// Uses ProjectChangeFingerprint for change detection — skips projects with unchanged learnings.
func ClusterLearnings(store *storage.Store, client extraction.LLMClient, vecStore *embedding.VectorStore) {
	if vecStore == nil || vecStore.Count() == 0 {
		log.Printf("  Clustering: skipped (no embeddings in vector store)")
		return
	}

	projects, err := store.ListProjects()
	if err != nil {
		log.Printf("warn: list projects for clustering: %v", err)
		return
	}

	skipped := 0
	clustered := 0
	for _, p := range projects {
		if p.SessionCount < 3 {
			continue
		}
		// Change detection: compare DB fingerprint with stored cluster hash
		fingerprint := store.ProjectChangeFingerprint(p.ProjectShort)
		storedHash, _ := store.GetClusterHash(p.ProjectShort)
		if storedHash == fingerprint {
			skipped++
			continue
		}
		if err := clusterProjectLearnings(store, client, vecStore, p.ProjectShort); err != nil {
			log.Printf("warn: cluster %s: %v", p.ProjectShort, err)
			continue
		}
		store.SetClusterHash(p.ProjectShort, fingerprint)
		clustered++
	}
	if skipped > 0 {
		log.Printf("  Clustering: %d projects clustered, %d skipped (unchanged)", clustered, skipped)
	}
}

func clusterProjectLearnings(store *storage.Store, client extraction.LLMClient, vecStore *embedding.VectorStore, project string) error {
	// Get all active learnings for project (excluding narrative, unfinished)
	learnings, err := store.GetActiveLearnings("", project, "", "")
	if err != nil || len(learnings) < 5 {
		return nil // Not enough to cluster
	}

	// Filter to clusterworthy categories
	var clusterable []models.Learning
	for _, l := range learnings {
		switch l.Category {
		case "narrative", "unfinished", "strategic":
			continue
		}
		clusterable = append(clusterable, l)
	}

	if len(clusterable) < 5 {
		return nil
	}

	// Cap at 300 to avoid O(n³) freeze with large projects
	const maxClusterable = 300
	if len(clusterable) > maxClusterable {
		// Sort by relevance: hit_count desc, then recency desc
		sort.Slice(clusterable, func(i, j int) bool {
			hi, hj := clusterable[i].HitCount, clusterable[j].HitCount
			if hi != hj {
				return hi > hj
			}
			return clusterable[i].CreatedAt.After(clusterable[j].CreatedAt)
		})
		log.Printf("  Clustering: capping %d → %d learnings for %s", len(clusterable), maxClusterable, project)
		clusterable = clusterable[:maxClusterable]
	}

	// Build documents using pre-computed embeddings from VectorStore
	ctx := context.Background()
	docs := make([]clustering.Document, 0, len(clusterable))
	skipped := 0

	for _, l := range clusterable {
		id := strconv.FormatInt(l.ID, 10)
		emb := vecStore.GetEmbedding(ctx, id)
		if emb == nil {
			skipped++
			continue // Not yet in vector store — will be included next run
		}
		docs = append(docs, clustering.Document{
			ID:      id,
			Content: l.Content,
			Embedding: emb,
			Metadata: map[string]any{
				"category":   l.Category,
				"hit_count":  l.HitCount,
				"created_at": l.CreatedAt,
			},
		})
	}

	if skipped > 0 {
		log.Printf("  Clustering %s: %d with embeddings, %d skipped (not yet indexed)", project, len(docs), skipped)
	}

	if len(docs) < 5 {
		return nil // Not enough with embeddings
	}

	// Cluster with 0.85 threshold, min 2 docs per cluster
	clusters := clustering.AgglomerativeClustering(docs, 0.85)
	clusters = clustering.FilterByMinSize(clusters, 2)

	if len(clusters) == 0 {
		return nil
	}

	// Label clusters via LLM (or simple heuristic if no client)
	var dbClusters []models.LearningCluster
	for i, c := range clusters {
		if i > 0 && client != nil {
			time.Sleep(500 * time.Millisecond) // rate-limit LLM calls
		}
		label := labelCluster(c, client)
		ids := make([]int64, 0, len(c.Documents))
		var totalRecency, totalHits float64
		for _, d := range c.Documents {
			id, _ := strconv.ParseInt(d.ID, 10, 64)
			ids = append(ids, id)
			if created, ok := d.Metadata["created_at"].(time.Time); ok {
				totalRecency += time.Since(created).Hours() / 24
			}
			if hc, ok := d.Metadata["hit_count"].(int); ok {
				totalHits += float64(hc)
			}
		}
		n := float64(len(c.Documents))
		idsJSON, _ := json.Marshal(ids)

		// Confidence: weighted formula — count 30%, recency 40%, hits 30%
		avgRecency := totalRecency / n
		avgHits := totalHits / n
		countScore := math.Min(n/10.0, 1.0)                                         // saturates at 10 learnings
		recencyScore := math.Exp(-avgRecency / 60.0)                                 // half-life ~42 days
		hitScore := math.Min(avgHits/5.0, 1.0)                                       // saturates at 5 hits
		confidence := countScore*0.3 + recencyScore*0.4 + hitScore*0.3
		if confidence > 1.0 {
			confidence = 1.0
		}
		if confidence < 0.05 {
			confidence = 0.05
		}

		dbClusters = append(dbClusters, models.LearningCluster{
			Project:        project,
			Label:          label,
			LearningCount:  len(c.Documents),
			AvgRecencyDays: avgRecency,
			AvgHitCount:    avgHits,
			Confidence:     confidence,
			LearningIDs:    string(idsJSON),
		})
	}

	return store.ReplaceLearningClusters(project, dbClusters)
}

// labelCluster generates a short German label for a cluster.
// Uses LLM if available, otherwise falls back to first content snippet.
func labelCluster(c clustering.Cluster, client extraction.LLMClient) string {
	if client == nil || len(c.Documents) == 0 {
		if len(c.Documents) > 0 {
			content := c.Documents[0].Content
			if len(content) > 50 {
				content = content[:50]
			}
			return content
		}
		return "Unnamed cluster"
	}

	// Build content list for LLM
	var contents []string
	for _, d := range c.Documents {
		s := d.Content
		if len(s) > 200 {
			s = s[:200]
		}
		contents = append(contents, "- "+s)
	}

	prompt := "You are a knowledge organizer. Give this cluster of insights a short English label (3-6 words). Only the label, no explanation."
	userMsg := fmt.Sprintf("Insights:\n%s", strings.Join(contents, "\n"))

	label, err := client.Complete(prompt, userMsg)
	if err != nil {
		return c.Documents[0].Content[:min(50, len(c.Documents[0].Content))]
	}
	// Trim quotes and whitespace
	label = trimLabel(label)
	return label
}

func trimLabel(s string) string {
	// Remove surrounding quotes
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}
