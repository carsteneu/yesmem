package extraction

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func mustOpenStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func insertTestLearning(store *storage.Store, content, category string) int64 {
	id, err := store.InsertLearning(&models.Learning{
		SessionID: "test-session", Category: category, Content: content,
		Project: "testproj", Confidence: 1.0, CreatedAt: time.Now(),
		ModelUsed: "test", Source: "llm_extracted",
	})
	if err != nil {
		panic(err)
	}
	return id
}
