package daemon

import (
	"log"
	"strconv"
	"strings"

	"github.com/carsteneu/yesmem/internal/clustering"
	"github.com/carsteneu/yesmem/internal/storage"
)

// RunQueryClustering clusters unclustered query_log entries and updates cluster scores.
// Called periodically by the daemon timer (every 30 minutes).
func RunQueryClustering(store *storage.Store) {
	unclustered, err := store.GetUnclusteredQueries(100)
	if err != nil {
		log.Printf("query-clustering: get unclustered: %v", err)
		return
	}
	if len(unclustered) < 5 {
		return // not enough for meaningful clustering
	}

	existing, err := store.GetQueryClusters("")
	if err != nil {
		log.Printf("query-clustering: get clusters: %v", err)
		return
	}

	assigned := 0
	var unmatched []storage.QueryLogEntry
	changedClusterIDs := map[int64]bool{}

	// Phase 1: Assign to existing clusters (cosine > 0.80)
	for _, q := range unclustered {
		if len(q.QueryVector) == 0 {
			continue
		}
		bestID := storage.FindNearestCluster(q.QueryVector, existing, 0.80)
		if bestID > 0 {
			store.AssignQueryToCluster(q.ID, bestID)
			for i := range existing {
				if existing[i].ID == bestID {
					existing[i].QueryCount++
					changedClusterIDs[bestID] = true
					break
				}
			}
			assigned++
			updateClusterScoresFromEntry(store, q, bestID)
		} else {
			unmatched = append(unmatched, q)
		}
	}

	// Phase 2: Cluster unmatched queries among themselves (cosine 0.80)
	newClusters := 0
	if len(unmatched) >= 3 {
		docs := make([]clustering.Document, 0, len(unmatched))
		for _, q := range unmatched {
			if len(q.QueryVector) == 0 {
				continue
			}
			docs = append(docs, clustering.Document{
				ID:        strconv.FormatInt(q.ID, 10),
				Content:   q.QueryText,
				Embedding: q.QueryVector,
				Metadata:  map[string]any{"project": q.Project},
			})
		}

		clusters := clustering.AgglomerativeClustering(docs, 0.80)
		clusters = clustering.FilterByMinSize(clusters, 2)

		for _, c := range clusters {
			project := ""
			if len(c.Documents) > 0 {
				if p, ok := c.Documents[0].Metadata["project"].(string); ok {
					project = p
				}
			}

			clusterID, err := store.SaveQueryCluster(storage.QueryCluster{
				Project:        project,
				CentroidVector: c.Centroid,
				Label:          labelFromDocs(c.Documents),
				QueryCount:     len(c.Documents),
			})
			if err != nil {
				log.Printf("query-clustering: save cluster: %v", err)
				continue
			}

			for _, d := range c.Documents {
				qid, _ := strconv.ParseInt(d.ID, 10, 64)
				store.AssignQueryToCluster(qid, clusterID)
				for _, q := range unmatched {
					if q.ID == qid {
						updateClusterScoresFromEntry(store, q, clusterID)
						break
					}
				}
			}
			newClusters++
		}
	}

	// Only update clusters whose query_count actually changed
	for _, c := range existing {
		if changedClusterIDs[c.ID] {
			store.SaveQueryCluster(c)
		}
	}

	// Purge old clustered query logs (retain 30 days)
	purged, _ := store.PurgeOldQueryLogs(30)

	log.Printf("query-clustering: %d assigned to existing, %d new clusters, %d purged (of %d unclustered)",
		assigned, newClusters, purged, len(unclustered))
}

// updateClusterScoresFromEntry uses the pre-loaded InjectedLearningIDs field (no extra DB query).
func updateClusterScoresFromEntry(store *storage.Store, q storage.QueryLogEntry, clusterID int64) {
	if q.InjectedLearningIDs == "" {
		return
	}
	for _, idStr := range strings.Split(q.InjectedLearningIDs, ",") {
		idStr = strings.TrimSpace(idStr)
		lid, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || lid <= 0 {
			continue
		}
		store.IncrementClusterScore(lid, clusterID, "inject")
	}
}

// labelFromDocs creates a simple label from the first query text.
func labelFromDocs(docs []clustering.Document) string {
	if len(docs) == 0 {
		return ""
	}
	label := docs[0].Content
	runes := []rune(label)
	if len(runes) > 60 {
		label = string(runes[:60])
	}
	return label
}
