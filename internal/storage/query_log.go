package storage

import (
	"encoding/binary"
	"log"
	"math"
	"strings"
)

// InsertQueryLog persists a query vector and the IDs of learnings returned
// by hybrid_search. This is passive data collection for future cluster-scoring.
func (s *Store) InsertQueryLog(project, queryText string, queryVec []float32, injectedIDs []string) {
	runes := []rune(queryText)
	if len(runes) > 500 {
		queryText = string(runes[:500])
	}
	vecBlob := float32ToBlob(queryVec)
	ids := strings.Join(injectedIDs, ",")

	_, err := s.db.Exec(`INSERT INTO query_log (project, query_text, query_vector, injected_learning_ids) VALUES (?, ?, ?, ?)`,
		project, queryText, vecBlob, ids)
	if err != nil {
		log.Printf("query_log: insert failed: %v", err)
	}
}

// float32ToBlob converts a float32 slice to a little-endian byte slice.
func float32ToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}
